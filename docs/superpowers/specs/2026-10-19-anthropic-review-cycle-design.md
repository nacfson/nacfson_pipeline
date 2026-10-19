# Anthropic-Style End-to-End Build, Verification & Code Review Cycle

## 1. Intent and Why

As AI agents (`agy`, `omp`, `opencode`, `claude`, `codex`, `grok`, `cursor`) author an increasing share of changes in `nacfson_pipeline`, line-by-line manual review of every routine diff is no longer the best use of human attention. Based on Anthropic's engineering model (*"How the Claude Code team does code review"*, Oct 2026), code review has two jobs:

1. **Find and fix issues before they ship** (moving most reading to automated checks and multi-agent review passes so human attention goes where the risk is).
2. **Build shared context and codebase understanding** (using on-demand slice explainers and interactive visualizations rather than reading trivial diffs).

The goal of this design is to embed the **entire 10-stage Anthropic build and review cycle** into `nacfson_pipeline` so that:
- Verification, code review, security review, and importance/risk classification run **continuously during local work (even when no PR exists)** as well as **on every Pull Request and in the Merge Queue**.
- Antigravity (`agy`) subagents with specialized role prompts execute the parallel LLM review, verifier voting, and exception classification passes first, while all rulebooks, skills, commands, hooks, and review receipts use **portable, agent-agnostic formats** compatible with `omp` (Oh-my-pi), `opencode`, Claude Code, Codex, Grok, and Cursor.
- Low-risk changes with green deterministic checks and zero open review/security findings can clear with minimal human overhead, while high-risk paths (`gateway/`, `deploy/platform/vault/`, `deploy/platform/cni/`, `.github/workflows/`, `.specify/memory/constitution.md`) strictly require human review, backed by fast rollback, a revert/incident circuit breaker, and a daily regression eval harness.

---

## 2. What the Owner Decided

1. **Pre-PR and PR Coverage:** The agent verification, code-review, PR check, and importance classification system must operate both locally (before a PR exists, gated at agent turn-end and `git commit`) and on GitHub PRs.
2. **Multi-Agent Hook & Format Portability:** Because work happens across `agy` (Antigravity), `omp` (Oh-my-pi), `opencode`, and other agents, the system must plug into our universal hook architecture (`scripts/universal-agent-hook.py`, `.omp/extensions/`, `.opencode/plugins/`, `.githooks/pre-commit`) and use portable Markdown prompts and JSON receipt formats.
3. **Full 10-Stage Lifecycle:** We must implement the complete cycle from the Anthropic blog post—not a partial subset:
   - Local verification before review
   - Pre-PR internal skills (Bug Hunter, Diff Simplifier, End-to-End Behavior Checker)
   - `REVIEW.md` rulebook + `CLAUDE.md` pointer to `AGENTS.md`
   - Parallel multi-agent review (`Quick` / `Standard` / `Deep`) + Verifier voting
   - Importance tagging (`Important`, `Security`, `Nit`, `Pre-existing`)
   - Convergence guardrails (max 3 rounds, diff-only re-review after round 1, bug-class flagging, no repeats)
   - Dedicated blocking Security Review
   - Two-stage Risk Classifier (`Low` / `Medium` / `High`) + Trusted Author list + Commit-bound approvals
   - CI PR checks + Merge Queue (`merge_group`)
   - Post-merge staged rollout (`local-k3s` → `vps-k3s`), one-step rollback, revert/incident circuit breaker, incident/clean-PR eval loop, noise tracking, nightly codebase scan, and on-demand slice explainers.
4. **Antigravity Subagents First for LLM Passes:** LLM-driven review passes will use Antigravity (`agy`) subagents with distinct role prompts first, recording their verified output into the portable review receipt so any hook or CI job can validate it deterministically.

---

## 3. Goals, Non-Goals, and Anti-Goals

