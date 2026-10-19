# Anthropic-Style End-to-End Build & Code Review Cycle Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the 10-stage Anthropic verification, multi-agent code & security review, importance/risk classification, hook gate, merge queue, rollback circuit breaker, and eval cycle into `nacfson_pipeline`.

**Architecture:** A zero-dependency Python 3.12 engine (`scripts/review-cycle.py`) orchestrates domain verification, two-stage change risk classification (`Low`/`Medium`/`High`), finding verification rules (`Important`/`Security`/`Nit`/`Pre-existing` with a 2-nit cap and 3-round convergence stop), diff-bound JSON receipts (`.git/review-cycle/receipt.json`), and regression evals (`tests/review-evals/`). Antigravity (`agy`) subagents run the parallel review hunters, verifier voter, and exception classifier using portable Markdown prompts (`.agents/review-prompts/`) and cross-agent skills/commands (`.agents/skills/`, `.omp/commands/`, `.opencode/commands/`), enforced locally by `universal-agent-hook.py`, `.omp`, `.opencode`, and `.githooks/pre-commit`, and on GitHub by `review-gate.yaml` and `merge_group` CI.

**Tech Stack:** Python 3.12 standard library (`unittest`, `hashlib`, `json`, `subprocess`, `argparse`, `fnmatch`), Bash, GitHub Actions, Antigravity Subagents / Skills, Oh-my-pi TypeScript extension, OpenCode JS plugin.

**Spec:** `docs/superpowers/specs/2026-10-19-anthropic-review-cycle-design.md`

## Global Constraints

- `scripts/review-cycle.py` and `tests/review_cycle/*.py` MUST use Python 3.12 standard library only (zero third-party pip packages required to run the review engine or hooks).
- Finding severities are exactly `Important`, `Security`, `Nit`, and `Pre-existing`.
- Change risk tiers are exactly `Low` (`0` humans required for trusted authors when circuit breaker is `CLOSED`), `Medium` (`1` human), and `High` (`2` humans / `CODEOWNERS` owner sign-off).
- Review depth modes are `Quick`, `Standard`, and `Deep`.
- Nits are capped at `2` per review receipt and never block commits or merges.
- Automated review-fix rounds are capped at `3` (`max_rounds = 3`). If round `3` still has open `Important` or `Security` findings (or round `> 3` is attempted), `gate_status` MUST be `"ESCALATE_TO_HUMAN"`.
- Every finding recorded in a receipt MUST have non-empty `file`, integer `line_start >= 1`, integer `line_end >= line_start`, and `evidence`.
- Findings targeting skipped vendor paths (`deploy/platform/cert-manager/upstream/**`, `deploy/platform/cni/upstream/**`, `**/gotk-components.yaml`) or `category == "style"` MUST be dropped automatically.
- Local receipts live in `.git/review-cycle/receipt.json` (and convergence state in `.git/review-cycle/state.json`) so they never dirty the working tree.
- `.githooks/pre-commit` enforces the staged review gate by default and honors `SKIP_REVIEW_GATE=1` as a logged break-glass override.
- Branch from `origin/main` in a clean worktree before executing implementation tasks; never push directly to `main`.

## Review Focus

- **Clean worktree or empty diff (`diff_sha256` on no changes):** Calling `check_gate` when no tracked files are modified/staged must return `(True, [])` so `Stop` hooks on read-only turns never block. (Pinned in Task 2 `test_empty_diff_passes_gate_without_receipt` and Task 4 `test_hook_allows_clean_worktree`).
- **Stale receipt after a post-review edit:** Modifying a file after recording a `PASS` receipt changes `diff_sha256`; `check_gate` must fail with `"diff_sha256 mismatch"` until re-verified. (Pinned in Task 2 `test_stale_diff_sha_blocks_gate`).
- **Convergence cap at round 3:** Submitting a 3rd review round that still has an open `Important` or `Security` finding must set `gate_status = "ESCALATE_TO_HUMAN"` and block commit. (Pinned in Task 2 `test_third_round_with_open_important_escalates_to_human`).
- **Open circuit breaker or untrusted author on `Low`-risk paths:** When `config/review-circuit-breaker.json` has `"state": "OPEN"` or the author is not in `trusted_authors`, a docs-only diff must escalate from `Low` (`0` humans) to at least `Medium` (`1` human). (Pinned in Task 2 `test_open_circuit_breaker_and_untrusted_author_escalate_low_risk`).
- **Vendor/skipped paths and missing `file:line` evidence:** Candidate findings on `deploy/platform/cni/upstream/*` or missing `line_start` must be stripped by `validate_and_record_receipt` rather than blocking the gate. (Pinned in Task 2 `test_skipped_paths_and_missing_line_evidence_are_filtered`).

