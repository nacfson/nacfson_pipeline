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
    )


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
