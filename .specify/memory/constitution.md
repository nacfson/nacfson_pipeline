
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

**Edge TLS key exception.** Private keys that the platform ingress presents for public TLS (edge certificate keys) and certificate-authority account keys (ACME account keys) are ingress material, not backend credentials. They MAY be stored as Kubernetes Secrets, only in the `platform` namespace (edge certificate keys) and the `cert-manager` namespace (ACME account keys). They MUST be generated and rotated inside the cluster by the certificate manager and MUST NEVER be committed to Git. They MUST NOT be created in, copied to, or mounted by any project namespace or project pod. Only platform components (the ingress controller, the certificate manager, and the reconciler) and the break-glass group may read them; project workloads and read-only operator access MUST NOT be able to read them. This exception covers no other credential.
*Rationale:* The ingress controller needs the key in memory for every TLS handshake and cannot call Vault per connection. A Vault sync would still produce a Kubernetes Secret, and would make public HTTPS depend on manual Vault unseal. Edge keys are short-lived, rotated on every renewal, and revocable.

### VI. Environmental Portability Without Code Changes
The platform deployment contract MUST run consistently across local native k3s, single VPS k3s, EKS, and GKE without modifying application source code. Differences in ingress controllers, persistent storage classes, external database endpoints, and image-pull secrets MUST be handled purely via environment-specific configuration. Colocated single-VPS deployment represents the initial footprint, not a structural barrier to future horizontal scale, separate database nodes, or high-availability migration.

### VII. Distinct Lifecycle Signals for Dependent Project Workloads
Always-on project HTTP workloads that include a site frontend depending on a project backend MUST declare three separate health roles: startup, readiness, and liveness. Startup MUST prove the frontend can reach the backend's process-availability signal at least once before the frontend is considered started. Readiness MUST continuously prove that same frontend-to-backend process-availability hop after startup; a readiness failure is the explicit "unconnected from backend" signal on the **cluster hop**, withholds new traffic from that instance, and is the durable hook reserved for future operator notifications. Liveness MUST verify only that the local process is alive and MUST NOT depend on peer or data-store reachability. Backend readiness, when declared, MUST reflect required data-dependency readiness rather than process aliveness alone. Always-on project app containers MUST explicitly declare an always-restart policy. Readiness failures MUST NOT by themselves restart containers after startup has succeeded. Push notification delivery is not required by this principle; consumers MAY be added later that watch the readiness disconnect signal without inventing a parallel probe type.

For public browser SPAs that call the project API on the project's published origin, "frontend can access backend" MUST ALSO include a **browser API origin allowlist**: the backend MUST declare the environment public app origin (`PUBLIC_APP_URL`) and MUST include that origin in its CORS allowlist (`ALLOWED_ORIGINS`). Cluster startup/readiness success on the process-availability hop MUST NOT be treated as proof that browser `/api` fetches succeed. Browser-shaped validation (Origin + CORS preflight asserting `Access-Control-Allow-Origin`) MUST be part of acceptance for such projects. Encoding CORS header assertions into kubelet `httpGet` readiness is not required when Git-declared allowlists plus synthetic validation cover the contract.
*Rationale:* Collapsing boot ordering, continuous disconnect detection, and process restart into one check either hides temporary unconnected states or causes restart storms. Separating startup from readiness keeps boot gating and the future notification surface explicit. Treating only the in-cluster hop as "access" hides CORS allowlist failures that leave pods Ready while the SPA reports it cannot connect.

## Security, Isolation & Architectural Constraints

- **Minimal Gateway Dependencies:** The authentication gateway MUST use Go standard-library cryptographic primitives, TLS, HTTP, and JSON functionality. Third-party Go modules, external gateway frameworks, Redis, or OAuth2 Proxy MUST NOT be introduced into the runtime image.
- **Fail-Closed Session Authority:** When a project asks the sign-in service to confirm a session, failure or unavailability leaves the visitor unsigned-in for that part. A project that does not use sign-in continues to answer while the sign-in service is unavailable. The sign-in service is not placed on the project's door. Online sign-out of a confirmed session stays.
- **Online Current-Session Revocation:** Users MUST be able to terminate their active platform session across all protected projects via an authenticated, CSRF-protected gateway endpoint. The revocation MUST invalidate subsequent requests immediately on the cluster network without waiting for access token expiration.
- **Public Surface Allowlist:** Ingress routing rules MUST reject all traffic to internal endpoints, including Keycloak administrative consoles, management ports, and cluster internal JWKS/introspection channels.
- **Controlled Ingress Bypass Prevention:** Platform admission controls and preflight validations MUST reject project manifests defining NodePort/LoadBalancer Services, hostPath mounts, host namespaces, or privileged container security contexts.
- **Probe Role Separation:** For dependent project frontend/backend pairs, startup checks gate first backend reachability on the cluster hop; readiness checks continuously expose frontend-to-backend unconnected state for traffic admission and future notifications; liveness checks remain process-local. Status endpoints used for these checks stay subject to the public-surface allowlist and MUST NOT imply that Keycloak or platform-admin health may be exposed publicly.
- **Browser API Origin Allowlist:** Public SPA projects MUST declare `PUBLIC_APP_URL` and include that origin in backend `ALLOWED_ORIGINS`. Ready cluster hops MUST NOT be accepted as sufficient proof of browser `/api` access when the allowlist omits the public origin.
- **Platform-Owned Edge TLS:** Public TLS MUST terminate at the platform ingress, which selects the certificate by requested hostname from a platform-owned certificate inventory. Projects MUST NOT declare certificates, certificate issuers, certificate-authority challenges, TLS stores, TLS options, or TLS Secrets, and project routes MUST NOT reference a certificate. Domain-ownership challenge workloads MUST run only in platform namespaces as ClusterIP services. Pre-deployment validation MUST reject a project route whose hostname is not covered by the platform inventory.

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

