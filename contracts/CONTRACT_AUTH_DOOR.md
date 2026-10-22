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
   - Success (`200` with `status: revoked`, or `302`) is allowed only after that delete succeeds. If the delete fails or the revocation client is unavailable, the gateway MUST NOT claim the session was revoked. It returns `502` or `503` with an error body. The platform cookie MAY still be cleared. The same `session_handle` stays active. The next `GET /auth` MUST still confirm that session. It MUST NOT be rejected as revoked until a later delete succeeds.
   - **Logout Redirect Allowlist**: The optional `redirect_uri` (form or query) is honored only if it passes the same check as sign-in: `https` scheme, no userinfo, and a host listed in `PROJECT_RETURN_HOSTS` (`AllowedReturnHosts`). Protocol-relative (`//host`), `javascript:`, other schemes, and unlisted hosts MUST NOT be returned as `Location`. The gateway ignores an invalid `redirect_uri` and returns the `200` JSON confirmation (only after the delete succeeds). It MUST NOT redirect to it.
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
