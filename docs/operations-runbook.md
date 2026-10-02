# Platform Operations Runbook

**Feature**: `001-project-platform`  
**System**: Personal Project Platform  
**Target Environments**: `local-k3s`, `vps-k3s`, `eks`, `gke`  
**Governing Documents**: `specs/001-project-platform/spec.md`, `specs/001-project-platform/plan.md`

---

## 1. Overview & Operational Principles

The Personal Project Platform hosts independent personal projects alongside shared infrastructure on a single host or cluster. Operations adhere to six foundational principles:
1. **Guaranteed QoS**: CPU and Memory resource `requests` strictly equal `limits`.
2. **Default-Deny Sandboxing**: All project namespaces enforce Kubernetes Restricted Pod Security Standard and default-deny ingress/egress NetworkPolicies.
3. **Fail-Closed ForwardAuth**: The shared Go Authentication Gateway enforces online session validity and fails closed (HTTP 503) if identity dependencies are unreachable.
4. **Deterministic Capacity**: Project onboarding is gated by automated `preflight-budget.sh` preventing over-allocation of node memory and CPU.
5. **Declarative State Tracking**: Production deployments track active Git commit SHAs in `.deploy-baseline` with automated rollback on failure.
6. **Isolated Persistence**: Relational databases use dedicated catalogs and restricted credentials with automated restore verification.

---

## 2. Manifest Deployment Sequence

To deploy the platform cleanly from scratch or apply changes, follow this strict sequential order:

```bash
# Ensure current working directory is repo root
cd /path/to/nacfson_pipeline

# Step 0: Preflight Capacity Budget Check
./scripts/preflight-budget.sh --environment vps-k3s --projects 1

# Step 1: Base Platform Governance & Namespaces
kubectl apply -f deploy/platform/governance/namespaces.yaml
kubectl apply -f deploy/platform/governance/service-account-template.yaml
kubectl apply -f deploy/platform/governance/base-network-policy.yaml

# Step 2: Database Infrastructure
kubectl apply -f deploy/platform/database/postgres-pvc.yaml
kubectl apply -f deploy/platform/database/postgres-service.yaml
kubectl apply -f deploy/platform/database/postgres-statefulset.yaml
kubectl wait --for=condition=ready pod/postgres-0 -n identity --timeout=120s
kubectl apply -f deploy/platform/database/postgres-init-job.yaml

# Step 3: Identity & Authentication Gateway
kubectl apply -f deploy/platform/identity/keycloak-realm-config.yaml
kubectl apply -f deploy/platform/identity/keycloak-deployment.yaml
kubectl wait --for=condition=available deployment/keycloak -n identity --timeout=180s
kubectl apply -f deploy/platform/identity/gateway-deployment.yaml
kubectl wait --for=condition=available deployment/auth-gateway -n identity --timeout=60s

# Step 4: Ingress & Security Middlewares
kubectl apply -f deploy/platform/ingress/traefik-middleware.yaml
kubectl apply -f deploy/platform/ingress/ingress-allowlist.yaml

# Step 5: Project PN Workloads
kubectl apply -f deploy/projects/pn/namespace.yaml
kubectl apply -f deploy/projects/pn/service-account.yaml
kubectl apply -f deploy/projects/pn/resource-quota.yaml
kubectl apply -f deploy/projects/pn/network-policy.yaml
kubectl apply -f deploy/projects/pn/database-secret.yaml
kubectl apply -f deploy/projects/pn/image-pull-secret.yaml
kubectl apply -f deploy/projects/pn/backend-service.yaml
kubectl apply -f deploy/projects/pn/backend-deployment.yaml
kubectl apply -f deploy/projects/pn/frontend-deployment.yaml
kubectl apply -f deploy/projects/pn/ingress-route.yaml

# Step 6: Disaster Recovery Scheduled Backup (Optional / Production)
kubectl apply -f deploy/platform/database/backup-cronjob.yaml
```

Alternatively, use Kustomize overlays:
```bash
# For local K3s:
kubectl apply -k deploy/environments/local-k3s/

# For single VPS production:
kubectl apply -k deploy/environments/vps-k3s/
```

Or deploy via the automated release script:
```bash
./scripts/apply-release.sh deploy/environments/vps-k3s/
```

