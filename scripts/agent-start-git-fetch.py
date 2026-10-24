#!/usr/bin/env python3
"""
agent-start-git-fetch.py — Non-Mutating Remote Refresh at AI Agent Session Start.

Runs `git fetch` so an agent starts with current remote-tracking refs,
then reports how the current branch diverged from its upstream. It never
merges, rebases, checks out, or otherwise touches the working tree.

Safety contract:
  - Fail open: every failure (offline, timeout, missing remote, not a repo,
    disabled by env) returns a status dict and exit code 0.
  - Non-interactive: `GIT_TERMINAL_PROMPT=0`, SSH `BatchMode=yes`, hard timeout.
  - Throttled: at most one fetch per cooldown window (default 60s), so agents
    that fire SessionStart on resume/compact do not stampede the remote.
  - Opt-out: `AGENT_START_FETCH_DISABLE=1`.

Environment:
  AGENT_START_FETCH_DISABLE     Set to 1/true/yes to skip entirely.
  AGENT_START_FETCH_REMOTE      Remote name; defaults to `origin`.
  AGENT_START_FETCH_TIMEOUT     Fetch timeout seconds; defaults to 12.
  AGENT_START_FETCH_COOLDOWN    Minimum seconds between fetches; defaults to 60.
  AGENT_START_FETCH_PRUNE       Set to 1/true/yes to also prune stale remote refs.
"""

from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
import time
from pathlib import Path

DEFAULT_TIMEOUT_SECONDS = 12
DEFAULT_COOLDOWN_SECONDS = 60
STAMP_NAME = "agent-start-fetch.stamp"
DISABLE_ENV = "AGENT_START_FETCH_DISABLE"

_TRUTHY = {"1", "true", "yes", "on"}


def _resolve_env(env: dict | None) -> dict:
    resolved = dict(os.environ if env is None else env)
    resolved["GIT_TERMINAL_PROMPT"] = "0"
    resolved.setdefault("GIT_SSH_COMMAND", "ssh -o BatchMode=yes -o ConnectTimeout=5")
    resolved.setdefault("GIT_OPTIONAL_LOCKS", "0")
    return resolved


def _int_env(env: dict, name: str, default: int) -> int:
    raw = env.get(name)
    if raw is None:
        return default
    try:
        return max(0, int(raw))
    except (TypeError, ValueError):
        return default


def _git(
    cwd: Path,
    *args: str,
    timeout: float = 10,
    env: dict | None = None,
) -> subprocess.CompletedProcess[str] | None:
    """Run git without a terminal and without ever raising."""
    try:
        return subprocess.run(
            ["git", *args],
            cwd=str(cwd),
            capture_output=True,
            text=True,
            stdin=subprocess.DEVNULL,
            timeout=timeout,
            env=_resolve_env(env),
            check=False,
        )
    except (OSError, subprocess.SubprocessError):
        return None


def _repo_root(start: Path | None = None) -> Path | None:
    cwd = Path(start or os.getcwd()).resolve()
    proc = _git(cwd, "rev-parse", "--show-toplevel", timeout=5)
    if proc is None or proc.returncode != 0:
        return None
    root = proc.stdout.strip()
    return Path(root) if root else None


def _stamp_path(root: Path, env: dict) -> Path:
    proc = _git(root, "rev-parse", "--absolute-git-dir", timeout=5, env=env)
    if proc is not None and proc.returncode == 0 and proc.stdout.strip():
        return Path(proc.stdout.strip()) / STAMP_NAME
    return root / ".git" / STAMP_NAME


def _cooldown_remaining(stamp: Path, cooldown: int) -> float:
    if cooldown <= 0:
        return 0.0
    try:
        age = time.time() - stamp.stat().st_mtime
    except OSError:
        return 0.0
    return max(0.0, cooldown - age)


def _select_remote(root: Path, env: dict) -> str | None:
    preferred = (env.get("AGENT_START_FETCH_REMOTE") or "").strip()
    if preferred:
        return preferred
    proc = _git(root, "remote", timeout=5, env=env)
    remotes = proc.stdout.split() if proc is not None and proc.returncode == 0 else []
    if "origin" in remotes:
        return "origin"
    return remotes[0] if remotes else None


def _upstream_state(
    root: Path, env: dict
) -> tuple[str | None, int | None, int | None]:
    upstream_proc = _git(
        root,
        "rev-parse",
        "--abbrev-ref",
        "--symbolic-full-name",
        "@{upstream}",
        timeout=5,
        env=env,
    )
    if upstream_proc is None or upstream_proc.returncode != 0:
        return None, None, None
    upstream = upstream_proc.stdout.strip()
    if not upstream:
        return None, None, None

    counts_proc = _git(
        root,
        "rev-list",
        "--left-right",
        "--count",
        f"HEAD...{upstream}",
        timeout=10,
        env=env,
    )
    if counts_proc is None or counts_proc.returncode != 0:
        return upstream, None, None
    parts = counts_proc.stdout.split()
    if len(parts) != 2 or not all(part.isdigit() for part in parts):
        return upstream, None, None
    return upstream, int(parts[0]), int(parts[1])


