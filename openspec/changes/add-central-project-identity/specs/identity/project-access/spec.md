# Spec Delta

## Purpose

Defines how projects select public or OIDC-based access, how protected ingress is reconciled, and which authentication responsibilities remain outside ProcessManager.

## ADDED Requirements

### Requirement: Explicit project access policy
Every managed project SHALL select exactly one access policy from `public`, `oidc-protected`, or `oidc-native`. ProcessManager MUST reject any missing or unsupported policy before changing the deployment repository.

#### Scenario: Supported policy is selected
- **WHEN** an administrator submits a project with one supported access policy and all policy-specific fields
- **THEN** ProcessManager validates the configuration for that policy

#### Scenario: Access policy is absent
- **WHEN** an administrator submits a project without an access policy
- **THEN** ProcessManager rejects the change and does not commit it to `main`

#### Scenario: Unsupported policy is selected
- **WHEN** an administrator submits an access policy outside the supported set
- **THEN** ProcessManager rejects the change and identifies the invalid policy

### Requirement: Public access bypasses platform login
A project using `public` SHALL route HTTPS requests from ingress-nginx directly to the project Service without requiring an OIDC client or oauth2-proxy.

#### Scenario: Anonymous public request
- **WHEN** an anonymous visitor requests a healthy public project hostname over HTTPS
- **THEN** ingress-nginx forwards the request to the project without redirecting to Keycloak

#### Scenario: Public project contains protected-only configuration
- **WHEN** a public project configuration includes an oauth2-proxy client Secret reference or allowed Keycloak group
- **THEN** ProcessManager rejects the contradictory configuration

### Requirement: Protected access uses a dedicated proxy
A project using `oidc-protected` MUST have a dedicated oauth2-proxy, Keycloak client, client Secret reference, cookie secret, exact HTTPS callback URI, and allowed Keycloak group that are not shared with another project or ProcessManager.

#### Scenario: Complete protected configuration
- **WHEN** a protected project references an existing encrypted Secret manifest and a registered client with an exact callback URI
- **THEN** ProcessManager may commit the validated access-policy configuration

#### Scenario: Protected configuration is incomplete
- **WHEN** the client identifier, Secret reference, callback URI, or allowed group is missing
- **THEN** ProcessManager rejects the change and does not commit it

#### Scenario: Client registration is not ready
- **WHEN** the protected project's Keycloak client has not been registered before reconciliation
- **THEN** the project is not reported ready and protected traffic is not forwarded to the application

### Requirement: Protected routing cannot bypass authentication
For an `oidc-protected` project, ingress-nginx SHALL route external requests only to that project's oauth2-proxy, and NetworkPolicy SHALL allow application ingress from that proxy while denying direct ingress-nginx access to the application Service.

#### Scenario: Authenticated allowed user
- **WHEN** a user has a valid OIDC session and belongs to the project's allowed Keycloak group
- **THEN** oauth2-proxy forwards the request to the project application

#### Scenario: Anonymous user
- **WHEN** a user without a valid session requests the protected hostname
- **THEN** the user is redirected to the project's Keycloak authorization flow and the request does not reach the application

#### Scenario: Authenticated disallowed user
- **WHEN** a valid Keycloak user does not belong to the project's allowed group
- **THEN** access is denied with `403` and the request does not reach the application

#### Scenario: Direct ingress bypass attempt
- **WHEN** ingress-nginx attempts to connect directly to the protected application Service
- **THEN** NetworkPolicy denies the connection

### Requirement: Trusted identity headers
The protected request path MUST remove client-supplied identity headers before oauth2-proxy sets the documented trusted upstream identity headers.

#### Scenario: Client spoofs identity header
- **WHEN** an external request supplies a header reserved for authenticated identity
- **THEN** the supplied value is removed and cannot determine the identity received by the application

#### Scenario: Proxy supplies authenticated identity
- **WHEN** oauth2-proxy forwards a request after successful authentication and group authorization
- **THEN** the application receives only the trusted identity values derived from the validated session

### Requirement: Native OIDC remains application-owned
A project using `oidc-native` SHALL receive direct ingress traffic and SHALL implement its own OIDC Authorization Code flow with PKCE, token validation, sessions, and application authorization.

#### Scenario: Native project configuration
- **WHEN** an administrator configures `oidc-native` with a client identifier and exact callback URIs
- **THEN** the deployment does not add the platform oauth2-proxy and routes traffic to the application

#### Scenario: Native application validates identity
- **WHEN** the application accepts an ID token
- **THEN** it validates issuer, audience, signature, expiry, and the flow's state and nonce before establishing a session

### Requirement: ProcessManager is configuration-only for authentication
ProcessManager SHALL validate and commit access-policy configuration but MUST NOT verify user credentials or sessions, issue or validate OIDC tokens, query the identity database, administer Keycloak accounts or groups, register clients, or receive Keycloak administrative credentials.

#### Scenario: User accesses a protected project
- **WHEN** a user authenticates to a protected project
- **THEN** Google verifies the ordinary user's primary identity, Keycloak establishes the platform session, and oauth2-proxy verifies the project session and allowed group without invoking ProcessManager

#### Scenario: Administrator configures a project
- **WHEN** an administrator selects `oidc-protected` in ProcessManager
- **THEN** ProcessManager validates only the hostname, policy, client identifier, callback URI, Secret-manifest reference, and allowed group

#### Scenario: Client registration is requested through ProcessManager
- **WHEN** a request asks ProcessManager to create or modify a Keycloak client or account
- **THEN** ProcessManager rejects the operation as outside its responsibility

### Requirement: GitOps reconciliation of access resources
A committed project access policy SHALL be reconciled by Flux through the reusable application chart. ProcessManager MUST NOT directly create or mutate the resulting Kubernetes authentication resources.

#### Scenario: Protected policy is committed
- **WHEN** a valid `oidc-protected` configuration reaches `main`
- **THEN** Flux reconciles the application, dedicated oauth2-proxy, Services, Ingress, certificate, Secret reference, and NetworkPolicies

#### Scenario: Reconciliation fails
- **WHEN** any required protected-access resource fails to reconcile or become healthy
- **THEN** ProcessManager reports the project as failed or reconciling rather than successfully deployed

### Requirement: Production custom authentication is removed
The production request path for ProcessManager and protected projects MUST NOT use the legacy custom Go password and session service after migration.

#### Scenario: Migration is complete
- **WHEN** a user accesses ProcessManager after the identity migration
- **THEN** authentication is performed through the dedicated oauth2-proxy and Keycloak client

#### Scenario: Legacy authentication endpoint is requested
- **WHEN** a production request targets a removed custom login or registration endpoint
- **THEN** the endpoint does not authenticate or create an account
