# Design

## Context

See `proposal.md` and the five capability specs for behavior. This repository currently contains architecture and OpenSpec planning material but no `platform-config`, GitOps controller configuration, reusable chart, or validation command. The sibling `/Users/hyungjuyu/Projects/Brain/ProcessManager` remains a Go/HTMX application that stores projects through the legacy auth service, uses an `authEnabled` Boolean, authenticates custom sessions, and constructs a write-capable Kubernetes client. Its existing GHCR workflow publishes mutable `latest`, semantic-version, and commit tags without a platform registration, digest-promotion, reconciliation, or rollback contract.

The target remains one k3s control-plane/worker node. Platform services are recoverable but not highly available. Git is the desired-state authority, Flux is the sole routine Kubernetes writer, Keycloak owns identity, and independently versioned trusted source repositories reach production only through a bounded Forgejo/OCI/ProcessManager delivery path.

## Goals / Non-Goals

**Goals:**

- Make the platform reconstructable from external infrastructure state, host bootstrap, deployment Git, immutable images, encrypted Secrets, and off-server backups.
- Establish one deterministic deployment layout, one reusable application chart, one validation gate, and one writer for every resource class.
- Replace custom production authentication with a recoverable Keycloak boundary and explicit project access policies.
- Replace ProcessManager's direct Kubernetes mutations with one shared Git transaction boundary and one read-only reconciliation observer.
- Register many existing trusted repositories and turn the current protected-branch head into a verified immutable release.
- Correlate delivery through workload readiness and perform at most one safe, project-scoped automatic rollback.

**Non-Goals:**

- Creating or transferring source repositories, supporting arbitrary source-control providers, or running untrusted production builds.
- Multi-node Kubernetes, PostgreSQL, or Keycloak high availability; multi-cluster placement; preview environments; canary or progressive delivery.
- Letting ProcessManager administer Keycloak accounts, groups, or clients, or letting projects access the identity database.
- Giving CI a host runtime socket, Kubernetes credential, deployment-repository credential, infrastructure credential, or another project's registry authority.
- Automatically reversing destructive or backward-incompatible state migrations.
- Provisioning external provider infrastructure or general application databases inside application delivery.

## Decisions

### 1. One change, dependency-ordered internal phases

The deployment foundation, identity boundary, shared ProcessManager GitOps kernel, and repository delivery path are implemented as phases of this change rather than separately applied OpenSpec changes. The ordering remains strict: the layout and validation contract precede platform services; the tagged project model and shared GitOps kernel precede delivery orchestration; the pilot cutover occurs last. Consolidation removes duplicate planning folders, not the architectural boundaries.

### 2. `platform-config/` is the deployment root

Deployment content lives under `platform-config/` with `clusters/production`, `infrastructure`, `applications`, and `charts/web-process`. The production controller reconciles `main` at `platform-config/clusters/production`. Infrastructure and application reconciliation units declare dependencies; every project has one directory and namespace-scoped entry point.

The layout is hosting-independent so it can be published to the eventual Forgejo deployment repository without changing the reconciled path. Project automation may mutate only its registered application path; bootstrap, infrastructure, shared charts, and other projects remain outside its authority.

### 3. Control planes have disjoint ownership

OpenTofu owns provider resources such as VPS instances, networks, DNS, firewalls, disks, and backup destinations. Ansible owns host policy, mounts, and k3s plus the minimum Flux bootstrap. After handover, Flux owns every Git-managed Kubernetes resource; bootstrap automation no longer applies application or platform manifests. Kubernetes schedules workloads from stable capabilities such as architecture, capacity, labels, taints, and storage class. Provider identifiers and node names never enter project configuration.

Managing Kubernetes resources with OpenTofu, continuing to apply manifests with Ansible, or retaining ProcessManager's direct mutations would create competing writers and is rejected.

### 4. Validation, chart, and Secrets form one configuration boundary

One runner-agnostic command renders the production root and every unit, lints and renders `charts/web-process`, validates values against its versioned schema, resolves references, enforces namespace and path ownership, rejects provider or node identity, scans for plaintext secrets without echoing values, and verifies clean renders are byte-identical.

`charts/web-process` is the only application chart. Its values contract includes immutable image digest, port, replicas, resources, health check, architecture, access policy, policy-specific identity references, and persistence classification. Secret-bearing manifests are committed only as SOPS-encrypted files; all other resources reference Secrets by namespace and name.

### 5. Keycloak is the identity authority

