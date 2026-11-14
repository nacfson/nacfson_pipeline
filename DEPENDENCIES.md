# nacfson_pipeline Dependencies, Pinned Versions & Tenant Contracts

This document tracks all runtime dependency rules, pinned third-party infrastructure versions, operator/CI toolchains, and cross-project tenant contracts in **`nacfson_pipeline`**.

---

## 1. First-Party Go Services (Zero Third-Party Dependencies)

Per [Constitution Principle III](.specify/memory/constitution.md) and Security Constraints, all in-repo Go services MUST use **pure Go standard library only** (`crypto/*`, `net/http`, `encoding/json`, `net/url`, `io`, etc.).

| Module | Path | Go Toolchain | Allowed External Modules | Published Image (`asia-northeast3-docker.pkg.dev/nacfson-pipeline/nacfson-public/` & `ghcr.io/nacfson/`) |
| :--- | :--- | :--- | :--- | :--- |
| `nacfson_pipeline/gateway` | [`gateway/go.mod`](gateway/go.mod) | `go 1.22` | **None** (`0` `require` directives) | `platform-auth-gateway` |
| `nacfson_pipeline/services/vault-proxies` | [`services/vault-proxies/go.mod`](services/vault-proxies/go.mod) | `go 1.22` | **None** (`0` `require` directives) | `vault-db-proxy`, `vault-ai-proxy`, `vault-notify-proxy` *(Spec 012 retired `vault-registry-proxy`)* |
| `nacfson_pipeline/services/error-pipeline` | [`services/error-pipeline/go.mod`](services/error-pipeline/go.mod) | `go 1.22` | **None** (`0` `require` directives) | `error-detector` |

> [!IMPORTANT]
> Never run `go get <third-party-pkg>` or introduce external gateway frameworks, OAuth2 Proxy, or Redis into [`gateway/`](gateway/), [`services/vault-proxies/`](services/vault-proxies/), or [`services/error-pipeline/`](services/error-pipeline/).

---

## 2. Pinned Infrastructure & Platform Versions

Per [Constitution Principle I](.specify/memory/constitution.md), third-party images MUST be pinned to exact upstream version tags (floating tags like `latest` or `16-alpine` on third-party images are prohibited), and hosted project images MUST be pinned by immutable `@sha256:...` digests.

