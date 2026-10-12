# Implementation Plan: Project PN Startup, Readiness Disconnect Signal, and Restart Policy

**Branch**: `007-pn-readiness-restart` | **Date**: 2026-10-12 | **Spec**: [spec.md](./spec.md)

**Input**: Feature specification from `specs/007-pn-readiness-restart/spec.md`

## Summary

Project PN frontend and backend get an explicit lifecycle contract: frontend **startup** waits for the first successful hop to the backend process-availability check (`/healthz` via nginx), frontend **readiness** continuously repeats that hop as the durable **unconnected** signal for future notifications, frontend **liveness** stays on local site content (`/`), backend **liveness** stays on `/healthz`, backend **readiness** moves to `/readyz` (data dependency), and both Deployments declare `restartPolicy: Always`. No push notification sinks ship in this feature. Constitution Principle VII / Amendment 2.1.0 already draft the governance; this plan applies it to manifests. Research: [research.md](./research.md).

## Technical Context

**Language/Version**: Kubernetes YAML (Deployments); existing Project PN nginx + Go API status routes (no app rebuild).

**Primary Dependencies**: Existing `pn-frontend` / `pn-backend` images (nginx templates already proxy `/healthz` and `/readyz`), existing NetworkPolicy FE→BE `:8080`, Flux `proj-pn` kustomization.

**Storage**: N/A for probe/restart declarations. Backend readiness continues to depend on vault-db-proxy → Postgres.

**Testing**: Manifest inspection; `kubectl` Ready/Endpoints/Events checks; in-cluster or port-forward curls against FE `/`, `/healthz` and BE `/healthz`, `/readyz` per [quickstart.md](./quickstart.md).

**Target Platform**: local k3s, VPS k3s; same manifests portable to EKS/GKE.

**Project Type**: Platform GitOps workload declaration change (Project PN only for implementation; governance already general).

**Performance Goals**: Post-startup FE disconnect → NotReady within one readiness interval; restore → Ready under 60s; zero restart storms from readiness-only failures.

**Constraints**: Restricted PSA; no NetworkPolicy widen; no image change; no door route change; no notification delivery; constitution 2.1.0 draft still needs maintainer approval before commit of the governance file as authoritative.

**Scale/Scope**: Two Deployments under `deploy/projects/pn/workloads/`; constitution already updated in working tree for FR-013.

## Constitution Check

*GATE: Evaluated against constitution draft 2.1.0 (Principle VII present in working tree). Re-checked after Phase 1 design.*

| Principle / rule | Before research | After design |
| --- | --- | --- |
| I. Declarative GitOps | Pass. Probe and restart changes are Git manifests. | Pass. |
| II. Isolation / Restricted / ClusterIP | Pass if probes use existing FE→BE allowlist and no Service type/port change. | Pass. [probe-matrix.md](./contracts/probe-matrix.md) keeps paths on FE `:80` and BE `:8080`. |
| III. Identity / public surface | Pass if no new public admin/health exposure requirement; project status URLs unchanged. | Pass. No ingress edit. |
| IV. Budgets | Pass. No request/limit change. | Pass. |
| V. Secrets / vault | Pass. FE still has no DB creds; BE readiness uses existing `/readyz`. | Pass. |
| VI. Portability | Pass. Probe fields are core Kubernetes. | Pass. |
| VII. Distinct lifecycle signals | Pass if FE startup+readiness use BE process-availability, FE liveness local, BE ready = dependency, Always restart, no push notify. | Pass. Contracts encode the matrix. |
| Probe role separation constraint | Pass under same matrix. | Pass. |
| Amendment 2.1.0 maintainer approval | Soft gate: working tree may hold the draft; commit of constitution as binding still needs approval. | Noted in Complexity Tracking. |

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
├── contracts/
│   ├── probe-matrix.md
│   └── disconnect-signal.md
└── tasks.md                 # not created by this command
```

### Source code (repository root)

```text
.specify/memory/constitution.md
deploy/projects/pn/workloads/frontend-deployment.yaml
deploy/projects/pn/workloads/backend-deployment.yaml
deploy/projects/pn/boundary/network-policy.yaml   # verify only; no edit expected
deploy/projects/pn/workloads/ingress-route.yaml   # verify only; no edit expected
```

**Structure Decision**: Manifest-only change on existing Project PN Deployments, plus already-drafted constitution Principle VII. No new services, images, or notification components.

## Probe failure effects (restart architecture)

Readiness does **not** restart the process. Startup failure after the window **does**.

| Probe | On failure | Affects process restart? | Operator / future notify meaning |
| --- | --- | --- | --- |
| **readinessProbe** | Pod removed from Service Endpoints (NotReady) | **No** — process keeps running | FE: explicit **unconnected** disconnect signal; BE: dependency not usable |
| **livenessProbe** | Container killed | **Yes** — restart under `restartPolicy: Always` | Process considered dead/stuck |
| **startupProbe** (FE) | While under `failureThreshold`: stay Starting, retry | **Not yet** | Boot wait for first FE→BE hop |
| **startupProbe** (FE) | After `failureThreshold` exhausted | **Yes** — kubelet kills container; pod restarts under `Always` | **Startup-failure restart**, not a readiness notification event |

```text
FE container start
        |
        v
 startupProbe /healthz  --fail (count < 36)--> retry; process stays up
        |
        | fail count reaches failureThreshold (36)
        v
   kill container ---> restartPolicy Always ---> new pod/container start
        |
        | success once
        v
 readinessProbe /healthz --fail--> NotReady (disconnect signal; NO restart)
 livenessProbe  /        --fail--> kill + restart
