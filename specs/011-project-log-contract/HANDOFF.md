# Hand-off: Spec 011, Project Log Contract v3

**Written**: 2026-11-05 · **For**: the next AI agent taking over this feature · **Operator**: nacfson

Read this file first, then [`deferred-decisions.md`](deferred-decisions.md) §0 and §2b. Those two sections hold every decision made after the spec was first written.

---

## 1. Where everything is

| Item | Location |
|---|---|
| Worktree (work here) | `/home/nacfson/Projects/nacfson_pipeline/.worktrees/011-project-log-contract` |
| Branch | `011-project-log-contract`, based on `main` at `95af0f5` (PR #127). Nothing committed yet. |
| Spec folder (untracked, **current**) | `specs/011-project-log-contract/` inside the worktree |
| Stale copy (**do not use**) | `/home/nacfson/Projects/nacfson_pipeline/specs/011-project-log-contract/` in the main checkout (branch `fix/vps-cpu-budget-and-observe`, ~30 commits behind `main`). It predates the reconciliation and the 2026-11-05 decisions. Ask the operator before deleting it, after the worktree copy is committed. |
| Design conversation (Nov 4) | Claude Code session `fea1dc00-823a-4c62-ab2d-4770d664064b` (`claude --resume …` from the repo root). It ended at a usage limit mid-reconciliation. |
| Decision-review conversation (Nov 5) | T3 thread "Restore Spec 011 Implementation Chat" (this hand-off's source) |
| Repository rules | `AGENTS.md`, `REVIEW.md`, `.specify/memory/constitution.md`, `docs/platform-architecture.html` (via `CLAUDE.md`) |

## 2. What the feature is (one paragraph)

Hosted projects (`proj-pn`, `proj-health`, `proj-blog`) must print one JSON log line per event, with OpenTelemetry names (`time`, `level`, `msg`, `service.name`, `service.version`, `exception.*`, `trace_id`, `http.*`). Fluent Bit adds trusted platform fields (`k8s.*`, `log.iostream`, `observed_time`), parses JSON into fields, redacts secrets, trims stack traces to 8 KiB, and sends lines it can't refine to a **quarantine** tenant in VictoriaLogs (`0:1`) instead of dropping them. Gates stop non-conforming projects before deploy: preflight (manifests), a version-label PR check, `logcheck` in project CI (every image build), and the image-pin helper. The existing error detector (`services/error-pipeline`) reports violations, redaction markers, and quarantine to Discord.

## 3. Status

| Step | State |
|---|---|
| Spec, plan, research, data-model, contracts, tasks (speckit) | ✅ Written 2026-11-04 |
| Analysis findings (C1, H1, H2, M2–M8) | ✅ Resolved 2026-11-04 (`deferred-decisions.md` §1) |
| Reconciliation with `main` (R-1 to R-9) | ⚠️ **Approved but only partly applied**, see §5 |
| Operator review of deferred decisions | ✅ Done 2026-11-05 (`deferred-decisions.md` §0), applied to spec/contracts/data-model/plan/tasks |
| `/speckit-analyze` re-run | ❌ Not done |
| Implementation (PR 1 onward) | ❌ Not started. 77+ tasks, all unchecked. |

## 4. Decisions made by the operator on 2026-11-05 (already written into the files)

| # | Decision |
|---|---|
| D1 | Untagged builds may run **everywhere** (VPS too). They print `v0.0.0-<short commit>`; tag builds print the plain tag. `logcheck` (the `log-contract` CI job) runs on **every** image build, and the pin helper looks its result up by revision commit. No image is exempt. A plain `v0.0.0` (local build outside Git) can't be pinned. The old `log-version-untagged` preflight rule and the manual `local-k3s` pin path are removed. |
| D6 | `exception.stacktrace` required on `fatal` only (optional on `error`), **capped at 8 KiB, top kept, bottom cut**, marker `…[truncated N bytes]`. `logcheck` reports `stacktrace-too-long`; the collector cuts it before storage (new FR-018c). |
| D19 | **The detector gets no Kubernetes access and does no runtime version comparison.** Alerts take the version from the log line. The manifest label `app.kubernetes.io/version` is verified **on the PR, never in the cluster**: new FR-024a / Gate 1b / task T043a (`scripts/check-version-labels.py`, `skopeo inspect`, rule `log-version-label-mismatch`). |
| D22 | Delivery order (plan § Delivery Order): **1** contract v3 §2–§6 + `internal/conform` + `cmd/logcheck` → **2** detector reads v2 **and** v3 (built, then pinned *before* 3) → **3** Fluent Bit v3 (the only PR touching live logs) → **4** manifest gate + version-label PR check → **5** image-pin gate → **6** violation reporting + quarantine alert → **7+** per-project migrations (PN, health, blog), then removal of the detector's v2 read. |
| D17/D18 | Collector stays `50m` CPU / `64Mi`. Load test (100 lines/s, then 1,000 lines/s for 10 s). If it is over, **stop and ask**. |
| Q1 | **Immediate quarantine alert**: the first quarantined line from a `v3/*` container alerts at once; then at most one per container per hour with a count (FR-025, US4-4). |

All other items in `deferred-decisions.md` §1–§2 stay as the agent's defaults. The operator explicitly chose not to revisit them.

## 5. Remaining reconciliation work (do this first)

On 2026-11-04 the operator approved R-1 to R-9 (`deferred-decisions.md` §2b): **extend the existing code on `main` instead of duplicating it.** Only `deferred-decisions.md`, part of `research.md` (R5 redaction), `plan.md` § Delivery Order, and the D1/D6/D19/Q1 edits are done. Still to apply:

| Change | Where it's still stale |
|---|---|
| **R-1** No new module. Use `services/error-pipeline/internal/conform`, `cmd/logcheck`, reusing `internal/classify` and `internal/redact` | `tasks.md` T005–T012, T032–T035, T050, T052, T057 still say `services/logcontract/…`; `plan.md` Project Structure + Complexity Tracking; `research.md` R10; `data-model.md` §8 |
| **R-2** No `schema/v3.json`; the contract Markdown is the single source, checked by Go drift tests (pattern: `classify/contract_test.go`) and a Python test vs the Lua tables | `tasks.md` T005, T006, T008, T015; `collector-pipeline.md` invariant "equal `schema/v3.json`"; `data-model.md` §8 |
| **R-3** Redaction reuses the existing `internal/redact` patterns (broader). Marker becomes `[REDACTED:<kind>]`. Vectors at `services/error-pipeline/internal/redact/testdata/vectors.json` | `spec.md` FR-017 pattern list; `tasks.md` T006, T007, T009, T011, T050, T052 |
| **R-4** gitleaks allowlist = exact strings in `.gitleaks.toml` (existing style), not paths | `tasks.md` T007a |
| **R-5** Detector dual-read is its own PR, deployed before the collector switch | `tasks.md` phases (add a detector dual-read phase between PR 1 and PR 3) |
| **R-6** FR-025 is built in this feature; SC-007/SC-008 verified here | `spec.md` SC-007, SC-008, US4 "depends on workstream 1" text, Assumptions; `tasks.md` Phase 6 needs detector implementation tasks, not only doc rewrites (T056–T058 are docs) |
| **R-7** Add `proj-blog` (container `blog-web`, Go `log` plain text): 5 legacy containers, annotation, migration row, pin | `spec.md` Background, FR-026, SC-006 ("four"), Assumptions; `tasks.md` T013 §6 rows, T020 legacy list, T040–T042, Phase 7 (no blog tasks) |
| **R-8** `scripts/image-pin.py` `diff_check` must allow the `app.kubernetes.io/version` line | `tasks.md` Phase 5 (no task yet) |
| **R-9** Contract v3 §2–§6 in PR 1; §1 (platform fields) in the collector PR | `tasks.md` T013 |

Also search `research.md` (R8, R9, R12, R13) and `quickstart.md` for leftovers of the **old** rules: `v0.0.0` only in `local-k3s`, `log-version-untagged`, detector pod RBAC / `version-mismatch` at runtime, `services/logcontract`.

Then run `/speckit-analyze` and fix what it reports. Then start PR 1 (TDD; see `superpowers:test-driven-development` if available).

## 6. Things to watch (told to the operator)

1. **PR 2 before PR 3.** If the collector renames fields before the dual-read detector image is pinned, Discord alerts stop silently. After PR 3 deploys, confirm one test error still reaches Discord.
2. **Collector CPU** on the 2-OCPU VPS: measure; don't raise limits without asking.
3. **Redaction misses** keep a secret stored for 30 days. Over-redaction is accepted.
4. **Quarantine blind spot** is covered by the Q1 alert. Build it in PR 6.
5. Records stored before PR 3 keep v2 field names for 30 days. The runbook must say so.

## 7. Open checks during implementation (not decisions)

| Check | Task |
|---|---|
| Can vmui v1.53.0 select a tenant? If not, document `curl -H 'ProjectID: 1'` | T055 |
| Does `render-validate.sh` apply the per-cluster Flux patches before preflight? | T043 |
| Which release tags the currently pinned images came from; if no version label, ask the operator | T040–T042 |
| Can this repo's CI read the GHCR project images for Gate 1b? If not, package read access or a read-only token from the operator | T043a |

## 8. Rules for working with this operator

- **Never SSH to the VPS.** Give the operator read-only commands to run instead.
- Commit, push, or open PRs **only when asked**. Run the `CLAUDE.md` / `AGENTS.md` §5 verification gates before claiming anything is done.
- Explain in **plain language, concept first, then a concrete example**, then the choice. Diagrams help. Avoid unexplained jargon and long walls of text.
- Ask only about choices that are genuinely the operator's (policy, cost, delivery). Recommend a default and say why. The operator tires of many decisions.
- When a plan written earlier no longer matches the code, say so and propose the fix before coding. Don't silently redesign.

## 9. Environment quirk

The repo's `PreToolUse` hook runs `python3 scripts/universal-agent-hook.py` with a **relative** path. If the shell's working directory is not a repository root (e.g. inside `specs/011-project-log-contract/`), **every Bash call fails**. Keep the shell in the worktree root (`/home/nacfson/Projects/nacfson_pipeline/.worktrees/011-project-log-contract`).
