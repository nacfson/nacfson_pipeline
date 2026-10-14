# Auth, Routing and GitOps Architecture

Status: sections 1-4 describe how things work. Section 5 is implemented (auth route
hostname substitution). Section 6 lists the remaining login fixes.

## 1. How a change reaches the cluster

```mermaid
flowchart TD
    GH["GitHub repo, branch main"]
    S["clusters/<cluster>/flux-system/gotk-sync.yaml<br/>Flux watches ./clusters/<cluster>"]
    K["clusters/<cluster>/kustomization.yaml"]
    L["clusters/<cluster>/layers.yaml<br/>what to install, in what order, with which values"]
    D["deploy/platform/* (shared base manifests)"]
    P["clusters/<cluster>/projects/proj-pn.yaml"]
    W["deploy/projects/pn/workloads"]
    C["Cluster state"]
    GH --> S --> K --> L --> D --> C
    K --> P --> W --> C
```

- `<cluster>` is `vps-k3s` (Oracle) or `local-k3s`.
- Flux has `prune: true`. Anything changed by hand with `kubectl` is reverted.
  Change Git, not the cluster.
- DNS records and the Oracle firewall are outside this repo and outside this chain.

## 2. File roles

| Location | Role |
|---|---|
| `deploy/platform/*` | Shared base manifests. They contain placeholder hosts (`*.example.com`). |
| `deploy/projects/pn/boundary` | pn namespace, network policies, quotas, RBAC |
| `deploy/projects/pn/workloads` | pn frontend and backend Deployments, Services, IngressRoute |
| `clusters/<cluster>/layers.yaml` | One Flux `Kustomization` per layer: path, `dependsOn` order, per-cluster patches |
| `clusters/<cluster>/projects/` | The Flux layer for each project |
| `gateway/` | Go source of the auth gateway |
| `.github/workflows/gitops.yaml` | CI render-validate before merge |

Layer order: `governance` -> `vault` -> `database` -> `identity` -> `ingress` -> `tls`;
`project-boundaries` -> `projects`.

## 3. Request path and auth components (Spec 009: 4-Layer Cookie-Free BFF)

| Component | Job |
|---|---|
| Traefik | Single entry point. Routes by host and path (`/api/*` -> project backend; `/*` -> project frontend). Unmatched requests get 404. |
| Project Frontend (`pn-frontend`) | Owns 100% of the Login UI. Talks exclusively to its own Project Backend (`/api/auth/*` and `/api/*`) using `Authorization: Bearer <session_handle>` with **zero cookies**. |
| Project Backend (`pn-backend` + `gateway/pkg/projectauth`) | Exposes `/api/auth/login`, `/api/auth/callback`, `/api/auth/session`, and `/api/auth/logout`; exchanges callback tickets and verifies `session_handle` against internal `gateway.identity.svc:8080`. |
| Gateway (`gateway/`, namespace `identity`) | Headless login logic only: direct Google redirect (`/oauth/start` with `kc_idp_hint=google` + AES-256-GCM encrypted PKCE `state`), callback handoff (`/oauth/callback`), internal ticket exchange (`POST /auth/exchange`), `session_handle` verification & transparent token refresh (`GET /auth`), and online logout (`POST /auth/logout`). |
| Keycloak | Internal identity broker (`keycloak-service.identity.svc:8080`). Brokers Google sign-in and maintains the root user session in PostgreSQL. |
| `strip-forged-headers` middleware | Removes client-sent identity headers at the edge and inside `RequireAuth`. |

4-Layer Cookie-Free Login & API Flow:

```mermaid
sequenceDiagram
    participant FE as Project Frontend (UI)
    participant BE as Project Backend (/api/*)
    participant G as Internal Gateway (gateway:8080)
    participant K as Keycloak + Google OIDC
    FE->>BE: 1. GET /api/auth/login
    BE->>G: 2. Redirect /oauth/start?return=https://pn.*/api/auth/callback
    G->>K: 3. 302 Direct to Google (kc_idp_hint=google + AES-GCM encrypted PKCE state)
    K->>G: 4. GET /oauth/callback?code=...&state=...
    G->>K: 5. Exchange code + decrypted PKCE verifier (:8080)
    G->>BE: 6. Seal session_handle, redirect /api/auth/callback?ticket=... (0 cookies)
    BE->>G: 7. POST http://gateway.identity.svc:8080/auth/exchange {ticket}
    G-->>BE: 8. 200 OK {session_handle, subject, issuer, audience}
    BE->>FE: 9. Prepare DB user & redirect /?code=<30s-pickup-code>
    FE->>BE: 10. POST /api/auth/session {code} -> {session_handle, user, requirements}
    FE->>BE: 11. GET /api/* (Authorization: Bearer <session_handle>)
    BE->>G: 12. GET http://gateway.identity.svc:8080/auth (Bearer <session_handle>)
    G-->>BE: 13. 200 OK (X-User-Subject, X-User-Issuer, auto-refreshes token if needed)
    BE-->>FE: 14. 200 OK JSON response
```

## 4. Known problem: patches overwrite shared values

