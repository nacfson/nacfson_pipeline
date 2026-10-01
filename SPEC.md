# Project platform specification

Status: planning specification; no implementation or deployment is included.

## 1. Purpose and scope

Build a small Kubernetes platform for managing personal projects with modest CPU and memory requirements. Provide shared Google-backed login, bounded project resources, persistent PostgreSQL, and Git-driven deployment.

The initial production target is one Oracle Cloud Always Free VPS running k3s. Local integration uses native k3s on Linux, without k3d. The deployment model must also support EKS and GKE; initial VPS support is not permission to omit those targets.

HTTP applications provide the first integration path, not the platform's only supported workload type. Background workers and scheduled jobs must be deployable without requiring an HTTP ingress or interactive login.

In this document, MUST identifies a required behavior, SHOULD identifies a recommended default, and MAY identifies an optional capability. Recommendations and unresolved details are explicitly distinguished from selected architecture.

## 2. Selected architecture

| Area | Selection |
| --- | --- |
| Kubernetes | Native k3s locally and on one production VPS; EKS/GKE deployment compatibility |
| Application packaging | Helm charts |
| Deployment reconciliation | Initial: Operator-applied Git-versioned manifests; Future: Automated FluxCD reconciliation and platform-preflight promotion |
| Application image delivery | GitHub Actions and GHCR; immutable deployment references |
| Ingress | Traefik; Cilium is not part of the initial design |
| Authentication gateway | Custom Go service using the standard library, without third-party Go modules at runtime |
| Identity provider | Keycloak with Google identity brokering |
| Registration | Any Google account may register; no administrator approval |
| Project access | Ordinary-user entry for every registered identity; business permissions come from the project declaration |
| Project credential verification | Projects independently verify the credential forwarded by the gateway |
| Database | Standard PostgreSQL (StatefulSet backed by persistent disk volume) |
| Database isolation | One initial PostgreSQL instance, separate databases and restricted roles |
| Persistent storage | Disk-backed persistent volumes on the VPS |
| Secrets | Operator-provided Kubernetes Secrets; no initial Vault deployment |
| Resource policy | Explicit CPU requests and limits, bounded memory, 1/n slices, preflight rejection, and namespace ResourceQuota |
| Workload boundary | Restricted Pod Security in project namespaces; platform-owned ServiceAccounts, quotas, and network policy |
| User-requested revocation | Current platform session only; authenticated gateway route |
| Backups | Optional; when enabled, daily at 00:00 UTC with seven-day retention |

Keycloak, PostgreSQL, Traefik, the gateway, and project workloads initially reside on the same VPS. In-cluster continuous GitOps controllers (FluxCD), separate infrastructure hosts, a second production server, and HA replicas are not initial requirements.

## 3. Traffic and trust boundaries

```text
Browser --> project.example.com --> Traefik --> Project workload
                                      |
                                      +--> Go gateway: authentication check
                                                   |
                                                Keycloak --> PostgreSQL
                                                   |
                                                 Google

GitHub candidate revision --> Operator verification & manual deployment (Initial)
                             [Future: platform-preflight --> protected branch --> FluxCD]
GitHub Actions --> GHCR --> Kubernetes image pulls
```

The authentication check MUST happen before a protected request reaches its project. The login flow redirects the browser through Keycloak and Google; the diagram does not imply that Google is contacted for every application request.

The existing hostname pattern MUST be preserved: a central identity hostname such as `auth.example.com` and separate project subdomains. Exact domain names are operator configuration, not embedded platform constants.

Keycloak's login and callback routes MUST remain reachable without an already authenticated platform session. Protected workloads MUST NOT have an alternative externally accessible route that bypasses the gateway. Workload traffic isolation MUST be enforced using network policy, not an assumption that ClusterIP services are inherently inaccessible to other pods.

### ISOLATE-01: Project workload and administrative boundaries

Project namespaces MUST enforce the Kubernetes Restricted Pod Security profile through Pod Security Admission. Audit and warn modes MUST NOT satisfy this requirement. Enforcement covers privileged containers, host namespaces, hostPath, host ports, privilege escalation, non-root execution, capabilities, and seccomp, as defined by that profile.