### Goals
- **Shift-Left Verification & Review:** Catch routine bugs, security flaws, and contract drift before a commit or PR is created.
- **High Signal, Low Noise:** Filter candidate agent findings through a separate fresh-context Verifier subagent that drops style/diagnostic noise and requires `file:line` evidence.
- **Guaranteed Convergence:** Cap automated review-fix cycles at 3 rounds and switch to diff-only re-reviews after round 1 so an agent author and agent reviewer never loop indefinitely.
- **Risk-Proportional Human Attention:** Classify every change into `Low` (0 humans required once automated checks pass for trusted authors), `Medium` (1 human), or `High` (2 humans / explicit owner sign-off) based on the diff itself and circuit-breaker health.
- **Measurable Quality Loop:** Score reviewer prompts and `REVIEW.md` rules against real past bug/incident fixtures and known-clean diffs (`tests/review-evals/`).

### Non-Goals
- Replacing existing deterministic validators (`scripts/platform-preflight.py`, `scripts/render-validate.sh`, `scripts/check-arch-doc-sync.py`, `.githooks/pre-commit` secret scanning). The new review cycle wraps and orchestrates them.
- Requiring external SaaS review subscriptions or paid third-party GitHub Apps to pass local hooks.

### Anti-Goals (Failure Conditions Even If Technically Working)
- **Endless Agent Loops:** Any review flow that allows more than 3 automated review-fix rounds without pausing for human direction.
- **Nit Inflation:** Posting unverified style nits that force agents to make unnecessary extra commits.
- **Bot Approval Bypass:** Allowing an AI agent's review receipt or bot comment to count toward a required human approval slot on `Medium` or `High` risk changes.
- **Single-Harness Lock-In:** Storing prompts or hook state in a proprietary format that prevents `omp` or `opencode` from running or verifying the cycle.

---

## 4. End-to-End Lifecycle & Checkpoints

Every change flows through five enforced checkpoints plus a post-merge and continuous-learning loop:

```
[Local Work]
  ├─ 1. Domain Verification (`scripts/review-cycle.py verify`)
  ├─ 2. Pre-PR Skill Triad (Diff Simplifier → Bug Hunter → E2E Behavior Checker)
  ├─ 3. Parallel Code Review + Security Review + Verifier Vote (`/code-review` & `/security-review`)
  ├─ 4. Risk & Importance Classification (`Important`/`Security`/`Nit`/`Pre-existing` + `Low`/`Medium`/`High` tier)
  ▼
[Checkpoint A: Pre-Commit & Agent Stop Hook]
  ├─ Enforced by `universal-agent-hook.py`, `.omp`, `.opencode`, `.githooks/pre-commit`
  ├─ Verifies secret scan + arch-doc-sync + diff-bound `.git/review-cycle/receipt.json`
  ▼
[Checkpoint B: Pull Request & Security Gate]
  ├─ PR Template (`Testing`, `Risk`, `How to revert`) + `CODEOWNERS`
  ├─ GitHub Actions (`review-gate.yaml`): validates receipt, zero open `Important`/`Security` findings, risk tier & commit-bound human approvals
  ▼
[Checkpoint C: Required Deterministic CI]
  ├─ `lint.yaml` (Go tests/vet, ShellCheck, yamllint, Ansible syntax)
  ├─ `gitops.yaml` (`preflight-tests`, `render-validate`, `platform-preflight` for `local-k3s` & `vps-k3s`)
  ▼
[Checkpoint D: Merge Queue (`merge_group`)]
  ├─ Re-runs verification, Kustomize render, preflight, and review gate on latest `main` + PR diff
  ▼
[Checkpoint E: Post-Merge Rollout, Rollback & Continuous Eval Loop]
  ├─ Staged environment promotion (`local-k3s` before `vps-k3s`) & feature flags
  ├─ One-step rollback (`scripts/rollback-change.sh`) & Revert/Incident Circuit Breaker
  ├─ Daily Incident/Clean-PR Eval Suite (`tests/review-evals/`), Noise Tracker, Nightly Scan & `/explain-slice`
```

---

## 5. Architecture & Component Specifications

