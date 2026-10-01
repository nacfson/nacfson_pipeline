# Quickstart & Verification Guide: Personal Project Platform

**Feature**: `001-project-platform`  
**Date**: 2026-10-01  
**Status**: Completed  

This guide provides step-by-step instructions and runnable test scenarios to validate the complete platform architecture, from capacity preflight and infrastructure bootstrapping to workload isolation, authentication, session revocation, and database persistence.

---

## Prerequisites

Before executing validation scenarios, verify that the following tools are available on the deployment host:
- **Kubernetes cluster**: Native K3s running on Linux (local or single VPS).
- **CLI Tools**: `kubectl` (v1.28+), `helm` (v3.12+), `curl`, `jq`, `openssl`.
- **Git**: Working copy of `nacfson_pipeline` on branch `001-project-platform`.

---

## Scenario 1: Capacity Discovery & Deterministic 1/n Budget Verification

Validate that actual hardware capacity is discovered, platform overhead is subtracted, and 1/n project allocations are computed accurately without division-by-zero errors.

```bash
# 1. Inspect actual node allocatable capacity
NODE_NAME=$(kubectl get nodes -o jsonpath='{.items[0].metadata.name}')
NODE_CPU=$(kubectl get node "$NODE_NAME" -o jsonpath='{.status.allocatable.cpu}')
NODE_MEM=$(kubectl get node "$NODE_NAME" -o jsonpath='{.status.allocatable.memory}')
echo "Observed Node Allocatable: CPU=${NODE_CPU}, Memory=${NODE_MEM}"

# 2. Run preflight budget validation (mocking 1 project on observed node)
# Input format defined in specs/001-project-platform/contracts/preflight-budget.json
./scripts/preflight-budget.sh --environment vps-k3s --projects 1
```

**Expected Outcome**:
- Script discovers node allocatable resources directly from the Kubernetes API.
- Rejects candidate configurations if peak memory exceeds calculated slice or if requests != limits.
- Exits cleanly with status `PASSED` when peak footprint fits inside the slice.
- Testing with `--projects 0` succeeds without division-by-zero errors (`SC-004`).

---

## Scenario 2: Workload Security & Restricted PSA Verification

Verify that project namespaces strictly enforce the Kubernetes Restricted Pod Security profile, reject root containers, block API token mounting, and prohibit non-ClusterIP services.

```bash
# 1. Verify Restricted Pod Security Admission label on project namespace
kubectl get namespace proj-pn -o jsonpath='{.metadata.labels.pod-security\.kubernetes\.io/enforce}'
# Must output: restricted

# 2. Attempt to deploy a pod requesting root execution (MUST FAIL)
kubectl apply -f - <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: test-root-violation
  namespace: proj-pn
spec:
  containers:
  - name: root-box
    image: busybox
    command: ["sleep", "3600"]
    securityContext:
      runAsUser: 0
EOF
# Expected: Error from server (Forbidden): violates PodSecurity "restricted:latest"

# 3. Verify ServiceAccount token automounting is disabled
kubectl get sa project-sa -n proj-pn -o jsonpath='{.automountServiceAccountToken}'
# Must output: false

# 4. Attempt to create a NodePort service in project namespace (MUST FAIL)
kubectl apply -f - <<EOF
apiVersion: v1
kind: Service
metadata:
  name: test-bypass-service
  namespace: proj-pn
spec:
  type: NodePort
  ports:
  - port: 80
  selector:
    app: test
EOF
# Expected: Rejected by admission policy (ClusterIP only permitted)
```

**Expected Outcome**:
- 100% of non-compliant pod/service attempts fail closed (`SC-005`).

---

## Scenario 3: NetworkPolicy Default-Deny & Egress Isolation

Verify that project namespaces isolate workloads with default-deny ingress and egress, allowing only internal DNS, PostgreSQL, and explicit external API allowlists.

```bash
# 1. From inside a project pod, verify internal DNS resolves
kubectl exec -n proj-pn deploy/pn-backend -- getent hosts postgres-service.identity.svc

# 2. Verify PostgreSQL port 5432 is reachable
kubectl exec -n proj-pn deploy/pn-backend -- nc -z -v -w 3 postgres-service.identity.svc 5432

# 3. Attempt an unauthorized outbound connection to undeclared internet (MUST TIMEOUT/FAIL)
kubectl exec -n proj-pn deploy/pn-backend -- nc -z -v -w 3 1.1.1.1 53
# Expected: Connection timed out / Network is unreachable
```