Under Restricted, a container MUST drop all capabilities and MAY add back only `NET_BIND_SERVICE`. An image that cannot run as non-root within that rule MUST be adapted. Project-namespace enforcement MUST NOT be relaxed to admit an image.

Each project MUST run its pods as a dedicated platform-provisioned ServiceAccount. That account MUST NOT receive application-granted Kubernetes API permissions. Automatic ServiceAccount token mounting MUST be disabled on both the ServiceAccount and the pod. Project pods MUST NOT mount a projected service-account token and MUST NOT select or create a different ServiceAccount. Pod Security Admission does not evaluate these token settings, so a platform admission control MUST reject them.

Namespace Pod Security labels, ResourceQuota, NetworkPolicy, and the project ServiceAccount MUST be reconciled only by the platform identity. The project reconciliation identity MAY create and update project workload objects. It MUST NOT be able to create or modify Namespace objects, ResourceQuota, NetworkPolicy, ServiceAccounts, Roles, RoleBindings, ClusterRoles, or ClusterRoleBindings. Project Services MUST be ClusterIP; NodePort and LoadBalancer Services are bypass routes and MUST be rejected. A project Ingress for a protected HTTP workload MUST use the gateway authentication check.

Restricted enforcement in this requirement applies to project namespaces. It does not select a Pod Security profile for Keycloak, PostgreSQL, Traefik, Flux (when deployed), or the gateway.

Public ingress MUST expose only the browser-facing routes required to sign in and to revoke the current session:

- The application realm's login and OIDC routes the browser follows, including login-actions and the Google identity-broker callback.
- Assets required by those pages.
- The gateway's current-session revocation route from AUTH-05.

That allowlist is narrower than exposing every `/realms/` path. Public ingress MUST NOT expose the Keycloak account console, `/admin/`, the administrative realm including `/realms/master/`, health, metrics, or management port `9000`. A separate administrative hostname is not sufficient access control. Operators MUST reach administrative, health, and metrics endpoints through an authenticated private path, such as port-forwarding. Token, introspection, and JWKS requests from the gateway and from projects MUST stay on the cluster network.

## 4. Identity and authentication requirements

### AUTH-01: Automatic Google registration

Keycloak MUST authenticate users through Google and register a platform identity on first successful sign-in. Registration MUST NOT require administrator approval, an account allowlist, or a domain allowlist.

An ordinary registered account MUST be able to enter every protected project, including projects added after the account was registered. No per-project approval or membership grant is required for that entry. Business permissions are specified in AUTH-06.

Registration MUST NOT grant Keycloak administration, Kubernetes access, or application administrator privileges. Keycloak administration and Kubernetes administration MUST remain separate operator-controlled bootstrap paths.

The platform MUST use a stable identity derived from Keycloak's issuer and subject. An email address MUST NOT be treated as a permanent unique identifier or sufficient evidence for linking an existing account.

### AUTH-02: Shared browser sign-in

Each protected project MUST have a distinct OIDC client/audience configuration. Projects MUST recognize the same platform account across those configurations.

Given a valid Keycloak SSO session in a browser, visiting a second project MUST NOT require another Google authentication. A redirect through Keycloak is permitted; sharing one application cookie or one unrestricted token across all projects is not required.

Google OAuth client configuration, Keycloak broker credentials, redirect URIs, and project client credentials MUST be supplied through deployment configuration and Kubernetes Secrets where secret values are involved.

### AUTH-03: Go gateway

The gateway MUST implement the browser OIDC flow and Traefik-compatible external authentication checks. Keycloak, not the gateway, MUST implement Google identity brokering and act as the credential issuer.

The gateway MUST use the authorization-code flow with PKCE and validate state, nonce where applicable, callback parameters, and the expected issuer. Redirect destinations MUST be restricted to configured project routes.

The gateway MUST use standard-library cryptographic primitives, TLS, HTTP, and JSON functionality. It MUST NOT implement new cryptographic primitives or depend on OAuth2 Proxy, an external gateway framework, Redis, or a third-party Go authentication module for the initial runtime.

The gateway MUST check both credential validity and current session validity before permitting a protected request. Authentication errors, unavailable authentication dependencies, and invalid session state MUST fail closed.

The gateway MUST supply a Keycloak-issued access token intended for the target project. It MUST NOT forward a Google access token or an OIDC ID token as the project's API access credential. Obtaining the correct project audience MUST be part of the client/gateway integration.

