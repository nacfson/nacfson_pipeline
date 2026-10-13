# Implementation Plan: Project PN Startup, Readiness Disconnect Signal, Restart Policy, and Browser API Origin Access

**Branch**: `007-pn-readiness-restart` | **Date**: 2026-10-12 | **Spec**: [spec.md](./spec.md)

**Input**: Feature specification from `specs/007-pn-readiness-restart/spec.md` (revised to include browser CORS / `ALLOWED_ORIGINS`)

## Summary

Project PN gets an explicit **two-path** access contract:

1. **Cluster hop** — FE **startup** waits for first FE nginx → BE auth router via `/api/auth/language-options` (amended 2026-10-13); FE **readiness** repeats that hop as the durable **unconnected** signal; FE **liveness** stays on `/`; BE **liveness** `/healthz`; BE **readiness** `/readyz`; both Deployments declare `restartPolicy: Always`.
2. **Browser API path** — Backend declares `PUBLIC_APP_URL` and `ALLOWED_ORIGINS` such that the public app origin is allowlisted (env patches for VPS/local). This closes the proven gap where probes were green but SPA `fetch` preflight failed without `Access-Control-Allow-Origin`.

No push notification sinks. No IngressRoute / NetworkPolicy edits. No image rebuild. Constitution Principle VII / Amendment 2.1.0 expanded for both paths. Research: [research.md](./research.md).

## Technical Context

**Language/Version**: Kubernetes YAML (Deployments + Flux patches); existing Project PN nginx + Go API status/CORS routes (no app rebuild).

**Primary Dependencies**: Existing `pn-frontend` / `pn-backend` images (nginx proxies `/healthz`/`/readyz`; backend chi CORS + `ALLOWED_ORIGINS`), existing NetworkPolicy FE→BE `:8080`, Flux `proj-pn` kustomization.

**Storage**: N/A for probe/restart/CORS declarations. Backend readiness continues to depend on vault-db-proxy → Postgres.

**Testing**: Manifest inspection; `kubectl` Ready/Endpoints/Events; FE/BE status curls; **CORS preflight** against public `/api` with `Origin: $PUBLIC_APP_URL` per [quickstart.md](./quickstart.md).

**Target Platform**: local k3s, VPS k3s; same manifests portable to EKS/GKE.

**Project Type**: Platform GitOps workload declaration change (Project PN implementation; governance generalizes).

**Performance Goals**: Post-startup FE disconnect → NotReady within one readiness interval; restore → Ready under 60s; zero restart storms from readiness-only failures; CORS allowlist present at deploy time (no probe interval for ACAO).

**Constraints**: Restricted PSA; no NetworkPolicy widen; no image change; no door route change; no notification delivery; kubelet `httpGet` cannot assert CORS headers — browser path enforced by config + validation; constitution 2.1.0 draft still needs maintainer approval.

**Scale/Scope**: Two Deployments under `deploy/projects/pn/workloads/`; Flux patches under `clusters/*/projects/proj-pn.yaml`; constitution Principle VII.

## Constitution Check

*GATE: Evaluated against constitution draft 2.1.0 (Principle VII, including browser origin allowlist). Re-checked after Phase 1 design.*

| Principle / rule | Before research | After design |
| --- | --- | --- |
| I. Declarative GitOps | Pass. Probe, restart, and CORS env are Git manifests/patches. | Pass. |
| II. Isolation / Restricted / ClusterIP | Pass if probes use existing FE→BE allowlist and no Service type/port change. | Pass. |
| III. Identity / public surface | Pass if no new public admin/health exposure; door unchanged. | Pass. |
| IV. Budgets | Pass. No request/limit change. | Pass. |
| V. Secrets / vault | Pass. CORS origins are non-secret public URLs. | Pass. |
| VI. Portability | Pass. Env patches carry per-environment public origin. | Pass. |
| VII. Distinct lifecycle + browser origin | Pass if cluster hop matrix holds **and** `PUBLIC_APP_URL` ∈ `ALLOWED_ORIGINS`. | Pass. Contracts encode both. |
| Probe role separation | Pass under cluster matrix; browser path is config/validation, not a second liveness. | Pass. |
| Amendment 2.1.0 maintainer approval | Soft gate. | Noted in Complexity Tracking. |

Initial result: pass for implementation design; commit gate for constitution approval remains procedural.

Post-design result: pass.

## Project Structure

### Documentation (this feature)

