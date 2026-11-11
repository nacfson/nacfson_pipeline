# Phase 0 Research: GKE Platform Portability & Full-State Cutover

**Feature**: [`specs/012-gke-platform-migration/spec.md`](spec.md)
**Date**: 2026-11-10

---

## R1: GKE Worker Node Sizing, Pool Split & `keycloak` 24/7 Uptime (Design 1)

- **Decision**: Provision a Zonal GKE Standard cluster in GCP project **`nacfson-pipeline`** (`asia-northeast3-a` Seoul, `--logging=NONE --monitoring=NONE`, GKE Dataplane V2 enabled; `asia-northeast1-a` Tokyo supported as a drop-in alternative) with two single-node pools (**Design 1**, ~\$53.31/mo PAYG / ~\$41.70/mo 1-yr CUD in `asia-northeast3-a` Seoul; ~\$40.69/mo PAYG in US Tier-1):
  1. **`ondemand-core` (`1 × e2-medium` On-Demand, 4 GB RAM, \$31.38/mo PAYG / \$19.77/mo 1-yr in Seoul)**: K8s allocatable `940m` CPU / `2,972 MiB` RAM (`~2,652 MiB` usable after `kube-system`). Runs **100% of live user traffic, OIDC token introspection (`keycloak` right-sized to `requests/limits: 832Mi` in `clusters/gke/` with `--features=persistent-user-sessions`), Vault proxies, and `ReadWriteOnce` PVC workloads** (`traefik` `30m/64Mi` req · `200m/96Mi` lim, `gateway`, `proj-pn` with `pn-backend` `100m/256Mi` in `clusters/gke/`, `proj-health` with `health-backend` `100m/256Mi`, `proj-blog` `50m/128Mi`, `postgres-0`, `openbao-0`, `vault-db-proxy`, `vault-ai-proxy`, `vault-notify-proxy`, `error-detector`, `postgres-init-job`, `postgres-daily-backup`, `openbao-daily-snapshot`).
  2. **`spot-controllers` (`1 × e2-small` Spot, 2 GB RAM, \$7.81/mo in Seoul)**: K8s allocatable `940m` CPU / `1,436 MiB` RAM (`~1,180 MiB` usable after `kube-system`), tainted `cloud.google.com/gke-spot=true:NoSchedule`. Runs **only strictly PVC-free background reconciliation and ACME controllers** (`cert-manager` ×3 `192Mi` requests / `240Mi` limits and `flux-system` ×2 `128Mi` requests / `256Mi` limits, keeping `128Mi` per Flux controller to match `local-k3s`).
- **Rationale**:
  - `gateway` (`gateway/internal/keycloak/oidc.go:105-141`) synchronously calls Keycloak's `/realms/platform/protocol/openid-connect/token/introspect` endpoint on every authenticated request (`mode: shared` in `proj-pn` and `proj-health`) and fails closed (`HTTP 503`) if `keycloak` is unreachable. Keeping right-sized `keycloak` (`832Mi`) on On-Demand `e2-medium` guarantees 24/7 user identity verification without adding `JWKS` offline fallback complexity to `gateway/`.
  - `cert-manager` (needed only every 60 days for Let's Encrypt renewal and every 5 minutes by Flux's `tls` dry-run webhook) and `flux-system` (`emptyDir` only) hold zero `ReadWriteOnce` PVCs and zero live user request paths; a ~60–120s GKE Spot replacement has zero impact on public HTTPS or signed-in users.
  - Passing `--logging=NONE --monitoring=NONE` avoids running duplicate GKE Cloud Logging/Monitoring DaemonSets (`~200 MiB`/node) alongside our own `DaemonSet/fluent-bit`, making `e2-small` (`1,436 MiB` allocatable) viable with `~620 MiB` memory limit headroom (`816 / 1,436 MiB`).
  - Compared to `1 × e2-medium` On-Demand + `1 × e2-medium` Spot, shrinking Node 2 to `e2-small` Spot cuts Node 2's Spot compute bill in half while moving `keycloak` onto 24/7 On-Demand compute.
