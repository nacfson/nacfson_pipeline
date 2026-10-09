# Contract: Traefik ForwardAuth HTTP API

> A project door no longer calls ForwardAuth. Visitor entry is specified in [`specs/006-separate-project-auth/contracts/project-door.md`](../../006-separate-project-auth/contracts/project-door.md). The `/auth` endpoint remains the confirmation call a shared project may make after the visitor has arrived. A missing session does not redirect the visitor before the project answers.

**Feature**: `001-project-platform`  
**Endpoint**: `http://gateway.identity.svc:8080/auth`  
**Method**: `GET`  
**Protocol**: HTTP/1.1  

This contract defines the interface between Traefik's `ForwardAuth` middleware and the custom Go authentication gateway.

---

## 1. Request Interface (Traefik -> Gateway)

Traefik passes incoming client HTTP requests to the gateway `/auth` endpoint with forwarded headers:

### Headers Received
- `X-Forwarded-Method`: Original request HTTP method (e.g. `GET`, `POST`).
- `X-Forwarded-Proto`: Original scheme (`https` or `http`).
- `X-Forwarded-Host`: Original target host (e.g. `project-a.example.com`).
- `X-Forwarded-Uri`: Original path and query string (e.g. `/dashboard/items?page=1`).
- `Cookie`: Browser session cookies containing `PLATFORM_SESSION=<cookie_token>`.
- `X-Forwarded-For`: Client IP address.

### Inbound Header Sanitization
The gateway MUST verify that any client-supplied `X-User-Subject`, `X-User-Issuer`, or forged `Authorization` headers are ignored or stripped so external clients cannot inject spoofed identities.

---

## 2. Response Interface (Gateway -> Traefik)

### Case A: Valid Authenticated Session (HTTP 200 OK)
The user has a valid active Keycloak session. Traefik permits the request and injects the following response headers into the upstream request to the project container:

```http
HTTP/1.1 200 OK
Content-Length: 0
Authorization: Bearer <Keycloak_Project_Scoped_JWT>
X-User-Subject: <Keycloak_User_Subject_UUID>
X-User-Issuer: https://auth.example.com/realms/platform
```

**Token Audience Rule**:
The JWT in `Authorization` MUST have `aud: "project-a"` (matching `X-Forwarded-Host`). It MUST NOT be a generic Google token or ID token.

---

### Case B: Unauthenticated Visitor (HTTP 302 Found)
The visitor lacks a valid session cookie. Traefik halts the upstream request and redirects the browser to Keycloak login:

```http
HTTP/1.1 302 Found
Location: https://auth.example.com/realms/platform/protocol/openid-connect/auth?client_id=project-a&response_type=code&scope=openid&redirect_uri=https://project-a.example.com/callback&state=<state_nonce>
Content-Length: 0
```

---

### Case C: Revoked / Expired Session (HTTP 401 Unauthorized or 302 Found)
The session cookie exists, but Keycloak synchronous validation reports the session is revoked or expired:

```http
HTTP/1.1 401 Unauthorized
Set-Cookie: PLATFORM_SESSION=; Path=/; Expires=Thu, 01 Jan 1970 00:00:00 GMT; HttpOnly; Secure; SameSite=Lax
Content-Type: application/json

{"error": "session_revoked", "message": "Platform session was revoked. Please log in again."}
```

---

### Case D: Dependency Failure / Keycloak Unavailable (HTTP 503 Service Unavailable)
Keycloak or network is unreachable:

```http
HTTP/1.1 503 Service Unavailable
Content-Type: application/json

{"error": "service_unavailable", "message": "Authentication service is temporarily unavailable."}
```
**Invariant**: Confirmation does not succeed while Keycloak is unreachable. This response is not placed on the project door. A project that does not use sign-in keeps answering.

---

## 3. Authentication Lifecycle & Cross-Project SSO Mechanics

### 3.1 Return Destination Tracking
1. A shared project sends the browser to the sign-in start with a `return` address already on that project's door. The gateway encodes that return address into an HMAC-SHA256 signed `state` query parameter. Any other return is rejected. Opening the project host does not require this step.
2. Google redirects to Keycloak broker endpoint (`/realms/platform/broker/google/endpoint`).
3. Keycloak creates the session and redirects to the gateway callback: `https://auth.example.com/oauth/callback?code=...&state=...`.
4. The gateway verifies the state signature, exchanges the code for tokens, sets the `PLATFORM_SESSION` cookie scoped to the project host (`HttpOnly`, `Secure`), and redirects to that same return address.

### 3.2 Login Initiation Verification (PKCE & State Integrity)
1. The gateway generates a cryptographic 32-byte `code_verifier`, computes `code_challenge = BASE64URL(SHA256(code_verifier))`, and generates a unique `nonce`.
2. State payload `{target_url, nonce, timestamp, hash(code_verifier)}` is signed using the gateway's private HMAC secret.
3. `code_verifier` is stored in a temporary 5-minute `HttpOnly` cookie (`OIDC_AUTH_STATE`).
4. At `/oauth/callback`, the gateway asserts HMAC signature validity, enforces timestamp freshness (rejecting requests older than 300 seconds), and passes `code_verifier` to Keycloak to complete token exchange.

### 3.3 Token Expiration & Refresh Flow
1. User SSO Sessions have a typical lifespan of 8–12 hours; project access JWTs are short-lived (5–15 minutes).
2. On every ForwardAuth check, if the project access token is near expiry but the Keycloak session is valid, the gateway uses the session refresh token to synchronously fetch a new project access token from Keycloak over the internal cluster network.
3. If the user session itself is expired, revoked, or signed for another project, confirmation returns HTTP 401 and does not redirect the visitor. If the sign-in service is unreachable, confirmation returns HTTP 503 and does not confirm the user. The project door still answers for parts that do not need a known user.

### 3.4 Cross-Project Token Acquisition (Project A to Project B)
1. Each project has a distinct Keycloak client and audience (`aud: project-a`, `aud: project-b`). Sharing generic tokens across projects is prohibited.
2. When a user with an active session on Project A visits Project B (`https://project-b.example.com`):
   - The gateway recognizes the valid `PLATFORM_SESSION` cookie.
   - If a token for `project-b` is not yet cached in the session, the gateway requests a project-b scoped token from Keycloak using OAuth 2.0 Token Exchange or promptless SSO redirect.
   - The gateway injects `Authorization: Bearer <token_b>` upstream to Project B.
   - Project B independently validates token signatures and confirms `aud == "project-b"`.

