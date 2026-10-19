# nacfson_pipeline Agent Guide

## 1. Project Context

`nacfson_pipeline` is a declarative, multi-tenant Kubernetes platform for running personal projects on modest compute resources (Oracle Cloud Always Free ARM64 VPS in `vps-k3s` and local Linux K3s in `local-k3s`, with portability to EKS/GKE).

Core platform pillars:
- **Continuous Pull-Based GitOps**: In-cluster FluxCD reconciles `deploy/` and `clusters/` in explicit dependency order (`cni` / `governance` → `vault` / `cert-manager` / `project-boundaries` → `vault-proxies` / `database` / `projects` → `identity` → `ingress` → `tls`).
- **Workload Isolation & $1/n$ Budgeting**: Project namespaces enforce Kubernetes `Restricted` Pod Security, `ClusterIP`-only services, strict Guaranteed QoS (`requests == limits`), and deterministic $1/n$ compute slicing enforced by [`scripts/platform-preflight.py`](scripts/platform-preflight.py).
- **Decoupled Project Door & Stdlib Auth Gateway**: Visitors reach projects directly through their own routes (`mode: none` or `mode: shared` in `sign-in.yaml`). Shared authentication is handled by a zero-dependency Go standard-library gateway (`gateway/`) and Keycloak Google OIDC brokering.
- **Zero-Plaintext Internal Cluster Vault**: Backend credentials (DB passwords, GHCR tokens, OAuth secrets, signing keys) live exclusively inside self-hosted OpenBao (`deploy/platform/vault/`) and execute via in-cluster Vault proxies (`services/vault-proxies/`).
- **Platform-Owned Edge TLS**: Public TLS terminates at Traefik using a single platform `TLSStore` and cert-manager inventory in `deploy/platform/tls/`.

---

## 2. Primary Documentation Index

Always consult these top-level indexes before exploring or modifying the codebase:

| Document | Purpose |
| :--- | :--- |
| [`.specify/memory/constitution.md`](.specify/memory/constitution.md) | **Supreme Authority (v2.2.0)**: Core principles (I–VII), security constraints, quality gates, and amendments. |
| [`docs/platform-architecture.html`](docs/platform-architecture.html) | **Canonical Interactive Architecture Handbook (Hook-Enforced)**: Visual topology, 7 end-to-end flows, CNI/eBPF packet simulator, Gateway allow-vs-block simulator, 10-layer Flux DAG, OSS catalog, $1/n$ budget calculator, and junior-engineer guide. |
| [`contracts/README.md`](contracts/README.md) | **Canonical Contract Catalog (`CONTRACT_*.md`)**: Consolidated, preflight-enforced contracts for [`CONTRACT_DATABASE.md`](contracts/CONTRACT_DATABASE.md), [`CONTRACT_PROBES_READINESS.md`](contracts/CONTRACT_PROBES_READINESS.md), [`CONTRACT_AUTH_DOOR.md`](contracts/CONTRACT_AUTH_DOOR.md), [`CONTRACT_TLS_INGRESS.md`](contracts/CONTRACT_TLS_INGRESS.md), and [`CONTRACT_ISOLATION_BUDGET.md`](contracts/CONTRACT_ISOLATION_BUDGET.md). |
| [`INDEX.md`](INDEX.md) | **Master Repository & Doc Map**: Complete catalog of `contracts/`, `docs/`, `specs/001..010`, `deploy/`, `clusters/`, `gateway/`, `services/`, `scripts/`, and `tests/`. |
| [`ARCHITECTURE.md`](ARCHITECTURE.md) | **System Topology & Data Flows**: Mermaid architecture diagrams, Flux GitOps layer DAG, TLS termination, auth routing, Vault proxy boundary, and probe roles. |
| [`DEPENDENCIES.md`](DEPENDENCIES.md) | **Dependencies, Pinned Versions & Tenant Contracts**: Go stdlib-only rule, pinned infra/image tags, CI toolchains, capacity envelopes, and `Project_PN` integration contracts. |
| [`README.md`](README.md) & [`SPEC.md`](SPEC.md) | **Onboarding Portal & Baseline Spec**: High-level traceability matrix, quickstart progression, and original platform specification baseline. |
| [`REVIEW.md`](REVIEW.md) & [`CLAUDE.md`](CLAUDE.md) | **Code Review Rulebook & Multi-Agent Entrypoint**: Severity taxonomy (`Important`, `Security`, `Nit`, `Pre-existing`), `file:line` evidence requirement, 2-nit cap, 3-round convergence limit, and Claude/multi-agent entrypoint. |
| Direct push to `main` | **Rejected** by the `main_PR` ruleset. Land every change through a pull request. Never `git push` to `main`. |

---

## 3. Task Routing (Read Before Editing)

Before modifying code, manifests, or scripts, read the corresponding domain documents listed below:

- **Constitutional invariants, admission rules, or $1/n$ resource budgeting (`scripts/platform-preflight.py`, `clusters/*/capacity.yaml`, `deploy/platform/governance/`)**:
  - [`.specify/memory/constitution.md`](.specify/memory/constitution.md)
  - [`contracts/CONTRACT_ISOLATION_BUDGET.md`](contracts/CONTRACT_ISOLATION_BUDGET.md)
  - [`ARCHITECTURE.md`](ARCHITECTURE.md) (§3.4)
  - [`specs/004-flux-gitops-reconciliation/contracts/platform-preflight.md`](specs/004-flux-gitops-reconciliation/contracts/platform-preflight.md)
  - [`specs/004-flux-gitops-reconciliation/contracts/manual-change-policy.md`](specs/004-flux-gitops-reconciliation/contracts/manual-change-policy.md)

- **CNI Networking, Cilium eBPF Datapath, `CiliumNetworkPolicy`, or Hubble Observability (`deploy/platform/cni/`, `deploy/platform/vault/networkpolicy.yaml`, `scripts/open-hubble-ui.sh`)**:
  - [`specs/010-cilium-hubble-observability/spec.md`](specs/010-cilium-hubble-observability/spec.md)
  - [`specs/010-cilium-hubble-observability/contracts/`](specs/010-cilium-hubble-observability/contracts/) (`cni-layer-and-bootstrap.md`, `openbao-apiserver-egress.md`, `hubble-observability-and-tunnel.md`)

- **Authentication Gateway (`gateway/`), Keycloak (`deploy/platform/identity/`), OIDC redirects, session validation, or online logout**:
  - [`contracts/CONTRACT_AUTH_DOOR.md`](contracts/CONTRACT_AUTH_DOOR.md)
  - [`docs/architecture-auth-routing.md`](docs/architecture-auth-routing.md)
  - [`docs/auth-gitops-faq.md`](docs/auth-gitops-faq.md)
  - [`docs/project-auth-verification.md`](docs/project-auth-verification.md)
  - [`specs/001-project-platform/contracts/gateway-forwardauth.md`](specs/001-project-platform/contracts/gateway-forwardauth.md) & [`session-revocation.md`](specs/001-project-platform/contracts/session-revocation.md)
  - [`specs/006-separate-project-auth/`](specs/006-separate-project-auth/) & [`specs/009-host-scoped-shared-auth/spec.md`](specs/009-host-scoped-shared-auth/spec.md)

- **Edge TLS, cert-manager, Let's Encrypt, or Traefik `IngressRoute` / `TLSStore` (`deploy/platform/tls/`, `deploy/platform/ingress/`, `deploy/platform/cert-manager/`)**:
  - [`contracts/CONTRACT_TLS_INGRESS.md`](contracts/CONTRACT_TLS_INGRESS.md)
  - [`docs/tls-certificates.md`](docs/tls-certificates.md)
  - [`specs/008-platform-tls-termination/spec.md`](specs/008-platform-tls-termination/spec.md)
  - [`specs/008-platform-tls-termination/contracts/`](specs/008-platform-tls-termination/contracts/) (`certificate-inventory.md`, `project-route-tls.md`, `preflight-tls-rules.md`)

- **OpenBao Cluster Vault (`deploy/platform/vault/`), Database (`deploy/platform/database/`), Vault Proxies (`services/vault-proxies/`), or credential rotation/backup (`scripts/vault-*.sh`)**:
  - [`contracts/CONTRACT_DATABASE.md`](contracts/CONTRACT_DATABASE.md)
  - [`docs/spec002-vault-guide.md`](docs/spec002-vault-guide.md)
  - [`specs/002-internal-cluster-vault/spec.md`](specs/002-internal-cluster-vault/spec.md) & [`contracts/`](specs/002-internal-cluster-vault/contracts/)
  - [`specs/005-post-vault-automation/spec.md`](specs/005-post-vault-automation/spec.md)

- **FluxCD GitOps reconciliation layers (`clusters/local-k3s/`, `clusters/vps-k3s/`), drift recovery, or break-glass operations**:
  - [`docs/operations-runbook.md`](docs/operations-runbook.md)
  - [`specs/004-flux-gitops-reconciliation/contracts/reconciliation-layers.md`](specs/004-flux-gitops-reconciliation/contracts/reconciliation-layers.md)
  - [`specs/004-flux-gitops-reconciliation/contracts/operations.md`](specs/004-flux-gitops-reconciliation/contracts/operations.md)

- **Host VM preparation & K3s Ansible bootstrap (`bootstrap/`)**:
  - [`docs/bootstrap-scope.md`](docs/bootstrap-scope.md)
  - [`specs/003-minimal-vm-bootstrap/contracts/bootstrap.md`](specs/003-minimal-vm-bootstrap/contracts/bootstrap.md)

- **Hosted Tenant Workloads / Project PN (`deploy/projects/pn/`, `clusters/*/projects/proj-pn.yaml`), probes, or browser API CORS**:
  - [`DEPENDENCIES.md`](DEPENDENCIES.md) (§5 Hosted Tenant Project Contracts)
  - [`docs/project-auth-verification.md`](docs/project-auth-verification.md)
  - [`specs/006-separate-project-auth/contracts/`](specs/006-separate-project-auth/contracts/) (`project-door.md`, `sign-in-choice.md`, `session-return.md`)
  - [`specs/007-pn-readiness-restart/contracts/`](specs/007-pn-readiness-restart/contracts/) (`probe-matrix.md`, `disconnect-signal.md`, `browser-api-access.md`)

