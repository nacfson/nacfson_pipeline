# Contract: Shared Project Authentication Verification Module

**Feature**: `009-host-scoped-shared-auth`
**Go Package**: `nacfson_pipeline/gateway/pkg/projectauth` (Go 1.22 stdlib-only, zero external dependencies)
**Language-Agnostic Standard**: Any project backend in any language (Go, Python, Node, Rust) implements or imports this exact 4-endpoint BFF + middleware contract over standard HTTP.

---

## 1. Project Backend Endpoints Exposed to Project Frontend (`/api/auth/*`)

The Shared Verification Module mounts the following endpoints on the Project Backend so the Project Frontend only ever communicates with its own `/api/*` backend:

### 1.1 `GET /api/auth/login` — Start Sign-In
- **Called by**: Project Frontend when the user clicks "Sign In with Google" on the project's login page.
- **Query Parameters**:
  - `next` (optional, default `/`): Relative path on the project frontend to return to after sign-in.
- **Behavior**:
  - Constructs the callback URL `https://<PUBLIC_APP_HOST>/api/auth/callback?next=<next>`.
  - Redirects (`302 Found`) to `https://<AUTH_PUBLIC_HOST>/oauth/start?return=<url_encoded_callback_url>`.

### 1.2 `GET /api/auth/callback` — Receive Pipeline Handoff & Prepare Project User
- **Called by**: Browser redirect from `auth.<domain>/oauth/callback?ticket=<ticket>`.
- **Behavior**:
  1. Reads `ticket` query parameter. If missing or invalid, redirects (`302 Found`) back to the project login page (`PUBLIC_APP_URL`).
  2. Calls internal Gateway `POST http://gateway.identity.svc:8080/auth/exchange` with `{"ticket": "<ticket>"}` and `X-Forwarded-Host: <PUBLIC_APP_HOST>`.
  3. Receives `{ session_handle, subject, issuer, audience }`. Verifies `audience == ExpectedAudience` (e.g., `project-pn`).
  4. Invokes the project's optional `UserResolver(ctx, issuer, subject)` hook to load or provision the user's project account and requirements in PostgreSQL.
  5. Stores `{ session_handle, user }` under a 30-second single-use random `code` in memory and redirects (`302 Found`) the browser tab to `https://<PUBLIC_APP_HOST><next>?code=<code>` (with **zero cookies**).

### 1.3 `POST /api/auth/session` — Frontend Exchanges Pickup Code for JSON
- **Called by**: Project Frontend JavaScript upon seeing `?code=<code>` in the URL after returning from login.
- **Request Body**:
  ```json
  {
    "code": "<one-time-pickup-code>"
  }
  ```
- **Response (`200 OK`)**:
  ```json
  {
    "session_handle": "<opaque-session-handle>",
    "subject": "f47ac10b-58cc-4372-a567-0e02b2c3d479",
    "issuer": "https://auth.nacfson.cloud/realms/platform",
    "user": { ... }
  }
  ```
- **Error Response (`401 Unauthorized`)**: If `code` is unknown, expired (`>30s`), or already used once.

### 1.4 `POST /api/auth/logout` — Revoke Session
- **Called by**: Project Frontend with `Authorization: Bearer <session_handle>`.
- **Behavior**:
  - Forwards `POST http://gateway.identity.svc:8080/auth/logout` with `Authorization: Bearer <session_handle>`.
  - Returns `200 OK` `{"status":"revoked"}`.

---

## 2. Protected Route Verification Middleware (`RequireAuth`)

Used on all protected Project Backend routes (such as `GET /api/auth/me` and `/api/*`):

1. **Header Sanitization**: Immediately deletes any client-supplied `X-User-Subject` and `X-User-Issuer` headers so spoofed headers are never trusted.
2. **Bearer Extraction**: Reads `Authorization: Bearer <session_handle>`. If missing, returns `401 Unauthorized` `{"error":"unauthorized","message":"missing authorization token"}`.
3. **Internal Gateway Verification**:
   - Sends `GET http://gateway.identity.svc:8080/auth` with `Authorization: Bearer <session_handle>` and `X-Forwarded-Host: <PUBLIC_APP_HOST>`.
   - Verify `200 OK` and `X-Project-Audience == ExpectedAudience`.
   - If `X-Refreshed-Session-Handle` is returned by the Gateway, propagates `X-Refreshed-Session-Handle` on the response header to the Frontend.
   - Injects verified `(Issuer, Subject)` into the request context for downstream handlers.
   - Fails closed (`401 Unauthorized` or `503 Service Unavailable`) if Gateway rejects the handle or is unreachable.
