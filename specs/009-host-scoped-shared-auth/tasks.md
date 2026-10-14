# Tasks: Backend-First Headless Login Logic & Shared Project Authentication

**Input**: Design documents from `/specs/009-host-scoped-shared-auth/`
**Prerequisites**: [plan.md](plan.md) (required), [spec.md](spec.md) (required for user stories), [research.md](research.md), [data-model.md](data-model.md), [contracts/](contracts/), [quickstart.md](quickstart.md)

**Tests**: Automated Go unit and contract tests are included for each user story to verify `SC-001` through `SC-006`.

**Organization**: Tasks are grouped by user story (`US1`, `US2`, `US3`) to enable independent implementation and testing of each story.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies)
- **[Story]**: Which user story this task belongs to (e.g., `US1`, `US2`, `US3`)
- Includes exact file paths and verbatim validation constraints from `data-model.md`

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Initialize package structure for the internal authentication gateway crypto/ticket primitives and the exported shared verification package.

- [X] T001 Create shared project authentication package directory structure in `gateway/pkg/projectauth/`
- [X] T002 [P] Verify Go 1.22 standard-library module boundaries (zero external dependencies) in `gateway/go.mod`

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Core cryptographic sealing/unsealing, canonical issuer resolution, and single-use ticket primitives required across all user stories.

**⚠️ CRITICAL**: No user story work can begin until this phase is complete.

- [X] T003 [P] Add `CanonicalIssuer()` and `IsAllowedIssuer(issuer string)` helper methods on `Config` ("Must match CanonicalIssuer() (KEYCLOAK_PUBLIC_URL fallback KEYCLOAK_ISSUER_URL)") in `gateway/internal/config/config.go`
- [X] T004 Implement AES-256-GCM + HMAC seal/unseal helpers (supporting `HMACSecret` and `HMACSecretPrevious` fallback) and thread-safe single-use `HandoffTicket` store (`ticket_id`: "128-bit random single-use ID", `host`: "Must match X-Forwarded-Host / host of calling Project Backend", `session_handle`: "Valid sealed SessionHandleData", `exp`: "60-second TTL") in `gateway/internal/auth/crypto.go`

**Checkpoint**: Foundation ready — user story implementation can now begin.

---

## Phase 3: User Story 1 - Pipeline Provides Headless Login Logic Only; Login Page Delegated to Each Project (Priority: P1) 🎯 MVP

**Goal**: Delegate 100% of the login page UI to each project while the pipeline executes only headless login logic: `/oauth/start` redirects directly to Google (`kc_idp_hint=google`) with a stateless AES-256-GCM encrypted PKCE `state` parameter (10-minute window, no `OIDC_AUTH_STATE` cookie failure), and `/oauth/callback` gracefully restarts sign-in on reloaded/consumed callbacks instead of showing raw `missing or expired auth state` errors.

**Independent Test**: Run `go test -v ./tests -run "TestDirectGoogleSignIn|TestStatelessEncryptedState|TestCallbackReloadGracefulRestart"` in `gateway/` to verify `/oauth/start` includes `kc_idp_hint=google`, `/oauth/callback` succeeds without any `OIDC_AUTH_STATE` cookie, and reloading a consumed callback redirects cleanly back to restart sign-in.

### Tests for User Story 1

- [X] T005 [P] [US1] Add unit tests for direct Google redirect (`kc_idp_hint=google`), cookie-free encrypted PKCE `state` round-trip, and graceful callback reload recovery in `gateway/tests/handler_test.go`

### Implementation for User Story 1

- [X] T006 [US1] Update `StatePayload` (`target_url`: "Must match AllowedReturnHosts (https://<project-host>/...)", `code_verifier`: "43-char base64url PKCE verifier (256-bit entropy)", `nonce`: "128-bit random hex/base64url", `ts`: "now - ts <= 600 (10-minute window)") and `initiateLogin` to seal `StatePayload` with AES-256-GCM and append `kc_idp_hint=google` on `/oauth/start` in `gateway/internal/auth/handler.go`
- [X] T007 [US1] Update `HandleCallback` to decrypt the PKCE `code_verifier` directly from `state` (with legacy `OIDC_AUTH_STATE` cookie fallback (`Max-Age: 600`) only when unencrypted state is used in legacy tests) and redirect to `/oauth/start?return=...` when a valid signed return URL is reloaded or expired in `gateway/internal/auth/handler.go`

