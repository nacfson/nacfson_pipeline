# Tasks

## 1. Cross-Repository Contract and Version Pins

- [ ] 1.1 In `Project_HW`, create the `platform-config` layout for cluster infrastructure, applications, and `charts/web-process`, and verify Flux can build the production root with no missing references.
- [ ] 1.2 In `Project_HW`, define and version the shared project access-policy values contract for `public`, `oidc-protected`, and `oidc-native`, including valid and invalid example values, and verify schema validation accepts only the specified combinations.
- [ ] 1.3 Select and pin k3s-compatible Keycloak, CloudNativePG, oauth2-proxy, and chart versions plus the off-server object-storage provider, retention values, and token/session lifetimes; record them in platform configuration and verify all Helm repositories and images resolve by immutable version or digest.
- [ ] 1.4 In both repositories, document the access-policy and trusted identity-header contract version consumed by each side, and verify incompatible versions fail validation instead of deploying.
- [ ] 1.5 Define and record the administrator role matrix, TOTP enrollment and reset procedure, recovery-account network restriction, credential custody, and security-event response; verify no role can silently grant itself broader privilege.
- [ ] 1.6 Define and record Google identity suspension, unlinking, replacement, duplicate-account handling, personal-account deletion and retention, and project-group administration; verify each lifecycle transition has an explicit authorization owner and stable issuer/subject behavior.

## 2. CloudNativePG Identity Storage

- [ ] 2.1 In `Project_HW`, add Flux-managed `cnpg-system` and `identity-system` namespaces with Pod Security labels, quotas, limits, and default-deny policies, and verify rendered manifests contain the required boundaries.
- [ ] 2.2 In `Project_HW`, deploy the pinned CloudNativePG operator and a one-instance Keycloak PostgreSQL cluster using `local-path-retain`, TLS, and a generated Keycloak owner Secret, and verify the cluster reaches healthy read-write status.
- [ ] 2.3 Add identity-database NetworkPolicies that permit TCP 5432 only from Keycloak and deny a test pod in every other namespace, and verify both the permitted TLS connection and denied project connection.
- [ ] 2.4 Configure encrypted off-server base backups, continuous WAL archiving, retention, and backup-age monitoring for the identity cluster, and verify a completed backup and archived WAL are present outside the VPS.

## 3. Keycloak Identity Authority

- [ ] 3.1 In `Project_HW`, deploy exactly one pinned Keycloak replica in `identity-system` against the CNPG credential with resource limits, health probes, TLS ingress at `auth.<domain>`, and no Kubernetes API token, and verify OIDC discovery returns the exact HTTPS issuer.
- [ ] 3.2 Add declarative `platform` realm configuration with one restricted local recovery administrator protected by password plus TOTP, a role-conditional TOTP step-up for Google-backed day-to-day administrators, administrator-event auditing, signing-key rotation policy, namespaced project groups, minimal token claims, and ordinary-user local credential flows disabled; verify Google primary authentication alone cannot enter administration, recovery requires both factors, and routine recovery-account use produces a reviewable security event.
- [ ] 3.3 Configure Google OpenID Connect as the sole initial broker for ordinary personal accounts and the primary authenticator for day-to-day administrators using a SOPS-encrypted client credential, the required Keycloak DNS and TCP 443 egress, and a first-broker-login flow that assigns no groups or roles; verify a new Google test account creates one fresh unprivileged platform identity, an explicitly promoted administrator must complete Keycloak TOTP, projects receive only the Keycloak issuer, and no non-Keycloak workload receives the Google credential or identity-database access.
- [ ] 3.4 Implement the dedicated identity-configuration job with narrowly scoped Keycloak Admin API credentials and rejection rules for wildcard callbacks or realm-management project roles, and verify valid client configuration is idempotent while invalid configuration is rejected.
- [ ] 3.5 Register distinct test-project and ProcessManager clients with exact callbacks, separate audiences, groups, and SOPS-encrypted client/cookie Secrets, and verify neither client can use the other's secret or callback.

## 4. Reusable Project Access Chart

- [ ] 4.1 Implement `public` and `oidc-native` branches in `charts/web-process` so they route ingress directly to the application and never render oauth2-proxy, and verify Helm renders the expected resources for both policies.
- [ ] 4.2 Implement the pinned per-project oauth2-proxy dependency for `oidc-protected`, including exact issuer, callback, existing Secret reference, secure cookie settings, allowed Keycloak group, probes, and limits, and verify Helm rejects any missing protected-only value.
- [ ] 4.3 Render separate proxy and application Services plus default-deny and allow NetworkPolicies so ingress-nginx reaches only the proxy and only the proxy reaches the application, and verify rendered policy selectors match no unrelated workload.
- [ ] 4.4 Add chart contract tests for all valid policies and for missing policy, contradictory public OIDC fields, wildcard/non-HTTPS callbacks, shared or cross-project Secret names, and unsupported policy values; verify the chart test suite fails each invalid case.

