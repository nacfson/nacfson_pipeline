# Implementation Plan: Backend-First Headless Login Logic & Shared Project Authentication

**Branch**: `009-host-scoped-shared-auth` | **Date**: 2026-10-14 | **Spec**: [spec.md](spec.md)

**Input**: Feature specification from `/specs/009-host-scoped-shared-auth/spec.md`

## Summary

Replace the fragile cross-subdomain cookie authentication flow (`PLATFORM_SESSION` with `Domain=pn.*` and `OIDC_AUTH_STATE` cookies) with a **4-Layer Cookie-Free Backend-For-Frontend (BFF) + Internal Gateway/Keycloak Sealed Session (`session_handle`)** architecture:
1. **Project Frontend owns 100% of the Login UI** and only communicates with its own **Project Backend (`/api/auth/*` and `/api/*`)**.
2. **Pipeline provides headless login logic only**: `/oauth/start` appends `kc_idp_hint=google` (bypassing the Keycloak username/password form) and encrypts the PKCE `code_verifier` directly inside the `state` parameter using AES-256-GCM keyed by the Gateway's OpenBao Vault secret (eliminating `OIDC_AUTH_STATE` cookie drops and `"missing or expired auth state"` errors).
3. **Backend-First Callback & Sealed Session Handle**: When Google sign-in finishes at `auth.<domain>/oauth/callback`, `Gateway` exchanges the code with Keycloak, seals `{ sid, sub, iss, aud, tokens, rt }` into a Vault-encrypted opaque `session_handle`, and redirects to the Project Backend (`/api/auth/callback?ticket=...`) with zero cookies.
4. **Shared Verification Module (`gateway/pkg/projectauth`)**: A zero-dependency Go 1.22 standard-library package (backed by a language-agnostic internal HTTP contract on `gateway.identity.svc:8080`) that exchanges the callback `ticket`, prepares the user's project data, bridges back to the Frontend UI via a 30-second one-time pickup code (`POST /api/auth/session` returning `{ session_handle, user, requirements }` JSON), and verifies `Authorization: Bearer <session_handle>` on all subsequent `/api/*` requests with canonical issuer validation and transparent Keycloak token refresh.

## Technical Context

**Language/Version**: Go `1.22` (standard library only, zero external modules in `go.mod`); Python `3.11` (for `platform-preflight.py` and validation tests)

**Primary Dependencies**: Go standard library (`net/http`, `crypto/aes`, `crypto/cipher`, `crypto/hmac`, `crypto/sha256`, `crypto/rand`, `encoding/json`, `encoding/base64`); Keycloak `26.0.0` (internal OIDC & session store); OpenBao `2.1.0` (Vault secret injection)

**Storage**: Stateless AES-256-GCM + HMAC sealed `session_handle` in Gateway backed by Keycloak's existing PostgreSQL session store; short-lived (30–60s) in-memory single-use handoff tickets and pickup codes with automatic TTL eviction

**Testing**: `go test -v ./... && go vet ./...` in `gateway/`; `python3 -m unittest discover tests/preflight`; `./scripts/render-validate.sh local-k3s` & `./scripts/render-validate.sh vps-k3s`

**Target Platform**: Linux `arm64` (`vps-k3s` Oracle Cloud Always Free) and `amd64` (`local-k3s`), Kubernetes (K3s + Traefik + FluxCD)

**Project Type**: Kubernetes platform authentication gateway (`gateway/`) + reusable project backend verification library (`gateway/pkg/projectauth`)

**Performance Goals**: `<15ms` internal `/auth` and `/auth/exchange` verification latency (excluding upstream Keycloak token refresh); zero additional pod memory overhead (`64Mi` gateway limit preserved)

**Constraints**: Zero third-party Go dependencies; zero browser cookies required; strict `Restricted` Pod Security; Guaranteed QoS (`requests == limits`); zero plaintext secrets in Git

**Scale/Scope**: Multi-tenant personal project platform supporting `proj-pn` and future projects in any language over internal HTTP (`gateway.identity.svc:8080`)

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

