# VPS `proj-health` Onboarding & OpenBao Seed Parity — Design

Date: 2026-10-24 · Size: small change (missing VPS project Kustomization + Vault seed/rotation script parity) · Status: awaiting approval

Marking: **(operator)** = decided by the operator. **(my call)** = my proposal, can be changed.

## Intent

Make `https://health.nacfson.cloud` reachable on `vps-k3s` (`168.110.101.69`) with a valid Let's Encrypt certificate, running `proj-health` pods (`health-frontend` and `health-backend`), and live flow visibility in Hubble UI (`proj-health` namespace). **(operator)**

## Why (Observed Live State vs Git)

When `proj-health` was onboarded in PR #61 (`766222d`), two gaps left `https://health.nacfson.cloud` returning `TRAEFIK DEFAULT CERT` (self-signed) and `HTTP/2 404`:

1. **Missing `clusters/vps-k3s/projects/proj-health.yaml` in Git**:
   - `clusters/local-k3s/projects/proj-health.yaml` was added for local rehearsal (`contracts/README.md` Step 7), while `clusters/vps-k3s/projects/kustomization.yaml` still lists only `proj-pn.yaml`.
   - Without `Kustomization/proj-health` on `vps-k3s`, `deploy/projects/health/workloads/` (`Deployment/health-frontend`, `Deployment/health-backend`, `Service/health-frontend`, `Service/health-backend`, and `IngressRoute/project-health-ingress`) is never deployed, so `proj-health` has zero pods and zero Hubble flows.

2. **Stalled `database` → `identity` → `ingress` → `tls` Flux chain on `vps-k3s`**:
   - Note: [`docs/superpowers/specs/2026-10-20-network-policy-check-intent.md`](2026-10-20-network-policy-check-intent.md) (lines 15–16) assumed the VPS already had the `health.nacfson.cloud` certificate and only lacked the route. Live inspection on `2026-10-24` (`sudo k3s kubectl get kustomizations.kustomize.toolkit.fluxcd.io -A` and `openssl s_client`) disproved that assumption:
     - `Kustomization/database` is `False` (`Job/identity/postgres-init-job status: 'Failed'`).
     - Because `database` is `False`, `identity`, `ingress`, and `tls` are blocked in `DependencyNotReady`, so `Certificate/health-tls` was never applied on `vps-k3s` and Traefik serves `CN=TRAEFIK DEFAULT CERT`.
   - PR #61 added `PROJECT_SLUGS: "pn,health"` to `deploy/platform/database/postgres-init-job.yaml` and `kv/data/projects/health/client-secret` to `deploy/platform/identity/keycloak-deployment.yaml`, which `curl -fsSL` under `set -euo pipefail`. However, [`scripts/vault-seed-inventory.sh`](../../../scripts/vault-seed-inventory.sh) and [`scripts/vault-rotate.sh`](../../../scripts/vault-rotate.sh) were never updated to seed or rotate `kv/database/health-user` and `kv/projects/health/client-secret` (`contracts/README.md` Step 0).

## Goals

- Add `clusters/vps-k3s/projects/proj-health.yaml` and register it in `clusters/vps-k3s/projects/kustomization.yaml` so Flux reconciles `proj-health` workloads on `vps-k3s`. **(operator)**
- Patch `IngressRoute/project-health-ingress` (`Host(\`health.nacfson.cloud\`)` and `Host(\`health.nacfson.cloud\`) && PathPrefix(\`/api\`)`) and `Deployment/health-backend` (`PUBLIC_APP_URL` and `ALLOWED_ORIGINS` for `https://health.nacfson.cloud`) in `clusters/vps-k3s/projects/proj-health.yaml`. **(my call)**
- Remove the duplicate `spec.images` block from `clusters/local-k3s/projects/proj-health.yaml` (and omit it from `clusters/vps-k3s/projects/proj-health.yaml`) so `deploy/projects/health/workloads/*.yaml` is the single digest pin location used by `update-project-images`. **(my call)**
- Add `kv/database/health-user` (`username="user_health"`) and `kv/projects/health/client-secret` (`client_id="project-health"`) to `scripts/vault-seed-inventory.sh` and `scripts/vault-rotate.sh`. **(operator)**
- Keep `docs/platform-architecture.html` synchronized (`700m / 768Mi` quota on `project-boundaries-health` and `proj-health.yaml` in `projects` `vps-k3s`). **(my call)**

## Non-goals

- Modifying `deploy/projects/health/boundary/` or `deploy/projects/health/workloads/` base manifests (already committed in `766222d`).
- Implementing GitHub issue #69 (`docs/superpowers/specs/2026-10-20-network-policy-check-intent.md` local VM network-policy harness).

## Anti-goals

