# Feature Specification: Backend-First Headless Login Logic & Shared Project Authentication

**Feature Branch**: `009-host-scoped-shared-auth`

**Created**: 2026-10-14

**Status**: Ready for Planning

**Input**: User description: "Direct Google sign-in without stopping at the intermediate identity-provider login form; headless pipeline login logic where the entire login page is delegated to the project side; backend-first authentication flow where the project frontend only communicates with its own project backend (/api/auth/*), and the project backend handles the pipeline login callback, verifies identity with the internal gateway, prepares project user requirements, and delivers authenticated state to the frontend without cross-subdomain platform cookies; fix internal authentication gateway session issuer and active-session validation; and provide a single shared, language-agnostic authentication verification contract and reusable module across all projects."

## Clarifications

### Session 2026-10-14

- Q: Should Spec 009 adopt the Backend-First flow where the Project Frontend only talks to its own Project Backend (`/api/auth/*`), and the Project Backend communicates with the internal pipeline gateway (`gateway.identity.svc:8080`) before sending the authenticated result to the frontend? → A: Option A — Backend-First Flow: Project Frontend only calls Project Backend (`/api/auth/*`); Project Backend receives the callback, verifies identity with internal `gateway:8080`, prepares project requirements/user data, and sends the authenticated state to the Frontend without pipeline-imposed cookies on the project.
- Q: How should the user's authenticated identity be held across Keycloak, the Internal Gateway, the Project Backend, and the Project Frontend without cookies? → A: Option A — 4-Layer BFF + Internal Gateway/Keycloak Sealed Session Handle (Zero Cookies): Keycloak holds the root session in PostgreSQL; Internal Gateway seals OAuth/refresh tokens into a Vault-encrypted opaque `session_handle` (and encrypts PKCE state without cookies); Project Backend (`/api/auth/callback`) exchanges the callback with Gateway, prepares project user data, redirects to Frontend with a one-time pickup code exchanged via `POST /api/auth/session` for `{ session_handle, user, requirements }`, and Frontend sends `Authorization: Bearer <session_handle>` on `/api/*`.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Pipeline Provides Headless Login Logic Only; Login Page Delegated to Each Project (Priority: P1)

Each hosted project owns and renders its entire login page and sign-in user interface. This pipeline provides **only the underlying headless login logic** (OAuth/OIDC redirect orchestration, stateless encrypted PKCE state verification, and internal session management) and never displays its own login page or an intermediate identity-provider (Keycloak) username/password form. When an unauthenticated visitor initiates sign-in from a project's login page, the request goes to the project's own backend (`/api/auth/login`), which triggers the pipeline's headless login logic to redirect straight to the external identity provider (e.g., Google account selection) and, upon completion, returns to the project backend (`/api/auth/callback`) with zero cookies and zero "missing or expired auth state" errors.

**Why this priority**: The pipeline is an authentication engine, not a user-facing login portal. Currently, when a project delegates sign-in to the pipeline, visitors are unexpectedly stopped at a pipeline-hosted Keycloak username/password login page and then fail to sign in due to expired/consumed authentication state cookies and rejected cross-subdomain cookies. Without enforcing that projects own the login UI and the pipeline executes only cookie-free headless login logic, no user can seamlessly log in through a project's web interface.

**Independent Test**: Start from the unauthenticated project's own login page, trigger sign-in via the project backend, verify the pipeline renders zero login pages of its own (redirecting straight from the project UI via the backend to Google sign-in), complete sign-in with zero cookies required, and confirm the project backend receives the callback and returns the user to the project frontend in an authenticated state on the first attempt.

**Acceptance Scenarios**:

1. **Given** an unauthenticated visitor on a project's own login page, **When** they trigger sign-in via the project backend (`/api/auth/login`), **Then** the pipeline executes only the headless login redirect directly to Google sign-in without rendering any pipeline-side or identity-broker login page.
2. **Given** a visitor returning from Google sign-in to complete authentication, **When** the pipeline's login callback logic completes, **Then** the pipeline forwards a one-time authentication handoff to the project backend (`/api/auth/callback`), which verifies the user with the internal gateway and returns the user to the project frontend as an authenticated user without "missing or expired auth state" errors.
3. **Given** a visitor who reloads or navigates back to a previously consumed sign-in callback address with a valid return destination, **When** the one-time sign-in state is no longer valid, **Then** the pipeline's login logic gracefully restarts the sign-in flow or returns the user to the project's login page rather than trapping them on a pipeline-side error or login screen.

---