The ingress/gateway boundary MUST remove or replace client-supplied identity headers. Session cookies MUST be Secure and HttpOnly, with appropriate SameSite behavior for the chosen OIDC flow. Tokens and client secrets MUST NOT be included in URLs or logged.

Session state MUST NOT rely exclusively on one gateway process's memory. Prefer Keycloak as the session authority and avoid an additional session service in the initial deployment. Exact browser cookie and session-check mechanics remain implementation-design details subject to AUTH-05.

### AUTH-04: Project-side verification

Projects MUST accept only requests the gateway has authenticated, and they MUST independently verify the forwarded credential. Successful verification establishes the platform identity in AUTH-06 and does not grant business permissions. Project verification MUST check:

- A signature using the configured Keycloak realm's published verification keys and an explicitly allowed algorithm.
- The expected issuer and target-project audience.
- Token expiration and other applicable validity timestamps.
- Required identity claims.

Projects MUST reject unsigned tokens, invalid signatures, expired credentials, unexpected issuers, and credentials targeted only at another project. Key rotation MUST be supported without distributing Keycloak's private signing key.

Public verification keys are not client secrets. Each confidential client MUST have its own credentials; projects MUST NOT share a central signing private key.

### AUTH-05: Current-session revocation

A user MUST be able to revoke the current platform session. Revocation MUST invalidate that session across protected projects while leaving other independent browser/device sessions active.

The platform MUST invalidate the relevant session and its ability to refresh credentials. After revocation completes, the old credential/session MUST be rejected on the next protected request, even if a previously issued token has not yet expired.

Signature verification alone is insufficient. The gateway MUST perform an online session/revocation check, and protected requests MUST NOT bypass that check. Any session cache MUST preserve the promised next-request rejection behavior.

This requirement does not revoke the user's Google account or prohibit a subsequent new sign-in. It does not retroactively cancel requests already in progress. The selected Keycloak APIs and behavior for terminating one SSO session and checking already-issued tokens MUST be demonstrated before this integration is accepted.

The user-facing revocation control MUST be a gateway route. It MUST authenticate the current platform session and MUST protect the browser request against cross-site request forgery. A request that does not present that session MUST NOT revoke anything. Public reachability is not anonymous permission to execute revocation.

The gateway's call to terminate the Keycloak session MUST be a private cluster-network request. It MUST use a credential authorized only for that session operation, distinct from the credential used to provision Keycloak clients. That call MUST NOT be exposed through public ingress.

### AUTH-06: Platform identity and project authorization

A valid gateway credential establishes platform identity only. That identity MUST be the pair of Keycloak issuer and subject. The credential MUST NOT itself grant business permissions.

A new identity MUST receive ordinary-user access automatically in every protected project that serves HTTP: entry to the application and its ordinary-user landing page. The landing page MUST NOT reveal other users' private data. A worker or scheduled job that does not serve HTTP has no landing page; its ordinary-role declaration still applies to any operation it exposes.

Each project MUST explicitly declare the ordinary role's business capabilities. A project whose declaration is missing MUST be rejected. A missing declaration MUST NOT be treated as granting every operation to any valid token. Undeclared operations MUST be denied.

The ordinary role MUST NOT include other users' private data or administrative operations, including when a project author writes those capabilities into the ordinary-role declaration. Access to the signed-in user's own data, and modification of shared project data, MUST be denied unless that project's declaration names those capabilities.

Application administrator rights MUST come from explicit operator provisioning of a project-scoped role binding to a verified issuer and subject. Administration MUST NOT be inferred from an email address, from being the first registered account, or from possession of a valid token. A binding MUST affect only the named identity and project.

The project MUST store and enforce these bindings. The gateway MUST forward issuer and subject only and MUST NOT mint a business-role claim. Satisfying this requirement MUST NOT add a platform authorization database, dashboard, or continuously running authorization controller.

## 5. Project management and deployment

### DEPLOY-01: Git is the desired-state source

Project declarations and environment-specific configuration MUST reside in GitHub. In the initial design, the operator reviews and applies manifests in ordered sequence; automated continuous reconciliation via FluxCD is deferred to future platform expansion.

