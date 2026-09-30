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
| Deployment reconciliation | FluxCD reading GitHub repositories |
| Application image delivery | GitHub Actions and GHCR; immutable deployment references |
| Ingress | Traefik; Cilium is not part of the initial design |
| Authentication gateway | Custom Go service using the standard library, without third-party Go modules at runtime |
| Identity provider | Keycloak with Google identity brokering |
| Registration | Any Google account may register; no administrator approval |
| Project access | Every registered account can access every protected project |
| Project credential verification | Projects independently verify the credential forwarded by the gateway |
| Database | PostgreSQL operated by CloudNativePG (CNPG) |
| Database isolation | One initial PostgreSQL instance, separate databases and restricted roles |
| Persistent storage | Disk-backed persistent volumes on the VPS |
| Secrets | Operator-provided Kubernetes Secrets; no initial Vault deployment |
| Resource policy | Explicit CPU requests and limits, bounded memory, with a per-project 1/n allocation model |
| User-requested revocation | Current platform session only |
| Backups | Optional; when enabled, daily at 00:00 UTC with seven-day retention |

Keycloak, PostgreSQL, Traefik, the gateway, Flux, and project workloads initially reside on the same VPS. Separate infrastructure hosts, a second production server, and HA replicas are not initial requirements.

## 3. Traffic and trust boundaries

```text
Browser --> project.example.com --> Traefik --> Project workload
                                      |
                                      +--> Go gateway: authentication check
                                                   |
                                                Keycloak --> PostgreSQL
                                                   |
                                                 Google

GitHub configuration --> FluxCD --> Helm / Kubernetes resources
GitHub Actions --> GHCR --> Kubernetes image pulls
```

The authentication check MUST happen before a protected request reaches its project. The login flow redirects the browser through Keycloak and Google; the diagram does not imply that Google is contacted for every application request.

The existing hostname pattern MUST be preserved: a central identity hostname such as `auth.example.com` and separate project subdomains. Exact domain names are operator configuration, not embedded platform constants.

Keycloak's login and callback routes MUST remain reachable without an already authenticated platform session. Protected workloads MUST NOT have an alternative externally accessible route that bypasses the gateway. Workload traffic isolation MUST be enforced using network policy, not an assumption that ClusterIP services are inherently inaccessible to other pods.

## 4. Identity and authentication requirements

### AUTH-01: Automatic Google registration

Keycloak MUST authenticate users through Google and register a platform identity on first successful sign-in. Registration MUST NOT require administrator approval, an account allowlist, or a domain allowlist.

An ordinary registered account MUST be able to enter every protected project, including projects added after the account was registered. No per-project approval or membership grant is required.

Registration MUST NOT grant Keycloak administration, Kubernetes access, or application administrator privileges. A project MAY distinguish business permissions such as read, edit, or administer without introducing separate project registration.

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

Projects MUST trust the gateway's authentication decision while independently verifying the forwarded credential. Project verification MUST check:

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

## 5. Project management and deployment

### DEPLOY-01: Git is the desired-state source

Project declarations and environment-specific configuration MUST reside in GitHub. FluxCD MUST reconcile Helm releases and Kubernetes resources from that desired state.

A separate runtime management database, dashboard, imperative deployment service, or continuously running resource-allocation controller is not required for the initial platform.

The project configuration contract MUST cover:

- Project identifier and namespace.
- Workload types, images, commands/arguments when needed, and replica counts.
- HTTP services, ports, hostnames, and health checks when applicable.
- Keycloak client/audience configuration and gateway routing for protected HTTP endpoints.
- References to Kubernetes Secrets.
- PostgreSQL database/role requirements and persistent volumes when needed.
- CPU and memory requests/limits and project-level aggregate budgets.

Adding a project MUST configure its deployment, protected routes, and identity client without changing the gateway's source code. Automated Keycloak client provisioning MUST be idempotent and MUST NOT overwrite operator-created secrets or grant administrator privileges to users.

### DEPLOY-02: Images and reconciliation

GitHub Actions MUST build and publish application images to GHCR. Deployment references SHOULD use immutable image digests rather than mutable tags.

Private GHCR images MUST remain usable through namespace-scoped Kubernetes image-pull Secrets. Local `gh` authentication MUST NOT be treated as Kubernetes authentication. Production pull credentials SHOULD be limited to package read access rather than copying a broadly scoped personal CLI credential.

Application images MUST support the node architecture. The known Project PN images publish Linux ARM64 and AMD64 variants. Helm and Flux configuration MUST NOT assume that all future application images have both variants.

### DEPLOY-03: Environment compatibility

The same application deployment contract MUST work on native local k3s, VPS k3s, EKS, and GKE without changing application source code for the target environment.

Storage classes, ingress exposure, certificates, image-pull credentials, and database endpoints MUST be environment-specific configuration. Compatibility MUST NOT depend on deploying the same CNI plugin to every target.

The initial ingress integration uses Traefik and the Go gateway. Cloud load balancers MAY expose that ingress; provider-specific ingress authentication implementations are not required.

