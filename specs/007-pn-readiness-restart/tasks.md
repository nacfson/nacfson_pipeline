# Tasks: Project PN Startup, Readiness Disconnect Signal, Restart Policy, and Browser API Origin Access

**Input**: Design documents from `/specs/007-pn-readiness-restart/`

**Prerequisites**: plan.md, spec.md, research.md, data-model.md, contracts/, quickstart.md

**Tests**: Validation is operator/cluster checks from `quickstart.md` plus static manifest inspection. No new automated test suite is required in this feature.

**Organization**: Tasks are grouped by user story so each story can be implemented and checked on its own.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies)
- **[Story]**: Which user story this task belongs to (US1, US2, US3, US4, US5)
- Setup, Foundational, and Polish tasks have no story label

## Path Conventions

Paths follow `specs/007-pn-readiness-restart/plan.md`: Project PN Deployments under `deploy/projects/pn/workloads/`, Flux patches under `clusters/*/projects/proj-pn.yaml`, constitution at `.specify/memory/constitution.md`.

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Confirm the design lock and working-tree governance draft before editing manifests

- [x] T001 Confirm Principle VII and Amendment 2.1.0 text in `.specify/memory/constitution.md` match the startup/readiness/liveness/always-restart rules in `specs/007-pn-readiness-restart/contracts/probe-matrix.md`. Record that 2.1.0 remains maintainer-approval-gated for commit.
- [x] T002 [P] Confirm `PN_BACKEND_HOST=pn-backend` and `PN_BACKEND_PORT=8080` remain set in `deploy/projects/pn/workloads/frontend-deployment.yaml` (required for FE `/healthz` proxy hop). Do not change image digests.
- [x] T001b Re-confirm Principle VII / Amendment 2.1.0 also document the **browser API origin allowlist** (`PUBLIC_APP_URL` ∈ `ALLOWED_ORIGINS`) per `contracts/browser-api-access.md` after the 007 revision.

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Freeze non-goals and isolation baselines so story edits cannot widen the door or NetworkPolicy

**⚠️ CRITICAL**: No user story work can begin until this phase is complete

- [x] T003 Capture a baseline that `deploy/projects/pn/boundary/network-policy.yaml` already allows FE→BE TCP `8080` and that this feature will leave that file unchanged (FR-010).
- [x] T004 [P] Capture a baseline that `deploy/projects/pn/workloads/ingress-route.yaml` keeps host→`pn-frontend:80` and `/api`→`pn-backend:8080` and that this feature will leave that file unchanged (FR-012).
- [x] T005 Align implementation targets with `specs/007-pn-readiness-restart/plan.md` section "How to change settings (explicit)" and "Probe failure effects": FE startup/readiness `/healthz`, FE liveness `/`, BE readiness `/readyz`, BE liveness `/healthz`, `restartPolicy: Always`, readiness never restarts, startup threshold exhaustion restarts.
- [x] T005b Align foundational targets with browser-path rules: BE `ALLOWED_ORIGINS` / `PUBLIC_APP_URL`; FE Ready ≠ CORS proof; validation via quickstart §6 (FR-014–FR-017).

**Checkpoint**: Foundation ready — user story manifest edits can begin

---

## Phase 3: User Story 1 - Startup waits for backend; exhausted startup restarts (Priority: P1) 🎯 MVP

**Goal**: Frontend declares `startupProbe` on `/healthz` with ≈180s failure window and `restartPolicy: Always`, so cold start waits for the first FE→BE process hop and restarts the container when the window is exhausted.

**Independent Test**: With backend scaled to 0, restart frontend; it stays Starting in-window, then restart count increases after ~180s. With backend up, startup succeeds without door changes (`quickstart.md` §4 / SC-001 / SC-001b).

### Implementation for User Story 1

- [x] T006 [US1] In `deploy/projects/pn/workloads/frontend-deployment.yaml`, set `spec.template.spec.restartPolicy: Always`.
- [x] T007 [US1] In `deploy/projects/pn/workloads/frontend-deployment.yaml`, add `startupProbe` on container `frontend`: `httpGet.path: /healthz`, `port: 80`, `periodSeconds: 5`, `timeoutSeconds: 3`, `failureThreshold: 36`, `successThreshold: 1` per `specs/007-pn-readiness-restart/contracts/probe-matrix.md` and plan FR-001/FR-001a/FR-001b mapping.
- [x] T008 [US1] Verify in `deploy/projects/pn/workloads/frontend-deployment.yaml` that FE `livenessProbe` remains `httpGet.path: /` on port `80` (process-local) and is not pointed at `/healthz` or `/readyz`.

**Checkpoint**: US1 MVP — FE startup gate + startup-failure restart architecture is declared in Git

---

## Phase 4: User Story 2 - Readiness is the FE↔BE disconnect signal (Priority: P1)