The base route in `deploy/platform/ingress/ingress-allowlist.yaml` allows
`/oauth/start` and `/oauth/callback`. The cluster patch in `layers.yaml` uses
`op: replace` on the whole `match` string, so it overwrites the base. The patch
was written before `/oauth/start` existed and kept only `/oauth/callback`.
Result: Traefik returns 404 for `/oauth/start` on the live cluster, and the
base file is correct but has no effect.

Rule that caused the bug: the cluster patch owned **both** the hostname and the
path list.

## 5. Change: auth route paths live in one place (implemented)

Status line at the top of this file applies to section 6. This section is
implemented in the same change as spec 004 R3 amendment.

Ownership:

| Information | Owner |
|---|---|
| Which paths are routed or allowed | `deploy/platform/ingress/ingress-allowlist.yaml` (one copy for all clusters) |
| Which hostname | `clusters/<cluster>/layers.yaml`, `ingress` layer, `postBuild.substitute.AUTH_HOST` |

```yaml
# deploy/platform/ingress/ingress-allowlist.yaml (base)
match: Host(`${AUTH_HOST}`) && (PathPrefix(`/oauth/start`) || PathPrefix(`/oauth/callback`))

# clusters/vps-k3s/layers.yaml, ingress layer (local-k3s sets auth.example.com)
postBuild:
  substitute:
    AUTH_HOST: auth.nacfson.cloud
```

Why this is allowed although spec 004 R3 bans `postBuild`:
- The ban is a **project rule**, not a Kubernetes or Flux limit. It exists because Flux
  replaces every `${...}` in a layer, and other layers contain shell `${VAULT_TOKEN}`
  and Keycloak `${VAULT:...}` placeholders that were corrupted in a dry run.
- `deploy/platform/ingress/` contains no other `${`, so none of those cases apply.
  R3 now has a narrow amendment: only layer `ingress`, only `AUTH_HOST`.

Guard rails in `scripts/render-invariants.py`:
- Invariant 1: `postBuild` allowed only on `ingress`, only `substitute`, only `AUTH_HOST`, non-empty.
- Invariant 9: no rendered IngressRoute `match` with an unresolved `${` or an empty `Host()`.

Verified with `scripts/render-validate.sh` on both clusters, plus negative tests
(postBuild on `identity`, extra variable, empty `AUTH_HOST`, removed `postBuild`) that all fail.

Side effect: the old `local-k3s` patch allowed `/realms/platform/broker/google/endpoint`,
and the `vps-k3s` patch allowed `/realms/platform/broker/` (the base value). Local now
follows the base, so its broker path is `/realms/platform/broker/`.

Out of scope: `COOKIE_DOMAIN`, `PROJECT_RETURN_HOSTS`, `CENTRAL_AUTH_URL`, the pn
route hosts and the Keycloak realm JSON still use per-cluster patches.

## 6. Login fixes that follow

| # | Where | Change |
|---|---|---|
| A | `layers.yaml` / ingress base | Done (PR #34 and section 5): `/oauth/start` is routed. |
| B | `layers.yaml` (identity, gateway env) | Done in PR #34. Set `PROJECT_RETURN_HOSTS=pn.nacfson.cloud`. Unset now, defaults to `pn.example.com`, so `/oauth/start` would return 400. |
| C | pn `ingress-route.yaml` | Add `forward-auth` to `/api`. Add a protected `/login` route. Keep `/` public. Keep `/api/auth/language-options` public. |
| D | `gateway/` `/auth` | Return `302` to `/oauth/start?return=<url>` for browser navigations. Keep `401` JSON for API calls. Add tests. |
| E | Project_PN frontend | Sign-in button navigates to `<EXPO_PUBLIC_AUTH_HOST>/oauth/start?return=<origin>/` (Project_PN PR #67, pending merge). Chosen over a `/login` route for now. |
| F | Project_PN `packages/api/src/client.ts` | Read the response body once. A non-refreshed 401 used to throw `body stream already read`, so the SPA showed "Could not connect to the backend server" (Project_PN PR #67, pending merge). |
| G | FE `startupProbe` and `scripts/smoke-auth-chain.sh` | Startup probe uses `/api/auth/language-options`; the smoke script checks the public chain (spec 007 amendment). |

Verification after A and B: open
`https://auth.nacfson.cloud/oauth/start?return=https://pn.nacfson.cloud/` in a
browser. Expected: redirect to Keycloak and Google sign-in.

## 7. Open decisions

- Who starts the login redirect. Interim answer: the SPA calls `/oauth/start`
  directly (row E). Long term the gateway 302 (row D) keeps the SPA free of the auth
  host and gives every project login for free. Revisit when row C (`forward-auth`
  on `/api`) is done, because the backend needs the injected JWT either way.

## 8. Publishing and pinning images

The cluster pulls `ghcr.io/nacfson/project-pn-*:<git sha>@sha256:<registry digest>`
(`deploy/projects/pn/workloads/*-deployment.yaml`).

- A local image ID (`docker images --digests`, for example `project-pn-frontend:local`)
  is **not** that digest and the image is not in GHCR. Pinning it gives `ImagePullBackOff`.
- Flow: merge the Project_PN change to `main`, let Project_PN CI
  (`publish-ghcr-images.yml`) build and push, take the digest it prints
  (`...@sha256:<digest>`), then update the pin here in a PR.
- Project_PN PR #67 changes frontend code only. The backend source is unchanged, so
  the backend pin stays as it is.
