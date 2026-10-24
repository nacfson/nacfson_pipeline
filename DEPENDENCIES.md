# nacfson_pipeline Dependencies, Pinned Versions & Tenant Contracts

This document tracks all runtime dependency rules, pinned third-party infrastructure versions, operator/CI toolchains, and cross-project tenant contracts in **`nacfson_pipeline`**.

---

## 1. First-Party Go Services (Zero Third-Party Dependencies)

Per [Constitution Principle III](.specify/memory/constitution.md#L13-L14) and [Security Constraints](.specify/memory/constitution.md#L36), all in-repo Go services MUST use **pure Go standard library only** (`crypto/*`, `net/http`, `encoding/json`, `net/url`, `io`, etc.).

| Module | Path | Go Toolchain | Allowed External Modules | Published Image |
| :--- | :--- | :--- | :--- | :--- |
| `nacfson_pipeline/gateway` | [`gateway/go.mod`](gateway/go.mod) | `go 1.22` | **None** (`0` `require` directives) | `ghcr.io/nacfson/platform-auth-gateway` |
| `nacfson_pipeline/services/vault-proxies` | [`services/vault-proxies/go.mod`](services/vault-proxies/go.mod) | `go 1.22` | **None** (`0` `require` directives) | `ghcr.io/nacfson/vault-db-proxy`, `ghcr.io/nacfson/vault-registry-proxy`, `ghcr.io/nacfson/vault-ai-proxy` |

> [!IMPORTANT]
> Never run `go get <third-party-pkg>` or introduce external gateway frameworks, OAuth2 Proxy, or Redis into [`gateway/`](gateway/) or [`services/vault-proxies/`](services/vault-proxies/).

---

## 2. Pinned Infrastructure & Platform Versions

Per [Constitution Principle I](.specify/memory/constitution.md#L6-L7), third-party images MUST be pinned to exact upstream version tags (floating tags like `latest` or `16-alpine` on third-party images are prohibited), and hosted project images MUST be pinned by immutable `@sha256:...` digests.

| Component | Pinned Version / Image | Where Declared |
| :--- | :--- | :--- |
| **Kubernetes (K3s)** | `v1.35.9+k3s1` (Traefik bundled) | [`bootstrap/bootstrap.yml`](bootstrap/bootstrap.yml) (`accepted_k3s_versions`) |
| **FluxCD CLI / Distribution** | `2.9.5` | [`.github/workflows/gitops.yaml`](.github/workflows/gitops.yaml), [`clusters/*/flux-system/`](clusters/vps-k3s/flux-system/gotk-components.yaml) |
| **Flux Source Controller** | `ghcr.io/fluxcd/source-controller:v1.9.5` | [`clusters/*/flux-system/gotk-components.yaml`](clusters/vps-k3s/flux-system/gotk-components.yaml) |
| **Flux Kustomize Controller** | `ghcr.io/fluxcd/kustomize-controller:v1.9.5` | [`clusters/*/flux-system/gotk-components.yaml`](clusters/vps-k3s/flux-system/gotk-components.yaml) |
| **OpenBao (Cluster Vault)** | `openbao/openbao:2.0.0` | [`deploy/platform/vault/openbao-statefulset.yaml`](deploy/platform/vault/openbao-statefulset.yaml), [`backup-cronjob.yaml`](deploy/platform/vault/backup-cronjob.yaml) |
| **Keycloak IdP** | `quay.io/keycloak/keycloak:24.0.5` | [`deploy/platform/identity/keycloak-deployment.yaml`](deploy/platform/identity/keycloak-deployment.yaml) |
| **PostgreSQL** | `postgres:16.8-alpine` | [`deploy/platform/database/postgres-statefulset.yaml`](deploy/platform/database/postgres-statefulset.yaml), [`postgres-init-job.yaml`](deploy/platform/database/postgres-init-job.yaml), [`backup-cronjob.yaml`](deploy/platform/database/backup-cronjob.yaml) |
| **cert-manager** | `quay.io/jetstack/cert-manager-{controller,cainjector,webhook}:v1.21.2` | [`deploy/platform/cert-manager/upstream/cert-manager.yaml`](deploy/platform/cert-manager/upstream/cert-manager.yaml) |
| **Cilium CNI & Operator** | `quay.io/cilium/cilium:v1.20.3`, `quay.io/cilium/operator-generic:v1.20.3` (pinned `@sha256:...` digests) | [`deploy/platform/cni/upstream/cilium.yaml`](deploy/platform/cni/upstream/cilium.yaml) |
| **Hubble Relay & Hubble UI** | `quay.io/cilium/hubble-relay:v1.20.3`, `quay.io/cilium/hubble-ui{,-backend}:v0.13.6` (pinned `@sha256:...` digests) | [`deploy/platform/cni/upstream/cilium.yaml`](deploy/platform/cni/upstream/cilium.yaml) |
| **Fluent Bit** | `fluent/fluent-bit:4.2.8` | [`deploy/platform/observability/fluent-bit-daemonset.yaml`](deploy/platform/observability/fluent-bit-daemonset.yaml) |
| **VictoriaLogs** | `victoriametrics/victoria-logs:v1.53.0` | [`deploy/platform/observability/victorialogs-statefulset.yaml`](deploy/platform/observability/victorialogs-statefulset.yaml) |
| **Init / Wait Helper** | `curlimages/curl:8.10.1` | [`deploy/platform/identity/`](deploy/platform/identity/), [`deploy/platform/database/`](deploy/platform/database/) |

---

## 3. Operator, Bootstrap & CI Toolchains

| Scope | Tool / Package | Pinned Version | Config / Manifest Location |
| :--- | :--- | :--- | :--- |
| **VM Bootstrap** | `ansible-core` | `==2.21.4` | [`bootstrap/requirements.txt`](bootstrap/requirements.txt) |
| **Preflight & Invariant Scripts** | `Python` + `PyYAML` | Python `3.12` + `PyYAML==6.0.3` | [`scripts/requirements-preflight.txt`](scripts/requirements-preflight.txt), [`.github/workflows/gitops.yaml`](.github/workflows/gitops.yaml) |
| **K8s Schema Validation** | `kubeconform` | `v0.6.7` | [`.github/workflows/gitops.yaml`](.github/workflows/gitops.yaml), [`scripts/render-validate.sh`](scripts/render-validate.sh) |
| **CRD Schema Catalog** | Datree CRDs Catalog | `main` (Traefik, cert-manager, Flux CRDs) | [`.github/workflows/gitops.yaml`](.github/workflows/gitops.yaml), [`scripts/render-validate.sh`](scripts/render-validate.sh) |
| **Shell Linting** | `shellcheck` | `v0.11.0` | [`.github/workflows/lint.yaml`](.github/workflows/lint.yaml) (`./scripts`) |
| **YAML Linting** | `yamllint` | `ibiqlik/action-yamllint@v3` | [`.github/workflows/lint.yaml`](.github/workflows/lint.yaml) (`deploy/`, `clusters/`) |
| **Secret Scanning** | `gitleaks` | `gitleaks/gitleaks-action@v2` | [`.github/workflows/secret-scan.yaml`](.github/workflows/secret-scan.yaml), [`.gitleaksignore`](.gitleaksignore) |

---

## 4. Node Capacity & $1/n$ Resource Budget Dependencies

[`scripts/platform-preflight.py`](scripts/platform-preflight.py) computes each project's resource budget from the environment's [`capacity.yaml`](clusters/vps-k3s/capacity.yaml):

| Environment | Node Allocatable | System Reserved | Platform Reservation (incl. Cilium + Hubble) | Capacity File |
| :--- | :--- | :--- | :--- | :--- |
| `vps-k3s` (Oracle ARM64) | `4000m` CPU / `24576 MiB` RAM | `200m` CPU / `512 MiB` RAM | `1920m` CPU / `5312 MiB` RAM (`2080m` / `19264 MiB` per-project slice for $n=1$) | [`clusters/vps-k3s/capacity.yaml`](clusters/vps-k3s/capacity.yaml) |
| `local-k3s` | `4000m` CPU / `8192 MiB` RAM | `200m` CPU / `512 MiB` RAM | `1920m` CPU / `5312 MiB` RAM (`2080m` / `2880 MiB` per-project slice for $n=1$) | [`clusters/local-k3s/capacity.yaml`](clusters/local-k3s/capacity.yaml) |

The table above is the last recorded platform reservation. The observability reservation (Fluent Bit and VictoriaLogs) is not yet measured and must not be added by hand. Do not replace `1920m` or `5312` until a live `platformReservations` measurement is committed. Store sizing that does not depend on that measurement: 30-day retention, 8 GiB disk cap, 10Gi store PVC, 1Gi tail PVC `fluent-bit-state`, `proj-*` only, privileged namespace `observability`. VPS access is `scripts/open-observe.sh`. `local-k3s` access is `scripts/open-victorialogs-ui.sh`.

---

## 5. Hosted Tenant Project Contracts (`Project_PN` & `proj-<slug>`)

`nacfson_pipeline` hosts tenant applications under [`deploy/projects/<project>/`](deploy/projects/pn/) with environment overlays in [`clusters/*/projects/proj-<project>.yaml`](clusters/vps-k3s/projects/proj-pn.yaml). Canonical cross-project contracts live in [`contracts/`](contracts/README.md):
- [`contracts/CONTRACT_DATABASE.md`](contracts/CONTRACT_DATABASE.md) — Standardized PostgreSQL naming (`proj_<slug>` / `user_<slug>`), `vault-db-proxy` wire protocol, and MySQL/NoSQL decision architecture.
- [`contracts/CONTRACT_PROBES_READINESS.md`](contracts/CONTRACT_PROBES_READINESS.md) — Startup, readiness (`/readyz`), liveness (`/healthz`), DB-hop readiness, and browser CORS.
- [`contracts/CONTRACT_AUTH_DOOR.md`](contracts/CONTRACT_AUTH_DOOR.md) — Direct project door, `signIn: none | shared`, and session confirmation.
- [`contracts/CONTRACT_TLS_INGRESS.md`](contracts/CONTRACT_TLS_INGRESS.md) — Platform-owned edge TLS inventory and `IngressRoute` rules.
- [`contracts/CONTRACT_ISOLATION_BUDGET.md`](contracts/CONTRACT_ISOLATION_BUDGET.md) — Restricted PSS, NetworkPolicy egress boundaries, and $1/n$ compute budget.
- [`contracts/CONTRACT_LOG_SCHEMA.md`](contracts/CONTRACT_LOG_SCHEMA.md) — Required hosted-project log pattern: stdout/stderr only, platform envelope, producer-owned body schema. Every `proj-*` workload uses this. Project PN is the first registered `kind` owner, not an exception.

### 5.1 Project PN (`proj-pn` / `/home/nacfson/Projects/Project_PN`)

| Contract Area | Pipeline Manifest / Setting | Tenant Requirement (`Project_PN`) |
| :--- | :--- | :--- |
| **Images (Multi-Arch Digest Pinning)** | [`deploy/projects/pn/workloads/backend-deployment.yaml`](deploy/projects/pn/workloads/backend-deployment.yaml)<br/>[`deploy/projects/pn/workloads/frontend-deployment.yaml`](deploy/projects/pn/workloads/frontend-deployment.yaml) | Must publish `linux/amd64` + `linux/arm64` images to `ghcr.io/nacfson/project-pn-{backend,frontend}` and reference them by `@sha256:...` digest. |
| **Sign-In Mode** | [`deploy/projects/pn/sign-in.yaml`](deploy/projects/pn/sign-in.yaml) (`mode: shared`) | Project renders its own sign-in UI, initiates browser login via `https://auth.nacfson.cloud/oauth/start`, and verifies confirmed sessions via the gateway (`http://gateway.identity.svc.cluster.local:8080`). |
| **Return Host Allowlist** | `PROJECT_RETURN_HOSTS` in [`clusters/*/layers.yaml`](clusters/vps-k3s/layers.yaml) (`identity` patch) | `pn.nacfson.cloud` (`vps-k3s`) / `pn.localhost` (`local-k3s`) must match the project's public hostname. |
| **Edge TLS** | [`deploy/platform/tls/certificates.yaml`](deploy/platform/tls/certificates.yaml) (`pn-tls`) | Platform owns `pn-tls` in `platform` namespace. `proj-pn` [`ingress-route.yaml`](deploy/projects/pn/workloads/ingress-route.yaml) specifies `tls: {}` with no `secretName`. |
| **Browser API CORS** | [`clusters/*/projects/proj-pn.yaml`](clusters/vps-k3s/projects/proj-pn.yaml) | Backend `PUBLIC_APP_URL` (`https://pn.nacfson.cloud`) MUST be present in `ALLOWED_ORIGINS`. |
| **Health Probes & Restart** | [`deploy/projects/pn/workloads/frontend-deployment.yaml`](deploy/projects/pn/workloads/frontend-deployment.yaml)<br/>[`deploy/projects/pn/workloads/backend-deployment.yaml`](deploy/projects/pn/workloads/backend-deployment.yaml)<br/>[`deploy/platform/vault-proxies/db-proxy.yaml`](deploy/platform/vault-proxies/db-proxy.yaml) | Frontend `startupProbe` (`/api/auth/language-options`) & `readinessProbe` (`/healthz`) check backend reachability, `livenessProbe` checks local `:80/`; Backend & `vault-db-proxy` expose `:8080/healthz` (liveness) and `:8080/readyz` (DB-hop readiness); project workloads set `restartPolicy: Always`. |
| **Database Access** | [`deploy/projects/pn/boundary/`](deploy/projects/pn/boundary/) & `vault-db-proxy` | Connects via workload identity `project:pn:workload:backend` to `vault-db-proxy.vault.svc:5432/proj_pn`, bound to restricted role `user_pn` on database `proj_pn` ([`CONTRACT_DATABASE.md`](contracts/CONTRACT_DATABASE.md)). |
| **Security Context & Ports** | [`deploy/projects/pn/boundary/namespace.yaml`](deploy/projects/pn/boundary/namespace.yaml) | Namespace enforces `restricted` PSS; containers run as UID `10001` (`runAsNonRoot: true`), drop `ALL` capabilities, `automountServiceAccountToken: false`; Services are `ClusterIP` (`:80` FE, `:8080` BE). |
| **Logs** | [`contracts/CONTRACT_LOG_SCHEMA.md`](contracts/CONTRACT_LOG_SCHEMA.md) | Print only. `kind:"analytics"` on stdout. `kind:"admin"` on stderr. No `ADMIN_LOG_PATH` and no log PVC. Body schemas stay in Project PN. The next project uses the same contract and adds its own `kind` row. |
