# Contract: Internal Authentication Gateway HTTP API

**Feature**: `009-host-scoped-shared-auth`
**Service**: `gateway.identity.svc:8080` (Internal Cluster HTTP) & `https://auth.<domain>` (Browser OAuth Redirect Hop)
**Protocol**: Standard HTTP/1.1 + JSON / HTTP Headers (Zero binary RPC or third-party SDK dependencies)

---

## 1. Headless Login Initiation (`GET /oauth/start`)

Redirects the user's browser directly to Google Sign-In (`kc_idp_hint=google`) with a stateless AES-256-GCM encrypted PKCE `state` parameter.

- **Caller**: Browser redirected from Project Backend `GET /api/auth/login`
- **Query Parameters**:
  - `return` (required): Full HTTPS URL on an allowed project host (typically `https://<project-host>/api/auth/callback?next=/`)
- **Behavior**:
  1. Validates `return` hostname against `PROJECT_RETURN_HOSTS` (`AllowedReturnHosts`). Returns `400 Bad Request` if not allowed.
  2. Generates PKCE `(code_verifier, code_challenge)` (`S256`).
  3. Encrypts `{ target_url, code_verifier, nonce, ts }` using AES-256-GCM keyed by the Gateway's Vault secret (`HMACSecret`) into the `state` parameter (10-minute TTL).
  4. **Sets zero cookies** (`OIDC_AUTH_STATE` is not required; a fallback host-only `Max-Age=600` cookie MAY be set only for backward compatibility with legacy tests, but decryption of `state` is primary and self-sufficient).
  5. Responds with `302 Found` to:
     `{KEYCLOAK_PUBLIC_URL}/protocol/openid-connect/auth?client_id=gateway-client&response_type=code&scope=openid+profile+email&redirect_uri=https://auth.<domain>/oauth/callback&state=<encrypted_state>&code_challenge=<challenge>&code_challenge_method=S256&kc_idp_hint=google`

---

## 2. OAuth Callback & Backend Handoff (`GET /oauth/callback`)

Receives the authorization code from Keycloak after Google Sign-In, seals the session into an opaque `session_handle`, and redirects to the Project Backend's `/api/auth/callback` with a one-time handoff `ticket`.

- **Query Parameters**:
  - `code`: OAuth authorization code from Keycloak
  - `state`: Encrypted/HMAC-signed state parameter
- **Behavior**:
  1. Decrypts and verifies `state` using `HMACSecret` (falling back to `HMACSecretPrevious`).
  2. **Graceful Reload / Back-Button Recovery (FR-004)**:
     - If `state` is expired (`> 600s`) or the one-time authorization `code` was already consumed by a prior request, and `state.TargetURL` is a valid allowed project host URL, responds with `302 Found` to `/oauth/start?return=<url.QueryEscape(state.TargetURL)>` instead of rendering a raw `400 missing or expired auth state` error page.
  3. Exchanges `code` + decrypted `code_verifier` with Keycloak (`http://keycloak-service.identity.svc:8080/realms/platform/protocol/openid-connect/token`).
  4. Seals `{ sid, sub, iss: CanonicalIssuer(), aud: deriveProjectAudience(returnHost), tokens: { aud: access_token }, rt: refresh_token, iat: now }` into an AES-256-GCM + HMAC opaque `session_handle`.
  5. Issues a short-lived (60s), single-use `ticket` bound to `returnHost` and redirects (`302 Found`) to `state.TargetURL` with `?ticket=<ticket>` appended (setting **zero** cross-subdomain `PLATFORM_SESSION` cookies).

---

## 3. Internal Ticket Exchange (`POST /auth/exchange`)

Called over the internal cluster network by a Project Backend (`/api/auth/callback`) to exchange the one-time callback `ticket` for the verified user identity and opaque `session_handle`.

- **Endpoint**: `POST http://gateway.identity.svc:8080/auth/exchange`
- **Request Headers**:
  - `Content-Type: application/json`
  - `X-Forwarded-Host: <project-host>` (e.g., `pn.nacfson.cloud`)
- **Request Body**:
  ```json
  {
    "ticket": "<one-time-handoff-ticket>"
  }
  ```
- **Success Response (`200 OK`)**:
  ```json
  {
    "session_handle": "<opaque-sealed-session-handle>",
    "subject": "f47ac10b-58cc-4372-a567-0e02b2c3d479",
    "issuer": "https://auth.nacfson.cloud/realms/platform",
    "audience": "project-pn",
    "expires_in": 28800
  }
  ```
- **Error Responses**:
  - `400 Bad Request`: Missing or malformed ticket
  - `401 Unauthorized`: Expired, already consumed, or wrong-host ticket

---

## 4. Internal Session Verification (`GET /auth`)

Called over the internal cluster network by a Project Backend on every protected `/api/*` request to verify `Authorization: Bearer <session_handle>`.

- **Endpoint**: `GET http://gateway.identity.svc:8080/auth`
- **Request Headers**:
  - `Authorization: Bearer <session_handle>`
  - `X-Forwarded-Host: <project-host>` (e.g., `pn.nacfson.cloud`)
- **Verification Logic**:
  1. Strips any inbound `X-User-Subject` or `X-User-Issuer` headers.
  2. Extracts `<session_handle>` from `Authorization: Bearer <session_handle>` (also supporting legacy cookie fallback only in unit tests if configured).
  3. Unseals and verifies `session_handle` signature/encryption, `SessionTimeout`, canonical issuer (`KEYCLOAK_PUBLIC_URL` or `KEYCLOAK_ISSUER_URL`), and project audience (`aud == deriveProjectAudience(targetHost)`).
  4. **Active Keycloak Validation & Transparent Refresh (FR-006)**:
     - Validates the sealed OAuth `AccessToken` against Keycloak token introspection (`/realms/platform/protocol/openid-connect/token/introspect`).
     - If the short-lived `AccessToken` is expired/inactive and `RefreshToken` is present, calls `ExchangeTokenForProject(RefreshToken, projectAudience)` to obtain a fresh access token and emits an updated `X-Refreshed-Session-Handle` header.
     - If Keycloak reports the session/refresh token as revoked or inactive, returns `401 Unauthorized`.
     - If Keycloak is unreachable, fails closed with `503 Service Unavailable`.
- **Success Response (`200 OK`)**:
  - `X-User-Subject: <sub UUID>`
  - `X-User-Issuer: <canonical issuer URL>`
  - `X-Project-Audience: project-<name>`
  - `X-Refreshed-Session-Handle: <new_session_handle>` *(optional, present when token was refreshed)*

---

## 5. Synchronous Session Logout (`POST /auth/logout`)

Terminates the user's active Keycloak session immediately across the cluster.

- **Endpoint**: `POST http://gateway.identity.svc:8080/auth/logout`
- **Request Headers**:
  - `Authorization: Bearer <session_handle>`
- **Behavior**:
  1. Unseals `session_handle` and extracts `SessionID` (`sid`).
  2. Calls Keycloak Admin API (`DELETE /admin/realms/platform/sessions/{sessionId}`) synchronously.
  3. Returns `200 OK` with `{"status":"revoked","message":"Platform session successfully terminated"}`.
