# Feature Specification: Project PN Startup, Readiness Disconnect Signal, and Restart Policy

**Feature Branch**: `007-pn-readiness-restart`

**Created**: 2026-10-12

**Status**: Draft

**Input**: User description: "Add startupProbe and readinessProbe so Project PN's frontend can access the backend explicitly. Use readinessProbe as the explicit unconnected-from-backend signal for future notifications. Also define restartPolicy. Update constitution with this architecture."

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Startup waits until frontend can reach backend; exhausted startup restarts the pod (Priority: P1)

As a platform operator, I want Project PN's frontend to finish starting only after it has successfully reached the backend process-availability check once, and I want a **failed startup window** to **restart that pod** under always-restart, so a cold start neither advertises a never-connected site nor leaves a permanently stuck starting container.

**Why this priority**: Boot ordering is the first failure mode when frontend and backend come up together; startup is the dedicated gate for that first successful hop, and startup-exhaustion restart is the fail-closed recovery path.

**Independent Test**: Start frontend while backend is down; startup must not succeed within the window, then the frontend pod/container must restart. Bring backend up on a subsequent attempt; startup succeeds without changing door routing.

**Acceptance Scenarios**:

1. **Given** the frontend process is up and the backend process-availability check is reachable from the frontend, **When** startup is evaluated, **Then** startup succeeds and the frontend may proceed to readiness/liveness evaluation.
2. **Given** the frontend process is up but the backend process-availability check is not reachable, **When** startup is evaluated within the allowed startup window, **Then** startup has not yet succeeded, the frontend is not treated as fully started, and the container is **not** yet restarted solely for those in-window failures.
3. **Given** the startup check fails repeatedly until the allowed startup window is exhausted (failure threshold reached), **When** the platform applies startup failure policy, **Then** the frontend container is **killed and restarted** according to the declared always-restart policy, and this restart is classified as a **startup failure**, not a readiness disconnect notification event.
4. **Given** a frontend pod was restarted because startup failed, **When** the backend process-availability check becomes reachable on a later start attempt within a new startup window, **Then** startup succeeds without manual pod deletion or door changes.

---

### User Story 2 - Readiness is the explicit frontend↔backend disconnect signal (Priority: P1)

As a platform operator, I want Project PN frontend readiness to mean “currently connected to the backend process-availability check,” so an unconnected state is visible as not-ready and can later drive notifications without a new probe invention.

**Why this priority**: The user requires readiness (not only startup) as the continuous, explicit unconnected signal reserved for future notification work.

**Independent Test**: After the frontend has started successfully, make the backend unreachable from the frontend; frontend must become not-ready. Restore reachability; frontend becomes ready again. No push notification system is required in this feature—only the durable not-ready signal.

**Acceptance Scenarios**:

1. **Given** the frontend has completed startup and the backend process-availability check succeeds from the frontend, **When** readiness is evaluated, **Then** the frontend is ready.
2. **Given** the frontend has completed startup and the backend process-availability check fails from the frontend, **When** readiness is evaluated, **Then** the frontend is not ready, new site traffic is withheld from that instance, and the not-ready condition is observable as the disconnect signal.
3. **Given** the frontend was not ready because of backend unreachability, **When** the backend process-availability check succeeds again, **Then** the frontend becomes ready without manual intervention and without requiring a notification system to exist yet.
4. **Given** a future notification feature consumes workload ready-state changes, **When** frontend readiness fails for backend unreachability, **Then** that same readiness signal is sufficient input (no second parallel FE→BE probe type is required by this feature).

---

### User Story 3 - Backend readiness reflects usable dependencies (Priority: P1)

As a platform operator, I want Project PN's backend ready signal to mean the backend can serve work that depends on its data store, not merely that the process is alive, so API traffic is withheld until the backend is actually usable.

**Why this priority**: Backend "ready" currently mirrors the alive check. The API path needs a stronger ready meaning, independent of the frontend disconnect signal.

**Independent Test**: Stop or block the backend's data dependency while leaving the backend process running; backend must leave ready state. Restore the dependency; backend returns to ready.

