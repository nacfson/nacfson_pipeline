import json
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]


class TestReviewConfigAndRulebooks(unittest.TestCase):
    def test_claude_md_points_to_agents_and_review_md(self):
        text = (ROOT / "CLAUDE.md").read_text(encoding="utf-8")
        self.assertIn("AGENTS.md", text)
        self.assertIn("REVIEW.md", text)

    def test_review_md_defines_severities_failure_modes_and_caps(self):
        text = (ROOT / "REVIEW.md").read_text(encoding="utf-8")
        for required in (
            "Important",
            "Security",
            "Nit",
            "Pre-existing",
            "file:line",
            "deploy/platform/cert-manager/upstream/",
            "deploy/platform/cni/upstream/",
            "gotk-components.yaml",
            "at most 2",
        ):
            self.assertIn(required, text)

    def test_review_policy_json_schema(self):
        policy = json.loads((ROOT / "config/review-policy.json").read_text(encoding="utf-8"))
        self.assertEqual(policy["max_nits"], 2)
        self.assertEqual(policy["max_rounds"], 3)
        self.assertIn("nacfson", policy["trusted_authors"])
        self.assertIn("gateway/**", policy["high_paths"])
        self.assertIn("deploy/platform/cni/upstream/**", policy["skip_paths"])

    def test_circuit_breaker_json_schema(self):
        cb = json.loads((ROOT / "config/review-circuit-breaker.json").read_text(encoding="utf-8"))
        self.assertEqual(cb["state"], "CLOSED")
        self.assertEqual(cb["recent_incidents"], 0)
        self.assertEqual(cb["max_recent_incidents"], 2)
        self.assertEqual(cb["incidents"], [])


if __name__ == "__main__":
    unittest.main()
