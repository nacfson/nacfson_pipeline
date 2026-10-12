# Research: 007-pn-readiness-restart

## R1. Frontend startup and readiness target path

**Decision**: Both frontend `startupProbe` and `readinessProbe` use `httpGet` path `/healthz` on container port `80`.

**Rationale**: The Project PN frontend image renders nginx locations that proxy `/healthz` and `/readyz` to `PN_BACKEND_HOST:PN_BACKEND_PORT` without rewriting the path. `/healthz` on the backend is process-availability only (`{"status":"ok"}`), matching the spec assumption that FE↔BE means backend process reachability, not DB readiness. Hitting FE `/healthz` proves nginx is up **and** the FE→BE hop succeeds under NetworkPolicy.

**Alternatives considered**:

| Option | Rejected because |
| --- | --- |
| FE readiness `/readyz` (proxied DB ping) | Couples FE Ready to Postgres; spec/constitution require process-availability for the disconnect signal |
| FE readiness `/` only + startup `/healthz` | Hides runtime disconnect from Ready state; conflicts with FR-002 / Principle VII |
| `exec` curl to `pn-backend:8080` | Needs a client binary in the Restricted image; nginx proxy already exists |

## R2. Frontend liveness target

**Decision**: Keep frontend `livenessProbe` on `httpGet` `/` port `80`.

**Rationale**: Local static/SPA serve proves the process is alive without peer dependency. Dependency failures must not restart the frontend (FR-003, FR-008, Principle VII).

**Alternatives considered**: Liveness on `/healthz` — rejected; backend blips would restart nginx.

## R3. Backend probe split

**Decision**: Backend `livenessProbe` remains `/healthz` on `8080`. Backend `readinessProbe` changes to `/readyz` on `8080`. No backend `startupProbe` required in this feature.

**Rationale**: App contract documents `/healthz` = process up, `/readyz` = Postgres pool ping. Current manifests incorrectly use `/healthz` for both. Backend startup ordering is already covered by readiness withholding API traffic; FE startup covers FE boot wait for BE process.

**Alternatives considered**:

| Option | Rejected because |
| --- | --- |
| Keep BE readiness `/healthz` | Violates FR-004 / Principle VII |
| Add BE startupProbe on `/readyz` | Optional nicety; not required by spec; readiness already gates traffic |

## R4. Restart policy

**Decision**: Set `spec.template.spec.restartPolicy: Always` explicitly on both `pn-frontend` and `pn-backend` Deployments.

**Rationale**: Spec FR-009 and Principle VII require an explicit always-restart declaration for always-on app workloads. For Deployment-managed Pods, `Always` is the only admissible value; stating it removes reliance on an undeclared default and documents intent in Git.

**Alternatives considered**: `OnFailure` / `Never` — invalid for Deployments and wrong for long-running HTTP apps.

## R5. Startup timing window

**Decision**: Frontend `startupProbe` uses `httpGet` `/healthz`, `periodSeconds: 5`, `timeoutSeconds: 3`, `failureThreshold: 36` (≈180s window), `successThreshold: 1`. Omit aggressive `initialDelaySeconds` (0 or small) so checks begin promptly.

**Rationale**: Single-node bring-up may still be starting `pn-backend` and dependencies. A ~3 minute startup window avoids killing FE during normal ordered boot while still failing closed if BE never answers. After success, kubelet stops startup probes and runs liveness/readiness.

**Alternatives considered**: 60s window — may be tight if BE migrations/proxy handshake lag; 600s — hides permanent BE absence too long for a 1-replica project slice.

## R6. Readiness timing (disconnect signal)

**Decision**: Frontend `readinessProbe` on `/healthz`: `periodSeconds: 5`, `timeoutSeconds: 3`, `failureThreshold: 3` (≈15s to NotReady), `successThreshold: 1`. Backend readiness on `/readyz` with the same period/timeout/failureThreshold pattern for consistency.

**Rationale**: SC-002 wants NotReady within one evaluation cadence after sustained failure; `failureThreshold: 3` filters single flake without erasing the disconnect signal. SC-004 restore under 60s is easily met at 5s periods.

**Alternatives considered**: `failureThreshold: 1` — noisier flaps; `failureThreshold: 12` — delays disconnect visibility for future notifications.

## R7. Notification scope

**Decision**: Do not implement Alertmanager, webhooks, or chat sinks in this feature. Document Ready=False / missing Endpoints / Probe Failed Events as the consumable disconnect signal ([disconnect-signal.md](./contracts/disconnect-signal.md)).

**Rationale**: FR-007 / Principle VII defer push delivery. Building sinks now expands scope and platform footprint without an approved notification design.

**Alternatives considered**: CronJob curl notifier — useful later; out of scope for 007.

## R8. NetworkPolicy and ingress

**Decision**: No NetworkPolicy or IngressRoute edits.

**Rationale**: FE→BE TCP 8080 already allowed; door already splits `/` → FE and `/api` → BE. FR-010 / FR-012 forbid widening isolation or changing the door for this feature.

## R9. Image digests

**Decision**: Keep currently pinned frontend/backend digests; only edit probe and `restartPolicy` fields.

**Rationale**: FR-011 — status routes already exist in the running image contract (`/healthz`, `/readyz` proxy on FE; handlers on BE).
