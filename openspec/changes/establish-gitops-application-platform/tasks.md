# Tasks

## 1. Deployment repository foundation

- [x] 1.1 In `nacfson_pipeline`, create `platform-config/` with `clusters/production`, `infrastructure`, `applications`, and `charts/web-process` plus their reconciliation entry points; verify the production root renders offline from a clean checkout with no unresolved references or placeholder resources.
- [x] 1.2 Declare infrastructure and application reconciliation units with explicit dependencies and health checks; verify every referenced path exists and infrastructure converges before identity and application consumers.
- [x] 1.3 Define the per-project directory contract with namespace manifest, reconciliation entry point, registration location, and values location; verify the applications aggregation renders successfully with zero projects.
- [x] 1.4 Record platform-owned and project-automation write boundaries in repository metadata; verify project mutations cannot reach bootstrap, infrastructure, shared charts, encrypted platform Secrets, or another project.
- [x] 1.5 Record the OpenTofu, Ansible, Flux, and Kubernetes ownership matrix plus capability-only placement contract; verify every resource class has one writer and project validation rejects provider resource identifiers and Kubernetes node names.

## 2. Reusable chart, Secrets, and validation

- [x] 2.1 Create `platform-config/charts/web-process` with metadata and a versioned values schema covering immutable digest, ports, replicas, resources, health checks, platform, access policy, identity references, and persistence; verify valid values render and incomplete values, mutable tags, and unsupported policy values fail schema validation.
- [x] 2.2 Implement the shared workload, Service, labels, resource limits, health probes, and namespace-scoped rendering used by every access policy; verify a complete fixture produces only the owning project's resources.
- [x] 2.3 Define the project HelmRelease convention that references the in-repository chart; verify a fixture resolves the chart path and a missing or out-of-boundary chart reference is rejected.
- [x] 2.4 Define the SOPS-encrypted manifest convention and Flux decryption wiring with the decryption key referenced only by in-cluster name; verify rendering succeeds without repository key material or plaintext values.
- [x] 2.5 Add plaintext-secret detection that reports only file location; verify a crafted plaintext fixture fails without echoing its value and the encrypted equivalent passes.
- [x] 2.6 Implement one validation entry point for root/unit rendering, chart lint/render, schema validation, reference resolution, namespace containment, path ownership, placement identity, plaintext secrets, and deterministic clean rendering; verify the zero-project tree passes and two renders are byte-identical.
- [x] 2.7 Add negative fixtures for unresolved references, contract violations, cross-namespace resources, out-of-boundary paths, plaintext Secrets, overlapping writers, and hard-coded provider or node identity; verify each fails with a specific violation and passes after correction.
- [x] 2.8 Document the validation command and prerequisites and make it the required production-branch check; verify a clean clone reproduces the passing result using only documented prerequisites.

## 3. Version pins and operating policy

- [x] 3.1 Select mutually compatible immutable versions or digests for k3s, Flux, Kustomize, Helm, SOPS, ingress-nginx, cert-manager, CloudNativePG, Keycloak, oauth2-proxy, Forgejo, the Actions runner, rootless BuildKit, registry, and platform workflow on `linux/amd64`; verify every repository and image resolves.
- [x] 3.2 Version the chart values, trusted identity-header, registration, source-build, `ProjectDesiredStateStore`, and `ProjectReconciliationObserver` contracts in both repositories; verify an incompatible version fails closed rather than being interpreted permissively.
- [x] 3.3 Select and record off-server object storage, backup retention, RPO/RTO, token/session/cookie lifetimes, administrator reauthentication, release readiness timeout, and build/image/audit retention; verify each value has an operational owner and enforceable configuration location.
- [x] 3.4 Define the administrator role matrix, TOTP enrollment/reset, recovery-code custody, recovery-account network restriction, credential custody, and security-event response; verify no role can silently grant itself broader privilege.
- [x] 3.5 Define Google identity suspension, unlinking, replacement, duplicate-account, changed-claim, deletion/tombstone, application-data retention, and project-group assignment/expiry/revocation behavior; verify every lifecycle transition has an authorization owner and preserves issuer/subject semantics.
- [x] 3.6 Add local-first bootstrap that verifies Forgejo, installs it only when absent, preserves existing data on rerun, seeds committed local history, registers Flux's read-only key, verifies the repository, and bootstraps Flux directly from Forgejo without external Git.