EKS/GKE deployment profiles MUST support an external PostgreSQL endpoint so RDS PostgreSQL or Cloud SQL can replace CNPG-managed PostgreSQL without changing application code. CNPG-managed PostgreSQL MUST remain a supported self-hosted option.

Supporting these environments means portable deployment, not one live database stretched across cloud providers or simultaneous active-active platform operation.

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

The platform MUST NOT schedule aggregate requested resources beyond available capacity or deliberately configure aggregate project memory limits above the application memory budget. Platform service budgets require measurement; this specification does not invent CPU or RAM amounts for Keycloak or PostgreSQL.

A zero-project deployment MUST still be valid and MUST NOT divide by zero. A project that cannot fit its workloads into its allocation MUST be reported as a capacity conflict rather than silently removing limits.

Recommended default: requests equal limits for strict slices. Whether requests may be lower than limits, how the deployed project count is determined during changes, and when reallocations are applied remain open decisions. Adding/removing a project MUST NOT silently hot-shrink running memory limits before that lifecycle policy is agreed.

## 7. PostgreSQL and persistence

### DATA-01: CNPG-managed PostgreSQL

The initial deployment MUST use one CNPG-managed PostgreSQL instance backed by persistent storage. PostgreSQL replicas and a multi-node CNPG topology are not initial requirements.

Keycloak and each database-using project MUST have separate databases and restricted roles. Project roles MUST NOT access Keycloak data, another project's database, or PostgreSQL administration capabilities. Cross-database access MUST be restricted explicitly; creating separate databases alone is insufficient.

Applications MUST connect using configurable endpoints and Secret references rather than depending on CNPG's internal implementation. CNPG MUST be operated as shared infrastructure, not installed separately by every application chart.

### DATA-02: VPS persistence

PostgreSQL data MUST be mounted through a PVC onto VPS-backed storage. The initial k3s storage profile SHOULD use local-path provisioning. Mounting an attached block disk into the configured storage location MAY be used without coupling application charts to OCI APIs.

Data MUST survive ordinary pod replacement. Persistent volumes MUST NOT be confused with backups or protection against disk/host loss. Database and PVC deletion/reclaim behavior MUST be explicit and protected against unintended Flux pruning or application uninstallation.

Project files requiring persistence MUST use their own configured volumes. A disk-backed volume on one node does not automatically follow a workload to another node.

## 8. Optional backups and future expansion

### BACKUP-01: Optional provider-independent backup

Backup enablement MUST be explicit and optional. With backups disabled, the platform MUST NOT claim disaster-recovery protection.

When enabled, backups MUST run every day at 00:00 UTC and retain restorable recovery data for seven days. Backups SHOULD be delivered to an independently operated Linux host or NAS over SSH/SFTP, without requiring a particular third-party object-storage service.

The backup destination, credentials, encryption, and restore process MUST be configured before enabling this feature. Copying a running PostgreSQL data directory without a PostgreSQL-consistent backup procedure MUST NOT be used.

A provider-independent logical backup path MUST include each required database and the roles needed to restore it. Logical backups do not provide recovery between backup times or a single atomic snapshot across separate databases. SSH delivery would require its own backup job; it is not CNPG's object-store backup integration.

Physical backups or continuous WAL archiving MAY be chosen later, but a usable backup chain MUST remain complete throughout retention. The backup format is not selected by this specification. Any enabled backup mode MUST pass an actual restore exercise.

### EXPAND-01: Simple now, expandable later

The initial deployment MUST remain colocated and MUST NOT require an HA topology, Vault, a dedicated PostgreSQL node, or separate identity infrastructure.

Configuration SHOULD permit later additional application/Keycloak replicas and independent PostgreSQL standby nodes. This is a migration path, not an initial availability guarantee. Single-host volumes may need migration, and applications may need appropriate connection recovery behavior when HA is introduced.

There is no current downtime SLO. Future HA can reduce downtime; the platform MUST NOT promise that scaling replicas eliminates every outage.

## 9. Secrets and administrative access

Secret values MUST NOT be committed as plaintext to GitHub, placed in Helm values stored in Git, printed in logs, or supplied in chat.

The operator MUST provide Kubernetes Secrets for Google broker credentials, Keycloak administration/client credentials, database credentials, GHCR pulls, and optional backup access. Workloads and Flux-managed configuration MUST reference those Secrets by name.

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

These are metadata observations, not startup verification. They do not establish required database variables, health endpoints, frontend API routing, browser-baked configuration, storage requirements, or existing authentication support. Application layers were not downloaded or executed during specification drafting.

Treat the frontend/backend pair as one candidate Project PN integration, not evidence of two independent projects. The second project for cross-project SSO remains deferred and MUST NOT block architecture planning.

### 10.2 Oracle Always Free

The current official Oracle documentation lists an aggregate A1 allowance equivalent to 2 OCPUs and 12 GB RAM, and 200 GB combined boot/block storage. AMD micro instances have 1 GB RAM each. These are published allowance figures, not measurements of an already provisioned VM or guaranteed capacity availability.

The deployment MUST discover actual node architecture and allocatable resources. It MUST NOT hard-code an assumed free-tier size into portable application charts.