```text
specs/007-pn-readiness-restart/
├── plan.md
├── research.md
├── data-model.md
├── quickstart.md
├── validation-note.md
├── contracts/
│   ├── probe-matrix.md
│   ├── disconnect-signal.md
│   └── browser-api-access.md
└── tasks.md
```

### Source code (repository root)

```text
.specify/memory/constitution.md
deploy/projects/pn/workloads/frontend-deployment.yaml
deploy/projects/pn/workloads/backend-deployment.yaml
clusters/vps-k3s/projects/proj-pn.yaml
clusters/local-k3s/projects/proj-pn.yaml
deploy/projects/pn/boundary/network-policy.yaml   # verify only
deploy/projects/pn/workloads/ingress-route.yaml   # verify only
```

**Structure Decision**: Manifest + Flux patch changes on existing Project PN Deployments; constitution Principle VII covers cluster hop **and** browser origin allowlist. No new services, images, or notification components.

## Two-path access (why probes alone are insufficient)

```text
Cluster hop (probes)                    Browser API path (SPA)
-------------------------               ------------------------------
kubelet                                 Browser JS
  -> FE :80 /healthz                      -> https://<door>/api/...
  -> pn-backend:8080/healthz              (cookies, CORS, Traefik /api)
     [no Origin / no ACAO needed]         Cache-Control/Pragma -> OPTIONS
                                          Origin: https://<door>
                                          needs ALLOWED_ORIGINS contain door
```

| Green signal | Proves | Does not prove |
| --- | --- | --- |
| FE readiness `/healthz` | Cluster FE→BE process hop | Browser CORS / public `/api` success |
| FE startup `/api/auth/language-options` | Cluster hop incl. nginx `/api/` proxy and BE auth router | Traefik routes, gateway, Keycloak, browser CORS (see `scripts/smoke-auth-chain.sh`) |
| BE Ready `/readyz` | DB dependency | CORS allowlist |
| `ALLOWED_ORIGINS` ∋ `PUBLIC_APP_URL` + ACAO on preflight | Browser-shaped Origin accepted | Every end-user network/extension |

## Probe failure effects (restart architecture)

Readiness does **not** restart the process. Startup failure after the window **does**. CORS misconfig does **not** flip FE Ready by itself in this feature (config gate + validation).

| Probe / gate | On failure | Affects process restart? | Operator meaning |
| --- | --- | --- | --- |
| **readinessProbe** | Pod removed from Service Endpoints | **No** | FE: cluster **unconnected**; BE: dependency not usable |
| **livenessProbe** | Container killed | **Yes** | Process considered dead/stuck |
| **startupProbe** (FE) | Under threshold: stay Starting | **Not yet** | Boot wait for first FE→BE hop |
| **startupProbe** (FE) | Threshold exhausted | **Yes** | **Startup-failure restart** |
| **ALLOWED_ORIGINS` gate** | Preflight lacks ACAO | **No** (pods may stay Ready) | Browser API path non-compliant — fix Git env |

```text
FE container start
        |
        v
 startupProbe /api/auth/language-options  --fail (count < 36)--> retry; process stays up
        |
        | fail count reaches failureThreshold (36)
        v
   kill container ---> restartPolicy Always ---> new start
        |
        | success once
        v
 readinessProbe /healthz --fail--> NotReady (cluster disconnect; NO restart)
 livenessProbe  /        --fail--> kill + restart

Parallel config gate (not a kubelet probe):
 PUBLIC_APP_URL ∈ ALLOWED_ORIGINS
 + quickstart CORS OPTIONS asserts ACAO
