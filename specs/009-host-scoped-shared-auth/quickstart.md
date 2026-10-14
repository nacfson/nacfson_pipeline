# Quickstart & Validation Guide: Backend-First Headless Login Logic & Shared Project Authentication

**Feature**: `009-host-scoped-shared-auth`
**Date**: 2026-10-14

## Prerequisites

- Go `1.22+`
- Python `3.11+`
- `kustomize` & `kubeconform` (used by `./scripts/render-validate.sh`)

---

## 1. Run Gateway & Shared Verification Module Unit/Contract Tests

Verify all 4 layers of the cookie-free BFF flow (`Frontend ➔ Project Backend ➔ Internal Gateway/Keycloak ➔ Project Backend ➔ Frontend`), direct Google redirect (`kc_idp_hint=google`), stateless encrypted PKCE `state`, one-time ticket/pickup exchange, canonical issuer validation, and transparent token refresh:

```bash
cd gateway
go test -v ./...
go vet ./...
```

**Expected Outcome**:
- `TestHeadlessGoogleRedirect`: Confirms `/oauth/start` redirects directly to Keycloak with `kc_idp_hint=google` and encrypted PKCE `state` (requiring zero `OIDC_AUTH_STATE` cookies).
- `TestCallbackHandoffAndZeroCookies`: Confirms `/oauth/callback` sets zero `PLATFORM_SESSION` cookies and redirects to the Project Backend (`/api/auth/callback?ticket=...`).
- `TestReloadedCallbackGracefulRestart`: Confirms reloading a consumed `/oauth/callback` URL with a valid project return URL redirects cleanly to restart sign-in rather than returning a raw `400 missing or expired auth state` error.
- `TestSharedVerificationModuleEndToEnd`: Confirms `Frontend ➔ Project Backend (/api/auth/callback & POST /api/auth/session) ➔ Internal Gateway (/auth/exchange & /auth) ➔ Project Backend ➔ Frontend` succeeds with `Authorization: Bearer <session_handle>`, refreshes expired short-lived tokens transparently, rejects cross-project audiences, strips spoofed headers, and revokes sessions on `POST /api/auth/logout`.

---

## 2. Run Platform Preflight & Manifest Validation Gates

Verify that all Kubernetes manifests, project boundaries (`signIn: shared`), network policies (egress to `gateway:8080`), and ingress routes pass `platform-preflight` for both `local-k3s` and `vps-k3s`:

```bash
python3 -m unittest discover tests/preflight
./scripts/render-validate.sh local-k3s
./scripts/render-validate.sh vps-k3s
```

**Expected Outcome**:
- All preflight unit tests pass (`OK`).
- Both `local-k3s` and `vps-k3s` Kustomize overlays render cleanly, pass `kubeconform`, and pass `scripts/platform-preflight.py`.
