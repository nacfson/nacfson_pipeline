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

### 2.3 Mandatory Restore & KMS Seal-Migration Sequence (`restore --target gke`)

On a fresh GKE cluster `postgres-0` and Keycloak do not exist until the `vault` layer is Ready, because `database` depends on `vault` and `identity` on `database`. The restore therefore finishes OpenBao (including the seal migration) before it lets Flux start the database, and holds Keycloak back until the databases are loaded:

1. Verify `manifest.sha256` across all 6 artifacts (`openbao.snap`, `keycloak.dump.gz`, `proj_pn.dump.gz`, `proj_health.dump.gz`, `edge-tls-secrets.yaml`, `victoria-logs-data.tar.gz`); abort immediately on any mismatch. `--target gke` always migrates to Cloud KMS; `--no-migrate-kms` is rejected because Flux applies the `gcpckms` config from Git.
2. Read the live `ConfigMap/openbao-config` that Flux applied from Git (it MUST contain `seal "gcpckms"`). The temporary Shamir config is that same file with only the `seal` stanza removed, so the storage path (`/var/lib/openbao/data`, the mounted disk) and addresses never differ from Git.
3. Pre-seed `edge-tls-secrets.yaml`, then suspend the Flux `vault` and `identity` Kustomizations (break-glass impersonation).
4. Apply the Shamir config, restart `openbao-0`, initialize if empty, restore `openbao.snap`, and unseal with the snapshot's unseal key.
5. Re-apply the live `gcpckms` config, restart `openbao-0`, run `bao operator unseal -migrate`, and confirm the node is on Cloud KMS: `recovery_seal=true`, `sealed=false`, `migration=false`. OpenBao 2.0 reports `type=shamir` after the migration, because `type` names the recovery seal once an auto-unseal seal is active, so `type` is not the check. On success the script records the bundle's `manifest.sha256` digest in the `vault/cutover-restore-state` ConfigMap. On failure the script stops with both layers still suspended for break-glass recovery.
   - **Rerun.** If `openbao-0` already runs unsealed on Cloud KMS, a rerun skips steps 3 and 4 and continues from the Kubernetes auth re-bind. It refuses to start when `cutover-restore-state` names a different bundle, so data from an older bundle is never kept silently.
6. Run `scripts/vault-configure-k8s-auth.sh` + `scripts/vault-reconcile.sh --confirm` so OpenBao trusts GKE's API server CA and token reviewer JWT.
7. Resume `vault` and wait until the `vault` and `database` Kustomizations are Ready (`database` gates on `StatefulSet/postgres` and `Job/postgres-init-job`) and `postgres-0` is rolled out.
8. Restore `keycloak.dump.gz`, `proj_pn.dump.gz`, and `proj_health.dump.gz` into `postgres-0`.
9. Resume `identity`, wait for it to be Ready, and confirm `Deployment/keycloak` is rolled out on the restored data.
10. Run `./scripts/smoke-auth-chain.sh` with `curl --resolve <host>:443:<ingress_ip>` before flipping DNS `A` records.
11. Within the same Gate 4 window once DNS flips and `smoke-auth-chain.sh` passes publicly, shrink/recreate `vps-k3s` into OCI Kafka Broker VM 1 and retire `clusters/vps-k3s/`.
