#!/usr/bin/env python3
"""Turn an `image-built` dispatch from a project repo into an image-bump issue.

Run by `.github/workflows/image-bump-request.yaml`.  The dispatch payload is a
hint only: the project and source repository must match
`config/image-bump-sources.json`, and every image is re-resolved and validated
from the registry by the `update-project-images` helper (dry run, so no file
is written).  The issue is the request; whoever handles it (a Claude session,
Copilot, or a person) decides whether a pin PR is opened.

Outcomes: invalid request or failed image validation -> exit 2, no issue;
every image already pinned -> exit 0, no issue; an open issue already asks for
the same commit -> exit 0, no issue; otherwise open a new issue and close older
open issues for the same project whose roles it covers.
"""

from __future__ import annotations

import argparse
import importlib.util
import json
import re
import subprocess
import sys
import tempfile
from dataclasses import dataclass
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
HELPER_PATH = (
    REPO_ROOT
    / ".agents"
    / "skills"
    / "update-project-images"
    / "scripts"
    / "update_project_images.py"
)
SOURCES_PATH = REPO_ROOT / "config" / "image-bump-sources.json"
SHA_RE = re.compile(r"[0-9a-f]{40}")
MARKER_RE = re.compile(r"<!-- image-bump-request: (\{.*?\}) -->")


class RequestError(RuntimeError):
    """The dispatch cannot become an issue; the CLI exits with status 2."""


@dataclass
class Request:
    project: str
    source_repo: str
    sha: str
    roles: list[str]
    run_url: str | None = None

    def commit_url(self) -> str:
        return f"https://github.com/{self.source_repo}/commit/{self.sha}"


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


def parse_request(event: dict, event_name: str) -> Request:
    if event_name == "repository_dispatch":
        payload = event.get("client_payload") or {}
    elif event_name == "workflow_dispatch":
        payload = event.get("inputs") or {}
    else:
        raise RequestError(f"unsupported event: {event_name!r}")
    return Request(
        project=str(payload.get("project") or "").strip().lower(),
        source_repo=str(payload.get("source_repo") or "").strip(),
        sha=str(payload.get("sha") or "").strip().lower(),
        roles=_roles(payload.get("roles")),
        run_url=str(payload.get("run_url") or "").strip() or None,
    )


def validate_request(request: Request, sources: dict) -> Request:
    """Check the hint against the allowlist and return it in canonical form."""
    projects = sources.get("projects") or {}
    entry = projects.get(request.project)
    if entry is None:
        raise RequestError(
            f"project {request.project!r} is not in config/image-bump-sources.json; "
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


def load_helper():
    spec = importlib.util.spec_from_file_location("update_project_images", HELPER_PATH)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"cannot load {HELPER_PATH}")
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


def resolve(root: Path, request: Request, helper) -> dict:
    """Dry-run the pin helper: resolve the commit tag and validate each image."""
    image_args = [f"{role}={request.commit_url()}" for role in request.roles]
    try:
        return helper.run_update(
            root=root,
            project=request.project,
            image_args=image_args,
            dry_run=True,
            strict=False,
            pull_check=False,
        )
    except helper.UpdateError as exc:
        raise RequestError(f"image validation failed: {exc}") from exc


def marker(request: Request) -> str:
    data = {
        "project": request.project,
        "roles": request.roles,
        "source_repo": request.source_repo,
        "sha": request.sha,
    }
    return f"<!-- image-bump-request: {json.dumps(data, sort_keys=True)} -->"


def parse_marker(body: str) -> dict | None:
    match = MARKER_RE.search(body or "")
    if not match:
        return None
    try:
        data = json.loads(match.group(1))
    except json.JSONDecodeError:
        return None
    return data if isinstance(data, dict) else None


def plan(request: Request, open_issues: list[dict]) -> tuple[int | None, list[int]]:
    """Return (duplicate issue number or None, issue numbers to supersede).

    A duplicate already asks for this commit and every requested role.  Older
    requests are superseded only when this one covers all of their roles, so a
    frontend-only build never drops a pending backend request.  Supersede
    numbers are returned with a duplicate too, so a retry after a failed close
    still closes them.
    """
    roles = set(request.roles)
    duplicate: int | None = None
    candidates: list[tuple[int, set[str]]] = []
    for issue in open_issues:
        data = parse_marker(issue.get("body") or "")
        if not data or data.get("project") != request.project:
            continue
        number, old_roles = int(issue["number"]), set(_roles(data.get("roles")))
        if duplicate is None and data.get("sha") == request.sha and roles <= old_roles:
            duplicate = number
        else:
            candidates.append((number, old_roles))
    supersede = sorted(number for number, old_roles in candidates if old_roles <= roles)
    return duplicate, supersede


def _code(value) -> str:
    text = " ".join(str(item) for item in value) if isinstance(value, list) else str(value)
    text = text.replace("`", "'").replace("|", "/").replace("\n", " ").strip()
    return f"`{text}`" if text else "—"


def render_title(request: Request, summary: dict) -> str:
    roles = [image["role"] for image in summary["images"] if not image["noop"]]
    return (
        f"image-bump: proj-{request.project} {', '.join(roles)} "
        f"→ {request.source_repo.split('/')[-1]}@{request.sha[:7]}"
    )