**Acceptance Scenarios**:

1. **Given** the backend process is up and its required data dependency is reachable, **When** readiness is evaluated, **Then** the backend is ready.
2. **Given** the backend process is up but its required data dependency is not reachable, **When** readiness is evaluated, **Then** the backend is not ready and does not receive new API traffic through its service.
3. **Given** the backend process is up, **When** only the alive check is considered, **Then** process aliveness alone MUST NOT be treated as readiness.

---

### User Story 4 - Alive checks and restart policy stay decoupled from soft disconnects (Priority: P1)

As a platform operator, I want process-alive checks and restart policy to stay independent of temporary frontend↔backend or data-store disconnects after startup, so readiness can flip for notification/traffic purposes without restart storms.

**Why this priority**: Readiness is the disconnect signal; using alive checks for the same dependency would restart containers and hide the outage.

**Independent Test**: After successful startup, induce a backend reachability failure that fails frontend readiness; confirm the frontend process is not restarted solely for that reason while its alive check still passes.

**Acceptance Scenarios**:

1. **Given** frontend and backend processes are healthy after startup, **When** a dependency needed only for readiness fails, **Then** workloads leave ready state and are not restarted solely because of that readiness failure.
2. **Given** a frontend or backend process is truly dead or non-responsive on its alive check, **When** the alive check fails per policy, **Then** the platform restarts that container according to the declared always-restart policy.
3. **Given** Project PN frontend and backend workload declarations, **When** an operator reviews restart behavior, **Then** both explicitly declare that containers always restart after failure or exit.

---

### Edge Cases

- Backend briefly flaps after startup: frontend readiness may toggle (that toggle is the intended disconnect signal); the process keeps running; alive checks must continue to pass; **readiness MUST NOT restart** the process.
- Startup failures inside the window: container stays up and keeps retrying startup; no restart yet.
- Startup window expires while backend never becomes reachable: frontend container/pod is **restarted** under always-restart; this is a **startup-failure restart architecture**, not a readiness disconnect notification event.
- After startup has succeeded once, backend becomes unreachable: readiness goes NotReady only; **no restart** from that readiness failure.
- Backend process is alive but data dependency is down: backend not ready; frontend readiness still follows frontend→backend process-availability and may remain ready.
- Both frontend and backend are single-instance with no surge: when frontend is not ready, the site service path has no healthy endpoint until readiness recovers.
- Future notification delivery (chat, email, ticket) is out of scope; only the ready-state disconnect signal is in scope.
- Network isolation still blocks unauthorized peers: startup and readiness checks MUST use the existing frontend-to-backend allowlist.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: Project PN frontend MUST declare a startup check that succeeds only after a frontend-originated Project PN backend process-availability check succeeds at least once.
- **FR-001a**: While the frontend startup window is still open, repeated startup-check failures MUST keep the container in starting state without treating those in-window failures as readiness disconnect notifications.
- **FR-001b**: When the frontend startup check fails until the configured startup failure threshold / window is exhausted, the platform MUST **kill and restart** that frontend container under the declared always-restart policy (startup-failure restart architecture).
- **FR-002**: Project PN frontend MUST declare a readiness check that continuously requires a successful frontend-originated Project PN backend process-availability check, producing an explicit not-ready disconnect signal when that hop fails after startup.
- **FR-003**: Project PN frontend alive checking MUST verify only that the frontend itself can serve its site content, and MUST NOT require backend reachability.
- **FR-004**: Project PN backend readiness MUST require a successful backend dependency-readiness signal (data store reachable), not merely process aliveness.
- **FR-005**: Project PN backend alive checking MUST verify only that the backend process is up, and MUST NOT fail solely because the data store is unreachable.
- **FR-006**: Frontend readiness not-ready caused by backend unreachability MUST withhold new traffic from that frontend instance and MUST remain the durable operator-visible disconnect signal reserved for future notification integration.
- **FR-007**: This feature MUST NOT implement push notification delivery; it MUST only establish the readiness disconnect signal those notifications will consume later.
- **FR-008**: Readiness failures for Project PN frontend or backend MUST NOT by themselves trigger container or pod restarts, before or after startup has succeeded; readiness affects traffic admission and the disconnect signal only, not process restart.
- **FR-009**: Project PN frontend and backend workload declarations MUST explicitly set container restart policy to always restart.
- **FR-010**: Startup and readiness frontend-to-backend checks MUST use the existing allowed project-internal path to the backend and MUST NOT relax isolation rules.
- **FR-011**: This feature MUST NOT require changes to Project PN application images solely to expose status checks already published by those images.
- **FR-012**: Public project door routing (site vs API addresses) MUST remain unchanged by this feature.
- **FR-013**: Platform governance MUST record the startup-versus-readiness-versus-alive probe architecture so later projects follow the same disconnect-signal pattern.

