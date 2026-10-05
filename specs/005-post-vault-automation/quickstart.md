# Quickstart & Verification Guide: Automated Post-Vault Orchestration

**Feature**: `005-post-vault-automation`  
**Target Cluster**: Oracle Cloud VPS / Local K3s  

---

## 1. Prerequisites

1. OpenBao Vault pod `vault/openbao-0` is unsealed and `1/1 Running`.
2. Core 11 credentials are seeded into Vault KV v2 (`kv/platform/*`, `kv/database/*`, `kv/gateway/*`).
3. Vault Kubernetes Auth is enabled:
   ```bash
   kubectl exec -n vault openbao-0 -- env VAULT_ADDR="http://127.0.0.1:8200" VAULT_TOKEN="<ROOT_TOKEN>" \
     bao auth enable kubernetes
   ```

---

## 2. Validation Scenarios

### Scenario 1: Automated Workload Credential Ingestion
1. Verify `postgres-0` initContainer completes and main container starts:
   ```bash
   kubectl get pod -n identity postgres-0
   # Expected: 1/1 Running
   ```
2. Verify zero secrets leaked in logs or environment:
   ```bash
   kubectl exec -n identity postgres-0 -- env | grep -i password
   # Expected: POSTGRES_PASSWORD_FILE=/var/run/secrets/database/admin_password (No plaintext value)
   ```
3. Verify database initialization job completes:
   ```bash
   kubectl get job -n identity postgres-init-job
   # Expected: 1/1 Completed
   ```

### Scenario 2: Container Image Build & Cluster Ingestion
1. Verify GitHub Actions builds multi-arch images:
   ```bash
   gh workflow view build-images.yaml
   ```
2. Verify proxy pods pull images and reach healthy state:
   ```bash
   kubectl get pods -n vault -l app.kubernetes.io/name=vault-db-proxy
   kubectl get pods -n vault -l app.kubernetes.io/name=vault-registry-proxy
   # Expected: 1/1 Running (Zero ErrImagePull)
   ```

### Scenario 3: Full Platform Convergence
1. Inspect all 10 platform kustomizations:
   ```bash
   kubectl get kustomizations -A
   # Expected: 10/10 Ready: True
   ```

### Scenario 4: Automated Snapshot Execution & Retention
1. Trigger daily snapshot test job:
   ```bash
   kubectl create job --from=cronjob/openbao-daily-snapshot -n vault test-snapshot
   kubectl wait --for=condition=complete job/test-snapshot -n vault --timeout=60s
   ```
2. Confirm backup file created and verified:
   ```bash
   kubectl exec -n vault openbao-0 -- ls -la /backups/
   ```

---

## 3. Live Cluster Verification Results (Oracle Cloud VPS)

| Component | Target State | Live Cluster Status |
| :--- | :--- | :--- |
| `vault/openbao-0` | `1/1 Running` | **`1/1 Running`** (Initialized & Unsealed) |
| `vault/vault-db-proxy` | `1/1 Running` | **`1/1 Running`** (GHCR Multi-Arch Image) |
| `vault/vault-registry-proxy` | `1/1 Running` | **`1/1 Running`** (GHCR Multi-Arch Image) |
| `identity/postgres-0` | `1/1 Running` | **`1/1 Running`** (Vault Secret Injection `0400`) |
| `identity/postgres-init-job` | `Completed` | **`1/1 Completed`** (Catalogs & Roles Initialized) |
| `identity/keycloak` | `1/1 Running` | **`1/1 Running`** (Ephemeral Secret Injection & Port 8080 Probes) |
| `identity/gateway` | `1/1 Running` | **`1/1 Running`** (GHCR Multi-Arch Image & Ephemeral HMAC Secrets) |
| `flux-system/database` | `Ready: True` | **`Ready: True`** |
| `flux-system/identity` | `Ready: True` | **`Ready: True`** |
| `flux-system/ingress` | `Ready: True` | **`Ready: True`** |
| `flux-system/vault-proxies` | `Ready: True` | **`Ready: True`** |
| `verify-platform.sh` | 8/8 Suites Passed | **`100% Passed`** (All 8 scenarios green) |
