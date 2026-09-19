# Proposal

## Why

Projects need a consistent way to opt into shared authentication without making ProcessManager an authentication service or coupling applications to a shared account database. A central standards-based identity boundary is required before protected projects can be onboarded safely and repeatably.

## What Changes

- Deploy one Keycloak replica inside the single-node k3s platform as the OIDC authority for a centrally administered population planned for at least 100,000 personal accounts, backed by a dedicated one-instance CloudNativePG database; the identity service is recoverable but not highly available.
- Broker ordinary personal-account authentication exclusively through Google OpenID Connect in the initial release; any Google account may establish a fresh platform identity on first login, with no project or administrator privilege granted by default.
- Authenticate day-to-day administrators with Google OpenID Connect plus a Keycloak-controlled TOTP step-up required by administrator role; retain one restricted local password-plus-TOTP account only for emergency recovery.
- Make Keycloak the only application allowed to access the identity database; projects consume OIDC and never query account tables.
- Define three explicit project access policies: `public`, `oidc-protected`, and `oidc-native`.
- Give every protected project an independent Keycloak client, oauth2-proxy instance, callback URI, client secret, cookie secret, and allowed Keycloak group.
- Extend ProcessManager project configuration to select an access policy and reference pre-existing OIDC client configuration and SOPS-encrypted Secrets, while keeping identity administration outside ProcessManager.
- Reconcile protected-project ingress, oauth2-proxy, Services, certificate, and NetworkPolicies through Flux and the reusable application chart.
- Add declarative Keycloak realm/client configuration, CNPG backup and point-in-time recovery, and end-to-end access-policy verification.
- **BREAKING** Remove the existing custom Go password/session service from the production authentication path; protected access will depend on Keycloak OIDC.

## Capabilities

### New Capabilities

- `identity/central-identity`: Central Keycloak identity authority, dedicated CNPG persistence, account-data isolation, client boundaries, and recovery behavior.
- `identity/project-access`: Project access-policy configuration, protected-project onboarding, oauth2-proxy enforcement, and ProcessManager responsibility boundaries.

### Modified Capabilities

None. The project has no existing OpenSpec capability specifications.

## Impact

- Affects ProcessManager project configuration and validation, the reusable `web-process` Helm chart, Flux platform and application configuration, ingress-nginx routing, NetworkPolicies, SOPS Secrets, and recovery procedures.
- Adds Keycloak and CloudNativePG as non-HA platform dependencies and increases the application VPS planning baseline to 4 vCPU, 16 GB RAM, and 100 GB persistent SSD; production acceptance includes 100,000 synthetic broker-linked identities, 5 platform-controlled login completions per second, 50 token refreshes per second, 5,000 concurrent sessions, and p95 platform authentication latency below 500 ms, excluding Google-controlled browser latency.
- Introduces `identity-system` and `cnpg-system` namespaces plus one per-project oauth2-proxy for `oidc-protected` applications.
- Requires a clean cutover from the custom password/session path: ordinary users establish fresh Google-backed platform identities, legacy passwords and personal identity associations are not migrated, and distinct OIDC clients must exist before protected projects become ready.
