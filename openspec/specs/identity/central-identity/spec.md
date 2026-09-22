# Central Identity Specification

## Purpose

Defines the platform identity authority, account-data isolation, client separation, and recoverability required for shared authentication across independently deployed projects.

## Requirements

### Requirement: Central OIDC authority
The platform SHALL expose Keycloak at an HTTPS issuer under `auth.<domain>` using one `platform` realm as the authoritative OAuth 2.0 and OpenID Connect identity service for ProcessManager and participating projects.

#### Scenario: OIDC discovery is available
- **WHEN** a client requests the `platform` realm OpenID Provider Configuration from the public issuer URL
- **THEN** the platform returns valid discovery metadata whose issuer exactly matches the configured HTTPS issuer

#### Scenario: Public project remains independent
- **WHEN** Keycloak is unavailable
- **THEN** a project configured with the `public` access policy remains reachable without invoking the identity service

### Requirement: Google-only personal account authentication
The initial release SHALL authenticate ordinary personal accounts exclusively by brokering Google OpenID Connect through Keycloak. Any Google account that completes the brokered flow MAY establish a fresh platform identity, but the realm MUST disable local self-registration, password login, and password recovery for ordinary users and MUST NOT grant a new account any project group or administrator role by default. Projects SHALL continue to trust only the platform Keycloak issuer.

#### Scenario: Google account signs in for the first time
- **WHEN** an ordinary user completes the Google OpenID Connect flow with an account not previously linked to the platform
- **THEN** Keycloak creates a fresh platform identity linked to the validated Google issuer and subject without assigning project membership or administrator privilege

#### Scenario: Ordinary user attempts local password authentication
- **WHEN** an ordinary user attempts to register, authenticate, or recover an account with a local Keycloak password
- **THEN** the platform does not offer or complete that credential flow

#### Scenario: Project receives brokered identity
- **WHEN** a Google-backed user authenticates to a participating project
- **THEN** the project receives tokens or trusted proxy identity from the platform Keycloak issuer and does not trust a Google token directly

#### Scenario: Recovery administrator requires local access
- **WHEN** Google authentication or broker configuration is unavailable
- **THEN** the separately protected local recovery administrator remains available through the restricted administrative path and ordinary users are not given a local-password fallback

### Requirement: Single-node recoverable identity topology
The initial production platform SHALL run one Keycloak replica and one CloudNativePG instance on its single k3s node and MUST NOT claim workload, node, or database high availability. Kubernetes SHALL restart a failed Keycloak pod, while protected authentication SHALL fail closed until the replacement is ready.

#### Scenario: Keycloak pod terminates
- **WHEN** the running Keycloak pod terminates
- **THEN** protected login and refresh operations fail closed until Kubernetes starts a ready replacement using the existing identity database

#### Scenario: Additional same-node replica is proposed
- **WHEN** an operator proposes another Keycloak replica without adding an independent schedulable node
- **THEN** the production configuration rejects the replica increase because it does not survive the shared node failure domain

### Requirement: Independent client security boundaries
The platform MUST assign a distinct Keycloak client to ProcessManager and to every `oidc-protected` or `oidc-native` project, with client-specific audiences, credentials, callback URIs, and authorization policy.

#### Scenario: Protected project client is registered
- **WHEN** an identity administrator registers a protected project
- **THEN** its client identifier, secret, callback URI, and allowed group are distinct from every other project and from ProcessManager

#### Scenario: Wildcard callback is rejected
- **WHEN** a proposed client configuration contains a wildcard redirect URI
- **THEN** the identity configuration is rejected and is not applied to Keycloak

#### Scenario: Project client requests administration access
- **WHEN** a project client is configured with realm-management or Keycloak Admin API privileges
- **THEN** the identity configuration is rejected

### Requirement: Account database isolation
Only Keycloak SHALL possess credentials and network access to the dedicated identity PostgreSQL database. No project, ProcessManager, oauth2-proxy, Forgejo, CI workload, or ingress component SHALL receive database credentials or a permitted database network path.

#### Scenario: Project attempts database access
- **WHEN** a pod in a project namespace attempts to connect to the identity database Service on TCP 5432
- **THEN** the connection is denied by network policy

