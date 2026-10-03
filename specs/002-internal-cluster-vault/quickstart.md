# Quickstart & Verification Guide: Internal Cluster Vault

**Feature**: `specs/002-internal-cluster-vault`  
**Target**: K3s Cluster on Oracle Cloud VPS / Local Linux  

---

## 1. Initial Bootstrap & Unseal

### Step 1: Deploy OpenBao Manifests
```bash
kubectl apply -k deploy/platform/vault/
```

### Step 2: Initialize OpenBao (Operator Only)
```bash
kubectl exec -n vault openbao-0 -- bao operator init -key-shares=1 -key-threshold=1 -format=json > /tmp/vault-init.json
```
* Extract and store the **Unseal Key** and **Initial Root Token** in your personal password manager.
* **Immediately delete** `/tmp/vault-init.json`.

### Step 3: Unseal OpenBao
```bash
kubectl exec -n vault openbao-0 -- bao operator unseal <UNSEAL_KEY>
```

---

## 2. Seed Initial Secrets & Enable Engines

### Step 1: Enable Secrets Engines
```bash
# Enable KV v2 for credentials
kubectl exec -n vault openbao-0 -- bao secrets enable -version=2 kv

# Enable Transit for Gateway signing
kubectl exec -n vault openbao-0 -- bao secrets enable transit
kubectl exec -n vault openbao-0 -- bao write -f transit/keys/gateway-session-key type=ed25519 exportable=false
```

### Step 2: Seed Staged GHCR Token
```bash
kubectl exec -n vault openbao-0 -- bao kv put kv/ghcr-pull-token \
  value="ghp_initial_token..." \
  state="verified_active" \
  owner="platform"
```

---

## 3. Verify Private GHCR Image Pull via Registry Proxy

### Step 1: Configure K3s Containerd Mirror
On the K3s host, ensure `/etc/rancher/k3s/registries.yaml` routes private images to the proxy:
```yaml
mirrors:
  "ghcr.io":
    endpoint:
      - "http://127.0.0.1:5000"
```
Restart K3s: `sudo systemctl restart k3s`.

### Step 2: Test Image Pull
```bash
sudo k3s crictl pull ghcr.io/nacfson/pn-backend@sha256:7f9a8b1...
```
* **Verify**: Image is successfully pulled.
* **Verify Confinement**: Inspect crictl config and node logs—no GitHub PAT is stored on node disk or memory.

---

## 4. Exercise Staged Rotation Drill ([User Story 3](file:///Users/hyungjuyu/Projects/Brain/nacfson_pipeline/specs/002-internal-cluster-vault/spec.md#L71-L86))

1. **Stage v2**:
   ```bash
   kubectl exec -n vault openbao-0 -- bao kv put kv/ghcr-pull-token \
     value="ghp_new_v2_token..." \
     state="staged"
   ```
2. **Run Adoption Check**:
   The registry proxy detects `v2`, probes `ghcr.io` with a `HEAD` request.
3. **Promote v2**:
   Upon probe success, proxy marks `v2` as `verified_active`.
4. **Invalidate v1**:
   Operator logs into GitHub Settings and deletes `v1`.

---

## 5. Exercise Disaster Recovery Drill ([User Story 4](file:///Users/hyungjuyu/Projects/Brain/nacfson_pipeline/specs/002-internal-cluster-vault/spec.md#L89-L105))

1. **Take Snapshot and Copy Offsite/Host**:
   ```bash
   # Save snapshot inside pod
   kubectl exec -n vault openbao-0 -- bao operator raft snapshot save /tmp/vault-backup.snap

   # Establish an independent recovery copy outside the pod/PVC
   kubectl cp vault/openbao-0:/tmp/vault-backup.snap ./vault-backup.snap
   ```
2. **Simulate Storage Wipeout**:
   ```bash
   kubectl delete statefulset -n vault openbao
   kubectl delete pvc -n vault data-openbao-0
   ```
3. **Re-deploy Fresh Instance & Copy Snapshot In**:
   ```bash
   kubectl apply -k deploy/platform/vault/
   # Wait for openbao-0 to be running in uninitialized state
   kubectl cp ./vault-backup.snap vault/openbao-0:/tmp/vault-backup.snap
   ```
4. **Restore Snapshot**:
   ```bash
   kubectl exec -n vault openbao-0 -i -- bao operator raft snapshot restore -force /tmp/vault-backup.snap
   ```
5. **Unseal and Reconcile**:
   Unseal with master key; verify all credential versions and access grants survive intact, then unpause operational execution.
