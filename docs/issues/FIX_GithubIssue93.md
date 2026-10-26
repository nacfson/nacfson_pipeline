# FIX_GithubIssue93

- Issue: https://github.com/nacfson/nacfson_pipeline/issues/93
- State: OPEN.
- Live contract: [`contracts/CONTRACT_AUTH_DOOR.md`](../../contracts/CONTRACT_AUTH_DOOR.md) §5 Session Lifetimes.

## What is wrong

A signed-in visitor is signed out after what feels like about 5 minutes, and signing in again sends them back to Google.

## Cause

The refresh token is already expired when the gateway first uses it.

1. The realm import set no lifespans, so the live realm ran on Keycloak 24 defaults: access token 300 s, SSO idle 1800 s, SSO max 36000 s, rotation off.
2. The gateway refreshes only when a protected project API call reaches `GET /auth` after the 5-minute access token has expired (`gateway/internal/keycloak/oidc.go` `ValidateAndRefresh`). A check while the token is still valid does not renew the Keycloak session. Frontend-only activity sends no request at all.
3. A visitor who signs in, uses the app for a few minutes, and makes no protected API call for 30 minutes loses the session. The next call refreshes with an expired token, Keycloak logs `REFRESH_TOKEN_ERROR` `invalid_token` `Token is not active`, the gateway returns `401`, and the Keycloak session is gone too, so sign-in goes through Google again.

Closed issue #4 asked for longer lifetimes, but those numbers never reached the realm or a contract ([`FIX_GithubIssue4.md`](FIX_GithubIssue4.md)).

## Evidence (2026-10-26, live vps-k3s)

| Check | Result |
| :--- | :--- |
| Keycloak log, 10 h | Four sign-outs (02:41, 03:28, 04:07, 09:51 UTC). Each one: two `INTROSPECT_TOKEN_ERROR` (`JWT check failed`), then `REFRESH_TOKEN_ERROR` `invalid_token` `Token is not active`, `client_auth_method="client-secret"`. |
| Live `realm` row | `access_token_lifespan=300`, `sso_idle_timeout=1800`, `sso_max_lifespan=36000`, remember-me `0/0`, `revoke_refresh_token=f`. |
| Keycloak pod | Running. No restart linked to the sign-outs. |
| Sign-in after an automatic sign-out | Google asks again, so the Keycloak session had expired, not only the gateway check. |

## Ruled out

| Candidate | Why not |
| :--- | :--- |
| Refresh-token rotation | Would log `Stale token`. Live `revoke_refresh_token=f`. |
| Wrong client secret after the OpenBao reshuffle | Would log `invalid_client_credentials` and break sign-in itself. Sign-in works. |
| Gateway HMAC key rotation | Would sign everyone out at once with `No confirmed session` and no Keycloak event. |
| Keycloak restart | Would log `Session not active`. Keycloak was up. |
| `audience` parameter on refresh | Keycloak accepted the request and rejected only the token's expiry. |

## Fix in this change

- Realm import declares `accessTokenLifespan=300`, `ssoSessionIdleTimeout=3600` (1 hour), `ssoSessionMaxLifespan=36000`, `revokeRefreshToken=false`.
- [`CONTRACT_AUTH_DOOR.md`](../../contracts/CONTRACT_AUTH_DOOR.md) §5 names every clock, what renews a session, and what does not.
- [`tests/preflight/test_keycloak_session_lifetimes.py`](../../tests/preflight/test_keycloak_session_lifetimes.py) locks the realm JSON to the contract and forbids client-level overrides.
- [`docs/operations-runbook.md`](../operations-runbook.md) §7 has the procedure to apply the values to the live realm, because startup import skips an existing realm. The docs no longer claim `OVERWRITE_EXISTING` re-imports it.

## Not fixed here

- The live realm still has `sso_idle_timeout=1800` until an operator runs the runbook procedure. That restart signs every user out once.
- Client-level overrides on the live realm were not read.
- The gateway still treats any refresh error (timeout, 5xx) as revoked and does not log why (`oidc.go` `ValidateAndRefresh`, `ValidateSession` non-200). The backend middleware replaces every gateway rejection with `session rejected by gateway`.
- `/auth/exchange` still reports `expires_in` as the 8-hour gateway timeout.
- Whether each frontend stores `X-Refreshed-Session-Handle` was not checked. Those frontends live in other repos.
- Logout followed by sign-in did not ask Google. Whether the Keycloak session delete succeeded was not checked.

## Fixed when

The live realm reads `300 | 3600 | 36000 | f` with no client-level rows, and a visitor who makes no protected API call for 40 minutes is still signed in on the next call.
