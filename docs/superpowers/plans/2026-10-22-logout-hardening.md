# Logout Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `POST /auth/logout` fail closed when Keycloak revocation fails (issue #57), redirect only to allowlisted hosts (issue #58), and drop the unconditional `localhost` CSRF exemption.

**Architecture:** `HandleLogout` in `gateway/internal/auth/logout.go` gains three behaviours: it returns the revoke error instead of dropping it, it passes `redirect_uri` through the existing `returnHostAllowed`, and `ValidateCSRF` honours the localhost exemption only when an explicit config flag is on. The revoker becomes a `keycloak.SessionClient` authenticating as `session-revocation-client`, wired in `main.go`. The realm import grants that service account `manage-users`.

**Tech Stack:** Go 1.22 module `nacfson_pipeline/gateway` (std `net/http`, `httptest`), Keycloak 24.0.5 realm import (`deploy/platform/identity/keycloak-realm-config.yaml`), Python `unittest` preflight tests (needs `pip install pyyaml`).

**Spec:** [`contracts/CONTRACT_AUTH_DOOR.md`](../../../contracts/CONTRACT_AUTH_DOOR.md) §3.4 (live contract), [`specs/001-project-platform/contracts/session-revocation.md`](../../../specs/001-project-platform/contracts/session-revocation.md), [`docs/issues/FIX_GithubIssue57.md`](../../issues/FIX_GithubIssue57.md), [`docs/issues/FIX_GithubIssue58.md`](../../issues/FIX_GithubIssue58.md).

## Global Constraints

- All commits, pushes and PRs go through the `gh-mcp` tools `git_commit`, `git_push`, `pr_create`. Never run `git commit`, `git push` or `gh` in the shell. Work on a feature branch, never `main`.
- The Keycloak Admin delete authenticates as client id `session-revocation-client` with `SESSION_REVOCATION_CLIENT_SECRET`. It never uses `gateway-client`, client-provisioning credentials or the master admin credential.
- `200` with `status: revoked`, or `302`, only after the delete succeeds. A failed or unavailable delete returns `502` or `503` with an error body.
- `redirect_uri` is honoured only when it is `https`, has no userinfo, and its host is in `PROJECT_RETURN_HOSTS` (`Config.AllowedReturnHosts`). Anything else is ignored and the `200` JSON confirmation is returned.
- A signed-out POST stays `401`. `GET /auth/logout` stays `405`.
- The Add-tab and other `frontend` code are out of scope. This plan touches only `nacfson_pipeline`.
- Run Go tests from `gateway/` with `go test ./...`.

## Review Focus

Most likely first. Each line is pinned by a test in the task named in brackets.

- Empty `SessionID` in a valid handle: nothing can be deleted, so it must not report `revoked` (expect `400 invalid_session`). [Task 3]
- Revoker not configured (secret missing): expect `503`, never a fake success. [Task 2, Task 3]
- Look-alike redirect hosts: `https://pn.example.com.evil.test/`, `https://pn.example.com@evil.test/`, `//evil.test`, `javascript:alert(1)`, `http://pn.example.com/`. None may appear in `Location`. [Task 4]
- Look-alike Origin hosts: `https://evilexample.com`, `https://example.com.evil.test`, `Origin: null`. None may pass the CSRF check. [Task 5]
- Failed revoke must leave the session usable: the next `GET /auth` with the same handle is still `200`, and the cookie is not cleared. [Task 3]

## Open decisions (confirm before Task 1)

1. **Cookie on failed revoke.** The contract says "MAY still be cleared"; `platform-architecture.html` says cleared "only as a success path". This plan picks **do not clear on failure**, so the user can retry. Task 1 changes the contract word to MUST NOT.
2. **"Production" for the CSRF exemption.** The gateway has no environment setting. This plan adds `Config.AllowLocalOrigins`, set only by env `LOGOUT_ALLOW_LOCAL_ORIGINS=true`, default off. No deploy manifest sets it, so production never exempts localhost.
3. **Scope.** "Entire issues" is read as #57, #58 and the CSRF hole. Other open issues (#63, #65, #69) are not touched.