A separate runtime management database, dashboard, imperative deployment service, or continuously running resource-allocation controller is not required for the initial platform.

The project configuration contract MUST cover:

- Project identifier and namespace.
- Workload types, images, commands/arguments when needed, and replica counts.
- HTTP services, ports, hostnames, and health checks when applicable.
- Keycloak client/audience configuration and gateway routing for protected HTTP endpoints.
- References to Kubernetes Secrets.
- PostgreSQL database/role requirements and persistent volumes when needed.
- CPU and memory requests/limits and project-level aggregate budgets.
- The ordinary-role declaration required by AUTH-06.

Adding a project MUST configure its deployment, protected routes, and identity client without changing the gateway's source code. Automated Keycloak client provisioning MUST be idempotent and MUST NOT overwrite operator-created secrets or grant administrator privileges to users. Operator-provisioned application role bindings are project data enforced by the application; client provisioning MUST NOT create them.

### DEPLOY-02: Images and reconciliation

GitHub Actions MUST build and publish application images to GHCR. Deployment references SHOULD use immutable image digests rather than mutable tags.

Private GHCR images MUST remain usable through namespace-scoped Kubernetes image-pull Secrets. Local `gh` authentication MUST NOT be treated as Kubernetes authentication. Production pull credentials SHOULD be limited to package read access rather than copying a broadly scoped personal CLI credential.

Application images MUST support the node architecture. The known Project PN images publish Linux ARM64 and AMD64 variants. Helm and Flux configuration MUST NOT assume that all future application images have both variants.

### DEPLOY-03: Environment compatibility

The same application deployment contract MUST work on native local k3s, VPS k3s, EKS, and GKE without changing application source code for the target environment.

Storage classes, ingress exposure, certificates, image-pull credentials, and database endpoints MUST be environment-specific configuration. Compatibility MUST NOT depend on deploying the same CNI plugin to every target.

The initial ingress integration uses Traefik and the Go gateway. Cloud load balancers MAY expose that ingress; provider-specific ingress authentication implementations are not required.

EKS/GKE deployment profiles MUST support an external PostgreSQL endpoint so RDS PostgreSQL or Cloud SQL can replace the self-hosted PostgreSQL StatefulSet without changing application code. The self-hosted PostgreSQL StatefulSet MUST remain a supported self-hosted option.

Supporting these environments means portable deployment, not one live database stretched across cloud providers or simultaneous active-active platform operation.

### DEPLOY-04: Deployment verification and future protected promotion

In the initial design, candidate manifests and resource budgets reside in Git. The operator manually verifies that candidate revisions comply with RESOURCE-02 and ISOLATE-01 controls against measured capacity before applying manifests to the cluster in ordered sequence.

For future automated GitOps expansion, GitHub Actions MUST run a required check named `platform-preflight` on the exact candidate revision. The check will run separately for each environment, using that environment's committed capacity, platform reservations, project count, calculated slices, and rendered workload resources. Rendering will use the same Helm and Kustomize versions and the same values Flux will apply for that environment.

The rendered aggregate MUST use peak concurrent resources: every container, rollout surge above steady-state replicas, Job parallelism, and CronJobs that the concurrency policy can run at the same time. Completed pods MUST NOT be counted. A zero-project revision MUST be accepted and MUST NOT divide by zero.

`platform-preflight` MUST reject a revision that violates RESOURCE-02 or that renders manifests weakening the ISOLATE-01 controls. It MUST also fail closed when the environment has no node-allocatable measurement, or when the revision's committed allocatable capacity is greater than the newest measurement for that environment. The measurement MUST be produced by observing that node. Setting the measurement equal to the desired committed figure is not an observation.

When automated GitOps is enabled in future phases, each environment will have a protected deployment branch tracked by Flux. Only a revision that passes `platform-preflight` for that environment MAY be promoted onto that branch. While operating under manual deployment ordering in the initial phase, the operator MUST NOT apply an over-budget revision to the cluster.

## 6. Resource allocation

### RESOURCE-01: Explicit bounded resources

Every application container MUST declare CPU and memory requests and limits. CPU use MUST have a maximum; unrestricted borrowing is not the selected policy. Memory consumption MUST have a maximum; equal scheduling weights MUST NOT be described as equal memory isolation.

