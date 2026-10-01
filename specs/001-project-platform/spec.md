# Feature Specification: Personal Project Platform

**Feature Branch**: `001-project-platform`

**Created**: 2026-10-01

**Status**: Draft

**Input**: User description: "Build a small Kubernetes platform for managing personal projects with modest CPU and memory requirements. Provide shared Google-backed login, bounded project resources, persistent PostgreSQL, and Git-driven deployment. Derived from SPEC.md."

## Clarifications

### Session 2026-10-01
- Q: For container resource budgeting, should CPU and memory requests strictly equal their limits, or should requests be permitted to be lower than limits? → A: Strict equality: Both CPU and memory requests must strictly equal their limits (`requests == limits`).
- Q: Can project applications connect to the outside internet by default, or should external internet connections be blocked unless explicitly permitted? → A: Block all outside internet by default; allow only cluster DNS and PostgreSQL port 5432, requiring explicit NetworkPolicy egress rules for external APIs.
- Q: When a manual deployment fails midway through applying manifests, how should the operator handle the partial failure? → A: Roll back to prior Git baseline: Revert all applied manifests to the last successfully deployed Git commit SHA.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Seamless Single Sign-On Access Across Projects (Priority: P1)

As a platform user, I want to authenticate once using my existing Google account and smoothly access all hosted personal project applications without repeatedly entering credentials or needing per-project account creation.

**Why this priority**: Single sign-on and automated registration represent the primary user entry path. Without a reliable identity gateway, no protected project can be accessed securely by users.

**Independent Test**: A user authenticates via Google on Project A, lands on its ordinary user landing page, and then navigates directly to Project B where their identity is immediately recognized without a secondary login prompt.

**Acceptance Scenarios**:

1. **Given** a user with a valid Google account who has never accessed the platform, **When** they visit any protected project URL, **Then** the platform automatically registers an ordinary user identity and presents the project's standard landing page without requiring administrative approval.
2. **Given** a user with an active authenticated session on Project A, **When** they navigate to Project B, **Then** they are recognized under the identical platform identity without another Google authentication challenge.
3. **Given** an unauthenticated visitor, **When** they attempt to access protected project endpoints directly, **Then** the platform denies access and redirects the visitor through the login flow.

---

### User Story 2 - Declarative Version-Controlled Deployment with Capacity Verification (Priority: P1)

As a platform operator, I want to define project deployments declaratively in Git and manually order and apply releases after verifying them against measured hardware capacity, ensuring that candidate revisions cannot exhaust server resources or disrupt running services (with automated in-cluster GitOps reconciliation scheduled for future expansion).

**Why this priority**: Declarative deployment and capacity verification protect the stability and availability of shared infrastructure, preventing misconfigured or oversized workloads from causing host starvation.

**Independent Test**: Prepare a candidate project configuration change with declared compute budgets; observe that valid configurations can be applied sequentially while over-budget configurations are blocked before application.

**Acceptance Scenarios**:

1. **Given** a candidate deployment revision whose peak resource footprint fits within the computed project budget, **When** the operator evaluates the candidate against capacity, **Then** the candidate passes validation and is applied to the environment.
2. **Given** a candidate deployment revision requesting resources that exceed the project's computed fair-share slice, **When** evaluated, **Then** the deployment is rejected before application and the existing live deployment remains completely undisturbed.
3. **Given** an active environment with zero deployed projects, **When** the capacity calculation runs, **Then** the system computes available capacity accurately without arithmetic division-by-zero errors.
4. **Given** a multi-manifest deployment where an intermediate component fails verification or health checks, **When** the failure occurs, **Then** the operator rolls back all changes to the previously recorded healthy Git baseline commit, leaving no partially deployed resources.

---

### User Story 3 - Workload Security Sandboxing and Perimeter Defense (Priority: P2)

As a platform operator, I want all deployed project applications to execute in strictly bounded, unprivileged sandboxes with private networking, preventing any compromised or misconfigured workload from affecting the host, the cluster control plane, or other projects.

**Why this priority**: Multi-project colocation on shared compute requires non-negotiable security isolation to guarantee system integrity and project data privacy.

**Independent Test**: Attempt to deploy an application container requiring root privileges, elevated system capabilities, host storage mounts, or direct administrative API access, and confirm the platform strictly refuses execution.

**Acceptance Scenarios**:

1. **Given** a project application container specification, **When** the container starts, **Then** it runs as a non-root user, drops all system capabilities, and has no automatic cluster administrative token mounted.
2. **Given** a running project workload, **When** it attempts to initiate unauthorized network connections to other project services or undeclared external internet endpoints, **Then** platform network policies block the traffic.
3. **Given** external web traffic, **When** a client sends requests to public ingress, **Then** protected project application routes are served via the authentication gateway, while internal administrative consoles (`/admin/`, master realm), private operational endpoints, and metrics are strictly rejected.

---

### User Story 4 - Dedicated Persistent Data Storage and Isolation (Priority: P2)

As a project author, I want my application to persist relational data reliably across restarts and redeployments while guaranteeing that no other project on the platform can inspect or tamper with my database.

**Why this priority**: Stateful applications require dependable data persistence, and strict database isolation prevents cross-project data leaks.

**Independent Test**: Write records to a project database, trigger StatefulSet database pod replacement and application pod upgrades, and confirm data integrity across in-place maintenance; then attempt cross-database queries from another project role to confirm access is rejected.

**Acceptance Scenarios**:

1. **Given** an application actively reading and writing persistent data, **When** the database pod or application pod is replaced or upgraded in place, **Then** all previously stored database records remain completely intact and accessible.
2. **Given** a database credential provisioned for Project A, **When** that credential is used to access or query Project B's database, **Then** the database engine strictly rejects the connection.
3. **Given** an application workload is removed or updated, **When** cleanup actions run, **Then** shared database instances and persistent volumes are protected against unintended deletion.

---

### User Story 5 - Instant Cross-Project Session Revocation (Priority: P2)

As a user, I want the ability to explicitly terminate my active session from any application, ensuring that access is revoked immediately across all connected projects on that browser session.

**Why this priority**: Immediate session termination is a critical privacy and security control, allowing users to secure their accounts on shared or public devices.

**Independent Test**: Trigger a sign-out request from one project, and immediately verify that subsequent requests to any other project using that session are rejected on the next request.

**Acceptance Scenarios**:

1. **Given** an active authenticated session spanning multiple projects, **When** the user invokes the sign-out action, **Then** the active session is revoked immediately across all platform projects.
2. **Given** a freshly revoked session, **When** the browser submits a protected request to any project, **Then** the request is rejected immediately, even if short-lived authorization credentials have not yet reached their expiration timestamp.
3. **Given** a revocation request submitted without active session credentials or lacking cross-site request protection, **When** processed, **Then** the request is rejected and active sessions remain untouched.

---

### User Story 6 - Multi-Environment Portability and Expansion (Priority: P3)

As an operator, I want the exact same application configurations and deployment declarations to execute consistently across local development workstations, personal virtual private servers, and commercial cloud environments without modifying application source code.

**Why this priority**: Portability prevents cloud vendor lock-in and provides a smooth growth path from a modest single-server deployment to scalable managed cloud environments.

**Independent Test**: Deploy the identical project application specification to a local development environment and a remote server using environment-specific overlays, verifying identical functional behavior.

**Acceptance Scenarios**:

1. **Given** an application specification validated on a single personal server, **When** deployed to a managed cloud cluster with cloud-specific storage and networking overlays, **Then** the application runs without modifications to its core business code.
2. **Given** an environment utilizing managed cloud database services instead of self-hosted database engines, **When** configured, **Then** the application connects seamlessly using standard connection references.

---

### Edge Cases