## Amendment 2.1.0

**Rationale**: Project PN needs an explicit frontend-to-backend connection contract covering **both** the cluster process-availability hop and the browser API origin allowlist. Startup must wait for the first successful backend reachability hop. Readiness must continuously expose **cluster** unconnected state as the operator-visible signal reserved for future notifications, without using liveness (and restart storms) for soft disconnects. Always-on app workloads must declare always-restart explicitly. Public SPA API access must declare `PUBLIC_APP_URL` inside `ALLOWED_ORIGINS`, because Ready probes alone do not prevent CORS preflight failures that surface as SPA "could not connect" errors.

**Impact**:

- Security: Unchanged isolation and public-surface rules. Probe targets remain project status checks on the existing frontend-to-backend allowlist; platform-admin and Keycloak health stay private. CORS origins are public URLs, not secrets.
- Resource quota: Negligible probe overhead only; no slice formula change.
- Portability: Unchanged for probes across environments. Per-environment public origins stay in Flux patches.
- Operations: Frontend not-ready remains the durable **cluster** disconnect hook. Browser origin allowlist + synthetic CORS checks are required acceptance for public SPA projects. Push notification delivery remains deferred.

**Migration**: Update Project PN frontend to declare startup and readiness checks against the backend process-availability signal, keep frontend liveness process-local, point backend readiness at dependency readiness, set explicit always-restart on frontend and backend, and declare `PUBLIC_APP_URL` / `ALLOWED_ORIGINS` (with env patches) so the public door origin is CORS-allowlisted. Align feature `007-pn-readiness-restart` with Principle VII. Do not ship notification sinks in the same migration.

**Maintainer approval**: This minor bump is written in the working tree with Principle VII and depends on the still-unapproved 2.0.0 draft lineage. Do not commit version 2.1.0 until the repository maintainer approves the constitution revision set.

## Amendment 2.2.0

**Rationale**: Public TLS belongs to the platform. Projects publish a hostname by routing only. Feature `008-platform-tls-termination` found that placing certificates and their domain-ownership challenge workloads in a project namespace collided with the project quota (no node ports), so Project PN never received a trusted certificate. The ingress controller must hold edge private keys in memory, which Principle V did not address: keys held as Kubernetes Secrets for the sign-in host (since cert-manager was introduced) already conflicted with its wording.

**Impact**:

- Security: Tightened for projects. Projects lose every TLS object, the challenge firewall exception, and the certificate-manager admission exception. Edge and ACME account keys are explicitly allowed as Kubernetes Secrets only in `platform` and `cert-manager`, readable only by platform components and break-glass. Backend credentials stay Vault-only. Challenge services become ClusterIP, so no node port opens.
- Resource quota: Unchanged for projects. Short-lived challenge pods count against the platform reservation.
- Portability: Unchanged. Certificate source and hostnames differ only through environment-specific configuration.

**Migration**: Move the Project PN certificate to the `platform` namespace and serve all edge certificates from one platform certificate inventory. Change project routes to select certificates by hostname only. Remove the Project PN challenge NetworkPolicy, and narrow the certificate-manager admission exception to `platform` and `cert-manager`. Add pre-deployment rejection of project TLS objects and uncovered hostnames. Delivered by feature `008-platform-tls-termination` in two steps, so the sign-in host keeps a trusted certificate.

**Maintainer approval**: Requested by the repository maintainer on 2026-10-12 (analysis finding C1 of feature 008, option A). Merging this amendment's pull request records approval. This minor bump builds on the 2.0.0 and 2.1.0 records above, which are still marked unapproved; the maintainer's merge approves the revision set 2.0.0–2.2.0.

## Clarification 2.2.1

**Rationale**: Project log collection needs a read-only view of container log files on the node, and that mount is a platform collector, not a project workload.

The log collector in the platform namespace `observability` may mount `/var/log/pods` read-only through `hostPath`. It is not a project workload, so Principle II and the `hostPath` rejection rule apply to project namespaces and manifests only.

**Impact**:

- Security: one read-only node mount for one platform pod.
- Resource: requests and limits are declared on the collector and the store. They are not in the recorded platform reservation until a live `platformReservations` measurement is committed. Do not add them by hand.
- Portability: unchanged.

**Maintainer approval**: The operator accepted this note on 2026-10-22 with the design.

**Version**: 2.2.1 | **Ratified**: 2026-10-01 | **Last Amended**: 2026-10-22
