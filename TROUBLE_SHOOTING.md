# Troubleshooting Guide: OAuth Callback Session Confirmation & Identity Claim Errors

**Companion to**: [`README.md`](README.md), [`docs/architecture-auth-routing.md`](docs/architecture-auth-routing.md), and [`docs/project-auth-verification.md`](docs/project-auth-verification.md)  
**Scope**: Root-cause analysis, Google OAuth 2.0 / OIDC numbered flow mapping, and permanent prevention rules (including the **Main-Domain Naming Rule**) for callback failures between hosted project backends (`pn-backend`) and the platform authentication gateway (`gateway.identity.svc:8080`).

---

## 1. Symptoms & Exact Error Logs

During the OAuth login callback (`GET /api/auth/callback?ticket=...`) on `Project PN`, the backend (`auth_handler.go:190-194` / `gateway_client.go:198-200`) logs one of two errors:

1. **`gateway rejected session handle with status 401`**
   ```text
   [AUTH_FAILURE] gateway session confirmation on callback failed from 10.42.0.1: gateway rejected session handle with status 401
   ```
   - **Who rejected it**: The internal **Platform Gateway** (`GET http://gateway.identity.svc:8080/auth` in [`gateway/internal/auth/handler.go`](gateway/internal/auth/handler.go)) returned `HTTP 401 Unauthorized`.

2. **`gateway returned invalid identity claims`**
   ```text
   [AUTH_FAILURE] gateway session confirmation on callback failed from 10.42.0.1: gateway returned invalid identity claims
   ```
   - **Who rejected it**: The **Platform Gateway** returned `HTTP 200 OK`, **but** the **Project Backend** (`parseConfirmedIdentityResponse()` in `gateway_client.go:198-200`) rejected the response headers/claims (`ident == nil`) because the audience (derived from host), issuer, subject, or email check failed.

---

## 2. Plain-Language Explanation (Without Code or Jargon)

1. **Google sign-in itself succeeded**:
   The user signed in with Google, Google confirmed their identity to the platform's central login broker (Keycloak), and the platform Gatekeeper (`gateway`) created a valid login pass and handed a one-time ticket to your App (`Project PN`).
2. **Ticket exchange succeeded, but the immediate double-check failed**:
   Your App traded that one-time ticket for the login pass (Step 1 succeeded), and then immediately asked the Gatekeeper a second time to verify the pass and return the user's ID card (Step 2 failed).
3. **Why Step 2 failed (Two Root Causes)**:
   - **Cause A — Public Main Domain Name vs. Internal Server Name Mismatch**:
     - When you sign in through the browser, your login pass is stamped with your **public main domain name** (`pn.nacfson.cloud` $\rightarrow$ `project-pn`).
     - When your App checks that pass inside the cluster network, if the request uses an **internal server name** (`pn-backend` or `gateway.identity.svc`) instead of the configured public domain name (`pn.nacfson.cloud`), the derived name becomes `project-pn-backend` instead of `project-pn`.
     - Because `"project-pn-backend"` does not match `"project-pn"`, either the Gatekeeper rejects the pass (`401`), or the Gatekeeper returns `"project-pn"` and your App rejects it as a mismatch (`gateway returned invalid identity claims`).
   - **Cause B — Missing Email on the Internal Pass (Contract & Image Drift)**:
     - Your App requires three fields on the confirmed identity response: **Issuer**, **Subject (User ID)**, and **Email** (`X-User-Email`).
     - When the Gatekeeper does not include `X-User-Email` (either because the Gatekeeper container is running a pre-fix image digest or because `email` was only present in the OIDC ID token and not extracted), your App treats the `200 OK` response as incomplete and logs `gateway returned invalid identity claims`.

---

## 3. Architecture Explained by Numbered Ordering (Google Official OAuth 2.0 / OIDC Flow)

