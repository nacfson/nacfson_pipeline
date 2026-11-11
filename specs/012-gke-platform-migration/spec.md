# Feature Specification: GKE Platform Portability & Full-State Cutover

**Feature Branch**: `012-gke-platform-migration`

**Created**: 2026-11-10

**Status**: Draft

**Input**: User description: "Let's start 012 spec. Migrate core platform and tenant workloads to Google Kubernetes Engine (GKE) in a single-shot cutover with GKE Dataplane V2 overlay, Fluent Bit Solution B node-local recovery, multi-node preflight math, and full-state backup/restore."

## Clarifications

### Session 2026-11-10

- Q: How should `local-k3s` pull container images from Google Artifact Registry (GAR) during local rehearsals since `local-k3s` does not run on GCP nodes with GKE Node IAM? → A: Make the GAR image repository publicly readable (`allUsers` granted `roles/artifactregistry.reader`, matching the public GitHub source repositories) so both `gke` and `local-k3s` pull identical `@sha256` digests from GAR without credentials and `vault-registry-proxy` is retired across all environments.
- Q: How should `pn-backend` Wiktionary (`api.wiktapi.dev`) and proxy outbound HTTPS egress be enforced on GKE without `CiliumNetworkPolicy` (`toFQDNs`)? → A: Replace the 4 `CiliumNetworkPolicy` blocks directly in `deploy/` with standard Kubernetes `networking.k8s.io/v1` `NetworkPolicy` opening TCP port `443` egress (`ipBlock: 0.0.0.0/0` with `except` blocking private RFC1918 ranges and `169.254.169.254/32` metadata server) scoped to `pn-backend`, `vault-ai-proxy`, and `vault-notify-proxy`, and keep `local-k3s` maintained with the exact same cloud-agnostic manifests as `clusters/gke/`.
- Q: What downstream output sink should Fluent Bit target in Spec 012 before the OCI Kafka cluster (Spec 013) is provisioned? → A: Remove in-cluster `StatefulSet/victorialogs` from `clusters/gke/` immediately in Spec 012 (freeing `250m` CPU / `512Mi` RAM and the `10Gi` PVC on GKE) and configure Fluent Bit `[OUTPUT] kafka` (`acks=all`, `Topics logs.proj.valid` / `logs.proj.quarantine`, `storage.total_limit_size 256M`) pointing to the 3 OCI WireGuard broker addresses from day 1.
- Q: Should `local-k3s` be retained after migrating production to GKE? → A: Yes — keep and maintain `clusters/local-k3s/` aligned with the GKE changes (shared standard `networking.k8s.io/v1` `NetworkPolicy`, public GAR image pulls, and Fluent Bit Solution B recovery) to preserve cloud-agnostic portability (Constitution Principle VI).
- Q: How should GKE worker nodes and edge ingress be sized and split to balance monthly cost against 24/7 project, database, and Keycloak user-identity verification uptime? → A: Use **Design 1** — a hybrid 2-node Zonal GKE Standard topology (`1 × e2-medium` On-Demand + `1 × e2-small` Spot, created with `--logging=NONE --monitoring=NONE` and no GCP LoadBalancer, ~\$53.31/mo PAYG / ~\$41.70/mo 1-yr in `asia-northeast3-a` Seoul, or ~\$40.69/mo PAYG in US Tier-1): expose Traefik directly via `hostPort: 80/443` on the On-Demand node's static IP, keep **100% of live user traffic, OIDC token introspection (`keycloak` right-sized to `832Mi` in `clusters/gke/` with `--features=persistent-user-sessions`), and `ReadWriteOnce` PVC workloads** (`proj-pn` with `pn-backend` right-sized to `256Mi` in `clusters/gke/`, `proj-health`, `proj-blog`, `postgres-0`, `openbao-0`, `vault-*-proxy`, `gateway`, `traefik`, `error-detector`, `postgres-init-job`, `postgres-daily-backup`, `openbao-daily-snapshot`) on the On-Demand `e2-medium` node 24/7, and offload strictly PVC-free background reconciliation/ACME controllers (`cert-manager` ×3 and `flux-system` ×2) onto the tiny `e2-small` Spot node (`cloud.google.com/gke-spot=true:NoSchedule`).
- Q: Which GCP Project ID and region/zone should `clusters/gke/`, Google Artifact Registry (`GAR`), and Cloud KMS target? → A: Target GCP Project ID **`nacfson-pipeline`** in **`asia-northeast3` / `asia-northeast3-a` (Seoul)** as the primary low-latency Asia zone (`asia-northeast3-docker.pkg.dev/nacfson-pipeline/nacfson-public/<image>@sha256:...`), while keeping `platform-preflight.py` regex validation region-agnostic (`<region>-docker.pkg.dev/nacfson-pipeline/...`) so switching between `asia-northeast3-a` (Seoul) and `asia-northeast1-a` (Tokyo) requires no preflight code changes.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Offline GKE Environment Overlay & Multi-Node Preflight Validation (Priority: P1)