def render_body(request: Request, summary: dict, receiver_run_url: str | None) -> str:
    source = f"[`{request.source_repo}@{request.sha[:7]}`]({request.commit_url()})"
    built_by = f" by [this build]({request.run_url})" if request.run_url else ""
    lines = [
        marker(request),
        f"## Image bump request: `proj-{request.project}`",
        "",
        f"New images were published{built_by} from {source}.",
        "Nothing has changed yet. This issue only asks whether to pin them.",
        "",
        "| Role | Deployment | Current | Proposed | Tag |",
        "| :--- | :--- | :--- | :--- | :--- |",
    ]
    for image in summary["images"]:
        current = image["old_image"].rsplit("@", 1)[-1][:19]
        proposed = "already pinned" if image["noop"] else image["digest"][:19]
        lines.append(
            f"| {image['role']} | {image['deployment']} | `{current}` | "
            f"`{proposed}` | {_code(image['tag'] or '')} |"
        )
    lines += ["", "### Image checks (registry only, dry run)", ""]
    for image in summary["images"]:
        if image["noop"]:
            continue
        passed = ", ".join(sorted(key for key, ok in image["checks"].items() if ok))
        lines.append(f"**{image['role']}** `{image['digest']}`")
        lines.append(f"- passed: {passed or 'none recorded'}")
        for warning in image["resolution_warnings"] + image["warnings"]:
            lines.append(f"- warning: {_code(warning)}")
        for skipped in image["skipped"]:
            lines.append(f"- skipped: {_code(skipped)}")
        for platform, evidence in sorted(image["evidence"].items()):
            lines.append(
                f"- {platform}: user {_code(evidence.get('user', ''))}, "
                f"revision {_code(evidence.get('revision', ''))}, "
                f"entrypoint {_code(evidence.get('entrypoint') or evidence.get('cmd') or '')}, "
                f"ports {_code(evidence.get('exposed_ports') or '')}"
            )
        lines.append("")
    image_flags = " ".join(
        f"--image {role}={request.commit_url()}" for role in request.roles
    )
    lines += [
        "### Decision",
        "",
        "- **Allow**: apply with the `update-project-images` skill, then open one PR "
        f"that closes this issue:",
        "",
        "  ```bash",
        "  python3 .agents/skills/update-project-images/scripts/update_project_images.py \\",
        f"    --root . --project {request.project} {image_flags}",
        "  ```",
        "- **Disallow**: close this issue as not planned. No branch, no PR.",
        "",
        "Before applying, re-check that this issue is still open and that no newer "
        "`image-bump` issue exists for this project.",
    ]
    if receiver_run_url:
        lines += ["", f"_Opened by [image-bump-request]({receiver_run_url})._"]
    return "\n".join(lines) + "\n"


def run_gh(args: list[str]) -> str:
    result = subprocess.run(["gh", *args], capture_output=True, text=True)
    if result.returncode != 0:
        raise RequestError(f"gh {' '.join(args[:2])} failed: {result.stderr.strip()}")
    return result.stdout


def list_open_issues(repo: str, label: str, gh=run_gh) -> list[dict]:
    output = gh(
        [
            "issue", "list", "--repo", repo, "--label", label, "--state", "open",
            "--limit", "100", "--json", "number,body",
        ]
    )
    return json.loads(output or "[]")


def open_issue(repo: str, label: str, title: str, body: str, gh=run_gh) -> int:
    gh(
        [
            "label", "create", label, "--repo", repo, "--force", "--color", "1D76DB",
            "--description", "Project image pin request from a source repo build",
        ]
    )
    with tempfile.NamedTemporaryFile("w", suffix=".md") as handle:
        handle.write(body)
        handle.flush()
        url = gh(
            [
                "issue", "create", "--repo", repo, "--title", title,
                "--body-file", handle.name, "--label", label,
            ]
        ).strip()
    match = re.search(r"/issues/(\d+)$", url)
    if not match:
        raise RequestError(f"cannot read the new issue number from {url!r}")
    return int(match.group(1))


def close_superseded(repo: str, numbers: list[int], new_number: int, gh=run_gh) -> None:
    for number in numbers:
        gh(
            [
                "issue", "close", str(number), "--repo", repo, "--reason", "not planned",
                "--comment", f"Superseded by #{new_number}.",
            ]
        )


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.split("\n", 1)[0])
    parser.add_argument("--event-file", type=Path, required=True)
    parser.add_argument("--event-name", required=True)
    parser.add_argument("--repo", required=True, help="owner/name that receives the issue")
    parser.add_argument("--root", type=Path, default=REPO_ROOT)
    parser.add_argument("--sources", type=Path, default=SOURCES_PATH)
    parser.add_argument("--receiver-run-url")
    parser.add_argument(
        "--dry-run", action="store_true", help="print the issue instead of calling gh"
    )
    args = parser.parse_args(argv)

    try:
        sources = load_sources(args.sources)
        event = json.loads(args.event_file.read_text(encoding="utf-8"))
        request = validate_request(parse_request(event, args.event_name), sources)
        summary = resolve(args.root, request, load_helper())
        if not summary["changed"]:
            print(f"proj-{request.project}: every requested image is already pinned")
            return 0
        title = render_title(request, summary)
        body = render_body(request, summary, args.receiver_run_url)
        if args.dry_run:
            print(title)
            print(body)
            return 0
        label = sources.get("label", "image-bump")
        duplicate, supersede = plan(request, list_open_issues(args.repo, label, gh=run_gh))
        if duplicate is not None:
            print(f"#{duplicate} already requests {request.sha[:7]}")
            number = duplicate
        else:
            number = open_issue(args.repo, label, title, body, gh=run_gh)
        # Open first, then close: a failed close is retried by the next run.
        close_superseded(args.repo, supersede, number, gh=run_gh)
        print(f"request #{number}; superseded {supersede or 'none'}")
    except RequestError as exc:
        print(f"::error::{exc}", file=sys.stderr)
        return 2
    return 0


if __name__ == "__main__":
    sys.exit(main())
