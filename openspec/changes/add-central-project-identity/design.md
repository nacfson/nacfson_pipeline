# Design

## Context

See `proposal.md` for motivation. The current ProcessManager implementation is a Go and HTMX application that constructs a direct Kubernetes client and creates Deployments, Services, and Ingresses imperatively. Its project form stores a Boolean `authEnabled` value through an HTTP client to the custom auth service. The manager authenticates through `internal/auth.Middleware`, which validates the custom session through `HTTPAuthStore`; `cmd/auth`, password/session handlers, login and registration templates, bootstrap-password tooling, and PostgreSQL account tables remain in the repository.

The target architecture in `ARCHITECTURE.md` changes both ownership boundaries: ProcessManager writes desired project configuration to Git for Flux to reconcile, and Keycloak plus per-project oauth2-proxy instances own authentication. The target remains a single k3s control-plane/worker node, so Keycloak and PostgreSQL are recoverable but not highly available.

The OpenSpec repository currently contains architecture and planning material only. The implementation source is in the separate ProcessManager repository, while new Flux, Helm, Keycloak, and CNPG configuration must be established as the deployment repository described by the architecture.

## Goals / Non-Goals

**Goals:**

- Make Keycloak the sole platform account, session, authorization-group, and token authority for a centrally administered population planned for at least 100,000 personal accounts, with Google as the sole initial upstream credential authority for ordinary users.
- Prevent every component except Keycloak from receiving identity-database credentials or network access.
- Convert the current Boolean authentication flag into an explicit, validated access-policy contract.
- Make protected-project resources deterministic GitOps output rather than imperative Kubernetes mutations.
- Preserve a stable user identity contract through OIDC issuer and subject claims.
- Provide independent client credentials and authorization policy for each protected project.
- Support repeatable backup, point-in-time recovery, migration, and rollback.
- Prove the Keycloak-controlled identity path against at least 100,000 synthetic broker-linked identities at 5 login completions per second, 50 token refreshes per second, and 5,000 concurrent sessions with p95 platform authentication latency below 500 ms, excluding Google-controlled browser latency.

**Non-Goals:**

- Building a custom OAuth/OIDC server or retaining the custom Go password/session service as a production fallback.
- Letting ProcessManager create accounts, assign groups, register Keycloak clients, verify credentials, or validate application sessions.
- Sharing Keycloak tables or database credentials with projects.
- Providing multi-node Kubernetes, PostgreSQL, or Keycloak high availability.
- Automating general per-project application database provisioning in this change.
- Introducing VPN-only project exposure, a service mesh, or a second deployment controller.

## Decisions

### 1. Keycloak is the identity authority

Keycloak runs in `identity-system` and exposes the `platform` realm at `https://auth.<domain>/realms/platform`. The realm contains the centrally administered personal-account population, while ProcessManager and each OIDC-enabled project use distinct clients.

A mature broker and identity authority is chosen over extending `cmd/auth` because external-provider integration, account linking, recovery administration, signing-key rotation, OIDC conformance, session management, and token validation are security-sensitive protocol concerns. Google verifies ordinary-user credentials and Keycloak establishes the platform identity and session. The legacy service is removed from the production path after migration rather than retained as a fallback.

Ordinary personal accounts authenticate only through a Google OpenID Connect identity broker configured in Keycloak. Any valid Google account may complete the first-broker-login flow and create a fresh platform identity, but that identity receives no project group or administrator role automatically. Local self-registration, password authentication, and password recovery are disabled for ordinary users. Projects remain coupled only to the platform Keycloak issuer, so the upstream provider can change without modifying every project's trust configuration.

The Google client credential is SOPS-encrypted and mounted only into Keycloak configuration. The broker link uses Google's issuer and subject rather than email as the stable upstream identity. Day-to-day administrators use the same Google primary authentication, receive administrator roles only through explicit assignment, and must complete a Keycloak role-conditional TOTP step-up. One restricted local recovery administrator uses a local password plus TOTP only when the normal Google-backed path cannot be used.

Google brokering requires Keycloak to resolve DNS and make outbound HTTPS connections to Google's OIDC endpoints. Standard Kubernetes `NetworkPolicy` cannot select an external service by DNS name, so the initial single-node deployment permits the required Keycloak TCP 443 egress and constrains or observes it at the host firewall, provider firewall, or a later egress proxy where practical. This exception does not permit any additional client to reach the identity database.

**Alternative considered:** Retain local Keycloak passwords for ordinary personal accounts alongside Google. Rejected for the initial release because parallel user credential and recovery paths increase account-takeover surface and operational responsibility without a stated requirement.

