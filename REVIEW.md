# Code Review & Verification Rulebook (`REVIEW.md`)

This rulebook governs every local, pre-commit, PR, and Merge Queue review pass across all agent harnesses (`agy`, `omp`, `opencode`, `claude`, `codex`, `grok`, `cursor`) and human reviewers in `nacfson_pipeline`.

---

## 1. Domain Failure Modes ("What Failure Looks Like Here")

Reviewers MUST focus on concrete failure mechanisms specific to each subsystem:

- **`gateway/` & `deploy/platform/identity/`**:
  - Authentication or session-revocation bypass, open redirects, missing OIDC audience/issuer/host checks, cross-host cookie leakage, or adding third-party Go dependencies to `go.mod` (violating the Go `1.22` standard-library-only rule).
- **`deploy/platform/vault/`, `deploy/platform/database/`, & `services/vault-proxies/`**:
  - Plaintext secrets or credentials committed to Git or mounted into pods via Kubernetes `Secret` objects or environment variables, broken OpenBao transit/DB proxy boundaries, or non-stdlib Go imports.
- **`deploy/platform/cni/`, `deploy/platform/tls/`, & `deploy/platform/ingress/`**:
  - Overly permissive egress (`0.0.0.0/0` or unrestricted `world` without explicit FQDN/port lock), tenant TLS `Secret`/`Certificate`/`Issuer`/`TLSStore` resources inside `proj-*` namespaces, setting `tls.secretName` on project `IngressRoute` manifests, or bypassing `deploy/platform/ingress/ingress-allowlist.yaml`.
- **`deploy/projects/` & `clusters/`**:
  - Breaking $1/n$ resource budgets, missing Guaranteed QoS (`requests == limits`), privileged or root containers (`runAsNonRoot: true` required), non-`ClusterIP` services, mutable image tags instead of immutable `@sha256:...` digests, or coupled liveness/readiness probes (`livenessProbe` must remain process-local while `startupProbe`/`readinessProbe` verify upstream hops).
- **`docs/`, `contracts/`, & `specs/`**:
  - Documentation or contract statements that contradict runtime manifests, `scripts/platform-preflight.py`, `scripts/render-invariants.py`, or `docs/platform-architecture.html`.

---

## 2. Severity Definitions

Every surviving finding MUST be assigned exactly one severity tag:

- **`Important`**: Blocking correctness bug, race condition, broken platform contract, $1/n$ budget/QoS violation, or behavioral regression introduced by the diff. Blocks commit and merge until fixed or explicitly resolved.
- **`Security`**: Blocking vulnerability, secret exposure, auth/OIDC/session bypass, privilege escalation, overly broad `CiliumNetworkPolicy` / RBAC rule, TLS boundary violation, or injection flaw. Blocks commit and merge until fixed or explicitly resolved.
- **`Nit`**: Non-blocking minor improvement with a concrete readability or maintainability benefit. Capped at **at most 2** per review pass and never blocks commit or merge.
- **`Pre-existing`**: Defect observed in adjacent untouched code that predates the current diff. Logged for visibility and never blocks the current change unless the diff worsens or depends on the defect.

---

## 3. Evidence, Signal & Convergence Rules

1. **Mandatory `file:line` Evidence**: Every finding MUST cite an exact `file:line` range (`line_start`–`line_end`) and explain the concrete failure mechanism. Never guess from variable or function names without reading the surrounding code context.
2. **Drop Style & Diagnostic Noise**: Formatting opinions, subjective naming preferences, and diagnostic/logging nitpicks MUST be dropped by the Verifier.
3. **Strict Nit Cap**: Report **at most 2** `Nit` findings per review pass. Drop lower-value nits beyond the top 2.
4. **Standard Summary Header**: Every review summary MUST begin with the exact finding counts in the format:
   `0 Important, 0 Security, N Nit, M Pre-existing`
   When `Important` and `Security` counts are both `0`, state plainly that nothing blocking needs fixing.
5. **Flag the Class of Bug**: When the same defect pattern appears in multiple places across the diff, group all occurrences into a single finding with a shared `class_of_bug` identifier so the author fixes the entire class in one round.
6. **Convergence Guardrails**: Automated review-fix cycles are capped at **3 rounds**. Round 1 reviews the full diff; Rounds 2 and 3 review only incremental changes plus open threads. Do not repeat findings already resolved (`false_positive`, `intentional`, `wont_fix`) at the same code location.

---

## 4. Skipped Paths

Automated review hunters MUST skip vendored upstream manifests unless a security gate specifically audits an upstream version upgrade:

- `deploy/platform/cert-manager/upstream/`
- `deploy/platform/cni/upstream/`
- `**/gotk-components.yaml`

---

## 5. Pre-Commit Gate

A `git commit` is blocked until the staged diff has a receipt with `"pipeline": "code-review"`. The code-review skill sets that field after its review stages. The agent hooks (`scripts/universal-agent-hook.py`, `.omp/extensions/arch-doc-sync.ts`, `.opencode/plugins/arch-doc-sync.js`) deny the commit and tell the agent to run [`.agents/skills/code-review/SKILL.md`](.agents/skills/code-review/SKILL.md) instead of calling `record-receipt` alone. The field is that skill's completion marker. The hook cannot prove subagents ran if the same agent writes the receipt. `.githooks/pre-commit` checks `review-cycle.py check-gate --scope staged --require-pipeline code-review`. `SKIP_REVIEW_GATE=1` is break-glass only.
