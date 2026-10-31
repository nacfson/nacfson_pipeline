#!/usr/bin/env python3
"""Request checks, run markers, dedupe and supersede for the image pin flow.

Run by `.github/workflows/image-pin.yaml`. This script owns every decision the
workflow makes about *whether* to act; the image resolution and the YAML edits
stay in the `update-project-images` helper.

Design notes that matter:

* Request identity is never derived from `run-name`. A workflow's display name
  is composed from dispatch inputs, so the requester authors it. Dedupe and
  supersede read a marker that each `validate` run writes about itself, and
  only when that run concluded successfully. See the design spec §7, §12.
* "Live" means queued, waiting or in_progress. A run is `queued` from dispatch
  until its validate job finishes, so two dispatches seconds apart both see no
  `waiting` run. And approval moves a run out of `waiting` into `in_progress`,
  so a newer approved request must still count as newer.
"""

from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
from dataclasses import dataclass, field
from datetime import datetime, timezone
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
SOURCES_PATH = REPO_ROOT / "config" / "image-pin-sources.json"
SHA_RE = re.compile(r"[0-9a-f]{40}")
DIGEST_RE = re.compile(r"sha256:[0-9a-f]{64}")

# A run in any of these states still covers the roles it claims.
LIVE_STATES = frozenset({"queued", "waiting", "in_progress"})


class RequestError(RuntimeError):
    """The dispatch cannot become a request; the CLI exits with status 2."""


@dataclass
class Request:
    project: str
    source_repo: str
    sha: str
    roles: list[str] = field(default_factory=list)
    run_url: str | None = None
    # Whether the project's images are publicly readable. Defaults to False so
    # an unset value renders the least information, never the most.
    images_public: bool = False

    def commit_url(self) -> str:
        return f"https://github.com/{self.source_repo}/commit/{self.sha}"


@dataclass(frozen=True)
class Marker:
    """What a validate run records about itself, for other runs to read.

    `status` and `conclusion` are deliberately separate. `status` says whether
    the run is still live; `conclusion` says whether its validate job actually
    succeeded. Only a marker with `conclusion == "success"` may suppress or
    replace another request, so a failed decoy run cannot hide a real one.
    """

    project: str
    sha: str
    roles: list[str]
    run_id: int
    status: str
    created_at: datetime
    conclusion: str = ""

    def to_json(self) -> str:
        return json.dumps(
            {
                "project": self.project,
                "sha": self.sha,
                "roles": list(self.roles),
                "run_id": self.run_id,
                "status": self.status,
                "conclusion": self.conclusion,
                "created_at": self.created_at.astimezone(timezone.utc).isoformat(),
            },
            sort_keys=True,
        )

    @classmethod
    def from_dict(cls, data: dict) -> "Marker":
        roles = data.get("roles")
        if not isinstance(roles, list):
            raise ValueError("marker roles must be a list")
        created = data.get("created_at")
        if not isinstance(created, str):
            raise ValueError("marker created_at must be a string")
        return cls(
            project=str(data["project"]),
            sha=str(data["sha"]),
            roles=[str(role) for role in roles],
            run_id=int(data["run_id"]),
            status=str(data["status"]),
            created_at=parse_time(created),
            # A missing conclusion is untrusted, not successful. Defaulting it
            # to "success" let a marker from a run that died early suppress a
            # real request, which is the exact hole this design closes.
            conclusion=str(data.get("conclusion", "")),
        )


def parse_time(value: str) -> datetime:
    """Parse an ISO-8601 timestamp, accepting a trailing Z."""
    text = value.strip()
    if text.endswith("Z"):
        text = text[:-1] + "+00:00"
    parsed = datetime.fromisoformat(text)
    if parsed.tzinfo is None:
        parsed = parsed.replace(tzinfo=timezone.utc)
    return parsed


def load_sources(path: Path = SOURCES_PATH) -> dict:
    return json.loads(Path(path).read_text(encoding="utf-8"))


def _roles(value) -> list[str]:
    if isinstance(value, str):
        items = value.split(",")
    elif isinstance(value, list):
        items = value
    else:
        items = []
    return [str(item).strip().lower() for item in items if str(item).strip()]