---

## File Structure

- `REVIEW.md` — Canonical code-owner review rulebook (domain failure modes, `file:line` evidence rule, 2-nit cap, skipped paths, severity & risk definitions).
- `CLAUDE.md` — Root agent pointer linking `AGENTS.md` and `REVIEW.md`.
- `config/review-policy.json` — Risk tier path globs (`low_paths`, `high_paths`), `skip_paths`, `trusted_authors`, `max_nits` (`2`), `max_rounds` (`3`).
- `config/review-circuit-breaker.json` — Revert/incident counter, threshold (`max_recent_incidents: 2`), and breaker state (`"CLOSED"` or `"OPEN"`).
- `scripts/review-cycle.py` — Unified CLI & library for `verify`, `classify-risk`, `record-receipt`, `resolve`, `check-gate`, `summary`, `eval`, and `circuit-breaker`.
- `.agents/review-prompts/*.md` — 7 specialized subagent prompt templates (`diff-simplifier.md`, `e2e-behavior.md`, `bug-reviewer.md`, `security-reviewer.md`, `contract-reviewer.md`, `finding-verifier.md`, `risk-classifier.md`).
- `.agents/skills/{code-review,security-review,pre-pr-verify,explain-slice}/SKILL.md` — Portable skills orchestrating Antigravity subagents and `scripts/review-cycle.py`.
- `.omp/commands/{code.review,security.review,pre.pr,explain.slice}.md` & `.opencode/commands/{code-review,security-review,pre-pr,explain-slice}.md` — Cross-agent slash commands.
- `scripts/universal-agent-hook.py`, `.omp/extensions/arch-doc-sync.ts`, `.opencode/plugins/arch-doc-sync.js`, `.githooks/pre-commit` — Multi-agent & Git hook gates updated to enforce `review-cycle.py check-gate`.
- `.github/PULL_REQUEST_TEMPLATE.md`, `.github/CODEOWNERS`, `.github/workflows/review-gate.yaml`, `.github/workflows/lint.yaml`, `.github/workflows/gitops.yaml` — PR template, code owners, review gate workflow, and `merge_group` triggers.
- `scripts/rollback-change.sh` & `tests/review-evals/{must-flag,must-pass}/*.json` — Fast rollback script and reviewer eval corpus.
- `tests/review_cycle/test_*.py` — Unit and integration test suite for the entire cycle.

---

### Task 1: Create Rulebooks (`REVIEW.md`, `CLAUDE.md`) and Policy Configs

**Files:**
- Create: `REVIEW.md`
- Create: `CLAUDE.md`
- Create: `config/review-policy.json`
- Create: `config/review-circuit-breaker.json`
- Create: `tests/review_cycle/__init__.py`
- Create: `tests/review_cycle/test_review_config.py`
- Test: `tests/review_cycle/test_review_config.py`

**Interfaces:**
- Consumes: Spec §5.1 (`REVIEW.md` and `CLAUDE.md`) and §5.5 (`config/review-policy.json` & `config/review-circuit-breaker.json`).
- Produces:
  - `config/review-policy.json` with keys: `schema_version` (`"1.0"`), `max_nits` (`2`), `max_rounds` (`3`), `trusted_authors` (`["nacfson"]`), `skip_paths` (`["deploy/platform/cert-manager/upstream/**", "deploy/platform/cni/upstream/**", "**/gotk-components.yaml"]`), `low_paths` (`["docs/**", "*.md", "tests/review-evals/**"]`), `high_paths` (`["gateway/**", "services/vault-proxies/**", "deploy/platform/**", "clusters/**", "bootstrap/**", ".github/workflows/**", "scripts/platform-preflight.py", "scripts/render-invariants.py", ".specify/memory/constitution.md", "contracts/**", "docs/platform-architecture.html"]`).
  - `config/review-circuit-breaker.json` with keys: `state` (`"CLOSED"`), `recent_incidents` (`0`), `max_recent_incidents` (`2`), `incidents` (`[]`).

