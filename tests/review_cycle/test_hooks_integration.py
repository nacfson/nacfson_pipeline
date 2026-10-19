import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
HOOK_SCRIPT = ROOT / "scripts" / "universal-agent-hook.py"


class TestHooksIntegration(unittest.TestCase):
    def test_hook_allows_clean_worktree(self):
        proc = subprocess.run(
            ["python3", str(HOOK_SCRIPT), "--agent", "antigravity", "--event", "PreToolUse"],
            input=json.dumps({"toolInput": {"CommandLine": "git status"}}),
            text=True,
            capture_output=True,
            cwd=ROOT,
            check=False,
        )
        self.assertEqual(proc.returncode, 0)
        data = json.loads(proc.stdout.strip())
        self.assertEqual(data["decision"], "allow")

    def test_hook_blocks_git_commit_when_staged_receipt_missing(self):
        with tempfile.TemporaryDirectory() as tmp:
            env = dict(os.environ, REVIEW_RECEIPT_PATH=str(Path(tmp) / "missing.json"), REVIEW_FORCE_CHANGED="scripts/review-cycle.py", REVIEW_FORCE_DIFF="+x\n")
            proc = subprocess.run(
                ["python3", str(HOOK_SCRIPT), "--agent", "antigravity", "--event", "PreToolUse"],
                input=json.dumps({"toolInput": {"CommandLine": "git commit -m 'test'"}}),
                text=True,
                capture_output=True,
                cwd=ROOT,
                env=env,
                check=False,
            )
            data = json.loads(proc.stdout.strip())
            self.assertEqual(data["decision"], "deny")
            self.assertIn("REVIEW-CYCLE", data["reason"])

    def test_omp_opencode_and_precommit_call_review_cycle_check_gate(self):
        omp_text = (ROOT / ".omp/extensions/arch-doc-sync.ts").read_text(encoding="utf-8")
        oc_text = (ROOT / ".opencode/plugins/arch-doc-sync.js").read_text(encoding="utf-8")
        pc_text = (ROOT / ".githooks/pre-commit").read_text(encoding="utf-8")
        self.assertIn("review-cycle.py", omp_text)
        self.assertIn("review-cycle.py", oc_text)
        self.assertIn("review-cycle.py check-gate --scope staged", pc_text)
        self.assertIn("SKIP_REVIEW_GATE", pc_text)
