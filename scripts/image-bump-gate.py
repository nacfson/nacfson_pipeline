#!/usr/bin/env python3
"""
image-bump-gate.py — Claude Code PreToolUse hook that holds publishing for approval.

Active only when `IMAGE_BUMP_GATE=1` is set, which the cloud environment used by
the image-bump decision task sets (docs/image-bump-dispatch.md). That session
is started by issues anyone can open, so in gate mode the hook allows only what
the `image-bump-decision` skill needs and returns permissionDecision "ask" for
everything else:

  - GitHub connector tools: reads pass, plus issue state changes and the
    skill's fixed comments on this repository; every other tool (PRs, pushes,
    merges, branches, files, other comments) asks.
  - Write / Edit / NotebookEdit and every tool not in UNGATED_TOOLS ask; the
    skill edits files only through the pin helper.
  - Bash: the command is split on `;`, `&&`, `||`, `|` and newlines, and every
    part must fully match one of the skill's own command shapes
    (ALLOWED_SEGMENTS), with refs, paths and arguments pinned. Redirections,
    heredocs and command substitution always ask (the code-review receipt
    step therefore prompts once).

Approving the prompt for `git push` and the PR is the "Allow"; denying it is
"Disallow". The fired task prompt and the issue text can never stand in for it.

Everywhere else the hook prints nothing and exits 0, so ordinary sessions are
unchanged.
"""

from __future__ import annotations

import json
import os
import re
import sys

GITHUB_PREFIX = "mcp__github__"
UNGATED_GITHUB_PREFIXES = ("get_", "list_", "search_")
UNGATED_GITHUB_TOOLS = {"issue_read", "pull_request_read"}
# Issue writes are the only GitHub writes without a prompt, and only the exact
# ones the skill makes on this repository: a state change, or one of its fixed
# comments. Anything else could carry data out (for example a secret or
# private source pasted into a comment), so it asks.
HOME_REPO = ("nacfson", "nacfson_pipeline")
ISSUE_STATE_KEYS = {"method", "owner", "repo", "issue_number", "state", "state_reason"}
FIXED_COMMENTS = {
    "Disallowed by the owner; nothing was pinned.",
    "No decision recorded; request left open.",
    "Already pinned; nothing to do.",
}
PIN_PR_COMMENT = re.compile(r"Pin PR: https://github\.com/nacfson/nacfson_pipeline/pull/\d+")
# Every other tool asks in gate mode (file writes, other connectors, session
# or schedule tools), so a tool added later is gated until listed here.
UNGATED_TOOLS = {
    "Read", "Grep", "Glob", "AskUserQuestion", "PushNotification", "ToolSearch",
    "Skill", "Agent", "TaskCreate", "TaskUpdate", "TaskList", "TaskGet",
}

