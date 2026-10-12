# Research: 007-pn-readiness-restart

## R1. Frontend startup and readiness target path

**Decision**: Both frontend `startupProbe` and `readinessProbe` use `httpGet` path `/healthz` on container port `80`.

**Rationale**: The Project PN frontend image renders nginx locations that proxy `/healthz` and `/readyz` to `PN_BACKEND_HOST:PN_BACKEND_PORT` without rewriting the path. `/healthz` on the backend is process-availability only, matching the cluster-hop disconnect signal. Hitting FE `/healthz` proves nginx is up **and** the FE→BE hop succeeds under NetworkPolicy.

**Alternatives considered**:

| Option | Rejected because |
| --- | --- |
| FE readiness `/readyz` (proxied DB ping) | Couples FE Ready to Postgres; spec/constitution require process-availability for the cluster disconnect signal |
| FE readiness `/` only + startup `/healthz` | Hides runtime cluster disconnect from Ready state; conflicts with FR-002 / Principle VII |
| `exec` curl to `pn-backend:8080` | Needs a client binary contract; nginx proxy already exists |

## R2. Frontend liveness target

**Decision**: Keep frontend `livenessProbe` on `httpGet` `/` port `80`.

**Rationale**: Local static/SPA serve proves the process is alive without peer dependency. Dependency failures must not restart the frontend (FR-003, FR-008, Principle VII).

**Alternatives considered**: Liveness on `/healthz` — rejected; backend blips would restart nginx.

## R3. Backend probe split

**Decision**: Backend `livenessProbe` remains `/healthz` on `8080`. Backend `readinessProbe` changes to `/readyz` on `8080`. No backend `startupProbe` required in this feature.

**Rationale**: App contract documents `/healthz` = process up, `/readyz` = Postgres pool ping. Manifests must not use `/healthz` for both.

**Alternatives considered**:

| Option | Rejected because |
| --- | --- |
| Keep BE readiness `/healthz` | Violates FR-004 / Principle VII |
| Add BE startupProbe on `/readyz` | Optional; readiness already gates API traffic |

## R4. Restart policy

**Decision**: Set `spec.template.spec.restartPolicy: Always` explicitly on both `pn-frontend` and `pn-backend` Deployments.

**Rationale**: Spec FR-009 and Principle VII require an explicit always-restart declaration for always-on app workloads.

**Alternatives considered**: `OnFailure` / `Never` — invalid for Deployments and wrong for long-running HTTP apps.

## R5. Startup timing window

**Decision**: Frontend `startupProbe` uses `periodSeconds: 5`, `timeoutSeconds: 3`, `failureThreshold: 36` (≈180s), `successThreshold: 1`.

**Rationale**: Single-node bring-up may still be starting `pn-backend` and dependencies. A ~3 minute window fails closed if BE never answers without killing FE during normal ordered boot.

**Alternatives considered**: 60s — tight for migrations/proxy handshake; 600s — hides permanent BE absence too long.

## R6. Readiness timing (cluster disconnect signal)

**Decision**: Frontend `readinessProbe` on `/healthz`: `periodSeconds: 5`, `timeoutSeconds: 3`, `failureThreshold: 3`. Backend readiness on `/readyz` with the same period/timeout/failureThreshold pattern.

**Rationale**: SC-002 wants NotReady within one evaluation cadence after sustained failure; threshold 3 filters single flake.

## R7. Notification scope

**Decision**: Do not implement Alertmanager, webhooks, or chat sinks. Document Ready=False / Endpoints / Events as the consumable **cluster** disconnect signal ([disconnect-signal.md](./contracts/disconnect-signal.md)).

**Rationale**: FR-007 defers push delivery.

## R8. NetworkPolicy and ingress

**Decision**: No NetworkPolicy or IngressRoute edits.

**Rationale**: FE→BE TCP 8080 already allowed; door already splits `/` → FE and `/api` → BE. FR-010 / FR-012.

## R9. Image digests

**Decision**: Keep currently pinned frontend/backend digests; edit probe fields, `restartPolicy`, and backend CORS-related env only.

**Rationale**: FR-011 — status routes and CORS middleware already exist in the running image contract.

## R10. Browser API path vs encoding CORS into readinessProbe

**Decision**: Enforce browser access via **declared** `PUBLIC_APP_URL` + `ALLOWED_ORIGINS` (base Deployment + per-env Flux patches) and **validate** with synthetic CORS preflight in [quickstart.md](./quickstart.md). Do **not** retarget FE readiness to “prove CORS” via kubelet `httpGet`.

**Rationale**: Production failure mode was SPA `fetch` with `Cache-Control`/`Pragma` forcing CORS preflight; `Origin: https://pn.nacfson.cloud` received no `Access-Control-Allow-Origin` while defaults allowed localhost only. Kubelet `httpGet` cannot assert response headers, so a Ready=true from `/healthz` would continue to lie about browser success if we only changed probes. Config + validation close the gap without image rebuild or Restricted exec complexity.

**Alternatives considered**:

| Option | Rejected because |
| --- | --- |
| FE readiness `exec` wget asserting ACAO | Brittle scripting in probe; needs `PUBLIC_APP_URL` in FE; Restricted/image tool assumptions; conflates cluster disconnect signal with CORS config |
| New nginx `/corsz` location requiring image rebuild | Violates FR-011 for this iteration |
| Ignore CORS; claim probes are enough | Proven false in production (SPA connection banner with Ready pods) |
| Empty `ALLOWED_ORIGINS` to disable CORS middleware | Breaks credentialed cross-tooling and still fails when browsers send Origin on non-simple requests; app defaults install localhost allowlist anyway |

## R11. Where to set public origins

**Decision**: Base `backend-deployment.yaml` lists approved localhost/Tauri origins plus known public hosts (`pn.nacfson.cloud`, `pn.example.com`). `clusters/vps-k3s` and `clusters/local-k3s` Flux patches set `PUBLIC_APP_URL` and a matching `ALLOWED_ORIGINS` for that door.

**Rationale**: Principle VI — environment differences stay in overlays/patches. Strategic merge on env by name lets patches override without forking the whole Deployment.

## R12. Relationship to SPA “Could not connect” UI

**Decision**: Document that App treats non-401 `me()` failures as connection errors; CORS/network failures surface that banner even when BE returns 401 to curl without browser CORS. Fixing allowlist addresses the class of failure where preflight blocks the readable HTTP response.

**Rationale**: Aligns operator mental model: cluster Ready ≠ user can call `/api` from the SPA.