**Alternative considered:** Use local Keycloak passwords plus TOTP for all administrators. Rejected because routine local credentials add password lifecycle and attack surface; the restricted recovery administrator provides Google-independent recovery without making local passwords the normal path.

**Alternative considered:** Continue evolving the Go auth service. Rejected because it would reproduce identity-provider responsibilities and preserve non-standard session contracts.

### 2. One realm, one client per trust boundary

One `platform` realm provides SSO and a stable issuer. Each project has a separate client, exact redirect URIs, client secret where applicable, audience, allowed group, and logout behavior. Group names are project-scoped, such as `project:<id>:users`; the ProcessManager administrator group remains separate. Google-backed day-to-day administrators require a role-conditional Keycloak TOTP step-up, while the local password-plus-TOTP recovery administrator is restricted to recovery.

**Alternative considered:** One realm per project. Rejected because the platform has one owner and shared accounts; realm proliferation would duplicate users, policy, keys, and operational work without a tenant-isolation requirement.

**Alternative considered:** One shared OAuth client for all proxies. Rejected because one leaked secret or callback mistake would affect every protected application.

### 3. CloudNativePG owns a dedicated Keycloak database

The CNPG operator runs in `cnpg-system`; a one-instance PostgreSQL cluster runs in `identity-system`. It uses TLS, `local-path-retain`, a Keycloak-only owner credential, scheduled base backups, continuous WAL archiving, encrypted off-server object storage, and tested point-in-time recovery.

NetworkPolicy allows TCP 5432 only from Keycloak pods to the CNPG read-write Service. No ingress route exposes PostgreSQL. The CNPG operator receives only its required Kubernetes RBAC. A second database pod on the same node is omitted because it consumes resources without surviving the node failure domain.

The initial production release runs one Keycloak replica. Kubernetes restarts that pod after a process failure, but login and refresh operations fail closed during the interruption. Running additional Keycloak replicas on the same node is rejected because they share the node, network, storage, control-plane, and power failure domains while consuming resources needed by applications and CI. Replica count is reconsidered only with independent schedulable nodes or measured throughput that cannot be met by resizing the single node.

Account population does not determine replica count by itself. Before production acceptance, a test-only harness that is unavailable in the production realm loads at least 100,000 synthetic broker-linked personal identities and exercises Keycloak-controlled login completion, refresh, logout, 5,000 concurrent sessions, and paginated administrator search at 5 login completions and 50 token refreshes per second. Those platform-controlled operations must maintain p95 latency below 500 ms; time spent in Google's browser and identity-provider systems is measured separately and is not attributed to Keycloak capacity.

**Alternative considered:** Run three Keycloak replicas on the single node. Rejected because this provides no node-level availability and adds JVM memory, cache, and scheduling overhead without removing the database or host failure domain.

**Alternative considered:** Reuse the existing shared application PostgreSQL server. Rejected because identity restoration, credentials, schema lifecycle, and blast radius must remain independent from project data.

### 4. Project access is an explicit tagged policy

Replace `authEnabled bool` with a tagged configuration:

```yaml
ingress:
  accessPolicy: public | oidc-protected | oidc-native
  hostname: project.example.com
  oidc:
    clientId: project-id
    secretName: project-id-oidc
    callbackURI: https://project.example.com/oauth2/callback
    allowedGroup: project:project-id:users
```

Policy-aware validation rejects fields that are missing, unsupported, contradictory, non-DNS-safe, non-HTTPS, wildcarded, or cross-project. The normalized configuration is rendered deterministically into the project's Flux `HelmRelease` values.

- `public`: direct ingress-to-application routing and no platform OIDC resources.
- `oidc-protected`: a dedicated oauth2-proxy between ingress and the application.
- `oidc-native`: direct ingress; the application owns Authorization Code with PKCE, tokens, sessions, and authorization.

A tagged policy prevents ambiguous combinations that a Boolean flag cannot represent.

### 5. Protected projects use a dedicated reverse proxy

The reusable `web-process` chart includes a pinned oauth2-proxy dependency enabled only for `oidc-protected`. It renders separate proxy and application Services plus these network paths:

```text
ingress-nginx -> project oauth2-proxy -> project application
                         |
                         +-> Keycloak OIDC endpoints
```

Default-deny policies allow ingress-nginx to reach the proxy but not the application. Only the proxy can reach the application. Client-supplied identity headers are removed before trusted identity headers are set. The proxy enforces the project's allowed Keycloak group.