```

**Invariant**: After startup has succeeded, FE→BE disconnect is readiness-only. Only a later **new** container start runs `startupProbe` again (for example after a liveness or startup-exhaustion restart).

### Startup-failure restart parameters (frontend)

| Field | Value | Effect |
| --- | --- | --- |
| `startupProbe.httpGet.path` | `/healthz` | Must reach BE process-availability via nginx |
| `periodSeconds` | `5` | Attempt cadence |
| `timeoutSeconds` | `3` | Per-attempt timeout |
| `failureThreshold` | `36` | `36 × 5s ≈ 180s` then **restart** |
| `successThreshold` | `1` | First success ends startup probing |
| `restartPolicy` | `Always` | Required so startup kill results in a new attempt |

This matches spec FR-001 / FR-001a / FR-001b and SC-001 / SC-001b.

## How to change probe settings (explicit)

Contract source of truth: [contracts/probe-matrix.md](./contracts/probe-matrix.md). Decisions: [research.md](./research.md). Apply only in the two Deployment files below. Do **not** edit NetworkPolicy or IngressRoute for probes.

### Files to edit

| File | Change |
| --- | --- |
| `deploy/projects/pn/workloads/frontend-deployment.yaml` | Add `restartPolicy: Always`; add `startupProbe`; retarget `readinessProbe`; keep `livenessProbe` on `/` |
| `deploy/projects/pn/workloads/backend-deployment.yaml` | Add `restartPolicy: Always`; retarget `readinessProbe` to `/readyz`; keep `livenessProbe` on `/healthz` |

### Before → after (frontend)

**Today (origin):**

| Role | Path | Port | Notes |
| --- | --- | --- | --- |
| liveness | `/` | `80` | `initialDelaySeconds: 5`, `periodSeconds: 10` |
| readiness | `/` | `80` | `initialDelaySeconds: 2`, `periodSeconds: 5` |
| startup | *(missing)* | — | — |
| restartPolicy | *(undeclared)* | — | Kubernetes default `Always`, not reviewable in Git |

**Required after:**

| Role | Path | Port | Timing |
| --- | --- | --- | --- |
| startup | `/healthz` | `80` | `periodSeconds: 5`, `timeoutSeconds: 3`, `failureThreshold: 36` (≈180s window), `successThreshold: 1` |
| readiness | `/healthz` | `80` | `periodSeconds: 5`, `timeoutSeconds: 3`, `failureThreshold: 3` (~15s to NotReady), `successThreshold: 1` |
| liveness | `/` | `80` | keep process-local; `periodSeconds: 10` (retain or set `timeoutSeconds: 3`) |
| restartPolicy | `Always` | pod template | under `spec.template.spec` |

Target YAML shape for `pn-frontend` (container probe block + restart):

```yaml
spec:
  template:
    spec:
      restartPolicy: Always
      containers:
        - name: frontend
          livenessProbe:
            httpGet:
              path: /
              port: 80
            initialDelaySeconds: 5
            periodSeconds: 10
            timeoutSeconds: 3
          readinessProbe:
            httpGet:
              path: /healthz
              port: 80
            periodSeconds: 5
            timeoutSeconds: 3
            failureThreshold: 3
            successThreshold: 1
          startupProbe:
            httpGet:
              path: /healthz
              port: 80
            periodSeconds: 5
            timeoutSeconds: 3
            failureThreshold: 36
            successThreshold: 1