def validate_request(request: Request, sources: dict) -> Request:
    """Check the hint against the allowlist and return it in canonical form.

    `run_url` is optional: a hand-run has no source run to point at, and the
    original rule only format-checks it when present.
    """
    projects = sources.get("projects") or {}
    entry = projects.get(request.project)
    if entry is None:
        raise RequestError(
            f"project {request.project!r} is not in config/image-pin-sources.json; "
            f"allowed: {', '.join(sorted(projects)) or 'none'}"
        )
    source_repo = entry["source_repo"]
    # workflow_dispatch runs omit source_repo and take it from the allowlist.
    if request.source_repo and request.source_repo.lower() != source_repo.lower():
        raise RequestError(
            f"source repository {request.source_repo!r} is not allowed for "
            f"project {request.project!r} (expected {source_repo!r})"
        )
    if not SHA_RE.fullmatch(request.sha):
        raise RequestError(f"sha must be a full 40-character commit sha: {request.sha!r}")
    allowed_roles = list(entry["roles"])
    roles = request.roles or allowed_roles
    unknown = sorted(set(roles) - set(allowed_roles))
    if unknown:
        raise RequestError(
            f"roles {unknown} are not allowed for project {request.project!r}; "
            f"allowed: {', '.join(allowed_roles)}"
        )
    run_url = request.run_url
    # run_url is rendered as a link, so allow nothing but a run (attempt) URL.
    run_re = rf"https://github\.com/{re.escape(source_repo)}/actions/runs/\d+(/attempts/\d+)?"
    if run_url and not re.fullmatch(run_re, run_url, re.IGNORECASE):
        raise RequestError(f"run_url is not a run of {source_repo}: {run_url!r}")
    return Request(
        project=request.project,
        source_repo=source_repo,
        sha=request.sha,
        roles=[role for role in allowed_roles if role in roles],
        run_url=run_url,
        images_public=_bool_flag(entry.get("images_public"), "images_public", request.project),
    )


def _bool_flag(value, field_name: str, project: str) -> bool:
    """Read a security-relevant boolean from config without truthiness coercion.

    `bool("false")` is True, so a quoted value in the JSON would flip a private
    project into publishing image configuration. Anything that is not a real
    JSON boolean is an error, not a default.
    """
    if value is None:
        return False
    if not isinstance(value, bool):
        raise RequestError(
            f"projects.{project}.{field_name} must be a JSON boolean, got {type(value).__name__}"
        )
    return value


def write_marker(marker: Marker, path: Path) -> None:
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(marker.to_json() + "\n", encoding="utf-8")


def read_marker(path: Path) -> Marker | None:
    """Return the marker, or None when absent or unusable.

    A corrupt or missing marker must never suppress a legitimate request, so
    every failure reads as "no marker".
    """
    try:
        raw = Path(path).read_text(encoding="utf-8")
        data = json.loads(raw)
    except (OSError, ValueError):
        return None
    if not isinstance(data, dict):
        return None
    try:
        return Marker.from_dict(data)
    except (KeyError, TypeError, ValueError):
        return None


def _usable(markers: list[Marker]) -> list[Marker]:
    """Markers that may decide anything for another run.

    Two independent conditions, and conflating them was a bug:
    * the run is still live (`LIVE_STATES`), and
    * that same run's validate concluded successfully (`conclusion`).

    A run whose validate failed must never suppress or replace another
    request, even though the runs API keeps listing it for 30 days.
    """
    return [
        marker
        for marker in markers
        if marker.status in LIVE_STATES and marker.conclusion == "success"
    ]


def is_duplicate(
    request: Request,
    live: list[Marker] | list[str],
    *,
    own_run_id: int | None = None,
) -> bool:
    """Is an earlier live request already covering these project/commit/roles?

    `own_run_id` excludes the evaluating run's own marker. Without it, a run
    that wrote its marker and then re-checked would treat itself as a duplicate
    and stop, so a legitimate request would silently do nothing.
    """
    wanted = set(request.roles)
    for marker in _usable(live):
        if own_run_id is not None and marker.run_id == own_run_id:
            continue
        if marker.project != request.project:
            continue
        if marker.sha != request.sha:
            continue
        if wanted <= set(marker.roles):
            return True
    return False