#### Scenario: Keycloak accesses account data
- **WHEN** Keycloak starts with its generated database credential
- **THEN** it can establish a TLS-protected connection to its dedicated database

#### Scenario: Non-Keycloak workload inspects its credentials
- **WHEN** a non-Keycloak workload's mounted Secrets and environment are inspected
- **THEN** no identity-database credential is present

### Requirement: Stable external identity reference
Applications that persist a reference to a platform account MUST identify that account by the validated OIDC issuer and subject pair and MUST NOT use email address or a private identity-table key as the immutable identifier.

#### Scenario: User email changes
- **WHEN** an account's email claim changes while its issuer and subject remain unchanged
- **THEN** the application continues to associate the account with the same local user data

#### Scenario: Subjects from different issuers match
- **WHEN** two validated identities have the same subject value but different issuers
- **THEN** an application treats them as different external identities

### Requirement: Identity administrator protection
Day-to-day Keycloak administrators MUST authenticate through Google OpenID Connect and complete a Keycloak-controlled TOTP step-up selected by administrator role. One restricted local recovery administrator MUST use a local password plus TOTP and MUST NOT be used for routine administration. Project clients MUST NOT inherit ProcessManager administrator membership or privileges.

#### Scenario: Day-to-day administrator authenticates without Keycloak TOTP
- **WHEN** a Google-backed account with an administrator role completes only the Google primary authentication
- **THEN** administrative access is denied until the account completes the Keycloak-controlled TOTP step-up

#### Scenario: Recovery administrator authenticates during Google outage
- **WHEN** Google authentication or broker configuration is unavailable and an operator uses the restricted recovery path
- **THEN** Keycloak requires both the local recovery password and TOTP before granting recovery administration access

#### Scenario: Recovery administrator is used routinely
- **WHEN** the local recovery administrator is used while the normal Google-backed administrator path is healthy
- **THEN** the login is recorded as a security event requiring operator review

#### Scenario: Project member lacks ProcessManager role
- **WHEN** a user belongs to a project's allowed group but not the ProcessManager administrator group
- **THEN** that membership does not authorize access to ProcessManager

### Requirement: Production account population acceptance
Before production acceptance, the Keycloak-controlled identity path MUST be exercised with at least 100,000 synthetic broker-linked personal identities, 5 login completions per second, 50 token refreshes per second, and 5,000 concurrent sessions while maintaining p95 platform authentication latency below 500 ms. The latency objective excludes time spent in Google's browser and identity-provider systems.

#### Scenario: Account population exercise
- **WHEN** a test-only acceptance harness that is unavailable in the production realm applies the target login-completion, refresh, and concurrent-session workload to at least 100,000 synthetic broker-linked identities
- **THEN** Keycloak-controlled login completion, token refresh, and logout maintain p95 latency below 500 ms, paginated administrator search completes without error, and no identity corruption or cross-account association occurs

#### Scenario: Account email changes at scale
- **WHEN** a synthetic account changes its email address during the account population exercise
- **THEN** its issuer and subject remain stable and its existing application association is preserved

### Requirement: Identity data recovery
The identity database MUST support encrypted off-server base backups, continuous WAL archiving, defined retention, and tested point-in-time recovery. A retained local volume alone MUST NOT be considered a backup.

#### Scenario: Point-in-time restore
- **WHEN** an operator selects a recoverable timestamp and restores the identity database into a replacement cluster
- **THEN** Keycloak starts against the restored database with accounts and client registrations reflecting that recovery point

#### Scenario: Local volume is lost
- **WHEN** the application VPS and local identity volume are unavailable
- **THEN** identity data can be restored from off-server backup material without access to the lost volume

### Requirement: Identity failure is closed
Protected authentication SHALL fail closed when Keycloak or the identity database cannot establish or validate the required identity operation.

#### Scenario: New login during identity outage
- **WHEN** a user starts login for an `oidc-protected` project while Keycloak or its database is unavailable
- **THEN** the request does not reach the protected application

#### Scenario: Account database unavailable
- **WHEN** Keycloak cannot reach its database
- **THEN** the platform reports the identity service unhealthy and does not bypass authentication