---

## 4. Non-Negotiable Engineering Rules

1. **Zero Third-Party Go Dependencies**: [`gateway/`](gateway/) and [`services/vault-proxies/`](services/vault-proxies/) MUST build with the Go `1.22` standard library only. Never add external modules to `go.mod`.
2. **Zero Plaintext Secrets in Git or Pods**: Never commit secrets or mount backend credentials into application pods via Kubernetes `Secret` objects or env vars. The **only** permitted Kubernetes TLS Secrets are edge certificate keys in `platform` (`auth-tls`, `pn-tls`) and ACME account keys in `cert-manager` (Constitution Amendment 2.2.0).
3. **Platform-Owned TLS Only**: Project namespaces (`proj-*`) MUST NOT contain `Certificate`, `Issuer`, `TLSStore`, `TLSOptions`, or TLS `Secret` resources, and project `IngressRoute` manifests MUST NOT set `tls.secretName`.
4. **Restricted Pod Security & Guaranteed QoS**: All project containers MUST run non-root (`runAsNonRoot: true`), drop `ALL` capabilities, disable service-account token auto-mounting, use `ClusterIP` services only, and declare explicit `requests == limits` fitting within their $1/n$ project budget.
5. **Immutable Image References**: Third-party images MUST use pinned exact version tags (never `:latest` or floating tags); hosted project images (`deploy/projects/pn/`) MUST use immutable `@sha256:...` digests.
6. **Probe Separation & Browser CORS**: Dependent frontend/backend workloads MUST separate `startupProbe` (initial cluster hop to backend), `readinessProbe` (continuous cluster hop / disconnect signal), and `livenessProbe` (process-local only), declare `restartPolicy: Always`, and include `PUBLIC_APP_URL` inside backend `ALLOWED_ORIGINS`.
7. **Single-Copy Ingress Allowlist**: Public auth routes live only in [`deploy/platform/ingress/ingress-allowlist.yaml`](deploy/platform/ingress/ingress-allowlist.yaml). Flux `postBuild` substitution is permitted only on the `ingress` layer for `AUTH_HOST`.
8. **Mandatory Architecture Handbook Synchronization & Multi-Agent Hook Gate**: Whenever any file under `deploy/`, `clusters/`, `gateway/`, `services/`, `bootstrap/`, `contracts/`, `specs/`, `.specify/memory/constitution.md`, `scripts/platform-preflight.py`, `scripts/render-invariants.py`, `ARCHITECTURE.md`, or `DEPENDENCIES.md` is modified, [`docs/platform-architecture.html`](docs/platform-architecture.html) MUST be updated in the same change to keep topology nodes, protocol flows, CNI/eBPF rules, Gateway allow/block scenarios, Flux DAG layers, OSS versions, and $1/n$ budget numbers synchronized. This rule is enforced automatically by [`scripts/check-arch-doc-sync.py`](scripts/check-arch-doc-sync.py), [`.githooks/pre-commit`](.githooks/pre-commit), and [`scripts/universal-agent-hook.py`](scripts/universal-agent-hook.py) across Antigravity ([`.agents/hooks.json`](.agents/hooks.json)), Claude Code ([`.claude/settings.json`](.claude/settings.json)), OpenAI Codex ([`.codex/hooks.json`](.codex/hooks.json)), Oh-my-pi ([`.omp/extensions/arch-doc-sync.ts`](.omp/extensions/arch-doc-sync.ts)), OpenCode ([`.opencode/plugins/arch-doc-sync.js`](.opencode/plugins/arch-doc-sync.js)), Grok Build ([`.grok/hooks.json`](.grok/hooks.json)), and Cursor ([`.cursor/hooks.json`](.cursor/hooks.json)).

---

## 5. Verification & Quality Gates

Run the relevant validation commands after making changes:

```bash
# 1. Go Auth Gateway unit & contract tests
(cd gateway && go test -v ./... && go vet ./...)

# 2. Go Vault Proxies build & vet check
(cd services/vault-proxies && go test -v ./... && go vet ./...)

# 3. Platform preflight unit tests (1/n budget, QoS, isolation, door & TLS rules)
python3 -m unittest discover tests/preflight

# 4. Full Kustomize render, kubeconform schema validation & preflight gate (local-k3s & vps-k3s)
./scripts/render-validate.sh local-k3s
./scripts/render-validate.sh vps-k3s

# 5. Canonical Architecture Handbook synchronization & invariant check
python3 scripts/check-arch-doc-sync.py --worktree

# 6. Multi-Agent Review Cycle verification, risk classification, eval suite & fast rollback
python3 -m unittest discover tests/review_cycle
python3 scripts/review-cycle.py verify --scope staged
python3 scripts/review-cycle.py classify-risk --scope branch
python3 scripts/review-cycle.py eval
./scripts/rollback-change.sh --dry-run HEAD
```