### 5.1 Universal Rulebooks (`REVIEW.md` and `CLAUDE.md`)
- **`CLAUDE.md` (Repo Root):** Lightweight entrypoint pointing Claude Code and compatible agents directly to `AGENTS.md` and `REVIEW.md` so conventions are never duplicated.
- **`REVIEW.md` (Repo Root):** Concise, code-owner-authored review rulebook loaded by every review pass. It specifies:
  1. **Domain Failure Modes ("What failure looks like here"):**
     - `gateway/` & `deploy/platform/identity/`: Auth bypass, open redirects, missing audience/host checks, or adding third-party Go dependencies.
     - `deploy/platform/vault/`, `deploy/platform/database/`, `services/vault-proxies/`: Plaintext secrets in Git/pods, broken transit/DB proxy boundaries, or non-stdlib Go imports.
     - `deploy/platform/cni/`, `deploy/platform/tls/`, `deploy/platform/ingress/`: Overly permissive egress (`0.0.0.0/0` / `world`), tenant TLS secrets, or bypassing `ingress-allowlist.yaml`.
     - `deploy/projects/` & `clusters/`: Breaking $1/n$ resource budgets, missing Guaranteed QoS (`requests == limits`), mutable image tags instead of `@sha256:...`, or coupled liveness/readiness probes.
     - `docs/`, `contracts/`, `specs/`: Documentation that lies or drifts from `docs/platform-architecture.html` and `scripts/render-invariants.py`.
  2. **Evidence & Signal Rules:**
     - Every finding MUST cite an exact `file:line` range and concrete failure mechanism (no guessing from variable/function names).
     - Style-only, formatting, and diagnostic cleanup comments are dropped.
     - Nits are capped at **at most 2 per review** and never block.
     - Summary MUST start with an explicit finding count (`0 Important, 0 Security, N Nit, M Pre-existing`) and state plainly when nothing needs fixing.
  3. **Skipped Paths:** Upstream vendored manifests (`deploy/platform/cert-manager/upstream/`, `deploy/platform/cni/upstream/`, `**/gotk-components.yaml`).

### 5.2 Portable Subagent Role Prompts & Cross-Harness Skills
To use Antigravity (`agy`) subagents first while keeping `omp` and `opencode` first-class citizens, all LLM prompts live in `.agents/review-prompts/` as plain Markdown and are exposed via `.agents/skills/`, `.omp/commands/`, and `.opencode/commands/`:

| Role / Skill | Prompt File | Purpose |
| :--- | :--- | :--- |
| **Diff Simplifier** | `.agents/review-prompts/diff-simplifier.md` | Pre-PR pass: identifies dead code, unnecessary abstractions, or bloated diffs before review. |
| **E2E Behavior Checker** | `.agents/review-prompts/e2e-behavior.md` | Pre-PR pass: verifies runtime contracts, probe separation, CORS, and handbook sync. |
| **Bug Hunter** | `.agents/review-prompts/bug-reviewer.md` | Parallel review hunter: logic errors, race conditions, nil/error handling, shell/python edge cases. |
| **Security Reviewer** | `.agents/review-prompts/security-reviewer.md` | Parallel blocking review pass: secret leaks, RBAC/NetworkPolicy widening, OIDC/cookie/TLS flaws, injection. |
| **Contract Reviewer** | `.agents/review-prompts/contract-reviewer.md` | Parallel review hunter: checks diff against ` .specify/memory/constitution.md`, `contracts/CONTRACT_*.md`, and `REVIEW.md`. |
| **Finding Verifier** | `.agents/review-prompts/finding-verifier.md` | Fresh-context voter: receives candidate findings from the parallel hunters, inspects actual `file:line` context, drops false positives/style noise, enforces the 2-nit cap, tags survivors (`Important`, `Security`, `Nit`, `Pre-existing`), and checks bug classes. |
| **Risk Classifier** | `.agents/review-prompts/risk-classifier.md` | Exception classifier (Stage 2 of risk classification): evaluates whether a diff with a `Low`/`Medium` path tier still carries hidden architectural or operational blast radius requiring an extra human reviewer. |

**Cross-Harness Wrappers:**
- `.agents/skills/code-review/SKILL.md` (orchestrates verification → parallel `agy` subagents (`bug-reviewer`, `security-reviewer`, `contract-reviewer`) → `finding-verifier` → `risk-classifier` → `scripts/review-cycle.py record-receipt`).
- `.agents/skills/security-review/SKILL.md` (runs focused blocking security review pass).
- `.agents/skills/pre-pr-verify/SKILL.md` (runs domain verification + `diff-simplifier` + `e2e-behavior` + `bug-reviewer`).
- `.agents/skills/explain-slice/SKILL.md` (generates on-demand codebase slice explainers & visual artifacts using `comprehension-digest` / `comprehension-visualize`).
- Command aliases in `.omp/commands/` (`code.review.md`, `security.review.md`, `pre.pr.md`, `explain.slice.md`) and `.opencode/commands/` (`code-review.md`, `security-review.md`, `pre-pr.md`, `explain-slice.md`).