- What happens when a user attempts to log in while the external identity provider is unreachable or experiencing an outage? The system MUST fail closed, present a clear service unavailability message, and prohibit unauthenticated entry.
- What happens when Keycloak or the session validation endpoint is unreachable during gateway request verification? The gateway MUST fail closed, return HTTP 503 Service Unavailable, and refuse unverified traffic.
- What happens when a candidate project deployment lacks a business permission declaration for ordinary users? Platform preflight validation MUST immediately reject the project deployment before application.
- What happens when an authenticated user has no elevated administrator bindings for a deployed project? The user MUST receive ordinary-user access to the project's standard landing page only, with all administrative and undeclared operations denied.
- What happens when a candidate deployment revision requests compute resources that would reduce an existing project's memory below its current operating allocation? The system MUST reject the candidate revision and retain the existing healthy deployment state.
- What happens when a project container attempts to mount system service tokens, run as the root user, declare non-ClusterIP services, or declare host-level network/storage access? Platform admission controls MUST immediately reject pod/service creation.
- What happens when a project container attempts to make an outbound connection to an external internet API without an explicit egress rule? Platform network policies MUST block the egress traffic immediately.
- What happens when a multi-manifest deployment fails midway through application? The operator MUST roll back all applied manifests to the previously recorded healthy Git baseline commit, leaving no partially deployed resources.
- What happens when all projects are undeployed, leaving zero active projects? The capacity calculation MUST succeed cleanly and report 100% idle capacity without arithmetic division-by-zero errors.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: System MUST automatically register any authenticated Google account upon first successful sign-in without requiring manual administrator approval or account allowlists.
- **FR-002**: System MUST recognize an active platform session across all protected applications without requiring repeated external identity challenges.
- **FR-003**: System MUST establish platform user identity strictly from immutable Keycloak issuer and subject identifiers, and MUST NOT use email addresses as permanent unique identifiers or account-linking credentials.
- **FR-004**: The authentication gateway MUST forward Keycloak-issued access tokens scoped specifically to the target project audience, stripping all client-supplied identity headers at the ingress boundary. Protected project applications MUST independently verify token digital signatures against Keycloak's published verification keys, validate audience, issuer, expiration, and required identity claims, and support key rotation without sharing private keys. All external ingress routes to protected projects MUST route through the gateway authentication check with zero bypass routes.
- **FR-005**: System MUST decouple platform authentication from business authorization. Every project MUST declaratively define its ordinary role's business capabilities; any candidate project deployment missing its ordinary capability declaration MUST be rejected prior to deployment. For valid projects, platform identity establishment does not grant elevated permissions; authenticated users without operator-provisioned administrator bindings MUST receive ordinary landing-page access only, and all undeclared actions MUST be denied.
- **FR-006**: System MUST grant application administrative privileges only through explicit operator-provisioned bindings to verified platform identities `(issuer, subject)` scoped strictly to the named project, never by inference from email addresses, registration order, or token possession. Ordinary user landing pages MUST NOT expose other users' private data or undeclared administrative capabilities.
- **FR-007**: System MUST provide an authenticated, cross-site request forgery protected logout mechanism on the gateway that synchronously terminates the active Keycloak session and invalidates its refresh capability across all protected projects on the next request, while preserving independent sessions on other devices. The gateway MUST perform online session validation on every protected request; token signature verification alone is insufficient. If the identity session service is unreachable or encounters dependency failure, the gateway MUST fail closed and deny access.
- **FR-008**: Public ingress routing MUST expose protected project application routes via the authentication gateway, and MUST expose only browser-facing authentication flows (login, broker callbacks) and session termination routes for the identity provider. Public ingress MUST strictly deny public access to internal administrative consoles (`/admin/`, master realm), private operational endpoints, health checks, and metrics.
- **FR-009**: System MUST enforce the Kubernetes Restricted Pod Security profile through Pod Security Admission (enforce mode) across all project namespaces, requiring non-root execution, dropped system capabilities (permitting at most `NET_BIND_SERVICE`), and platform-level rejection of automatic and projected service-account token mounts. Workloads MUST run under dedicated platform-owned ServiceAccounts. Project Services MUST be ClusterIP only (NodePort and LoadBalancer Services MUST be rejected). Projects and project reconciliation identities MUST NOT possess permissions to create or modify Namespace labels, ServiceAccounts, ResourceQuotas, NetworkPolicies, or RBAC roles.
- **FR-010**: System MUST enforce default-deny NetworkPolicies on both ingress and egress in project namespaces; project workloads MUST be permitted egress only to internal cluster DNS and shared PostgreSQL (port 5432) by default, requiring explicit declarative egress allowlists for any external APIs or services.
- **FR-011**: Every container specification MUST declare bounded CPU and memory requests and limits, with requests strictly equaling limits (`requests == limits`) for both CPU and memory. Unbounded borrowing, overcommit, or unconstrained memory execution MUST be prohibited.
- **FR-012**: System MUST calculate per-project resource budgets using a deterministic 1/n allocation model: Application Capacity = Node Allocatable Capacity - Platform Overhead (Keycloak, PostgreSQL, Ingress, System), divided equally by the deployed project count (`Per-Project Budget = Application Capacity / n`). Aggregate peak concurrency across all project containers (including rollout surge, Job parallelism, and scheduled CronJobs) MUST fit within the assigned slice. Capacity calculations MUST be based on observed node allocatable capacity measurements, and platform-owned namespace ResourceQuotas MUST enforce these slices as admission backstops.
- **FR-013**: Deployment configurations MUST be maintained declaratively in Git and verified against peak concurrent resource limits and security rules prior to deployment. The deployed baseline MUST be recorded by Git commit SHA. In the initial phase, the operator orders and applies verified Git manifests manually, and on any partial deployment failure, the operator MUST roll back all applied manifests to the last recorded healthy Git commit baseline. Automated in-cluster continuous GitOps reconciliation is scheduled for future expansion.
- **FR-014**: System MUST reject candidate revisions that reduce existing workloads' memory limits, introduce new projects that force existing projects below their deployed peak memory envelopes, or exceed measured allocatable capacity.
- **FR-015**: System MUST provide dedicated persistent relational database instances and restricted user roles per project, backed by persistent disk storage surviving pod replacements, and strictly prohibiting cross-database or administrative access.
- **FR-016**: System MUST manage sensitive credentials outside version-controlled source manifests and enforce least-privilege separation between administrative operational duties (e.g., session revocation credentials separated from client provisioning credentials).
- **FR-017**: System MUST define a declarative project configuration contract enabling onboarding of new projects without modifying authentication gateway source code, including idempotent Keycloak client provisioning that preserves operator secrets. Workload manifests MUST reference immutable image digests (SHA256) from private container registries via namespace-scoped image-pull secrets, and MUST support the host node architecture (with multi-architecture Linux AMD64 and ARM64 packaging).
- **FR-018**: When disaster recovery backup is enabled, the system MUST execute automated daily backups at 00:00 UTC with 7-day retention and verifiable restore capability.