Platform operators need a declarative `clusters/gke/` environment overlay (`1 × e2-medium` On-Demand + `1 × e2-small` Spot on a Zonal GKE Standard cluster with `--logging=NONE --monitoring=NONE`, exposing Traefik via `hostPort: 80/443` on the On-Demand node without a GCP LoadBalancer) and a synchronized `clusters/local-k3s/` overlay that both render cleanly and pass all static preflight, admission, network boundary, node-pool placement, and $1/n$ multi-node capacity checks offline before any cloud resources are provisioned.

**Why this priority**: Issue #144 enforces a strict Gate 1 constraint: zero cloud VMs or clusters may be provisioned or modified until 100% of manifests, scripts, and preflight rules pass offline. Keeping `local-k3s` and `gke` on the same standard Kubernetes primitives proves cloud-agnostic portability (Principle VI).

**Independent Test**: Run `./scripts/render-validate.sh local-k3s`, `./scripts/render-validate.sh gke`, and `python3 -m unittest discover tests/preflight` offline; verify that all Kustomize layers render, pass schema checks, multiply DaemonSet reservations across the configured node count, verify On-Demand (`e2-medium`) vs. Spot (`e2-small`) per-node CPU/memory fit, and enforce standard `networking.k8s.io/v1` outbound egress boundaries identically across both `local-k3s` and `gke`.

**Acceptance Scenarios**:

1. **Given** the `clusters/gke/` environment overlay with `nodeCount: 2` (`1 × e2-medium` On-Demand + `1 × e2-small` Spot), **When** `scripts/platform-preflight.py` evaluates capacity, **Then** DaemonSet CPU and memory reservations are multiplied by the node count and both the On-Demand `e2-medium` pool (hosting live projects, `keycloak`, `postgres-0`, `openbao-0`, `vault-*-proxy`, `gateway`, `traefik`, `error-detector`, and `RWO` backup/init jobs) and the Spot `e2-small` pool (hosting PVC-free `cert-manager` ×3 and `flux-system` ×2) are verified to fit within per-node and aggregate $1/n$ allocatable CPU and memory limits.
2. **Given** `deploy/platform/governance/manual-change-policy.yaml`, **When** GKE system controllers (`system:serviceaccount:kube-system:*`, GKE node/addon principals) or `.gke.io`/`.google.com` CRDs reconcile in managed namespaces, **Then** the admission policy permits them alongside K3s controller principals while continuing to reject unauthorized manual mutations.
3. **Given** the unified network policy manifests in `deploy/`, **When** rendered for both `gke` and `local-k3s`, **Then** the 4 `CiliumNetworkPolicy` blocks are replaced by portable `networking.k8s.io/v1` `NetworkPolicy` rules (opening TCP `:443` public egress with `except` blocks for RFC1918 and `169.254.169.254/32` on `pn-backend`, `vault-ai-proxy`, and `vault-notify-proxy`, plus `kube-apiserver` CIDR egress for `openbao`), so both `gke` and `local-k3s` enforce identical cloud-agnostic network boundaries.
4. **Given** `deploy/platform/observability/error-detector.yaml`, **When** rendered for `gke` and `local-k3s`, **Then** `PVC/error-detector-state` uses the environment's default storage class instead of hardcoding `storageClassName: local-path`.

