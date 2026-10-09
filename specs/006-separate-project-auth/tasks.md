# Tasks: Separate Project Entry from Sign-In

**Input**: Design documents from `/specs/006-separate-project-auth/`

**Prerequisites**: plan.md, spec.md, research.md, data-model.md, contracts/, quickstart.md

**Tests**: The specification does not request a test-first approach. Tasks below change the door, the declaration, and the gateway. Existing preflight and gateway suites are updated only where the old wall would make them fail.

**Organization**: Tasks are grouped by user story so each story can be implemented and checked on its own.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies)
- **[Story]**: Which user story this task belongs to (US1, US2, US3)
- Setup, Foundational, and Polish tasks have no story label

## Path Conventions

Paths follow `specs/006-separate-project-auth/plan.md`: constitution and `SPEC.md` at the repo root, Project PN under `deploy/projects/pn/`, gateway under `gateway/`, preflight under `scripts/` and `tests/preflight/`.

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Add the sign-in choice to the project declaration

- [x] T001 Add optional `signIn` to `specs/001-project-platform/contracts/project-manifest-spec.yaml`. The field is not required. Allowed values are `shared` and `none`. Absent means `none`.

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Amend the constitution and put the shared door rules in place before any story changes a project

**⚠️ CRITICAL**: No user story work can begin until this phase is complete

- [x] T002 Amend `.specify/memory/constitution.md` to version 2.0.0 using the replacement text, impact, and Project PN migration in `specs/006-separate-project-auth/contracts/constitution-amendment.md`. Do not edit a project door in this task. Leave the major bump uncommitted if the maintainer has not approved it, and record that approval in the amendment notes.
- [x] T003 Align `SPEC.md` §3, AUTH-03, and AUTH-04 with the 2.0.0 rule that the project answers before sign-in and that a project may have no connection to sign-in. Depends on T002.
- [x] T004 [P] Clear `Authorization` as well as `X-User-Subject` and `X-User-Issuer` in the `strip-forged-headers` middleware in `deploy/platform/ingress/traefik-middleware.yaml`. Do not attach `forward-auth` to a project route in this task.
- [x] T005 [P] Teach `scripts/platform-preflight.py` the door rules in `specs/006-separate-project-auth/contracts/project-door.md`: reject `forward-auth` on a project route, reject a route port that the project's NetworkPolicy does not allow from the platform namespace, reject a Service outside the project namespace, and reject gateway egress when `signIn` is `none` or omitted. A rejected candidate must leave the last accepted door unchanged.

**Checkpoint**: Foundation ready. User stories can begin.

---

## Phase 3: User Story 1 - Open a project that does not use sign-in (Priority: P1) 🎯 MVP

**Goal**: A visitor reaches a project that does not use sign-in, including while the sign-in service is unavailable. A presented identity claim is not a signed-in user.

**Independent Test**: Stop the shared sign-in service. Open the project that does not use sign-in. The visitor receives the project and completes its public task.

### Implementation for User Story 1

- [x] T006 [US1] Add `tests/preflight/fixtures/sign_in_none_door.yaml` with `signIn` omitted, an IngressRoute that does not name `forward-auth`, door ports that the fixture NetworkPolicy allows from the platform namespace, and no egress to the gateway or the sign-in service. Register that fixture as accepted in `tests/preflight/test_platform_preflight.py`.
- [x] T007 [US1] Change `scripts/verify-revocation.sh` and `scripts/verify-platform.sh` so a project door without `forward-auth` passes. Keep the check that `/auth/logout` remains on `deploy/platform/ingress/ingress-allowlist.yaml`.

**Checkpoint**: User Story 1 is testable on its own. A `none` door is accepted and the old wall is not required.

---

## Phase 4: User Story 2 - Open a project, then sign in only if it asks (Priority: P1)

**Goal**: Project PN stays on its site and API roads. Sign-in runs only after PN asks, and the browser returns to the same PN address.

**Independent Test**: Open Project PN. Confirm the project appears before any sign-in request. Complete sign-in when PN asks. Confirm the visitor returns to the same project address.

### Implementation for User Story 2

- [x] T008 [P] [US2] Declare `signIn: shared` in `deploy/projects/pn/sign-in.yaml`. Do not put business permissions in that file.
- [x] T009 [P] [US2] Remove `forward-auth` from both routes in `deploy/projects/pn/workloads/ingress-route.yaml`. Keep `strip-forged-headers`. Set priority 10 on `Host(pn.example.com)` to `pn-frontend:80` and priority 100 on `Host(pn.example.com) && PathPrefix(/api)` to `pn-backend:8080`. Leave route order as host then `/api` so the match patches in `clusters/local-k3s/projects/proj-pn.yaml` and `clusters/vps-k3s/projects/proj-pn.yaml` stay valid.
- [x] T010 [P] [US2] Allow egress only to the gateway on port 8080 in `deploy/projects/pn/boundary/network-policy.yaml`. Keep the existing ingress allows for ports 80 and 8080 from the platform namespace, and the inside allow for port 8080.
- [x] T011 [P] [US2] Implement session return and confirmation in `gateway/internal/auth/handler.go` per `specs/006-separate-project-auth/contracts/session-return.md`, and align `gateway/tests/contract_test.go` with that contract. The cookie is HttpOnly and Secure and scoped to the project host. A return address off that project's door is rejected. An unreachable sign-in service does not confirm the user. Opening the project host does not require the cookie.