Keycloak runs in `identity-system` with one `platform` realm at `https://auth.<domain>/realms/platform`. Ordinary users authenticate only through Google OpenID Connect and receive no project group or administrator role by default. Day-to-day administrators use Google plus a Keycloak-controlled role-conditional TOTP step-up. One restricted local password-plus-TOTP recovery administrator remains for Google or broker outages.

ProcessManager and every OIDC-enabled project use distinct clients, exact callbacks, audiences, credentials, and groups. Projects trust the platform Keycloak issuer, never Google tokens directly. Stable application identity is the validated `(issuer, subject)` pair; email and display claims are mutable.

### 6. Identity persistence is isolated and recoverable

CloudNativePG runs a one-instance PostgreSQL cluster for Keycloak with TLS, `local-path-retain`, scheduled encrypted off-server base backups, continuous WAL archiving, retention, and tested point-in-time recovery. Network policy permits database access only from Keycloak. A second same-node database or Keycloak replica is rejected because it does not survive the shared node failure domain.

Production acceptance exercises at least 100,000 synthetic broker-linked identities, 5 platform-controlled login completions per second, 50 token refreshes per second, and 5,000 concurrent sessions while keeping p95 platform-controlled authentication latency below 500 ms. Google-controlled browser latency is measured separately.

### 7. Access policy is tagged and chart-owned

The Boolean authentication flag becomes exactly one of:

- `public`: ingress routes directly to the application and no OIDC resources render.
- `oidc-protected`: ingress routes only through a dedicated oauth2-proxy that enforces the project's Keycloak group.
- `oidc-native`: ingress routes directly and the application owns Authorization Code with PKCE, token validation, sessions, and authorization.

For protected projects, default-deny network policy allows ingress-nginx to reach the proxy but not the application; only the proxy reaches the application. Reserved identity headers are removed before trusted proxy headers are set. Keycloak client desired state is managed by a narrowly scoped identity-configuration job. ProcessManager references an existing client and encrypted Secret but never receives Keycloak administration credentials.

### 8. ProcessManager has one shared GitOps kernel

`ProjectDesiredStateStore` is the sole deployment-repository transaction boundary. It loads remote `main`, returns the typed project snapshot and revision, accepts a deterministic mutation conditioned on the expected project revision, restricts the diff to the allowed project path, runs repository validation, signs the commit, and pushes without force. Unrelated branch advancement may be regenerated only after confirming the project revision is unchanged; same-project advancement is a conflict.

`ProjectReconciliationObserver` is the sole read-only boundary for correlating a project and deployment revision with Flux and Kubernetes readiness. The ProcessManager composition root constructs one store and one observer. Access-policy handlers, registration, promotion, and rollback receive those instances; no feature constructs another Git client, mutable project catalog, Kubernetes writer, or reconciliation observer.

A successful Git push means `committed`, not `ready`. ProcessManager's application mutation RBAC and direct create, update, scale, delete, Service, and Ingress paths are removed. Its own authentication moves behind a dedicated oauth2-proxy; middleware accepts only sanitized trusted identity and no longer calls the custom auth service.

### 9. Registration is declarative and source/build ownership is separate

Each managed project stores `applications/<project>/registration.yaml` beside its release files. It records canonical Forgejo identity, protected default branch, build-contract path and version, assigned registry namespace, supported platform, deployment path, runtime policy, and automatic-delivery policy. ProcessManager reconstructs registration from Git after restart; no database catalog becomes authoritative.

The source repository contains `.platform/build.yaml` with a repository-local Dockerfile builder, relative context and Dockerfile paths, required test target, runtime target, and allowed platforms. Source cannot choose the registry namespace, production digest, runtime access policy, resources, Secrets, or infrastructure.

### 10. Builds use one pinned trusted workflow and capability-scoped credentials

Every registered repository invokes one platform-owned Forgejo action or reusable workflow pinned to an immutable revision. The runner executes declared tests and rootless BuildKit with bounded concurrency, clean workspaces, resource limits, no host socket, and no cluster or deployment-repository credential. Pull requests, forks, tags, unregistered repositories, and non-default branches receive no production publication or promotion authority.

Only the publication step receives a short-lived or project-scoped credential for the assigned OCI namespace. A build-result credential may notify ProcessManager only for that project. The callback is a notification, not proof: ProcessManager independently verifies the Forgejo run, workflow, protected branch head, contract version, registry namespace, digest, manifest, and target platform.

### 11. Promotion is digest-only and project-scoped

