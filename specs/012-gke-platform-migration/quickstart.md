# Quickstart & 4-Gate Validation Guide: Spec 012 (GKE Platform Migration)

**Feature**: [`specs/012-gke-platform-migration/spec.md`](spec.md)
**Contracts**: [`gke-capacity-and-placement.md`](contracts/gke-capacity-and-placement.md) | [`portable-network-policy-and-gar.md`](contracts/portable-network-policy-and-gar.md) | [`fluentbit-recovery-and-cutover-bundle.md`](contracts/fluentbit-recovery-and-cutover-bundle.md)

> **Core Constraint**: Zero cloud VMs or GKE resources are provisioned or modified during Gates 1–3. Every offline and `local-k3s` check below MUST pass first.

---

## Gate 1 — Offline Render, Schema & Multi-Node Preflight Validation

Run from repository root:

```bash
# 1. Run preflight & hook unit tests (multi-node DaemonSet math, Design 1 pool fit, portable NetworkPolicy, hostPath rules)
python3 -m unittest discover tests/preflight
python3 -m unittest discover tests/hooks

# 2. Render & validate all three overlays in parallel (local-k3s, gke, and pre-retirement vps-k3s)
./scripts/render-validate.sh local-k3s
./scripts/render-validate.sh gke
./scripts/render-validate.sh vps-k3s

# 3. Verify Go stdlib builds (zero third-party dependencies after vault-registry-proxy retirement)
(cd gateway && go test -v ./... && go vet ./...)
(cd services/vault-proxies && go test -v ./... && go vet ./...)

# 4. Verify Canonical Architecture Handbook synchronization
python3 scripts/check-arch-doc-sync.py --worktree
```

**Expected Outcome**:
- `platform-preflight.py` reports `PASSED` for `gke` with `allocatable=(1880m, 4408Mi)`, `perProjectBudget=(303m, 418Mi)`, `ondemand-core` (`e2-medium`) hosting `keycloak` + all `proj-*` + `RWO` PVCs, and `spot-controllers` (`e2-small`) hosting `cert-manager` + `flux-system`.
- Zero `CiliumNetworkPolicy` or `vault-registry-proxy` resources exist in rendered outputs.

---

## Gate 2 — `local-k3s` Fluent Bit Crash & Sink-Outage Recovery Rehearsal

On the local Linux K3s rehearsal cluster (`clusters/local-k3s/`):

```bash
# 1. Reconcile updated observability layer (Solution B /var/lib/fluent-bit-state)
flux reconcile kustomization observability --with-source
kubectl -n observability rollout status daemonset/fluent-bit --timeout=120s

# 2. Simulate a 2-minute downstream sink outage by scaling victorialogs to 0
kubectl -n observability scale statefulset/victorialogs --replicas=0

# 3. Emit 200 numbered Contract v3 log lines from a test pod in proj-pn, then restart fluent-bit mid-outage
kubectl -n observability rollout restart daemonset/fluent-bit
kubectl -n observability rollout status daemonset/fluent-bit --timeout=120s

# 4. Restore downstream sink and verify 200/200 lines arrive with 0 gaps and 0 duplicates
kubectl -n observability scale statefulset/victorialogs --replicas=1
kubectl -n observability rollout status statefulset/victorialogs --timeout=120s
```

**Expected Outcome**: `/var/lib/fluent-bit-state/chunks` retains unacknowledged chunks across the pod restart and flushes all 200 records cleanly once the sink returns (`SC-002`).

---

## Gate 3 — Production Backup Extraction & Local Full-State Restore Drill

```bash
BUNDLE_DIR="$HOME/.nacfson-cutover/rehearsal-$(date -u +%Y%m%d)"

# 1. Export SHA-256-verified bundle from vps-k3s (read-only extraction, zero mutation on vps-k3s)
./scripts/cutover-bundle.sh export --bundle-dir "$BUNDLE_DIR" --ssh oracleCloud
./scripts/cutover-bundle.sh verify --bundle-dir "$BUNDLE_DIR"

# 2. Restore bundle onto clean local-k3s rehearsal cluster (OpenBao + K8s auth re-bind + keycloak + proj_*)
./scripts/cutover-bundle.sh restore --bundle-dir "$BUNDLE_DIR" --target local-k3s

# 3. Verify returning user (issuer, subject) continuity & auth chain
./scripts/smoke-auth-chain.sh --env local-k3s
```

**Expected Outcome**: `manifest.sha256` verifies cleanly, OpenBao re-binds `auth/kubernetes/config` to `local-k3s` automatically, and `smoke-auth-chain.sh` confirms preserved `(issuer, subject)` identities (`SC-003`).

---

## Gate 4 — Single-Shot GKE Cutover, `gcpckms` Migration & `vps-k3s` Repurposing

1. Lower DNS `A` record TTLs for `{auth,pn,health,blog}.nacfson.cloud` to `60s` at least 24h prior.
2. Provision Zonal GKE Standard cluster in GCP project `nacfson-pipeline` (`--project=nacfson-pipeline --zone=asia-northeast3-a --logging=NONE --monitoring=NONE --enable-dataplane-v2 --workload-pool=nacfson-pipeline.svc.id.goog`) with `ondemand-core` (`1 × e2-medium` + static IPv4 + firewall TCP `80,443`) and `spot-controllers` (`1 × e2-small --spot`).
   - **Before step 3 (open item from the PR 149 review):** run `kubectl get endpoints kubernetes -n default` and check the API server address. A private address (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`) is already allowed by `openbao-ingress-policy`. A public address is not: add it as an `ipBlock` `/32` on TCP `:443` to the `openbao-ingress-policy` patch in `clusters/gke/layers.yaml` first, otherwise OpenBao TokenReview is dropped and every `auth/kubernetes` login fails.
3. Bootstrap FluxCD pointing at `clusters/gke/`, pre-seed `edge-tls-secrets.yaml`, quiesce `vps-k3s` writes, run final `./scripts/cutover-bundle.sh export` + `restore --target gke --migrate-kms`.
4. Verify HTTPS SNI + login via `curl --resolve` and `./scripts/smoke-auth-chain.sh` (`SC-004`), then flip DNS `A` records to Node 1's static IPv4.
5. Migrate `openbao-0` seal from Shamir to GCP Cloud KMS (`project=nacfson-pipeline`, `region=asia-northeast3`, `bao operator unseal -migrate`) and verify unattended unseal across a `kubectl delete pod -n vault openbao-0` restart.
6. Within the same cutover window, shrink/recreate the former `vps-k3s` OCI VM into Kafka Broker VM 1 (`1 OCPU / 5 GB`) for Spec 013 and retire `clusters/vps-k3s/`.