| Principle / Gate | Pre-Design Status | Post-Design Status | Verification Notes |
| :--- | :--- | :--- | :--- |
| **I. Declarative Configuration (GitOps)** | PASS | PASS | All gateway/manifest configs remain declarative in `deploy/` and `clusters/`; verified via `render-validate.sh`. |
| **II. Strict Workload Isolation & Restricted PSA** | PASS | PASS | Project backends communicate with `gateway.identity.svc:8080` over existing `signIn: shared` NetworkPolicy egress on port `8080`; no NodePort or privilege changes. |
| **III. Defense-in-Depth Identity & Decoupled Auth** | PASS | PASS | Gateway uses Go 1.22 stdlib only; identity is strictly `(issuer, subject)`; online revocation (`POST /auth/logout`) immediately terminates Keycloak session; fail-closed on Keycloak outage. |
| **IV. Bounded Resource Allocation & 1/n Budgeting** | PASS | PASS | No changes to container CPU/memory requests or limits; passes `platform-preflight.py`. |
| **V. Zero Plaintext Secrets (Cluster Vault)** | PASS | PASS | `session_handle` and PKCE `state` encryption use existing Vault-injected `/var/run/secrets/gateway/hmac` secret. |
| **VI. Environmental Portability** | PASS | PASS | Canonical issuer resolves automatically from `KEYCLOAK_PUBLIC_URL` / `KEYCLOAK_ISSUER_URL` env config across `local-k3s` and `vps-k3s`. |
| **VII. Distinct Lifecycle Signals & Browser CORS** | PASS | PASS | Preserves startup/readiness/liveness probes and `PUBLIC_APP_URL` in `ALLOWED_ORIGINS`. |

## Project Structure

### Documentation (this feature)

```text
specs/009-host-scoped-shared-auth/
├── plan.md                                    # Implementation plan (/speckit-plan output)
├── research.md                                # Phase 0 architectural decisions
├── data-model.md                              # Phase 1 entities, encryption schemas & state transitions
├── quickstart.md                              # Phase 1 runnable validation guide
├── contracts/
│   ├── gateway-internal-auth-api.md           # Internal Gateway HTTP contract (:8080)
│   └── shared-verification-module.md          # Shared Project Backend BFF & middleware contract
└── tasks.md                                   # Phase 2 task breakdown (/speckit-tasks output)
```

### Source Code (repository root)

```text
gateway/
├── cmd/gateway/
│   └── main.go                                # Mounts /oauth/start, /oauth/callback, /auth/exchange, /auth, /auth/logout
├── internal/
│   ├── auth/
│   │   ├── handler.go                         # Headless Google redirect, AES-GCM state, sealed session_handle, /auth/exchange & /auth
│   │   ├── logout.go                          # Bearer session_handle & legacy cookie online session revocation
│   │   ├── crypto.go                          # AES-256-GCM seal/unseal & single-use handoff ticket store
│   │   └── session_validator.go               # Active token validation & transparent refresh orchestration
│   ├── config/
│   │   └── config.go                          # CanonicalIssuer() helper & runtime config
│   └── keycloak/
│       ├── oidc.go                            # Token introspection with actual OAuth token & refresh grant
│       └── session.go                         # Keycloak Admin API session termination
├── pkg/
│   └── projectauth/
│       ├── middleware.go                      # Reusable Go BFF handlers (/api/auth/login, /callback, /session, /logout) & RequireAuth middleware
│       └── middleware_test.go                 # Multi-project & end-to-end BFF verification tests
└── tests/
    ├── handler_test.go                        # Unit tests for kc_idp_hint, AES-GCM state, reload recovery & ticket exchange
    ├── contract_test.go                       # Contract tests for canonical issuer, token refresh & audience isolation
    ├── logout_test.go                         # Logout tests for Bearer session_handle & revocation
    └── revocation_contract_test.go            # Online revocation contract tests

docs/
├── architecture-auth-routing.md               # Updated 4-layer BFF + sealed session_handle architecture docs
└── project-auth-verification.md               # Updated language-agnostic HTTP & Go shared module integration guide
```

**Structure Decision**: Extend the existing `gateway/` Go standard-library module with `gateway/internal/auth/crypto.go` for AES-256-GCM state/session sealing and `gateway/pkg/projectauth/` as the exported, zero-dependency shared verification package for Go project backends, while documenting the language-agnostic HTTP contract in `specs/009-host-scoped-shared-auth/contracts/`.

## Complexity Tracking

> Zero Constitution violations. No complexity exceptions required.
