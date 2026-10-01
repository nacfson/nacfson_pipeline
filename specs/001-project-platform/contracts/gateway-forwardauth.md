# Contract: Traefik ForwardAuth HTTP API

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
**Invariant**: Fail-closed strictly enforced. The gateway MUST NEVER allow an unverified request through when Keycloak is down.