def is_replaced(
    request: Request,
    live: list[Marker] | list[str],
    *,
    requested_at: datetime,
    own_run_id: int | None = None,
) -> bool:
    """Is a live request newer than this one, covering all of its roles?

    Three traps, each of which produced a wrong answer:

    * The evaluating run must not see itself. Its own marker is written minutes
      after the run was created, so comparing marker time to request time made a
      run declare itself `replaced` and quietly do nothing.
    * The comparison is marker creation vs this request's creation, not vs
      wall-clock now. Comparing to `now` inverted the answer for old requests.
    * `own_run_id` is the reliable way to exclude self; creation times come from
      different runners and cannot be ordered by equality.

    "Newer" is by creation time rather than run id, because ids are not a
    reliable ordering across queued and re-run requests.
    """
    wanted = set(request.roles)
    for marker in _usable(live):
        if own_run_id is not None and marker.run_id == own_run_id:
            continue
        if marker.project != request.project:
            continue
        if not wanted <= set(marker.roles):
            continue
        if marker.created_at > requested_at:
            return True
    return False


def run_gh(args: list[str]) -> str:
    result = subprocess.run(["gh", *args], capture_output=True, text=True)
    if result.returncode != 0:
        raise RequestError(f"gh {' '.join(args[:2])} failed: {result.stderr.strip()}")
    return result.stdout


# ---------------------------------------------------------------------------
# Rendering (spec §7.3, §8)
# ---------------------------------------------------------------------------

PIN_REPO = "nacfson/nacfson_pipeline"
PIN_RUN_URL = f"https://github.com/{PIN_REPO}/actions/runs"

# Every row of the §7.3 table. Keeping this as a dict rather than an if-chain
# means an unknown outcome raises instead of quietly rendering a blank message.
OUTCOME_TITLES = {
    "pr_open": "PR open",
    "rejected": "Rejected",
    "replaced": "Replaced by a newer build",
    "already_pinned": "Already pinned",
    "failed": "Failed",
    "expired": "Expired or cancelled",
}


def outcome_title(outcome: str) -> str:
    return OUTCOME_TITLES[outcome]


def _short_digest(image: dict) -> str:
    """The 8-hex form used in messages: enough to tell two digests apart."""
    digest = str(image.get("digest") or "")
    return digest.split(":", 1)[1][:8] if ":" in digest else digest[:8]


def render_message(
    request: Request,
    summary: dict,
    *,
    pin_run_id: int | None = None,
) -> dict:
    """The Discord payload for a new request.

    The review button points at the *pin run*, which is the only page that can
    approve the gate. Pointing it at the source project's build run would look
    right and approve nothing.
    """
    lines = [f"New images ready: proj-{request.project}"]
    changed = [image for image in summary.get("images", []) if not image.get("noop")]
    width = max((len(str(image["role"])) for image in changed), default=0)
    for image in changed:
        old = _short_digest({"digest": image.get("old_image", "")})
        lines.append(f"{str(image['role']).ljust(width)}   {old} -> {_short_digest(image)}")
    if request.run_url:
        lines.append(f"Build {request.run_url}")

    link = f"{PIN_RUN_URL}/{pin_run_id}" if pin_run_id is not None else None
    return {
        "content": "\n".join(lines),
        "allowed_mentions": {"parse": []},
        "review_url": link,
    }


def _md_cell(value: object) -> str:
    """Make a string safe inside a markdown table cell."""
    text = " ".join(str(value).split())
    return text.replace("|", "\\|").replace("`", "'")


def _md_line(value: object, limit: int = 300) -> str:
    """Make a string safe as a single markdown line.

    Newlines are the dangerous part: they let a value open its own heading, so
    warning text from an image could forge a `### Checks` section above the
    real one. Collapsing whitespace is the fix; truncation bounds the damage.
    """
    text = " ".join(str(value).split())
    if len(text) > limit:
        text = text[:limit] + "…"
    return text.replace("`", "'")


def _evidence_rows(image: dict, include_evidence: bool) -> list[str]:
    """Per-platform lines. Image configuration only when the image is public."""
    rows = []
    platforms = image.get("platforms") or []
    if platforms:
        rows.append(f"- platforms: {', '.join(platforms)}")
    failed = sorted(
        key for key, ok in (image.get("checks") or {}).items() if not ok
    )
    if failed:
        rows.append(f"- failed checks: {', '.join(failed)}")
    if not include_evidence:
        return rows
    for platform, values in sorted((image.get("evidence") or {}).items()):
        details = []
        for field in ("user", "source", "revision"):
            if values.get(field):
                details.append(f"{field}={values[field]}")
        entrypoint = values.get("entrypoint") or []
        if entrypoint:
            details.append(f"entrypoint={' '.join(str(part) for part in entrypoint)}")
        ports = values.get("exposed_ports") or []
        if ports:
            details.append(f"ports={', '.join(str(port) for port in ports)}")
        if details:
            rows.append(f"- {platform}: {'; '.join(details)}")
    return rows


