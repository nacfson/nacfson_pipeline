#!/usr/bin/env python3

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

LIVE_STATES = frozenset({"queued", "waiting", "in_progress"})


class RequestError(RuntimeError):
    pass


@dataclass
class Request:
    project: str
    source_repo: str
    sha: str
    roles: list[str] = field(default_factory=list)
    run_url: str | None = None
    images_public: bool = False

    def commit_url(self) -> str:
        return f"https://github.com/{self.source_repo}/commit/{self.sha}"


@dataclass(frozen=True)
class Marker:

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
            conclusion=str(data.get("conclusion", "")),
        )


def parse_time(value: str) -> datetime:
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
    projects = sources.get("projects") or {}
    entry = projects.get(request.project)
    if entry is None:
        raise RequestError(
            f"project {request.project!r} is not in config/image-pin-sources.json; "
            f"allowed: {', '.join(sorted(projects)) or 'none'}"
        )
    source_repo = entry["source_repo"]
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



PIN_REPO = "nacfson/nacfson_pipeline"
PIN_RUN_URL = f"https://github.com/{PIN_REPO}/actions/runs"

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
    digest = str(image.get("digest") or "")
    return digest.split(":", 1)[1][:8] if ":" in digest else digest[:8]


def render_message(
    request: Request,
    summary: dict,
    *,
    pin_run_id: int | None = None,
) -> dict:
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
    text = " ".join(str(value).split())
    return text.replace("|", "\\|").replace("`", "'")


def _md_line(value: object, limit: int = 300) -> str:
    text = " ".join(str(value).split())
    if len(text) > limit:
        text = text[:limit] + "…"
    return text.replace("`", "'")


def _evidence_rows(image: dict, include_evidence: bool) -> list[str]:
    rows = []
    if image.get("validation_skipped"):
        rows.append(
            "- **not inspected in this job** — validation ran in the earlier "
            "`validate` run that the operator approved"
        )
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



YAML_PREFIX = "deploy/projects/"
YAML_SUFFIX = "/workloads/"
HANDBOOK_PATH = "docs/platform-architecture.html"
FULL_DIGEST_RE = re.compile(r"sha256:[0-9a-f]{64}")
SHORT_DIGEST_RE = re.compile(r"sha256:[0-9a-f]{8}(?![0-9a-f])")
IMAGE_LINE_RE = re.compile(r"^(\s*)image:\s*(\S+)\s*$")
VERSION_LINE_RE = re.compile(
    r"^(\s*)app\.kubernetes\.io/version:\s*(?:\"([^\"]+)\"|'([^']+)'|(\S+))\s*$"
)
VERSION_VAL_RE = re.compile(r"^v\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$")


class _DiffError(Exception):
    pass


@dataclass(frozen=True)
class _DiffLine:

    sign: str
    body: str
    hunk: int


FILE_HEADER_PREFIXES = ("diff --git ", "index ", "old mode ", "new mode ")
BINARY_MARKER = "Binary files "


def _parse_diff(diff_text: str, path: str) -> list[_DiffLine]:
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
    return tuple(prefix.strip("/").split("/"))


def _segments(path: str) -> list[str]:
    return [segment for segment in path.split("/") if segment not in ("", ".")]


def _is_under(path: str, prefix: str) -> bool:
    if ".." in _segments(path):
        return False
    wanted = _segments(prefix)
    actual = _segments(path)
    return actual[: len(wanted)] == wanted and len(actual) > len(wanted)


def _check_yaml_file(path: str, diff_text: str, project: str, approved: dict[str, str]) -> str:
    expected_prefix = f"{YAML_PREFIX}{project}{YAML_SUFFIX}"
    if not _is_under(path, expected_prefix):
        raise _DiffError(f"{path}: not under {expected_prefix}")

    removed: list[str] = []
    added: list[str] = []
    version_removed: list[str] = []
    version_added: list[str] = []
    for change in _parse_diff(diff_text, path):
        stripped = change.body.strip()
        if not stripped:
            raise _DiffError(f"{path}: a changed line is blank")
        if stripped.startswith("image:"):
            (added if change.sign == "+" else removed).append(change.body)
        elif stripped.startswith("app.kubernetes.io/version:"):
            (version_added if change.sign == "+" else version_removed).append(change.body)
        else:
            raise _DiffError(
                f"{path}: changed line is neither an `image:` line nor a version label line: {stripped[:80]!r}"
            )

    if len(removed) != 1 or len(added) != 1:
        raise _DiffError(
            f"{path}: expected exactly one removed and one added image line, "
            f"found {len(removed)} removed and {len(added)} added"
        )

    if len(version_removed) > 1 or len(version_added) > 1:
        raise _DiffError(
            f"{path}: expected at most one removed and one added version label line, "
            f"found {len(version_removed)} removed and {len(version_added)} added"
        )

    if version_removed and not version_added:
        raise _DiffError(f"{path}: version label removed without replacement")

    if version_added:
        new_v_line = version_added[0]
        new_v_match = VERSION_LINE_RE.match(new_v_line)
        if not new_v_match:
            raise _DiffError(f"{path}: cannot parse the version label line: {new_v_line.strip()!r}")
        indent = new_v_match.group(1)
        new_v = new_v_match.group(2) or new_v_match.group(3) or new_v_match.group(4)
        if not VERSION_VAL_RE.match(new_v) or new_v == "v0.0.0":
            raise _DiffError(f"{path}: invalid version label {new_v!r}")
        if version_removed:
            old_v_line = version_removed[0]
            old_v_match = VERSION_LINE_RE.match(old_v_line)
            if old_v_match and old_v_match.group(1) != indent:
                raise _DiffError(
                    f"{path}: version label indentation changed "
                    f"({len(old_v_match.group(1))} -> {len(indent)} spaces)"
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

        if _blank(old_line, old_full + old_short) != _blank(new_line, new_full + new_short):
            raise _DiffError(
                f"{HANDBOOK_PATH}: a change is not a digest swap: {new_line.strip()[:80]!r}"
            )

        if len(old_full) != len(new_full):
            raise _DiffError(
                f"{HANDBOOK_PATH}: digest count changed "
                f"({len(old_full)} -> {len(new_full)})"
            )
        if old_full:
            for new_digest in new_full:
                if new_digest in old_full:
                    continue
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
    parser = argparse.ArgumentParser(description='Request checks, run markers, dedupe and supersede for the image pin flow.')
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
