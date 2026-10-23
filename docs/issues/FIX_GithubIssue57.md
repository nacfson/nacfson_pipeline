# FIX_GithubIssue57

- Issue: https://github.com/nacfson/nacfson_pipeline/issues/57
- State: OPEN. Handler, revoker wiring, and realm-import role grant are in this change. Not verified against a live Keycloak 24.0.5. The production 403 was inferred from the import, not observed.
- Live contract: [`contracts/CONTRACT_AUTH_DOOR.md`](../../contracts/CONTRACT_AUTH_DOOR.md) §3.4
- Older contract: [`specs/001-project-platform/contracts/session-revocation.md`](../../specs/001-project-platform/contracts/session-revocation.md) Case C

## What is wrong

`POST /auth/logout` reports success even when the Keycloak session delete fails. `HandleLogout` drops the error (`_ = h.Revoker.RevokeSession(...)` / `_ = h.Client.RevokeSession(...)` in `gateway/internal/auth/logout.go`). The next `GET /auth` with the same bearer handle can still return 200.

The revocation secret is loaded and never used. `cmd/gateway/main.go` builds one Keycloak client from `gateway-client` and passes that client as the revoker. `Client.RevokeSession` asks for an admin token as `gateway-client`. The realm import does not grant `session-revocation-client` or `gateway-client` the role needed to delete sessions. A production 403 was inferred from that import. It was not observed on a live cluster.

## Contract

The live rule, added with this note:

- The Admin delete authenticates as `session-revocation-client` with `SESSION_REVOCATION_CLIENT_SECRET`. Not `gateway-client`.
- `200` / `302` success is allowed only after that delete succeeds.
- If the delete fails, return `502` or `503`. Do not claim `revoked`. The platform cookie MUST NOT be cleared on failure. The same `session_handle` is not revoked.

`specs/009-host-scoped-shared-auth/contracts/gateway-internal-auth-api.md` §5 still says logout always returns `200`. That note does not override the live contract.

## How to check

No cluster is required for the handler bug. Point a failing revoker at `HandleLogout` and then call `HandleForwardAuth` with the same handle. Logout must not be `200` with `status: revoked`, and the next `/auth` must not be treated as a completed revocation.

For the client bug, a token request made by `RevokeSession` must send `client_id=session-revocation-client` and `SESSION_REVOCATION_CLIENT_SECRET`, not `gateway-client`.

## Fixed when

Both checks pass, and the realm import grants that service account the session-deletion role. Existing logout tests that only use a revoker which always succeeds do not cover this.

## Not checked

Live Keycloak was not called. The VPS API tunnel was down. Anonymous `GET /auth/logout` on `https://auth.nacfson.cloud` returns `405`, which only shows the route exists.