**Goal**: Frontend `readinessProbe` continuously checks `/healthz` so NotReady means cluster-unconnected from backend (future notification hook), without restarting the process.

**Independent Test**: After FE has been Ready once, scale backend to 0; FE becomes NotReady, Endpoints empty, restart count does not climb from readiness alone; restore backend → Ready under 60s (`quickstart.md` §2 / SC-002 / SC-004 / SC-007).

### Implementation for User Story 2

- [x] T009 [US2] In `deploy/projects/pn/workloads/frontend-deployment.yaml`, change `readinessProbe` from `path: /` to `httpGet.path: /healthz`, `port: 80`, with `periodSeconds: 5`, `timeoutSeconds: 3`, `failureThreshold: 3`, `successThreshold: 1`. Remove reliance on readiness `initialDelaySeconds` for the FE→BE hop (startup covers boot wait).
- [x] T010 [US2] Confirm `specs/007-pn-readiness-restart/contracts/disconnect-signal.md` still matches the FE readiness behavior (NotReady = cluster disconnect; no push sinks) and that no notification component was added under `deploy/`.
- [x] T010b [US2] Confirm disconnect-signal contract explicitly states FE Ready ≠ browser CORS success and points at `browser-api-access.md`.

**Checkpoint**: US1+US2 — FE startup and continuous cluster disconnect signal both live on the frontend Deployment

---

## Phase 5: User Story 3 - Backend readiness reflects data dependency (Priority: P1)

**Goal**: Backend readiness uses `/readyz` (DB/dependency), not `/healthz`, so API traffic is withheld when the data path is down while the process can still be alive.

**Independent Test**: With process up and data path broken, BE `/healthz` 200, `/readyz` 503, pod NotReady, Endpoints empty, no readiness-driven restart storm (`quickstart.md` §3 / SC-003).

### Implementation for User Story 3

- [x] T011 [P] [US3] In `deploy/projects/pn/workloads/backend-deployment.yaml`, set `spec.template.spec.restartPolicy: Always`.
- [x] T012 [US3] In `deploy/projects/pn/workloads/backend-deployment.yaml`, change `readinessProbe` from `path: /healthz` to `httpGet.path: /readyz`, `port: 8080`, with `periodSeconds: 5`, `timeoutSeconds: 3`, `failureThreshold: 3`, `successThreshold: 1`.
- [x] T013 [US3] In `deploy/projects/pn/workloads/backend-deployment.yaml`, keep `livenessProbe` on `httpGet.path: /healthz`, `port: 8080` (process-only; must not equal readiness path after this task).

**Checkpoint**: US3 independently delivers correct BE ready vs alive split

---

## Phase 6: User Story 4 - Alive checks + restart policy decoupled from soft disconnects (Priority: P1)

**Goal**: Prove and lock that readiness failures do not restart processes; only liveness failure and startup-window exhaustion restart under explicit `Always`.

**Independent Test**: Post-startup FE readiness failure leaves restart count unchanged while liveness `/` still passes; both Deployments declare `restartPolicy: Always` (SC-005 / SC-006).

### Implementation for User Story 4

- [x] T014 [P] [US4] Re-read `deploy/projects/pn/workloads/frontend-deployment.yaml` and assert the failure-effect matrix: `startupProbe` threshold restart allowed; `readinessProbe` `/healthz` must not be used as liveness; `livenessProbe` remains `/`; `restartPolicy: Always` present.
- [x] T015 [P] [US4] Re-read `deploy/projects/pn/workloads/backend-deployment.yaml` and assert: `readinessProbe` `/readyz`, `livenessProbe` `/healthz`, `restartPolicy: Always`, and readiness path ≠ liveness path.
- [x] T016 [US4] Diff-check that `deploy/projects/pn/boundary/network-policy.yaml` and `deploy/projects/pn/workloads/ingress-route.yaml` have no feature-driven edits (FR-010 / FR-012).

**Checkpoint**: All probe/restart invariants hold in manifests

---

## Phase 7: User Story 5 - Browser API origin allowlist (Priority: P1)

**Goal**: Backend `PUBLIC_APP_URL` and `ALLOWED_ORIGINS` include the environment public app origin so SPA CORS preflight succeeds; docs and validation treat this as required alongside cluster probes.

**Independent Test**: `quickstart.md` §6 OPTIONS with `Origin: $PUBLIC_APP_URL` returns ACAO; static §0 shows env on base + Flux patches (SC-008 / SC-009 / SC-010).

### Implementation for User Story 5

