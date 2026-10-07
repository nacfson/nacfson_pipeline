# Project Auth Verification Guide

**Audience**: AI agent (or developer) implementing a hosted project on this platform  
**Scope**: Project-side token verification and authorization only  
**Platform authority**: `SPEC.md` AUTH-03–AUTH-06, AC-01–AC-05, AC-12; contract `specs/001-project-platform/contracts/gateway-forwardauth.md`

This document tells a project how to **accept**, **verify**, and **authorize** users. The project does **not** implement browser login, Google OAuth, or Keycloak brokering. That is the platform Go gateway + Keycloak.

---

## 1. Trust path (do not invent another)

```text
Browser
  → https://<project>.<domain>   (Traefik)
    → ForwardAuth → Go gateway
         → Keycloak (session / project-scoped access token)
    → Project workload
         ← Authorization: Bearer <JWT>
         ← X-User-Subject: <sub>
         ← X-User-Issuer: <issuer>
```

| Layer | Responsibility |
|-------|----------------|
| Traefik + gateway | Login redirect, session cookie, session validity, inject headers |
| Keycloak | Identity issuer, Google broker, SSO session, JWT signing |
| **This project** | Verify JWT; map `(issuer, subject)` to ordinary/admin capabilities |

**Forbidden for the project**

- Talking to Keycloak or Google for browser login
- Trusting `X-User-*` or `Authorization` from the client without independent JWT verification
- Accepting Google access tokens or OIDC ID tokens as API credentials
- Minting or trusting business-role claims from the gateway
- Using email as a permanent unique user id or account-link key

---

## 2. What the project receives

On every authenticated request that Traefik forwards upstream, expect:

| Header | Meaning | Trust rule |
|--------|---------|------------|
| `Authorization: Bearer <jwt>` | Keycloak **access token** for **this** project audience | Verify signature + claims yourself |
| `X-User-Subject` | Keycloak `sub` (informational mirror) | Do **not** trust alone; must match JWT `sub` |
| `X-User-Issuer` | Keycloak realm issuer URL | Do **not** trust alone; must match JWT `iss` |

Strip or ignore any client-supplied identity headers before verification. Treat gateway-injected headers as hints only after the JWT validates.

Configure these values from deployment config (not hard-coded secrets in source):

| Config | Example / rule |
|--------|----------------|
| Expected issuer (`iss`) | `https://auth.<domain>/realms/platform` |
| Expected audience (`aud`) | This project's Keycloak client id (e.g. `project-pn`) |
| JWKS URL | Realm JWKS over **cluster network** (not public ingress). Example shape: `http://keycloak.identity.svc:8080/realms/platform/protocol/openid-connect/certs` |
| Allowed JWT alg | Explicit allowlist (typically `RS256`); reject others |

Public JWKS keys are not client secrets. Do not embed Keycloak private keys.

---

## 3. Mandatory JWT verification checklist (AUTH-04 / AC-04)

For each protected HTTP request (API and UI behind ForwardAuth):

1. **Presence**: `Authorization` header exists; scheme is `Bearer`; token is non-empty.
2. **Signature**: Verify with current JWKS; support key rotation (cache keys; refresh on unknown `kid`).
3. **Algorithm**: Reject tokens whose header `alg` is not in the allowlist (`none` forbidden).
4. **Issuer**: `iss` equals the configured realm issuer exactly.
5. **Audience**: Token is intended for **this** project (`aud` contains/equals the project client id). Reject tokens minted only for another project.
6. **Time**: Enforce `exp` (and `nbf` if present) with a small clock skew tolerance.
7. **Identity claims**: Require `sub`. Prefer establishing platform identity as the pair `(iss, sub)`.
8. **Credential type**: Reject ID tokens used as API access credentials. Accept only the Keycloak **access token** the gateway forwards for this project.
9. **Fail closed**: On any failure (missing header, bad sig, wrong aud, expired, JWKS unreachable), return **401/403** and do not execute business logic.

After success, platform identity = `(issuer, subject)` from the verified JWT. Email/display name may be informational only.

### Automated tests the project MUST ship

| Case | Input | Expected |
|------|-------|----------|
| Valid project access token | Correct `iss`, `aud`, sig, unexpired | 200 / authorized as ordinary (or admin if bound) |
| Wrong audience | Valid token for another project | Reject |
| Wrong issuer | Foreign `iss` | Reject |
| Bad signature | Tampered payload | Reject |
| Expired | `exp` in the past | Reject |
| ID token substituted | OIDC ID token instead of access token | Reject |
| Missing / forged headers | No Bearer, or only spoofed `X-User-Subject` | Reject |
| Key rotation | New JWKS `kid` after rotation | Still accepts after JWKS refresh |

---

## 4. Authorization after identity (AUTH-06 / AC-12)