CPU requests influence scheduling and CPU weight; CPU limits bound consumption. Memory requests influence scheduling; memory limits bound consumption and can cause OOM termination. These controls do not physically dedicate CPU cores or RAM to a project.

### RESOURCE-02: Project-level 1/n budgeting

The budget calculation MUST exclude capacity needed by the operating system, Kubernetes, and shared platform services. Node allocatable capacity MUST be used rather than raw advertised VM capacity, and reservations already excluded from allocatable MUST NOT be subtracted twice.

The model is:

```text
Application capacity = node allocatable capacity - shared platform allocation
Per-project budget = application capacity / deployed project count
```

All workloads, containers, and replicas belonging to one project MUST fit inside its aggregate project budget. A frontend and backend in one project MUST NOT count as two projects merely because they occupy separate pods.

The formula applies separately to CPU and to memory. The aggregate that MUST fit is the peak defined in DEPLOY-04. For each resource, the peak request total and the peak limit total MUST fit inside that resource's project slice. The platform MUST NOT schedule aggregate requested resources beyond available capacity or configure aggregate project memory limits above the application memory budget. Platform service budgets require measurement; this specification does not invent CPU or RAM amounts for Keycloak or PostgreSQL.

A zero-project deployment MUST still be valid and MUST NOT divide by zero. A project that cannot fit its workloads into its allocation MUST be rejected by `platform-preflight` rather than silently removing limits or leaving an over-budget revision for Flux to apply.

The deployed peak memory envelope is the peak concurrent memory-limit total of the revision currently on that environment's protected branch. Until section 12 item 1 is resolved, a workload present in both that revision and the candidate MUST keep a memory limit at least as high as the deployed limit. Removing a workload is allowed. Any other decrease in a project's peak memory-limit total MUST be rejected, including a replacement introduced under a new name with a smaller limit. The candidate MUST also be rejected when a project's recalculated memory slice is below the deployed peak of the workloads that candidate still contains. Adding a project is rejected when that comparison fails for any existing project. Restarting workloads MUST NOT bypass the rejection. CPU limit changes are outside this freeze. While the candidate is rejected, the protected branch stays unchanged.

Each project namespace MUST carry a platform-owned ResourceQuota for `requests.cpu`, `requests.memory`, `limits.cpu`, and `limits.memory`, set to that project's accepted slice. Because the promoted revision's peak already fits the slice, rollout surge included in that peak remains admissible. Quota is an admission backstop. An oversized Deployment can be stored while its pods are rejected, and lowering quota does not resize existing workloads, so quota MUST NOT substitute for rejecting the revision before promotion.

Recommended default: requests equal limits for strict slices. Whether requests may be lower than limits, and the future policy for applying a smaller slice to an existing project, remain open in section 12. Those open decisions MUST NOT be used to promote a shrinking revision.

## 7. PostgreSQL and persistence

### DATA-01: Standard PostgreSQL StatefulSet

The initial deployment MUST use one standard PostgreSQL instance (deployed via a Kubernetes StatefulSet) backed by persistent storage. A complex operator (such as CNPG), PostgreSQL replicas, and multi-node clustering are not initial requirements.

Keycloak and each database-using project MUST have separate databases and restricted roles. Project roles MUST NOT access Keycloak data, another project's database, or PostgreSQL administration capabilities. Cross-database access MUST be restricted explicitly; creating separate databases alone is insufficient.

Applications MUST connect using configurable endpoints and Secret references rather than depending on internal container implementation details. PostgreSQL MUST be operated as shared platform infrastructure, not installed separately by every application chart.

### DATA-02: VPS persistence

PostgreSQL data MUST be mounted through a PVC onto VPS-backed storage. The initial k3s storage profile SHOULD use local-path provisioning. Mounting an attached block disk into the configured storage location MAY be used without coupling application charts to OCI APIs.

Data MUST survive ordinary pod replacement. Persistent volumes MUST NOT be confused with backups or protection against disk/host loss. Database and PVC deletion/reclaim behavior MUST be explicit and protected against unintended Flux pruning or application uninstallation.

Project files requiring persistence MUST use their own configured volumes. A disk-backed volume on one node does not automatically follow a workload to another node.

## 8. Optional backups and future expansion

### BACKUP-01: Optional provider-independent backup