## 4. CloudNativePG identity storage

- [x] 4.1 Add Flux-managed `cnpg-system` and `identity-system` namespaces with Pod Security labels, quotas, limits, and default-deny policies; verify rendered manifests contain all boundaries.
- [ ] 4.2 Deploy the pinned CloudNativePG operator and one-instance Keycloak PostgreSQL cluster with `local-path-retain`, TLS, and a generated Keycloak-only owner Secret; verify the cluster reaches healthy read-write status.
- [ ] 4.3 Add NetworkPolicies permitting TCP 5432 only from Keycloak and denying every project namespace; verify Keycloak connects over TLS while a test project pod is denied and receives no database credential.
- [ ] 4.4 Configure encrypted off-server base backups, continuous WAL archiving, retention, and backup-age monitoring; verify a completed backup and archived WAL exist outside the VPS.

## 5. Keycloak identity authority

- [ ] 5.1 Deploy exactly one pinned Keycloak replica in `identity-system` against the CNPG credential with limits, probes, TLS ingress at `auth.<domain>`, and no Kubernetes API token; verify OIDC discovery returns the exact HTTPS issuer.
- [ ] 5.2 Add declarative `platform` realm configuration with one password-plus-TOTP recovery administrator, Google-backed administrator role-conditional TOTP, administrator-event auditing, signing-key rotation, namespaced groups, minimal claims, and ordinary-user local credential flows disabled; verify each administrator path and routine recovery-account alert.
- [ ] 5.3 Configure Google OpenID Connect as the sole ordinary-user broker using a SOPS-encrypted credential, constrained HTTPS egress, and a first-login flow assigning no groups or roles; verify a new account is unprivileged, projects receive only the Keycloak issuer, and no other workload receives the broker credential.
- [ ] 5.4 Implement the narrowly scoped identity-configuration job with idempotent realm/client reconciliation and rejection of wildcard callbacks or project realm-management roles; verify valid configuration converges and invalid configuration is not applied.
- [ ] 5.5 Register distinct ProcessManager and test-project clients with exact callbacks, separate audiences, groups, and encrypted client/cookie Secrets; verify neither client can use the other's secret, callback, audience, or administrator membership.

## 6. Project access chart

- [x] 6.1 Implement `public` and `oidc-native` chart branches that route directly to the application and never render oauth2-proxy; verify anonymous public access and the expected native-OIDC resources.
- [x] 6.2 Implement the pinned per-project oauth2-proxy dependency for `oidc-protected` with exact issuer, callback, existing Secret, secure cookie settings, allowed group, probes, and limits; verify every missing or contradictory protected-only value is rejected.
- [x] 6.3 Render separate proxy and application Services plus default-deny and allow NetworkPolicies so ingress-nginx reaches only the proxy and only the proxy reaches the application; verify selectors match no unrelated workload or bypass path.
- [x] 6.4 Add chart contract cases for all policies, wildcard/non-HTTPS callbacks, public-with-OIDC fields, shared/cross-project Secrets, unsupported policy, and reserved-header behavior; verify every invalid combination fails and every supported combination renders deterministically.

## 7. Shared ProcessManager GitOps kernel

