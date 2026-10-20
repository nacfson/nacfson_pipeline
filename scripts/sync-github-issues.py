#!/usr/bin/env python3
"""Refresh docs/issues/index.md from GitHub without rewriting FIX bodies."""

import json
import os
import re
import subprocess
import sys
import tempfile
from pathlib import Path

# shortcut: --limit 100 drops later issues; their rows stay, raise it past 100 issues
GH_CMD = [
    "gh",
    "issue",
    "list",
    "--repo",
    "nacfson/nacfson_pipeline",
    "--state",
    "all",
    "--limit",
    "100",
    "--json",
    "number,title,state",
]
ISSUE_URL = "https://github.com/nacfson/nacfson_pipeline/issues/{number}"
# shortcut: a title containing | or a newline splits the row, escape titles if one shows up
ISSUE_CELL = re.compile(r"\[#(\d+)\]\(([^)]+)\)\s*(.*)\Z")


class ApplyResult:
    def __init__(self, index_text, stubs):
        self.index_text = index_text
        self.stubs = stubs


def _stub(number, state, title):
    return (
        f"# FIX_GithubIssue{number}\n"
        "\n"
        f"- Issue: {ISSUE_URL.format(number=number)}\n"
        f"- State: {state}. Created by scripts/sync-github-issues.py. Not analyzed.\n"
        "- Live contract: Not checked.\n"
        "\n"
        "## What is wrong\n"
        "\n"
        f"{title}\n"
        "\n"
        "## Not checked\n"
        "\n"
        "This file was created on project open. It does not replace a human analysis.\n"
    )


def _cells(line):
    text = line.strip()
    if not text.startswith("|"):
        return None
    parts = [part.strip() for part in text.strip("|").split("|")]
    if not parts or not ISSUE_CELL.match(parts[0]):
        return None
    return parts


def _render(cells):
    return "| " + " | ".join(cells) + " |"


def _new_row(item):
    number = int(item["number"])
    name = f"FIX_GithubIssue{number}.md"
    return _render(
        [
            f"[#{number}]({ISSUE_URL.format(number=number)}) {item['title']}",
            str(item["state"]),
            f"[`{name}`]({name})",
            "Not checked.",
        ]
    )


def apply_issues(index_text, issues, existing_fix_names):
    marker = "| Issue |"
    at = index_text.find(marker)
    if at < 0:
        raise ValueError("index has no issue table")
    line_start = index_text.rfind("\n", 0, at)
    if line_start < 0:
        preamble, rest = "", index_text
    else:
        preamble, rest = index_text[: line_start + 1], index_text[line_start + 1 :]

    by_number = {int(item["number"]): item for item in issues}
    seen = set()
    out = []
    last_table = -1
    for line in rest.splitlines():
        cells = _cells(line)
        number = int(ISSUE_CELL.match(cells[0]).group(1)) if cells else None
        if number is not None and number in by_number:
            item = by_number[number]
            match = ISSUE_CELL.match(cells[0])
            if cells[1] != item["state"] or match.group(3) != item["title"]:
                cells[0] = f"[#{number}]({match.group(2)}) {item['title']}"
                cells[1] = str(item["state"])
                line = _render(cells)
            seen.add(number)
        out.append(line)
        if line.startswith("|"):
            last_table = len(out) - 1

    added = []
    for item in issues:
        number = int(item["number"])
        if number in seen:
            continue
        seen.add(number)
        added.append(_new_row(item))
    if added:
        out[last_table + 1 : last_table + 1] = added

    body = "\n".join(out)
    if rest.endswith("\n") or out:
        body += "\n"

    stubs = {}
    for item in issues:
        name = f"FIX_GithubIssue{int(item['number'])}.md"
        if name in existing_fix_names or name in stubs:
            continue
        stubs[name] = _stub(int(item["number"]), item["state"], item["title"])
    return ApplyResult(preamble + body, stubs)


def _replace(path, text):
    fd, tmp_name = tempfile.mkstemp(dir=path.parent, prefix=f".{path.name}.")
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            handle.write(text)
    except BaseException:
        os.unlink(tmp_name)
        raise
    os.replace(tmp_name, path)


def main():
    try:
        proc = subprocess.run(GH_CMD, capture_output=True, text=True, encoding="utf-8")
    except OSError:
        return 1
    if proc.returncode != 0:
        if proc.stderr:
            sys.stderr.write(proc.stderr)
        return 1
    try:
        payload = json.loads(proc.stdout)
        if not isinstance(payload, list):
            raise ValueError("gh json is not a list")
        issues = [
            {
                "number": int(item["number"]),
                "title": str(item["title"]),
                "state": str(item["state"]),
            }
            for item in payload
        ]
    except (json.JSONDecodeError, KeyError, TypeError, ValueError):
        return 1

    # cwd is the tree to update. The plugin sets it; the failure test points it at a copy.
    issues_dir = Path.cwd() / "docs" / "issues"
    index_path = issues_dir / "index.md"
    if not index_path.is_file():
        return 1
    existing = {path.name for path in issues_dir.glob("FIX_GithubIssue*.md")}
    result = apply_issues(index_path.read_text(encoding="utf-8"), issues, existing)
    _replace(index_path, result.index_text)
    for name, text in result.stubs.items():
        _replace(issues_dir / name, text)
    return 0


if __name__ == "__main__":
    sys.exit(main())
