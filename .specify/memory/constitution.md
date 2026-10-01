<!--
# Sync Impact Report
- Version change: 1.1.0 -> 1.2.0
- List of modified principles:
  - V. Isolated Shared Persistence & Zero Plaintext Secrets (Replaced CNPG operator dependency with standard decoupled PostgreSQL StatefulSet for maximum cloud portability)
- Added sections:
  - None
- Removed sections:
  - None
- Follow-up TODOs:
  - None
-->

# nacfson_pipeline Constitution

## Core Principles

### I. Declarative Configuration as Single Source of Truth (Phased GitOps)
All declarative configurations, workload definitions, and environment profiles MUST reside in Git. For the initial platform design, the operator manually reviews, orders, and applies manifests in sequential order, verifying capacity budgets and isolation compliance without requiring an in-cluster automated continuous reconciliation controller. Automated in-cluster GitOps reconciliation (FluxCD) and automated branch promotion gating (`platform-preflight`) are established as the target operating model for future platform expansion. Deployment references MUST use immutable image digests rather than mutable tags.

### II. Strict Workload Isolation & Restricted Pod Security
All project namespaces MUST enforce the Kubernetes Restricted Pod Security profile through Pod Security Admission (audit and warn modes are strictly prohibited). Application containers MUST run as non-root and drop all capabilities, permitting at most `NET_BIND_SERVICE`. Workloads MUST run under dedicated platform-provisioned ServiceAccounts with automatic API token mounting disabled. Project pods MUST NOT mount service-account tokens or access the Kubernetes API. Project Services MUST be ClusterIP only; NodePort and LoadBalancer services represent perimeter bypasses and MUST be rejected. Cross-workload traffic MUST be constrained by explicit NetworkPolicies.

### III. Defense-in-Depth Identity & Decoupled Authorization
Public ingress MUST expose only the minimal browser-facing OIDC routes and authenticated session revocation. Administrative consoles (including Keycloak `/admin/`, master realm, metrics port 9000, and health endpoints) MUST NEVER be exposed via public ingress and require private, authenticated cluster access. Platform authentication MUST be handled via a custom Go gateway built strictly with the Go standard library (zero runtime third-party dependencies). The gateway forwards Keycloak-issued target-project access tokens; projects MUST independently verify token signatures, issuer, audience, and expiration. Platform identity is strictly `(issuer, subject)`—email addresses MUST NOT serve as persistent unique identifiers. Identity establishment does NOT grant application permissions; projects MUST explicitly declare business authorization, and missing declarations MUST fail closed.

### IV. Bounded Resource Allocation & 1/n Budgeting
Every container MUST define explicit CPU and memory requests and limits; unbounded borrowing or unconstrained memory execution is prohibited. Compute resources MUST be allocated according to a deterministic 1/n project model: application capacity equals measured node allocatable capacity minus platform reservations, divided equally across deployed projects. The aggregate resource consumption of a project (accounting for peak concurrency, rollout surge, Jobs, and CronJobs) MUST fit within its assigned slice. Candidate revisions exceeding project budgets or decreasing existing memory limits during the interim freeze MUST be rejected before application. Platform-managed namespace ResourceQuotas act as non-bypassable admission backstops.

### V. Isolated Shared Persistence & Zero Plaintext Secrets
Initial persistent database storage MUST be operated via a standard PostgreSQL instance (deployed via a Kubernetes StatefulSet without complex operator dependencies) backed by persistent disk storage, with strict per-project database and role isolation. Projects MUST connect via dedicated credentials and MUST NOT access Keycloak data, administrative endpoints, or peer project databases. Persistent Volume Claims MUST be backed by node storage and survive ordinary pod lifecycles. The deployment contract MUST maintain universal compatibility allowing seamless substitution with external managed database endpoints (e.g., AWS RDS or GCP Cloud SQL). Plaintext secret material MUST NEVER be committed to Git, embedded in Helm values, output in build logs, or transmitted in communication channels. Secrets MUST be operator-provisioned Kubernetes Secrets referenced by name, with strictly segregated credentials between operational duties (e.g., session revocation credentials separated from client provisioning credentials).