- [ ] **Step 1: Write the failing test**

Create `tests/review_cycle/__init__.py` (empty) and `tests/review_cycle/test_review_config.py`:

```python
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `python3 -m unittest tests.review_cycle.test_review_config -v`
Expected: FAIL with `FileNotFoundError` for `CLAUDE.md`, `REVIEW.md`, and `config/review-policy.json`.

- [ ] **Step 3: Create `CLAUDE.md`, `REVIEW.md`, `config/review-policy.json`, and `config/review-circuit-breaker.json`**

Populate `CLAUDE.md` with pointers to `AGENTS.md` and `REVIEW.md` plus the 5 verification commands from `AGENTS.md` §5. Populate `REVIEW.md` with all domain failure modes, evidence rules (`file:line`), severity definitions (`Important`, `Security`, `Nit`, `Pre-existing`), the `at most 2` nits cap, summary format (`0 Important, 0 Security, N Nit, M Pre-existing`), and skipped paths from Spec §5.1. Populate `config/review-policy.json` and `config/review-circuit-breaker.json` with the exact keys listed in Interfaces.

- [ ] **Step 4: Run test to verify it passes**

Run: `python3 -m unittest tests.review_cycle.test_review_config -v`
Expected: PASS (4 tests).

- [ ] **Step 5: Commit**

```bash
git add CLAUDE.md REVIEW.md config/review-policy.json config/review-circuit-breaker.json tests/review_cycle/
git commit -m "feat(review): add REVIEW.md, CLAUDE.md, and review policy configs"
```

---

### Task 2: Implement Core Verification, Classification & Receipt Engine (`scripts/review-cycle.py`)

**Files:**
- Create: `scripts/review-cycle.py`
- Create: `tests/review_cycle/test_review_engine.py`
- Test: `tests/review_cycle/test_review_engine.py`

**Interfaces:**
- Consumes: `config/review-policy.json` and `config/review-circuit-breaker.json` from Task 1.
- Produces in `scripts/review-cycle.py` (importable via `importlib.util`):
  - `compute_diff_sha256(diff_text: str) -> str` (normalizes line endings, strips `index ..` lines, returns hex sha256; returns `"EMPTY"` when stripped diff is empty).
  - `classify_risk(changed_files: list[str], policy: dict, breaker: dict, author: str | None = None, classifier2_override: str | None = None) -> dict` returning `{"path_tier": str, "classifier2_override": str | None, "effective_tier": str, "required_human_approvals": int, "review_depth": str, "circuit_breaker": str}`.
    - Path tier is `"High"` if any non-skipped file matches `policy["high_paths"]`; `"Low"` if all non-skipped files match `policy["low_paths"]` and none match `high_paths`; otherwise `"Medium"`.
    - If `breaker["state"] == "OPEN"` or (`author` is provided and `author not in policy["trusted_authors"]`), `"Low"` escalates to `"Medium"` (and untrusted author on `"Medium"` escalates to `"High"`). `classifier2_override` can only raise the tier, never lower it.
    - `required_human_approvals`: `0` for `"Low"`, `1` for `"Medium"`, `2` for `"High"`.
    - `review_depth`: `"Quick"` for `"Low"`, `"Standard"` for `"Medium"`, `"Deep"` for `"High"`.
  - `validate_and_record_receipt(payload: dict, receipt_path: Path, state_path: Path, diff_sha: str, changed_files: list[str], policy: dict, breaker: dict) -> dict`:
    - Filters out ` payload.get("findings", [])` where `file` matches `policy["skip_paths"]`, `category == "style"`, or `file`/`line_start`/`line_end`/`evidence` is missing/invalid, or `(file, line_start, title)` was already resolved in `state_path` with `resolution in {"false_positive", "intentional", "wont_fix"}`.
    - Caps findings with `severity == "Nit"` at `policy["max_nits"]` (`2`).
    - Increments `round` in `state_path` when `diff_sha` changes on the same branch/session (resets when `state_path` is absent). Sets `re_review_mode = "diff_only"` when `round > 1` else `"full"`.
    - Counts `important_open` (`severity == "Important"` and `status == "open"`) and `security_open` (`severity == "Security"` and `status == "open"`).
    - Sets `gate_status`:
      - `"ESCALATE_TO_HUMAN"` if `round > policy["max_rounds"]` or (`round == policy["max_rounds"]` and (`important_open > 0` or `security_open > 0`)).
      - `"PASS"` if `verification.passed` is `True` and `important_open == 0` and `security_open == 0` and `round <= policy["max_rounds"]`.
      - `"BLOCKED"` otherwise.
    - Writes receipt JSON to `receipt_path` and returns the receipt dict.
  - `resolve_finding(receipt_path: Path, state_path: Path, finding_id: str, resolution: str, reason: str) -> dict`:
    - Validates `resolution in {"fixed", "false_positive", "intentional", "wont_fix"}` and non-empty `reason`. Marks matching finding `status = "resolved"`, records resolution in `state_path` (including noise stats count per resolution type), recomputes `important_open`, `security_open`, and `gate_status`, and saves `receipt_path`.
  - `check_gate(scope: str, receipt_path: Path, diff_text_override: str | None = None, changed_files_override: list[str] | None = None) -> tuple[bool, list[str]]`:
    - If `changed_files` is empty or `compute_diff_sha256(diff_text) == "EMPTY"`, returns `(True, [])`.
    - Otherwise checks `receipt_path` exists, `receipt["diff_sha256"] == current_sha`, and `receipt["gate_status"] == "PASS"`.
  - `format_markdown_summary(receipt: dict) -> str`:
    - Starts with `## Automated Review Receipt` and `<important_open> Important, <security_open> Security, <nit_count> Nit, <pre_existing_count> Pre-existing` (plus `"Nothing to fix."` when `important_open == 0 and security_open == 0`).