### User Story 2 - 4-Layer Cookie-Free Identity Flow (`Frontend ➔ Backend ➔ Gateway/Keycloak ➔ Backend ➔ Frontend`) (Priority: P1)

Neither the project frontend nor the project backend stores OAuth tokens or cookies. Instead, authentication state is held internally by Keycloak and the internal pipeline gateway (`gateway.identity.svc:8080`), which seals the Keycloak session and OAuth/refresh tokens using its Vault secret key into an opaque `session_handle`. When Google sign-in finishes, `/api/auth/callback` on the project backend exchanges the handoff ticket with internal `gateway.identity.svc:8080`, prepares the user's project account and requirements in PostgreSQL, redirects the browser back to the project frontend UI with a short-lived one-time pickup code, and returns `{ session_handle, user, requirements }` as JSON via `POST /api/auth/session`. All subsequent requests follow `Frontend ➔ Backend ➔ Gateway/Keycloak ➔ Backend ➔ Frontend` using `Authorization: Bearer <session_handle>`.

**Why this priority**: Cross-subdomain cookies (`PLATFORM_SESSION`) and browser state cookies (`OIDC_AUTH_STATE`) are rejected or dropped by browsers, and exposing raw OAuth/refresh tokens to the frontend weakens security. Keeping all OAuth/Keycloak session state sealed inside Gateway/Keycloak while communicating along `Frontend ➔ Backend ➔ Gateway ➔ Backend ➔ Frontend` over JSON and HTTP headers eliminates 100% of cookie failures and survives Kubernetes pod restarts.

**Independent Test**: Complete sign-in on the project domain and verify that zero cookies (`OIDC_AUTH_STATE` or `PLATFORM_SESSION`) are required or set on the project domain, that the frontend exchanges the one-time pickup code at `POST /api/auth/session` for `{ session_handle, user }`, and that subsequent `/api/auth/me` and `/api/*` requests with `Authorization: Bearer <session_handle>` succeed through `Backend ➔ Gateway/Keycloak ➔ Backend ➔ Frontend`.

**Acceptance Scenarios**:

1. **Given** a user who completes Google sign-in, **When** the pipeline callback finishes at `auth.<domain>/oauth/callback`, **Then** the gateway exchanges the authorization code with Keycloak, seals the session and tokens into a Vault-encrypted opaque `session_handle`, and redirects to the project backend (`/api/auth/callback`) with a one-time handoff ticket and zero cookies.
2. **Given** the project backend receiving `/api/auth/callback`, **When** it exchanges the handoff ticket with `gateway.identity.svc:8080`, **Then** the project backend receives the verified `(issuer, subject)` and opaque `session_handle`, prepares the user's project data, redirects the browser to the frontend UI with a one-time pickup code, and delivers `{ session_handle, user, requirements }` as JSON when the frontend calls `POST /api/auth/session`.
3. **Given** an authenticated user on the project frontend, **When** the frontend requests any `/api/*` endpoint on its project backend with `Authorization: Bearer <session_handle>`, **Then** the project backend verifies the `session_handle` with `gateway.identity.svc:8080` and returns the project response without any cookies.
4. **Given** an active session verified by the internal authentication gateway, **When** the session issuer and online session status are checked (or when a short-lived access token inside the sealed handle expires), **Then** the gateway validates the canonical issuer, transparently refreshes the Keycloak token internally, and keeps the session active until expired or explicitly logged out.

---

### User Story 3 - Shared, Language-Agnostic Authentication Verification Across Projects (Priority: P2)

Every hosted project backend uses the same standardized authentication verification flow and shared verification module. All OAuth/OIDC sign-in orchestration, PKCE state encryption, session validation, and token refresh remain centralized in the internal authentication gateway, while any project backend (regardless of programming language or runtime) initiates login, exchanges callbacks, verifies opaque `session_handle` headers, and revokes sessions through a uniform internal HTTP contract.

**Why this priority**: As multiple projects are hosted on the platform, duplicating custom authentication logic or introducing heavy binary RPC toolchains creates maintenance drift and compatibility barriers. A single shared verification module and standard HTTP verification contract allow any project backend to integrate identically.

**Independent Test**: Verify that Project PN's backend and a second test consumer (or language-agnostic HTTP harness) initiate login, exchange callback tickets, and authenticate/reject `/api/*` requests identically using the shared verification module and standard internal HTTP endpoints without project-specific authentication forks.

**Acceptance Scenarios**:

1. **Given** a project backend written in Go, **When** it integrates platform authentication, **Then** it uses the shared authentication verification module rather than maintaining a custom project-specific implementation.
2. **Given** a project backend written in any language capable of standard HTTP requests, **When** it exchanges a login callback or verifies a user's `session_handle` against the internal authentication service, **Then** it succeeds using standard HTTP requests without requiring binary RPC compilers or language-specific SDKs.
3. **Given** a `session_handle` belonging to Project A, **When** it is presented to Project B's backend through the shared verification logic, **Then** access is denied because the sealed audience inside the handle does not match Project B.

---

### Edge Cases

- **Client clock skew or cookie blocking during sign-in**: Because the PKCE verifier and return metadata are sealed inside the encrypted/signed `state` parameter (rather than relying on a browser cookie), strict browser cookie blocking or client clock skew never causes `missing or expired auth state` errors.
- **Back-button or page reload on the callback URL**: If a user presses Back or reloads `/oauth/callback` or `/api/auth/callback` after the one-time code or ticket was already consumed, the system redirects cleanly to restart sign-in or return to the project's login page rather than displaying a dead-end `missing or expired auth state` text error.
- **Short-lived access token expiration during an active session**: When the short-lived Keycloak access token sealed inside `session_handle` expires before the overall session timeout, the internal authentication gateway transparently refreshes it with Keycloak using the sealed refresh token (and can return an updated `session_handle` in the response header if rotated) rather than falsely reporting the user as logged out.
- **Gateway pod restart in Kubernetes**: Because `session_handle` is cryptographically sealed with the Gateway's Vault secret and backed by Keycloak's PostgreSQL session store, restarting or scaling the `gateway` pod in Kubernetes does not log out active users.
- **Issuer URL representation differences (public vs. internal)**: When the browser initiates sign-in via the public broker URL while internal services communicate over private cluster addresses, session issuer checks recognize the canonical public realm issuer consistently.
- **Unreachable internal authentication service**: If the internal authentication service or identity provider is unreachable, protected operations fail closed to an unauthenticated state without crashing public endpoints.
- **Spoofed identity headers**: Client-supplied identity headers (`X-User-Subject`, `X-User-Issuer`, or forged `Authorization` headers) are stripped and never trusted without verification against the internal gateway.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The pipeline MUST provide only headless login logic and delegate the entire login page/UI to each hosted project; when sign-in is initiated from a project via its backend (`/api/auth/login`), the pipeline MUST instruct the identity broker (`kc_idp_hint=google`) to redirect the user directly to Google sign-in without rendering any pipeline-side or identity-provider username/password login page.
- **FR-002**: Authentication MUST follow the 4-layer cookie-free BFF flow (`Frontend ➔ Backend ➔ Internal Gateway/Keycloak ➔ Backend ➔ Frontend`): `auth.<domain>/oauth/callback` MUST seal the Keycloak session and tokens using the Gateway's Vault secret into an opaque `session_handle` and redirect to the project backend (`/api/auth/callback`) with a one-time handoff ticket; the project backend MUST exchange that ticket with `gateway.identity.svc:8080`, prepare the user's project data, redirect the browser to the project frontend with a short-lived one-time pickup code, and return `{ session_handle, user, requirements }` as JSON via `POST /api/auth/session` with zero cookies set on the project.
- **FR-003**: Sign-in PKCE state across the external Google OAuth redirect MUST be cryptographically encrypted and authenticated inside the `state` parameter using the Gateway's Vault secret (eliminating reliance on `OIDC_AUTH_STATE` browser cookies) with a 10-minute validity window so that browser cookie blocking, client clock skew, or Google 2FA prompts never cause `missing or expired auth state` errors.
- **FR-004**: When `/oauth/callback` or `/api/auth/callback` receives a request whose one-time code or handoff ticket is expired or already consumed, if the signed state contains an authentic, allowed project return URL, the service MUST redirect the browser to restart sign-in or return to the project's login page rather than rendering a raw `400 missing or expired auth state` error page.
- **FR-005**: The internal authentication gateway MUST validate session issuers consistently against the canonical public realm issuer (`KEYCLOAK_PUBLIC_URL` falling back to `KEYCLOAK_ISSUER_URL`) so that sessions created during browser sign-in are not rejected during internal verification.
- **FR-006**: Active session validation against the identity provider MUST verify valid OAuth tokens sealed inside `session_handle` (and transparently refresh expired short-lived access tokens using the sealed refresh token while the Keycloak session remains active) rather than passing a non-token session UUID to token introspection.
- **FR-007**: All internal communication between project backends, the authentication gateway, and the identity provider (login initiation, ticket exchange, `session_handle` verification, logout revocation, and public key retrieval) MUST occur over internal cluster addresses (`gateway.identity.svc:8080` and `keycloak-service.identity.svc:8080`) without requiring a public domain for service-to-service traffic.
- **FR-008**: Project-side authentication handling MUST be consolidated into a single reusable verification package so that every project backend using the same language shares identical source code for `/api/auth/*` endpoints (`/api/auth/login`, `/api/auth/callback`, `/api/auth/session`, `/api/auth/logout`), `session_handle` verification, audience enforcement, and header sanitization.
- **FR-009**: The authentication contract between project backends and the internal authentication gateway MUST rely solely on standard HTTP requests and standard JSON/header formats so that projects in any programming language can integrate without binary RPC frameworks or code generation toolchains.
- **FR-010**: Each project backend MUST only accept `session_handle` credentials explicitly scoped to its own project audience (`project-<name>`) and MUST reject handles scoped to any other project.
- **FR-011**: Project ingress routes and network policies MUST remain compliant with `platform-preflight` isolation rules (project doors reference only in-namespace services on allowed ports, and `signIn: shared` projects open egress only to `gateway` on port `8080`).