| Component | Pinned Version / Image | Where Declared |
| :--- | :--- | :--- |
| **Kubernetes (GKE Standard)** | Zonal Standard (`nacfson-pipeline`, `asia-northeast3-a`, `1 × e2-medium` On-Demand + `1 × e2-small` Spot, `--logging=NONE --monitoring=NONE`) | [`clusters/gke/capacity.yaml`](clusters/gke/capacity.yaml), [`clusters/gke/layers.yaml`](clusters/gke/layers.yaml) |
| **Kubernetes (K3s)** | `v1.35.9+k3s1` (Traefik bundled on `local-k3s` / legacy `vps-k3s`) | [`bootstrap/bootstrap.yml`](bootstrap/bootstrap.yml) (`accepted_k3s_versions`) |
| **Traefik Edge Ingress (GKE)** | `traefik:v3.3.4` (`hostPort: 80/443`, `Service` `type: ClusterIP`, non-root `UID 65532`) | [`deploy/platform/traefik/deployment.yaml`](deploy/platform/traefik/deployment.yaml) |
| **FluxCD CLI / Distribution** | `2.9.5` | [`.github/workflows/gitops.yaml`](.github/workflows/gitops.yaml), [`clusters/*/flux-system/`](clusters/gke/flux-system/gotk-components.yaml) |
| **Flux Source Controller** | `ghcr.io/fluxcd/source-controller:v1.9.5` | [`clusters/*/flux-system/gotk-components.yaml`](clusters/gke/flux-system/gotk-components.yaml) |
| **Flux Kustomize Controller** | `ghcr.io/fluxcd/kustomize-controller:v1.9.5` | [`clusters/*/flux-system/gotk-components.yaml`](clusters/gke/flux-system/gotk-components.yaml) |
| **OpenBao (Cluster Vault)** | `openbao/openbao:2.0.0` (`gcpckms` auto-unseal on `gke`, Shamir on `local-k3s`) | [`deploy/platform/vault/openbao-statefulset.yaml`](deploy/platform/vault/openbao-statefulset.yaml), [`backup-cronjob.yaml`](deploy/platform/vault/backup-cronjob.yaml) |
| **Keycloak IdP** | `quay.io/keycloak/keycloak:24.0.5` (`--features=persistent-user-sessions`) | [`deploy/platform/identity/keycloak-deployment.yaml`](deploy/platform/identity/keycloak-deployment.yaml) |
| **PostgreSQL** | `postgres:16.8-alpine` | [`deploy/platform/database/postgres-statefulset.yaml`](deploy/platform/database/postgres-statefulset.yaml), [`postgres-init-job.yaml`](deploy/platform/database/postgres-init-job.yaml), [`backup-cronjob.yaml`](deploy/platform/database/backup-cronjob.yaml) |
| **cert-manager** | `quay.io/jetstack/cert-manager-{controller,cainjector,webhook}:v1.21.2` | [`deploy/platform/cert-manager/upstream/cert-manager.yaml`](deploy/platform/cert-manager/upstream/cert-manager.yaml) |
| **Cilium CNI & Operator (`local-k3s`)** | `quay.io/cilium/cilium:v1.20.3`, `quay.io/cilium/operator-generic:v1.20.3` (`policy-cidr-match-mode: "nodes"`) | [`deploy/platform/cni/upstream/cilium.yaml`](deploy/platform/cni/upstream/cilium.yaml), [`deploy/platform/cni/kustomization.yaml`](deploy/platform/cni/kustomization.yaml) |
| **Hubble Relay & Hubble UI (`local-k3s`)** | `quay.io/cilium/hubble-relay:v1.20.3`, `quay.io/cilium/hubble-ui{,-backend}:v0.13.6` | [`deploy/platform/cni/upstream/cilium.yaml`](deploy/platform/cni/upstream/cilium.yaml) |
| **Fluent Bit** | `fluent/fluent-bit:4.2.8` (`/var/lib/fluent-bit-state` `256Mi`/node cap; `[OUTPUT] kafka` on `gke`) | [`deploy/platform/observability/fluent-bit-daemonset.yaml`](deploy/platform/observability/fluent-bit-daemonset.yaml), [`clusters/gke/observability/`](clusters/gke/observability/kustomization.yaml) |
| **VictoriaLogs (`local-k3s` only)** | `victoriametrics/victoria-logs:v1.53.0` *(deleted on `gke` in favor of OCI Kafka -> VM 4)* | [`deploy/platform/observability/victorialogs-statefulset.yaml`](deploy/platform/observability/victorialogs-statefulset.yaml) |
| **Init / Wait Helper** | `curlimages/curl:8.10.1` | [`deploy/platform/identity/`](deploy/platform/identity/), [`deploy/platform/database/`](deploy/platform/database/) |

---

## 3. Operator, Bootstrap & CI Toolchains

| Scope | Tool / Package | Pinned Version | Config / Manifest Location |
| :--- | :--- | :--- | :--- |
| **VM Bootstrap (`local-k3s`)** | `ansible-core` | `==2.21.4` | [`bootstrap/requirements.txt`](bootstrap/requirements.txt) |
| **Multi-Arch Image Build & GAR Cleanup** | Google Cloud Build + `docker buildx` | `gcr.io/cloud-builders/docker` (`linux/amd64,linux/arm64`, keep `2` / `7d` `< 0.5 GB`) | [`deploy/gcp/cloudbuild.yaml`](deploy/gcp/cloudbuild.yaml), [`deploy/gcp/gar-cleanup-policy.json`](deploy/gcp/gar-cleanup-policy.json) |
| **Preflight, Kustomize Fallback & Invariants** | `Python` + `PyYAML` | Python `3.11+` / `3.12` + `PyYAML==6.0.3` | [`scripts/platform-preflight.py`](scripts/platform-preflight.py), [`scripts/kustomize_render.py`](scripts/kustomize_render.py), [`.github/workflows/gitops.yaml`](.github/workflows/gitops.yaml) |
| **Full-State Cutover & DB Migration** | Bash + `kubectl` + `sha256sum` | POSIX / GNU coreutils | [`scripts/cutover-bundle.sh`](scripts/cutover-bundle.sh), [`scripts/db-migrate-dump.sh`](scripts/db-migrate-dump.sh), [`scripts/vault-configure-k8s-auth.sh`](scripts/vault-configure-k8s-auth.sh) |
| **K8s Schema Validation** | `kubeconform` | `v0.6.7` | [`.github/workflows/gitops.yaml`](.github/workflows/gitops.yaml), [`scripts/render-validate.sh`](scripts/render-validate.sh) |
| **CRD Schema Catalog** | Datree CRDs Catalog | `6ec4c804fcb9b0a90fec9dd468aa57b984be150a` | [`scripts/render-validate.sh`](scripts/render-validate.sh) |
| **Shell Linting** | `shellcheck` | `v0.11.0` | [`.github/workflows/lint.yaml`](.github/workflows/lint.yaml) (`./scripts`) |
| **YAML Linting** | `yamllint` | `ibiqlik/action-yamllint@v3` | [`.github/workflows/lint.yaml`](.github/workflows/lint.yaml) (`deploy/`, `clusters/`) |
| **Secret Scanning** | `gitleaks` | `gitleaks/gitleaks-action@v2` | [`.github/workflows/secret-scan.yaml`](.github/workflows/secret-scan.yaml), [`.gitleaksignore`](.gitleaksignore) |