- [ ] **Step 1: Write the failing test**

Create `tests/review_cycle/test_review_engine.py`:

```python
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `python3 -m unittest tests.review_cycle.test_review_engine -v`
Expected: FAIL with `FileNotFoundError` for `scripts/review-cycle.py`.

- [ ] **Step 3: Implement `scripts/review-cycle.py`**

Implement the functions specified in Interfaces (`compute_diff_sha256`, `classify_risk`, `validate_and_record_receipt`, `resolve_finding`, `check_gate`, `format_markdown_summary`, `run_domain_verification`, `run_eval_suite`, `update_circuit_breaker`) and CLI subcommands (`verify`, `classify-risk`, `record-receipt`, `resolve`, `check-gate`, `summary`, `eval`, `circuit-breaker`). Make `scripts/review-cycle.py` executable (`chmod +x`).

- [ ] **Step 4: Run test to verify it passes**

Run: `python3 -m unittest tests.review_cycle.test_review_engine -v`
Expected: PASS (6 tests).

- [ ] **Step 5: Commit**

```bash
git add scripts/review-cycle.py tests/review_cycle/test_review_engine.py
git commit -m "feat(review): implement review-cycle.py verification, classification, and receipt engine"
```

---

### Task 3: Add Antigravity Subagent Prompts & Multi-Agent Skills/Commands (`agy`, `omp`, `opencode`)

**Files:**
- Create: `.agents/review-prompts/diff-simplifier.md`
- Create: `.agents/review-prompts/e2e-behavior.md`
- Create: `.agents/review-prompts/bug-reviewer.md`
- Create: `.agents/review-prompts/security-reviewer.md`
- Create: `.agents/review-prompts/contract-reviewer.md`
- Create: `.agents/review-prompts/finding-verifier.md`
- Create: `.agents/review-prompts/risk-classifier.md`
- Create: `.agents/skills/code-review/SKILL.md`
- Create: `.agents/skills/security-review/SKILL.md`
- Create: `.agents/skills/pre-pr-verify/SKILL.md`
- Create: `.agents/skills/explain-slice/SKILL.md`
- Create: `.omp/commands/code.review.md`
- Create: `.omp/commands/security.review.md`
- Create: `.omp/commands/pre.pr.md`
- Create: `.omp/commands/explain.slice.md`
- Create: `.opencode/commands/code-review.md`
- Create: `.opencode/commands/security-review.md`
- Create: `.opencode/commands/pre-pr.md`
- Create: `.opencode/commands/explain-slice.md`
- Create: `tests/review_cycle/test_prompts_and_skills.py`
- Test: `tests/review_cycle/test_prompts_and_skills.py`

**Interfaces:**
- Consumes: `REVIEW.md` from Task 1 and `scripts/review-cycle.py` CLI subcommands from Task 2.
- Produces:
  - 7 subagent prompt files in `.agents/review-prompts/` that reference `REVIEW.md` and specify the exact JSON finding schema (`id`, `category`, `severity`, `class_of_bug`, `file`, `line_start`, `line_end`, `title`, `evidence`).
  - 4 skills in `.agents/skills/` instructing Antigravity (`agy`) to dispatch fresh subagents via `invoke_subagent` with the role prompts in `.agents/review-prompts/` and record verified results via `python3 scripts/review-cycle.py record-receipt`.
  - 4 command wrappers each in `.omp/commands/` and `.opencode/commands/` pointing to the same prompts and `scripts/review-cycle.py` workflow.

- [ ] **Step 1: Write the failing test**

Create `tests/review_cycle/test_prompts_and_skills.py`:

```python
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `python3 -m unittest tests.review_cycle.test_prompts_and_skills -v`
Expected: FAIL with missing `.agents/review-prompts/diff-simplifier.md`.