The application Ingress never points directly to the protected application Service. Readiness covers the proxy, application, callback configuration, and authentication behavior rather than only pod availability.

**Alternative considered:** ingress-nginx external-auth annotations backed by one global oauth2-proxy. Rejected because per-project clients and cookies provide clearer isolation, simpler callbacks, and smaller credential blast radius.

### 6. Identity configuration and application release are separate writes

Keycloak realm/client desired state lives under `infrastructure/identity/` and is applied by a dedicated identity-configuration job using a narrowly scoped Keycloak Admin API credential. The job registers the client before a protected project can become ready. Client and proxy cookie secrets are SOPS-encrypted in the project directory.

ProcessManager never runs the identity-configuration job and never receives its credential. It accepts only an existing client identifier, encrypted Secret-manifest reference, callback URI, and allowed group. Initially, onboarding is ordered:

1. Identity administrator commits and applies the client configuration.
2. Identity administrator commits the SOPS-encrypted project Secret.
3. Project administrator selects `oidc-protected` in ProcessManager.
4. ProcessManager validates and commits Helm values.
5. Flux reconciles the chart and reports readiness.

This two-part operation is deliberate. Automating both later requires a dedicated least-privilege identity-config controller, not broader ProcessManager access.

### 7. ProcessManager trusts ingress identity but does not authenticate users

ProcessManager's own hostname is protected by its dedicated oauth2-proxy and Keycloak client. The Go application receives a documented trusted identity contract only from that proxy. Its middleware constructs the request actor from sanitized proxy headers and enforces the ProcessManager administrator group; it does not call Keycloak on each request or validate a password/session against the legacy auth service.

The existing `AuthStore` currently combines sessions, users, projects, and audit operations. Migration separates these concerns:

- remove session and user-account methods from manager runtime dependencies;
- move project desired state to the deployment repository contract;
- derive the authenticated actor from trusted proxy identity;
- retain audit recording behind a non-auth-specific interface and store the immutable OIDC issuer/subject plus display attributes;
- remove manager startup flags and Secrets for the custom auth-service URL and internal token.

This avoids replacing one identity database dependency with a Keycloak Admin API dependency.

### 8. User identity is `(issuer, subject)`

Applications that persist user-owned data store the validated OIDC issuer and `sub` claim as the external key. Email, display name, and group claims are mutable cached attributes. No project stores a Keycloak table identifier or creates a cross-database foreign key.

For `oidc-protected`, applications that need only whole-site admission need not consume user identity headers. Applications requiring durable user identity or fine-grained authorization use `oidc-native` rather than expanding the proxy-header contract.

### 9. Flux is the sole Kubernetes writer

ProcessManager commits access-policy values to `main`; Flux reconciles the chart. The existing `CreateProcess`, delete, scale, and direct Ingress mutation paths are removed or replaced as part of the broader GitOps cutover. ProcessManager retains read-only Kubernetes observation for deployment status.

A successful Git push is `Committed`, not `Ready`. Protected projects become ready only after Flux and workloads are healthy and the client registration precondition is satisfied.

### 10. Verification is behavior-oriented

The permanent contract tests cover configuration discrimination and observable access behavior:

- invalid policy combinations never create a commit;
- public requests do not redirect to Keycloak;
- anonymous protected requests redirect;
- allowed users reach the application;
- disallowed users receive `403`;
- spoofed identity headers do not reach the application;
- ingress cannot bypass the proxy;
- ProcessManager has no credential-verification or Keycloak-administration path;
- CNPG denial and point-in-time recovery are exercised operationally.

Protocol and network behavior require an integration environment with ingress-nginx, Keycloak, CNPG, oauth2-proxy, and Flux. Unit tests do not substitute for that smoke path.

### 11. Implementation spans two repositories

This is an umbrella change. Platform desired state, reusable charts, Flux resources, SOPS policy, and recovery automation are implemented in the `Project_HW` repository that owns this OpenSpec change. Go application changes are implemented in the sibling `/home/nacfson/Projects/ProcessManager` repository.

The repositories share only the versioned project access-policy values contract and trusted identity-header contract. They do not use relative filesystem imports or a synchronized source checkout. Each repository receives its own commits and verification; end-to-end verification runs only after both sides publish compatible versions. The apply workflow must name the repository for every task and must not treat a commit in one repository as completion of the other.

## Risks / Trade-offs

