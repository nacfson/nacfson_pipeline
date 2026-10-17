# Handoff: Project PN — 4-Layer Cookie-Free BFF & Sealed `session_handle` (Spec 009)

**Audience**: Project PN application developers / Spec-Kit (`/speckit-specify`)
**Date**: 2026-10-14
**Platform Spec**: [`specs/009-host-scoped-shared-auth/spec.md`](spec.md)
**Platform Reference Implementation**: [`gateway/pkg/projectauth/middleware.go`](../../gateway/pkg/projectauth/middleware.go)
**Platform Status**: Merged to `nacfson_pipeline` `main` ([PR #40](https://github.com/nacfson/nacfson_pipeline/pull/40))

---

## 1. Summary & Architecture

`nacfson_pipeline` Spec 009 replaces cross-subdomain `PLATFORM_SESSION` browser cookies with a **4-layer cookie-free Backend-For-Frontend (BFF) + Internal Gateway/Keycloak sealed `session_handle` architecture**:

```text
Project PN Frontend (Login UI)
  │ 1. GET https://pn.nacfson.cloud/api/auth/login
  ▼
Project PN Backend
  │ 2. Sets host-only __Host-pa_nonce cookie (10m) & 302 redirects to:
  │    https://auth.nacfson.cloud/oauth/start?return=https://pn.nacfson.cloud/api/auth/callback?pa_bind=<sha256(nonce)>
  ▼
Pipeline Gateway & Keycloak (Headless Google OIDC)
  │ 3. Sets host-only OIDC_AUTH_STATE cookie on auth.nacfson.cloud & redirects directly to Google (kc_idp_hint=google)
  │ 4. On /oauth/callback, exchanges code with Keycloak, seals OAuth/refresh tokens into AES-256-GCM session_handle (v1.<ciphertext>),
  │    issues a 60s single-use ticket, and 302 redirects to:
  │    https://pn.nacfson.cloud/api/auth/callback?pa_bind=<sha256(nonce)>&ticket=v1.<ticket>
  ▼
Project PN Backend (/api/auth/callback)
  │ 5. Verifies __Host-pa_nonce == pa_bind (prevents Login CSRF) and clears __Host-pa_nonce
  │ 6. Calls internal POST http://gateway.identity.svc:8080/auth/exchange {"ticket": "..."}
  │    -> receives { session_handle: "v1....", subject, issuer, audience: "project-pn" }
  │ 7. Provisions/loads learner via EnsurePlatformUser, stores a 30s single-use pickup code, and 302 redirects to:
  │    https://pn.nacfson.cloud/?code=<32-char-hex-pickup-code>
  ▼
Project PN Frontend
  │ 8. Reads ?code=... on launch, scrubs URL via history.replaceState, and calls:
  │    POST https://pn.nacfson.cloud/api/auth/session {"code": "<32-char-hex>"}
  │    -> receives { token: "v1.<session_handle>", user, ... } and saves to sessionStorage
  │ 9. Sends Authorization: Bearer v1.<session_handle> on all /api/* requests
```

---

## 2. Platform Internal Gateway Contract (Already Live in `nacfson_pipeline`)

| Endpoint | Method | Caller | Request | Response |
| :--- | :--- | :--- | :--- | :--- |
| `https://auth.nacfson.cloud/oauth/start` | `GET` | Browser (via `/api/auth/login` redirect) | `?return=<url-encoded-project-backend-callback>` | Sets host-only `OIDC_AUTH_STATE` on `auth.nacfson.cloud`; `302 Found` to Keycloak with `kc_idp_hint=google` |
| `http://gateway.identity.svc:8080/auth/exchange` | `POST` | Project PN Backend (cluster-internal) | Header: `X-Forwarded-Host: pn.nacfson.cloud`<br>Body: `{"ticket": "v1...."}` | `200 OK` JSON:<br>`{"session_handle": "v1....", "subject": "<sub-uuid>", "issuer": "https://auth.nacfson.cloud/realms/platform", "audience": "project-pn", "expires_in": 28800}` |
| `http://gateway.identity.svc:8080/auth` | `GET` | Project PN Backend (cluster-internal) | Headers:<br>`Authorization: Bearer v1.<session_handle>`<br>`X-Forwarded-Host: pn.nacfson.cloud` | `200 OK` Headers:<br>`Authorization: Bearer <verified-keycloak-access-jwt>`<br>`X-User-Subject: <sub-uuid>`<br>`X-User-Issuer: https://auth.nacfson.cloud/realms/platform`<br>`X-Project-Audience: project-pn`<br>`X-Refreshed-Session-Handle: v1.<rotated>` *(optional)* |
| `http://gateway.identity.svc:8080/auth/logout` | `POST` | Project PN Backend (cluster-internal) | Header:<br>`Authorization: Bearer v1.<session_handle>` | `200 OK` JSON:<br>`{"status": "revoked"}` |

---

## 3. Required Changes in `Project_PN` Backend (`backend/`)

### 3.1 Add BFF Auth Endpoints (`backend/internal/http/auth_handler.go` & `server.go`)

Register three public endpoints on the Project PN Backend router (no auth middleware required on these three routes):

1. **`GET /api/auth/login`**:
   - Generate a 16-byte random hex `nonce` (`32` hex characters).
   - Set a host-only Login CSRF binding cookie on `pn.nacfson.cloud`:
     ```go
     http.SetCookie(w, &http.Cookie{
         Name:     "__Host-pa_nonce",
         Value:    nonce,
         Path:     "/",
         MaxAge:   600,
         HttpOnly: true,
         Secure:   true,
         SameSite: http.SameSiteLaxMode,
     })
     ```
   - Compute `bindDigest := base64.RawURLEncoding.EncodeToString(sha256(nonce))`.
   - Build callback URL: `${PUBLIC_APP_URL}/api/auth/callback?pa_bind=${bindDigest}` (plus optional sanitized `next` path).
   - Redirect (`302 Found`) to `${PUBLIC_AUTH_ORIGIN}/oauth/start?return=${url.QueryEscape(callbackURL)}` (where `PUBLIC_AUTH_ORIGIN` is derived from `CENTRAL_AUTH_URL` scheme+host, e.g. `https://auth.nacfson.cloud`).

2. **`GET /api/auth/callback`**:
   - Read `ticket := r.URL.Query().Get("ticket")` and `expectedBind := r.URL.Query().Get("pa_bind")`.
   - Verify `__Host-pa_nonce` cookie exists and matches `expectedBind` using `subtle.ConstantTimeCompare` (return `400 Bad Request` on mismatch to block Login CSRF).
   - Call `POST http://gateway.identity.svc:8080/auth/exchange` (i.e. `GATEWAY_AUTH_URL + "/exchange"`) with header `X-Forwarded-Host: pn.nacfson.cloud` and JSON body `{"ticket": ticket}`.
   - If ticket exchange fails or is expired/replayed, redirect gracefully (`302 Found`) back to `${PUBLIC_APP_URL}/`.
   - Clear `__Host-pa_nonce` (`MaxAge: -1`).
   - Verify `exch.Audience == "project-pn"`.
   - Confirm the session handle once against `GET http://gateway.identity.svc:8080/auth` (or decode the verified Keycloak JWT returned by `/auth`) to obtain email/claims and call `svc.EnsurePlatformUser(r.Context(), exch.Issuer, exch.Subject, email, "google")`.
   - Generate a 32-character hex single-use pickup `code` with a **30-second TTL**, store `{ SessionHandle: exch.SessionHandle, ExpiresAt: now.Add(30 * time.Second) }` in memory (evicting expired entries), and redirect (`302 Found`) to `${PUBLIC_APP_URL}/?code=${code}`.

3. **`POST /api/auth/session`**:
   - **Mandatory Security Gate (`CWE-770`)**: Cap request body size before decoding JSON:
     ```go
     r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
     var body struct {
         Code string `json:"code"`
     }
     if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(strings.TrimSpace(body.Code)) != 32 {
         writeError(w, http.StatusBadRequest, "invalid pickup code")
         return
     }
     ```
   - Atomically look up and delete `code` from the in-memory pickup map (single-use enforcement). If missing or expired, return `401 Unauthorized`.
   - Return `200 OK` JSON compatible with `@project-pn/api` `SessionResponse`:
     ```json
     {
       "token": "v1.<session_handle>",
       "refresh_token": "v1.<session_handle>",
       "expires_at": "2026-10-15T07:30:00Z"
     }
     ```

### 3.2 Support `Bearer v1.<session_handle>` in `gateway_client.go` & `auth_middleware.go`

1. In [`backend/internal/auth/gateway_client.go`](../../../Project_PN/backend/internal/auth/gateway_client.go):
   - Add `ConfirmSessionHandle(ctx context.Context, host, sessionHandle string) (*ConfirmedIdentity, string, error)`:
     - Sends `GET http://gateway.identity.svc:8080/auth` with headers:
       - `Authorization: Bearer ` + `sessionHandle`
       - `X-Forwarded-Host: ` + `host`
     - On `200 OK`, reads the Gateway's response `Authorization: Bearer <keycloak-jwt>`, `X-User-Subject`, `X-User-Issuer`, and optional `X-Refreshed-Session-Handle`.
     - Validates the returned `<keycloak-jwt>` audience (`project-pn`), subject, and issuer (using the existing `ConfirmIdentity` claim checks) and returns `ConfirmedIdentity` (where `ident.Token` is the underlying Keycloak JWT).
   - Add `ExchangeTicket(ctx context.Context, host, ticket string)` and `LogoutSessionHandle(ctx context.Context, sessionHandle string)` (`POST http://gateway.identity.svc:8080/auth/logout` with `Authorization: Bearer <sessionHandle>`).

2. In [`backend/internal/http/auth_middleware.go`](../../../Project_PN/backend/internal/http/auth_middleware.go):
   - When `token := bearerToken(r)` has prefix `"v1."` (a Vault-sealed `session_handle`) and `gateway != nil`:
     - Call `ident, refreshedHandle, err := gateway.ConfirmSessionHandle(r.Context(), r.Host, token)`.
     - If `ident == nil || err != nil`, return `401 Unauthorized` (`AUTH-101`).
     - If `refreshedHandle != ""`, set response header `w.Header().Set("X-Refreshed-Session-Handle", refreshedHandle)`.
     - Replace `token` for downstream JWT claim extraction (`verifier.Verify(r.Context(), ident.Token)`) while preserving the original `v1.<session_handle>` in `authSession` so `POST /api/auth/logout` can revoke it at the gateway!

3. In [`backend/internal/http/auth_handler.go`](../../../Project_PN/backend/internal/http/auth_handler.go) (`centralLogout`):
   - When `session.credential` starts with `"v1."`, call `gateway.LogoutSessionHandle(r.Context(), session.credential)` (`POST http://gateway.identity.svc:8080/auth/logout`).

---

## 4. Required Changes in `Project_PN` Frontend (`frontend/`)

### 4.1 Start Login via Project Backend (`frontend/src/screens/CentralAuthScreen.tsx`)
- In `handleSignIn()`, change browser web sign-in from redirecting to `auth.nacfson.cloud/oauth/start` to redirecting to the Project PN Backend:
  ```ts
  if (browserWeb) {
    window.location.href = `${API_BASE_URL}/api/auth/login`;
  }
  ```

### 4.2 Exchange `?code=` Pickup Code on Web Launch (`frontend/src/utils/authLaunch.ts`)
- Update `parseSessionFromLaunchUrl()` in [`frontend/src/utils/authLaunch.ts`](../../../Project_PN/frontend/src/utils/authLaunch.ts):
  - When `isBrowserWeb()` is true:
    1. Check `new URLSearchParams(window.location.search).get('code')`.
    2. If `code` is present:
       - Immediately remove `code` from the browser URL bar using `window.history.replaceState({}, document.title, cleanUrl)` so reloads do not re-submit the single-use code.
       - Call `POST ${API_BASE_URL}/api/auth/session` with body `JSON.stringify({ code })` and `Content-Type: application/json`.
       - If `200 OK`, return the parsed `SessionResponse` (`{ token, refresh_token, expires_at }`) so the existing startup flow in `App.tsx` saves it to `sessionStorage` and transitions to the authenticated screen.

---

## 5. Security Checklist for `Project_PN` Implementation (Cursor Audit Compliance)

To ensure `cursor[bot]` Pull Merge Audit passes with zero blocking findings on the `Project_PN` PR:
- [ ] **Bounded JSON Body (`CWE-770`)**: `POST /api/auth/session` MUST wrap `r.Body` with `http.MaxBytesReader(w, r.Body, 4<<10)` before `json.NewDecoder(r.Body).Decode(&body)` and verify `len(code) == 32`.
- [ ] **Login CSRF Binding (`CWE-352`)**: `GET /api/auth/login` MUST set `__Host-pa_nonce` (`HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode, Path: "/", MaxAge: 600`) and pass its SHA-256 base64url digest in `pa_bind` inside the `return` URL; `GET /api/auth/callback` MUST verify `__Host-pa_nonce` matches `pa_bind` via `subtle.ConstantTimeCompare` before calling `/auth/exchange`.
- [ ] **Single-Use 30s Pickup Code (`CWE-384`)**: `POST /api/auth/session` MUST delete the pickup code on first use and evict expired entries under a mutex.
- [ ] **No Hardcoded Base64 JWT Literals in Tests (`Gitleaks`)**: Construct test JWT strings at runtime using `base64.RawURLEncoding.EncodeToString` rather than `"hdr.eyJ..."` literals.