- Plaintext secrets in Git or Kubernetes `Secret` objects for database or OAuth credentials.
- Duplicate `spec.images` overrides in `clusters/*/projects/proj-health.yaml` that silently override workload digest updates.

## Constraints

- **Constitution & Preflight**:
  - `IngressRoute/project-health-ingress` must declare `entryPoints: [websecure]`, `tls: {}`, and `strip-forged-headers` (no `forward-auth`, no `tls.secretName`).
  - `health.nacfson.cloud` must match `Certificate/health-tls` in `clusters/vps-k3s/layers.yaml` (`check_edge_tls` Rule TLS-5).
  - `PUBLIC_APP_URL` must be included in `ALLOWED_ORIGINS` (`CONTRACT_PROBES_READINESS.md` §4).
  - `vps-k3s` and `local-k3s` renders must pass `./scripts/render-validate.sh` and `scripts/platform-preflight.py` with `projectCount == 2` (`proj-pn` peak `700m / 896Mi` and `proj-health` peak `600m / 640Mi` within the `1202.5m / 9632Mi` `vps-k3s` $1/n$ slice).
- **Handbook Gate**: Any change under `clusters/` requires updating `docs/platform-architecture.html` (`scripts/check-arch-doc-sync.py`).

## Approaches

1. **Declarative Kustomization patches in `clusters/<env>/projects/proj-health.yaml` + Vault script parity (Chosen)**:
   - Mirrors `clusters/{local,vps}-k3s/projects/proj-pn.yaml` exactly: `deploy/projects/health/workloads/` holds the immutable `@sha256:...` image pins, while `clusters/<env>/projects/proj-health.yaml` patches the environment-specific `IngressRoute` host and backend `PUBLIC_APP_URL` / `ALLOWED_ORIGINS`. `scripts/vault-seed-inventory.sh` and `scripts/vault-rotate.sh` seed and rotate `health-user` and `projects/health/client-secret`.
   - *Trade-off*: None; matches existing `proj-pn` pattern and `update-project-images` contract.
2. **Keep `spec.images` in `clusters/<env>/projects/proj-health.yaml`**:
   - *Rejected*: `update_project_images.py` edits only `deploy/projects/<slug>/workloads/*.yaml`. Leaving `spec.images` in `clusters/<env>/projects/proj-health.yaml` causes Kustomize/Flux to overwrite updated workload digests with stale hashes.

## Design Details (Planner Calls to Verify)

1. **`clusters/vps-k3s/projects/proj-health.yaml`**:
   - `metadata.name: proj-health`, `metadata.namespace: proj-health`, `spec.serviceAccountName: health-reconciler`.
   - `spec.dependsOn`: `project-boundaries-health`, `ingress`, `vault-proxies` (in `flux-system`).
   - `spec.path: ./deploy/projects/health/workloads`.
   - Patches `IngressRoute/project-health-ingress` routes `0` and `1` to `Host(\`health.nacfson.cloud\`)` and `Host(\`health.nacfson.cloud\`) && PathPrefix(\`/api\`)`.
   - Patches `Deployment/health-backend` container `backend` with:
     - `ALLOWED_ORIGINS: "http://localhost:8081,http://localhost:19006,tauri://localhost,http://tauri.localhost,https://health.nacfson.cloud"`
     - `PUBLIC_APP_URL: "https://health.nacfson.cloud"`
2. **`clusters/local-k3s/projects/proj-health.yaml`**:
   - Drops `spec.images` and applies the matching `health.example.com` patches for symmetry with `clusters/local-k3s/projects/proj-pn.yaml`.
3. **`scripts/vault-seed-inventory.sh` & `scripts/vault-rotate.sh`**:
   - Seeds `kv/database/health-user` (`username="user_health"`) and `kv/projects/health/client-secret` (`client_id="project-health"`) idempotently (skipping existing secrets unless `--force` is passed).
   - Adds `postgres-health-db-password` and `project-health-client-secret` rotation targets to `scripts/vault-rotate.sh`.
4. **`docs/platform-architecture.html`**:
   - Fixes `DAG_LAYERS['project-boundaries-health']` text to `700m CPU, 768Mi RAM` and `health-workload-sa` (matching `deploy/projects/health/boundary/resource-quota.yaml` and `service-account.yaml`).
   - Updates `DAG_LAYERS['projects'].yaml['vps-k3s']` to list both `proj-pn.yaml` and `proj-health.yaml`.

## What's Left to the Builder

- Implement the unit tests (`test_25` and `test_26` in `tests/preflight/test_platform_preflight.py`) and manifest/script changes per [`docs/superpowers/plans/2026-10-24-vps-proj-health-onboarding.md`](../plans/2026-10-24-vps-proj-health-onboarding.md).
