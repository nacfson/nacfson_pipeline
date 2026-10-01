# Contract: Synchronous Session Revocation API

**Feature**: `001-project-platform`  
**Endpoint**: `https://auth.example.com/auth/logout`  
**Method**: `POST`  
**Protocol**: HTTPS  

This contract defines the user-initiated logout endpoint implemented by the Go gateway to revoke an active platform session across all connected projects immediately.

---

## 1. Request Interface (Browser -> Gateway)

```http
POST /auth/logout HTTP/1.1
Host: auth.example.com
Cookie: PLATFORM_SESSION=<session_cookie_token>
Origin: https://project-a.example.com
X-CSRF-Token: <csrf_token>
Content-Type: application/x-www-form-urlencoded

redirect_uri=https://project-a.example.com/goodbye
```

### Security Preconditions
1. **Authenticated Request**: The request MUST present a valid `PLATFORM_SESSION` cookie. An anonymous or unauthenticated request MUST NOT trigger any backend Keycloak call.
2. **CSRF Protection**: The request MUST include a valid CSRF token matching the session or have a validated `Origin`/`Referer` header matching a registered project domain. Cross-site requests without valid protection are rejected immediately (HTTP 403 Forbidden).

---

## 2. Gateway Backend Revocation (Gateway -> Keycloak Admin API)

The gateway issues an authenticated private cluster request to Keycloak's user session termination endpoint:

```http
DELETE /admin/realms/platform/sessions/<sessionId> HTTP/1.1
Host: keycloak.identity.svc:8080
Authorization: Bearer <Platform_Session_Revocation_Token>
```

### Credential Segregation
The gateway uses a dedicated client credential (`session-revocation-client`) scoped strictly to the Keycloak `manage-users` session deletion permission. It MUST NOT use client-provisioning credentials or master admin credentials.

---

## 3. Response Interface (Gateway -> Browser)

### Case A: Successful Revocation (HTTP 302 or 200)

The gateway clears the session cookie and redirects the user:

```http
HTTP/1.1 302 Found
Location: https://project-a.example.com/goodbye
Set-Cookie: PLATFORM_SESSION=; Path=/; Domain=.example.com; Expires=Thu, 01 Jan 1970 00:00:00 GMT; HttpOnly; Secure; SameSite=Lax
Content-Length: 0
```

### Next Request Guarantee
On the very next HTTP request from this browser to ANY project (e.g. `project-b.example.com`), the gateway's `/auth` endpoint detects that the session was revoked and denies access (HTTP 401/302). Independent sessions on other devices remain completely unaffected.

---

### Case B: Invalid CSRF Token (HTTP 403 Forbidden)

```http
HTTP/1.1 403 Forbidden
Content-Type: application/json

{"error": "invalid_csrf_token", "message": "Cross-site request forgery validation failed."}
```
Active sessions remain untouched.
