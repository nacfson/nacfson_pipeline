
# nacfson_pipeline Constitution

## Core Principles

### I. Declarative Configuration as Single Source of Truth (Continuous GitOps)
All declarative configurations, workload definitions, and environment profiles MUST reside in Git. Each environment MUST track a protected deployment source. Changes MUST reach a cluster only through an in-cluster, pull-based reconciler (FluxCD) running as ordinary cluster workloads, and only after a revision has passed that environment's required checks, including `platform-preflight`. The reconciler MUST apply declarations in explicit dependency order, correct drift, and prune removed declarations. Persistent data declarations (volume claims and database or vault storage) MUST be protected from pruning. The reconciler MUST NOT store, decrypt, generate, or deliver secret material. It MUST read the repository without any repository credential stored in the cluster, and MUST have no write access to the repository. Manual cluster changes are prohibited except the one-time reconciler installation and documented break-glass actions (for example reconciliation suspend/resume, vault unseal, emergency changes, and verification exercises). Any break-glass change MUST be reconciled back to Git. Rollback MUST be performed by reverting the deployment source. Images built by this project MUST be referenced by immutable SHA256 digests. Third-party images MUST be pinned to an exact upstream version tag; floating tags (such as `latest` or `16-alpine`) are prohibited.
*Rationale:* Operator-applied ordering skipped declarations, left drift and orphaned objects, and depended on one workstation's credentials. Pull-based reconciliation keeps the management interface closed to inbound automation.

### II. Strict Workload Isolation & Restricted Pod Security
All project namespaces MUST enforce the Kubernetes Restricted Pod Security profile through Pod Security Admission (audit and warn modes are strictly prohibited). Application containers MUST run as non-root and drop all capabilities, permitting at most `NET_BIND_SERVICE`. Workloads MUST run under dedicated platform-provisioned ServiceAccounts with automatic API token mounting disabled. Project pods MUST NOT mount service-account tokens or access the Kubernetes API. Project Services MUST be ClusterIP only; NodePort and LoadBalancer services represent perimeter bypasses and MUST be rejected. Cross-workload traffic MUST be constrained by explicit NetworkPolicies.

### III. Defense-in-Depth Identity & Decoupled Authorization
Public ingress MUST expose only the minimal browser-facing OIDC routes and authenticated session revocation. Administrative consoles (including Keycloak `/admin/`, master realm, metrics port 9000, and health endpoints) MUST NEVER be exposed via public ingress and require private, authenticated cluster access. Platform authentication MUST be handled via a custom Go gateway built strictly with the Go standard library (zero runtime third-party dependencies). The shared sign-in service establishes who a user is only after a project asks. It does not select the project's published addresses. A project that uses shared sign-in verifies a confirmed identity for signature, issuer, audience, and expiration. A project that does not use shared sign-in has no connection to the sign-in service. Platform identity is strictly `(issuer, subject)`—email addresses MUST NOT serve as persistent unique identifiers. Identity establishment does NOT grant application permissions; projects MUST explicitly declare business authorization, and missing declarations MUST fail closed.

### IV. Bounded Resource Allocation & 1/n Budgeting
Every container MUST define explicit CPU and memory requests and limits; unbounded borrowing or unconstrained memory execution is prohibited. Compute resources MUST be allocated according to a deterministic 1/n project model: application capacity equals measured node allocatable capacity minus platform reservations, divided equally across deployed projects. The aggregate resource consumption of a project (accounting for peak concurrency, rollout surge, Jobs, and CronJobs) MUST fit within its assigned slice. Candidate revisions exceeding project budgets or decreasing existing memory limits during the interim freeze MUST be rejected before application. Platform-managed namespace ResourceQuotas act as non-bypassable admission backstops.

### V. Isolated Shared Persistence & Zero Plaintext Secrets (Centralized Cluster Vault)
Initial persistent database storage MUST be operated via a standard PostgreSQL instance (deployed via a Kubernetes StatefulSet without complex operator dependencies) backed by persistent disk storage, with strict per-project database and role isolation. Projects MUST connect via restricted roles and MUST NOT access Keycloak data, administrative endpoints, or peer project databases. Persistent Volume Claims MUST be backed by node storage and survive ordinary pod lifecycles. The deployment contract MUST maintain universal compatibility allowing seamless substitution with external managed database endpoints (e.g., AWS RDS or GCP Cloud SQL). Plaintext secret material MUST NEVER be committed to Git, embedded in Helm values, output in build logs, or transmitted in communication channels. All platform and project backend credentials (database passwords, container registry tokens, signing keys, external API credentials) MUST be stored, managed, and executed exclusively within a self-hosted internal cluster Vault inside an isolated trusted execution boundary. Backend credentials MUST NOT be delivered to application nodes or project pods through Kubernetes Secrets, environment variables, volume mounts, or in-memory responses. Applications and nodes MUST invoke authorized operations through protected Vault integrations that execute on their behalf, receiving only sanitized operational results. Strictly segregated credentials MUST be maintained between operational duties (e.g., session revocation credentials separated from client provisioning credentials).