- **Alternatives Considered**:
  - *`2 × e2-medium` All-Spot*: Rejected because GCP Spot preemption stops `postgres-0`, `openbao-0`, and live `proj-*` pods and risks GCE `ReadWriteOnce` `Multi-Attach` volume locks.
  - *`1 × e2-medium` On-Demand + `1 × e2-medium` Spot (`keycloak` on Spot + `JWKS` fallback in `gateway/`)*: Rejected because it costs more and still blocks new Google OIDC sign-ins and token refreshes during Spot preemption.
  - *`1 × e2-medium` On-Demand + `1 × e2-small` On-Demand*: Rejected on cost when `cert-manager` + `flux-system` tolerate Spot recycling cleanly.
  - *`e2-micro` (1 GB RAM) for Node 2*: Rejected because GKE reserves `~612 MiB` + `~330 MiB` `kube-system` (`anetd` Dataplane V2), leaving `< 100 MiB` and causing `anetd` OOMs.
  - *Converting `cert-manager` into a `CronJob`*: Rejected because `cert-manager` has no one-shot CLI mode, its admission webhook is queried every 5 minutes by Flux `tls` reconciliation, and it breaks `CONTRACT_TLS_INGRESS.md`.

---

## R2: Zero-LoadBalancer Edge Ingress (`Traefik` `hostPort: 80/443`)

- **Decision**: Introduce `deploy/platform/traefik/` (reconciled only by `clusters/gke/layers.yaml`, since K3s bundles Traefik in `kube-system`) deploying `traefik:v3.3.4` in namespace `platform` as a single-replica `Deployment` (`strategy: Recreate` so rolling updates never collide on host ports `80/443`, `resources: {requests: {cpu: 30m, memory: 64Mi}, limits: {cpu: 200m, memory: 96Mi}}`) pinned to `ondemand-core` (`cloud.google.com/gke-provisioning: standard`). The container runs non-root (`runAsNonRoot: true`, `runAsUser: 65532`, `capabilities.drop: ["ALL"]`) listening on container ports `8000` (`web`) and `8443` (`websecure`) bound via `hostPort: 80` and `hostPort: 443`, with a `ClusterIP` `Service` (zero `Service` `type: LoadBalancer`). External traffic reaches Node 1's reserved static IPv4 directly through a GCP VPC firewall rule opening TCP `80,443` to the `ondemand-core` network tag.
- **Rationale**:
  - GCP charges `\$0.005/hr` (\$3.65/mo) per public node IPv4 regardless, whereas a GCP Cloud Network/HTTP LoadBalancer forwarding rule adds **+\$18.00/mo** (`\$0.025/hr`) for a single On-Demand ingress node.
  - Running `traefik` inside namespace `platform` matches Rule 1 of every project's `NetworkPolicy` (`from: namespaceSelector: {matchLabels: {name: platform}}` on ports `80` and `8080`, e.g., `deploy/projects/pn/boundary/network-policy.yaml:15-25`) and keeps `TLSStore/default` and `Secret/{auth,pn,health,blog}-tls` co-located in `platform` (`--providers.kubernetescrd.allowcrossnamespace=true` routes to `identity` and `proj-*` `ClusterIP` services while `Secret` reads remain confined to `platform`).
- **Alternatives Considered**:
  - *`Service` `type: LoadBalancer`*: Rejected because it wastes \$18.00/mo (\$216/yr) forwarding to a single On-Demand node.
  - *`hostNetwork: true` on Traefik*: Rejected because `hostNetwork` bypasses pod network namespace isolation and complicates `NetworkPolicy` pod-IP matching; CNI `hostPort: 80/443` preserves normal Pod IP identity inside namespace `platform`.

---

## R3: Multi-Node & Per-Pool $1/n$ Capacity Math (`scripts/platform-preflight.py`)

