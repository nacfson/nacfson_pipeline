# nacfson_pipeline Documentation & Repository Index

This file is the master navigation index for **`nacfson_pipeline`**. For AI agent workflow rules and task-based routing, start at [`AGENTS.md`](AGENTS.md).

---

## 1. Documentation Tiers & Precedence

| Tier | Document(s) | Role & Authority |
| :--- | :--- | :--- |
| **Tier 1: Supreme Governance** | [`.specify/memory/constitution.md`](.specify/memory/constitution.md) | **Supreme Authority (v2.2.1)**. Non-negotiable security, isolation, GitOps, TLS, probe, and $1/n$ resource invariants. Any conflicting manifest or code change MUST fail review. The observability reservation is not yet measured and must not be added by hand. |
| **Tier 2: Canonical Contracts & System Portals** | [`contracts/README.md`](contracts/README.md) (`CONTRACT_*.md`), [`AGENTS.md`](AGENTS.md), [`CLAUDE.md`](CLAUDE.md), [`REVIEW.md`](REVIEW.md), [`INDEX.md`](INDEX.md), [`ARCHITECTURE.md`](ARCHITECTURE.md), [`DEPENDENCIES.md`](DEPENDENCIES.md), [`README.md`](README.md), [`SPEC.md`](SPEC.md) | Canonical consolidated contracts ([`CONTRACT_DATABASE.md`](contracts/CONTRACT_DATABASE.md), [`CONTRACT_PROBES_READINESS.md`](contracts/CONTRACT_PROBES_READINESS.md), [`CONTRACT_AUTH_DOOR.md`](contracts/CONTRACT_AUTH_DOOR.md), [`CONTRACT_TLS_INGRESS.md`](contracts/CONTRACT_TLS_INGRESS.md), [`CONTRACT_ISOLATION_BUDGET.md`](contracts/CONTRACT_ISOLATION_BUDGET.md), [`CONTRACT_LOG_SCHEMA.md`](contracts/CONTRACT_LOG_SCHEMA.md)), agent router, code review rulebook, and system topology. |
| **Tier 3: Feature Specifications** | [`specs/001-*` … `specs/009-*`](#3-feature-specifications-specs) | SpecKit packages (`spec.md`, `plan.md`, `tasks.md`, `data-model.md`, `contracts/`, `validation.md`) defining feature requirements and protocol contracts. |
| **Tier 4: Operations & Deep-Dive Guides** | [`docs/`](#2-operational-guides--visual-references-docs) | Day-2 operational runbooks, subsystem deep dives, FAQs, and interactive HTML visualizers. |

---

## 2. Operational Guides & Visual References (`docs/`)

### Markdown Guides

| Document | Topic & When to Read |
| :--- | :--- |
| [`docs/architecture-auth-routing.md`](docs/architecture-auth-routing.md) | End-to-end walkthrough of how Git changes reach the cluster, how Traefik routes `pn.nacfson.cloud` vs `auth.nacfson.cloud`, and how the Go auth gateway + Keycloak sign-in flow works. |
| [`docs/auth-gitops-faq.md`](docs/auth-gitops-faq.md) | Troubleshooting FAQ for common points of confusion across the Go gateway, Keycloak, Traefik middlewares, and Flux GitOps reconciliation. |
| [`TROUBLESHOOTING.md`](TROUBLESHOOTING.md) & [`TROUBLE_SHOOTING.md`](TROUBLE_SHOOTING.md) | Production troubleshooting runbooks covering 2-OCPU node CPU exhaustion (`pn-backend` `0/1 Pending` / `503`), `open-observe.sh` 3-UI tunnel auto-reconnect, `postgres-init-job` recovery, zero-data-loss OpenBao Unseal Key regeneration, OAuth callback confirmation (`401`), and the Main-Domain Naming Rule. |
| [`docs/issues/index.md`](docs/issues/index.md) | Filed GitHub issues. One `docs/issues/FIX_GithubIssue<number>.md` per issue. OpenCode refreshes this index on project open. |
| [`docs/project-auth-verification.md`](docs/project-auth-verification.md) | Contract guide for hosted tenant applications (e.g., `Project_PN`) on verifying platform identity, `(issuer, subject)` resolution, and fail-closed authorization. |
| [`docs/tls-certificates.md`](docs/tls-certificates.md) | Platform-owned edge TLS architecture (cert-manager + Let's Encrypt HTTP-01 solver + Traefik default `TLSStore` in `platform` namespace). |
| [`docs/operations-runbook.md`](docs/operations-runbook.md) | Day-2 GitOps runbook: Flux layer inspection, suspend/resume break-glass procedures, drift recovery, incident triage, and project log history (section 15). |
| [`docs/spec002-vault-guide.md`](docs/spec002-vault-guide.md) | Practical guide to the OpenBao zero-plaintext cluster Vault, `db-proxy`, `registry-proxy`, Transit signing, and credential rotation lifecycle. |
| [`docs/bootstrap-scope.md`](docs/bootstrap-scope.md) | Scope and input contract for single-VM Ansible host preparation and K3s installation (`bootstrap/`). |

### Interactive Visual Artifacts (`docs/*.html` & `agy_plan/html/`)

| File | Description |
| :--- | :--- |
| [`docs/platform-architecture.html`](docs/platform-architecture.html) | **Canonical Interactive Architecture Handbook (Hook-Synced)**: 8-tab interactive guide covering cluster topology, 8 end-to-end flows, CNI & eBPF packet path simulator, Gateway allow-vs-block decision simulator, 13-layer Flux GitOps DAG, 18-component OSS catalog, live $1/n$ resource budget calculator, and junior-engineer mental models + failure drills. |
| [`docs/ghcr-token-lifecycle.html`](docs/ghcr-token-lifecycle.html) | Interactive diagram of the zero-plaintext GHCR image pull token lifecycle via `registry-proxy` and OpenBao. |
| [`docs/internal-login-variations.html`](docs/internal-login-variations.html) | Visual comparison of browser login, redirect, and session cookie flows across projects and the auth gateway. |
| [`docs/internal-ai-gateway.html`](docs/internal-ai-gateway.html) | Interactive architecture guide and simulator for `vault-ai-proxy`, loopback (`127.0.0.1:8081`) direct RAM kill-switch access, Kubernetes pod identity verification, and GitOps verification gates. |
| [`agy_plan/html/`](agy_plan/html/) | Self-contained HTML implementation plans generated during Spec-Driven Development. |

---

## 3. Feature Specifications (`specs/`)

Each directory under `specs/` is a SpecKit feature package containing `spec.md` (requirements), `plan.md` (technical design), `tasks.md` (ordered implementation tasks), and `contracts/` (protocol & validation contracts).

| Spec Package | Summary & Key Contracts |
| :--- | :--- |
| [`specs/001-project-platform/`](specs/001-project-platform/) | **Core Platform & Workload Sandboxing**: Traefik ingress, Go stdlib auth gateway, Keycloak Google OIDC brokering, Restricted PSS, and 1/n resource budgets.<br/>Contracts: [`gateway-forwardauth.md`](specs/001-project-platform/contracts/gateway-forwardauth.md), [`session-revocation.md`](specs/001-project-platform/contracts/session-revocation.md) |
| [`specs/002-internal-cluster-vault/`](specs/002-internal-cluster-vault/) | **Internal Cluster Vault (OpenBao)**: Zero-plaintext secret storage and proxy execution boundary.<br/>Contracts: [`database-proxy.md`](specs/002-internal-cluster-vault/contracts/database-proxy.md), [`registry-proxy.md`](specs/002-internal-cluster-vault/contracts/registry-proxy.md), [`database-provisioning-backup.md`](specs/002-internal-cluster-vault/contracts/database-provisioning-backup.md), [`google-oauth-broker.md`](specs/002-internal-cluster-vault/contracts/google-oauth-broker.md), [`transit-signing.md`](specs/002-internal-cluster-vault/contracts/transit-signing.md) |
| [`specs/003-minimal-vm-bootstrap/`](specs/003-minimal-vm-bootstrap/) | **Minimal VM Bootstrap**: Idempotent Ansible playbook for host preflight, pinned K3s installation, and readiness checks.<br/>Contracts: [`bootstrap.md`](specs/003-minimal-vm-bootstrap/contracts/bootstrap.md) |
| [`specs/004-flux-gitops-reconciliation/`](specs/004-flux-gitops-reconciliation/) | **Flux GitOps Reconciliation**: In-cluster pull-based FluxCD controllers, dependency-ordered layers, and CI `platform-preflight` gating.<br/>Contracts: [`reconciliation-layers.md`](specs/004-flux-gitops-reconciliation/contracts/reconciliation-layers.md), [`platform-preflight.md`](specs/004-flux-gitops-reconciliation/contracts/platform-preflight.md), [`manual-change-policy.md`](specs/004-flux-gitops-reconciliation/contracts/manual-change-policy.md), [`bootstrap-reconciler-stage.md`](specs/004-flux-gitops-reconciliation/contracts/bootstrap-reconciler-stage.md), [`operations.md`](specs/004-flux-gitops-reconciliation/contracts/operations.md) |
| [`specs/005-post-vault-automation/`](specs/005-post-vault-automation/) | **Post-Vault Workload Orchestration**: Automated CI image publishing and post-unseal workload secret/proxy wiring.<br/>Contracts: [`ci-image-publishing.md`](specs/005-post-vault-automation/contracts/ci-image-publishing.md), [`workload-secret-injection.md`](specs/005-post-vault-automation/contracts/workload-secret-injection.md) |
| [`specs/006-separate-project-auth/`](specs/006-separate-project-auth/) | **Separate Project Entry from Sign-In (Constitution 2.0.0)**: Projects open via their own door (`none` vs `shared` sign-in declaration); gateway confirms sessions on demand instead of blocking public routes.<br/>Contracts: [`constitution-amendment.md`](specs/006-separate-project-auth/contracts/constitution-amendment.md), [`project-door.md`](specs/006-separate-project-auth/contracts/project-door.md), [`sign-in-choice.md`](specs/006-separate-project-auth/contracts/sign-in-choice.md), [`session-return.md`](specs/006-separate-project-auth/contracts/session-return.md) |
| [`specs/007-pn-readiness-restart/`](specs/007-pn-readiness-restart/) | **Project PN Probes, Restart Policy & Browser API CORS (Constitution 2.1.0)**: Separate `startupProbe` / `readinessProbe` / `livenessProbe` roles and `PUBLIC_APP_URL` inside `ALLOWED_ORIGINS`.<br/>Contracts: [`probe-matrix.md`](specs/007-pn-readiness-restart/contracts/probe-matrix.md), [`disconnect-signal.md`](specs/007-pn-readiness-restart/contracts/disconnect-signal.md), [`browser-api-access.md`](specs/007-pn-readiness-restart/contracts/browser-api-access.md) |
| [`specs/008-platform-tls-termination/`](specs/008-platform-tls-termination/) | **Platform-Owned TLS Termination (Constitution 2.2.0)**: Centralizes all edge certificates and `TLSStore` in `platform`; forbids project-namespace TLS resources.<br/>Contracts: [`certificate-inventory.md`](specs/008-platform-tls-termination/contracts/certificate-inventory.md), [`project-route-tls.md`](specs/008-platform-tls-termination/contracts/project-route-tls.md), [`preflight-tls-rules.md`](specs/008-platform-tls-termination/contracts/preflight-tls-rules.md) |
| [`specs/009-host-scoped-shared-auth/`](specs/009-host-scoped-shared-auth/) | **Host-Scoped Session Cookies & Shared Project Auth**: Direct Google OIDC redirect without intermediate Keycloak login UI, host-scoped cookies, and shared verification contract.<br/>Spec: [`spec.md`](specs/009-host-scoped-shared-auth/spec.md) |
| [`specs/010-cilium-hubble-observability/`](specs/010-cilium-hubble-observability/) | **Cilium + Hubble CNI & Visual Platform Network Observability**: Git-vendored Cilium `v1.20.3` + Hubble Relay + Hubble UI `v0.13.6`, K3s Flannel replacement, OpenBao `CiliumNetworkPolicy` (`toEntities: ["kube-apiserver"]`), and private loopback tunnel (`scripts/open-hubble-ui.sh`).<br/>Contracts: [`cni-layer-and-bootstrap.md`](specs/010-cilium-hubble-observability/contracts/cni-layer-and-bootstrap.md), [`openbao-apiserver-egress.md`](specs/010-cilium-hubble-observability/contracts/openbao-apiserver-egress.md), [`hubble-observability-and-tunnel.md`](specs/010-cilium-hubble-observability/contracts/hubble-observability-and-tunnel.md) |

---

## 4. Repository Directory & Code Map

### `bootstrap/` — Host VM & K3s Ansible Automation
- [`bootstrap/bootstrap.yml`](bootstrap/bootstrap.yml) — Main entrypoint playbook (`request_validation` → `preflight` → `install` → `verify` → `reconciler`).
- [`bootstrap/tasks/preflight.yml`](bootstrap/tasks/preflight.yml) — Host OS, CPU/RAM, swap, port, and controller Flux/CNI manifest inspections.
- [`bootstrap/tasks/install.yml`](bootstrap/tasks/install.yml) — Pinned K3s server installation (`--flannel-backend=none --disable-network-policy`), non-root kubeconfig setup, and Cilium CNI bootstrap application.
- [`bootstrap/tasks/verify.yml`](bootstrap/tasks/verify.yml) — Systemd, API server, Cilium CNI, node, and CoreDNS readiness checks.
- [`bootstrap/tasks/reconciler.yml`](bootstrap/tasks/reconciler.yml) — FluxCD reconciler bootstrap on the cluster.

### `clusters/` — FluxCD Environment Entrypoints
- [`clusters/local-k3s/`](clusters/local-k3s/) — Local Linux K3s environment (`capacity.yaml`, `layers.yaml`, `flux-system/`, `projects/proj-pn.yaml`).
- [`clusters/vps-k3s/`](clusters/vps-k3s/) — Production Oracle Cloud ARM64 VPS environment (`capacity.yaml`, `layers.yaml`, `flux-system/`, `projects/proj-pn.yaml`).

### `deploy/` — Kubernetes Base Manifests
- [`deploy/platform/cni/`](deploy/platform/cni/) — Upstream Cilium `v1.20.3` + Hubble Relay `v1.20.3` + Hubble UI `v0.13.6` manifest bundle (`upstream/cilium.yaml`) and Kustomization resource overlays (`kustomization.yaml`).
- [`deploy/platform/governance/`](deploy/platform/governance/) — Namespace definitions (`platform`, `identity`, `vault`), default-deny NetworkPolicies, `ValidatingAdmissionPolicy` (`manual-change-policy.yaml`), break-glass & operator RBAC.
- [`deploy/platform/vault/`](deploy/platform/vault/) — OpenBao StatefulSet, service, network policy (including `CiliumNetworkPolicy/openbao-apiserver-egress` and `openbao-backup-policy`), backup CronJob, and HCL policies.
- [`deploy/platform/vault-proxies/`](deploy/platform/vault-proxies/) — In-cluster `db-proxy` and `registry-proxy` Deployments.
- [`deploy/platform/database/`](deploy/platform/database/) — Shared PostgreSQL StatefulSet, PVC, init job, and backup CronJob.
- [`deploy/platform/identity/`](deploy/platform/identity/) — Keycloak Deployment & realm config (`keycloak-realm-config.yaml`) and Go Auth Gateway Deployment (`gateway-deployment.yaml`).
- [`deploy/platform/ingress/`](deploy/platform/ingress/) — Traefik `IngressRoute` allowlist (`ingress-allowlist.yaml`), HTTP-to-HTTPS redirect, and middlewares.
- [`deploy/platform/cert-manager/`](deploy/platform/cert-manager/) — Upstream cert-manager `v1.21.2` manifest bundle.
- [`deploy/platform/tls/`](deploy/platform/tls/) — Let's Encrypt `ClusterIssuer`s (`cluster-issuers.yaml`), platform `Certificate` inventory (`certificates.yaml`), and default Traefik `TLSStore` (`tls-store.yaml`).
- [`deploy/platform/observability/`](deploy/platform/observability/) — Privileged namespace `observability`, Fluent Bit `4.2.8` DaemonSet (`proj-*` only, 1Gi tail PVC), VictoriaLogs `v1.53.0` ClusterIP store (10Gi PVC, 30 days, 8 GiB cap). The observability reservation is not yet measured and must not be added by hand.
- [`deploy/projects/pn/`](deploy/projects/pn/) — Project PN tenant manifests:
  - [`boundary/`](deploy/projects/pn/boundary/) — Namespace (`proj-pn` with Restricted PSS), `ResourceQuota`, `LimitRange`, RBAC, NetworkPolicy.
  - [`sign-in.yaml`](deploy/projects/pn/sign-in.yaml) — Project sign-in mode declaration (`mode: shared`).
  - [`workloads/`](deploy/projects/pn/workloads/) — Backend/frontend Deployments, Services, `IngressRoute`, and migration Job.

### `gateway/` — Go Standard-Library Authentication Gateway
- [`gateway/cmd/gateway/main.go`](gateway/cmd/gateway/main.go) — HTTP server entrypoint.
- [`gateway/internal/config/config.go`](gateway/internal/config/config.go) — Environment configuration loader.
- [`gateway/internal/auth/`](gateway/internal/auth/) — OAuth start/callback handlers (`handler.go`), session validation (`session_validator.go`), and CSRF-protected online logout/revocation (`logout.go`).
- [`gateway/internal/keycloak/`](gateway/internal/keycloak/) — OIDC token exchange/verification (`oidc.go`) and Keycloak session revocation client (`session.go`).
- [`gateway/tests/`](gateway/tests/) — Contract and unit tests (`contract_test.go`, `handler_test.go`, `logout_test.go`, `revocation_contract_test.go`).

### `services/vault-proxies/` — Zero-Plaintext Vault Proxy Services
- [`services/vault-proxies/cmd/db-proxy/`](services/vault-proxies/cmd/db-proxy/) — PostgreSQL wire/query proxy authenticating via OpenBao without exposing DB credentials to project pods.
- [`services/vault-proxies/cmd/registry-proxy/`](services/vault-proxies/cmd/registry-proxy/) — OCI registry pull proxy injecting GHCR credentials from OpenBao.

### `scripts/` & `tests/` — Validation, Preflight & Operations Scripts
- [`scripts/platform-preflight.py`](scripts/platform-preflight.py) — CI/pre-promotion static gate verifying $1/n$ resource budgets, QoS (`requests == limits`), Restricted PSS, door/sign-in contracts, database/readiness contracts, and platform-owned TLS rules.
- [`scripts/review-cycle.py`](scripts/review-cycle.py) & [`scripts/rollback-change.sh`](scripts/rollback-change.sh) — Multi-agent code review verification, risk classification (`Low`/`Medium`/`High`), receipt gate engine, eval runner, and fast rollback + circuit-breaker incident script governed by [`REVIEW.md`](REVIEW.md).
- [`scripts/check-arch-doc-sync.py`](scripts/check-arch-doc-sync.py) & [`scripts/universal-agent-hook.py`](scripts/universal-agent-hook.py) — Canonical architecture handbook (`docs/platform-architecture.html`) invariant verifier and universal multi-agent hook adapter (`.agents/hooks.json`, `.claude/settings.json`, `.codex/hooks.json`, `.omp/extensions/arch-doc-sync.ts`, `.opencode/plugins/arch-doc-sync.js`, `.grok/hooks.json`, `.cursor/hooks.json`, and `.githooks/pre-commit`).
- [`scripts/agent-start-git-fetch.py`](scripts/agent-start-git-fetch.py) — Non-mutating `git fetch` at AI agent session start (throttled, fail-open, never touches the working tree); registered through `SessionStart` in the agent hook configs and the `.omp/extensions/git-fetch-session-start.ts` / `.opencode/plugins/git-fetch-session-start.js` adapters.
- [`scripts/render-validate.sh`](scripts/render-validate.sh) & [`scripts/render-invariants.py`](scripts/render-invariants.py) — Kustomize rendering, `kubeconform` schema validation, and invariant checks across `local-k3s` and `vps-k3s`.
- [`scripts/open-hubble-ui.sh`](scripts/open-hubble-ui.sh) — Private loopback port-forward tunnel (`http://127.0.0.1:12000`) and `--verify` 5-point live Cilium + Hubble verification helper.
- [`scripts/open-victorialogs-ui.sh`](scripts/open-victorialogs-ui.sh) — Private loopback port-forward (`http://127.0.0.1:9428/select/vmui/`) and `--verify` for VictoriaLogs. Runbook section 15.
- [`scripts/open-observe.sh`](scripts/open-observe.sh) — One workstation session for `ssh oracleCloud`: API forward on `127.0.0.1:16443`, then Hubble `127.0.0.1:12000` and VictoriaLogs `127.0.0.1:9428`.
- [`scripts/db-migrate-dump.sh`](scripts/db-migrate-dump.sh) — Standardized, project-agnostic zero-plaintext PostgreSQL dump import (`--file`, `--mode full|data`) and export (`--export`) pipeline utility.
- [`scripts/smoke-auth-chain.sh`](scripts/smoke-auth-chain.sh) — End-to-end smoke test for the sign-in redirect and auth routing chain.
- [`scripts/vault-*.sh`](scripts/) — `vault-init.sh`, `vault-seed-inventory.sh`, `vault-reconcile.sh`, `vault-rotate.sh`, `vault-configure-k8s-auth.sh`, `vault-set-google-oauth.sh`.
- [`scripts/verify-*.sh`](scripts/) — Live cluster verification suites: `verify-platform.sh`, `verify-isolation.sh`, `verify-identity.sh`, `verify-persistence.sh`, `verify-revocation.sh`, `verify-backup.sh`, `verify-tls.sh`.
- [`tests/preflight/test_platform_preflight.py`](tests/preflight/test_platform_preflight.py) — Unit test suite and YAML fixtures (`tests/preflight/fixtures/`) for `platform-preflight.py`.
- [`tests/review_cycle/`](tests/review_cycle/) & [`tests/review-evals/`](tests/review-evals/) — Unit tests and `must-flag` / `must-pass` regression eval corpus for `scripts/review-cycle.py` and multi-agent review governance.
- [`tests/hooks/`](tests/hooks/) — Unit tests for the agent session-start remote refresh (`agent-start-git-fetch.py`) and universal hook `SessionStart` adapters.
- [`tests/vault/`](tests/vault/) — Integration and disaster recovery drills for OpenBao and Vault proxies.

