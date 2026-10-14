# Research & Architectural Decisions: Backend-First Headless Login Logic & Shared Project Authentication

**Feature**: `009-host-scoped-shared-auth`
**Date**: 2026-10-14

## 1. 4-Layer Cookie-Free BFF + Internal Gateway/Keycloak Session Architecture

- **Decision**: Adopt the **4-Layer Backend-For-Frontend (BFF) + Internal Gateway/Keycloak Sealed Session (`session_handle`)** pattern (`Frontend ➔ Project Backend ➔ Internal Gateway/Keycloak ➔ Project Backend ➔ Frontend`) with **zero browser cookies**.
- **Rationale**:
  - Previously, `auth.nacfson.cloud/oauth/callback` attempted to emit a `Set-Cookie: PLATFORM_SESSION=...; Domain=pn.nacfson.cloud` header, which browsers silently drop under RFC 6265 §5.3 because a sibling subdomain (`auth.*`) is forbidden from setting cookies for another sibling subdomain (`pn.*`).
  - Furthermore, storing PKCE verifiers in a short-lived `OIDC_AUTH_STATE` cookie on `auth.*` failed with `missing or expired auth state` whenever browser privacy shields, clock skew, or back-button reloads occurred.
  - By delegating the entire Login UI to the Project Frontend and having the Project Frontend communicate strictly with its own Project Backend (`/api/auth/*`), which in turn communicates with `gateway.identity.svc:8080` over the private cluster network, we eliminate 100% of browser cookies while keeping all OAuth access and refresh tokens sealed inside the internal `identity` namespace.
- **Alternatives Considered**:
  - *Cross-subdomain parent cookie (`Domain=.nacfson.cloud`)*: Rejected because it leaks session credentials across all sibling project subdomains and violates strict per-project isolation.
  - *Host-only cookie handoff on the project domain*: Rejected per user clarification (`"the project doesn't have to store cookie; frontend => Backend => keycloak or gateway => Backend => frontend"`), as cookie-free JSON + `Authorization: Bearer <session_handle>` is simpler, immune to browser cookie policies, and works identically for web SPAs and native/Tauri clients.
  - *Exposing raw Keycloak JWT/Refresh tokens directly to Frontend JavaScript*: Rejected because refresh tokens and Keycloak session tokens should remain sealed inside the internal Gateway/Keycloak boundary.

---

## 2. Stateless Encrypted PKCE State (`state` Parameter Without `OIDC_AUTH_STATE` Cookie)

- **Decision**: Encrypt and authenticate the PKCE `code_verifier`, project `return_url`, project `audience`, and `timestamp` directly inside the OAuth `state` query parameter using AES-256-GCM (keyed by `SHA-256(HMACSecret)` from OpenBao Vault, with `HMACSecretPrevious` fallback for zero-downtime key rotation) and a 10-minute TTL (`600s`).
- **Rationale**:
  - Eliminates the `OIDC_AUTH_STATE` browser cookie completely while preserving full cryptographic PKCE verification (`S256` challenge sent to Keycloak/Google, encrypted verifier decrypted only by `Gateway` upon `/oauth/callback`).
  - If a user reloads or presses Back on a consumed callback URL whose `state` still decrypts to an allowed project return URL, `Gateway` gracefully redirects back to restart login or return to the project's login page instead of trapping the user on a raw `400 missing or expired auth state` text page.
- **Alternatives Considered**:
  - *Keeping `OIDC_AUTH_STATE` cookie with `Max-Age=600`*: Still vulnerable to in-app WebViews, strict privacy blockers, and cross-tab login flows. Stateless AES-256-GCM encrypted `state` has zero browser storage dependencies.

---

## 3. Direct Google Redirect (`kc_idp_hint=google`) & Project-Owned Login UI

- **Decision**: The pipeline provides **headless login logic only**. When the Project Frontend initiates sign-in via the Project Backend (`GET/POST /api/auth/login`), the internal Gateway builds the Keycloak authorization URL with `kc_idp_hint=google` so Keycloak immediately redirects the browser to Google account selection without rendering any Keycloak username/password login screen.
- **Rationale**:
  - Enforces User Story 1 & FR-001: Hosted projects own 100% of their login page UI, while the pipeline acts strictly as a headless authentication engine.
- **Alternatives Considered**:
  - *Custom Keycloak HTML login theme*: Unnecessary maintenance burden when the project already renders its own login UI and uses Google as the upstream identity provider.

---

## 4. Pod-Restart Resilient Sealed `session_handle` & One-Time Callback Handoff