A production candidate binds canonical repository, source commit, Forgejo run, contract version, target platform, registry namespace, and registry-confirmed digest. Tags may exist for discovery but never identify desired production state. If source `main` advanced, the result becomes `superseded`.

For an eligible result, delivery submits a typed release mutation and expected project revision to `ProjectDesiredStateStore`. The mutation changes only digest and source/build metadata. An unrelated project commit is preserved; a newer release for the same project conflicts and supersedes stale work. A global branch-head equality check is rejected because unrelated projects share the branch.

### 12. Release state is reconstructable and rollback is bounded

Forgejo owns build-run state, the registry owns manifests, Git owns registration and desired release state, Flux owns reconciliation, and Kubernetes owns workload readiness. Delivery correlates them by project, source commit, run, digest, and deployment commit:

```text
REGISTERED
    |
    v
QUEUED --> TESTING --> BUILDING --> BUILD_FAILED
                               |
                               v
                         ARTIFACT_READY
                               |
                  +------------+------------+
                  |                         |
                  v                         v
             SUPERSEDED                COMMITTED
                                           |
                                           v
                                      RECONCILING
                                      /         \
                                     v           v
                                  READY        FAILED
                                                 |
                                                 v
                                           ROLLING_BACK
                                            /          \
                                           v            v
                                  ROLLBACK_READY   ROLLBACK_FAILED
```

Promotion records the previous `ready` digest and source metadata when one exists. Automatic rollback requires available observation, a confirmed failure or observed readiness timeout, the failed release still being current, a recorded previous `ready` release, and one-release backward-compatible durable state. Delivery submits one restore mutation through the shared store. Success is `rollback-ready`; failure is terminal `rollback-failed` with an alert. There is no rollback loop or synthetic target for a failed first release.

### 13. Integrated architecture

```text
  OpenTofu                    Ansible
  provider resources         host + k3s/Flux bootstrap
       |                           |
       +-------------+-------------+
                     |
                     v
  +------------------------------------------------------------------+
  | Kubernetes cluster                                               |
  |                                                                  |
  | platform-config main --> Flux --> shared platform + applications |
  |          ^                         |                              |
  |          |                         +--> Keycloak --> CNPG         |
  |          |                         +--> project workloads         |
  +----------|--------------------------------------------------------+
             | signed, non-force Git                  ^ read-only state
             |                                        |
  +----------+----------------------------------------+----------------+
  | ProcessManager                                                     |
  |  ProjectDesiredStateStore      ProjectReconciliationObserver       |
  |             ^                              |                       |
  |             +--- identity/project handlers + delivery correlator --+
  +-------------------------------+------------------------------------+
                                  ^ authenticated build result
                                  |
  Existing trusted repositories --> Forgejo Actions --> OCI registry
       `.platform/build.yaml`       tests + rootless      image@sha256
                                    BuildKit

  Off-server backups <--- CNPG WAL/base backup and encrypted recovery data
```

Changing or replacing a node preserves the contract when the replacement exposes the required capabilities. This supports reconstruction, not uninterrupted availability.

### 14. Implementation spans two repositories without shared source checkout

Deployment configuration, charts, validation, platform services, and recovery configuration live in `nacfson_pipeline`. ProcessManager application changes live in `/Users/hyungjuyu/Projects/Brain/ProcessManager`. They share versioned values, registration, trusted-header, and GitOps behavior contracts, not relative imports or synchronized commits. Each repository receives its own verification; end-to-end acceptance begins only after compatible contract versions exist on both sides.

## Risks / Trade-offs

- **[Single-node failure removes identity and applications]** → State non-HA explicitly, retain off-server backups and immutable images, and drill full replacement recovery.
- **[Platform services and CI exhaust one node]** → Start from the approved 4 vCPU/16 GB RAM/100 GB SSD baseline, enforce requests, quotas, and runner concurrency one, run acceptance load, then resize or separate workloads from evidence.
- **[Offline validation misses cluster admission behavior]** → Prove render, schema, reference, ownership, secret, and determinism offline; separately exercise controllers, networking, and readiness on the real cluster.
- **[Proxy trust is bypassed]** → Route protected ingress only to oauth2-proxy, strip reserved headers, apply default-deny policy, and prohibit alternate Services and NodePorts.
- **[Identity client and encrypted Secret drift]** → Generate once, validate references before project readiness, test login, and rotate the pair through staged commits.
- **[Backup exists but cannot restore]** → Gate production on point-in-time recovery into a replacement cluster and successful Keycloak login.
- **[Compromised protected source branch reaches production]** → Require branch protection, pinned workflow, tests, project-scoped credentials, and independent Forgejo/registry verification; source still cannot change runtime policy.
- **[Shared runner crosses project authority]** → Use rootless isolation, clean workspaces, no host or cluster credential, and per-project registry/result capabilities.
- **[Older work overwrites newer project state]** → Check protected source head and expected project revision; stale builds and conflicts become `superseded`.
- **[Rollback overwrites unrelated work or restores an incompatible binary]** → Generate only a project-scoped mutation against current Git, require the failed release to remain current, and block automatic delivery for incompatible migrations.
- **[Observation outage appears to be release failure]** → Report unavailable separately; never roll back from observation loss alone.
- **[A second writer is introduced during implementation]** → Contract tests and composition-root review require one store, one observer, Flux-only Kubernetes mutation, and no delivery-owned Git adapter.