- [x] 7.1 In `/Users/hyungjuyu/Projects/Brain/ProcessManager`, replace `authEnabled` with tagged access-policy and OIDC types matching the versioned chart contract; verify table-driven parsing and validation cover every supported and contradictory combination.
- [x] 7.2 Update project forms and handlers for policy-specific fields and actionable errors; verify handler and template behavior for `public`, `oidc-protected`, and `oidc-native` submissions.
- [x] 7.3 Implement the versioned `ProjectDesiredStateStore` as the sole deployment-repository writer: load remote `main`, expose typed project snapshots/revisions, apply deterministic path-scoped mutations with an expected project revision, run repository validation, sign, and push without force; verify unrelated-project advancement regenerates safely and same-project advancement conflicts.
- [x] 7.4 Replace project/process create, update, scale, delete, Service, and Ingress Kubernetes mutations with shared-store desired-state operations and reduce manager RBAC/client interfaces to observation; verify no application mutation verb remains.
- [x] 7.5 Implement `ProjectReconciliationObserver` as the sole read-only Flux/Kubernetes status boundary, correlating an exact deployment revision with chart and workload conditions; verify committed, reconciling, ready, failed, and unavailable without exposing mutation operations.
- [x] 7.6 Wire exactly one store and one observer in the ProcessManager composition root and add shared contract tests used by access and delivery mutations; verify no feature constructs another Git client, mutable catalog, Kubernetes writer, or reconciliation observer.
- [x] 7.7 Add the runtime contract-compatibility gate; verify an absent or incompatible values, store, or observer version disables affected operations while preserving the last committed desired state and creating no fallback adapter.

## 8. ProcessManager authentication cutover

- [x] 8.1 Implement trusted-proxy middleware that rejects absent or malformed identity, parses immutable issuer/subject and groups, strips spoofed headers, and enforces the ProcessManager administrator group; verify middleware tests cover spoofing and issuer separation.
- [x] 8.2 Split project and audit operations from `AuthStore`, remove manager runtime calls to custom session/user endpoints, and verify `cmd/manager` starts without auth-service URL, internal token, or auth database credential.
- [x] 8.3 Store audit actors by issuer and subject with mutable display attributes; verify email changes preserve actor identity and equal subjects from different issuers remain distinct.
- [ ] 8.4 Deploy ProcessManager behind its dedicated oauth2-proxy and Keycloak client with no direct ingress path; verify administrator access succeeds and project-only membership receives `403`.
- [ ] 8.5 Register and deploy a representative `oidc-protected` project through identity configuration plus ProcessManager; verify anonymous redirect, allowed access, denied-group `403`, encrypted Secret use, and application response.
- [ ] 8.6 Attempt reserved-header spoofing and direct ingress-to-application traffic; verify the spoofed value is removed and NetworkPolicy denies bypass.
- [ ] 8.7 Delete Keycloak and separately interrupt its database; verify protected routes fail closed, public routes remain available, Kubernetes restores the single Keycloak replica when possible, and ProcessManager reports dependency failure rather than readiness.
- [ ] 8.8 Invalidate legacy personal sessions without importing accounts, passwords, or identity associations; establish fresh Google-backed identities and explicitly assign only required roles/groups, verifying first login grants none automatically.
- [x] 8.9 Remove `cmd/auth`, custom login/registration/password/session production routes and templates, `k8s/auth`, bootstrap-password tooling, obsolete auth Secrets, and related deployment scripts; verify no production endpoint authenticates through the legacy service and remaining Go behavior passes.

## 9. Declarative project onboarding

