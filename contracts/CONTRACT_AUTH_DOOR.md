# CONTRACT_AUTH_DOOR: Project Door Separation, Sign-In Choice & Session Verification

**Status**: Enforced (`platform-preflight.py`, `gateway/`)  
**Governing Principles**: [Constitution Principle III & Amendment 2.0.0](../.specify/memory/constitution.md#L13-L15)  
**Related Specs**: [`specs/001-project-platform/`](../specs/001-project-platform/), [`specs/006-separate-project-auth/`](../specs/006-separate-project-auth/), [`specs/009-host-scoped-shared-auth/`](../specs/009-host-scoped-shared-auth/)

---

## 1. Direct Project Door Invariants

1. **No Gatekeeper on the Front Door**: Visitors reach a hosted project (`https://<slug>.nacfson.cloud`) directly through the project's own `IngressRoute`. Project routes MUST NOT attach `forward-auth` middleware.
2. **Mandatory Header Stripping**: Every route in a project `IngressRoute` MUST attach the `strip-forged-headers` middleware (`namespace: platform`) so external callers cannot spoof identity headers.
3. **Port Alignment**: Every service port referenced by a project `IngressRoute` MUST exist on a `ClusterIP` `Service` in `proj-<slug>` and MUST be explicitly permitted from the `platform` namespace in the project's `NetworkPolicy`.

---

## 2. Sign-In Mode Declaration (`deploy/projects/<slug>/sign-in.yaml`)

Each project declares its relationship with the platform sign-in service in `sign-in.yaml` (and `boundary/sign-in.yaml` `ConfigMap`):

| Mode | Meaning | NetworkPolicy Egress to `identity/gateway:8080` | Forbidden Env Vars |
| :--- | :--- | :--- | :--- |
| **`signIn: none`** | Project does not use platform sign-in. Remains 100% functional even if the auth gateway is down. | **Forbidden** (`check_project_doors` rejects any egress to `identity` or `gateway`) | `AUTH_JWT_JWKS_URL`, `CENTRAL_AUTH_URL`, `CENTRAL_AUTH_INTERNAL_URL`, `keycloak-service` |
| **`signIn: shared`** | Project owns its sign-in button/UI, initiates login via `/oauth/start`, and confirms sessions with the Go gateway on `:8080`. | **Permitted** (`to: identity/gateway` on port `8080` only; direct egress to `keycloak` is forbidden) | `AUTH_JWT_JWKS_URL`, `CENTRAL_AUTH_URL`, `CENTRAL_AUTH_INTERNAL_URL`, `keycloak-service` |

---

## 3. Shared Sign-In & Session Confirmation Protocol (`signIn: shared`)

1. **Browser Login Initiation**:
   - When an unsigned visitor clicks "Sign In" inside the project UI, the browser navigates to:
     `https://<AUTH_HOST>/oauth/start?return_to=https://<project-host>/<path>`
   - `<project-host>` MUST be listed in `PROJECT_RETURN_HOSTS` on the `gateway` Deployment ([`clusters/*/layers.yaml`](../clusters/vps-k3s/layers.yaml)).
2. **Backend Session Confirmation**:
   - The project backend verifies the visitor's session by calling `GET http://gateway.identity.svc:8080/auth` (`GATEWAY_AUTH_URL`), forwarding the incoming `Cookie` and `X-Forwarded-Host` (`<project-host>`).
   - If the gateway returns `401` or is unreachable, the project backend treats the visitor as unsigned-in (**fail-closed** for protected actions, while public project pages continue to work).
3. **Canonical Identity Tuple `(issuer, subject)`**:
   - Projects MUST key user records by `(issuer, subject)`—never by email address.
   - Authentication establishes *who* the visitor is, never implicit admin or business permissions.
4. **Online Current-Session Revocation**:
   - CSRF-protected `POST https://<AUTH_HOST>/auth/logout` revokes the active session in Keycloak and invalidates subsequent session checks across the cluster immediately.
   - The Keycloak Admin delete MUST authenticate as `session-revocation-client` with `SESSION_REVOCATION_CLIENT_SECRET`. It MUST NOT use `gateway-client`, client-provisioning credentials, or the master admin credential. That service account MUST hold the realm-management role required to delete one session (`manage-users`).
   - Success (`200` with `status: revoked`, or `302`) is allowed only after the `session-revocation-client` delete succeeds.
   - An authenticated session whose session id is empty is `400` `{"error":"invalid_session","message":"Malformed session"}`. It is not a revocation attempt.
   - If `RevokeSession` returns an error, including a refused delete or a failed token request, return `502` `{"error":"revocation_failed","message":"Keycloak session deletion failed"}`.
   - If the revocation client is not configured (`Revoker` nil because `SESSION_REVOCATION_CLIENT_SECRET` is unset), return `503` `{"error":"revocation_unavailable","message":"Session revocation client is not configured"}`.
   - Those three responses MUST NOT contain `status: revoked`, MUST NOT clear the platform cookie, and MUST NOT set `Location`, even when `redirect_uri` is allowlisted.
   - The same `session_handle` stays active. The next `GET /auth` MUST still confirm that session until a later delete succeeds.
   - **Logout Redirect Allowlist**: The optional `redirect_uri` (form or query) is honored only if it passes the same check as sign-in: `https` scheme, no userinfo, and a host listed in `PROJECT_RETURN_HOSTS` (`AllowedReturnHosts`). A `302` is returned only after the delete succeeds; failure responses MUST NOT set `Location`. Protocol-relative (`//host`), `javascript:`, other schemes, and unlisted hosts MUST NOT be returned as `Location`. The gateway ignores an invalid `redirect_uri` and returns the `200` JSON confirmation (only after the delete succeeds). It MUST NOT redirect to it.
   - **Logout CSRF Origin**: The `Origin`/`Referer` check MUST accept only a host under the cookie domain or a registered project host. The `localhost` and `127.0.0.1` exemption MUST NOT apply in production.
   - A later spec note that logout always returns `200 {"status":"revoked"}` ([`specs/009-host-scoped-shared-auth/contracts/gateway-internal-auth-api.md`](../specs/009-host-scoped-shared-auth/contracts/gateway-internal-auth-api.md) §5) does not override this failure rule.

---

## 4. Google SSO Just-In-Time Provisioning (`AUTH-01`)

Keycloak, not the gateway and not a project, creates the platform user on first successful Google sign-in.

| Setting | Required value | Why |
| :--- | :--- | :--- |
| Google IdP `firstBrokerLoginFlowAlias` | `google jit first broker login` | Built-in `first broker login` is interactive (Review Profile / Confirm Link / password or email re-auth). Keycloak 24 ignores IdP `updateProfileFirstLoginMode`. |
| `idp-review-profile` config `update.profile.on.first.login` | `off` | No Keycloak profile form after Google returns. |
| `idp-create-user-if-unique` | `REQUIRED` | Creates the user when username and email are unused. Duplicate email/username fails closed instead of offering account linking. |
| Google IdP `trustEmail` | `true` | Google asserts `email_verified`; do not send a Keycloak verify-email action. |
| Google IdP `syncMode` | `IMPORT` | Import profile attributes on first login; later logins match the existing federated identity (`google` + Google `sub`). |
| Google IdP `linkOnly` | `false` | `true` would refuse to create users and require an admin-created account. |
| `VERIFY_PROFILE` required action | `enabled: false` | Keycloak 24 enables this by default and then refuses tokens (`invalid_grant` / "Account is not fully set up") when `firstName`/`lastName` are empty. |
| `registrationAllowed` | `false` | Public username/password registration stays closed. Broker JIT is independent of that switch. |

**Do not auto-link by email.** AUTH-01 forbids treating email as a permanent unique identifier or as sufficient evidence to attach a Google identity to an existing Keycloak user. The JIT flow therefore MUST NOT include `idp-auto-link`, `idp-confirm-link`, `idp-email-verification`, or `idp-username-password-form`. If a Keycloak user already owns that email, first Google login fails and an operator links the federated identity through the private admin path.

Subsequent Google logins skip first-broker-login because Keycloak already stores `FederatedIdentity(google, sub)`.

Source of truth: [`deploy/platform/identity/keycloak-realm-config.yaml`](../deploy/platform/identity/keycloak-realm-config.yaml). Config validation: [`tests/preflight/test_keycloak_jit_provisioning.py`](../tests/preflight/test_keycloak_jit_provisioning.py).

---

## 5. Session Lifetimes

How long a sign-in lasts. These are the only contracted values. The realm import and [`tests/preflight/test_keycloak_session_lifetimes.py`](../tests/preflight/test_keycloak_session_lifetimes.py) must match this table.

| Setting | Value | Resets? | Meaning |
| :--- | :--- | :--- | :--- |
| `accessTokenLifespan` | `300` | New token on each refresh | Project access token, 5 minutes. The gateway refreshes only after it expires. |
| `ssoSessionIdleTimeout` | `3600` | Yes, on every refresh | Keycloak session and refresh token, 1 hour without a refresh. |
| `ssoSessionMaxLifespan` | `36000` | No | Keycloak absolute cap, 10 hours from sign-in. |
| `revokeRefreshToken` | `false` | — | No refresh-token rotation. An older handle stays usable until its refresh token expires. |
| Gateway `SessionTimeout` | `8h` | No | Gateway absolute cap from sign-in (`gateway/internal/config/config.go`). It ends a session before Keycloak's 10 hours. |

1. **What renews a session**: only a request to a protected project API (wrapped in `projectauth.RequireAuth`) that reaches `GET /auth` after the access token has expired. The gateway then refreshes with Keycloak and returns `X-Refreshed-Session-Handle`. A check while the access token is still valid does not renew the Keycloak session.
2. **What does not renew a session**: static page loads, `/api/auth/*`, unprotected endpoints, and frontend-only actions (navigation over cached data, scrolling, typing before save).
3. **Resulting behavior**: a visitor who makes a protected API call at least once in each window between 5 and 60 minutes after the last refresh stays signed in up to 8 hours. With no such call for 60 minutes, the next call gets `REFRESH_TOKEN_ERROR` `Token is not active` in Keycloak, a `401` from the gateway, and a new Google sign-in.
4. **Applying a change**: `kc.sh start --import-realm` skips a realm that already exists, so editing the realm import does not change a running cluster. After merging, apply the values to the live realm with the procedure in [`docs/operations-runbook.md`](../docs/operations-runbook.md) §7, then confirm them with the read-only query there.
5. Client-level overrides (`client.session.idle.timeout`, `client.session.max.lifespan`, realm `clientSessionIdleTimeout`) MUST NOT be set. They would silently shorten this table.

Source of truth: [`deploy/platform/identity/keycloak-realm-config.yaml`](../deploy/platform/identity/keycloak-realm-config.yaml). Background: [`docs/issues/FIX_GithubIssue93.md`](../docs/issues/FIX_GithubIssue93.md).