### Key Entities

- **Platform Identity**: Represents a unique, verified user account defined by the pair of external identity issuer and subject identifier.
- **User Session**: Represents an active authenticated state in a user's browser, valid across all authorized project applications until expired or explicitly revoked.
- **Project Application**: An independent software workload (HTTP web service, background worker, or scheduled batch job) belonging to a specific namespace with dedicated compute and storage budgets.
- **Resource Budget**: The computed fair-share slice of CPU and memory allocated to a project where requests strictly equal limits, evaluated against peak concurrency (including rollout surge and batch jobs).
- **Project Database**: An isolated relational storage instance provisioned for a specific project, accessible only with project-scoped credentials.
- **Deployment Revision**: A version-controlled declarative release candidate representing the complete desired state of all platform services and project applications.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: First-time sign-in flow completes in under 5 seconds from Google authentication to landing page presentation.
- **SC-002**: 100% of cross-project navigation events within an active session occur without secondary login challenges.
- **SC-003**: 100% of requests presenting a revoked session are rejected on the immediate subsequent attempt across all protected applications.
- **SC-004**: 100% of candidate deployment revisions exceeding allocated CPU/memory capacity, lacking node capacity measurements, or violating workload isolation rules are rejected prior to deployment by operator pre-application verification (with automated promotion checks in future GitOps).
- **SC-005**: 100% of attempts by a project workload to mount cluster tokens, run as root, declare non-ClusterIP services, or query unauthorized databases fail closed.
- **SC-006**: Stored application and database data survives 100% of ordinary workload container restarts, StatefulSet pod replacements, and in-place node OS upgrades. (Cross-node rescheduling or physical host destruction is explicitly bounded and requires restoration from scheduled disaster recovery backup archives).
- **SC-007**: Zero code changes required in project applications when switching deployment targets between local environments, standalone servers, and managed cloud infrastructure.
- **SC-008**: 100% of disaster recovery backup archives (when enabled) successfully pass automated restore verification testing.

## Assumptions

- Google identity services provide federated identity brokering for platform user authentication.
- Workloads primarily consist of web applications, asynchronous background workers, and scheduled batch tasks.
- Hardware capacity is observed directly from the host environment rather than relying on unverified virtual machine sizing specifications.
- Application configuration and environmental secrets are provisioned by the operator prior to initial workload deployment.
- Project authors declare their required ordinary business permissions and compute envelopes in their project specifications.