- [x] 9.1 Add the registration schema for canonical Forgejo identity, protected default branch, build-contract reference, assigned registry namespace, platform, deployment path, runtime policy, and automatic-delivery policy; verify missing, duplicate, unsupported, or source-owned platform fields fail specifically.
- [x] 9.2 Add the versioned `.platform/build.yaml` schema for repository-local Dockerfile, relative context/Dockerfile, required test target, runtime target, and registered platforms; verify path traversal, unsupported builders/versions, extra platforms, registry fields, production digests, runtime policy, and Secrets are rejected.
- [x] 9.3 Extend deployment validation for uniqueness across project identifier, repository identity, deployment path, and registry namespace; verify cross-project collisions and files outside `applications/<project>/` are rejected.
- [x] 9.4 Add `applications/<project>/registration.yaml` as non-Kubernetes metadata excluded from Kustomize resources; verify the root renders with registration present and fails when it is referenced as a Kubernetes resource.
- [x] 9.5 Extend the tagged ProcessManager project model with immutable repository identity, default branch, contract reference, registry namespace, platform, deployment path, and delivery policy; verify all valid and conflicting combinations.
- [x] 9.6 Implement registration reconstruction and mutations through the shared store; verify a clean ProcessManager restart reloads every registration from Git without a database catalog or delivery-owned Git client.
- [x] 9.7 Update registration handlers and UI/API to verify Forgejo reachability, canonical identity, branch protection, approved workflow, build contract, assignments, and runtime policy before commit; verify each rejection path and successful registration.
- [x] 9.8 Make registration idempotent; verify an identical replay creates no commit while immutable conflicts preserve committed state.
- [x] 9.9 Implement deregistration that disables build-result admission and promotion before committing release removal; verify source repository, registry history, Secrets, persistent data, and backups remain unless separately authorized.
- [x] 9.10 Reject source/build-result attempts to change hostname, access policy, resources, replicas, health checks, Secrets, persistence, or infrastructure; verify authorized runtime policy updates remain a separate shared-store mutation.

## 10. Trusted Forgejo build path

- [x] 10.1 Implement the pinned platform action or reusable workflow that validates `.platform/build.yaml`, runs the Dockerfile test target, builds the runtime target with rootless BuildKit, and emits source/build metadata; verify valid, test-failed, contract-failed, and build-failed outcomes.
- [ ] 10.2 Configure the trusted runner with concurrency one, requests/limits, clean workspaces, no Kubernetes token, and no host runtime socket; verify a second build queues and a malicious fixture cannot access cluster or host runtime.
- [ ] 10.3 Enforce protected-default-branch and approved-workflow admission before production credentials are available; verify tags, other branches, pull requests, forks, unregistered repositories, and unprotected branches receive none.
- [ ] 10.4 Issue registry credentials scoped to one assigned project namespace and unavailable during untrusted verification; verify the project publishes inside its namespace and receives authorization failure elsewhere.
- [ ] 10.5 Add a project-scoped authenticated build-result notification carrying repository, commit, run, contract, platform, namespace, and digest; verify malformed, unauthenticated, cross-project, and duplicate notifications cannot mutate deployment state.
- [ ] 10.6 Verify runner and credential isolation with untrusted and malicious fixtures; confirm neither can obtain a platform-config, infrastructure, host, Kubernetes, or another-project credential.

## 11. Artifact verification and automatic promotion

- [x] 11.1 Add narrow Forgejo and OCI Distribution interfaces for run, branch head, workflow, manifest, digest, and platform verification; verify success, unavailable services, forged identity, missing manifest, mutable reference, wrong namespace, and wrong platform.
- [x] 11.2 Implement the idempotent build-result endpoint with independent Forgejo/registry verification; verify a callback alone cannot prove success and replay never creates duplicate promotion.
- [x] 11.3 Recheck protected `main` immediately before promotion; verify the current result proceeds and an older queued result becomes `superseded` without production change.
- [x] 11.4 Build a typed release mutation changing only digest and source/build metadata and submit it through `ProjectDesiredStateStore`; verify no runtime policy, Secret, chart, bootstrap, infrastructure, or cross-project diff.
- [x] 11.5 Supply the expected project release revision and interpret shared-store conflicts; verify unrelated branch advancement regenerates safely while a newer same-project release becomes `superseded` without delivery-owned Git retry/push code.
- [x] 11.6 Record project, repository, source commit, run, contract version, platform, namespace, digest, predecessor, deployment commit, and actor; verify audit output and commit messages contain no credentials.