---

### User Story 2 - Crash- & Outage-Resilient Log Collector State on Multi-Node Clusters (Priority: P1)

Platform operators need the node log collector (`DaemonSet/fluent-bit`) to run across multiple GKE nodes without `ReadWriteOnce` PersistentVolumeClaim `Multi-Attach` errors while preserving 100% of both read-offset bookmarks (`tail.db`) and unacknowledged in-flight log batches (`/state/chunks`) across pod restarts and cross-cloud network outages.

**Why this priority**: A cluster-wide `ReadWriteOnce` PVC prevents `fluent-bit` from starting on a second GKE node, while removing state persistence loses logs during pod restarts or WAN outages. Solution B (`hostPath: /var/lib/fluent-bit-state` with `tail.db` + `storage.type filesystem`) is required on day 1 of multi-node operation.

**Independent Test**: Render `deploy/platform/observability/` and run preflight + observability manifest tests; on `local-k3s`, restart the `fluent-bit` pod while a test container emits sequential log lines during a simulated sink outage, and verify zero lost or duplicated log lines after recovery.

**Acceptance Scenarios**:

1. **Given** `DaemonSet/fluent-bit` deployed on a multi-node cluster, **When** pods schedule on every node, **Then** each pod mounts node-local `/var/lib/fluent-bit-state` (`type: DirectoryOrCreate`) at `/state` with zero `ReadWriteOnce` PVC contention.
2. **Given** `fluent-bit.conf` configured with `DB /state/tail.db`, `storage.path /state/chunks`, `storage.type filesystem`, `Emitter_Storage.type filesystem`, and `storage.total_limit_size 256M`, **When** the downstream sink is unreachable and the `fluent-bit` pod restarts, **Then** in-flight unacknowledged chunks and the file byte offset are recovered from `/var/lib/fluent-bit-state` without dropping or duplicating records.
3. **Given** Constitution Clarification 2.2.1 and `scripts/platform-preflight.py`, **When** preflight inspects `observability`, **Then** `DaemonSet/fluent-bit` is permitted to mount `/var/log/pods` (read-only) and `/var/lib/fluent-bit-state` (read-write), while any other `hostPath` in `observability` or any `hostPath` in `proj-*` is rejected.

---

### User Story 3 - Full-State Backup Bundle & Single-Shot GKE Cutover with Identity Continuity (Priority: P2)

Platform operators need a repeatable backup extraction, local restore rehearsal, and single-shot cutover procedure that migrates OpenBao secrets, PostgreSQL tenant databases (`proj_*`), the Keycloak identity database (`keycloak`), and edge TLS certificates (`auth-tls`, `pn-tls`, `health-tls`, `blog-tls`) from `vps-k3s` to GKE with zero loss of user `(issuer, subject)` identities or HTTPS availability.

**Why this priority**: Restoring only `proj_*` databases breaks existing user identities if Keycloak generates new user `sub` UUIDs, and restoring an OpenBao Raft snapshot from `vps-k3s` overwrites `auth/kubernetes/config` with the old K3s cluster CA and JWT reviewer token.

**Independent Test**: Run the full-state extraction and restore rehearsal against `local-k3s` (`scripts/verify-backup.sh` and `scripts/smoke-auth-chain.sh`), confirming that restored `keycloak` + `proj_*` databases, OpenBao Kubernetes auth re-binding, and pre-seeded TLS Secrets pass end-to-end sign-in without manual database edits.

**Acceptance Scenarios**:

1. **Given** a production backup bundle from `vps-k3s`, **When** restored onto a target cluster, **Then** SHA-256 checksums are verified for the OpenBao `.snap` file, `keycloak` and `proj_*` PostgreSQL dumps, edge TLS Secrets, and the VictoriaLogs archive before any state is imported.
2. **Given** an OpenBao Raft snapshot restored from `vps-k3s` onto GKE, **When** `scripts/vault-configure-k8s-auth.sh` and `scripts/vault-reconcile.sh --confirm` execute immediately post-restore, **Then** OpenBao replaces the old K3s CA and token reviewer JWT with the GKE cluster's credentials and all Vault proxies (`db-proxy`, `vault-ai-proxy`, `vault-notify-proxy`) authenticate successfully.
3. **Given** the restored `keycloak` and `proj_*` PostgreSQL databases and pre-seeded edge TLS Secrets on GKE, **When** a returning user signs in via Google OIDC, **Then** Keycloak resolves the existing `(issuer, subject)` UUID and realm session lifetimes (`300 | 1800 | 36000`) and Traefik serves valid TLS certificates immediately upon DNS cutover.

---

### Edge Cases

- **OpenBao Raft Snapshot K8s Auth Overwrite**: Restoring `openbao.snap` overwrites `auth/kubernetes/config` inside OpenBao with the source cluster's API server CA and JWT; the restore script MUST re-run `vault-configure-k8s-auth.sh` automatically before unsealing/releasing dependent proxies.
- **GKE Spot VM Preemption of Offloaded Workloads & Planned Node Maintenance**: All live user-facing workloads (`traefik`, `gateway`, `proj-*`), `keycloak` (right-sized to `832Mi` with `--features=persistent-user-sessions`), and all `ReadWriteOnce` PVC workloads (`openbao-0`, `postgres-0`, `error-detector`, `postgres-daily-backup`, `openbao-daily-snapshot`) stay on the On-Demand `e2-medium` node 24/7 so Spot preemptions never stop projects, never interrupt user OIDC identity verification (`HTTP 503`), and never attach or truncate `RWO` disks. When Google preempts the tiny `e2-small` Spot node hosting PVC-free `cert-manager` and `flux-system`, public HTTPS and signed-in sessions continue without interruption while GKE replaces the Spot node in ~60–120s; if the On-Demand node undergoes a planned GKE OS upgrade, `openbao-0` auto-unseals unattended via GCP Cloud KMS (`gcpckms`).
- **Collector Disk Cap During Extended WAN Outage**: If downstream log ingestion is unreachable for hours, Fluent Bit's filesystem buffer (`/var/lib/fluent-bit-state/chunks`) MUST stop growing at `storage.total_limit_size 256M` so it never exhausts the GKE node's boot disk.
- **DNS Cutover Propagation Window**: Pre-seeding `auth-tls`, `pn-tls`, `health-tls`, and `blog-tls` Secrets in `platform` before flipping DNS `A` records prevents Let's Encrypt HTTP-01 challenge failures while global DNS caches still point to the old IP.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The repository MUST provide a declarative `clusters/gke/` FluxCD environment overlay targeting a Zonal GKE Standard cluster (created with `--logging=NONE --monitoring=NONE`) using **Design 1** (`1 × e2-medium` On-Demand node + `1 × e2-small` Spot node): `traefik` MUST bind directly to `hostPort: 80/443` on the On-Demand node (`Service` `type: ClusterIP` + static node IP, with zero GCP `LoadBalancer`), 100% of live user-facing, OIDC identity (`keycloak` right-sized to `832Mi` in `clusters/gke/`), proxy, and `ReadWriteOnce` PVC workloads (`proj-pn` with `pn-backend` right-sized to `256Mi` in `clusters/gke/`, `proj-health`, `proj-blog`, `postgres-0`, `openbao-0`, `vault-*-proxy`, `gateway`, `traefik`, `error-detector`, `postgres-init-job`, `postgres-daily-backup`, `openbao-daily-snapshot`) MUST run on the On-Demand `e2-medium` node, strictly PVC-free background reconciliation/ACME Deployments (`cert-manager` ×3 and `flux-system` ×2) MUST schedule onto the `e2-small` Spot node via `nodeSelector` and `cloud.google.com/gke-spot=true:NoSchedule` tolerations, and `clusters/local-k3s/` MUST remain maintained in lockstep for cloud-agnostic verification.
- **FR-002**: `deploy/platform/governance/manual-change-policy.yaml` MUST allowlist GKE control-plane and node service accounts (`system:serviceaccount:kube-system:*`, `system:gke-*`, `.gke.io` and `.google.com` API groups) alongside existing K3s controller identities without weakening manual-change rejection for human or unauthorized principals.
- **FR-003**: `scripts/platform-preflight.py` MUST support multi-node capacity manifests (`clusters/gke/capacity.yaml`) by multiplying DaemonSet resource reservations by the declared node count and verifying both cluster-wide $1/n$ project budgets and per-node-pool (`e2-medium` On-Demand vs. `e2-small` Spot) CPU/memory schedulability.
- **FR-004**: Container image builds and registry hosting MUST migrate to **Google Cloud Build** (within the 2,500 build-minutes/month Free Tier) and a publicly readable **Google Artifact Registry (GAR)** repository (`allUsers` granted `roles/artifactregistry.reader`, with a cleanup policy keeping total storage under the 0.5 GB/month Free Tier), allowing both `gke` and `local-k3s` to pull identical `@sha256:...` digests directly without pull credentials, retiring `vault-registry-proxy` across all environments, and updating `platform-preflight.py`, `image-bump-request.yaml`, and `update-project-images` to validate GAR digests.
- **FR-005**: The 4 `CiliumNetworkPolicy` manifests in `deploy/` MUST be migrated to portable, cloud-agnostic `networking.k8s.io/v1` `NetworkPolicy` rules (opening TCP port `443` public egress via `ipBlock: 0.0.0.0/0` with `except` blocks for RFC1918 private subnets `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16` and metadata server `169.254.169.254/32` scoped specifically to `pn-backend`, `vault-ai-proxy`, and `vault-notify-proxy`), and `scripts/platform-preflight.py` MUST validate these portable `NetworkPolicy` rules identically across both `clusters/gke/` and `clusters/local-k3s/`.
- **FR-006**: `DaemonSet/fluent-bit` MUST replace the cluster-wide `ReadWriteOnce` `fluent-bit-state` PVC with a per-node `hostPath` (`/var/lib/fluent-bit-state`, `type: DirectoryOrCreate`) configured with `DB /state/tail.db`, `storage.path /state/chunks`, `storage.type filesystem`, `Emitter_Storage.type filesystem`, and `storage.total_limit_size 256M`, while `clusters/gke/` omits in-cluster `StatefulSet/victorialogs`, `Service/victorialogs`, and `NetworkPolicy/victorialogs-ingress` from day 1, pauses `Deployment/error-detector` at `replicas: 0` (with Flux `observability` `healthChecks` waiting only on `DaemonSet/fluent-bit` until Spec 014 switches `error-detector` to Kafka `logs.proj.errors`), and configures `[OUTPUT] kafka` (`rdkafka.request.required.acks -1`, topics `logs.proj.valid` and `logs.proj.quarantine`) pointing to the 3 OCI WireGuard broker addresses.
- **FR-007**: Constitution Clarification 2.2.1 (via formal Constitution Amendment `2.3.0`) and `scripts/platform-preflight.py` MUST be updated to permit `/var/lib/fluent-bit-state` (`readOnly: false`) on `DaemonSet/fluent-bit` in namespace `observability` alongside `/var/log/pods` (`readOnly: true`), ratify public Google Artifact Registry (`GAR`) digest pulls alongside `GHCR`, and authorize GCP Cloud KMS (`gcpckms`) auto-unseal for `openbao-0`, while rejecting any unauthorized `hostPath` mount.
- **FR-008**: On GKE, `keycloak` MUST run on the On-Demand `e2-medium` node with persistent database user sessions (`--features=persistent-user-sessions` stored in `postgres-0`) so user identity verification stays online 24/7 and planned restarts never wipe active sessions, and `openbao-0` MUST be migrated from Shamir seal to **GCP Cloud KMS (`gcpckms`) auto-unseal** via GKE Workload Identity (`bao operator unseal -migrate`) immediately after restoring the `vps-k3s` Raft snapshot at Gate 4 so planned GKE node upgrades or restarts unseal `openbao-0` unattended.
- **FR-009**: `scripts/db-migrate-dump.sh` and the backup/restore scripts MUST export, checksum-verify (SHA-256), and restore the `keycloak` database alongside all `proj_<slug>` databases, OpenBao Raft snapshots (`.snap`), edge TLS Secrets (`auth-tls`, `pn-tls`, `health-tls`, `blog-tls`), and the 30-day VictoriaLogs archive (`victoria-logs-data.tar.gz`).
- **FR-010**: The OpenBao restore procedure MUST automatically re-bind Kubernetes authentication (`scripts/vault-configure-k8s-auth.sh`) and reconcile policies (`scripts/vault-reconcile.sh --confirm`) immediately after restoring a cross-cluster Raft snapshot.
- **FR-011**: `clusters/vps-k3s/` MUST remain validated in parallel with `local-k3s` and `gke` during Gates 1–3; at Gate 4, as soon as `scripts/smoke-auth-chain.sh` passes on GKE and DNS `A` records flip, the `vps-k3s` OCI VM MUST be **immediately shrunk or recreated into Kafka Broker VM 1 within the same cutover window**, and `clusters/vps-k3s/` retired.