---

## 3. Secret Provisioning and Rotation Procedures

All secrets are managed via Kubernetes Secrets. Never commit plaintext secrets into Git.

### 3.1. Gateway HMAC Secret (`GATEWAY_HMAC_SECRET`)
- **Purpose**: Signs OIDC state parameters and `PLATFORM_SESSION` cookies.
- **Rotation Frequency**: 90 days or upon suspected compromise.
- **Procedure**:
  1. Generate a new cryptographically secure 32+ byte key:
     ```bash
     NEW_SECRET=$(openssl rand -base64 32)
     ```
  2. Update Secret in `identity` namespace:
     ```bash
     kubectl create secret generic auth-gateway-secrets -n identity \
       --from-literal=GATEWAY_HMAC_SECRET="$NEW_SECRET" \
       --dry-run=client -o yaml | kubectl apply -f -
     ```
  3. Restart gateway pods to pick up the new secret:
     ```bash
     kubectl rollout restart deployment/auth-gateway -n identity
     ```
  *Note: Rotating the HMAC secret will require active users to re-authenticate on their next request.*

### 3.2. Keycloak Google OAuth2 Broker Secrets
- **Rotation**:
  1. In the Google Cloud Console, generate a new OAuth2 client secret for the platform web application.
  2. Update `deploy/platform/identity/keycloak-realm-config.yaml` or inject via Secret environment variables in `deploy/platform/identity/keycloak-deployment.yaml`.
  3. Rollout restart Keycloak:
     ```bash
     kubectl rollout restart deployment/keycloak -n identity
     ```
  4. Delete the expired secret from Google Cloud Console.

### 3.3. Project Database Credentials
- **Procedure**:
  1. Generate new password: `NEW_PW=$(openssl rand -base64 24)`.
  2. Update password in PostgreSQL:
     ```bash
     kubectl exec -n identity statefulset/postgres -- psql -U postgres -c \
       "ALTER USER user_pn WITH PASSWORD '$NEW_PW';"
     ```
  3. Update `deploy/projects/pn/database-secret.yaml`:
     ```bash
     kubectl create secret generic pn-database-credentials -n proj-pn \
       --from-literal=DB_PASSWORD="$NEW_PW" \
       --dry-run=client -o yaml | kubectl apply -f -
     ```
  4. Restart project backend deployment:
     ```bash
     kubectl rollout restart deployment/pn-backend -n proj-pn
     ```

### 3.4. GHCR Image Pull Secret (`ghcr-creds`)
- **Procedure**:
  1. Create a GitHub Personal Access Token (PAT) with `read:packages` scope.
  2. Update the secret:
     ```bash
     kubectl create secret docker-registry ghcr-creds -n proj-pn \
       --docker-server=ghcr.io \
       --docker-username=nacfson \
       --docker-password="$NEW_GHCR_PAT" \
       --dry-run=client -o yaml | kubectl apply -f -
     ```

---

## 4. Rollback and Disaster Recovery Procedures

### 4.1. Automated Release Rollback
If a deployment fails or causes service disruption, trigger an atomic rollback using `scripts/rollback-release.sh`:

```bash
# Reads previous stable Git commit SHA from .deploy-baseline and restores manifests
./scripts/rollback-release.sh
```

### 4.2. Manual Disaster Recovery: Database Restore
In case of catastrophic disk failure or node rescheduling across physical hardware boundaries:

1. Locate the latest verified backup archive from `/backups/pg_dumpall_<TIMESTAMP>.sql.gz`.
2. Provision a new PostgreSQL StatefulSet instance with fresh PVC.
3. Execute the verified restore command:
   ```bash
   gunzip -c /backups/pg_dumpall_LATEST.sql.gz | kubectl exec -i -n identity statefulset/postgres -- psql -U postgres -d postgres
   ```
4. Run the restore verification script to validate integrity:
   ```bash
   ./scripts/verify-backup.sh
   ```

---

## 5. Ongoing Monitoring and Health Verification

Run the master verification suite at any time to audit platform health, security constraints, and compliance:

```bash
./scripts/verify-platform.sh
```

All 6 scenarios must exit with `PASSED` status.