def render_pr_body(
    request: Request,
    summary: dict,
    checks: dict[str, str],
    pin_run_id: int | None = None,
) -> str:
    """The PR description.

    `request.images_public` decides whether image configuration is included.
    It is False unless the allowlist says otherwise, so an unset or unknown
    project renders the least information rather than the most.
    """
    include_evidence = bool(request.images_public)
    lines = [
        f"Pins `proj-{request.project}` to the digests approved in "
        f"{f'[run {pin_run_id}]({PIN_RUN_URL}/{pin_run_id})' if pin_run_id else 'the pin run'}.",
        "",
        f"Built from `{request.source_repo}@{request.sha[:7]}`.",
        "",
        "| Role | Container | Old digest | New digest | Tag |",
        "| :--- | :--- | :--- | :--- | :--- |",
    ]
    for image in summary.get("images", []):
        if image.get("noop"):
            continue
        old = _short_digest({"digest": image.get("old_image", "")})
        tag = image.get("tag") or "—"
        lines.append(
            f"| {_md_cell(image['role'])} | {_md_cell(image.get('container') or image.get('deployment'))} | "
            f"`{old}` | `{_short_digest(image)}` | `{_md_cell(tag)}` |"
        )

    lines += ["", "### Validation", ""]
    for image in summary.get("images", []):
        if image.get("noop"):
            continue
        lines.append(f"**{_md_cell(image['role'])}** `{_md_cell(image.get('digest'))}`")
        lines += _evidence_rows(image, include_evidence)
        # Warnings are omitted for private images. They are not a fixed
        # vocabulary: the helper builds them from the image's own config —
        # `image user is 10001`, `ExposedPorts declares [...]`,
        # `secret-like env DATABASE_URL`, the source label — which is exactly
        # the data the public/private rule forbids publishing.
        if include_evidence:
            for warning in image.get("warnings") or []:
                lines.append(f"- warning: {_md_line(warning)}")
        lines.append("")

    if not include_evidence:
        lines += [
            "Image configuration and its warnings are omitted because this "
            "project's images are not public.",
            "",
        ]

    lines += ["### Checks", ""]
    if checks:
        for name in sorted(checks):
            lines.append(f"- `{_md_line(name, limit=120)}`: {_md_line(checks[name], limit=200)}")
    else:
        lines.append("- none recorded")
    lines += [
        "",
        "### CI",
        "",
        "GitHub's normal PR workflows did not run on this PR: it was opened with "
        "`GITHUB_TOKEN`, which starts no workflows. The pin job ran the admission "
        "gate instead — the diff check, the handbook check, and `render-validate` "
        "plus `platform-preflight` for both environments.",
        "",
    ]
    return "\n".join(lines)


# ---------------------------------------------------------------------------
# Diff check (spec §9)
# ---------------------------------------------------------------------------

YAML_PREFIX = "deploy/projects/"
YAML_SUFFIX = "/workloads/"
HANDBOOK_PATH = "docs/platform-architecture.html"
# A full digest, and the 8-hex short form the handbook uses in `Pinned @sha256:`
# badges. The short form is only ever compared, never approved.
FULL_DIGEST_RE = re.compile(r"sha256:[0-9a-f]{64}")
SHORT_DIGEST_RE = re.compile(r"sha256:[0-9a-f]{8}(?![0-9a-f])")
IMAGE_LINE_RE = re.compile(r"^(\s*)image:\s*(\S+)\s*$")


class _DiffError(Exception):
    """Raised inside a per-file check; collected into the violation list."""


@dataclass(frozen=True)
class _DiffLine:
    """One content line of a unified diff, with its marker stripped."""

    sign: str  # "+" or "-"
    body: str
    hunk: int


FILE_HEADER_PREFIXES = ("diff --git ", "index ", "old mode ", "new mode ")
BINARY_MARKER = "Binary files "