### Key Entities

- **Opaque Session Handle (`session_handle`)**: A Vault-encrypted, HMAC-authenticated token created by the internal gateway containing the user's Keycloak `session_id`, `(issuer, subject)`, project `audience`, and OAuth/refresh tokens. Opaque to the project frontend and backend; sent by the frontend as `Authorization: Bearer <session_handle>` on `/api/*` requests with zero cookies.
- **One-Time Handoff Ticket & Pickup Code**: Short-lived (30–60s), single-use cryptographic codes that bridge the browser redirect from `auth.<domain>/oauth/callback` ➔ Project Backend `/api/auth/callback` ➔ Project Frontend `POST /api/auth/session` without exposing credentials in URLs or using cookies.
- **Internal Authentication Gateway**: The cluster-internal Go standard-library service (`gateway.identity.svc:8080`) that holds the Vault key, orchestrates headless OAuth redirects, seals/verifies `session_handle` payloads, refreshes Keycloak tokens internally, and confirms `(issuer, subject)` for project backends over standard internal HTTP.
- **Shared Verification Module**: The unified backend authentication package used across projects to expose `/api/auth/*` endpoints, exchange one-time callback tickets with the internal gateway, strip forged headers, and verify `Authorization: Bearer <session_handle>` on `/api/*` routes.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: 100% of login page UI is owned and rendered by the project frontend, and 100% of sign-in initiations (`/api/auth/login`) execute headless pipeline login logic that redirects straight to Google sign-in with zero pipeline-side or identity-broker login pages presented to the user.
- **SC-002**: 100% of completed Google sign-in flows follow `Gateway ➔ Project Backend (/api/auth/callback) ➔ Project Frontend (POST /api/auth/session)` with zero cookies required or set on the project domain, landing the user on the project in an authenticated state on the first attempt.
- **SC-003**: 100% of subsequent requests from the project frontend to `/api/*` sub-paths using `Authorization: Bearer <session_handle>` follow `Frontend ➔ Backend ➔ Gateway/Keycloak ➔ Backend ➔ Frontend` and succeed as the authenticated user without any cookies.
- **SC-004**: 0% of authentication flows require or emit browser cookies (`PLATFORM_SESSION` or `OIDC_AUTH_STATE`).
- **SC-005**: Reloading or navigating back to a consumed callback URL with a valid signed return address results in 0 raw `missing or expired auth state` error pages shown to the user.
- **SC-006**: 100% of project backend authentication verification cases (valid ticket exchange, active `session_handle` check, transparent token refresh, pod-restart resilience, wrong project audience, forged headers, and session logout) pass using the single shared verification module over standard HTTP, and `platform-preflight` passes on both `local-k3s` and `vps-k3s`.

## Assumptions

- The public identity broker domain (`auth.nacfson.cloud`) remains registered as the single Authorized Redirect URI in Google Cloud Console for the platform's Google OAuth broker, acting strictly as an invisible redirect hop so new projects do not require manual Google Cloud Console reconfiguration.
- Project PN's existing learner accounts and `(issuer, subject)` identity bindings in PostgreSQL remain unchanged by this specification.
- Local development bypass (`mock-dev-token` when explicitly enabled in non-production environments) remains supported for offline local testing.