## 12. Release observation and bounded rollback

- [x] 12.1 Implement the delivery-state correlator above `ProjectReconciliationObserver` for queued, testing, building, build-failed, artifact-ready, superseded, committed, reconciling, ready, failed, rolling-back, rollback-ready, and rollback-failed; verify identifiers persist and build/Git success never implies readiness.
- [x] 12.2 Persist the previous `ready` digest and source metadata in each promotion and reconstruct it after ProcessManager restart; verify restart during reconciliation retains candidate and rollback target without process memory.
- [x] 12.3 Add the rollback-compatibility declaration and gate; verify compatible releases remain eligible while destructive, irreversible, or unknown migrations are blocked before automatic promotion.
- [x] 12.4 Trigger rollback only for the exact current project revision on confirmed Flux failure or observed readiness timeout; verify observation loss reports unavailable and never triggers rollback.
- [x] 12.5 Submit one typed rollback mutation with the expected failed revision through the shared store; verify unrelated commits remain, same-project advancement supersedes rollback, no prior `ready` release creates no target, and force-push is impossible.
- [x] 12.6 Observe rollback through the shared observer; verify success becomes `rollback-ready`, failure becomes terminal `rollback-failed` with an alert, and the same failure cannot trigger another automatic rollback.
- [x] 12.7 Update ProcessManager project/observatory surfaces with source commit, run, digest, deployment revision, state, reason, and rollback result; verify every externally visible state is distinguishable.

## 13. Recovery and production acceptance

- [ ] 13.1 Rotate one project's Keycloak client and oauth2-proxy cookie Secrets atomically; verify new sessions work and unrelated projects remain unchanged.
- [ ] 13.2 Restore the CNPG identity database to a selected timestamp in a replacement cluster; verify Keycloak starts with accounts and clients from that recovery point.
- [ ] 13.3 Recreate the application cluster from OpenTofu-managed external resources, Ansible host/bootstrap state, deployment Git, the SOPS recovery key, CNPG backups, and registry images; verify public, protected, ProcessManager, Keycloak, and pilot routes satisfy their contracts without project edits for the replacement node.
- [ ] 13.4 Load at least 100,000 synthetic broker-linked identities with a test-only harness, then exercise login completion, refresh, logout, 5,000 concurrent sessions, email change, and paginated administrator search at 5 logins and 50 refreshes per second; verify p95 platform latency stays below 500 ms and issuer/subject associations remain correct.
- [ ] 13.5 Select one low-risk stateless existing repository, add only the reviewed build contract and pinned workflow, and register it without creating, deleting, or otherwise rewriting source history.
- [ ] 13.6 Run the pilot through protected-`main` tests, rootless build, OCI publication, independent digest verification, signed promotion, Flux reconciliation, and workload readiness; verify `ready` identifiers match repository, commit, run, digest, and deployment revision.
- [ ] 13.7 Exercise duplicate notification, stale build, forged result, mutable tag, wrong namespace/platform, failed test, registry outage, ProcessManager restart, non-fast-forward, and unrelated-project commit; verify specified states, no direct Kubernetes mutation, and no unrelated diff.
- [ ] 13.8 Exercise confirmed release failure, observation outage, no-known-good failure, successful rollback, superseded rollback, and rollback failure; verify only the eligible current release rolls back once and terminal failure stops automatic mutation.
- [ ] 13.9 Remove the pilot's mutable GHCR production path only after the dedicated-registry release and rollback drill pass; verify production references only the dedicated registry digest and historical images follow retention policy.
- [ ] 13.10 Reconcile `ARCHITECTURE.md` with the implemented ownership, identity, GitOps kernel, onboarding, delivery, and rollback behavior; run strict OpenSpec validation plus relevant ProcessManager unit/handler/contract tests, chart and manifest validation, security checks, recovery drills, and end-to-end acceptance before marking the unified change complete.