Backup enablement MUST be explicit and optional. With backups disabled, the platform MUST NOT claim disaster-recovery protection.

When enabled, backups MUST run every day at 00:00 UTC and retain restorable recovery data for seven days. Backups SHOULD be delivered to an independently operated Linux host or NAS over SSH/SFTP, without requiring a particular third-party object-storage service.

The backup destination, credentials, encryption, and restore process MUST be configured before enabling this feature. Copying a running PostgreSQL data directory without a PostgreSQL-consistent backup procedure MUST NOT be used.

A provider-independent logical backup path MUST include each required database and the roles needed to restore it. Logical backups do not provide recovery between backup times or a single atomic snapshot across separate databases. A scheduled CronJob executes the backup process, uploading database dumps to the configured destination.

Physical backups or continuous WAL archiving MAY be chosen later, but a usable backup chain MUST remain complete throughout retention. The backup format is not selected by this specification. Any enabled backup mode MUST pass an actual restore exercise.

### EXPAND-01: Simple now, expandable later

The initial deployment MUST remain colocated and MUST NOT require an HA topology, Vault, a dedicated PostgreSQL node, separate identity infrastructure, or an in-cluster automated GitOps reconciliation controller (FluxCD).

Configuration SHOULD permit later additional application/Keycloak replicas, independent PostgreSQL standby nodes, and the introduction of automated GitOps reconciliation via FluxCD and `platform-preflight`. This is a migration path, not an initial availability guarantee. Single-host volumes may need migration, and applications may need appropriate connection recovery behavior when HA is introduced.

There is no current downtime SLO. Future HA can reduce downtime; the platform MUST NOT promise that scaling replicas eliminates every outage.

## 9. Secrets and administrative access

Secret values MUST NOT be committed as plaintext to GitHub, placed in Helm values stored in Git, printed in logs, or supplied in chat.

The operator MUST provide Kubernetes Secrets for Google broker credentials, Keycloak client-provisioning credentials, the gateway's session-termination credential, database credentials, GHCR pulls, and optional backup access. The session-termination credential MUST be limited to that operation and MUST be distinct from the client-provisioning credential. Workloads and Flux-managed configuration MUST reference those Secrets by name.

Flux MUST NOT prune or overwrite externally supplied Secret contents. Development and production MUST use separate secret material and identity client configurations.

Kubernetes Secret base64 encoding MUST NOT be treated as encryption. Cluster access MUST be constrained by RBAC and encryption at rest configured for production. An independently secured recovery copy is recommended; Git alone cannot recreate manually supplied Secrets.

Vault integration MAY be introduced later behind the existing Secret-reference contract. OCI Vault is not a required dependency.

## 10. Known image evidence and infrastructure constraints

### 10.1 Project PN image metadata

Authenticated GHCR inspection confirmed these tags and OCI indexes:

| Image | Index digest | Published application platforms |
| --- | --- | --- |
| `ghcr.io/nacfson/project-pn-backend:a7c3213` | `sha256:d3ab8644467bbbb2a49b0afc6631b2600ce344908ce15ddf99e8478c11ecaf21` | Linux AMD64 and ARM64 |
| `ghcr.io/nacfson/project-pn-frontend:a7c3213` | `sha256:bc70cb06625c439d911a05d97febb7275b642ab7386022625302f1e224a696ce` | Linux AMD64 and ARM64 |

The ARM64 backend config declares command `./api`, working directory `/app`, port `8080/tcp`, and an `APP_ADDR` environment-variable name. The frontend config declares Nginx through `/docker-entrypoint.sh`, port `80/tcp`, and no application-specific environment-variable names in the inspected defaults. Neither inspected config declares volumes.

These are metadata observations, not startup verification. They do not establish required database variables, health endpoints, frontend API routing, browser-baked configuration, storage requirements, existing authentication support, or compatibility with the Restricted profile. Application layers were not downloaded or executed during specification drafting.

The frontend image listens on port `80` and starts through Nginx's entrypoint. Restricted permits a non-root user and at most the `NET_BIND_SERVICE` capability. If either supplied image cannot run under ISOLATE-01, that image MUST be adapted. Project-namespace enforcement MUST NOT be relaxed to admit it.