def _parse_diff(diff_text: str, path: str) -> list[_DiffLine]:
    """Parse a unified diff into content lines, strictly.

    A naive `startswith("+++")` skip-list is wrong: a *content* line whose text
    begins with `+` renders as `+++...`, so it was dropped and an arbitrary
    change could ride along with a legitimate pin. Instead this tracks the hunk
    header state and decides by position: the first line of a hunk is a content
    line, and `---`/`+++` are only file headers *before* any hunk.

    Anything unrecognised is an error rather than a skip, because a diff this
    parser cannot read is a diff it must not approve.
    """
    lines = diff_text.splitlines()
    content: list[_DiffLine] = []
    hunk = -1
    index = 0
    while index < len(lines):
        line = lines[index]
        if line.startswith(FILE_HEADER_PREFIXES):
            index += 1
            continue
        if line.startswith(BINARY_MARKER):
            raise _DiffError(f"{path}: binary diff is not a pin")
        if line.startswith("--- ") and hunk < 0:
            # File header. The +++ line must follow.
            if index + 1 >= len(lines) or not lines[index + 1].startswith("+++ "):
                raise _DiffError(f"{path}: malformed diff header")
            index += 2
            continue
        if line.startswith("@@ "):
            hunk += 1
            index += 1
            continue
        if hunk < 0 and not line.strip():
            index += 1
            continue
        if hunk < 0:
            raise _DiffError(f"{path}: content before the first hunk: {line[:80]!r}")
        if line.startswith("\\"):
            # "\ No newline at end of file"
            index += 1
            continue
        if not line:
            raise _DiffError(f"{path}: empty line inside a hunk")
        sign, body = line[0], line[1:]
        if sign not in "+- ":
            raise _DiffError(f"{path}: unparseable diff line: {line[:80]!r}")
        if sign in "+-":
            content.append(_DiffLine(sign=sign, body=body, hunk=hunk))
        index += 1
    return content


def _require_prefix(prefix: str) -> tuple[str, ...]:
    """Reject any path segment that could escape the directory it claims."""
    return tuple(prefix.strip("/").split("/"))


def _segments(path: str) -> list[str]:
    return [segment for segment in path.split("/") if segment not in ("", ".")]


def _is_under(path: str, prefix: str) -> bool:
    """True only if `path` is inside `prefix`, with no traversal segments."""
    if ".." in _segments(path):
        return False
    wanted = _segments(prefix)
    actual = _segments(path)
    return actual[: len(wanted)] == wanted and len(actual) > len(wanted)


def _check_yaml_file(path: str, diff_text: str, project: str, approved: dict[str, str]) -> str:
    """Validate one workload file's diff; return the digest it pins."""
    expected_prefix = f"{YAML_PREFIX}{project}{YAML_SUFFIX}"
    if not _is_under(path, expected_prefix):
        raise _DiffError(f"{path}: not under {expected_prefix}")

    removed: list[str] = []
    added: list[str] = []
    for change in _parse_diff(diff_text, path):
        stripped = change.body.strip()
        if not stripped:
            raise _DiffError(f"{path}: a changed line is blank")
        if not stripped.startswith("image:"):
            raise _DiffError(f"{path}: changed line is not an `image:` line: {stripped[:80]!r}")
        (added if change.sign == "+" else removed).append(change.body)

    if len(removed) != 1 or len(added) != 1:
        raise _DiffError(
            f"{path}: expected exactly one removed and one added line, "
            f"found {len(removed)} removed and {len(added)} added"
        )

    old_line, new_line = removed[0], added[0]
    old_match = IMAGE_LINE_RE.match(old_line)
    new_match = IMAGE_LINE_RE.match(new_line)
    if old_match is None or new_match is None:
        raise _DiffError(f"{path}: cannot parse the image line")
    if old_match.group(1) != new_match.group(1):
        raise _DiffError(
            f"{path}: indentation changed "
            f"({len(old_match.group(1))} -> {len(new_match.group(1))} spaces)"
        )

    old_ref, new_ref = old_match.group(2), new_match.group(2)
    if not FULL_DIGEST_RE.search(new_ref):
        raise _DiffError(f"{path}: new image is not pinned by digest: {new_ref!r}")
    old_repo = old_ref.rsplit("@", 1)[0]
    if "@" not in new_ref:
        raise _DiffError(f"{path}: new image has no digest: {new_ref!r}")
    new_repo, new_digest = new_ref.rsplit("@", 1)
    if new_repo != old_repo:
        raise _DiffError(
            f"{path}: repository changed ({old_repo!r} -> {new_repo!r}); "
            "a pin may only change the digest"
        )

    approved_refs = {ref for ref in approved.values()}
    if new_ref not in approved_refs:
        raise _DiffError(f"{path}: {new_ref} is not one of the approved images")
    return new_digest