```

### Startup-failure restart parameters (frontend)

| Field | Value | Effect |
| --- | --- | --- |
| `startupProbe.httpGet.path` | `/api/auth/language-options` | Must reach the BE auth router via nginx `location /api/` (amended 2026-10-13) |
| `periodSeconds` | `5` | Attempt cadence |
| `timeoutSeconds` | `3` | Per-attempt timeout |
| `failureThreshold` | `36` | `36 × 5s ≈ 180s` then **restart** |
| `successThreshold` | `1` | First success ends startup probing |
| `restartPolicy` | `Always` | Required so startup kill results in a new attempt |

## How to change settings (explicit)

Contracts: [probe-matrix.md](./contracts/probe-matrix.md), [disconnect-signal.md](./contracts/disconnect-signal.md), [browser-api-access.md](./contracts/browser-api-access.md). Decisions: [research.md](./research.md).

### Files to edit

| File | Change |
| --- | --- |
| `deploy/projects/pn/workloads/frontend-deployment.yaml` | `restartPolicy: Always`; `startupProbe` + readiness `/healthz`; liveness `/` |
| `deploy/projects/pn/workloads/backend-deployment.yaml` | `restartPolicy: Always`; readiness `/readyz`; liveness `/healthz`; env `ALLOWED_ORIGINS`, `PUBLIC_APP_URL` |
| `clusters/vps-k3s/projects/proj-pn.yaml` | Patch BE env: `PUBLIC_APP_URL=https://pn.nacfson.cloud`, matching `ALLOWED_ORIGINS` |
| `clusters/local-k3s/projects/proj-pn.yaml` | Patch BE env: `PUBLIC_APP_URL=https://pn.example.com`, matching `ALLOWED_ORIGINS` |
| `.specify/memory/constitution.md` | Principle VII + Amendment 2.1.0 include browser origin allowlist |

### Frontend probe block (required)

```yaml
spec:
  template:
    spec:
      restartPolicy: Always
      containers:
        - name: frontend
          livenessProbe:
            httpGet: { path: /, port: 80 }
            initialDelaySeconds: 5
            periodSeconds: 10
            timeoutSeconds: 3
          readinessProbe:
            httpGet: { path: /healthz, port: 80 }
            periodSeconds: 5
            timeoutSeconds: 3
            failureThreshold: 3
            successThreshold: 1
          startupProbe:
            httpGet: { path: /api/auth/language-options, port: 80 }
            periodSeconds: 5
            timeoutSeconds: 3
            failureThreshold: 36
            successThreshold: 1
```

Notes:

- FE `/healthz` is nginx → `pn-backend:8080/healthz` (process hop, readiness). FE startup uses `/api/auth/language-options` (nginx `location /api/` → backend router). Do **not** point FE readiness at `/readyz` (DB coupling).
- Do **not** pretend FE Ready proves CORS.

### Backend probe + CORS env (required)

```yaml
spec:
  template:
    spec:
      restartPolicy: Always
      containers:
        - name: backend
          env:
            - name: ALLOWED_ORIGINS
              value: "http://localhost:8081,http://localhost:19006,tauri://localhost,http://tauri.localhost,https://pn.nacfson.cloud,https://pn.example.com"
            - name: PUBLIC_APP_URL
              value: "https://pn.nacfson.cloud"
          livenessProbe:
            httpGet: { path: /healthz, port: 8080 }
            initialDelaySeconds: 5
            periodSeconds: 10
            timeoutSeconds: 3
          readinessProbe:
            httpGet: { path: /readyz, port: 8080 }
            periodSeconds: 5
            timeoutSeconds: 3
            failureThreshold: 3
            successThreshold: 1
```

Environment Flux patches **override** `PUBLIC_APP_URL` / `ALLOWED_ORIGINS` to the door host for that cluster.

### Must not change

| Item | Why |
| --- | --- |
| Image digests / tags | Status + CORS middleware already in image (FR-011) |
| `PN_BACKEND_HOST` / `PN_BACKEND_PORT` | Already `pn-backend` / `8080` |
| NetworkPolicy FE→BE `:8080` | Already allows the hop (FR-010) |
| IngressRoute `/` and `/api` | Door unchanged (FR-012) |
| Notification sinks | Deferred |
| Retarget readiness to assert ACAO via `httpGet` | kubelet cannot check response headers; use config + quickstart (R10) |

### Tuning knobs

| Goal | Knob | Default |
| --- | --- | --- |
| Longer FE boot wait | FE `startupProbe.failureThreshold` | `36 × 5s ≈ 180s` |
| Faster cluster disconnect visibility | FE `readinessProbe.failureThreshold` | `3` (~15s) |
| Add another approved SPA origin | Extend `ALLOWED_ORIGINS` (keep `PUBLIC_APP_URL` in the list) | env / Flux patch |

## Complexity Tracking

| Violation | Why Needed | Simpler Alternative Rejected Because |
| --- | --- | --- |
| Constitution 2.1.0 unapproved for commit | Governance requires maintainer approval | Shipping probes/CORS without Principle VII leaves FR-013 undocumented |
| Browser path not encoded as kubelet readiness | `httpGet` cannot assert `Access-Control-Allow-Origin` | Fake “Ready = CORS OK” would lie; exec/header probes need brittle scripting or image changes (FR-011) |