Oracle documents capacity shortages and possible reclamation of idle free instances. Optional backups and a single VPS mean the initial platform may lose availability and data after host/disk loss. No backup durability or production HA claim is made.

## 11. Acceptance scenarios

### AC-01: First sign-in

Given a Google account with no platform account, signing into a protected project creates an ordinary Keycloak identity and permits entry without administrator approval. The account receives no infrastructure or application administrator rights merely by registering.

### AC-02: Cross-project SSO

Given two independent protected projects and a valid browser SSO session established through the first, entering the second recognizes the same issuer/subject without another Google authentication. A frontend/backend pair in one project is not sufficient evidence for this scenario.

### AC-03: Authentication enforcement

Unauthenticated requests cannot enter protected application routes. Forged identity headers do not impersonate a user. Failure or unavailability of the gateway/session authority denies access. No exposed alternate route bypasses the authentication check.

### AC-04: Project token verification

A target-project access token with valid identity and signature is accepted. Invalid signatures, wrong issuer, wrong project audience, expired tokens, and ID tokens substituted for API access credentials are rejected. Key rotation is exercised.

### AC-05: Current-session revocation

Create two independent sessions for one account. Revoke the current session and demonstrate that its next protected request to either project is rejected, including with a previously issued unexpired access token. The other session remains usable. Refresh cannot resurrect the revoked session; a deliberate new sign-in is allowed.

### AC-06: Resource bounds

Deploy a multi-workload project and verify that aggregate requests/limits fit its allocation. Exercise CPU load and memory exhaustion in an isolated environment to observe configured enforcement. Reject configurations that exceed available project capacity, and handle zero deployed projects safely. No unsafe automatic resizing behavior is implied by these checks.

### AC-07: Persistence and isolation

Replace the PostgreSQL pod and verify retained data. Demonstrate that one project role cannot access Keycloak or another project's database. Confirm that ordinary application removal does not unexpectedly delete the shared database/PVC.

### AC-08: Private image deployment

Use an operator-provided namespace image-pull Secret to deploy both supplied ARM64 image variants. Confirm actual startup and serving behavior after supplying documented application configuration. Missing or insufficient registry credentials must remain observable, not be bypassed by making packages public.

### AC-09: GitOps delivery

Build/publish an immutable image, update the Git deployment reference, and observe Flux reconcile the intended version. Demonstrate that manually supplied Secrets are not committed, overwritten, or pruned. Redeploying the same desired state is idempotent.

### AC-10: Optional backup

With backup disabled, no backup job or storage credentials are required. With it enabled, verify the UTC schedule, retention, failed-backup visibility, and recovery of database contents and required roles on a clean target. Merely producing a file is insufficient proof.

### AC-11: Deployment target compatibility

Exercise the same application/authentication contract on local k3s and VPS k3s, then on EKS and GKE using target-specific ingress exposure, storage, credentials, and database settings. Each target remains unverified until exercised; rendered manifests alone are not cloud deployment proof.

## 12. Unresolved details and implementation prerequisites

These items are not reasons to ask for project images again or to delay the architecture specification. They are details to resolve before their affected implementation paths are accepted:

1. **Resource allocation lifecycle:** whether requests equal limits; project-count semantics during deployment changes; when recalculated budgets are applied; and how to handle an application's minimum memory exceeding its slice. Continuous live resizing is not selected.
2. **Application runtime contract:** Project PN's required configuration, database schema/migrations, health checks, frontend API routing, persistent files, and token-verification integration. Metadata inspection alone does not resolve these.
3. **Second independent project:** supplied later for AC-02 and AC-05; not an architecture prerequisite.
4. **Deployment inputs:** actual hostnames, Google OAuth registration, Secret names/contents, GitHub repository layout, OCI VM details, and selected k3s/Traefik/Keycloak/CNPG/Flux versions. Actual node capacity must be measured.
5. **Current-session enforcement mechanics:** browser cookie boundaries, per-project token acquisition/audience handling, and Keycloak session termination/introspection behavior satisfying AC-05 without third-party gateway modules.
6. **Optional backup configuration:** backup format and destination are required only when backups are enabled.

No application code, Helm chart, controller, cluster, or secret is implemented by this document.

## 13. References

- [Traefik ForwardAuth](https://doc.traefik.io/traefik/reference/routing-configuration/http/middlewares/forwardauth/)
- [Keycloak OIDC endpoints](https://www.keycloak.org/securing-apps/oidc-layers)
- [CloudNativePG architecture](https://cloudnative-pg.io/docs/1.30/architecture/)
- [CloudNativePG backups](https://cloudnative-pg.io/docs/1.30/backup/)
- [K3s persistent storage](https://docs.k3s.io/storage/)
- [Kubernetes resource requests and limits](https://kubernetes.io/docs/concepts/configuration/manage-resources-containers/)
- [Flux GitHub bootstrap](https://fluxcd.io/flux/installation/bootstrap/github/)
- [GHCR authentication](https://docs.github.com/packages/working-with-a-github-packages-registry/working-with-the-container-registry)
- [Oracle Always Free resources](https://docs.oracle.com/en-us/iaas/Content/FreeTier/freetier_topic-Always_Free_Resources.htm)