Authentication ≠ authorization.

1. **Ordinary role (required)**: Declare explicit business capabilities (see project manifest `ordinaryRole.capabilities`). Missing declaration must fail deploy / fail closed at runtime — never “allow everything.”
2. **Default after first sign-in**: Any valid `(issuer, subject)` gets **ordinary** access only: landing page + declared ordinary capabilities. No Keycloak admin, no K8s admin, no app admin from registration alone.
3. **Deny by default**: Undeclared operations, other users’ private data, and admin operations are denied unless the ordinary-role declaration explicitly allows the capability (and even then, ordinary must not include other users’ private data or admin ops).
4. **Admin bindings**: Application administrator rights come only from **operator-provisioned**, project-scoped bindings to a verified `(issuer, subject)`. Store and enforce bindings **inside the project**. The gateway must not mint role claims.
5. **Identity key**: Bindings and user data keyed by `(issuer, subject)`, never by email alone.

### Authorization verification cases

| Case | Expected |
|------|----------|
| New user, valid token | Ordinary landing page; no other users’ private data |
| Ordinary user hits undeclared / admin API | Denied |
| Ordinary user requests another user’s private data | Denied |
| Operator admin binding for `(iss, sub)` on this project | Admin ops allowed for that identity only |
| Same identity without binding on another project | No admin there (when second project exists) |
| Valid token alone | Not sufficient for admin |

---

## 5. What the platform already verifies (do not re-implement login)

You may assume Traefik routes protected hosts through ForwardAuth. You still **must** verify the JWT (defense in depth; AUTH-04).

Platform-owned behaviors (gateway / Keycloak / ingress):

- Unauthenticated browser → redirect to Keycloak / Google (AC-01, AC-03)
- Shared SSO cookie across project hosts; per-project audience tokens (AC-02)
- Session revocation via gateway route; next request fails even if old JWT unexpired (AC-05)
- Fail closed when Keycloak/gateway unavailable (AC-03)

Project agents should **not** implement OIDC authorization-code + PKCE against Keycloak for the browser flow unless the platform explicitly assigns that work. Default: verify inbound Bearer JWT + enforce AUTH-06.

---

## 6. Suggested implementation shape

```text
Incoming request
  → Require Authorization Bearer
  → Verify JWT (JWKS, iss, aud, exp, alg, sub)
  → identity = (iss, sub)
  → Load ordinaryRole capabilities
  → If operation needs admin → require project admin binding for identity
  → If operation not in allowed set → deny
  → Else execute
```

Health endpoints used by Kubernetes probes may stay unauthenticated **only** if they expose no user data and are not publicly useful for bypass. Prefer cluster-internal probe paths consistent with the project’s ingress design.

---

## 7. Manual / integration smoke list (for the implementing agent)

Run against a deployed environment when available:

1. **AC-01**: First Google sign-in → ordinary landing; no admin rights.
2. **AC-03**: Unauthenticated curl to protected route → not 200 with app content; forged `X-User-Subject` does not impersonate.
3. **AC-04**: Unit/integration tests from §3 table all pass.
4. **AC-12**: Ordinary vs admin vs cross-user denial cases from §4 table pass.
5. **AC-05** (platform-led): After gateway logout/revoke, next project request fails even with a previously issued token — project must not skip online session checks the platform owns; project still rejects expired/invalid JWTs itself.

Record results (pass/fail + command) in the project’s own verification notes; do not commit secrets or live tokens.

---

## 8. References (read these, do not contradict them)

| Document | Use |
|----------|-----|
| `SPEC.md` §4 AUTH-01–AUTH-06, §11 AC-01–AC-05, AC-12 | Normative requirements |
| `specs/001-project-platform/contracts/gateway-forwardauth.md` | Headers, token audience, ForwardAuth behavior |
| `specs/001-project-platform/contracts/project-manifest-spec.yaml` | `ordinaryRole.capabilities` declaration |
| `specs/001-project-platform/data-model.md` | `(issuer, subject)`, admin bindings |
| `deploy/platform/identity/keycloak-realm-config.yaml` | Example client id / audience (`project-pn`) |

---

## 9. Agent completion criteria

Mark project auth integration **done** only when all are true:

- [ ] Protected handlers verify Keycloak access JWT (sig, `iss`, `aud`, `exp`, alg, `sub`) via JWKS with rotation support  
- [ ] Platform identity is `(issuer, subject)`; email is not the primary key  
- [ ] Ordinary capabilities are explicit; undeclared ops denied  
- [ ] Admin only via stored `(issuer, subject)` bindings for this project  
- [ ] Tests cover accept + reject matrix in §3 and authorization matrix in §4  
- [ ] No project-owned browser OIDC/Google login path that bypasses the gateway