**Checkpoint**: User Story 1 is independently functional and testable.

---

## Phase 4: User Story 2 - 4-Layer Cookie-Free Identity Flow (`Frontend ➔ Backend ➔ Gateway/Keycloak ➔ Backend ➔ Frontend`) (Priority: P1)

**Goal**: Implement the 4-layer cookie-free BFF session flow where `Gateway` seals the Keycloak session and OAuth/refresh tokens into a Vault-encrypted opaque `session_handle`, hands a one-time `ticket` to `/api/auth/callback` on the Project Backend (with zero `PLATFORM_SESSION` cross-subdomain cookies), exchanges the ticket over internal HTTP (`POST /auth/exchange`), validates canonical issuers, and transparently refreshes expired short-lived access tokens during `GET /auth`.

**Independent Test**: Run `go test -v ./tests -run "TestTicketExchange|TestBearerSessionHandle|TestCanonicalIssuer|TestTransparentTokenRefresh"` in `gateway/` to verify zero `Domain=pn.*` cookies are emitted, `POST /auth/exchange` returns the sealed `session_handle`, and `GET /auth` accepts `Authorization: Bearer <session_handle>` while refreshing expired access tokens with Keycloak.

### Tests for User Story 2

- [X] T008 [P] [US2] Add contract and unit tests for `POST /auth/exchange`, `Authorization: Bearer <session_handle>` verification on `GET /auth`, canonical issuer matching, and transparent OAuth token refresh in `gateway/tests/contract_test.go`
- [X] T009 [P] [US2] Add unit tests for `POST /auth/logout` using `Authorization: Bearer <session_handle>` in `gateway/tests/logout_test.go`

### Implementation for User Story 2

- [X] T010 [P] [US2] Update `ValidateSession` in `gateway/internal/keycloak/oidc.go` to introspect the actual OAuth access token (rather than passing a session UUID) and support refreshing expired access tokens via `ExchangeTokenForProject`
- [X] T011 [US2] Implement `SessionHandleData` sealing (`sid`: "Non-empty UUID", `sub`: "Non-empty UUID / string", `iss`: "Must match CanonicalIssuer() (KEYCLOAK_PUBLIC_URL fallback KEYCLOAK_ISSUER_URL)", `aud`: "Must match deriveProjectAudience(targetHost) (e.g., project-pn)", `tokens`: "Keyed by aud", `rt`: "Non-empty OAuth refresh token", `iat`: "now - iat < SessionTimeout (8h default)"), callback handoff `ticket` redirect on `/oauth/callback`, `HandleTicketExchange` (`POST /auth/exchange`), and Bearer `session_handle` verification with transparent token refresh in `gateway/internal/auth/handler.go`
- [X] T012 [US2] Update `HandleLogout` in `gateway/internal/auth/logout.go` to accept `Authorization: Bearer <session_handle>` (alongside legacy cookie fallback) and synchronously revoke the underlying Keycloak `SessionID`
- [X] T013 [US2] Mount `/auth/exchange` alongside `/oauth/start`, `/oauth/callback`, `/auth`, and `/auth/logout` in `gateway/cmd/gateway/main.go`

**Checkpoint**: User Stories 1 and 2 are both independently functional and testable.

---

## Phase 5: User Story 3 - Shared, Language-Agnostic Authentication Verification Across Projects (Priority: P2)

**Goal**: Provide a single, reusable Go 1.22 standard-library verification package (`gateway/pkg/projectauth`) implementing the Project Backend BFF endpoints (`GET /api/auth/login`, `GET /api/auth/callback`, `POST /api/auth/session`, `POST /api/auth/logout`) and the `RequireAuth` middleware (`Authorization: Bearer <session_handle>`, spoofed header stripping, and audience isolation) over standard internal HTTP.

