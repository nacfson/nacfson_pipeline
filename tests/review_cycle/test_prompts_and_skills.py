import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]

PROMPTS = (
    "diff-simplifier.md",
    "e2e-behavior.md",
    "bug-reviewer.md",
    "security-reviewer.md",
    "contract-reviewer.md",
    "finding-verifier.md",
    "risk-classifier.md",
)

SKILLS = (
    "code-review",
    "security-review",
    "pre-pr-verify",
    "explain-slice",
)


class TestPromptsAndSkills(unittest.TestCase):
    def test_all_seven_subagent_prompts_exist_and_reference_review_md(self):
        for name in PROMPTS:
            path = ROOT / ".agents" / "review-prompts" / name
            self.assertTrue(path.is_file(), f"Missing prompt {path}")
            text = path.read_text(encoding="utf-8")
            self.assertIn("REVIEW.md", text)
            self.assertIn("line_start", text)

    def test_all_four_agent_skills_have_valid_frontmatter_and_subagent_flow(self):
        for skill in SKILLS:
            path = ROOT / ".agents" / "skills" / skill / "SKILL.md"
            self.assertTrue(path.is_file(), f"Missing skill {path}")
            text = path.read_text(encoding="utf-8")
            self.assertTrue(text.startswith("---\n"))
            self.assertIn(f"name: {skill}", text)
            if skill != "explain-slice":
                self.assertIn("scripts/review-cycle.py", text)
                self.assertIn("invoke_subagent", text)

    def test_omp_and_opencode_command_wrappers_exist(self):
        for cmd in ("code.review.md", "security.review.md", "pre.pr.md", "explain.slice.md"):
            self.assertTrue((ROOT / ".omp" / "commands" / cmd).is_file(), f"Missing omp {cmd}")
        for cmd in ("code-review.md", "security-review.md", "pre-pr.md", "explain-slice.md"):
            self.assertTrue((ROOT / ".opencode" / "commands" / cmd).is_file(), f"Missing opencode {cmd}")