### Key Entities

- **Frontend startup signal**: One-time (per container start) proof that the frontend reached the backend process-availability check before the frontend is considered started.
- **Startup-failure restart**: Lifecycle outcome when the startup window is exhausted without success; the container/pod is restarted under always-restart so a new startup attempt can begin.
- **Frontend readiness / disconnect signal**: Continuous judgment that the frontend currently reaches the backend process-availability check; not-ready means explicitly unconnected and is the hook for future notifications; **does not restart** the process.
- **Backend readiness signal**: Judgment that the Project PN API workload may receive traffic; depends on backend data-store availability; **does not restart** the process by itself.
- **Alive signal**: Judgment that a container process should keep running without restart; independent of peer or data-store readiness after startup; failure restarts the process.
- **Restart policy**: Declared rule that Project PN app containers always restart after exit, failed alive check, or exhausted startup failure; not after readiness-only failure.
- **Future notification consumer**: Out-of-scope component that will watch readiness disconnect signals; not built in this feature.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: In 100% of cold-start tests where the backend process-availability check remains unreachable from the frontend for the full startup window, the frontend container is restarted after startup failure threshold exhaustion, and restart count increases for that startup-failure path.
- **SC-001b**: In 100% of cold-start tests where the backend process-availability check becomes reachable before the startup window ends, frontend startup succeeds without a startup-failure restart.
- **SC-002**: In 100% of post-startup tests where the backend process-availability check becomes unreachable from the frontend, the frontend becomes not ready within one readiness evaluation interval and remains not restarted solely for that readiness condition while alive checks pass.
- **SC-003**: In 100% of test runs where the backend data dependency is down while the backend process remains up, the backend is not ready for traffic and is not restarted solely for that condition.
- **SC-004**: After backend process availability is restored following a post-startup disconnect, the frontend returns to ready without operator action in under 1 minute.
- **SC-005**: 100% of Project PN frontend and backend workload declarations under this feature explicitly state always-restart behavior.
- **SC-006**: A post-startup dependency-only outage produces zero restart storms attributable to readiness coupling (alive checks continue to pass while readiness fails).
- **SC-007**: An operator can identify frontend↔backend unconnected state solely from the frontend not-ready / disconnect signal without deploying a notification system yet.

## Assumptions

- "Frontend can access the backend explicitly" uses the backend **process-availability** check from the frontend for both startup and readiness, not the backend data-store readiness check.
- Readiness not-ready is intentionally used as the continuous disconnect indicator even when that briefly removes a single frontend instance from traffic; that tradeoff is accepted to keep the notification hook explicit.
- Push/email/chat notification delivery, sinks, and paging policies are explicitly deferred.
- Project PN images already expose distinct process-availability and dependency-readiness status checks; the platform only aims startup/readiness/alive checks at the correct ones.
- Long-running Project PN frontend and backend are always-on app workloads; always-restart is the correct explicit policy (one-shot batch jobs are out of scope).
- Existing project network allowlisting already permits frontend-to-backend status checks; no isolation redesign is in scope.
- Replica count and rollout surge settings stay as they are today.
- Public exposure of status URLs through the project door is unchanged and out of scope.
- Constitution amendment for this probe architecture is part of the same change set as this feature's acceptance.
