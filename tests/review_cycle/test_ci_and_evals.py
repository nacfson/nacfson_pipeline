import os
import re
import subprocess
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]


class TestCiGovernanceAndEvals(unittest.TestCase):
    def test_pr_template_and_codeowners_exist(self):
        pr_tpl = (ROOT / ".github/PULL_REQUEST_TEMPLATE.md").read_text(encoding="utf-8")
        for heading in (
            "## Intent",
            "## Testing",
            "## Risk",
            "## How to Revert",
            "## Automated Review Receipt",
        ):
            self.assertIn(heading, pr_tpl)
        codeowners = (ROOT / ".github/CODEOWNERS").read_text(encoding="utf-8")
        for protected in (
            "/gateway/",
            "/deploy/platform/",
            "/.github/workflows/",
            "/.specify/memory/constitution.md",
        ):
            self.assertIn(protected, codeowners)

    def test_workflows_include_merge_group_and_review_gate(self):
        for wf in ("lint.yaml", "gitops.yaml", "review-gate.yaml"):
            text = (ROOT / ".github/workflows" / wf).read_text(encoding="utf-8")
            self.assertIsNotNone(
                re.search(r"^\s*merge_group\s*:", text, re.MULTILINE),
                f"{wf} missing merge_group trigger",
            )
        rg_text = (ROOT / ".github/workflows/review-gate.yaml").read_text(encoding="utf-8")
        self.assertIsNotNone(
            re.search(r"^\s*schedule\s*:", rg_text, re.MULTILINE),
            "review-gate.yaml missing schedule trigger",
        )
        self.assertIn("0 3 * * *", rg_text)
        self.assertIn("python3 -m unittest discover tests/review_cycle", rg_text)
        self.assertIn("python3 scripts/review-cycle.py eval", rg_text)
        self.assertIn("python3 scripts/review-cycle.py classify-risk --scope branch", rg_text)

    def test_eval_suite_runs_and_scores_100_percent(self):
        proc = subprocess.run(
            ["python3", str(ROOT / "scripts/review-cycle.py"), "eval", "--json"],
            text=True,
            capture_output=True,
            cwd=ROOT,
            check=False,
        )
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertIn('"recall": 1.0', proc.stdout)
        self.assertIn('"false_positive_rate": 0.0', proc.stdout)
        self.assertIn('"must_flag_total": 2', proc.stdout)
        self.assertIn('"must_pass_total": 2', proc.stdout)

    def test_rollback_script_dry_run(self):
        script = ROOT / "scripts/rollback-change.sh"
        self.assertTrue(script.is_file())
        self.assertTrue(os.access(script, os.X_OK))
        proc = subprocess.run(
            ["bash", str(script), "--dry-run", "HEAD"],
            text=True,
            capture_output=True,
            cwd=ROOT,
            check=False,
        )
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertIn("DRY-RUN", proc.stdout)
        self.assertIn("SKIP_REVIEW_GATE=1", proc.stdout)


if __name__ == "__main__":
    unittest.main()