## 5. ProcessManager Project Configuration and GitOps

- [ ] 5.1 In `/home/nacfson/Projects/ProcessManager`, replace the project `authEnabled` Boolean with tagged access-policy and OIDC configuration types, then verify table-driven validation covers every valid and invalid contract case.
- [ ] 5.2 Update the project form and handlers to collect policy-specific fields and render actionable validation errors, and verify handler/template tests cover `public`, `oidc-protected`, and `oidc-native` submissions.
- [ ] 5.3 Add deterministic HelmRelease values generation and signed, non-force Git commits to the remote `main` head, including bounded non-fast-forward regeneration, and verify a project change modifies only its allowed deployment-repository path.
- [ ] 5.4 Replace project/process create, update, scale, and delete Kubernetes mutations with Git desired-state operations and reduce the Kubernetes client surface to observation, then verify manager RBAC and client interfaces expose no application mutation verbs.
- [ ] 5.5 Correlate the committed Git revision with Flux Kustomization and HelmRelease conditions and the required proxy/application readiness, and verify ProcessManager reports committed, reconciling, ready, and failed as distinct states.

## 6. ProcessManager Authentication Cutover

- [ ] 6.1 In `/home/nacfson/Projects/ProcessManager`, implement trusted-proxy identity middleware that rejects absent identity, parses immutable issuer/subject and groups, and enforces the ProcessManager administrator group; verify spoofed or malformed identity headers are rejected by middleware tests.
- [ ] 6.2 Split project and audit operations from the existing `AuthStore`, remove manager runtime calls to custom session/user endpoints, and verify `cmd/manager` starts without an auth-service URL, auth internal token, or auth database credential.
- [ ] 6.3 Store audit actors by issuer and subject with display attributes treated as mutable, and verify an email change preserves the same actor identity while identical subjects from different issuers remain distinct.
- [ ] 6.4 In `Project_HW`, deploy ProcessManager behind its dedicated oauth2-proxy and Keycloak client with a NetworkPolicy that prevents direct ingress access, and verify administrator-group access succeeds while a project-only user receives `403`.

## 7. Protected-Project Integration and Legacy Removal

- [ ] 7.1 Register and deploy a representative `oidc-protected` project through the two-step identity configuration plus ProcessManager flow, and verify anonymous redirect, allowed access, denied-group `403`, Secret use, and successful application response.
- [ ] 7.2 Attempt reserved-header spoofing and direct ingress-to-application traffic against the representative project, and verify the spoofed value is removed and the bypass connection is denied.
- [ ] 7.3 Delete the Keycloak pod and verify protected routes fail closed until Kubernetes starts one ready replacement, then separately stop the identity database and verify protected routes remain closed, ProcessManager reports the dependency failure, and a public project remains reachable.
- [ ] 7.4 Invalidate legacy personal sessions without importing legacy accounts, passwords, or identity associations; create fresh Google-backed identities, explicitly assign only required project groups, and verify first login grants no group or administrator privilege automatically.
- [ ] 7.5 Remove `cmd/auth`, custom login/registration/password/session production routes and templates, `k8s/auth`, bootstrap-password tooling, obsolete auth Secrets, and related deployment scripts from `/home/nacfson/Projects/ProcessManager`; verify no production endpoint can authenticate through the legacy service and the remaining Go tests pass.

## 8. Recovery and Production Acceptance

- [ ] 8.1 Rotate one project's Keycloak client and oauth2-proxy cookie secrets atomically and verify that project recovers with new sessions while unrelated projects remain authenticated and unchanged.
- [ ] 8.2 Restore the CNPG identity database to a selected timestamp in a replacement cluster and verify Keycloak starts with accounts and client registrations from that recovery point.
- [ ] 8.3 Recreate the application cluster from the off-server deployment-repository mirror, SOPS recovery key, CNPG backups, and registry images, and verify public, protected, ProcessManager, and Keycloak routes satisfy their documented behavior.
- [ ] 8.4 Load at least 100,000 synthetic broker-linked personal identities through a test-only acceptance harness that is unavailable in the production realm, then exercise platform-controlled login completion, token refresh, logout, 5,000 concurrent sessions, email change, and paginated administrator search at 5 login completions and 50 token refreshes per second; verify p95 platform authentication latency remains below 500 ms and issuer/subject associations remain correct without including Google-controlled browser latency.
- [ ] 8.5 Reconcile `ARCHITECTURE.md` with the implemented identity behavior, run strict OpenSpec validation and the cross-repository unit, chart, manifest, and end-to-end checks, and verify every scenario in both identity capability specs has passing evidence before marking the change complete.
