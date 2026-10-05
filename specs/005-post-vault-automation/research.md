# Phase 0 Research: Automated Post-Vault Workload Orchestration

## 1. Workload Credential Delivery Mechanism

### Decision: Native Kubernetes ServiceAccount InitContainer Ingestion
- **Context**: Workloads (`postgres-0`, `keycloak`, `gateway`) need runtime secrets injected into in-memory `tmpfs` mounts without storing plaintext credentials in Git, environment variables, or persistent storage.
- **Alternatives Evaluated**:
  1. *Vault Agent Mutating Webhook Injector*: Runs a cluster-wide daemon mutating pod specs on admission. **Rejected**: Violates minimal resource footprint and 1/n budget; adds complex mutating webhook failure modes to single-node K3s.
  2. *Static Secret Sync Controller (External Secrets Operator)*: Syncs Vault secrets into Kubernetes `v1/Secret` objects. **Rejected**: Explicitly violates Constitution Principle V ("Plaintext secret material MUST NOT be delivered through Kubernetes Secrets").
  3. *Native InitContainer via Vault Kubernetes Auth (`auth/kubernetes`)*: Pod mounts projected ServiceAccount token (`automountServiceAccountToken: true` for the platform SA), runs a minimal initContainer that authenticates via `POST /v1/auth/kubernetes/login`, fetches the scoped secret, writes it to the shared in-memory `emptyDir` volume (`0400`), and terminates.
- **Rationale**: 100% compliant with Constitution Principle II (Restricted Pod Security) and Principle V (zero plaintext secrets on disk). No daemon sidecars running permanently, 0 MB steady-state memory overhead once the pod is initialized.

---

## 2. Multi-Architecture Container Image Delivery

### Decision: Automated GitHub Actions CI Publishing to GHCR
- **Context**: Custom Go services (`vault-db-proxy`, `vault-registry-proxy`, and `platform-auth-gateway`) currently fail with `ErrImagePull` because their container images have not been published to GHCR.
- **Alternatives Evaluated**:
  1. *Manual Host Node Build / Import (`ctr image import`)*: Building tarballs locally and importing over SSH. **Rejected**: Violates declarative GitOps, breaks on node replacement, and violates Constitution Principle I.
  2. *GitHub Actions Multi-Arch OCI Pipeline (`build-images.yaml`)*: Automated workflow using QEMU + Docker Buildx to cross-compile Go binaries for `linux/amd64` and `linux/arm64` (aarch64 for Oracle Cloud VPS), publishing to `ghcr.io/nacfson/*` with immutable SHA digests.
- **Rationale**: Aligns with Constitution Principle I & VI (environmental portability and immutable digests). Cluster automatically pulls new images upon Git push.

---

## 3. Storage & Retention for Automated Backups

### Decision: Dedicated Local Persistent Volume with Cron Retention Pruning
- **Context**: Spec 005 requires automated daily backups for both Vault Raft storage and PostgreSQL catalogs without manual operator intervention.
- **Alternatives Evaluated**:
  1. *Offsite Object Storage (OCI/S3 Sync)*: Syncing backups to external cloud buckets. **Deferred**: Requires configuring out-of-band cloud credentials and IAM policies; can be added in a future phase.
  2. *Dedicated Local PVC Storage with 7-Day Pruning*: Storing snapshots in dedicated, protected PersistentVolumeClaims (`openbao-backup-pvc`, `postgres-backup-pvc`) using K3s `local-path` storage, with automated retention pruning (`find /backups -mtime +7 -delete`).
- **Rationale**: Simplest, self-contained, zero cloud billing or external IAM dependency; satisfies single-node operational needs immediately.

---

## 4. Post-Unseal Convergence Safety Gate

### Decision: Dependency-Ordered Flux Kustomization Cascade
- **Context**: Once Vault is unsealed by the operator, downstream layers must converge automatically without race conditions.
- **Architecture**:
  1. `vault`: Health check passes on `StatefulSet/vault/openbao` (`1/1 Running`).
  2. `vault-proxies`: Automatically reconciles once `vault` is ready, running `vault-db-proxy` and `vault-registry-proxy`.
  3. `database`: Reconciles PostgreSQL StatefulSet with secret-fetching initContainer, unblocking `postgres-init-job`.
  4. `identity`: Reconciles Keycloak and Go Authentication Gateway once PostgreSQL is healthy.
  5. `ingress` & `proj-pn`: Reconciles edge routes and sandboxed tenant workloads.