def fetch(
    cwd: str | Path | None = None,
    *,
    timeout: int | None = None,
    cooldown: int | None = None,
    env: dict | None = None,
    force: bool = False,
) -> dict:
    """Fetch the remote and return a JSON-serializable status dict. Never raises."""
    resolved_env = _resolve_env(env)
    timeout = (
        _int_env(resolved_env, "AGENT_START_FETCH_TIMEOUT", DEFAULT_TIMEOUT_SECONDS)
        if timeout is None
        else max(0, timeout)
    )
    cooldown = (
        _int_env(resolved_env, "AGENT_START_FETCH_COOLDOWN", DEFAULT_COOLDOWN_SECONDS)
        if cooldown is None
        else max(0, cooldown)
    )

    result: dict = {
        "status": "skipped",
        "reason": "",
        "message": "",
        "remote": None,
        "upstream": None,
        "ahead": None,
        "behind": None,
        "duration_seconds": 0.0,
    }

    if str(resolved_env.get(DISABLE_ENV, "")).strip().lower() in _TRUTHY:
        result["reason"] = f"{DISABLE_ENV} is set"
        return result

    root = _repo_root(Path(cwd) if cwd is not None else None)
    if root is None:
        result["reason"] = "not inside a git work tree"
        return result

    stamp = _stamp_path(root, resolved_env)
    if not force:
        remaining = _cooldown_remaining(stamp, cooldown)
        if remaining > 0:
            result["reason"] = f"cooldown active ({int(remaining) + 1}s remaining)"
            return result

    remote = _select_remote(root, resolved_env)
    if remote is None:
        result["reason"] = "no git remote configured"
        return result
    result["remote"] = remote

    # Stamp before fetching so concurrent session-start hooks do not stampede.
    try:
        stamp.parent.mkdir(parents=True, exist_ok=True)
        stamp.touch()
    except OSError:
        pass

    started = time.monotonic()
    fetch_args = ["fetch", "--quiet", remote]
    if str(resolved_env.get("AGENT_START_FETCH_PRUNE", "")).strip().lower() in _TRUTHY:
        fetch_args.insert(1, "--prune")
    proc = _git(
        root,
        *fetch_args,
        timeout=timeout,
        env=resolved_env,
    )
    result["duration_seconds"] = round(time.monotonic() - started, 2)

    if proc is None:
        result["status"] = "error"
        result["reason"] = f"git fetch did not finish within {timeout}s"
        result["message"] = (
            f"[git fetch] {remote}: timed out after {timeout}s; "
            "using local refs. Working tree unchanged."
        )
        return result

    if proc.returncode != 0:
        detail = (proc.stderr or proc.stdout).strip().splitlines()
        reason = detail[-1] if detail else f"git fetch exited {proc.returncode}"
        result["status"] = "error"
        result["reason"] = reason
        result["message"] = (
            f"[git fetch] {remote}: failed ({reason}); "
            "using local refs. Working tree unchanged."
        )
        return result

    upstream, ahead, behind = _upstream_state(root, resolved_env)
    result.update(
        status="fetched",
        upstream=upstream,
        ahead=ahead,
        behind=behind,
    )

    if upstream is None:
        result["message"] = (
            f"[git fetch] {remote}: fetched; no upstream tracking branch. "
            "Working tree unchanged."
        )
    elif ahead == 0 and behind == 0:
        result["message"] = (
            f"[git fetch] {remote}: {upstream} is up to date. Working tree unchanged."
        )
    elif ahead is None or behind is None:
        result["message"] = (
            f"[git fetch] {remote}: fetched {upstream}. Working tree unchanged."
        )
    else:
        divergence = []
        if behind:
            divergence.append(f"behind {behind}")
        if ahead:
            divergence.append(f"ahead {ahead}")
        result["message"] = (
            f"[git fetch] {remote}: {upstream} is {' and '.join(divergence)}. "
            "Working tree unchanged; merge or rebase manually."
        )
    return result


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        description="Non-mutating remote refresh for AI agent session start."
    )
    parser.add_argument("--cwd", default=None, help="Repository directory (default: cwd).")
    parser.add_argument("--timeout", type=int, default=None, help="Fetch timeout in seconds.")
    parser.add_argument("--cooldown", type=int, default=None, help="Cooldown in seconds.")
    parser.add_argument("--force", action="store_true", help="Ignore the cooldown window.")
    parser.add_argument(
        "--format",
        choices=("text", "json", "none"),
        default="text",
        help="Output format (default: text).",
    )
    args = parser.parse_args(argv)

    result = fetch(
        args.cwd,
        timeout=args.timeout,
        cooldown=args.cooldown,
        force=args.force,
    )

    if args.format == "json":
        print(json.dumps(result))
    elif args.format == "text" and result["message"] and result["status"] != "skipped":
        print(result["message"])
    return 0


if __name__ == "__main__":
    sys.exit(main())