---

## 4. Node Capacity & $1/n$ Resource Budget Dependencies

[`scripts/platform-preflight.py`](scripts/platform-preflight.py) computes each project's $1/n$ resource budget and multi-node per-pool schedulability from the environment's `capacity.yaml`:

| Environment | Node Allocatable | System Reserved | Platform Reservation (Exact Preflight Output) | Per-Project $1/n$ Slice ($n = 3$) | Capacity File |
| :--- | :--- | :--- | :--- | :--- | :--- |
| **`gke`** (`1 × e2-medium` On-Demand + `1 × e2-small` Spot, `nodeCount: 2`) | `1880m` CPU / `4408 MiB` RAM (`940m/2972Mi` + `940m/1436Mi`) | `350m` CPU / `576 MiB` RAM (`200m/320Mi` + `150m/256Mi`) | **`970m` CPU / `3152 MiB` RAM** (`2×` `fluent-bit` DaemonSet, pre-reserved `error-detector` `25m/64Mi` slot, `keycloak` `832Mi`, `Traefik` `30m/96Mi`) | **`303m` CPU / `418 MiB` RAM** (`proj-pn` `150m/384Mi`, `proj-health` `150m/320Mi`, `proj-blog` `50m/128Mi`) | [`clusters/gke/capacity.yaml`](clusters/gke/capacity.yaml) |
| **`local-k3s`** (`nodeCount: 1`) | `4000m` CPU / `8192 MiB` RAM | `200m` CPU / `512 MiB` RAM | **`1370m` CPU / `5856 MiB` RAM** | **`877m` CPU / `779 MiB` RAM** | [`clusters/local-k3s/capacity.yaml`](clusters/local-k3s/capacity.yaml) |
| **`vps-k3s`** *(transitional until Gate 4 cutover)* | `2000m` CPU / `10898 MiB` RAM | `200m` CPU / `512 MiB` RAM | **`1370m` CPU / `5856 MiB` RAM** (`-25m / -64Mi` from pre-Spec-012 `1395m / 5920 MiB` after retiring `vault-registry-proxy`) | **`210m` CPU / `1681 MiB` RAM** | [`clusters/vps-k3s/capacity.yaml`](clusters/vps-k3s/capacity.yaml) |

---

## 5. Hosted Tenant Project Contracts (`Project_PN` & `proj-<slug>`)

`nacfson_pipeline` hosts tenant applications (`proj-pn`, `proj-health`, `proj-blog`) under [`deploy/projects/`](deploy/projects/) with environment overlays in [`clusters/*/projects/`](clusters/gke/projects/kustomization.yaml). Canonical cross-project contracts live in [`contracts/`](contracts/README.md):
- [`contracts/CONTRACT_DATABASE.md`](contracts/CONTRACT_DATABASE.md) — Standardized PostgreSQL naming (`proj_<slug>` / `user_<slug>`), `vault-db-proxy` wire protocol, and MySQL/NoSQL decision architecture.
- [`contracts/CONTRACT_PROBES_READINESS.md`](contracts/CONTRACT_PROBES_READINESS.md) — Startup, readiness (`/readyz`), liveness (`/healthz`), DB-hop readiness, and browser CORS.
- [`contracts/CONTRACT_AUTH_DOOR.md`](contracts/CONTRACT_AUTH_DOOR.md) — Direct project door, `signIn: none | shared`, and session confirmation.
- [`contracts/CONTRACT_TLS_INGRESS.md`](contracts/CONTRACT_TLS_INGRESS.md) — Platform-owned edge TLS inventory and `IngressRoute` rules.
- [`contracts/CONTRACT_ISOLATION_BUDGET.md`](contracts/CONTRACT_ISOLATION_BUDGET.md) — Restricted PSS, portable `networking.k8s.io/v1` `NetworkPolicy` egress boundaries, multi-node pool placement, and $1/n$ compute budget.
- [`contracts/CONTRACT_LOG_SCHEMA.md`](contracts/CONTRACT_LOG_SCHEMA.md) — Required hosted-project log pattern (`v3`): stdout/stderr only, node-local `/var/lib/fluent-bit-state` recovery (`256 MiB` cap), and platform envelope.

