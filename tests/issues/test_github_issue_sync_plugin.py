import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]


class GithubIssueSyncPluginTest(unittest.TestCase):
    def test_plugin_spawns_sync_and_swallows_failure(self):
        text = (ROOT / ".opencode/plugins/github-issue-sync/index.ts").read_text()
        assert 'id: "nacfson-github-issue-sync"' in text
        assert "scripts/sync-github-issues.py" in text
        assert "ctx.location.project.canonical" in text
        assert "catch" in text


if __name__ == "__main__":
    unittest.main()
