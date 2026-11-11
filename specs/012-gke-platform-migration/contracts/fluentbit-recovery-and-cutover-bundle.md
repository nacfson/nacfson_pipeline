# Contract: Fluent Bit Node-Local Recovery & Full-State Cutover Bundle

**Feature**: [`specs/012-gke-platform-migration/spec.md`](../spec.md)
**Enforced By**: `.specify/memory/constitution.md` (Amendment `2.3.0`), `scripts/platform-preflight.py`, `scripts/db-migrate-dump.sh`, `scripts/cutover-bundle.sh`

---

## 1. Fluent Bit Solution B Node-Local Recovery (`FR-006`, `FR-007`)

### 1.1 `DaemonSet/fluent-bit` Volume & Toleration Invariants

1. `PVC/fluent-bit-state` is deleted from `deploy/platform/observability/`.
2. `DaemonSet/fluent-bit` declares exactly two `hostPath` volumes in namespace `observability`:
   - `varlogpods`: `path: /var/log/pods`, `type: Directory`, mounted `readOnly: true` at `/var/log/pods`.
   - `state`: `path: /var/lib/fluent-bit-state`, `type: DirectoryOrCreate`, mounted `readOnly: false` at `/state`.
3. `scripts/platform-preflight.py` rejects any other `hostPath` path, any `readOnly: false` mount on `/var/log/pods`, any `hostPath` on non-`fluent-bit` workloads in `observability`, and any `hostPath` in `proj-*`.
4. `DaemonSet/fluent-bit` tolerates `cloud.google.com/gke-spot=true:NoSchedule` so log collection runs on every worker node.

### 1.2 `fluent-bit.conf` Buffering & GKE Kafka Sink

1. Base `fluent-bit.conf` (`local-k3s` & `gke`):
   - `[SERVICE]`: `storage.path /state/chunks`, `storage.sync normal`, `storage.checksum off`, `storage.max_chunks_up 128`
   - `[INPUT] tail`: `DB /state/tail.db`, `storage.type filesystem`
   - `[FILTER] multiline`: `emitter_storage.type filesystem`
   - `[FILTER] rewrite_tag`: `Emitter_Name rt_quarantine`, `Emitter_Storage.type filesystem`
2. `clusters/gke/observability/` Kustomize overlay:
   - Deletes `StatefulSet/victorialogs`, `Service/victorialogs`, and orphaned `NetworkPolicy/victorialogs-ingress`.
   - Patches `Deployment/error-detector` to `replicas: 0` until Spec 014 switches it to consume Kafka `logs.proj.errors` (retaining `PVC/error-detector-state` and pre-reserving its `25m/64Mi` 1-pod budget slot via `max(replicas, 1)` in `platform-preflight.py`), and scopes GKE's Flux `observability` `healthChecks` to `DaemonSet/fluent-bit` only.
   - Replaces `[OUTPUT]` with two `[OUTPUT] kafka` blocks pointing to `Brokers 10.200.0.11:9092,10.200.0.12:9092,10.200.0.13:9092` (OCI WireGuard broker IPs), `rdkafka.request.required.acks -1`, `rdkafka.enable.idempotence true`, `Retry_Limit False`:
     - `Match proj.*` → `Topics logs.proj.valid`, `storage.total_limit_size 192M`
     - `Match quarantine.*` → `Topics logs.proj.quarantine`, `storage.total_limit_size 64M`
   - Total node-local disk cap per GKE node is strictly `192M + 64M = 256 MiB`.

---

## 2. Full-State Backup, Restore & `gcpckms` Auto-Unseal Contract (`FR-008`–`FR-011`)

### 2.1 `scripts/db-migrate-dump.sh` CLI Extension

Supports `--keycloak` alongside `--project <slug>`:

```bash
# Export Keycloak identity DB (preserves realm 'platform', user sub UUIDs, and persistent sessions):
./scripts/db-migrate-dump.sh --keycloak --export "$BUNDLE_DIR/keycloak.dump.gz" [--ssh oracleCloud]

# Import Keycloak identity DB onto target cluster:
./scripts/db-migrate-dump.sh --keycloak --file "$BUNDLE_DIR/keycloak.dump.gz" --yes
```

When `--keycloak` is passed, `TARGET_DB="keycloak"` and `TARGET_ROLE="${KEYCLOAK_DB_ROLE:-keycloak_user}"`.

### 2.2 `scripts/cutover-bundle.sh` Subcommands

```bash
# 1. Extract full production state from vps-k3s into a 0700 out-of-git directory + manifest.sha256:
./scripts/cutover-bundle.sh export --bundle-dir "$HOME/.nacfson-cutover/20261110" --ssh oracleCloud

# 2. Offline integrity & archive header verification (Gate 3):
./scripts/cutover-bundle.sh verify --bundle-dir "$HOME/.nacfson-cutover/20261110"

# 3. Restore onto local-k3s (Gate 3 rehearsal) or GKE (Gate 4 cutover; --migrate-kms enabled by default on gke):
./scripts/cutover-bundle.sh restore --bundle-dir "$HOME/.nacfson-cutover/20261110" --target local-k3s|gke [--migrate-kms|--no-migrate-kms]
```

### 2.3 Mandatory Restore & KMS Seal-Migration Sequence (`restore --target gke --migrate-kms`)

1. Verify `manifest.sha256` across all 6 artifacts (`openbao.snap`, `keycloak.dump.gz`, `proj_pn.dump.gz`, `proj_health.dump.gz`, `edge-tls-secrets.yaml`, `victoria-logs-data.tar.gz`); abort immediately on any mismatch.
2. Pre-seed `edge-tls-secrets.yaml` into namespaces `platform` and `cert-manager` (`--as=system:admin --as-group=platform:break-glass` or GKE break-glass RBAC).
3. Run `flux suspend kustomization vault`, boot `openbao-0` (`serviceAccountName: openbao`, annotated `iam.gke.io/gcp-service-account: openbao-kms@nacfson-pipeline.iam.gserviceaccount.com`) with Shamir-only `openbao-config`, restore `openbao.snap`, unseal, and **immediately execute** `scripts/vault-configure-k8s-auth.sh` + `scripts/vault-reconcile.sh --confirm` so OpenBao replaces the old K3s API server CA/JWT with GKE's `kube-root-ca.crt` and token reviewer JWT.
4. Restore `keycloak.dump.gz`, `proj_pn.dump.gz`, and `proj_health.dump.gz` into `postgres-0` and restart `Deployment/keycloak` (`--features=persistent-user-sessions`).
5. Run `./scripts/smoke-auth-chain.sh` against Node 1's static IP via `--resolve` before flipping DNS `A` records.
6. Run `flux resume kustomization vault` to reconcile the `seal "gcpckms"` config overlay on `openbao-0` and execute `bao operator unseal -migrate` so subsequent GKE node upgrades unseal `openbao-0` unattended via Workload Identity.
7. Within the same Gate 4 window once DNS flips and `smoke-auth-chain.sh` passes publicly, shrink/recreate `vps-k3s` into OCI Kafka Broker VM 1 and retire `clusters/vps-k3s/`.