### 5.1 Project PN (`proj-pn` / `/home/nacfson/Projects/Project_PN`)

| Contract Area | Pipeline Manifest / Setting | Tenant Requirement (`Project_PN`) |
| :--- | :--- | :--- |
| **Images (Multi-Arch Digest Pinning)** | [`deploy/projects/pn/workloads/backend-deployment.yaml`](deploy/projects/pn/workloads/backend-deployment.yaml)<br/>[`deploy/projects/pn/workloads/frontend-deployment.yaml`](deploy/projects/pn/workloads/frontend-deployment.yaml) | Must publish `linux/amd64` + `linux/arm64` images to public GAR (`asia-northeast3-docker.pkg.dev/nacfson-pipeline/nacfson-public/project-pn-{backend,frontend}`) or `ghcr.io/nacfson/project-pn-{backend,frontend}` and reference them by `@sha256:...` digest. |
| **Sign-In Mode** | [`deploy/projects/pn/sign-in.yaml`](deploy/projects/pn/sign-in.yaml) (`mode: shared`) | Project renders its own sign-in UI, initiates browser login via `https://auth.nacfson.cloud/oauth/start`, and verifies confirmed sessions via the gateway (`http://gateway.identity.svc.cluster.local:8080`). |
| **Return Host Allowlist** | `PROJECT_RETURN_HOSTS` in [`clusters/*/layers.yaml`](clusters/gke/layers.yaml) (`identity` patch) | `pn.nacfson.cloud` (`gke` / `vps-k3s`) / `pn.example.com` (`local-k3s`) must match the project's public hostname. |
| **Edge TLS** | [`deploy/platform/tls/certificates.yaml`](deploy/platform/tls/certificates.yaml) (`pn-tls`) | Platform owns `pn-tls` in `platform` namespace. `proj-pn` [`ingress-route.yaml`](deploy/projects/pn/workloads/ingress-route.yaml) specifies `tls: {}` with no `secretName`. |
| **Browser API CORS** | [`clusters/*/projects/proj-pn.yaml`](clusters/gke/projects/proj-pn.yaml) | Backend `PUBLIC_APP_URL` (`https://pn.nacfson.cloud`) MUST be present in `ALLOWED_ORIGINS`. |
| **Health Probes & Restart** | [`deploy/projects/pn/workloads/frontend-deployment.yaml`](deploy/projects/pn/workloads/frontend-deployment.yaml)<br/>[`deploy/projects/pn/workloads/backend-deployment.yaml`](deploy/projects/pn/workloads/backend-deployment.yaml)<br/>[`deploy/platform/vault-proxies/db-proxy.yaml`](deploy/platform/vault-proxies/db-proxy.yaml) | Frontend `startupProbe` (`/api/auth/language-options`) & `readinessProbe` (`/healthz`) check backend reachability, `livenessProbe` checks local `:80/`; Backend & `vault-db-proxy` expose `:8080/healthz` (liveness) and `:8080/readyz` (DB-hop readiness); project workloads set `restartPolicy: Always`. |
| **Database Access** | [`deploy/projects/pn/boundary/`](deploy/projects/pn/boundary/) & `vault-db-proxy` | Connects via workload identity `project:pn:workload:backend` to `vault-db-proxy.vault.svc:5432/proj_pn`, bound to restricted role `user_pn` on database `proj_pn` ([`CONTRACT_DATABASE.md`](contracts/CONTRACT_DATABASE.md)). |
| **Security Context & Ports** | [`deploy/projects/pn/boundary/namespace.yaml`](deploy/projects/pn/boundary/namespace.yaml) | Namespace enforces `restricted` PSS; containers run non-root (`runAsNonRoot: true`), drop `ALL` capabilities, `automountServiceAccountToken: false`; Services are `ClusterIP` (`:80` FE, `:8080` BE). |
| **Logs** | [`contracts/CONTRACT_LOG_SCHEMA.md`](contracts/CONTRACT_LOG_SCHEMA.md) | Print only (`v3` OpenTelemetry JSON lines on stdout/stderr with `event.name` replacing legacy `kind`). Zero `ADMIN_LOG_PATH` and zero log PVC; all 5 project containers across `proj-pn`, `proj-health`, and `proj-blog` are registered. |