- **Decision**: Extend `clusters/gke/capacity.yaml` and `scripts/platform-preflight.py` (backward-compatible with single-node `local-k3s` and `vps-k3s` schemas) with:
  1. Optional `nodeCount` (default `1`) and `pools` array (`name`, `provisioning` `standard|spot`, `allocatable`, `systemReserved`, `labels`, `taints`).
  2. When `Kind == "DaemonSet"`, multiply its container `requests.cpu` and `limits.memory` by `nodeCount` in aggregate `platformReservations` (`fluent-bit` counted `2×` on 2-node GKE).
  3. Verify **per-pool schedulability**: match each workload's `nodeSelector` / `tolerations` to its target pool (`spot-controllers` for `cloud.google.com/gke-spot=true` workloads, `ondemand-core` otherwise) and reject if steady-state platform + tenant `requests.cpu`, `requests.memory`, or `limits.memory` exceed that node pool's `allocatable - systemReserved`.
  4. Apply GKE-specific right-sizing (`keycloak` `100m/832Mi` req · `500m/832Mi` lim, `postgres` `50m` req, `gateway` `25m` req, `openbao` `25m/64Mi` req · `200m/96Mi` lim, `vault-*-proxy` `15m` req × 3, `fluent-bit` `25m` req, `error-detector` `25m` req, `CronJob`s `25m` req, `pn-backend` `100m/256Mi`, `health-backend` `100m/256Mi`, `blog-web` `50m/128Mi`, `flux-system` `128Mi` limits × 2, `cert-manager` `128Mi/64Mi/48Mi` limits) via `clusters/gke/` Kustomize patches so `clusters/local-k3s/` retains its existing baseline memory limits and passes Rule 5 (`Interim Memory Limit Freeze` vs. baseline) unchanged.
- **Rationale**:
  - Aggregate cluster `allocatable` (`1,880m` CPU, `4,408 MiB` RAM) minus `platformReservations` (`970m` CPU, `3,152 MiB` RAM: `576Mi` `systemReserved` + `256Mi` `flux-system` + `240Mi` `cert-manager` + `1,408Mi` `identity` + `192Mi` `observability` + `384Mi` `vault` + `96Mi` `traefik`) yields `applicationCapacity = (910m, 1,256 MiB)` and a $1/3$ per-project budget of **`(303m, 418 MiB)`**, cleanly fitting `proj-pn` (`150m, 384 MiB`), `proj-health` (`150m, 320 MiB`), and `proj-blog` (`50m, 128 MiB`) while leaving `65m` CPU (`875 / 940m`) and `316 MiB` `requests.memory` (`2,656 / 2,972 MiB`, `>= 192 MiB` required) headroom on `ondemand-core` for midnight backup `CronJob`s and `620 MiB` `limits.memory` (`816 / 1,436 MiB`) headroom on `spot-controllers`.
- **Alternatives Considered**:
  - *Shrinking `pn-backend` directly in `deploy/projects/pn/workloads/backend-deployment.yaml`*: Rejected because `platform-preflight.py` Rule 5 compares `local-k3s` against `main` baseline and rejects project memory limit reductions unless scoped to the new `clusters/gke/` environment overlay.

---

## R4: Cloud-Agnostic `networking.k8s.io/v1` `NetworkPolicy` & OpenBao KMS Egress

- **Decision**:
  1. Remove the 4 `cilium.io/v2` `CiliumNetworkPolicy` resources (`openbao-apiserver-egress`, `ai-proxy-egress`, `notify-proxy-egress` in `deploy/platform/vault/network-policy.yaml`, and `pn-backend-translation-egress` in `deploy/projects/pn/boundary/translation-egress.yaml`).
  2. Replace them directly in `deploy/` with standard `networking.k8s.io/v1` `NetworkPolicy` rules:
     - For `pn-backend`, `vault-ai-proxy`, and `vault-notify-proxy`: allow TCP `:443` outbound egress to `ipBlock: {cidr: 0.0.0.0/0, except: [10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 169.254.169.254/32, 100.64.0.0/10]}` alongside explicit `kube-dns` (`:53`) and required internal cluster hops.
     - For `openbao`, `vault-ai-proxy`, and `vault-notify-proxy` `kube-apiserver` access: keep RFC1918 `:443/:6443` (`kube-apiserver`, including the internal `kubernetes.default.svc` ClusterIP) in base, enable `policy-cidr-match-mode: "nodes"` in `deploy/platform/cni/` (`local-k3s` Cilium ConfigMap) so standard `ipBlock` rules match the K3s single-node `kube-apiserver` IP on `local-k3s`, and add a `clusters/gke/layers.yaml` patch on `openbao-ingress-policy` (for `openbao` only) permitting `169.254.169.254/32` TCP `:80,:988` (`gke-metadata-server` Workload Identity) and `199.36.153.8/30` TCP `:443` (`private.googleapis.com` Private Google Access for `cloudkms.googleapis.com`).
  3. Update `scripts/platform-preflight.py` (`check_translation_egress`, `check_ai_gateway_contract`, `check_notify_gateway_contract`) to assert standard `networking.k8s.io/v1` `NetworkPolicy` with mandatory RFC1918 + `169.254.169.254/32` `except` blocks identically across `local-k3s` and `gke`.