- [ ] **Step 3: Create the 7 subagent prompts, 4 `.agents/skills/` files, and `.omp`/`.opencode` command wrappers**

Write `.agents/review-prompts/*.md` with role-specific checklists aligned with `REVIEW.md` and the JSON finding output format (`line_start`, `line_end`, `severity`, `class_of_bug`). Write `.agents/skills/{code-review,security-review,pre-pr-verify,explain-slice}/SKILL.md` with YAML frontmatter (`name`, `description`) and step-by-step instructions to run `python3 scripts/review-cycle.py verify` / `classify-risk`, launch fresh-context subagents via `invoke_subagent`, run `finding-verifier`, and persist the receipt with `python3 scripts/review-cycle.py record-receipt`. Write the corresponding command wrappers in `.omp/commands/` and `.opencode/commands/`.

- [ ] **Step 4: Run test to verify it passes**

Run: `python3 -m unittest tests.review_cycle.test_prompts_and_skills -v`
Expected: PASS (3 tests).

- [ ] **Step 5: Commit**

```bash
git add .agents/review-prompts/ .agents/skills/ .omp/commands/ .opencode/commands/ tests/review_cycle/test_prompts_and_skills.py
git commit -m "feat(review): add Antigravity subagent review prompts and multi-agent skills"
```

---

### Task 4: Wire Universal Agent Hooks (`agy`, `omp`, `opencode`, `claude`, `codex`) & `.githooks/pre-commit`

**Files:**
- Modify: `scripts/universal-agent-hook.py`
- Modify: `.omp/extensions/arch-doc-sync.ts`
- Modify: `.opencode/plugins/arch-doc-sync.js`
- Modify: `.githooks/pre-commit`
- Create: `tests/review_cycle/test_hooks_integration.py`
- Test: `tests/review_cycle/test_hooks_integration.py`

**Interfaces:**
- Consumes: `check_gate` from `scripts/review-cycle.py` (Task 2) and `check-arch-doc-sync.py`.
- Produces:
  - `scripts/universal-agent-hook.py`:
    - On `PreToolUse` when command is `git commit`: runs `check-arch-doc-sync` AND `review_cycle.check_gate("staged", receipt_path)` (unless `SKIP_REVIEW_GATE=1` is set in env or command). If errors exist, returns agent-specific deny/block JSON with `[REVIEW-CYCLE REQUIRED]` or `[ARCH-DOC-SYNC REQUIRED]`.
    - On `Stop`: runs `check-arch-doc-sync` (`worktree`).
  - `.omp/extensions/arch-doc-sync.ts` and `.opencode/plugins/arch-doc-sync.js`: execute `python3 scripts/review-cycle.py check-gate --scope staged` before `git commit`.
  - `.githooks/pre-commit`: runs `python3 scripts/review-cycle.py check-gate --scope staged` after the architecture handbook check, unless `SKIP_REVIEW_GATE=1`.

