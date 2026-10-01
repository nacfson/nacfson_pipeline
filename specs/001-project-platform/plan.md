# Implementation Plan: Personal Project Platform

**Branch**: `001-project-platform` | **Date**: 2026-10-01 | **Spec**: [spec.md](file:///home/nacfson/Projects/nacfson_pipeline/specs/001-project-platform/spec.md)

**Input**: Feature specification from `specs/001-project-platform/spec.md` derived from `SPEC.md` and Ratified Constitution v1.2.0.

---

## Summary

The Personal Project Platform provides a lightweight, secure, and portable Kubernetes operating environment designed for co-locating personal projects on modest hardware (initially a single Oracle Cloud Always Free VPS running native K3s). It implements:
1. **Centralized Google-Backed Single Sign-On**: Traefik `ForwardAuth` integration powered by a custom Go authentication gateway built strictly using the Go standard library (zero runtime third-party dependencies), interacting with Keycloak to provide seamless cross-project SSO and synchronous online session revocation.
2. **Strict Workload Sandboxing**: Kubernetes `Restricted` Pod Security profile in `enforce` mode, platform-owned ServiceAccounts with disabled API token mounting, ClusterIP-only Services, and default-deny ingress/egress NetworkPolicies with explicit allowlists.
3. **Deterministic 1/n Resource Allocation**: Hardware-measured allocatable capacity minus platform overhead divided equally across active projects, enforcing strict `requests == limits` (Guaranteed QoS) and peak concurrency budgeting backed by namespace ResourceQuotas.
4. **Decoupled Shared Persistence**: Single standard PostgreSQL `StatefulSet` with disk-backed PVC storage, enforcing per-project database and role isolation without complex operator CRDs.
5. **Phased GitOps Delivery**: Declarative Git-versioned manifests verified by operator preflight checks and applied in sequential order, with pinned Git commit SHA baseline tracking and atomic rollback on partial deployment failure.

---

## Technical Context

**Language/Version**: Go 1.22+ (Authentication Gateway, stdlib only), POSIX Shell/Bash (automation & preflight scripts), YAML (Kubernetes manifests, Kustomize overlays, Helm values), SQL (PostgreSQL 16 DDL/DCL).

**Primary Dependencies**: Kubernetes 1.28+ (native K3s), Traefik v2/v3 (built-in K3s ingress controller), Keycloak 24+ (OIDC provider & Google identity broker), PostgreSQL 16 (standard Kubernetes StatefulSet).

**Storage**: PersistentVolumeClaim backed by node disk storage (`local-path` provisioner on K3s; environment-configurable for cloud CSI block storage).

**Testing**: Go standard testing suite (`go test`) for gateway logic; BATS / Bash automated preflight and security verification test scripts; end-to-end `curl` and `kubectl` integration validation.

**Target Platform**: Linux Server (Ubuntu/Debian/Oracle Linux on Oracle Cloud Always Free A1 ARM64 and AMD micro instances; local Linux native K3s; portable to AWS EKS and GCP GKE).

**Project Type**: Kubernetes Infrastructure-as-Code Platform & Go HTTP Microservice.

**Performance Goals**:
- Google first-time sign-in flow < 5 seconds (`SC-001`).
- Cross-project SSO recognized across 100% of protected applications without repeated challenges (`SC-002`).
- Synchronous session revocation effective on 100% of immediate next requests (`SC-003`).
- Gateway authentication check latency p95 < 50ms on local cluster network.

**Constraints**:
- Constrained single VPS memory envelope (~12 GB RAM, 2 OCPU on A1).
- Strict `requests == limits` for all project containers; unbounded borrowing and overcommit prohibited.
- Gateway must contain zero runtime third-party Go modules.
- Enforced Restricted Pod Security Standard across all project namespaces.
- Ingress strictly blocks internal Keycloak admin consoles, metrics (port 9000), and health endpoints.
- Multi-architecture OCI images (Linux AMD64 and ARM64) referenced by immutable SHA256 digests from GHCR.

**Scale/Scope**: 1 to $n$ co-located personal projects (initially Project PN backend and frontend); 1 shared PostgreSQL instance; 1 Keycloak instance; 1 Traefik controller; 1 Go ForwardAuth gateway.

---

## Constitution Check

*GATE: All gates evaluated against Constitution v1.2.0. Status: PASS.*

| Principle / Rule | Compliance Analysis | Status |
|------------------|---------------------|:------:|
| **I. Declarative GitOps (Phased)** | Manifests versioned in Git; immutable SHA256 digests; manual operator sequencing in Phase 1 with Git baseline SHA tracking and rollback; automated FluxCD deferred to Phase 2. | **PASS** |
| **II. Strict Workload Isolation** | Project namespaces enforce Restricted PSA (`pod-security.kubernetes.io/enforce: restricted`); non-root execution; drop ALL capabilities; token mounting disabled; ClusterIP only; default-deny NetworkPolicies. | **PASS** |
| **III. Defense-in-Depth Identity** | Go gateway built strictly with standard library; Traefik ForwardAuth; fail-closed session authority; online session revocation; public ingress blocks Keycloak `/admin/`, master realm, metrics port 9000. | **PASS** |
| **IV. Bounded 1/n Budgeting** | Explicit CPU/memory requests and limits (`requests == limits`); deterministic 1/n model based on measured allocatable capacity; peak concurrency accounting; ResourceQuota backstops. | **PASS** |
| **V. Isolated Persistence & Secrets** | Standard PostgreSQL StatefulSet with disk PVC; dedicated per-project databases and roles; zero plaintext secrets in Git; operator-provisioned Kubernetes Secrets by name. | **PASS** |
| **VI. Multi-Env Portability** | Deployable across local K3s, VPS K3s, EKS, and GKE without changing application code; multi-arch (AMD64/ARM64) image packaging; optional daily backups with restore verification. | **PASS** |

---

## Project Structure

### Documentation (this feature)

```text
specs/001-project-platform/
├── plan.md              # This implementation plan
├── research.md          # Phase 0 architectural research & decisions
├── data-model.md        # Phase 1 entities, schemas, and state transitions
├── quickstart.md        # Phase 1 runnable validation & testing guide
├── contracts/           # Phase 1 interface contracts
│   ├── gateway-forwardauth.md      # Traefik ForwardAuth HTTP interface
│   ├── session-revocation.md       # Synchronous logout API contract
│   ├── project-manifest-spec.yaml  # Declarative project onboarding schema
│   └── preflight-budget.json       # Preflight capacity calculation schema
├── checklists/
│   └── requirements.md  # Specification quality checklist (16/16 pass)
└── tasks.md             # Phase 2 task breakdown (created by /speckit-tasks)
```

### Source Code (repository root)

```text
deploy/
├── platform/
│   ├── ingress/
│   │   ├── traefik-config.yaml          # Traefik middleware & ForwardAuth IngressRoute
│   │   └── ingress-allowlist.yaml       # Public ingress routing rules (blocking /admin/)
│   ├── identity/
│   │   ├── keycloak-deployment.yaml     # Keycloak deployment & Service
│   │   ├── keycloak-realm-config.yaml   # Realm setup, Google broker, client scopes
│   │   └── gateway-deployment.yaml      # Custom Go ForwardAuth gateway deployment
│   ├── database/
│   │   ├── postgres-statefulset.yaml    # Standard PostgreSQL StatefulSet & Service
│   │   ├── postgres-pvc.yaml            # PersistentVolumeClaim for database storage
│   │   └── postgres-init-job.yaml       # Idempotent DB & role provisioning script
│   └── governance/
│       ├── restricted-psa.yaml          # Namespace PSA labels & admission constraints
│       └── base-network-policy.yaml     # Default-deny ingress/egress policies
├── environments/
│   ├── local-k3s/
│   │   └── kustomization.yaml           # Local K3s overrides
│   └── vps-k3s/
│       └── kustomization.yaml           # Single VPS production overrides
└── projects/
    └── pn/
        ├── namespace.yaml               # proj-pn namespace with Restricted PSA
        ├── resource-quota.yaml          # 1/n CPU and memory ResourceQuota (requests==limits)
        ├── network-policy.yaml          # Egress allowlists (DNS, PostgreSQL, external APIs)
        ├── service-account.yaml         # Dedicated SA with automountToken: false
        ├── backend-deployment.yaml      # Project PN backend (immutable digest, port 8080)
        ├── frontend-deployment.yaml     # Project PN frontend (immutable digest, port 80)
        └── ingress-route.yaml           # Traefik IngressRoute with ForwardAuth middleware

gateway/                                 # Custom Go Authentication Gateway (Stdlib Only)
├── cmd/
│   └── gateway/
│       └── main.go                      # HTTP server, routing, startup
├── internal/
│   ├── auth/
│   │   ├── handler.go                   # /auth ForwardAuth handler, header stripping
│   │   └── logout.go                    # /auth/logout CSRF-protected revocation handler
│   ├── keycloak/
│   │   ├── client.go                    # HTTP client for Keycloak session termination
│   │   └── token.go                     # Token verification and audience mapping
│   └── config/
│       └── config.go                    # Environment variable configuration
├── go.mod                               # Go module declaration (zero external runtime modules)
├── Dockerfile                           # Multi-stage scratch/distroless build (AMD64/ARM64)
└── tests/
    ├── handler_test.go                  # Unit tests for /auth and /auth/logout
    └── keycloak_test.go                 # Mock tests for online session validation

scripts/
├── preflight-budget.sh                  # Capacity discovery & 1/n budget calculation
├── apply-release.sh                     # Ordered sequential manifest deployment
├── rollback-release.sh                  # Atomic rollback to last healthy Git commit SHA
└── verify-platform.sh                   # End-to-end automated platform verification
```

**Structure Decision**: Selected a modular infrastructure layout separating shared platform services (`deploy/platform/`), environment overlays (`deploy/environments/`), project tenant configurations (`deploy/projects/`), the standalone Go authentication gateway microservice (`gateway/`), and operational automation tooling (`scripts/`).

---

## Complexity Tracking

> *Constitution Check completed with zero violations. No special exceptions required.*

| Area | Decision | Compliance Justification |
|------|----------|--------------------------|
| Database Architecture | Standard PostgreSQL StatefulSet | Replaced CNPG operator to eliminate CRD bloat and conserve 250MB RAM. |
| Ingress & Auth | Go stdlib gateway + Traefik ForwardAuth | Zero runtime third-party Go modules; enforces fail-closed session check. |
| Resource Allocation | Strict `requests == limits` | Guaranteed QoS class eliminates single-node noisy-neighbor OOM kills. |
| Deployment Workflow | Phased GitOps | Manual ordered deployment with Git SHA baseline saves RAM while preserving Git as truth. |