## Migration Plan

1. Add `platform-config`, reconciliation units, `web-process` base schema, encrypted-secret convention, ownership metadata, validation command, and negative fixtures; prove clean deterministic offline rendering.
2. After OpenTofu and Ansible provide the required external and host capabilities, bootstrap Flux once and hand all Git-managed resources to reconciliation.
3. Pin and deploy ingress, certificates, SOPS decryption, CloudNativePG, the identity database, Keycloak, backups, and the identity-configuration job.
4. Configure the realm, Google broker, day-to-day administrator TOTP, recovery administrator, distinct clients, groups, callbacks, and rotation procedures.
5. Extend `web-process` for all access policies and prove redirect, allow, deny, header stripping, no-bypass networking, public independence, and native OIDC boundaries.
6. Implement the tagged project model, `ProjectDesiredStateStore`, `ProjectReconciliationObserver`, deterministic project mutations, exact-revision status, and shared contract tests; remove ProcessManager's application mutation authority.
7. Put ProcessManager behind its own oauth2-proxy, cut request identity to trusted headers, split project/audit concerns from `AuthStore`, invalidate legacy sessions, and remove custom login, registration, password, verification, session, credential, and deployment paths.
8. Add registration and build-contract schemas, Forgejo validation, pinned trusted workflow, rootless BuildKit, queueing, and project-scoped registry/result credentials.
9. Add independent Forgejo and OCI verification, current-head digest promotion, project-scoped compare-and-swap, release metadata, end-to-end state correlation, rollback compatibility, one-attempt rollback, and alerts.
10. Onboard one low-risk stateless existing repository and prove successful delivery plus failed test, stale build, forged result, wrong namespace/platform, service outage, non-fast-forward, restart reconstruction, observation outage, successful rollback, superseded rollback, and rollback failure.
11. Remove the pilot's mutable GHCR production path only after the dedicated registry release and rollback drill pass; retain historical images according to policy.
12. Run clean-cluster reconstruction, Keycloak signing-key rotation, CNPG point-in-time restore, node-replacement recovery, credential isolation, 100,000-identity capacity acceptance, and the complete project delivery path before production acceptance.

Rollback before the legacy-auth cutover restores the previous ProcessManager ingress and auth path by reverting Git commits. After legacy sessions and endpoints are removed, restoring them requires an explicit security decision; they are not kept as a hidden fallback. Delivery rollback disables automatic promotion first, preserves the last committed Git state, and leaves Flux reconciling it. Mutable-tag or direct-cluster deployment is not an allowed fallback.

## Open Questions

- Pin exact k3s, Flux, Kustomize, Helm, SOPS, ingress-nginx, cert-manager, Keycloak, CloudNativePG, oauth2-proxy, Forgejo, runner, BuildKit, registry, and workflow versions or digests.
- Select off-server object storage, backup retention, recovery-point objective, and recovery-time objective.
- Select access-token, refresh-token, SSO, cookie, administrator reauthentication, readiness-timeout, build-result, image, and audit-retention values.
- Define administrator roles, TOTP enrollment/reset, recovery-code custody, recovery-network restriction, and security-event response.
- Define Google identity suspension, unlinking, replacement, duplicate-account, changed-claim, deletion, tombstone, application-data, and audit-retention behavior.
- Define project-group assignment, expiry, revocation, and any project-owner delegation.
- Select the exact Forgejo reusable-workflow or pinned-action mechanism and build-result credential format and rotation interval.
- Define the isolated identity-capacity harness, duration, error budget, telemetry, and cleanup.