- **Rationale**:
  - Managed GKE Dataplane V2 enforces standard `networking.k8s.io/v1` `NetworkPolicy` in eBPF natively without exposing `cilium.io/v2` `toFQDNs` L7 DNS proxy policies.
  - Using `private.googleapis.com` (`199.36.153.8/30`) for `openbao-0` Cloud KMS auto-unseal avoids opening general internet `0.0.0.0/0:443` egress from the Vault pod while keeping `169.254.169.254/32` strictly blocked on all tenant and proxy pods.
- **Alternatives Considered**:
  - *Self-managed Cilium on GKE*: Rejected because running unmanaged Cilium on GKE conflicts with GKE Dataplane V2 / `anetd` lifecycle upgrades and adds `~300 MiB`/node of duplicate CNI overhead.

---

## R5: Google Cloud Build + Public Google Artifact Registry (`GAR`) & `vault-registry-proxy` Retirement

- **Decision**: Build all first-party platform (`gateway`, `vault-db-proxy`, `vault-ai-proxy`, `vault-notify-proxy`, `error-detector`) and tenant (`pn-frontend`, `pn-backend`, `health-frontend`, `health-backend`, `blog-web`) images as multi-architecture (`linux/amd64,linux/arm64`) OCI manifest lists with **Google Cloud Build** (2,500 free build-min/mo) and push immutable `@sha256:...` digests to a single regional **Google Artifact Registry (`GAR`)** Docker repository (`asia-northeast3-docker.pkg.dev/nacfson-pipeline/nacfson-public/<image>@sha256:...`) configured with:
  1. `allUsers` granted `roles/artifactregistry.reader` (publicly readable, matching the public GitHub source repos).
  2. An Artifact Registry cleanup policy keeping only the `2` most recent tagged/pinned digests per package and deleting untagged older layers after 7 days so total storage stays well below the **0.5 GB/mo Free Tier** (~225 MB multi-arch across all 10 active images).
  3. Complete retirement of `Deployment/vault-registry-proxy` and `Service/vault-registry-proxy` (`deploy/platform/vault-proxies/registry-proxy.yaml`), `ServiceAccount/vault-registry-proxy` (`deploy/platform/vault/service-accounts.yaml`), `NetworkPolicy/registry-proxy-policy`, OpenBao role/policy `registry-proxy`, and K3s `/etc/rancher/k3s/registries.yaml` mirror template (`deploy/platform/vault/registries.yaml.template`) across all environments.
- **Rationale**:
  - Making GAR publicly readable allows both `gke` (via free Private Google Access intra-region pulls) and `local-k3s` (via direct HTTPS pulls) to pull identical `@sha256:...` digests with **zero pull secrets** and **zero K3s-specific containerd mirror hacks**, while freeing `25m` CPU / `64Mi` limit in `vault`.
- **Alternatives Considered**:
  - *Private GAR with Kubernetes `imagePullSecrets` on `local-k3s`*: Rejected because storing long-lived GCP service-account JSON keys in Kubernetes `Secret` objects violates Constitution Principle V (Zero Plaintext Secrets).

---

## R6: Fluent Bit Solution B Node-Local Recovery (`/var/lib/fluent-bit-state`) & Kafka Output