- **Decision**:
  1. When Google returns to `auth.<domain>/oauth/callback`, `Gateway` exchanges the authorization code with Keycloak (`keycloak-service.identity.svc:8080`) and seals `{ SessionID, Subject, Issuer, Audience, AccessToken, RefreshToken, CreatedAt, ExpiresAt }` using AES-256-GCM + HMAC with its Vault secret into an **opaque `session_handle`**.
  2. `Gateway` redirects the browser to the Project Backend callback (`https://<project-host>/api/auth/callback?ticket=<sealed-handoff-ticket>`).
  3. The Project Backend (via the Shared Verification Module) exchanges the `ticket` with `http://gateway.identity.svc:8080/auth/exchange` (or verifies it via `/auth`), receives `{ session_handle, subject, issuer, audience }`, provisions/loads the user's project account and requirements in PostgreSQL, stores a 30-second one-time pickup code in the Project Backend, and redirects the browser tab to the Project Frontend (`https://<project-host>/?code=<pickup-code>`).
  4. The Project Frontend calls `POST /api/auth/session` with `{ "code": "<pickup-code>" }` to receive `{ session_handle, user, requirements }` as pure JSON, and sends `Authorization: Bearer <session_handle>` on all subsequent `/api/*` requests.
- **Rationale**:
  - Because `session_handle` is cryptographically sealed with the Gateway's Vault secret and backed by Keycloak's PostgreSQL session store, **restarting or scaling the `gateway` pod in Kubernetes never logs out active users**.
  - Because `/api/auth/callback` redirects back to the Frontend UI with a 30-second pickup code exchanged via `POST /api/auth/session`, the browser never displays raw JSON on screen and never puts long-lived tokens in URL history.
- **Alternatives Considered**:
  - *In-memory session map in `gateway` pod RAM*: Rejected because pod restarts or rolling updates in Kubernetes would wipe active sessions.

---

## 5. Canonical Issuer Validation & Active Token Refresh in Internal Gateway

- **Decision**:
  - **Canonical Issuer**: `Gateway` compares `session.Issuer` against `canonicalIssuer = cfg.KeycloakPublicURL` (falling back to `cfg.KeycloakIssuerURL`), accepting either the canonical public URL (`https://auth.nacfson.cloud/realms/platform`) or internal URL (`http://keycloak-service.identity.svc:8080/realms/platform`) configured for that environment.
  - **Active Session Validation & Transparent Refresh**: Instead of passing `session.SessionID` (a UUID) to `/protocol/openid-connect/token/introspect` (which expects an OAuth token and always returned `active: false` for a UUID), `Gateway` introspects the sealed `AccessToken` (or checks the JWT `exp` claim + introspects/refreshes via `ExchangeTokenForProject(session.RefreshToken, audience)` when the short-lived access token is expired). If the refresh token is also rejected by Keycloak (session revoked or logged out), `Gateway` returns `401 Unauthorized`. If refreshed, `Gateway` optionally returns the rotated `X-Refreshed-Session-Handle` header so the backend/frontend can update it seamlessly.
- **Rationale**:
  - Directly fixes the two internal Gateway bugs identified in live cluster testing while maintaining strict fail-closed online revocation semantics (Constitution Principle III).

---

## 6. Shared, Language-Agnostic Verification Contract & Go Package (`gateway/pkg/projectauth`)

- **Decision**:
  - Standardize the internal HTTP contract on `gateway.identity.svc:8080` using plain HTTP + JSON/Headers:
    - `GET /oauth/start?return=<https://project-host/api/auth/callback>` (or internal `POST /auth/login-url`)
    - `POST /auth/exchange` (exchanges callback handoff `ticket` for `{ session_handle, subject, issuer, audience }`)
    - `GET /auth` (verifies `Authorization: Bearer <session_handle>` + `X-Forwarded-Host: <project-host>`, returning `200 OK` with `X-User-Subject`, `X-User-Issuer`, `X-Project-Audience`, and optional `X-Refreshed-Session-Handle`)
    - `POST /auth/logout` (revokes the Keycloak session inside `session_handle` synchronously via Keycloak Admin API)
  - Provide a zero-dependency Go 1.22 standard-library reusable package (`gateway/pkg/projectauth`) implementing `/api/auth/login`, `/api/auth/callback`, `/api/auth/session`, `/api/auth/logout`, and the `RequireAuth` middleware for Go project backends, while allowing non-Go projects (Python, Node, Rust) to call the exact same HTTP endpoints without protobuf/gRPC toolchains.
- **Rationale**:
  - Complies with Non-Negotiable Engineering Rule 1 (Zero Third-Party Go Dependencies) and FR-008/FR-009.
