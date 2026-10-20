import json
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
CANDIDATES = (
    ROOT / ".opencode" / "opencode.jsonc",
    ROOT / ".opencode" / "opencode.json",
    ROOT / "opencode.jsonc",
    ROOT / "opencode.json",
)


def config_text():
    for path in CANDIDATES:
        if path.is_file():
            return path.read_text(encoding="utf-8")
    raise AssertionError("no project MCP config exists")


def strip_jsonc(text):
    out = []
    i = 0
    in_string = False
    while i < len(text):
        ch = text[i]
        if in_string:
            out.append(ch)
            if ch == "\\" and i + 1 < len(text):
                out.append(text[i + 1])
                i += 2
                continue
            if ch == '"':
                in_string = False
            i += 1
            continue
        if ch == '"':
            in_string = True
            out.append(ch)
            i += 1
            continue
        if text.startswith("//", i):
            i = text.find("\n", i)
            if i < 0:
                break
            continue
        out.append(ch)
        i += 1
    return "".join(out)


class TestOpencodeMcpConfig(unittest.TestCase):
    def test_github_mcp_is_project_local_and_issue_scoped(self):
        text = config_text()
        assert "gho_" not in text and "github_pat_" not in text and "Bearer ghp_" not in text
        data = json.loads(strip_jsonc(text))
        github = data["mcp"]["servers"]["github"]
        assert github["type"] == "remote"
        assert github["url"] == "https://api.githubcopilot.com/mcp/"
        assert github["headers"]["X-MCP-Toolsets"] == "context,issues"
        assert "enabled" not in github
        assert "github" not in data["mcp"] or data["mcp"]["github"] == data["mcp"]["servers"]["github"]