**Expected Outcome**:
- Traffic to other namespaces or undeclared internet endpoints is dropped immediately by NetworkPolicy (`FR-010`).

---

## Scenario 4: Shared Authentication Gateway & Google SSO Verification

Verify that unauthenticated requests are redirected, registered users receive ordinary landing-page access, and client-supplied identity headers are stripped.

```bash
# 1. Attempt unauthenticated request to protected project (MUST REDIRECT)
curl -i -k https://pn.example.com/
# Expected: HTTP/1.1 302 Found, Location: https://auth.example.com/realms/platform/protocol/openid-connect/auth...

# 2. Attempt request with forged identity headers (MUST BE STRIPPED)
curl -i -k -H "X-User-Subject: malicious-admin" https://pn.example.com/
# Expected: Gateway strips forged header, denies unauthenticated request

# 3. Authenticate via Google, obtain session cookie, and access Project A
curl -i -k -b "PLATFORM_SESSION=${VALID_COOKIE}" https://pn.example.com/
# Expected: HTTP/1.1 200 OK (Served landing page, ordinary user capabilities only)

# 4. Access Project B with same session cookie (Cross-Project SSO)
curl -i -k -b "PLATFORM_SESSION=${VALID_COOKIE}" https://project-b.example.com/
# Expected: HTTP/1.1 200 OK (Recognized identical issuer/subject without secondary Google login)
```

**Expected Outcome**:
- Sub-5 second first sign-in (`SC-001`).
- 100% seamless cross-project SSO recognition (`SC-002`).

---

## Scenario 5: Synchronous Online Session Revocation Verification

Verify that invoking logout terminates the active Keycloak session and immediately invalidates subsequent requests across all projects.

```bash
# 1. Submit CSRF-protected logout request
curl -i -k -X POST https://auth.example.com/auth/logout \
  -b "PLATFORM_SESSION=${VALID_COOKIE}" \
  -H "X-CSRF-Token: ${CSRF_TOKEN}" \
  -H "Origin: https://pn.example.com"

# 2. Immediately attempt to access Project A with previously issued session cookie
curl -i -k -b "PLATFORM_SESSION=${VALID_COOKIE}" https://pn.example.com/
# Expected: HTTP/1.1 401 Unauthorized or 302 Found (Access Denied immediately)

# 3. Immediately attempt to access Project B with same cookie
curl -i -k -b "PLATFORM_SESSION=${VALID_COOKIE}" https://project-b.example.com/
# Expected: HTTP/1.1 401 Unauthorized or 302 Found (Access Denied immediately)
```

**Expected Outcome**:
- 100% of requests presenting revoked session rejected on immediate subsequent attempt (`SC-003`).

---

## Scenario 6: PostgreSQL Pod Replacement & Persistence Verification

Verify that project data survives StatefulSet pod deletion and rolling maintenance, and that cross-database access is prohibited.

```bash
# 1. Insert test record into project database
kubectl exec -n identity statefulset/postgres -- psql -U user_pn -d proj_pn -c \
  "INSERT INTO test_persistence (val) VALUES ('persisted_data');"

# 2. Delete the PostgreSQL pod to simulate maintenance or restart
kubectl delete pod postgres-0 -n identity

# 3. Wait for StatefulSet to recreate pod and become Ready
kubectl wait --for=condition=ready pod/postgres-0 -n identity --timeout=60s

# 4. Read back test record from recreated pod
kubectl exec -n identity statefulset/postgres -- psql -U user_pn -d proj_pn -c \
  "SELECT val FROM test_persistence;"
# Expected: Outputs 'persisted_data' intact

# 5. Attempt cross-database query into Keycloak DB using project credentials (MUST FAIL)
kubectl exec -n identity statefulset/postgres -- psql -U user_pn -d keycloak -c "\dt"
# Expected: FATAL: permission denied for database "keycloak"
```

**Expected Outcome**:
- Data survives pod recreation (`SC-006`).
- Cross-database access is strictly rejected (`FR-015`).