---

## File Structure

| File | Responsibility |
| :--- | :--- |
| `gateway/internal/auth/logout.go` | Modify. Fail-closed revoke, redirect allowlist, CSRF flag check. |
| `gateway/internal/auth/handler.go` | Modify. `NewHandler` no longer defaults the revoker to the gateway client. |
| `gateway/internal/keycloak/session.go` | Modify. Add `SessionRevocationClientID`; remove `Client.RevokeSession`. |
| `gateway/internal/config/config.go` | Modify. Remove the secret fallback; add `AllowLocalOrigins`. |
| `gateway/cmd/gateway/main.go` | Modify. Wire `SessionClient` as the revoker. |
| `gateway/tests/session_client_test.go` | Create. Token request identity. |
| `gateway/tests/config_test.go` | Create. Config loading rules. |
| `gateway/tests/logout_test.go` | Modify. Failure, redirect and CSRF tests; update shared setup. |
| `gateway/tests/revocation_contract_test.go` | Modify. Add `AllowedReturnHosts` to its config. |
| `deploy/platform/identity/keycloak-realm-config.yaml` | Modify. Service-account user with `manage-users`. |
| `tests/preflight/test_keycloak_jit_provisioning.py` | Modify. Assert that role grant. |
| Contract and doc files | Modify in Task 1. |

---

### Task 1: Branch, clean the tree, reconcile the docs

**Files:**
- Modify: `contracts/CONTRACT_AUTH_DOOR.md:44-46`, `specs/001-project-platform/contracts/session-revocation.md:25-28`, `docs/platform-architecture.html:1581-1583`, `docs/issues/FIX_GithubIssue57.md`, `docs/issues/FIX_GithubIssue58.md`, `docs/issues/index.md`

**Interfaces:**
- Consumes: the uncommitted contract edits already in the working tree.
- Produces: contract wording that later tasks' tests assert: cookie not cleared on failure; `LOGOUT_ALLOW_LOCAL_ORIGINS` name; `502` for a failed delete, `503` for an unavailable revoker.

- [ ] **Step 1: Create the branch and unstage unrelated files**

Run: `cd ~/Projects/nacfson_pipeline && git switch -c fix/logout-hardening && git restore --staged kubectl docs/superpowers/specs/2026-10-22-log-viewer-design.md`
Expected: `git status --short` shows `kubectl` and the log-viewer spec as untracked, not staged. Never stage them in this plan.

- [ ] **Step 2: Reconcile the contract wording**

In `CONTRACT_AUTH_DOOR.md` §3.4: change "The platform cookie MAY still be cleared" to "The platform cookie MUST NOT be cleared"; state `502` for a failed delete and `503` when the revocation client is unavailable; replace the CSRF sentence's "in production" with "The `localhost` and `127.0.0.1` exemption applies only when `LOGOUT_ALLOW_LOCAL_ORIGINS=true`, which defaults to off and is never set in cluster manifests". Make the same two changes in `session-revocation.md` preconditions 2 and 3 and in the `platform-architecture.html` logout `desc` and `code` strings (`502 or 503`, cookie kept on failure).

- [ ] **Step 3: Update the issue notes**

`FIX_GithubIssue58.md`: replace "Live contract: not written" with a link to `CONTRACT_AUTH_DOOR.md` §3.4. `FIX_GithubIssue57.md` and `index.md`: update the 58 row's "Live contract" cell to `CONTRACT_AUTH_DOOR.md` §3.4. Leave State as OPEN.

- [ ] **Step 4: Verify**

Run: `python3 scripts/check-arch-doc-sync.py --worktree`
Expected: exit 0. Then `grep -n "MAY still be cleared" contracts specs docs -r` returns nothing.

- [ ] **Step 5: Commit** with `git_commit`, message `docs(auth): pin logout failure, redirect and CSRF rules`, listing only the six files above.

---

### Task 2: Revoker authenticates as `session-revocation-client`

**Files:**
- Modify: `gateway/internal/keycloak/session.go`, `gateway/internal/config/config.go:82`, `gateway/internal/auth/handler.go:68-77`, `gateway/cmd/gateway/main.go:33`
- Create: `gateway/tests/session_client_test.go`, `gateway/tests/config_test.go`