# Harmless redirections are removed before the "no redirection" check.
SAFE_REDIRECTS = re.compile(r"\s*(?:2>&1|2?>\s*/dev/null)(?=\s|$)")
UNSAFE_SHELL = re.compile(r"\$\(|`|<\(|>\(|>|<<")
SEPARATORS = re.compile(r"\|\||&&|[;|\n]")
# Each shape is pinned to what the skill runs: fixed refs, the worktree path
# pattern, in-repo test directories, and the helper's exact arguments. A free
# argument anywhere (a remote URL, a ref, a test directory, --source,
# --output) could fetch and run code that the hook never sees.
_SLUG = r"[a-z0-9][a-z0-9-]*"
_WORKTREE = rf"\.worktrees/images-{_SLUG}-[0-9a-f]{{7}}"
_DEPLOY_PATH = rf"deploy/projects/{_SLUG}/workloads/[\w.-]+\.ya?ml|docs/platform-architecture\.html"
_COMMIT_LINK = r"https://github\.com/[\w.-]+/[\w.-]+/commit/[0-9a-f]{40}"
ALLOWED_SEGMENTS = [
    re.compile(pattern)
    for pattern in (
        rf"cd (?:{_WORKTREE}|\.\./\.\.)",
        r"(?:ls|pwd|true)(?: [\w./-]+)*",
        r"(?:cat|head|tail|wc) (?:-n \d+ )?(?!\.\.)\w[\w.-]*(?:/(?!\.\.)[\w.-]+)*",
        r"git fetch(?: -q)? origin main",
        rf"git worktree add {_WORKTREE} -b claude/images-{_SLUG}-[0-9a-f]{{7}} origin/main",
        rf"git worktree remove(?: --force)? {_WORKTREE}",
        rf"git add (?:(?:{_DEPLOY_PATH}) ?)+",
        r"git commit -m '[^'\\]*'",
        r"git status(?: --short)?",
        r"git diff (?:--cached(?: --name-only| --stat)?|origin/main\.\.\.HEAD(?: --stat)?)",
        r"python3 \.agents/skills/update-project-images/scripts/update_project_images\.py"
        rf" --root \. --project {_SLUG}(?: --image {_SLUG}={_COMMIT_LINK})+"
        rf"(?: --dry-run)? --summary-file /tmp/image-bump-{_SLUG}\.json",
        r"python3 scripts/check-arch-doc-sync\.py --worktree",
        r"python3 scripts/review-cycle\.py (?:verify|classify-risk|check-gate)(?: --[a-z-]+(?: [\w-]+)?)*",
        r"python3 -m unittest discover (?:tests/preflight|tests/hooks|-s tests/skills)",
        r"\./scripts/render-validate\.sh (?:local|vps)-k3s",
        r"skopeo login ghcr\.io -u nacfson --password-stdin",
        r"command -v skopeo",
        r"(?:sudo )?apt-get (?:update|install -y skopeo)",
    )
]
# The private-package login is allowed only as this whole command, so the
# token is never printed on its own.
GHCR_LOGIN = (
    '[ -n "${GHCR_READ_TOKEN:-}" ] && echo "$GHCR_READ_TOKEN" '
    "| skopeo login ghcr.io -u nacfson --password-stdin"
)
REASON = (
    "image-bump gate: this action is outside the approved image-bump steps or "
    "publishes code, so it needs your approval. Approve to allow; deny to disallow."
)


def enabled(env=os.environ) -> bool:
    return str(env.get("IMAGE_BUMP_GATE", "")).strip().lower() in ("1", "true", "yes")


def command_allowed(command: str) -> bool:
    if " ".join(command.split()) == GHCR_LOGIN:
        return True
    command = SAFE_REDIRECTS.sub("", command.replace("\\\n", " "))
    if UNSAFE_SHELL.search(command):
        return False
    for segment in SEPARATORS.split(command):
        segment = " ".join(segment.split()).strip("{}() ").strip()
        if not segment:
            continue
        if not any(pattern.fullmatch(segment) for pattern in ALLOWED_SEGMENTS):
            return False
    return True


def github_write_allowed(name: str, tool_input: dict) -> bool:
    if (str(tool_input.get("owner", "")).lower(), str(tool_input.get("repo", "")).lower()) != HOME_REPO:
        return False
    if name == "issue_write":
        return set(tool_input) <= ISSUE_STATE_KEYS and tool_input.get("method") == "update"
    if name == "add_issue_comment":
        body = str(tool_input.get("body", "")).strip()
        return body in FIXED_COMMENTS or bool(PIN_PR_COMMENT.fullmatch(body))
    return False


def decide(payload: dict, env=os.environ) -> dict | None:
    """Return the hook output for `payload`, or None to stay silent."""
    if not enabled(env):
        return None
    tool = str(payload.get("tool_name") or "")
    tool_input = payload.get("tool_input")
    if not isinstance(tool_input, dict):
        tool_input = {}
    if tool.startswith(GITHUB_PREFIX):
        name = tool[len(GITHUB_PREFIX):]
        gated = not (
            name in UNGATED_GITHUB_TOOLS
            or name.startswith(UNGATED_GITHUB_PREFIXES)
            or github_write_allowed(name, tool_input)
        )
    elif tool == "Bash":
        gated = not command_allowed(str(tool_input.get("command") or ""))
    else:
        gated = tool not in UNGATED_TOOLS
    if not gated:
        return None
    return {
        "hookSpecificOutput": {
            "hookEventName": "PreToolUse",
            "permissionDecision": "ask",
            "permissionDecisionReason": REASON,
        }
    }


def main() -> int:
    try:
        payload = json.load(sys.stdin)
    except (json.JSONDecodeError, ValueError):
        payload = {}
    result = decide(payload if isinstance(payload, dict) else {})
    if result is not None:
        print(json.dumps(result))
    return 0


if __name__ == "__main__":
    sys.exit(main())
