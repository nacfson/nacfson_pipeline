#!/usr/bin/env python3
"""
universal-agent-hook.py — Multi-Agent Lifecycle Hook Adapter for nacfson_pipeline.

Enforces that `docs/platform-architecture.html` stays synchronized whenever
architecture-affecting files are modified across all major AI coding agents:
  - Antigravity CLI / IDE / 2.0 (.agents/hooks.json)
  - Claude Code (.claude/settings.json)
  - OpenAI Codex CLI (.codex/hooks.json)
  - Grok Build (.grok/hooks.json)
  - Cursor (.cursor/hooks.json)
  - Oh-my-pi (.omp/extensions/arch-doc-sync.ts calls this script)
  - OpenCode (.opencode/plugins/arch-doc-sync.js calls this script)
"""

from __future__ import annotations

import argparse
import importlib.util
import json
import sys
from pathlib import Path

SCRIPT_DIR = Path(__file__).resolve().parent
_spec = importlib.util.spec_from_file_location(
    "check_arch_doc_sync", SCRIPT_DIR / "check-arch-doc-sync.py"
)
sync_checker = importlib.util.module_from_spec(_spec)
assert _spec and _spec.loader
_spec.loader.exec_module(sync_checker)


def load_stdin_json() -> dict:
    try:
        if sys.stdin.isatty():
            return {}
        raw = sys.stdin.read().strip()
        if not raw:
            return {}
        return json.loads(raw)
    except Exception:
        return {}


def extract_command(payload: dict) -> str:
    tool_input = payload.get("toolInput") or payload.get("tool_input") or {}
    if isinstance(tool_input, dict):
        for key in ("CommandLine", "command", "cmd", "script"):
            val = tool_input.get(key)
            if isinstance(val, str) and val:
                return val
    cmd = payload.get("command")
    return cmd if isinstance(cmd, str) else ""


def is_git_commit_command(cmd: str) -> bool:
    if not cmd:
        return False
    return "git commit" in cmd or "git-commit" in cmd


def main() -> int:
    parser = argparse.ArgumentParser(description="Universal multi-agent hook adapter.")
    parser.add_argument(
        "--agent",
        choices=["auto", "antigravity", "claude", "codex", "grok", "cursor", "generic"],
        default="auto",
    )
    parser.add_argument(
        "--event",
        choices=["auto", "PreToolUse", "Stop"],
        default="auto",
    )
    args = parser.parse_args()

    payload = load_stdin_json()

    # Detect event name
    event = args.event
    if event == "auto":
        raw_event = (
            payload.get("hookEventName")
            or payload.get("hook_event_name")
            or payload.get("event")
            or "Stop"
        )
        if str(raw_event).lower() in ("pretooluse", "pre_tool_use", "beforeshellexecution"):
            event = "PreToolUse"
        else:
            event = "Stop"

    # Detect agent dialect
    agent = args.agent
    if agent == "auto":
        if "hookEventName" in payload or "toolInput" in payload or "stopHookActive" in payload:
            agent = "antigravity"
        elif "hook_event_name" in payload or "tool_input" in payload or "stop_hook_active" in payload:
            agent = "claude"
        else:
            agent = "generic"

    # Prevent infinite Stop-hook loops if agent already re-prompted once
    if event == "Stop" and (
        payload.get("stopHookActive") is True or payload.get("stop_hook_active") is True
    ):
        print(json.dumps({"decision": "allow"}))
        return 0

    # For PreToolUse, only gate `git commit` commands so normal exploration/test commands run freely
    if event == "PreToolUse":
        cmd = extract_command(payload)
        if not is_git_commit_command(cmd):
            print(json.dumps({"decision": "allow"}))
            return 0
        errors = sync_checker.check_static_invariants()
        staged_errs = sync_checker.check_git_co_update("staged")
        worktree_errs = sync_checker.check_git_co_update("worktree")
        errors.extend(staged_errs or worktree_errs)
    else:
        # Stop event: verify static invariants + working tree co-update
        errors = sync_checker.check_static_invariants()
        errors.extend(sync_checker.check_git_co_update("worktree"))

    if not errors:
        print(json.dumps({"decision": "allow"}))
        return 0

    reason = "[ARCH-DOC-SYNC REQUIRED] " + " | ".join(errors)

    if agent == "antigravity":
        if event == "PreToolUse":
            print(json.dumps({"decision": "deny", "reason": reason, "systemMessage": reason}))
        else:
            print(json.dumps({"decision": "continue", "reason": reason, "systemMessage": reason}))
        return 0

    if agent in ("claude", "codex", "grok", "cursor"):
        print(json.dumps({"decision": "block", "reason": reason}))
        return 2 if event == "PreToolUse" else 0

    # Generic fallback
    print(json.dumps({"ok": False, "decision": "block", "reason": reason}))
    print(reason, file=sys.stderr)
    return 1


if __name__ == "__main__":
    sys.exit(main())