**Interfaces:**
- Produces: `const SessionRevocationClientID = "session-revocation-client"` in package `keycloak`; `NewSessionClient(adminURL, clientID, clientSecret string) *SessionClient` (already exists, unchanged); `auth.NewHandler(cfg, validator, client)` now leaves `Revoker` nil; `Config.SessionRevocationSecret` is `""` when neither `SESSION_REVOCATION_CLIENT_SECRET_FILE` nor `SESSION_REVOCATION_CLIENT_SECRET` is set.

- [ ] **Step 1: Write the failing tests**

`TestSessionClientUsesRevocationIdentity` in `session_client_test.go`: start an `httptest.Server` that records the token-endpoint form and answers `{"access_token":"t"}`, then `204` on `DELETE /admin/realms/platform/sessions/sess-1`. Call `keycloak.NewSessionClient(srv.URL, keycloak.SessionRevocationClientID, "rev-secret").RevokeSession("sess-1")`. Assert no error, form `client_id == "session-revocation-client"`, `client_secret == "rev-secret"`, and the DELETE carried `Authorization: Bearer t`.
`TestSessionClientFailsWhenDeleteRefused`: DELETE answers `403`; assert `RevokeSession` returns a non-nil error.
`TestConfigDoesNotFallBackToGatewaySecret` in `config_test.go`: with `t.Setenv("GATEWAY_CLIENT_SECRET", "gw-secret")` and the two revocation variables unset, `config.LoadFromEnv()` returns `SessionRevocationSecret == ""`.

- [ ] **Step 2: Run to verify failure**

Run: `cd gateway && go test ./tests -run 'SessionClient|ConfigDoesNotFall' -v`
Expected: build FAIL (`SessionRevocationClientID` undefined), then the config test FAIL with `"gw-secret"`.

- [ ] **Step 3: Implement**

Add the constant in `session.go` and delete `func (c *Client) RevokeSession`. In `config.go` pass `""` as the default of the `sessionRevocationSecret` `readSecretFileOrEnv` call. In `NewHandler` remove the `revoker = client` default. In `main.go`, after `NewHandler`, call `handler.SetRevoker(keycloak.NewSessionClient(cfg.KeycloakAdminURL, keycloak.SessionRevocationClientID, cfg.SessionRevocationSecret))` only when `cfg.SessionRevocationSecret != ""`; otherwise `log.Println` a warning that logout will return 503. Remove the `else if h.Client != nil` branch in `logout.go` (Task 3 rewrites that block, so just keep the build green).

- [ ] **Step 4: Run to verify pass**

Run: `cd gateway && go build ./... && go test ./tests -run 'SessionClient|ConfigDoesNotFall' -v`
Expected: PASS, build exit 0.

- [ ] **Step 5: Grant the role in the realm import and test it**

Add to `keycloak-realm-config.yaml` a top-level `users` entry with `username: service-account-session-revocation-client`, `enabled: true`, `serviceAccountClientId: session-revocation-client`, `clientRoles: {"realm-management": ["manage-users"]}`. In `test_keycloak_jit_provisioning.py` add `test_session_revocation_service_account_can_delete_sessions` asserting that user exists with exactly that role. Run `pip install pyyaml` then `python3 -m unittest tests.preflight.test_keycloak_jit_provisioning -v`. Expected: PASS. Note: not verified against a live Keycloak 24.0.5 import; record this in the PR body.

- [ ] **Step 6: Commit** with `git_commit`, message `fix(gateway): revoke sessions as session-revocation-client`.

---

### Task 3: Logout fails closed (issue #57)

**Files:**
- Modify: `gateway/internal/auth/logout.go` (the block from `// 3. Synchronous Backend Keycloak Session Invalidation` to the success response), `gateway/tests/logout_test.go`