- **[Single-node outage removes Keycloak and PostgreSQL]** → Treat the service as non-HA, keep public projects independent, maintain off-server CNPG backups, and drill replacement recovery.
- **[Keycloak or CNPG resource pressure competes with CI]** → Use the 4 vCPU/16 GB/100 GB baseline only as an initial planning value, enforce requests and quotas, cap CI concurrency at one, run the 100,000-account acceptance workload, and resize or separate workloads when the approved latency or headroom objectives are not met.
- **[Two-step client and project configuration can drift]** → Keep both sources declarative, use deterministic client IDs, validate references before commit, and report the project non-ready until registration exists.
- **[Proxy-header trust can be bypassed by another network path]** → Route protected ingress only to oauth2-proxy, apply default-deny policies, strip reserved headers, and avoid alternate Services or NodePorts.
- **[One realm increases shared identity blast radius]** → Isolate clients, audiences, secrets, groups, and callbacks; deny realm-management roles to applications; back up and audit realm configuration.
- **[Removing custom auth changes existing sessions and personal identities]** → Use a maintenance window, invalidate legacy sessions, do not migrate legacy passwords or personal identity associations, require ordinary users to establish fresh Google-backed identities, and explicitly reassign any required project groups.
- **[SOPS Secret and Keycloak client secret can diverge]** → Generate once during identity configuration, verify the proxy login before readiness, and rotate the pair atomically through staged Git commits.
- **[CNPG backup exists but is not restorable]** → Gate production acceptance on a point-in-time restore into a replacement cluster and a successful Keycloak login against the restored data.

## Migration Plan

1. Establish the deployment repository, Flux bootstrap, reusable chart, SOPS decryption, and read-only ProcessManager observation required by the GitOps architecture.
2. Install the CNPG operator and identity database with deny-by-default networking, off-server base backup, WAL archive, retention, and restore configuration.
3. Deploy Keycloak, initialize the `platform` realm and offline recovery administrator, enforce local password plus TOTP for recovery, configure Google OpenID Connect, require role-conditional Keycloak TOTP for day-to-day administrators, disable ordinary-user local credential flows, and rotate bootstrap credentials.
4. Add declarative realm/client configuration and its dedicated narrowly scoped reconciliation job.
5. Add the tagged project access-policy schema and deterministic Helm values without enabling protected traffic.
6. Extend `web-process` with public, protected, and native routing variants, the pinned oauth2-proxy dependency, SOPS Secret references, and NetworkPolicies.
7. Register and deploy a test protected project; prove redirect, allow, deny, header stripping, no-bypass networking, and failed-client behavior.
8. Register a separate ProcessManager client and place its Ingress behind its dedicated oauth2-proxy.
9. Change ProcessManager request identity to the trusted proxy contract, split project/audit dependencies from the legacy `AuthStore`, and remove runtime use of custom session verification.
10. Invalidate legacy sessions, require ordinary users to establish fresh Google-backed platform identities with no default privileges, explicitly reassign required project groups, then remove public custom login, registration, password, verification, and session endpoints and their manifests.
11. Remove custom auth database credentials, internal auth token, write-capable application RBAC, and obsolete deployment scripts after the cutover is verified.
12. Perform Keycloak signing-key rotation, CNPG point-in-time restore, clean-cluster bootstrap, rollback drills, and the 100,000-account workload acceptance exercise before production acceptance.

Rollback before step 10 restores the previous ProcessManager Ingress and custom auth path by reverting the Git commits. After legacy sessions and endpoints are removed, rollback requires an explicit security decision and database restoration; the legacy service is not kept live as a hidden fallback.

## Open Questions

- Pin the exact Keycloak, CloudNativePG, oauth2-proxy, k3s, Flux, ingress-nginx, and cert-manager versions after compatibility checks.
- Select the off-server object-storage provider, backup retention, recovery-point objective, and recovery-time objective.
- Select access-token, refresh-token, SSO idle, SSO maximum, oauth2-proxy cookie, and administrator reauthentication lifetimes.
- Define the administrator role matrix and which role may assign or revoke every other privileged role.
- Define TOTP enrollment, reset authorization, recovery-code custody, alerting, and post-reset restrictions.
- Select the recovery-administrator network restriction and credential-custody procedure.
- Define Google identity suspension, unlinking, replacement, duplicate-account, and changed-claim behavior.
- Define personal-account deletion, tombstone, application-data, and audit-retention behavior.
- Define project-group assignment, expiry, revocation, and any project-owner delegation.
- Define the isolated 100,000-identity acceptance harness, test duration, error budget, telemetry, and cleanup.