- [ ] **Step 1: Write the failing test**

Create `tests/review_cycle/test_hooks_integration.py`:

```python
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `python3 -m unittest tests.review_cycle.test_hooks_integration -v`
Expected: FAIL because `universal-agent-hook.py`, `.omp`, `.opencode`, and `.githooks/pre-commit` do not call `review-cycle.py` yet.

- [ ] **Step 3: Update `scripts/universal-agent-hook.py`, `.omp/extensions/arch-doc-sync.ts`, `.opencode/plugins/arch-doc-sync.js`, and `.githooks/pre-commit`**

In `scripts/universal-agent-hook.py`, load `scripts/review-cycle.py` via `importlib.util`. Support `REVIEW_RECEIPT_PATH`, `REVIEW_FORCE_CHANGED`, and `REVIEW_FORCE_DIFF` env overrides for deterministic testing, and on `PreToolUse` for `git commit` commands (when `SKIP_REVIEW_GATE != "1"`), call `review_cycle.check_gate("staged", receipt_path, diff_text_override=..., changed_files_override=...)` and append `[REVIEW-CYCLE REQUIRED] ...` on failure. Update `.omp/extensions/arch-doc-sync.ts`, `.opencode/plugins/arch-doc-sync.js`, and `.githooks/pre-commit` to invoke `python3 scripts/review-cycle.py check-gate --scope staged` (unless `SKIP_REVIEW_GATE=1`).

- [ ] **Step 4: Run test to verify it passes**

Run: `python3 -m unittest tests.review_cycle.test_hooks_integration -v`
Expected: PASS (3 tests).

- [ ] **Step 5: Commit**

```bash
git add scripts/universal-agent-hook.py .omp/extensions/arch-doc-sync.ts .opencode/plugins/arch-doc-sync.js .githooks/pre-commit tests/review_cycle/test_hooks_integration.py
git commit -m "feat(hooks): enforce review-cycle gate across multi-agent hooks and pre-commit"
```

---

### Task 5: Add PR Template, `CODEOWNERS`, Merge Queue CI, Fast Rollback & Eval Corpus

**Files:**
- Create: `.github/PULL_REQUEST_TEMPLATE.md`
- Create: `.github/CODEOWNERS`
- Create: `.github/workflows/review-gate.yaml`
- Modify: `.github/workflows/lint.yaml`
- Modify: `.github/workflows/gitops.yaml`
- Create: `scripts/rollback-change.sh`
- Create: `tests/review-evals/must-flag/plaintext-secret.json`
- Create: `tests/review-evals/must-flag/missing-qos-limits.json`
- Create: `tests/review-evals/must-pass/clean-doc-update.json`
- Create: `tests/review-evals/must-pass/clean-gateway-test.json`
- Modify: `AGENTS.md`
- Modify: `INDEX.md`
- Create: `tests/review_cycle/test_ci_and_evals.py`
- Test: `tests/review_cycle/test_ci_and_evals.py`

**Interfaces:**
- Consumes: `scripts/review-cycle.py` (`eval`, `classify-risk`, `circuit-breaker`) from Task 2.
- Produces:
  - `.github/PULL_REQUEST_TEMPLATE.md` containing sections `## Intent`, `## Testing & Verification`, `## Risk Tier`, `## How to Revert`, and `## Automated Review Receipt`.
  - `.github/CODEOWNERS` mapping sensitive paths (`gateway/`, `services/vault-proxies/`, `deploy/platform/`, `clusters/`, `bootstrap/`, `.github/workflows/`, `.specify/memory/constitution.md`, `contracts/`) to `@nacfson`.
  - `.github/workflows/lint.yaml` and `.github/workflows/gitops.yaml` updated with `merge_group:` trigger.
  - `.github/workflows/review-gate.yaml` running on `pull_request`, `merge_group`, and `schedule` (nightly `0 3 * * *`), executing `python3 -m unittest discover tests/review_cycle`, `python3 scripts/review-cycle.py eval`, and `python3 scripts/review-cycle.py classify-risk --scope branch`.
  - `scripts/rollback-change.sh` (executable bash script supporting `--dry-run <commit>` and live revert + `python3 scripts/review-cycle.py circuit-breaker --record-incident <commit>`).
  - Seed eval corpus in `tests/review-evals/{must-flag,must-pass}/*.json` scored by `python3 scripts/review-cycle.py eval` (exits `0` with `recall: 1.0`, `false_positive_rate: 0.0`).