- [x] T021 [US5] In `deploy/projects/pn/workloads/backend-deployment.yaml`, declare `ALLOWED_ORIGINS` (localhost/Tauri defaults plus known public hosts) and `PUBLIC_APP_URL` per `contracts/browser-api-access.md` / plan.
- [x] T022 [P] [US5] In `clusters/vps-k3s/projects/proj-pn.yaml`, patch BE env so `PUBLIC_APP_URL=https://pn.nacfson.cloud` and `ALLOWED_ORIGINS` includes that origin.
- [x] T023 [P] [US5] In `clusters/local-k3s/projects/proj-pn.yaml`, patch BE env so `PUBLIC_APP_URL=https://pn.example.com` and `ALLOWED_ORIGINS` includes that origin.
- [x] T024 [US5] Ensure `contracts/browser-api-access.md`, revised `spec.md` / `plan.md` / `quickstart.md` §6, and Principle VII text all state FE Ready ≠ browser API success (FR-017 / SC-010).
- [x] T025 [US5] Run quickstart §0 CORS-related `rg` checks and, when the public door is reachable, §6 preflight ACAO assertion; record results in `validation-note.md`.

**Checkpoint**: Browser-path allowlist declared and validated independently of kubelet Ready

---

## Phase 8: Polish & Cross-Cutting Concerns

**Purpose**: Static contract checks and quickstart validation evidence

- [x] T017 [P] Run static `rg` checks from `specs/007-pn-readiness-restart/quickstart.md` §0 against probe/restart fields on frontend/backend Deployments.
- [x] T018 [P] Confirm no notification sink/CronJob/Alertmanager manifests were added for this feature (FR-007) under `deploy/projects/pn/` or `deploy/platform/`.
- [x] T019 Execute applicable cluster scenarios in `specs/007-pn-readiness-restart/quickstart.md` (§1–§4 when a cluster is available) and record outcomes against SC-001 through SC-007. If the cluster is unavailable, record that limitation explicitly and keep §0 static results.
- [x] T020 Note in the PR/commit message that `.specify/memory/constitution.md` 2.1.0 (and 2.0.0 lineage) still requires maintainer approval before treating the constitution bump as committed governance.
- [x] T026 [P] Extend polish evidence for SC-008–SC-010 (allowlist static + CORS §6 + FE Ready warning present in contracts).
- [x] T027 Update `validation-note.md` for the revised 007 scope (probes + browser API access).

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies — start immediately
- **Foundational (Phase 2)**: Depends on Setup — BLOCKS all user stories
- **User Story 1 (Phase 3)**: After Foundational — MVP cluster hop
- **User Story 2 (Phase 4)**: After US1 (same file `frontend-deployment.yaml`)
- **User Story 3 (Phase 5)**: After Foundational — can run in parallel with US1/US2 (different file)
- **User Story 4 (Phase 6)**: After US1–US3 manifest edits exist (verification story)
- **User Story 5 (Phase 7)**: After Foundational — can run in parallel with US1–US4 (backend env + docs); should land before calling 007 “complete”
- **Polish (Phase 8)**: After desired stories complete

### User Story Dependencies

- **US1 (P1)**: No dependency on US2–US5
- **US2 (P1)**: Depends on US1 only because both edit `frontend-deployment.yaml`
- **US3 (P1)**: Independent of US1/US2 (backend probes)
- **US4 (P1)**: Depends on US1–US3 declarations being present to verify decoupling
- **US5 (P1)**: Independent of FE probe stories; shares `backend-deployment.yaml` with US3 env section (coordinate merges)

### Parallel Opportunities

- After Foundational: **T011 [US3]** can run in parallel with **T006–T008 [US1]**
- **T021–T023 [US5]** can run in parallel with FE probe work once backend file ownership is clear
- T022 and T023 are [P]
- T017/T018 and T026 are [P]

---

## Implementation Strategy

### MVP First (User Story 1 Only)

1. Complete Phase 1 Setup
2. Complete Phase 2 Foundational
3. Complete Phase 3 US1 (`startupProbe` + `restartPolicy: Always` on FE)
4. **STOP and VALIDATE** with `quickstart.md` §4 cold-start behavior
5. Proceed to US2 for the continuous disconnect signal
6. **Do not call the feature complete** until US5 (browser allowlist) lands—otherwise production SPA connect errors can remain with green probes

### Incremental Delivery

1. Setup + Foundational → baselines locked
2. US1 → FE boot gate + startup-failure restart
3. US2 → FE readiness cluster disconnect signal
4. US3 → BE `/readyz` readiness
5. US4 → cross-check no readiness restarts / door-NP untouched
6. US5 → `ALLOWED_ORIGINS` / `PUBLIC_APP_URL` + CORS validation
7. Polish → static + cluster + CORS evidence

---

## Notes

- [P] = different files, no incomplete-task dependency
- Do not implement push notifications in any task
- Do not retarget FE readiness to `/readyz` (DB coupling — rejected in research.md)
- Do not claim kubelet Ready proves CORS (R10)
- Constitution commit remains approval-gated even if manifests ship
- Suggested minimum “user-visible access works” scope: **US1+US2+US5** (cluster hop + browser origin)
