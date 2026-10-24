import importlib.util
import json
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "scripts" / "review-cycle.py"


def _load_engine():
    spec = importlib.util.spec_from_file_location("review_cycle", SCRIPT)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


class TestReviewEngine(unittest.TestCase):
    def setUp(self):
        self.policy = json.loads((ROOT / "config/review-policy.json").read_text(encoding="utf-8"))
        self.breaker = json.loads((ROOT / "config/review-circuit-breaker.json").read_text(encoding="utf-8"))

    def test_empty_diff_passes_gate_without_receipt(self):
        eng = _load_engine()
        with tempfile.TemporaryDirectory() as tmp:
            ok, errs = eng.check_gate(
                "staged",
                Path(tmp) / "receipt.json",
                diff_text_override="",
                changed_files_override=[],
            )
            self.assertTrue(ok)
            self.assertEqual(errs, [])

    def test_open_circuit_breaker_and_untrusted_author_escalate_low_risk(self):
        eng = _load_engine()
        low = eng.classify_risk(["README.md"], self.policy, self.breaker, author="nacfson")
        self.assertEqual(low["effective_tier"], "Low")
        self.assertEqual(low["required_human_approvals"], 0)
        self.assertEqual(low["review_depth"], "Quick")

        open_cb = dict(self.breaker, state="OPEN")
        escalated_cb = eng.classify_risk(["README.md"], self.policy, open_cb, author="nacfson")
        self.assertEqual(escalated_cb["effective_tier"], "Medium")
        self.assertEqual(escalated_cb["required_human_approvals"], 1)

        untrusted = eng.classify_risk(["README.md"], self.policy, self.breaker, author="outsider")
        self.assertEqual(untrusted["effective_tier"], "Medium")
        self.assertEqual(untrusted["required_human_approvals"], 1)

        high = eng.classify_risk(["gateway/main.go"], self.policy, self.breaker, author="nacfson")
        self.assertEqual(high["effective_tier"], "High")
        self.assertEqual(high["required_human_approvals"], 2)
        self.assertEqual(high["review_depth"], "Deep")

    def test_skipped_paths_and_missing_line_evidence_are_filtered(self):
        eng = _load_engine()
        with tempfile.TemporaryDirectory() as tmp:
            r_path = Path(tmp) / "receipt.json"
            s_path = Path(tmp) / "state.json"
            payload = {
                "agent_harness": "antigravity",
                "verification": {"passed": True, "commands_run": ["pytest"]},
                "findings": [
                    {
                        "id": "F-VENDOR",
                        "category": "bug",
                        "severity": "Important",
                        "file": "deploy/platform/cni/upstream/cilium.yaml",
                        "line_start": 10,
                        "line_end": 12,
                        "title": "Vendor issue",
                        "evidence": "upstream line",
                    },
                    {
                        "id": "F-NOLINE",
                        "category": "bug",
                        "severity": "Important",
                        "file": "scripts/foo.py",
                        "title": "Guessed from name",
                        "evidence": "no line numbers",
                    },
                    {
                        "id": "F-STYLE",
                        "category": "style",
                        "severity": "Important",
                        "file": "scripts/foo.py",
                        "line_start": 1,
                        "line_end": 1,
                        "title": "Formatting",
                        "evidence": "style only",
                    },
                    {
                        "id": "F-NIT1",
                        "category": "nit",
                        "severity": "Nit",
                        "file": "scripts/foo.py",
                        "line_start": 2,
                        "line_end": 2,
                        "title": "Nit 1",
                        "evidence": "line 2",
                    },
                    {
                        "id": "F-NIT2",
                        "category": "nit",
                        "severity": "Nit",
                        "file": "scripts/foo.py",
                        "line_start": 3,
                        "line_end": 3,
                        "title": "Nit 2",
                        "evidence": "line 3",
                    },
                    {
                        "id": "F-NIT3",
                        "category": "nit",
                        "severity": "Nit",
                        "file": "scripts/foo.py",
                        "line_start": 4,
                        "line_end": 4,
                        "title": "Nit 3 dropped by cap",
                        "evidence": "line 4",
                    },
                ],
            }
            receipt = eng.validate_and_record_receipt(
                payload, r_path, s_path, "sha-1", ["scripts/foo.py"], self.policy, self.breaker
            )
            self.assertEqual(receipt["gate_status"], "PASS")
            self.assertEqual(receipt["summary"]["important_open"], 0)
            self.assertEqual(receipt["summary"]["nit_count"], 2)
            self.assertIn("Nothing to fix.", eng.format_markdown_summary(receipt))

    def test_stale_diff_sha_blocks_gate(self):
        eng = _load_engine()
        with tempfile.TemporaryDirectory() as tmp:
            r_path = Path(tmp) / "receipt.json"
            s_path = Path(tmp) / "state.json"
            diff_a = eng.compute_diff_sha256("+line1\n")
            eng.validate_and_record_receipt(
                {"verification": {"passed": True, "commands_run": ["ok"]}, "findings": []},
                r_path,
                s_path,
                diff_a,
                ["scripts/foo.py"],
                self.policy,
                self.breaker,
            )
            ok, _ = eng.check_gate(
                "staged", r_path, diff_text_override="+line1\n", changed_files_override=["scripts/foo.py"]
            )
            self.assertTrue(ok)
            ok2, errs2 = eng.check_gate(
                "staged", r_path, diff_text_override="+line1_modified\n", changed_files_override=["scripts/foo.py"]
            )
            self.assertFalse(ok2)
            self.assertTrue(any("diff_sha256 mismatch" in e for e in errs2))

    def test_commit_gate_requires_code_review_pipeline_field(self):
        eng = _load_engine()
        with tempfile.TemporaryDirectory() as tmp:
            r_path = Path(tmp) / "receipt.json"
            s_path = Path(tmp) / "state.json"
            diff = "+line1\n"
            diff_sha = eng.compute_diff_sha256(diff)
            eng.validate_and_record_receipt(
                {"verification": {"passed": True, "commands_run": ["ok"]}, "findings": []},
                r_path,
                s_path,
                diff_sha,
                ["scripts/foo.py"],
                self.policy,
                self.breaker,
            )
            ok, errs = eng.check_gate(
                "staged",
                r_path,
                diff_text_override=diff,
                changed_files_override=["scripts/foo.py"],
                require_pipeline="code-review",
            )
            self.assertFalse(ok)
            self.assertTrue(any("code-review skill" in e for e in errs))
            self.assertTrue(any("Do not self-record" in e for e in errs))

            eng.validate_and_record_receipt(
                {
                    "pipeline": "code-review",
                    "verification": {"passed": True, "commands_run": ["ok"]},
                    "findings": [],
                },
                r_path,
                s_path,
                diff_sha,
                ["scripts/foo.py"],
                self.policy,
                self.breaker,
            )
            ok2, errs2 = eng.check_gate(
                "staged",
                r_path,
                diff_text_override=diff,
                changed_files_override=["scripts/foo.py"],
                require_pipeline="code-review",
            )
            self.assertTrue(ok2, errs2)

    def test_third_round_with_open_important_escalates_to_human(self):
        eng = _load_engine()
        with tempfile.TemporaryDirectory() as tmp:
            r_path = Path(tmp) / "receipt.json"
            s_path = Path(tmp) / "state.json"
            finding = {
                "id": "F-1",
                "category": "bug",
                "severity": "Important",
                "class_of_bug": "nil-deref",
                "file": "gateway/main.go",
                "line_start": 20,
                "line_end": 25,
                "title": "Nil pointer dereference",
                "evidence": "gateway/main.go:20-25",
            }
            for idx in (1, 2, 3):
                receipt = eng.validate_and_record_receipt(
                    {"verification": {"passed": True, "commands_run": []}, "findings": [finding]},
                    r_path,
                    s_path,
                    f"sha-{idx}",
                    ["gateway/main.go"],
                    self.policy,
                    self.breaker,
                )
            self.assertEqual(receipt["round"], 3)
            self.assertEqual(receipt["re_review_mode"], "diff_only")
            self.assertEqual(receipt["gate_status"], "ESCALATE_TO_HUMAN")

    def test_resolve_finding_clears_block_and_suppresses_repeat(self):
        eng = _load_engine()
        with tempfile.TemporaryDirectory() as tmp:
            r_path = Path(tmp) / "receipt.json"
            s_path = Path(tmp) / "state.json"
            finding = {
                "id": "F-SEC1",
                "category": "security",
                "severity": "Security",
                "file": "gateway/main.go",
                "line_start": 40,
                "line_end": 42,
                "title": "Potential redirect",
                "evidence": "gateway/main.go:40-42",
            }
            r1 = eng.validate_and_record_receipt(
                {"verification": {"passed": True, "commands_run": []}, "findings": [finding]},
                r_path,
                s_path,
                "sha-1",
                ["gateway/main.go"],
                self.policy,
                self.breaker,
            )
            self.assertEqual(r1["gate_status"], "BLOCKED")
            r_resolved = eng.resolve_finding(r_path, s_path, "F-SEC1", "false_positive", "Validated host allowlist")
            self.assertEqual(r_resolved["gate_status"], "PASS")
            # Next round with same finding should suppress it as already resolved
            r2 = eng.validate_and_record_receipt(
                {"verification": {"passed": True, "commands_run": []}, "findings": [finding]},
                r_path,
                s_path,
                "sha-2",
                ["gateway/main.go"],
                self.policy,
                self.breaker,
            )
            self.assertEqual(r2["gate_status"], "PASS")
            self.assertEqual(r2["summary"]["security_open"], 0)

    def test_independent_clean_commits_reset_round_to_one(self):
        eng = _load_engine()
        with tempfile.TemporaryDirectory() as tmp:
            r_path = Path(tmp) / "receipt.json"
            s_path = Path(tmp) / "state.json"
            for idx in (1, 2, 3, 4):
                receipt = eng.validate_and_record_receipt(
                    {"verification": {"passed": True, "commands_run": ["unittest"]}, "findings": []},
                    r_path,
                    s_path,
                    f"clean-sha-{idx}",
                    ["scripts/review-cycle.py"],
                    self.policy,
                    self.breaker,
                )
                self.assertEqual(receipt["round"], 1)
                self.assertEqual(receipt["gate_status"], "PASS")

    def test_vault_proxy_manifest_triggers_go_vault_proxies_tests(self):
        eng = _load_engine()
        res = eng.run_domain_verification(
            scope="staged",
            changed_files_override=["deploy/platform/vault-proxies/ai-proxy.yaml"],
            diff_text_override="+replicas: 1\n",
        )
        self.assertTrue(
            any("cd services/vault-proxies && go test -v ./... && go vet ./..." in cmd for cmd in res["commands_run"]),
            f"expected vault-proxies go test in commands_run, got {res['commands_run']}",
        )


if __name__ == "__main__":
    unittest.main()