**Interfaces:**
- Consumes: `Handler.Revoker keycloak.SessionTerminator`, nil when unconfigured (Task 2).
- Produces: `HandleLogout` responses: `503 {"error":"revocation_unavailable"}` when `Revoker == nil`; `502 {"error":"revocation_failed"}` when `RevokeSession` errors; `400 {"error":"invalid_session"}` when `SessionID == ""`. In all three the cookie is not cleared and no `Location` is set.

- [ ] **Step 1: Write the failing tests** in `logout_test.go`, reusing `setupLogoutHandler`, `mockSessionTerminator` and `encodeTestCookie`

- `TestLogoutFailedRevokeReturns502`: `terminator.err = errors.New("kc down")`, valid cookie and `Origin: https://pn.example.com`. Assert code `502`, body `error == "revocation_failed"`, body does not contain `revoked`, no `Set-Cookie` header clearing `PLATFORM_SESSION`, no `Location`.
- `TestLogoutWithoutRevokerReturns503`: `handler.SetRevoker(nil)`; assert `503`, `error == "revocation_unavailable"`.
- `TestLogoutEmptySessionIDRejected`: cookie with `SessionID: ""`; assert `400`, `error == "invalid_session"`, zero revoke calls.
- In `revocation_contract_test.go` add `TestRevocationFailureKeepsSessionActive`: a `statefulSessionRegistry` variant whose `RevokeSession` returns an error without deleting. After a failed logout, `HandleForwardAuth` with the same cookie still returns `200`.

- [ ] **Step 2: Run to verify failure**

Run: `cd gateway && go test ./tests -run 'FailedRevoke|WithoutRevoker|EmptySessionID|FailureKeepsSession' -v`
Expected: all four FAIL (current code returns 200 and clears the cookie).

- [ ] **Step 3: Implement in `HandleLogout`**

Before any cookie clearing: reject empty `SessionID` (400), nil `Revoker` (503), revoke error (502), each via a small helper `writeLogoutError(w http.ResponseWriter, status int, code, message string)` that writes JSON like the existing `invalid_session` responses. Only after a nil error from `RevokeSession` call `h.clearSessionCookie(w)` and continue to the redirect/JSON step. Remove the `_ = usedBearer` line and the unused variable if nothing else reads it.

- [ ] **Step 4: Run the full gateway suite**

Run: `cd gateway && go test ./...`
Expected: PASS. If an existing test relied on an empty `SessionID` succeeding, fix that test's fixture, not the handler.

- [ ] **Step 5: Commit** with `git_commit`, message `fix(gateway): logout returns 502/503 when revocation fails (#57)`.

---

### Task 4: Redirect allowlist (issue #58)

**Files:**
- Modify: `gateway/internal/auth/logout.go` (redirect block), `gateway/tests/logout_test.go` (`setupLogoutHandler`, `TestLogoutSuccessWithValidOriginAndRedirect`), `gateway/tests/revocation_contract_test.go:41-47`

**Interfaces:**
- Consumes: `(*Handler).returnHostAllowed(targetURL string) bool` in `handler.go`, unchanged.
- Produces: `HandleLogout` returns `302` only when `returnHostAllowed(redirect_uri)` is true; otherwise `200` with `{"status":"revoked",...}` and no `Location`.

- [ ] **Step 1: Give the shared fixtures an allowlist**

Add `AllowedReturnHosts: []string{"pn.example.com"}` to the config in `setupLogoutHandler` and in `TestRevocationContract`/`TestRevocationContractCSRFRejection`. Run `go test ./tests` — still PASS.

- [ ] **Step 2: Write the failing test**

`TestLogoutIgnoresDisallowedRedirect` in `logout_test.go`: table over `redirect_uri` values `//evil.test/x`, `javascript:alert(1)`, `http://pn.example.com/x`, `https://evil.test/`, `https://pn.example.com@evil.test/`, `https://pn.example.com.evil.test/`. For each, send a valid signed-in POST (Origin `https://pn.example.com`) as a form field, and again as a query parameter. Assert code `200`, empty `Location`, body `status == "revoked"`, one revoke call.
`TestLogoutSignedOutRedirectStays401`: no cookie, `redirect_uri=https://evil.test/`; assert `401`, empty `Location`.
Keep `TestLogoutSuccessWithValidOriginAndRedirect` as the allowlisted `302` case.

