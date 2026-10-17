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