Treat the frontend/backend pair as one candidate Project PN integration, not evidence of two independent projects. The second project for cross-project SSO remains deferred and MUST NOT block architecture planning.

### 10.2 Oracle Always Free

The current official Oracle documentation lists an aggregate A1 allowance equivalent to 2 OCPUs and 12 GB RAM, and 200 GB combined boot/block storage. AMD micro instances have 1 GB RAM each. These are published allowance figures, not measurements of an already provisioned VM or guaranteed capacity availability.

The deployment MUST discover actual node architecture and allocatable resources. It MUST NOT hard-code an assumed free-tier size into portable application charts. `platform-preflight` MUST compare the revision's committed allocatable capacity with a measurement taken from that node, as required by DEPLOY-04.

Oracle documents capacity shortages and possible reclamation of idle free instances. Optional backups and a single VPS mean the initial platform may lose availability and data after host/disk loss. No backup durability or production HA claim is made.

## 11. Acceptance scenarios

### AC-01: First sign-in

Given a Google account with no platform account, signing into a protected project creates an ordinary Keycloak identity and opens that project's ordinary-user landing page without administrator approval. The account receives no infrastructure administrator rights, no application administrator rights, and no access to other users' private data merely by registering.

### AC-02: Cross-project SSO

Given two independent protected projects and a valid browser SSO session established through the first, entering the second recognizes the same issuer/subject without another Google authentication. A frontend/backend pair in one project is not sufficient evidence for this scenario.

### AC-03: Authentication enforcement

Unauthenticated requests cannot enter protected application routes. Forged identity headers do not impersonate a user. Failure or unavailability of the gateway/session authority denies access. No exposed alternate route bypasses the authentication check.

### AC-04: Project token verification

A target-project access token with valid identity and signature is accepted. Invalid signatures, wrong issuer, wrong project audience, expired tokens, and ID tokens substituted for API access credentials are rejected. Key rotation is exercised.

### AC-05: Current-session revocation

Create two independent sessions for one account. Revoke the current session through the authenticated gateway route and demonstrate that its next protected request to either project is rejected, including with a previously issued unexpired access token. The other session remains usable. Refresh cannot resurrect the revoked session; a deliberate new sign-in is allowed. A cross-site request, and a request without the current session, do not revoke anything.

### AC-06: Resource bounds

For one environment, demonstrate that `platform-preflight` accepts a revision whose peak aggregate fits the project slices and that only the promoted revision is reconciled by Flux. Demonstrate that an over-budget revision is not promoted and that the protected branch is unchanged. Demonstrate that a zero-project revision is accepted and does not divide by zero.

While section 12 item 1 remains unresolved, demonstrate that a revision which lowers an existing workload's memory limit is rejected, and that a revision which adds a project so an existing slice falls below its deployed peak memory envelope is rejected. In both cases the protected branch is unchanged.

Exercise CPU load and memory exhaustion in an isolated environment to observe configured enforcement. Those load tests MUST stay off a shared production node.

### AC-07: Persistence and isolation

Replace the PostgreSQL pod and verify retained data. Demonstrate that one project role cannot access Keycloak or another project's database. Confirm that ordinary application removal does not unexpectedly delete the shared database/PVC.

### AC-08: Private image deployment

Use an operator-provided namespace image-pull Secret to deploy both supplied ARM64 image variants. Confirm actual startup and serving behavior after supplying documented application configuration. Missing or insufficient registry credentials must remain observable, not be bypassed by making packages public.

### AC-09: Deployment delivery and future GitOps

Build/publish an immutable image, update the Git deployment reference, and manually apply manifests in order (Initial). Verify that manually supplied Secrets are not committed, overwritten, or pruned, and that redeploying the same desired state is idempotent. For future automated GitOps: observe Flux reconcile the promoted version from the protected branch after passing `platform-preflight`.

### AC-10: Optional backup

With backup disabled, no backup job or storage credentials are required. With it enabled, verify the UTC schedule, retention, failed-backup visibility, and recovery of database contents and required roles on a clean target. Merely producing a file is insufficient proof.

### AC-11: Deployment target compatibility

Exercise the same application/authentication contract on local k3s and VPS k3s, then on EKS and GKE using target-specific ingress exposure, storage, credentials, and database settings. Each target remains unverified until exercised; rendered manifests alone are not cloud deployment proof. Each target's `platform-preflight` uses that target's measured allocatable capacity.