### Key Entities

- **Cluster Capacity Profile (`clusters/gke/capacity.yaml`)**: Declares per-node-pool allocatable CPU/memory (`1 × e2-medium` On-Demand + `1 × e2-small` Spot), `nodeCount: 2`, and platform reservations for multi-node $1/n$ budget enforcement.
- **Collector Recovery State (`/var/lib/fluent-bit-state`)**: Per-node directory holding SQLite byte-offset bookmarks (`tail.db`) and filesystem-buffered log chunks (`chunks/`) awaiting downstream acknowledgement.
- **Full-State Migration Bundle**: SHA-256-verified archive containing `openbao.snap`, `keycloak.dump.gz`, `proj_*.dump.gz`, edge TLS Secret manifests (out-of-git), and `victoria-logs-data.tar.gz`.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: 100% of Kustomize layers in `clusters/local-k3s/` and `clusters/gke/` render cleanly and pass schema validation and `platform-preflight.py` checks offline in under 60 seconds.
- **SC-002**: Zero log lines are lost or duplicated when `fluent-bit` pods restart during a 2-minute simulated downstream outage on a multi-node or local rehearsal cluster (within the 256 MiB per-node disk buffer cap).
- **SC-003**: 100% of existing user accounts retain their exact `(issuer, subject)` identity and active project data after restoring the production backup bundle onto a clean rehearsal cluster and running `scripts/smoke-auth-chain.sh`.
- **SC-004**: Edge HTTPS routes (`auth`, `pn`, `health`, `blog`) serve valid, trusted TLS certificates with zero certificate warning window at the moment DNS `A` records switch to the GKE ingress IP.

## Assumptions

- A single Zonal GKE Standard cluster is used so the GKE cluster management fee is covered by the Google Cloud Free Tier.
- External DNS `A` records for `nacfson.cloud` subdomains have their TTL lowered to 60–300 seconds at least 24 hours prior to Gate 4 cutover.
- Cross-cloud Kafka streaming (`Fluent Bit → OCI Kafka ×3 → Flink`) is decoupled into Spec 013; Spec 012 ensures the GKE platform and Fluent Bit local filesystem buffering are ready for that sink.