```

Notes:

- Drop readiness `initialDelaySeconds` reliance for the FE→BE hop; `startupProbe` covers boot wait. Kubelet delays liveness/readiness until startup succeeds when `startupProbe` is present.
- FE `/healthz` is nginx → `pn-backend:8080/healthz` (process hop). Do **not** point FE readiness at `/readyz` (that would couple FE Ready to DB).

### Before → after (backend)

**Today (origin):**

| Role | Path | Port | Notes |
| --- | --- | --- | --- |
| liveness | `/healthz` | `8080` | process up — keep |
| readiness | `/healthz` | `8080` | **wrong role** — same as liveness |
| startup | *(not required)* | — | optional; out of scope |
| restartPolicy | *(undeclared)* | — | declare `Always` |

**Required after:**

| Role | Path | Port | Timing |
| --- | --- | --- | --- |
| readiness | `/readyz` | `8080` | `periodSeconds: 5`, `timeoutSeconds: 3`, `failureThreshold: 3` |
| liveness | `/healthz` | `8080` | `periodSeconds: 10`, `timeoutSeconds: 3` |
| restartPolicy | `Always` | pod template | under `spec.template.spec` |

Target YAML shape for `pn-backend`:

```yaml
spec:
  template:
    spec:
      restartPolicy: Always
      containers:
        - name: backend
          livenessProbe:
            httpGet:
              path: /healthz
              port: 8080
            initialDelaySeconds: 5
            periodSeconds: 10
            timeoutSeconds: 3
          readinessProbe:
            httpGet:
              path: /readyz
              port: 8080
            periodSeconds: 5
            timeoutSeconds: 3
            failureThreshold: 3
            successThreshold: 1
```

### Must not change (probe work)

| Item | Why |
| --- | --- |
| Image digests / tags | Status routes already exist (FR-011) |
| `PN_BACKEND_HOST` / `PN_BACKEND_PORT` | Already `pn-backend` / `8080` |
| NetworkPolicy FE→BE `:8080` | Already allows the hop (FR-010) |
| IngressRoute `/` and `/api` | Door unchanged (FR-012) |
| Notification sinks | Deferred; readiness NotReady is the signal only |

### Tuning knobs (if validation needs it)

| Goal | Knob | Default in this plan |
| --- | --- | --- |
| Longer FE boot wait for slow BE | FE `startupProbe.failureThreshold` (× `periodSeconds`) | `36 × 5s ≈ 180s` |
| Faster disconnect visibility | FE `readinessProbe.failureThreshold` | `3` (~15s) |
| Less flap noise | Raise FE readiness `failureThreshold` or `periodSeconds` | Prefer keeping path=`/healthz` |
| Never restart on soft disconnect | Keep FE/BE liveness off dependency paths | Mandatory |

Path roles stay fixed by Principle VII; timing may be tuned in the same manifests without changing door or policy.

## Complexity Tracking

| Violation | Why Needed | Simpler Alternative Rejected Because |
| --- | --- | --- |
| Constitution 2.1.0 (and 2.0.0 lineage) is unapproved for commit | Governance requires maintainer approval for the version bump that adds Principle VII | Implementing probes without Principle VII leaves FR-013 / platform standard undocumented; approving later still needed before treating 2.1.0 as binding |


[You have received this identical output 3 times. Re-reading '/home/nacfson/Projects/nacfson_pipeline/specs/007-pn-readiness-restart/plan.md:raw' will not change it — use a narrower selector (path:A-B), or proceed with the edit.]