In [Google's official OpenID Connect / OAuth 2.0 Web Server documentation](https://developers.google.com/identity/openid-connect/openid-connect), the server flow has **6 numbered steps**. Below is how `nacfson_pipeline` executes each numbered step across the Project Frontend, Project Backend, Platform Gateway, and Keycloak:

### **1. Create an Anti-Forgery State Token & Set Authorization Parameters**
* **Google Documentation Rule**: Generate a unique anti-forgery `state` token (and PKCE verifier/challenge) before redirecting the browser.
* **Platform Execution**:
  1. **1.1 (`pn-frontend` $\rightarrow$ `pn-backend`)**: The user clicks Sign In on `https://pn.nacfson.cloud`, calling `GET https://pn.nacfson.cloud/api/auth/login`.
  2. **1.2 (`pn-backend` $\rightarrow$ `Platform Gateway`)**: `pn-backend` redirects the browser to `GET https://auth.nacfson.cloud/oauth/start?return=https://pn.nacfson.cloud/api/auth/callback`.
  3. **1.3 (`Platform Gateway`)**: In [`gateway/internal/auth/handler.go`](gateway/internal/auth/handler.go), the Gateway generates a PKCE `verifier` and `challenge`, seals `StatePayload` (`target_url`, `nonce`, `ts`, `vd`) using AES-256-GCM into `state`, and sets the host-only `OIDC_AUTH_STATE` binding cookie.

### **2. Send an Authentication Request to Google**
* **Google Documentation Rule**: Redirect the user's browser to `https://accounts.google.com/o/oauth2/v2/auth` with `client_id`, `response_type=code`, `scope=openid email profile`, `redirect_uri`, and `state`.
* **Platform Execution**:
  1. **2.1 (`Platform Gateway` $\rightarrow$ `Keycloak`)**: The Gateway redirects the browser to `https://auth.nacfson.cloud/realms/platform/protocol/openid-connect/auth` with `client_id=gateway-client`, `redirect_uri=https://auth.nacfson.cloud/oauth/callback`, `state=<gateway_state>`, `code_challenge=<challenge>`, and `kc_idp_hint=google`.
  2. **2.2 (`Keycloak` $\rightarrow$ `Google`)**: Because `kc_idp_hint=google` is set, Keycloak ([`deploy/platform/identity/keycloak-realm-config.yaml`](deploy/platform/identity/keycloak-realm-config.yaml)) skips its own login screen, generates its broker `state`, and redirects the browser to `https://accounts.google.com/o/oauth2/v2/auth` with `redirect_uri=https://auth.nacfson.cloud/realms/platform/broker/google/endpoint`.

### **3. Google Prompts User for Consent / Sign-In**
* **Google Documentation Rule**: Google authenticates the user and prompts for account selection / consent.
* **Platform Execution**:
  1. **3.1 (`Browser` on `accounts.google.com`)**: The user selects their Google account directly on Google's sign-in page.

### **4. Handle the OAuth 2.0 Server Response & Confirm Anti-Forgery State Token**
* **Google Documentation Rule**: Google redirects the browser back to the registered `redirect_uri` with `?code=...&state=...`, and the receiving server confirms `state`.
* **Platform Execution**:
  1. **4.1 (`Google` $\rightarrow$ `Keycloak`)**: Google redirects the browser to `https://auth.nacfson.cloud/realms/platform/broker/google/endpoint?code=<google_code>&state=<kc_state>`.
  2. **4.2 (`Traefik` $\rightarrow$ `Keycloak`)**: Traefik ([`deploy/platform/ingress/ingress-allowlist.yaml`](deploy/platform/ingress/ingress-allowlist.yaml)) routes `/realms/platform/broker/` to `keycloak-service.identity.svc:8080`, where Keycloak validates `<kc_state>`.

### **5. Exchange Code for Access Token and ID Token**
* **Google Documentation Rule**: The confidential server sends a `POST` request to `https://oauth2.googleapis.com/token` to exchange `code` for tokens.
* **Platform Execution**:
  1. **5.1 (`Keycloak` $\leftrightarrow$ `Google`)**: Keycloak sends `<google_code>` and `${GOOGLE_CLIENT_SECRET}` to `https://oauth2.googleapis.com/token` and receives Google's `id_token` and `access_token`.
  2. **5.2 (`Keycloak` $\rightarrow$ `Platform Gateway`)**: On first Google SSO, Keycloak runs `google jit first broker login` and creates the local user (AUTH-01) before minting `<keycloak_code>`. Subsequent logins reuse `FederatedIdentity(google, sub)`. Keycloak then stores the root user session in PostgreSQL and redirects the browser to `https://auth.nacfson.cloud/oauth/callback?code=<keycloak_code>&state=<gateway_state>`.
  3. **5.3 (`Platform Gateway` $\leftrightarrow$ `Keycloak`)**: The Gateway verifies `<gateway_state>` and the PKCE `verifier`, then calls `POST http://keycloak-service.identity.svc:8080/realms/platform/protocol/openid-connect/token` ([`gateway/internal/keycloak/oidc.go`](gateway/internal/keycloak/oidc.go)) to exchange `<keycloak_code>` for a Keycloak JWT (`access_token`) and `refresh_token`.

### **6. Obtain User Information from the Token & Authenticate the User**
* **Google Documentation Rule**: Extract the verified user identity claims (`iss`, `sub`, `email`), provision/load the user in the app database, and establish the app session.
* **Platform Execution (Where the Failure Occurs)**:
  1. **6.1 (`Platform Gateway` $\rightarrow$ `pn-backend`)**: The Gateway seals `{sid, sub, iss, aud: "project-pn", tokens, rt}` into `v1.<session_handle>`, issues a 60-second single-use `ticket`, and redirects the browser to `https://pn.nacfson.cloud/api/auth/callback?ticket=<ticket>`.
  2. **6.2 (`pn-backend` $\leftrightarrow$ `Platform Gateway` `POST /auth/exchange`) — SUCCEEDS**:
     `pn-backend` exchanges `ticket` at `http://gateway.identity.svc:8080/auth/exchange` and receives `200 OK` with `{session_handle, subject, issuer, audience: "project-pn"}`.
  3. **6.3 (`pn-backend` $\leftrightarrow$ `Platform Gateway` `GET /auth`) — FAILS HERE (`auth_handler.go:190-194`)**:
     `pn-backend` immediately calls `GET http://gateway.identity.svc:8080/auth` (`ConfirmSessionHandle`) with `Authorization: Bearer <session_handle>`:
     - **Failure Mode 1 (`401`)**: `HandleForwardAuth` rejects the handle if `X-Forwarded-Host` resolves to an internal host instead of `pn.nacfson.cloud`, or if Keycloak `/token/introspect` returns `active: false` (e.g., when `KC_HOSTNAME_URL` was not set on Keycloak).
     - **Failure Mode 2 (`gateway returned invalid identity claims`)**: `HandleForwardAuth` returns `200 OK`, but `parseConfirmedIdentityResponse(resp, effectiveHost)` in `gateway_client.go:198-200` returns `ident == nil` because `effectiveHost` was `"pn-backend"` (deriving `"project-pn-backend"` $\neq$ `"project-pn"`) or `X-User-Email` was missing from the response headers.
  4. **6.4 (`pn-backend` $\rightarrow$ `pn-frontend`)**:
     Once 6.2/6.3 succeeds, `pn-backend` redirects to `https://pn.nacfson.cloud/?code=<30s_pickup_code>`, and `pn-frontend` exchanges `code` at `POST /api/auth/session` for `{session_handle, user, requirements}`.

---

## 4. Technical Breakdown of the Two Failure Modes

| Failure Mode | Code Location | Exact Condition Triggering the Error |
| :--- | :--- | :--- |
| **`gateway rejected session handle with status 401`** | [`gateway/internal/auth/handler.go`](gateway/internal/auth/handler.go) (`HandleForwardAuth`) | 1. **Host/Audience mismatch (`handler.go:150-154`)**: `deriveProjectAudience(targetHost)` does not match `session.Audience` (`"project-pn"`) because `X-Forwarded-Host` was missing or set to an internal service name.<br/>2. **Keycloak Introspection (`handler.go:165-209`)**: `/token/introspect` returned `{"active": false}` and token refresh failed (fixed by setting `KC_HOSTNAME_URL` and `KC_HOSTNAME_STRICT_BACKCHANNEL=false` on Keycloak).<br/>3. **Issuer mismatch (`handler.go:145-148`)**: `session.Issuer` did not match `KEYCLOAK_PUBLIC_URL` or `KEYCLOAK_ISSUER_URL`. |
| **`gateway returned invalid identity claims`** | `Project_PN` `gateway_client.go:198-200` (`parseConfirmedIdentityResponse`) | `GET /auth` returned `200 OK`, but `parseConfirmedIdentityResponse(resp, effectiveHost)` returned `nil` because:<br/>1. **Audience mismatch**: `DeriveProjectAudience(effectiveHost)` derived `"project-pn-backend"` from an internal `r.Host` instead of `"project-pn"` from `pn.nacfson.cloud`.<br/>2. **Missing `X-User-Email`**: Gateway container was pinned to a pre-PR-#42 digest (`sha256:3a9d08b3...`) instead of the post-PR-#42 digest (`sha256:55ae4634...`), or `email` was not present in the access token.<br/>3. **Missing/invalid `Authorization: Bearer <jwt>`, `X-User-Subject`, or `X-User-Issuer`**. |

---

## 5. Permanent Prevention Rules (How to Prevent This Issue in the Future)

To guarantee this issue cannot recur when adding new projects or updating services, enforce these four rules:

### Rule 1 (Primary): Domain-Based Naming Rule (Always Use the Configured Main Domain)
* **Never** derive project identity, audience, or `X-Forwarded-Host` from raw internal Kubernetes request headers (`r.Host` such as `pn-backend`, `pn-backend.proj-pn.svc:8080`, or `gateway.identity.svc:8080`).
* **Always** derive the project name and audience deterministically from the project's configured public domain under the platform's main domain (`PUBLIC_APP_URL`):
  - **Main Platform Domain**: `nacfson.cloud` (production `vps-k3s`) / `example.com` (`local-k3s`)
  - **Auth Broker Domain**: `auth.<main-domain>` (`https://auth.nacfson.cloud`)
  - **Project Public Domain (`PUBLIC_APP_URL`)**: `https://<project-slug>.<main-domain>` (e.g., `https://pn.nacfson.cloud`)
  - **Canonical Project Host (`X-Forwarded-Host`)**: `<project-slug>.<main-domain>` (e.g., `pn.nacfson.cloud`)
  - **Canonical Project Audience (`X-Project-Audience`)**: `project-<project-slug>` (e.g., `project-pn`)

### Rule 2: Complete Identity in One Step on Ticket Exchange
* `POST /auth/exchange` should return the complete verified user identity (`session_handle`, `subject`, `issuer`, `audience`, and `email`) so `/api/auth/callback` does not need a redundant second network hop (`GET /auth`) just to read user claims.

### Rule 3: Single Shared Verification Contract
* Hosted Go backends should use the shared verification logic ([`gateway/pkg/projectauth/middleware.go`](gateway/pkg/projectauth/middleware.go)) rather than maintaining custom per-project header parsers that can drift from `gateway/internal/auth/handler.go`.

### Rule 4: Explicit Per-Field Error Messages & Post-Merge Image Digest Pinning
* Never collapse multiple validation checks into `ident == nil` with a generic `"gateway returned invalid identity claims"` message. Log the exact field that failed (`audience mismatch: got X want Y`, `missing X-User-Email`, `issuer mismatch`).
* When modifying `gateway/` source code, wait for `.github/workflows/build-images.yaml` to publish the new multi-arch image on `main`, then pin the resulting `@sha256:<digest>` in [`deploy/platform/identity/gateway-deployment.yaml`](deploy/platform/identity/gateway-deployment.yaml).
