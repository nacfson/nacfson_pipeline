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
