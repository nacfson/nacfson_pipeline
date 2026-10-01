# Phase 0 Research: Personal Project Platform Architecture

**Feature**: `001-project-platform`  
**Date**: 2026-10-01  
**Status**: Completed  

This document consolidates architectural decisions, rationale, and evaluated alternatives for the Personal Project Platform, resolving all technical unknowns in alignment with the Ratified Constitution v1.2.0 and SPEC.md.

---

## 1. Traefik ForwardAuth Integration with Go Standard Library Gateway

### Decision
Implement the authentication gateway as a lightweight HTTP microservice written exclusively using the Go standard library (`net/http`, `crypto/tls`, `encoding/json`, `time`, etc.) with zero runtime third-party Go modules. The gateway integrates with Traefik via Traefik's native `ForwardAuth` middleware.

### Rationale
- **Minimal Resource Footprint**: Go standard library binaries require <15-25 MB RAM and negligible idle CPU, preserving tight memory on a single VPS (Oracle Cloud Always Free).
- **Zero Third-Party Dependency Drift**: Eliminates runtime CVE supply-chain exposure from OAuth2 Proxy or heavy framework dependencies (Redis, Gin, gorilla).
- **ForwardAuth Protocol Simplicity**: Traefik delegates incoming requests to `http://gateway.identity.svc:8080/auth`. If the gateway returns HTTP 200, Traefik forwards the request to the upstream project pod along with injected headers (`Authorization: Bearer <project_token>`, `X-User-Subject`, `X-User-Issuer`). If the gateway returns HTTP 401 or 302, Traefik halts the request and redirects the browser to the login flow.
- **Header Sanitization**: The gateway and Traefik middleware explicitly strip all inbound client-supplied `X-User-*` or forged `Authorization` headers prior to evaluating and proxying requests.

### Alternatives Considered
- **OAuth2 Proxy**: Rejected due to high memory footprint (often 60–120 MB RAM), dependency on external Redis for reliable session state, and lack of fine-grained multi-project audience token exchange without complex configuration.
- **Envoy External Auth (ext_authz)**: Rejected because Traefik is the native K3s ingress controller and deploying Envoy adds substantial architectural complexity.

---

## 2. Keycloak Session Validation & Revocation Flow (Fail-Closed)

### Decision
Keycloak acts as the OpenID Connect (OIDC) identity provider and federated Google identity broker. The Go gateway performs:
1. Authorization Code Flow with PKCE for user login via Google broker.
2. Synchronous online session validation against Keycloak's token introspection or session status endpoint on every protected request.
3. User-directed logout via a CSRF-protected `POST /auth/logout` endpoint that terminates the Keycloak user session via Keycloak's Admin/Session REST API using a dedicated platform session-revocation client credential.
4. Fail-closed error handling: if Keycloak is unreachable or encounters dependency failure, the gateway returns HTTP 503 Service Unavailable immediately.

### Rationale
- **Online Revocation Guarantee**: Token signature verification alone cannot detect a revoked session before token expiration. Online session checking guarantees immediate (next-request) rejection across all platform projects.
- **Single Session Isolation**: Terminating the specific browser SSO session in Keycloak preserves independent sessions active on other user devices.
- **Privilege Segregation**: The gateway uses a narrowly scoped Kubernetes Secret containing client credentials authorized only for session termination, completely segregated from Keycloak client provisioning or realm administration credentials.
- **Per-Project Audience**: Keycloak client scopes are configured so tokens forwarded to Project A bear audience `project-a`, preventing token re-use attacks against Project B.

### Alternatives Considered
- **Short-Lived JWTs (e.g., 60 seconds) without Online Session Checks**: Rejected because token revocation is delayed by the token lifetime, failing the explicit user requirement for immediate next-request session invalidation (FR-007, SC-003).
- **Redis Shared Session Store**: Rejected to avoid operating an additional stateful service on the VPS. Keycloak is already persistent and acts as the authoritative session provider.

---

## 3. Standard PostgreSQL StatefulSet & Disk Persistence (Option B)

### Decision
Deploy PostgreSQL as a standard Kubernetes `StatefulSet` (single replica) backed by persistent disk storage via a PersistentVolumeClaim (`local-path` storage provisioner on K3s, portable to cloud CSI block storage). Strict database and role isolation is enforced via an initialization script or Job:
- Keycloak receives its own database (`keycloak`) and restricted role (`keycloak_user`).
- Each project receives a dedicated database (`project_<id>`) and restricted role (`project_<id>_user`).
- Project credentials cannot connect to or query other databases or PostgreSQL administrative schemas.
- Backup is handled via a scheduled CronJob executing logical `pg_dump` dumps to an offsite host or secondary storage.