### 5.3 Review Depth Modes (`Quick`, `Standard`, `Deep`)
Selected automatically by path sensitivity or overridden via `--depth`:
- **`Quick`:** For docs-only or comment-only changes (`*.md`, `docs/` excluding `platform-architecture.html`). Runs 1 combined review + verifier pass.
- **`Standard`:** Default for scripts, tests, and ordinary manifests. Runs parallel `bug-reviewer`, `security-reviewer`, and `contract-reviewer` + `finding-verifier`.
- **`Deep`:** Mandatory for `High`-risk paths (`gateway/`, `services/vault-proxies/`, `deploy/platform/`, `.github/workflows/`, `.specify/memory/constitution.md`). Adds `e2e-behavior` and `risk-classifier` exception audit.

### 5.4 Convergence Guardrails & Finding Resolution Protocol
To prevent infinite agent review-fix loops:
1. **Round Cap (Max 3 Rounds):** Tracked per branch/change-id in `.git/review-cycle/state.json`. If round 3 still produces open `Important` or `Security` findings, `scripts/review-cycle.py` sets `status: "escalate_to_human"` and blocks further automated fix rounds, instructing the agent to stop and ask the human partner to reconsider the approach.
2. **Diff-Only Re-Review After Round 1:** Round 1 reviews the full diff against `main`. Rounds 2 and 3 review only the incremental diff since the previous round plus previously open threads.
3. **Flag the Class of Bug:** The verifier groups identical issues across multiple lines/files into a single finding (`class_of_bug`) so the author fixes all occurrences in one round.
4. **No Repeats:** Findings already resolved (`false_positive`, `intentional`, `wont_fix`) with a recorded reason for the same code location are suppressed in subsequent rounds.
5. **Mandatory Thread Resolution:** Every `Important` and `Security` finding has a stable `finding_id`. It blocks commit and merge until either:
   - The code is modified and re-verified as fixed, OR
   - It is explicitly resolved via `python3 scripts/review-cycle.py resolve --id <finding_id> --resolution <false_positive|intentional|wont_fix> --reason "..."`.

### 5.5 Two-Stage Risk Classifier, Trusted Authors & Commit-Bound Approvals
Configured in `config/review-policy.json` and executed by `scripts/review-cycle.py classify-risk`:

| Risk Tier | Criteria | Required Human Approvals |
| :--- | :--- | :--- |
| **`Low`** | Docs, comments, unit test additions, or isolated eval fixtures (`tests/`, `docs/` non-handbook) with 0 open findings, trusted author, and circuit breaker `CLOSED`. | **0 additional humans** (the human who prompted/authored the change + green automated & agent review gates suffice). |
| **`Medium`** | Ordinary scripts, non-boundary project workload tweaks (`deploy/projects/pn/`), hooks, skills, or non-critical tooling. | **1 human approval** (can be the trusted author/prompter signing off on the PR checklist). |
| **`High`** | Any change touching sensitive paths (`gateway/`, `services/vault-proxies/`, `deploy/platform/**`, `clusters/**`, `bootstrap/**`, `.github/workflows/**`, `scripts/platform-preflight.py`, `scripts/render-invariants.py`, `.specify/memory/constitution.md`, `contracts/**`), OR flagged by Classifier 2 (`risk-classifier` subagent), OR untrusted author, OR circuit breaker `OPEN`. | **2 human approvals** (or explicit repository owner sign-off via `CODEOWNERS` in single-maintainer mode). |

**Safety Rules:**
- **The change sets the tier, not the author.**
- **Trusted Authors List (`config/review-policy.json`):** Only listed repo maintainers qualify for `Low`/`Medium` lighter paths; external/untrusted contributors always require maintainer approval.
- **Agent Approval Never Counts as Human Approval:** The review receipt proves automated checks ran; it never fills a human approval slot.
- **Commit-Bound Approvals & Receipts:** Every receipt records `diff_sha256` (hash of the normalized unified diff) and `head_tree_sha`. Any new edit or commit invalidates the receipt and requires a diff-only re-verification.