### VI. Environmental Portability Without Code Changes
The platform deployment contract MUST run consistently across local native k3s, single VPS k3s, EKS, and GKE without modifying application source code. Differences in ingress controllers, persistent storage classes, external database endpoints, and image-pull secrets MUST be handled purely via environment-specific configuration. Colocated single-VPS deployment represents the initial footprint, not a structural barrier to future horizontal scale, separate database nodes, or high-availability migration.

## Security, Isolation & Architectural Constraints

- **Minimal Gateway Dependencies:** The authentication gateway MUST use Go standard-library cryptographic primitives, TLS, HTTP, and JSON functionality. Third-party Go modules, external gateway frameworks, Redis, or OAuth2 Proxy MUST NOT be introduced into the runtime image.
- **Fail-Closed Session Authority:** When a project asks the sign-in service to confirm a session, failure or unavailability leaves the visitor unsigned-in for that part. A project that does not use sign-in continues to answer while the sign-in service is unavailable. The sign-in service is not placed on the project's door. Online sign-out of a confirmed session stays.
- **Online Current-Session Revocation:** Users MUST be able to terminate their active platform session across all protected projects via an authenticated, CSRF-protected gateway endpoint. The revocation MUST invalidate subsequent requests immediately on the cluster network without waiting for access token expiration.
- **Public Surface Allowlist:** Ingress routing rules MUST reject all traffic to internal endpoints, including Keycloak administrative consoles, management ports, and cluster internal JWKS/introspection channels.
- **Controlled Ingress Bypass Prevention:** Platform admission controls and preflight validations MUST reject project manifests defining NodePort/LoadBalancer Services, hostPath mounts, host namespaces, or privileged container security contexts.

## Deployment Quality Gates & Verification Standards

- **Capacity & Budget Verification:** Candidate deployment revisions MUST be verified against actual, observed node allocatable capacity by the automated `platform-preflight` required check before they can be promoted to an environment's deployment source. The platform reservation MUST include the reconciler's own resource footprint.
- **Interim Memory Limit Freeze:** Until the dynamic allocation lifecycle policy is formally ratified, revisions that reduce an existing workload's memory limits, or introduce new projects that force existing project slices below their deployed peak memory envelope, MUST be rejected prior to application.
- **Multi-Architecture Image Packaging:** Applications MUST be built and packaged into multi-architecture OCI images (supporting both Linux AMD64 and ARM64) pushed to GHCR and referenced in manifests by immutable SHA256 digests.
- **Restoration Verification for Backups:** When optional scheduled backups are enabled (default daily at 00:00 UTC with 7-day retention), backup validity MUST be proven via documented, successful restore exercises on a clean environment. Unexercised backups MUST NOT be considered disaster recovery assets.
- **Automated Manifest Validation:** Helm templates and Kustomize overlays MUST render cleanly with pinned tool versions and pass schema validation against target Kubernetes API versions.
- **Repository Secret Hygiene:** The repository is public. Every candidate revision MUST pass secret scanning, and the full history MUST pass a secret scan before the repository's visibility is widened. Any real credential that has appeared in Git history, encrypted or not, MUST be treated as exposed and rotated.

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

## Amendment 2.0.0

**Rationale**: Visitors open a project without the sign-in service standing on that road. The sign-in service remains the only shared way to establish who a user is, and only when that project asks.

**Impact**:

- Security: `none` projects have no platform identity. `shared` projects still reject a missing, forged, expired, or foreign identity on the parts that need a known user. Admin routes stay private.
- Resource quota: Unchanged.
- Portability: Unchanged. The door remains configuration, not application code.

**Migration**: Project PN declares `shared`. Its site and API addresses stay. `forward-auth` is removed from those routes. Ports 80 and 8080 stay aligned with its NetworkPolicy. Egress to the gateway on port 8080 is added for session confirmation only.

**Maintainer approval**: This major bump is written in the working tree and is not approved for commit. Do not commit version 2.0.0 until the repository maintainer approves it.

**Version**: 2.0.0 | **Ratified**: 2026-10-01 | **Last Amended**: 2026-10-09