**Checkpoint**: User Stories 1 and 2 both work. PN answers before sign-in, and a `none` project still has no sign-in connection.

---

## Phase 5: User Story 3 - A project's door matches its own network (Priority: P2)

**Goal**: A published door uses only entries the project's internal network already allows. A bad candidate is rejected and the previous door stays.

**Independent Test**: Publish a door whose ports the internal network allows, and confirm those entries work. Publish a candidate that names a port the internal network does not allow, and confirm it is rejected.

### Implementation for User Story 3

- [x] T012 [P] [US3] Add `tests/preflight/fixtures/door_port_mismatch.yaml` whose door targets port 9090 while its NetworkPolicy allows only the project's existing entry ports from the platform namespace.
- [x] T013 [US3] Register `tests/preflight/fixtures/door_port_mismatch.yaml` in `tests/preflight/test_platform_preflight.py` as a rejection. Assert the previously accepted door is unchanged. Depends on T005 and T006.

**Checkpoint**: All three stories are independently functional.

---

## Phase 6: Polish & Cross-Cutting Concerns

**Purpose**: Point the old door contract at the new one, then run the validation guide

- [x] T014 [P] State at the top of `specs/001-project-platform/contracts/gateway-forwardauth.md` that a project door no longer calls ForwardAuth, and point readers to `specs/006-separate-project-auth/contracts/project-door.md`
- [x] T015 Run the checks in `specs/006-separate-project-auth/quickstart.md` and record the outcomes against SC-001 through SC-005

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies. Start immediately.
- **Foundational (Phase 2)**: Depends on Setup. Blocks every user story. T003 depends on T002. T004 and T005 may run with T002.
- **User Stories (Phase 3+)**: Depend on Foundational. US1 is the MVP. US2 may follow US1 or proceed beside it after Foundational, because it edits different files. US3 depends on T005 and on the US1 fixture registration in T006 because both touch `tests/preflight/test_platform_preflight.py`.
- **Polish (Phase 6)**: Depends on the stories being completed.

### User Story Dependencies

- **User Story 1 (P1)**: After Foundational. No dependency on US2 or US3.
- **User Story 2 (P1)**: After Foundational. Independently testable. Does not require a `none` project to be deployed.
- **User Story 3 (P2)**: After Foundational and T006. The mismatch fixture is its own story. It must not change the PN door.

### Parallel Opportunities

- T004 and T005 can run in parallel with T002.
- T008, T009, T010, and T011 touch different files and can run in parallel after Foundational.
- T012 can be written while T011 is in progress.
- T014 can run beside the last story edits.

### Parallel Example: User Story 2

```bash
# After Phase 2, these four files are independent:
Task: "Declare signIn shared in deploy/projects/pn/sign-in.yaml"
Task: "Remove forward-auth and set priorities in deploy/projects/pn/workloads/ingress-route.yaml"
Task: "Allow gateway egress on port 8080 in deploy/projects/pn/boundary/network-policy.yaml"
Task: "Implement session return in gateway/internal/auth/handler.go"
```

---

## Implementation Strategy

### MVP First (User Story 1 Only)

1. Complete Phase 1 and Phase 2.
2. Complete Phase 3 (T006, T007).
3. Stop and validate User Story 1: a `none` door is accepted, and the verify scripts do not demand `forward-auth`.
4. Do not start the PN door edit until this passes.

### Incremental Delivery

1. Setup and Foundational, including constitution 2.0.0 in the same revision as later door edits.
2. User Story 1, then validate the `none` path.
3. User Story 2, then validate that PN answers before sign-in and returns to the same address.
4. User Story 3, then validate that port 9090 is rejected and the previous door remains.
5. Run `specs/006-separate-project-auth/quickstart.md`.

### Parallel Team Strategy

1. Complete Setup and Foundational together. T003 waits for T002.
2. After that, one person can take User Story 1, another User Story 2.
3. User Story 3 starts after T006, because the preflight test file is shared.

---

## Notes

- Do not ship a Project PN door change in a revision that lacks constitution 2.0.0.
- Do not reorder the two PN routes. The Flux match patches address them by index.
- `signIn` omitted means `none`. Project PN is the one project that declares `shared`.
- A confirmed identity is `(issuer, subject)` for that project only. It does not grant business permissions.