- **Decision**:
  1. Replace `PVC/fluent-bit-state` (`ReadWriteOnce`) in `deploy/platform/observability/` with `hostPath: {path: /var/lib/fluent-bit-state, type: DirectoryOrCreate}` mounted at `/state` (`readOnly: false`) on `DaemonSet/fluent-bit`.
  2. Configure `fluent-bit.conf` `[SERVICE]` (`storage.path /state/chunks`, `storage.sync normal`, `storage.checksum off`, `storage.max_chunks_up 128`), `[INPUT] tail` (`DB /state/tail.db`, `storage.type filesystem`), `[FILTER] multiline` (`emitter_storage.type filesystem`), and `[FILTER] rewrite_tag` (`Emitter_Storage.type filesystem`).
  3. Keep `StatefulSet/victorialogs` (`[OUTPUT] http`) in base `deploy/platform/observability/` so `clusters/local-k3s/` remains 100% self-contained for local rehearsals, and add `clusters/gke/observability/` Kustomize overlay that deletes `StatefulSet/victorialogs`, `Service/victorialogs`, and orphaned `NetworkPolicy/victorialogs-ingress`, pauses `Deployment/error-detector` at `replicas: 0` until Spec 014 switches it to Kafka `logs.proj.errors` (while `platform-preflight.py` still pre-reserves its 1-pod `25m/64Mi` slot via `max(replicas, 1)` so re-enabling `replicas: 1` in Spec 014 requires zero re-budgeting, and GKE's Flux `observability` `healthChecks` wait only on `DaemonSet/fluent-bit`), and patches `fluent-bit-config` to `[OUTPUT] kafka` (`Brokers 10.200.0.11:9092,10.200.0.12:9092,10.200.0.13:9092`, `rdkafka.request.required.acks -1`, `rdkafka.enable.idempotence true`, `Retry_Limit False`, `Topics logs.proj.valid` capped at `storage.total_limit_size 192M` and `Topics logs.proj.quarantine` capped at `storage.total_limit_size 64M` = `256M` total node disk cap).
- **Rationale**:
  - A cluster-wide `ReadWriteOnce` GCE Persistent Disk cannot mount on 2 GKE nodes simultaneously (`Multi-Attach error`). Node-local `/var/lib/fluent-bit-state` gives each node independent SQLite offset tracking (`tail.db`) plus filesystem-persisted unacknowledged chunks (`/state/chunks`) bounded strictly to `256 MiB` per node.
- **Alternatives Considered**:
  - *`emptyDir` for `/state`*: Rejected because restarting `fluent-bit` during a cross-cloud GKE ↔ OCI WireGuard outage drops all buffered chunks in memory/emptyDir and re-reads `/var/log/pods` from head (causing duplicates or gaps).

---

## R7: OpenBao Cross-Cluster Snapshot Restore, K8s Auth Re-Binding & `gcpckms` Auto-Unseal

- **Decision**:
  1. Define dedicated `ServiceAccount/openbao` in `deploy/platform/vault/service-accounts.yaml` (`automountServiceAccountToken: false` in base; patched in `clusters/gke/layers.yaml` with `iam.gke.io/gcp-service-account: openbao-kms@nacfson-pipeline.iam.gserviceaccount.com` and `serviceAccountName: openbao` + `automountServiceAccountToken: true` on `StatefulSet/openbao` so GKE Workload Identity's `gke-metadata-server` can authenticate the pod to Cloud KMS).
  2. Update `scripts/vault-configure-k8s-auth.sh` to support GKE seamlessly: read cluster CA from `ConfigMap/kube-root-ca.crt` in `kube-system` (fallback to `kubectl config view --raw`), support `break-glass` / standard GKE cluster-admin RBAC without K3s-only `ssh oracleCloud` assumptions, and remove retired `vault-registry-proxy` role binding.
  3. At Gate 4 cutover, `flux suspend kustomization vault`, temporarily patch `ConfigMap/openbao-config` without `seal "gcpckms"` so `openbao-0` starts under Shamir seal, restore `openbao.snap` (`bao operator raft snapshot restore`), immediately run `scripts/vault-configure-k8s-auth.sh` + `scripts/vault-reconcile.sh --confirm` to replace the overwritten K3s API server CA/JWT with GKE's credentials, and then `flux resume kustomization vault` to reconcile the `clusters/gke/` `seal "gcpckms"` config patch (`$0.06/mo` Cloud KMS key) and execute `bao operator unseal -migrate` with the Shamir recovery key.
- **Rationale**:
  - OpenBao refuses to restore a Shamir-sealed Raft snapshot directly onto an already-`gcpckms`-initialized instance without matching seal configuration; restoring under Shamir first and migrating via `bao operator unseal -migrate` is OpenBao's supported seal-migration path and guarantees unattended auto-unseal on future GKE node upgrades.
- **Alternatives Considered**:
  - *Re-initializing an empty `gcpckms` Vault and manually re-entering secrets*: Rejected because re-creating Transit signing keys or DB passwords invalidates existing artifacts and risks human error during cutover.

---

## R8: Full-State Backup Bundle (`keycloak` + `proj_*` + OpenBao + Edge TLS + VictoriaLogs)

- **Decision**:
  1. Extend `scripts/db-migrate-dump.sh` with `--keycloak` (mapping `TARGET_DB="keycloak"`, `TARGET_ROLE="keycloak_user"`) alongside `--project <slug>` (`proj_<slug>` / `user_<slug>`), keeping SHA-256 sidecar verification (`.sha256`) and zero-plaintext `kubectl exec -i` streaming.
  2. Enable `--features=persistent-user-sessions` on `Deployment/keycloak` so active Keycloak user sessions are persisted in `postgres-0` (`keycloak` DB) rather than volatile JVM heap memory.
  3. Add `scripts/cutover-bundle.sh` (`export` | `verify` | `restore`) orchestrating the 5-part SHA-256-verified migration bundle (stored strictly outside Git in an operator-local `0700` staging directory): `openbao.snap`, `keycloak.dump.gz`, `proj_pn.dump.gz` + `proj_health.dump.gz`, `edge-tls-secrets.yaml` (`auth-tls`, `pn-tls`, `health-tls`, `blog-tls` in `platform` + Let's Encrypt account key in `cert-manager`), and `victoria-logs-data.tar.gz` (held for OCI VM 4 import in Spec 013).
- **Rationale**:
  - Restoring `keycloak` alongside `proj_*` preserves every returning user's exact `(issuer, subject)` UUID and realm session lifetimes (`300 | 1800 | 36000`). Pre-seeding `auth-tls`, `pn-tls`, `health-tls`, and `blog-tls` before flipping DNS `A` records avoids Let's Encrypt HTTP-01 challenge failures while global DNS caches propagate.
- **Alternatives Considered**:
  - *Letting Keycloak recreate an empty realm on GKE*: Rejected because Keycloak generates new random `sub` UUIDs on first Google OIDC login, orphaning existing user rows in `proj_pn` and `proj_health`.

---

## R9: Constitution Amendment `2.3.0` Scope

- **Decision**: Draft **Constitution Amendment `2.3.0`** (minor version bump `2.2.1 → 2.3.0` in `.specify/memory/constitution.md`, requiring maintainer approval on PR merge per Governance §57–65) covering three narrowly scoped evolutions:
  1. **Collector Node-Local Recovery State (extends Clarification 2.2.1)**: Permits `DaemonSet/fluent-bit` in platform namespace `observability` to mount `/var/lib/fluent-bit-state` (`readOnly: false`, `DirectoryOrCreate`) alongside `/var/log/pods` (`readOnly: true`); all other `hostPath` mounts in `observability` or `proj-*` remain prohibited.
  2. **Public Container Registry Portability (updates Multi-Architecture Image Packaging)**: Permits first-party multi-arch/target-arch OCI images to be hosted in a publicly readable Google Artifact Registry (`GAR`) repository (`@sha256:...` digests) alongside or in place of `GHCR`, retiring `vault-registry-proxy`.
  3. **Cloud KMS Seal Wrapper Exception (Principle V)**: Permits `openbao-0` in `vault` to use an external Cloud KMS envelope key (`gcpckms`) authenticated exclusively via keyless Workload Identity (zero plaintext KMS credentials in Git or Kubernetes `Secret`s) solely to wrap OpenBao's master seal key for unattended node-upgrade recovery.
- **Rationale**: Keeps `.specify/memory/constitution.md`, `scripts/platform-preflight.py`, and `docs/platform-architecture.html` 100% aligned with zero unratified exceptions.
- **Alternatives Considered**:
  - *Bypassing preflight checks via environment flags*: Prohibited by Constitution Governance §56 ("Manifests, charts, and operational changes that contradict this document MUST NOT be approved or deployed").