### AC-12: Ordinary authorization

Using an application that enforces AUTH-06, demonstrate ordinary-user entry to the landing page. Demonstrate that undeclared operations, administrative operations, and other users' private data are denied. Demonstrate that an operator-provisioned administrator binding grants administrative operations only to the named issuer and subject.

A second protected project is required to demonstrate that the same binding grants no administration there. That half waits on section 12 item 3. The single-project checks do not. A gateway that forwards a valid token is not sufficient evidence for this scenario.

### AC-13: Workload and Keycloak boundary

Demonstrate that a project namespace rejects a privileged pod, a pod using hostPath or a host namespace, a pod running as root, a pod mounting a service-account token, and a project Service that is not ClusterIP. Demonstrate that the project reconciliation identity cannot remove Restricted enforcement, ResourceQuota, or NetworkPolicy.

Demonstrate that public ingress does not serve Keycloak `/admin/`, the administrative realm, the account console, health, or metrics. Demonstrate that the revocation route does nothing without the current session.

## 12. Unresolved details and implementation prerequisites

These items are not reasons to ask for project images again or to delay the architecture specification. They are details to resolve before their affected implementation paths are accepted:

1. **Resource allocation lifecycle:** whether requests equal limits, and the future policy for applying a smaller slice to an existing project, including an application whose minimum memory cannot fit that slice. Until that policy is accepted, DEPLOY-04 rejects memory-limit shrinks and project additions that would put an existing project's memory slice below its deployed peak memory envelope. Continuous live resizing is not selected.
2. **Application runtime contract:** Project PN's required configuration, database schema/migrations, health checks, frontend API routing, persistent files, token-verification integration, ordinary-role declaration, and where that project stores AUTH-06 role bindings. AC-12 waits on an application that enforces those rules. Metadata inspection does not establish Restricted-profile compatibility.
3. **Second independent project:** supplied later for AC-02, AC-05, and the cross-project half of AC-12; not an architecture prerequisite.
4. **Deployment inputs:** actual hostnames, Google OAuth registration, Secret names/contents, GitHub repository layout, OCI VM details, and selected k3s/Traefik/Keycloak/PostgreSQL/Flux versions. Actual node capacity must be measured before `platform-preflight` can pass.
5. **Current-session enforcement mechanics:** browser cookie boundaries, per-project token acquisition/audience handling, and Keycloak session termination/introspection behavior satisfying AC-05 without third-party gateway modules. AUTH-05 already specifies the public revocation route, cross-site request protection, and the private session-termination call.
6. **Optional backup configuration:** backup format and destination are required only when backups are enabled.

No application code, Helm chart, controller, cluster, or secret is implemented by this document.

## 13. References

- [Traefik ForwardAuth](https://doc.traefik.io/traefik/reference/routing-configuration/http/middlewares/forwardauth/)
- [Keycloak OIDC endpoints](https://www.keycloak.org/securing-apps/oidc-layers)
- [Keycloak exposed paths](https://www.keycloak.org/server/reverseproxy#_exposed_path_recommendations)
- [PostgreSQL Documentation](https://www.postgresql.org/docs/16/)
- [Kubernetes StatefulSet](https://kubernetes.io/docs/concepts/workloads/controllers/statefulset/)
- [K3s persistent storage](https://docs.k3s.io/storage/)
- [Kubernetes resource requests and limits](https://kubernetes.io/docs/concepts/configuration/manage-resources-containers/)
- [Kubernetes Restricted Pod Security](https://kubernetes.io/docs/concepts/security/pod-security-standards/#restricted)
- [Kubernetes resource quotas](https://kubernetes.io/docs/concepts/policy/resource-quotas/#how-kubernetes-resourcequotas-work)
- [Flux GitHub bootstrap](https://fluxcd.io/flux/installation/bootstrap/github/)
- [Flux GitRepository](https://fluxcd.io/flux/components/source/gitrepositories/)
- [GHCR authentication](https://docs.github.com/packages/working-with-a-github-packages-registry/working-with-the-container-registry)
- [Oracle Always Free resources](https://docs.oracle.com/en-us/iaas/Content/FreeTier/freetier_topic-Always_Free_Resources.htm)