def _check_handbook(diff_text: str, approved: dict[str, str]) -> None:
    """Every handbook difference must map an old digest to an approved new one.

    Two independent guarantees, because checking only that the new digest is
    somewhere in the approved set lets rows swap digests:

      1. the line, with digest tokens blanked, is otherwise unchanged; and
      2. the old digest on that line is one the pin is replacing with the new
         digest on that same line.
    """
    if not diff_text.strip():
        raise _DiffError(f"{HANDBOOK_PATH}: html_changed was reported but the diff is empty")

    approved_pairs = set()
    for ref in approved.values():
        repo, _, digest = ref.partition("@")
        if digest:
            approved_pairs.add((repo, digest))

    content = _parse_diff(diff_text, HANDBOOK_PATH)
    removed = [change.body for change in content if change.sign == "-"]
    added = [change.body for change in content if change.sign == "+"]

    if not removed and not added:
        raise _DiffError(f"{HANDBOOK_PATH}: the diff changes no lines")
    if len(removed) != len(added):
        raise _DiffError(
            f"{HANDBOOK_PATH}: {len(removed)} removed vs {len(added)} added lines"
        )

    for old_line, new_line in zip(removed, added):
        old_full = FULL_DIGEST_RE.findall(old_line)
        new_full = FULL_DIGEST_RE.findall(new_line)
        old_short = SHORT_DIGEST_RE.findall(old_line)
        new_short = SHORT_DIGEST_RE.findall(new_line)

        # 1. Nothing but digest tokens may differ.
        if _blank(old_line, old_full + old_short) != _blank(new_line, new_full + new_short):
            raise _DiffError(
                f"{HANDBOOK_PATH}: a change is not a digest swap: {new_line.strip()[:80]!r}"
            )

        # 2. Each new digest must be the replacement for an old one present on
        #    the same line. A line may carry several rows, so compare sets.
        if len(old_full) != len(new_full):
            raise _DiffError(
                f"{HANDBOOK_PATH}: digest count changed "
                f"({len(old_full)} -> {len(new_full)})"
            )
        if old_full:
            # Full digests: the repository is on the same line, so require the
            # approved (repo, digest) pair to be an exact repin of an old digest.
            for new_digest in new_full:
                if new_digest in old_full:
                    continue  # an unchanged digest elsewhere in the same row
                if not any(
                    digest == new_digest and repo in new_line
                    for repo, digest in approved_pairs
                ):
                    raise _DiffError(
                        f"{HANDBOOK_PATH}: {new_digest} is not an approved digest for "
                        "a repository on that line"
                    )
            if set(new_full) == set(old_full) and old_line != new_line:
                raise _DiffError(f"{HANDBOOK_PATH}: a digest line changed without a repin")

        # Short badges are 8-hex prefixes of a full digest. They must be the
        # prefix of an approved digest, compared against the same field the
        # approved refs carry (`sha256:<hex>`), not against bare hex.
        for new_token in new_short:
            hex_part = new_token.split(":", 1)[1]
            if not any(
                approved_digest.split(":", 1)[1].startswith(hex_part)
                for _, approved_digest in approved_pairs
            ):
                raise _DiffError(f"{HANDBOOK_PATH}: short digest {new_token} is not approved")
        for old_token, new_token in zip(old_short, new_short):
            if old_token == new_token:
                continue
            # The badge must still be the prefix of the digest that row pins,
            # so a badge cannot be repointed at a different role's image.
            if not any(
                approved_digest.split(":", 1)[1].startswith(new_token.split(":", 1)[1])
                for _, approved_digest in approved_pairs
            ):
                raise _DiffError(f"{HANDBOOK_PATH}: short digest {new_token} is not approved")


def _blank(text: str, tokens: list[str]) -> str:
    for token in tokens:
        text = text.replace(token, "\x00")
    return text