### 5.6 Unified CLI Engine & Receipt Schema (`scripts/review-cycle.py`)
`scripts/review-cycle.py` (Python 3.12 stdlib only) is the single source of truth called by humans, agents (`agy`, `omp`, `opencode`), hooks, and GitHub Actions:

- `python3 scripts/review-cycle.py verify [--scope staged|worktree|branch]`:
  Runs the deterministic checks applicable to changed files:
  - Go tests & `go vet` for `gateway/` and `services/vault-proxies/`
  - `python3 -m unittest discover tests/preflight`
  - `./scripts/render-validate.sh local-k3s` & `vps-k3s` (when `deploy/` or `clusters/` change, if `flux`/`kubeconform` are installed locally, and always in CI)
  - `python3 scripts/check-arch-doc-sync.py`
  - Secret pattern scan
- `python3 scripts/review-cycle.py classify-risk [--scope staged|worktree|branch]`:
  Computes deterministic path risk tier, checks circuit-breaker state, and outputs required review depth (`Quick` / `Standard` / `Deep`) and human approval count.
- `python3 scripts/review-cycle.py record-receipt --input <findings.json>`:
  Validates verifier output against `REVIEW.md` rules (requires `file:line`, enforces 2-nit cap, increments convergence round, checks for open `Important`/`Security` items) and writes `.git/review-cycle/receipt.json` (and optional PR-portable `.review-cycle-receipt.json` or PR comment payload).
- `python3 scripts/review-cycle.py check-gate [--scope staged|worktree|branch]`:
  Verifies that a fresh receipt matching the current diff hash exists, convergence round $\le 3$, deterministic verification passed, and 0 `Important` or `Security` findings remain unresolved.
- `python3 scripts/review-cycle.py resolve --id <id> --resolution <type> --reason <text>`:
  Records an explicit thread resolution and logs noise telemetry.
- `python3 scripts/review-cycle.py eval`:
  Runs the regression eval suite in `tests/review-evals/` (scoring bug recall on incident fixtures and false-positive rate on clean fixtures).

**Receipt Schema (`.git/review-cycle/receipt.json`):**
```json
{
  "schema_version": "1.0",
  "timestamp_utc": "2026-10-19T13:40:00Z",
  "agent_harness": "antigravity",
  "scope": "staged",
  "diff_sha256": "<sha256-of-normalized-diff>",
  "touched_files": ["..."],
  "review_depth": "Standard",
  "round": 1,
  "max_rounds": 3,
  "verification": {
    "passed": true,
    "commands_run": ["..."]
  },
  "risk": {
    "path_tier": "Medium",
    "classifier2_override": null,
    "effective_tier": "Medium",
    "required_human_approvals": 1,
    "circuit_breaker": "CLOSED"
  },
  "summary": {
    "important_open": 0,
    "security_open": 0,
    "nit_count": 1,
    "pre_existing_count": 0
  },
  "findings": [
    {
      "id": "F-001",
      "category": "bug",
      "severity": "Nit",
      "class_of_bug": "error-wrapping",
      "file": "scripts/example.py",
      "line_start": 42,
      "line_end": 45,
      "title": "...",
      "evidence": "...",
      "verifier_votes": 2,
      "status": "open",
      "resolution": null,
      "resolution_reason": null
    }
  ],
  "gate_status": "PASS"
}
```

### 5.7 Hook Integration Across All Agent Harnesses
1. **`scripts/universal-agent-hook.py` (`agy`, `claude`, `codex`, `grok`, `cursor`):**
   - On `PreToolUse` (`git commit`): Runs `check-arch-doc-sync.py` AND `review-cycle.py check-gate --scope staged`. Blocks `git commit` if verification or review receipt is missing/stale or has open `Important`/`Security` findings.
   - On `Stop`: Runs `check-arch-doc-sync.py` AND checks if tracked files are modified without a valid worktree/staged review receipt; if missing, prompts the agent once to run the verification & review cycle before finishing.
2. **`.omp/extensions/arch-doc-sync.ts` & `.opencode/plugins/arch-doc-sync.js`:**
   - Updated to call `scripts/review-cycle.py check-gate` alongside `check-arch-doc-sync.py` on `git commit` and turn-end/session-idle.