- [ ] **Step 3: Run to verify failure**

Run: `cd gateway && go test ./tests -run 'IgnoresDisallowedRedirect|SignedOutRedirect' -v`
Expected: `IgnoresDisallowedRedirect` FAIL (gets 302); `SignedOutRedirect` already passes.

- [ ] **Step 4: Implement**

In the redirect block, change the condition to `redirectURI != "" && h.returnHostAllowed(redirectURI)`. Otherwise fall through to the JSON confirmation.

- [ ] **Step 5: Run to verify pass**

Run: `cd gateway && go test ./...`
Expected: PASS.

- [ ] **Step 6: Commit** with `git_commit`, message `fix(gateway): only redirect logout to allowlisted https hosts (#58)`.

---

### Task 5: Localhost CSRF exemption off by default

**Files:**
- Modify: `gateway/internal/config/config.go`, `gateway/internal/auth/logout.go:49`, `gateway/tests/logout_test.go`, `gateway/tests/config_test.go`

**Interfaces:**
- Produces: `Config.AllowLocalOrigins bool`, true only when env `LOGOUT_ALLOW_LOCAL_ORIGINS == "true"`. `ValidateCSRF` accepts `localhost` / `127.0.0.1` Origin or Referer only when it is true.

- [ ] **Step 1: Write the failing tests**

`TestLogoutRejectsLocalhostOriginByDefault` in `logout_test.go`: valid cookie, `Origin: http://localhost:3000` and again `http://127.0.0.1`; assert `403 invalid_csrf_token`, zero revoke calls.
`TestLogoutAcceptsLocalhostWhenFlagSet`: same request with `cfg.AllowLocalOrigins = true`; assert `200`.
`TestLogoutRejectsLookalikeOrigins`: `Origin` values `https://evilexample.com`, `https://example.com.evil.test`, `null`; assert `403`.
`TestAllowLocalOriginsDefaultsOff` in `config_test.go`: unset env gives `false`; `LOGOUT_ALLOW_LOCAL_ORIGINS=true` gives `true`; `=1` gives `false`.

- [ ] **Step 2: Run to verify failure**

Run: `cd gateway && go test ./tests -run 'LocalhostOrigin|LookalikeOrigins|AllowLocalOrigins' -v`
Expected: build FAIL (`AllowLocalOrigins` undefined).

- [ ] **Step 3: Implement**

Add the field and its env parse (exact string match `"true"`). In `ValidateCSRF` guard the `hostname == "localhost" || hostname == "127.0.0.1"` clause with `h.Config.AllowLocalOrigins`.

- [ ] **Step 4: Run to verify pass**

Run: `cd gateway && go test ./...`
Expected: PASS.

- [ ] **Step 5: Commit** with `git_commit`, message `fix(gateway): localhost CSRF origin exemption is opt-in`.

---

### Task 6: Final verification and PR

**Files:** none new.

- [ ] **Step 1: Full verification**

Run, from the repo root: `cd gateway && go vet ./... && go test ./... && cd .. && python3 scripts/check-arch-doc-sync.py --worktree && python3 -m unittest discover tests/preflight`
Expected: all exit 0. If a preflight test fails for a reason other than missing `yaml`, stop and report it.

- [ ] **Step 2: Check the diff is only the intended files**

Run: `git status --short && git diff main --stat`
Expected: no `kubectl`, no `docs/superpowers/specs/2026-10-22-log-viewer-design.md`.

- [ ] **Step 3: Update the issue notes' State line** in `FIX_GithubIssue57.md` and `FIX_GithubIssue58.md` to say the fix is in the PR and was not verified against a live Keycloak, then `git_commit` as `docs(issues): note logout fixes for #57 and #58`.

- [ ] **Step 4: Push and open the PR** with `git_push` then `pr_create`. Title `fix(gateway): harden POST /auth/logout (#57, #58)`. Body: link both issues with `Closes #57` and `Closes #58`; list the three open decisions from this plan; state that the realm-import role grant is untested against a live Keycloak 24.0.5 and that the 403 in #57 was inferred, not observed.