def diff_check(changed: dict[str, str], summary: dict, approved: dict[str, str]) -> list[str]:
    """Validate the pin diff. Returns violations; empty means it passed.

    `changed` maps path -> unified diff text for every modified file.
    `summary` is the helper's summary dict.
    `approved` maps role -> `repo@sha256:<64-hex>` that the operator approved.

    Rules, in the order they are checked:
      1. The file set equals the paths the helper actually wrote (roles whose
         image changed), plus the handbook when the helper rewrote it.
      2. Every workload path is under this project's workloads directory.
      3. Each workload file has exactly one removed and one added `image:` line,
         same repository, same indentation, new digest approved.
      4. Every handbook difference maps an old digest to an approved new one.
    """
    project = summary["project"]
    violations: list[str] = []

    written_paths = {
        image["path"] for image in summary.get("images", []) if not image.get("noop")
    }
    expected = set(written_paths)
    if summary.get("html_changed"):
        expected.add(HANDBOOK_PATH)

    actual = set(changed)
    for path in sorted(actual - expected):
        violations.append(f"{path}: changed but the helper did not write it")
    for path in sorted(expected - actual):
        violations.append(f"{path}: the helper wrote it but it is not in the diff")
    if violations:
        return violations

    if not expected:
        return [f"diff is empty: nothing was written (project {project})"]

    for path in sorted(written_paths & actual):
        try:
            _check_yaml_file(path, changed[path], project, approved)
        except _DiffError as exc:
            violations.append(str(exc))

    if HANDBOOK_PATH in actual:
        try:
            _check_handbook(changed[HANDBOOK_PATH], approved)
        except _DiffError as exc:
            violations.append(str(exc))

    return violations


def request_from_env(project: str, sha: str, roles: str, run_url: str | None) -> Request:
    return Request(
        project=(project or "").strip().lower(),
        source_repo="",
        sha=(sha or "").strip().lower(),
        roles=_roles(roles),
        run_url=(run_url or "").strip() or None,
    )