3. **`.githooks/pre-commit`:**
   - Runs: (1) Secret leak scan, (2) `check-arch-doc-sync.py --staged`, and (3) `python3 scripts/review-cycle.py check-gate --scope staged` (with an explicit break-glass env var `SKIP_REVIEW_GATE=1` logged for emergency hotfixes).

### 5.8 Pull Request, Merge Queue, Post-Merge Rollback & Circuit Breaker
1. **PR Template (`.github/PULL_REQUEST_TEMPLATE.md`) & `.github/CODEOWNERS`:**
   - Enforces sections for **Intent**, **Testing & Verification Output**, **Risk Tier (`Low`/`Medium`/`High`)**, and **How to Revert**.
   - `CODEOWNERS` maps sensitive paths (`gateway/`, `deploy/platform/`, `clusters/`, `.github/workflows/`, `.specify/memory/constitution.md`, `contracts/`) to repository owners.
2. **GitHub Actions Workflows:**
   - Update `.github/workflows/lint.yaml` and `.github/workflows/gitops.yaml` to include `merge_group:` triggers for the GitHub Merge Queue.
   - Add `.github/workflows/review-gate.yaml` triggered on `pull_request`, `merge_group`, and `schedule` (nightly scan):
     - Runs `scripts/review-cycle.py verify`, `classify-risk`, and `eval`.
     - Validates the PR body (`Testing`, `Risk`, `How to revert` sections populated) and checks the circuit-breaker status.
3. **Fast Rollback & Circuit Breaker (`scripts/rollback-change.sh` & `config/review-circuit-breaker.json`):**
   - `scripts/rollback-change.sh <commit-or-pr>` creates a clean revert commit, verifies Kustomize/preflight health across `local-k3s` and `vps-k3s`, and records the revert in `tests/review-evals/incidents/` and `config/review-circuit-breaker.json`.
   - If recent reverts/incidents exceed the threshold in `config/review-circuit-breaker.json`, the circuit breaker trips to `OPEN`, automatically promoting all `Low` changes to `Medium` (requiring human sign-off) until reset after a clean eval pass.

### 5.9 Continuous Reviewer Improvement (`tests/review-evals/`)
- Contains curated diff fixtures:
  - **Incident / Regression Fixtures (`tests/review-evals/must-flag/`):** Real or representative bugs (e.g., plaintext secret in manifest, missing Guaranteed QoS, unpinned image tag, gateway auth bypass, missing `docs/platform-architecture.html` co-update) with expected file, line range, and severity (`Important` or `Security`).
  - **Clean Fixtures (`tests/review-evals/must-pass/`):** Known-clean diffs that must produce `0 Important, 0 Security` findings so false-positive noise is measured and prevented.

---

## 6. Planner / Builder Calls (Marked as Our Calls)

1. **Zero External Dependencies for the Engine:** `scripts/review-cycle.py` and its tests in `tests/review_cycle/` will be written in Python 3.12 standard library only, matching `scripts/check-arch-doc-sync.py`.
2. **Local Receipt Storage + Optional Commit Trailer / PR Summary:** Local receipts live in `.git/review-cycle/receipt.json` (so they never dirty the working tree or cause merge conflicts), while `scripts/review-cycle.py summary --markdown` outputs a formatted Markdown block that agents paste into the PR description (under `## Automated Review Receipt`) for CI `review-gate.yaml` to verify on GitHub PRs.
3. **Architecture Handbook Co-Update:** Because adding `.github/workflows/`, `scripts/`, and governance documentation is part of our repository workflow, we will also keep `docs/platform-architecture.html` and `INDEX.md` / `AGENTS.md` synchronized so `check-arch-doc-sync.py` passes cleanly.

---

## 7. What Is Left to the Builder

- Exact CLI argument flags and helper class decomposition inside `scripts/review-cycle.py`.
- Exact wording of the 7 subagent prompt templates in `.agents/review-prompts/*.md`, provided they enforce `REVIEW.md` rules and emit the JSON schema in §5.6.
- Selection of the initial 4–6 seed fixtures in `tests/review-evals/must-flag/` and `tests/review-evals/must-pass/`.