### VI. Environmental Portability Without Code Changes
The platform deployment contract MUST run consistently across local native k3s, single VPS k3s, EKS, and GKE without modifying application source code. Differences in ingress controllers, persistent storage classes, external database endpoints, and image-pull secrets MUST be handled purely via environment-specific configuration. Colocated single-VPS deployment represents the initial footprint, not a structural barrier to future horizontal scale, separate database nodes, or high-availability migration.

## Security, Isolation & Architectural Constraints

- **Minimal Gateway Dependencies:** The authentication gateway MUST use Go standard-library cryptographic primitives, TLS, HTTP, and JSON functionality. Third-party Go modules, external gateway frameworks, Redis, or OAuth2 Proxy MUST NOT be introduced into the runtime image.
- **Fail-Closed Session Authority:** The gateway MUST validate credential validity and active session status synchronously before proxying requests. Any authentication failure, expired session, or backend dependency failure MUST fail closed.
- **Online Current-Session Revocation:** Users MUST be able to terminate their active platform session across all protected projects via an authenticated, CSRF-protected gateway endpoint. The revocation MUST invalidate subsequent requests immediately on the cluster network without waiting for access token expiration.
- **Public Surface Allowlist:** Ingress routing rules MUST reject all traffic to internal endpoints, including Keycloak administrative consoles, management ports, and cluster internal JWKS/introspection channels.
- **Controlled Ingress Bypass Prevention:** Platform admission controls and preflight validations MUST reject project manifests defining NodePort/LoadBalancer Services, hostPath mounts, host namespaces, or privileged container security contexts.

## Deployment Quality Gates & Verification Standards

- **Capacity & Budget Verification:** Candidate deployment revisions MUST be verified against actual, observed node allocatable capacity before application (manually verified in the initial phase; automated via `platform-preflight` prior to branch promotion in future GitOps).
- **Interim Memory Limit Freeze:** Until the dynamic allocation lifecycle policy is formally ratified, revisions that reduce an existing workload's memory limits, or introduce new projects that force existing project slices below their deployed peak memory envelope, MUST be rejected prior to application.
- **Multi-Architecture Image Packaging:** Applications MUST be built and packaged into multi-architecture OCI images (supporting both Linux AMD64 and ARM64) pushed to GHCR and referenced in manifests by immutable SHA256 digests.
- **Restoration Verification for Backups:** When optional scheduled backups are enabled (default daily at 00:00 UTC with 7-day retention), backup validity MUST be proven via documented, successful restore exercises on a clean environment. Unexercised backups MUST NOT be considered disaster recovery assets.
- **Automated Manifest Validation:** Helm templates and Kustomize overlays MUST render cleanly with pinned tool versions and pass schema validation against target Kubernetes API versions.

## Governance

- **Supremacy & Precedence:** This Constitution is the authoritative standard for system architecture, workload security, resource boundaries, and operational procedures in `nacfson_pipeline`. Manifests, charts, and operational changes that contradict this document MUST NOT be approved or deployed.
- **Amendment Process:** Amendments to this Constitution require:
  1. A clear, documented rationale outlining the technical necessity or architectural evolution.
  2. An impact assessment detailing security, resource quota, and environmental portability consequences.
  3. A formal semantic version bump approved by the repository maintainer.
  4. Migration plans for existing deployed projects when breaking governance changes occur.
- **Versioning Policy:**
  - **MAJOR (X.0.0):** Incompatible governance shifts, removal or weakening of core security/isolation principles, or structural changes to the deployment model.
  - **MINOR (1.X.0):** Addition of new principles, new supported workload types, expansion of platform services, or material tightening/phasing of standards.
  - **PATCH (1.0.X):** Clarifications, wording refinements, documentation synchronization, and non-semantic corrections.
- **Compliance Review:** All pull requests and candidate revisions MUST be verified for compliance with this Constitution during code review and pre-deployment execution.

**Version**: 1.2.0 | **Ratified**: 2026-10-01 | **Last Amended**: 2026-10-01