### Rationale
- **Simplicity and Portability**: Standard StatefulSet eliminates complex Kubernetes operators (e.g., CNPG, Zalando, Crunchy) that consume 150–300 MB RAM for operator pods, define dozens of custom resource definitions (CRDs), and complicate migration to managed cloud databases (AWS RDS, GCP Cloud SQL).
- **Sufficient Persistence Boundary**: Pod replacement (StatefulSet rolling updates) and in-place node OS upgrades cleanly reattach the persistent volume without data loss.
- **Clean Expansion Path**: The standard connection contract (PostgreSQL hostname, port, database, user, password Secret) allows seamless cutover to an external managed RDS/Cloud SQL instance in cloud profiles without touching application code.

### Alternatives Considered
- **CloudNativePG (CNPG)**: Explicitly evaluated and rejected per user request. While robust for multi-node failover, it adds unnecessary controller overhead and CRD coupling on a single VPS where cross-node failover is impossible.
- **Per-Project PostgreSQL Deployments**: Rejected because running multiple separate PostgreSQL instances would exhaust the VPS memory budget.

---

## 4. Deterministic 1/n Resource Allocation & Strict `requests == limits`

### Decision
Compute resources are partitioned according to a deterministic 1/n project model:
$$\text{Application Capacity} = \text{Node Allocatable Capacity} - \text{Shared Platform Overhead}$$
$$\text{Per-Project Budget} = \frac{\text{Application Capacity}}{n}$$
Where:
- $n$ is the active deployed project count.
- Platform overhead accounts for system daemons, K3s server/agent, Traefik, Keycloak, PostgreSQL, and Go Gateway.
- Container specifications must strictly enforce `requests == limits` for both CPU and memory (Option A selected in clarification).
- Aggregate peak concurrent footprint across all project containers (including rollout surge above steady-state replicas, Job parallelism, and scheduled CronJobs) must fit within the project's slice.
- Platform-owned namespace `ResourceQuota` enforces these boundaries as a non-bypassable admission backstop.

### Rationale
- **Noisy Neighbor Elimination**: On a resource-constrained single VPS (e.g. 12 GB RAM A1 or 1 GB AMD micro), memory overcommit leads to unpredictable OOM-killer invocations that can terminate critical platform services (Keycloak or database). Setting `requests == limits` assigns pods to the Kubernetes `Guaranteed` Quality of Service (QoS) class, which is the last to be evicted.
- **Predictable Preflight**: The exact peak memory footprint can be statically verified against the project slice before manifests are applied to the cluster.

### Alternatives Considered
- **Burstable QoS (`requests < limits`)**: Rejected during clarification (Question 1) because CPU/memory overcommit risks cascading host memory exhaustion on a single server.

---

## 5. Kubernetes Restricted Pod Security Standard & Admission Hardening

### Decision
Enforce the Kubernetes `Restricted` Pod Security Standard via Pod Security Admission (PSA) in `enforce` mode across all project namespaces (`pod-security.kubernetes.io/enforce: restricted`).
Platform admission controls additionally enforce:
1. Automatic and projected ServiceAccount token mounting is disabled (`automountServiceAccountToken: false`).
2. Pods execute under dedicated, platform-owned ServiceAccounts with zero Kubernetes API RBAC permissions.
3. Project Services must be `ClusterIP` only. `NodePort` and `LoadBalancer` services are rejected.
4. Container security context must require `runAsNonRoot: true`, drop `ALL` capabilities, and allow at most `NET_BIND_SERVICE`.
5. Projects cannot modify Namespace labels, ServiceAccounts, ResourceQuotas, or NetworkPolicies.

### Rationale
- **Perimeter Sandboxing**: Prevents compromised project containers from escalating privileges to the host kernel, mounting host filesystems (`hostPath`), or escaping container boundaries.
- **Control Plane Protection**: Disabling service account token mounting ensures workloads cannot discover or interact with the Kubernetes API server (`https://kubernetes.default.svc`).

### Alternatives Considered
- **Baseline Pod Security Profile**: Rejected because `Baseline` allows running as root, retaining capabilities, and mounting host paths, which violates Constitution Principle II.

---

## 6. NetworkPolicy Isolation (Default-Deny Ingress & Egress with Allowlists)