def _markers_from_json(text: str) -> list[Marker]:
    """Read a list of marker objects, skipping anything unusable."""
    try:
        data = json.loads(text or "[]")
    except ValueError:
        return []
    if not isinstance(data, list):
        return []
    markers = []
    for item in data:
        if not isinstance(item, dict):
            continue
        try:
            markers.append(Marker.from_dict(item))
        except (KeyError, TypeError, ValueError):
            continue
    return markers


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.split("\n", 1)[0])
    parser.add_argument("--root", type=Path, default=REPO_ROOT)
    parser.add_argument("--sources", type=Path, default=SOURCES_PATH)
    sub = parser.add_subparsers(dest="command", required=True)

    check = sub.add_parser("check-request", help="validate the dispatch inputs")
    check.add_argument("--project", required=True)
    check.add_argument("--sha", required=True)
    check.add_argument("--roles", default="")
    check.add_argument("--run-url", default="")
    check.add_argument("--json", action="store_true")

    dup = sub.add_parser("dedupe", help="is this request already covered?")
    dup.add_argument("--project", required=True)
    dup.add_argument("--sha", required=True)
    dup.add_argument("--roles", default="")
    dup.add_argument("--live", default="[]", help="JSON list of live markers")
    dup.add_argument(
        "--own-run-id",
        type=int,
        default=None,
        help="this run's id, excluded so a run cannot see itself as a duplicate",
    )
    dup.add_argument("--json", action="store_true")

    newer = sub.add_parser("newer-check", help="has a newer request replaced this one?")
    newer.add_argument("--project", required=True)
    newer.add_argument("--sha", required=True)
    newer.add_argument("--roles", default="")
    newer.add_argument("--live", default="[]", help="JSON list of live markers")
    newer.add_argument(
        "--requested-at",
        required=True,
        help="ISO-8601 time this request was created; markers newer than it replace it",
    )
    newer.add_argument(
        "--own-run-id",
        type=int,
        default=None,
        help="this run's id, excluded so a run cannot declare itself replaced",
    )
    newer.add_argument("--json", action="store_true")

    mark = sub.add_parser("write-marker", help="record what this run concluded")
    mark.add_argument("--project", required=True)
    mark.add_argument("--sha", required=True)
    mark.add_argument("--roles", default="")
    mark.add_argument("--run-id", required=True, type=int)
    mark.add_argument("--status", default="queued", choices=sorted(LIVE_STATES))
    mark.add_argument("--conclusion", default="success")
    mark.add_argument("--out", required=True, type=Path)

    diffp = sub.add_parser("diff-check", help="validate the pin diff before opening a PR")
    diffp.add_argument(
        "--changed-dir",
        required=True,
        type=Path,
        help="directory of per-file unified diffs, named after each changed path "
        "(created with `git diff --name-only` + `git diff -- <path>`)",
    )
    diffp.add_argument("--summary", required=True, type=Path, help="helper summary JSON")
    diffp.add_argument(
        "--approved",
        required=True,
        type=Path,
        help="JSON object mapping role -> approved repo@sha256:<64-hex>",
    )
    diffp.add_argument("--json", action="store_true")

    render = sub.add_parser("render-message", help="the Discord payload for a request")
    render.add_argument("--project", required=True)
    render.add_argument("--sha", required=True)
    render.add_argument("--roles", default="")
    render.add_argument("--summary", required=True, type=Path)
    render.add_argument("--pin-run-id", type=int, default=None)
    render.add_argument("--out", type=Path, help="write the payload here instead of stdout")

    body = sub.add_parser("render-pr-body", help="the PR description for a pin")
    body.add_argument("--project", required=True)
    body.add_argument("--sha", required=True)
    body.add_argument("--roles", default="")
    body.add_argument("--summary", required=True, type=Path)
    body.add_argument("--checks", type=Path, help="JSON object of check name -> result")
    body.add_argument("--pin-run-id", type=int, default=None)
    body.add_argument("--out", type=Path, help="write the body here instead of stdout")

    args = parser.parse_args(argv)

    try:
        sources = load_sources(args.sources)

        if args.command == "write-marker":
            # Canonicalize through the same validation as every other verb, and
            # reject what the allowlist rejects. Writing the raw inputs made the
            # marker's identity differ from the request identity (case, and an
            # empty --roles meaning "every role"), so dedupe silently matched
            # nothing. A request that was rejected never gets a marker, so its
            # failure stays visible.
            canonical = validate_request(
                request_from_env(args.project, args.sha, args.roles, None), sources
            )
            write_marker(
                Marker(
                    project=canonical.project,
                    sha=canonical.sha,
                    roles=canonical.roles,
                    run_id=args.run_id,
                    status=args.status,
                    created_at=datetime.now(timezone.utc),
                    conclusion=args.conclusion,
                ),
                args.out,
            )
            print(f"marker written to {args.out}")
            return 0

        if args.command == "diff-check":
            summary = json.loads(Path(args.summary).read_text(encoding="utf-8"))
            approved = json.loads(Path(args.approved).read_text(encoding="utf-8"))
            changed = {}
            base = Path(args.changed_dir)
            for path in sorted(base.rglob("*")):
                if path.is_file():
                    rel = path.relative_to(base).as_posix()
                    # The dir name encodes the repository path with '/' kept as
                    # directories, so the relative path is the repo path.
                    changed[rel] = path.read_text(encoding="utf-8")
            violations = diff_check(changed, summary, approved)
            if args.json:
                print(json.dumps({"passed": not violations, "violations": violations}))
            else:
                for violation in violations:
                    print(f"::error::{violation}")
                print("diff check passed" if not violations else "diff check FAILED")
            return 0 if not violations else 1

        request = validate_request(
            request_from_env(args.project, args.sha, args.roles, getattr(args, "run_url", "")),
            sources,
        )

        if args.command == "check-request":
            payload = {
                "project": request.project,
                "source_repo": request.source_repo,
                "sha": request.sha,
                "roles": request.roles,
                "run_url": request.run_url,
            }
            if args.json:
                print(json.dumps(payload, sort_keys=True))
            else:
                print(f"{request.project} {request.sha[:7]} {','.join(request.roles)}")
            return 0

        if args.command in ("render-message", "render-pr-body"):
            summary = json.loads(Path(args.summary).read_text(encoding="utf-8"))
            if args.command == "render-message":
                payload = json.dumps(
                    render_message(request, summary, pin_run_id=args.pin_run_id),
                    indent=2,
                    sort_keys=True,
                )
            else:
                checks = {}
                if args.checks:
                    checks = json.loads(Path(args.checks).read_text(encoding="utf-8"))
                payload = render_pr_body(request, summary, checks, args.pin_run_id)
            if args.out:
                Path(args.out).write_text(payload + "\n", encoding="utf-8")
                print(f"wrote {args.out}")
            else:
                print(payload)
            return 0

        live = _markers_from_json(args.live)

        if args.command == "dedupe":
            duplicate = is_duplicate(request, live, own_run_id=args.own_run_id)
            if args.json:
                print(json.dumps({"duplicate": duplicate}))
            else:
                print("duplicate" if duplicate else "new")
            return 0

        if args.command == "newer-check":
            replaced = is_replaced(
                request,
                live,
                requested_at=parse_time(args.requested_at),
                own_run_id=args.own_run_id,
            )
            if args.json:
                print(json.dumps({"replaced": replaced}))
            else:
                print("replaced" if replaced else "current")
            return 0

    except RequestError as exc:
        print(f"::error::{exc}", file=sys.stderr)
        return 2
    return 0


if __name__ == "__main__":
    sys.exit(main())
