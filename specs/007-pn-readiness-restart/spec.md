# Feature Specification: Project PN Startup, Readiness Disconnect Signal, Restart Policy, and Browser API Origin Access

**Feature Branch**: `007-pn-readiness-restart`

**Created**: 2026-10-12

**Updated**: 2026-10-12

**Status**: Draft

**Input**: User description: "Add startupProbe and readinessProbe so Project PN's frontend can access the backend explicitly. Use readinessProbe as the explicit unconnected-from-backend signal for future notifications. Also define restartPolicy. Update constitution with this architecture." **Revision**: After production diagnosis, "access the backend explicitly" MUST also cover the **browser-shaped `/api` path** (CORS origin allowlist), because FE→BE `/healthz` probes alone do not prevent the SPA "Could not connect to the backend server" failure when `ALLOWED_ORIGINS` omits the public app origin.

## Clarifying model *(mandatory)*

Two different access paths exist. Both are in scope for Project PN "frontend can access backend":

| Path | Who calls | What success means | Proved by |
| --- | --- | --- | --- |
| **Cluster hop** | kubelet → FE nginx → BE process | FE process can reach BE process-availability | FE `startupProbe` on `/api/auth/language-options` (amended 2026-10-13), `readinessProbe` on `/healthz` |
| **Browser API path** | Browser JS → public door `/api` → BE | SPA can complete CORS-safe fetches to the API on the public origin | Backend `ALLOWED_ORIGINS` includes `PUBLIC_APP_URL` / public door origin; validated by Origin + preflight checks |

A green cluster hop **MUST NOT** be treated as proof that the browser API path works. Proven failure mode: FE Ready + BE `/api/auth/me` answering 401, while browser `fetch` with `Cache-Control`/`Pragma` triggers CORS preflight that fails because `https://pn.nacfson.cloud` was not allowlisted → SPA shows connection error (non-401).

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Startup waits until frontend can reach backend; exhausted startup restarts the pod (Priority: P1)

As a platform operator, I want Project PN's frontend to finish starting only after it has successfully reached the backend process-availability check once, and I want a **failed startup window** to **restart that pod** under always-restart, so a cold start neither advertises a never-connected site nor leaves a permanently stuck starting container.

**Why this priority**: Boot ordering is the first failure mode when frontend and backend come up together; startup is the dedicated gate for that first successful **cluster** hop, and startup-exhaustion restart is the fail-closed recovery path.

**Independent Test**: Start frontend while backend is down; startup must not succeed within the window, then the frontend pod/container must restart. Bring backend up on a subsequent attempt; startup succeeds without changing door routing.

**Acceptance Scenarios**:

1. **Given** the frontend process is up and the backend process-availability check is reachable from the frontend, **When** startup is evaluated, **Then** startup succeeds and the frontend may proceed to readiness/liveness evaluation.
2. **Given** the frontend process is up but the backend process-availability check is not reachable, **When** startup is evaluated within the allowed startup window, **Then** startup has not yet succeeded, the frontend is not treated as fully started, and the container is **not** yet restarted solely for those in-window failures.
3. **Given** the startup check fails repeatedly until the allowed startup window is exhausted (failure threshold reached), **When** the platform applies startup failure policy, **Then** the frontend container is **killed and restarted** according to the declared always-restart policy, and this restart is classified as a **startup failure**, not a readiness disconnect notification event.
4. **Given** a frontend pod was restarted because startup failed, **When** the backend process-availability check becomes reachable on a later start attempt within a new startup window, **Then** startup succeeds without manual pod deletion or door changes.

---

### User Story 2 - Readiness is the explicit frontend↔backend disconnect signal (Priority: P1)

As a platform operator, I want Project PN frontend readiness to mean “currently connected to the backend process-availability check **on the cluster hop**,” so an unconnected state is visible as not-ready and can later drive notifications without a new probe invention.

**Why this priority**: The user requires readiness (not only startup) as the continuous, explicit unconnected signal reserved for future notification work.

**Independent Test**: After the frontend has started successfully, make the backend unreachable from the frontend; frontend must become not-ready. Restore reachability; frontend becomes ready again. No push notification system is required in this feature—only the durable not-ready signal.

**Acceptance Scenarios**:

1. **Given** the frontend has completed startup and the backend process-availability check succeeds from the frontend, **When** readiness is evaluated, **Then** the frontend is ready.
2. **Given** the frontend has completed startup and the backend process-availability check fails from the frontend, **When** readiness is evaluated, **Then** the frontend is not ready, new site traffic is withheld from that instance, and the not-ready condition is observable as the disconnect signal.
3. **Given** the frontend was not ready because of backend unreachability, **When** the backend process-availability check succeeds again, **Then** the frontend becomes ready without manual intervention and without requiring a notification system to exist yet.
4. **Given** a future notification feature consumes workload ready-state changes, **When** frontend readiness fails for backend unreachability, **Then** that same readiness signal is sufficient input (no second parallel FE→BE **cluster-hop** probe type is required by this feature).
5. **Given** frontend readiness is Ready, **When** an operator interprets that state, **Then** they MUST NOT conclude that the browser API / CORS path is proven (see User Story 5).

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

### User Story 5 - Browser API origin allowlist matches the public app door (Priority: P1)

As a platform operator (and as an end user of the Project PN SPA), I want the backend CORS allowlist to include the project's public app origin, so browser `fetch` calls to `/api` that trigger CORS preflight (for example via non-safelisted headers such as `Cache-Control` / `Pragma`) succeed at the CORS layer whenever the API is otherwise reachable—rather than showing “Could not connect to the backend server” while cluster probes are green.

**Why this priority**: Production diagnosis showed the cluster hop succeeding while the SPA failed with a network-class error because `ALLOWED_ORIGINS` defaulted to localhost/Tauri only. Without this story, “frontend can access backend explicitly” remains incomplete for the real user path.

**Independent Test**: With pods Ready, send `OPTIONS` (and a GET) to the public `/api` door with `Origin: <PUBLIC_APP_URL>` and CORS preflight request headers; responses MUST include `Access-Control-Allow-Origin` reflecting that origin. Removing the origin from the allowlist MUST make preflight fail the ACAO assertion even if FE `/healthz` still passes.

**Acceptance Scenarios**:

1. **Given** Project PN backend workload env for an environment, **When** an operator inspects declarations, **Then** `PUBLIC_APP_URL` is set to that environment's public app origin and `ALLOWED_ORIGINS` is an explicit allowlist that **includes** that origin (plus any approved local/dev origins).
2. **Given** `Origin` equals `PUBLIC_APP_URL`, **When** a CORS preflight `OPTIONS` is issued against a public `/api` route with `Access-Control-Request-Headers` including `cache-control` and `pragma`, **Then** the response includes `Access-Control-Allow-Origin` for that origin (and credentials allowlisting behavior remains consistent with the app's cookie/auth mode).
3. **Given** FE `startupProbe` (`/api/auth/language-options`) and `readinessProbe` (`/healthz`) succeed, **When** `PUBLIC_APP_URL` is missing from `ALLOWED_ORIGINS`, **Then** the configuration is **non-compliant** with this feature even if pods are Ready—the cluster hop MUST NOT be accepted as sufficient proof of browser API access.
4. **Given** environment-specific Flux patches (VPS vs local), **When** the public door host differs, **Then** each environment's patch sets `PUBLIC_APP_URL` / `ALLOWED_ORIGINS` for that host without changing IngressRoute path splitting rules.

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
- Browser extensions, client DNS failures, or wrong SPA API base URL outside the declared public origin are **out of scope** for Ready semantics; this feature only guarantees the declared public origin is CORS-allowlisted.
- Same-origin browser requests that still send `Origin` and non-safelisted headers can still require ACAO; allowlisting the public origin remains mandatory.
- kubelet `httpGet` probes cannot assert CORS response headers; browser-path compliance is enforced by **declared config + validation**, not by retargeting readiness at a header-asserting probe in this feature.

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
- **FR-011**: This feature MUST NOT require changes to Project PN application **images** solely to expose status checks already published by those images. Declaring backend env (`ALLOWED_ORIGINS`, `PUBLIC_APP_URL`) and probe fields in Git is required and is not an image rebuild.
- **FR-012**: Public project door routing (site vs API addresses) MUST remain unchanged by this feature.
- **FR-013**: Platform governance MUST record the startup-versus-readiness-versus-alive probe architecture **and** the browser API origin-allowlist contract so later projects follow the same pattern.
- **FR-014**: Project PN backend MUST declare `PUBLIC_APP_URL` to the environment's public app origin and MUST declare `ALLOWED_ORIGINS` such that the public app origin is included.
- **FR-015**: Environment overlays/patches (VPS, local) MUST set `PUBLIC_APP_URL` / `ALLOWED_ORIGINS` for that environment's public door host.
- **FR-016**: Feature validation MUST include a browser-shaped CORS check (Origin = public app URL; preflight headers including cache-control/pragma) proving `Access-Control-Allow-Origin` for that origin on the public `/api` path—or an equivalent in-cluster check against the backend with the same Origin.
- **FR-017**: Documentation and operator guidance MUST state that FE Ready (cluster hop) is necessary but **not sufficient** for browser API access success.

### Key Entities

- **Frontend startup signal**: One-time (per container start) proof that the frontend reached the backend process-availability check before the frontend is considered started (**cluster hop**).
- **Startup-failure restart**: Lifecycle outcome when the startup window is exhausted without success; the container/pod is restarted under always-restart so a new startup attempt can begin.
- **Frontend readiness / disconnect signal**: Continuous judgment that the frontend currently reaches the backend process-availability check on the **cluster hop**; not-ready means explicitly unconnected and is the hook for future notifications; **does not restart** the process; **does not prove** browser CORS success.
- **Backend readiness signal**: Judgment that the Project PN API workload may receive traffic; depends on backend data-store availability; **does not restart** the process by itself.
- **Alive signal**: Judgment that a container process should keep running without restart; independent of peer or data-store readiness after startup; failure restarts the process.
- **Restart policy**: Declared rule that Project PN app containers always restart after exit, failed alive check, or exhausted startup failure; not after readiness-only failure.
- **Browser API origin allowlist**: Backend CORS configuration (`ALLOWED_ORIGINS`) that MUST include `PUBLIC_APP_URL` for the environment so SPA `/api` fetches that trigger preflight are not blocked while the API is up.
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
- **SC-007**: An operator can identify frontend↔backend **cluster** unconnected state solely from the frontend not-ready / disconnect signal without deploying a notification system yet.
- **SC-008**: 100% of environment backend declarations under this feature include the environment public app origin in `ALLOWED_ORIGINS` and set `PUBLIC_APP_URL` to that origin.
- **SC-009**: In 100% of CORS validation runs against the public `/api` door (or equivalent), `OPTIONS` with `Origin: <PUBLIC_APP_URL>` and cache-control/pragma request headers returns `Access-Control-Allow-Origin` for that origin.
- **SC-010**: Feature docs explicitly warn that FE Ready ≠ browser API success; a reviewer can find that warning without reading chat history.

## Assumptions

- "Frontend can access the backend explicitly" means **both** the cluster process-availability hop **and** the browser API origin-allowlist contract for the public SPA door.
- Cluster-hop readiness still uses backend **process-availability** (`/healthz`), not the backend data-store readiness check (`/readyz`).
- Readiness not-ready is intentionally used as the continuous **cluster** disconnect indicator even when that briefly removes a single frontend instance from traffic; that tradeoff is accepted to keep the notification hook explicit.
- Push/email/chat notification delivery, sinks, and paging policies are explicitly deferred.
- Project PN images already expose distinct process-availability and dependency-readiness status checks; the platform only aims startup/readiness/alive checks at the correct ones.
- Long-running Project PN frontend and backend are always-on app workloads; always-restart is the correct explicit policy (one-shot batch jobs are out of scope).
- Existing project network allowlisting already permits frontend-to-backend status checks; no isolation redesign is in scope.
- Replica count and rollout surge settings stay as they are today.
- Public exposure of status URLs through the project door is unchanged and out of scope.
- Constitution amendment for this probe **and** browser-origin architecture is part of the same change set as this feature's acceptance.
- Hosted web SPA resolves API base URL to `window.location.origin` when non-local; therefore the public door origin is the CORS origin that MUST be allowlisted.
- kubelet probes remain unable to assert CORS headers; config + synthetic validation cover that gap without an image rebuild in this feature.