**Independent Test**: Run `go test -v ./pkg/projectauth/...` in `gateway/` to verify two simulated projects (`project-pn` and `project-alpha`) execute the complete `Frontend ➔ Backend ➔ Gateway ➔ Backend ➔ Frontend` flow identically and reject cross-project credentials.

### Tests for User Story 3

- [X] T014 [P] [US3] Create end-to-end BFF flow, one-time `PickupCode` redemption, header sanitization, and cross-project audience rejection tests in `gateway/pkg/projectauth/middleware_test.go`

### Implementation for User Story 3

- [X] T015 [US3] Implement the reusable `Verifier` and BFF HTTP handlers (`HandleLogin`, `HandleCallback`, `HandleSession` with `PickupCode` constraints `code`: "128-bit random single-use ID, deleted immediately on first POST /api/auth/session", `session_handle`: "Non-empty opaque handle", `expires_at`: "now + 30s", `HandleLogout`, and `RequireAuth` middleware) in `gateway/pkg/projectauth/middleware.go`

**Checkpoint**: All user stories (`US1`, `US2`, `US3`) are complete and independently verified.

---

## Phase 6: Polish & Cross-Cutting Concerns

**Purpose**: Documentation synchronization and full platform quality gate verification across `local-k3s` and `vps-k3s`.

- [X] T016 [P] Update authentication routing and verification documentation in `docs/architecture-auth-routing.md` and `docs/project-auth-verification.md`
- [X] T017 Run all verification gates from `specs/009-host-scoped-shared-auth/quickstart.md` (`go test`, `go vet`, `tests/preflight`, and `./scripts/render-validate.sh` for `local-k3s` and `vps-k3s`)

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies — can start immediately.
- **Foundational (Phase 2)**: Depends on Setup completion — BLOCKS all user stories.
- **User Story 1 (Phase 3 - P1 MVP)**: Depends on Foundational (Phase 2).
- **User Story 2 (Phase 4 - P1)**: Depends on Foundational (Phase 2) and integrates with `/oauth/callback` in `gateway/internal/auth/handler.go` from User Story 1.
- **User Story 3 (Phase 5 - P2)**: Depends on User Story 2 internal Gateway endpoints (`POST /auth/exchange`, `GET /auth`, `POST /auth/logout`).
- **Polish (Phase 6)**: Depends on Phases 3–5 completion.

### Parallel Opportunities

- `T003` (`gateway/internal/config/config.go`) and `T004` (`gateway/internal/auth/crypto.go`) can run in parallel during Phase 2.
- `T008` (`gateway/tests/contract_test.go`), `T009` (`gateway/tests/logout_test.go`), and `T010` (`gateway/internal/keycloak/oidc.go`) can run in parallel at the start of Phase 4.
- `T014` (`gateway/pkg/projectauth/middleware_test.go`) and `T016` (`docs/`) touch independent files and can be developed in parallel with their respective phases.

---

## Parallel Example: User Story 2

```bash
# Run parallel test & Keycloak client updates across separate files:
Task T008: "Add contract and unit tests for POST /auth/exchange and Bearer session_handle in gateway/tests/contract_test.go"
Task T009: "Add unit tests for POST /auth/logout with Bearer session_handle in gateway/tests/logout_test.go"
Task T010: "Update ValidateSession in gateway/internal/keycloak/oidc.go to introspect OAuth access tokens"
```

---

## Implementation Strategy

### MVP First (User Story 1 + User Story 2 Core Gateway)

1. Complete Phase 1: Setup (`T001`–`T002`)
2. Complete Phase 2: Foundational (`T003`–`T004`)
3. Complete Phase 3: User Story 1 (`T005`–`T007`) — Headless direct Google sign-in + stateless AES-256-GCM PKCE state
4. Complete Phase 4: User Story 2 (`T008`–`T013`) — 4-layer cookie-free `session_handle` + `/auth/exchange` + canonical issuer & active token refresh
5. Complete Phase 5: User Story 3 (`T014`–`T015`) — Shared `gateway/pkg/projectauth` verification package
6. Complete Phase 6: Polish & full cluster preflight validation (`T016`–`T017`)