### Decision
Every project namespace carries a default-deny `NetworkPolicy` for both ingress and egress:
- **Ingress**: Deny all inbound traffic except HTTP traffic arriving from the Traefik ingress controller namespace.
- **Egress**: Deny all outbound traffic except:
  1. Internal cluster DNS (`kube-system/kube-dns` on port 53 UDP/TCP).
  2. Platform PostgreSQL service (`identity/postgres-service` on port 5432 TCP).
  3. Explicitly declared egress allowlists (by IP block or namespace selector) for external APIs declared in the project's onboarding manifest.

### Rationale
- **Least-Privilege Network Defense**: Selected in clarification (Question 2). Prevents compromised pods from port-scanning internal cluster services, reaching Keycloak administrative interfaces, or unauthorized external data exfiltration.

### Alternatives Considered
- **Allow All External Internet Egress**: Rejected during clarification (Question 2) because it leaves external data exfiltration unconstrained.

---

## 7. Phased GitOps: Manual Deployment Ordering, Release Baseline Recording, & Rollback

### Decision
Platform deployment operates under **Phase 1: Phased GitOps**:
- All manifests (platform core and project charts) are versioned in Git.
- An operator preflight verification script (`preflight.sh`) computes node allocatable capacity, verifies 1/n project budgets, validates Restricted PSA compliance, and confirms immutable SHA256 image references.
- Candidate releases are tagged in Git. The operator manually applies manifests in sequential order:
  1. Namespaces, ResourceQuotas, NetworkPolicies, ServiceAccounts.
  2. Persistent volumes, PostgreSQL StatefulSet, database init.
  3. Keycloak, Traefik ForwardAuth gateway.
  4. Project applications (PN backend, frontend).
- **Rollback on Partial Failure**: If any manifest fails to apply or fails health checks, the operator immediately reverts all applied resources to the previously recorded healthy Git baseline commit SHA (selected in clarification Question 3).
- **Phase 2 Expansion Path**: Automated in-cluster continuous GitOps reconciliation via FluxCD is deferred to Phase 2 to conserve ~250–350 MB RAM and simplify early debugging.

### Rationale
- **Resource Optimization**: Eliminating Flux controllers saves substantial memory on low-tier servers during the initial bootstrapping phase.
- **Deterministic State Recovery**: Pinned Git commit SHAs provide an unambiguous audit trail and deterministic rollback targets.

---

## 8. Multi-Architecture (AMD64 & ARM64) Image Delivery via GHCR Digests

### Decision
All application images are built and pushed to GitHub Container Registry (GHCR) as multi-architecture OCI image indexes supporting both `linux/amd64` and `linux/arm64`. Deployment manifests MUST reference images by their immutable SHA256 index digest (e.g. `image@sha256:...`) rather than mutable tags.
Workloads access private GHCR images using namespace-scoped Kubernetes `imagePullSecrets` provisioned by the operator.

### Rationale
- **Architectural Portability**: Allows deployment across local x86_64 Linux workstations, Oracle Cloud A1 ARM64 instances, AMD micro instances, and cloud managed clusters without rebuilding images.
- **Immutability & Integrity**: Pinned image digests prevent untracked upstream changes or cache poisoning from silently mutating running workloads.

---

## Summary of Decisions

| Architectural Area | Selected Decision | Verification Anchor |
|--------------------|-------------------|---------------------|
| Ingress & Auth Gateway | Traefik ForwardAuth + Go stdlib gateway (<25MB RAM) | `FR-004`, `FR-008`, `contracts/gateway-forwardauth.md` |
| Identity & Session Authority | Keycloak OIDC, Google Broker, fail-closed online validation | `FR-001`, `FR-003`, `FR-007`, `contracts/session-revocation.md` |
| Persistence Architecture | Standard PostgreSQL StatefulSet (disk PVC, per-project DB/roles) | `FR-015`, `SC-006`, `data-model.md` |
| Resource Budgeting | Strict `requests == limits`, deterministic 1/n slices, peak concurrency | `FR-011`, `FR-012`, `FR-014`, `contracts/preflight-budget.json` |
| Workload Sandboxing | Restricted PSA enforce, token-mount disabled, ClusterIP only | `FR-009`, `SC-005` |
| Network Defense | Default-deny Ingress and Egress, explicit allowlists | `FR-010`, `contracts/project-manifest-spec.yaml` |
| Deployment Reconciliation | Phased GitOps (operator applied, Git SHA baseline, atomic rollback) | `FR-013`, `SC-004`, `quickstart.md` |
| Image Packaging | Multi-arch OCI (AMD64/ARM64) via GHCR immutable SHA256 digests | `FR-017`, `SC-007` |