- [ ] **Step 1: Write the failing test**

Create `tests/review_cycle/test_ci_and_evals.py`:

```python
import subprocess
import unittest
from pathlib import Path
import yaml

ROOT = Path(__file__).resolve().parents[2]


class TestCiGovernanceAndEvals(unittest.TestCase):
    def test_pr_template_and_codeowners_exist(self):
        pr_tpl = (ROOT / ".github/PULL_REQUEST_TEMPLATE.md").read_text(encoding="utf-8")
        for heading in ("## Testing", "## Risk", "## How to Revert", "## Automated Review Receipt"):
            self.assertIn(heading, pr_tpl)
        codeowners = (ROOT / ".github/CODEOWNERS").read_text(encoding="utf-8")
        for protected in ("/gateway/", "/deploy/platform/", "/.github/workflows/", "/.specify/memory/constitution.md"):
            self.assertIn(protected, codeowners)

    def test_workflows_include_merge_group_and_review_gate(self):
        for wf in ("lint.yaml", "gitops.yaml", "review-gate.yaml"):
            doc = yaml.safe_load((ROOT / ".github/workflows" / wf).read_text(encoding="utf-8"))
            triggers = doc.get("on") or doc.get(True)
            self.assertIn("merge_group", triggers, f"{wf} missing merge_group trigger")
        rg = yaml.safe_load((ROOT / ".github/workflows/review-gate.yaml").read_text(encoding="utf-8"))
        triggers = rg.get("on") or rg.get(True)
        self.assertIn("schedule", triggers)

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

    def test_rollback_script_dry_run(self):
        script = ROOT / "scripts/rollback-change.sh"
        self.assertTrue(script.is_file())
        proc = subprocess.run(
            ["bash", str(script), "--dry-run", "HEAD"],
            text=True,
            capture_output=True,
            cwd=ROOT,
            check=False,
        )
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertIn("DRY-RUN", proc.stdout)
```

- [ ] **Step 2: Run test to verify it fails**

Run: `python3 -m unittest tests.review_cycle.test_ci_and_evals -v`
Expected: FAIL with missing `.github/PULL_REQUEST_TEMPLATE.md`.

- [ ] **Step 3: Create PR template, `CODEOWNERS`, `review-gate.yaml`, `rollback-change.sh`, eval fixtures, and update workflows & docs**

Create `.github/PULL_REQUEST_TEMPLATE.md`, `.github/CODEOWNERS`, `.github/workflows/review-gate.yaml`, `scripts/rollback-change.sh` (`chmod +x`), and the 4 eval fixtures in `tests/review-evals/{must-flag,must-pass}/`. Add `merge_group:` to `.github/workflows/lint.yaml` and `.github/workflows/gitops.yaml`. Document `REVIEW.md` and `scripts/review-cycle.py` in `AGENTS.md` and `INDEX.md`.

- [ ] **Step 4: Run all tests and verification gates to verify they pass**

Run: `python3 -m unittest discover tests/review_cycle -v`
Expected: PASS (all 16 tests across 4 test files).

Run: `python3 -m unittest discover tests/preflight`
Expected: PASS.

Run: `python3 scripts/check-arch-doc-sync.py --worktree`
Expected: `[ARCH-DOC-SYNC PASS]`.

- [ ] **Step 5: Commit**

```bash
git add .github/PULL_REQUEST_TEMPLATE.md .github/CODEOWNERS .github/workflows/ scripts/rollback-change.sh tests/review-evals/ tests/review_cycle/test_ci_and_evals.py AGENTS.md INDEX.md
git commit -m "feat(review): add PR governance, merge queue, rollback circuit breaker, and eval suite"
```
