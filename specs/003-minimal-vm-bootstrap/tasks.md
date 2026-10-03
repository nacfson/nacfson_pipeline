# Tasks: Minimal VM Bootstrap

**Input**: Design documents in `specs/003-minimal-vm-bootstrap/`.
**Prerequisites**: [plan.md](plan.md), [spec.md](spec.md), [research.md](research.md), [data-model.md](data-model.md), [operator contract](contracts/bootstrap.md), and [quickstart.md](quickstart.md).

**Organization**: Setup, shared safety prerequisites, then the three P1 user stories in specification order. Paths below are repository-relative.

**Tests**: The specification explicitly requires fresh-host, failure, and rerun acceptance exercises. Their test definitions already exist in `quickstart.md`; use them before changing behavior and run them after implementation. Add no new test framework or general host-management tooling. Record actual results in `specs/003-minimal-vm-bootstrap/validation.md`; unexecuted VM tests remain unexecuted and their tasks remain unchecked.

## Format: `[ID] [P?] [Story] Description`

- `[P]` identifies tasks that can proceed together only after their documented common prerequisites, on separate files and without a shared VM mutation.
- `[US1]`, `[US2]`, and `[US3]` identify specification stories.
- All work is initially unchecked. Completing a documentation or syntax check does not complete live acceptance.

## Phase 1: Setup

**Purpose**: Establish the minimal controller dependency and playbook entry point.

- [x] T001 [P] Create `bootstrap/requirements.txt` with `ansible-core==2.21.4` for the Python 3.12 controller; add no external collections, cloud SDK, or custom language runtime.
- [x] T002 [P] Create `bootstrap/ansible.cfg` with SSH host-key verification, existing SSH agent/config support, noninteractive authentication, and a 15-second connection timeout; do not introduce credential variables or disable trust checks.
- [x] T003 Create `bootstrap/bootstrap.yml` with controller validation followed by one-target preflight/install/verify phases, initially stopping before any unfinished installation path and routing `--check` through read-only validation with `validation_only` status; initialize the non-secret result fields `target`, `requested_version`, `observed_version` when known, `completed_stages`, four readiness outcomes, `status`, `failed_stage` when applicable, and `next_action`, preserving the data-model constraint "Status is `ready`, `failed`, or `validation_only`."

## Phase 2: Foundational Safety Checks

**Purpose**: Establish checks that must protect every later installation path.

- [x] T004 Implement controller-side request validation in `bootstrap/bootstrap.yml` before target fact gathering or mutation, preserving the exact data-model constraints "Exactly one inventory host; nonempty address or existing SSH alias", "Explicit nonempty existing remote user", and "Exact release tag; initial accepted value `v1.35.9+k3s1`"; zero/multiple hosts and omitted user/version must exit nonzero rather than silently succeeding with no matched hosts.
- [x] T005 Create `bootstrap/tasks/preflight.yml` with read-only Ubuntu 24.04 ARM64/Python 3.12/systemd/sudo checks, at least 2 CPUs, 1,900 MiB total RAM, 10 GiB free on the existing filesystem backing `/var/lib/rancher`, disabled swap, cgroups and overlay/bridge support, host tools, DNS/default route/HTTPS trust, release/image endpoint access, default CIDR overlap checks, and required-port conflicts; use 60-second inspection bounds and the nearest existing ancestor when the data directory is absent, then fail before persistent installation changes without installing missing packages or changing network policy.
- [x] T006 Add a fresh-host admission guard to `bootstrap/tasks/preflight.yml` that rejects existing K3s/RKE2/kubeadm executables, units, configuration, data, receipts, and conflicting services before mutation; do not execute unknown binaries or inspect secret contents, and keep all existing installations rejected until the compatible-rerun classifier in T018 replaces this guard.

**Checkpoint**: Fresh targets can be assessed, but no unknown existing installation can be overwritten. All user-story work depends on T001–T006.

## Phase 3: User Story 1 — Prepare an Existing VM (Priority: P1)

**Goal**: Bootstrap a fresh supported VM and report verified base-cluster readiness with no application or Vault deployment.

**Independent test**: Execute `quickstart.md` sections 1–3 on two clean supported VM installations, which may be sequential. Each uses one invocation and the three agreed inputs; all four readiness checks pass and only default cluster components exist.

**Existing test definitions**: Spec Story 1.1–1.4, SC-001, SC-002, and SC-005; read these before implementation. T012 records execution, not newly invented acceptance requirements.

- [x] T007 [P] [US1] Create `bootstrap/templates/k3s.service.j2` for root-owned default `k3s server` operation with notify/delegation/process-kill behavior, restart-on-failure, required kernel module loading, and a 300-second startup bound; retain default data/network behavior and restrictive administrative file permissions without external datastore, token, or registry settings.
- [x] T008 [P] [US1] Create the fresh-only artifact stage in `bootstrap/tasks/install.yml`: fetch the selected ARM64 release's named binary and SHA-256 manifest over verified HTTPS, validate the exact artifact checksum, and atomically install `/usr/local/bin/k3s` within the contract's 30-second idle and 600-second outer download bounds; fail on missing/mismatched artifacts, clean only owned staging, and never fall back to a latest channel or unpinned installer script.
- [x] T009 [US1] Complete `bootstrap/tasks/install.yml` by installing/validating the T007 unit, atomically writing the receipt, then enabling/starting the service with a 60-second nonblocking activation-request bound; preserve the data-model constraint "Root-owned JSON, mode `0600`, at `/var/lib/nacfson-bootstrap/installation.json`" with `profile_revision` = "Internal bootstrap compatibility revision, initially `1`", `k3s_version` = "Exact installed release", `binary_sha256` = "Verified digest of the installed public binary", and `unit_sha256` = "Digest of the installed non-secret systemd unit"; write it only after both managed files are complete and include no secret or datastore hashes.
- [x] T010 [P] [US1] Create `bootstrap/tasks/verify.yml` to check service active/enabled, authorized local `/readyz`, exactly one expected Ready node, and CoreDNS readiness through privileged `k3s kubectl` over SSH; confirm requested and observed versions agree, use bounded checks, and never export kubeconfig or invoke application deployment.
- [x] T011 [US1] Wire the completed fresh-install path and success result in `bootstrap/bootstrap.yml`, emitting `ready` only after T010 passes and identifying separate platform deployment as the next step; keep native task failures fatal and never return simulated success when the target is unavailable.
- [ ] T012 [US1] Run the fresh-host and check-mode acceptance commands from `specs/003-minimal-vm-bootstrap/quickstart.md` on two authorized clean test installations and create `specs/003-minimal-vm-bootstrap/validation.md` with target profile, selected/observed version, four readiness outcomes, no-extra-workload/credential observations, and actual pass/fail evidence; do not provision a VM as part of bootstrap or mark unavailable tests complete.

**Checkpoint**: A fresh-host MVP is demonstrable. Existing clusters still stop safely until US3 is complete; this checkpoint does not complete the full feature.

## Phase 4: User Story 2 — Receive an Actionable Failure (Priority: P1)

**Goal**: Detect invalid prerequisites, terminate bounded failures, and report partial progress honestly.

**Independent test**: Exercise invalid requests and individual prerequisite/download/readiness failures on disposable fixtures; verify nonzero exit, correct stage and next action, no installation changes on preflight failure, and no false rollback claim after partial installation.

**Existing test definitions**: Spec Story 2.1–2.3, SC-002, SC-004, and SC-005; `quickstart.md` sections 2 and 5.

- [x] T013 [P] [US2] Add actionable prerequisite outcomes to `bootstrap/tasks/preflight.yml`, identifying each failed local observation without dumping raw environments or secrets; enforce 60-second ordinary task bounds and state the limit that host inspection cannot prove OCI policy or complete image-pull connectivity.
- [x] T014 [P] [US2] Enforce failure boundaries in `bootstrap/tasks/install.yml`: 30-second download idle timeouts, 600-second outer artifact limits, 60-second activation-request bounds, verified-image refusal, and stage tracking; stop on failure while preserving installed files/data and clean only owned temporary staging, with no reset or claimed rollback.
- [x] T015 [P] [US2] Enforce separate 300-second service/API/node/DNS readiness deadlines and 5-second individual API request limits in `bootstrap/tasks/verify.yml`; distinguish each failed stage and ensure retries cannot exceed their stage deadline or turn a failed condition into success.
- [x] T016 [US2] Complete sanitized failure and check-mode handling in `bootstrap/bootstrap.yml`, preserving `failed` and `validation_only` statuses, completed stages, target, and corrective action; native unreachable/authentication errors must remain nonzero, and `--check` must execute safe inspections while making zero installation/service changes and never reporting a fresh host ready.
- [ ] T017 [US2] Execute the US2 cases from `specs/003-minimal-vm-bootstrap/quickstart.md`, including empty/multiple target inventories, missing inputs/access, unsupported profile, capacity/network failure, blocked/corrupt downloads, interrupted installation, and each readiness timeout; record elapsed bounds, unchanged preflight-failure state, sanitized output, and failure evidence in `specs/003-minimal-vm-bootstrap/validation.md`.

**Checkpoint**: Failure reporting is useful and bounded; completion of syntax/check mode alone is not live readiness evidence.

## Phase 5: User Story 3 — Rerun Without Losing State (Priority: P1)

**Goal**: Accept only verified compatible existing installations, preserve healthy hosts, and reject conflicting/partial state without automatic repair.

**Independent test**: Record node UID, sentinel data, file/receipt hashes, and service start/restart observations on a disposable healthy installation; run twice unchanged. Separately test stopped-compatible, conflicting, and unverified partial installations.

**Existing test definitions**: Spec Story 3.1–3.3, SC-003/004, `data-model.md` state transitions, and `quickstart.md` sections 4–5.

- [x] T018 [US3] Replace the all-existing-state rejection in `bootstrap/tasks/preflight.yml` with fresh/managed-compatible/conflicting-or-unknown classification: validate receipt ownership/schema, selected version, actual binary/unit hashes and expected unit, reject unexpected configuration/drop-ins/environment sources or foreign data, and exclude only recognized managed cluster routes/listeners from fresh-host overlap/conflict checks; missing/invalid receipt or mismatched effective configuration must stop unchanged, without executing an unverified binary or reading credential contents.
- [x] T019 [US3] Add state-dependent behavior in `bootstrap/tasks/install.yml`: managed-compatible hosts skip artifact installation and receipt writes; enabled/running services remain untouched, and stopped/disabled compatible services may be enabled/started without reinstall; preserve the data-model constraints "The receipt is not rewritten on a healthy rerun" and "Partial executable/unit installation without a receipt requires explicit operator recovery."
- [x] T020 [US3] Integrate the classified rerun path in `bootstrap/bootstrap.yml` so that only fresh or verified managed-compatible states can reach service mutation and readiness; reject version/profile conflicts without upgrade, downgrade, adoption, or reset, and preserve failure-state reporting for a compatible host that is not ready.
- [ ] T021 [US3] Perform two normal healthy reruns using `specs/003-minimal-vm-bootstrap/quickstart.md` section 4 and record unchanged node UID, sentinel, installed-file/receipt hashes, and service start/restart observations in `specs/003-minimal-vm-bootstrap/validation.md`; confirm zero reinstall, unnecessary restart, or configuration/data changes.
- [ ] T022 [US3] Exercise the remaining US3 state transitions on disposable hosts and record evidence in `specs/003-minimal-vm-bootstrap/validation.md`: stopped-compatible start, conflicting release, modified unit/binary, config or systemd overrides, missing/invalid receipt, and partial installation; verify permitted starts preserve identity/data while denied transitions perform no overwrite or reset.

**Checkpoint**: All three user stories can be validated independently by their fixtures. Safe reruns do not imply general repair, upgrades, or disaster recovery.

## Phase 6: Polish & Cross-Cutting Validation

**Purpose**: Integrate lightweight validation and confirm the delivered scope.

- [x] T023 [P] Extend `.github/workflows/lint.yaml` with a separate bootstrap syntax-check job using Python 3.12 and `bootstrap/requirements.txt`; invoke the playbook with explicit harmless syntax-check inputs, preserve existing application checks, and add no VM credentials or remote deployment actions.
- [x] T024 [P] Update `specs/003-minimal-vm-bootstrap/quickstart.md` and `docs/bootstrap-scope.md` to match the implemented command, prerequisites, accepted version, and ownership/rerun rules; retain exactly three mandatory feature settings and clearly identify base-cluster readiness versus later application/Vault deployment.
- [x] T025 Run final syntax/document checks and audit `bootstrap/` against FR-001–FR-009 and the operator contract, recording the coverage and SC-001–SC-005 evidence in `specs/003-minimal-vm-bootstrap/validation.md`; confirm no backend credential distribution, cloud/firewall/disk provisioning, app/Vault deployment, monitoring, backup, or upgrades were introduced, and retain explicit pending status for any unexecuted live acceptance.

## Dependencies & Execution Order

### Phase dependencies

```text
Setup T001–T003
    → Foundation T004–T006
    → US1 T007–T012 (fresh-host MVP)
    → US2 T013–T017 (failure behavior)
    → US3 T018–T022 (compatible reruns)
    → Polish T023–T025
```

All three stories are P1. The order reflects their shared files and runtime dependencies, not lower priority for error handling or preservation. US1 already fails safely on unknown existing state; US3 adds the ability to accept compatible existing hosts.

- T001 and T002 are independent; T003 follows their setup.
- T004 → T005 → T006 establish the shared guards.
- After T006, T007, T008, and T010 can be authored independently. T009 requires T007/T008. T011 requires T009/T010. T012 requires the integrated implementation and an authorized test target.
- After US1, T013/T014/T015 can proceed independently. T016 integrates them; T017 validates the result.
- US3 remains serial: T018 → T019 → T020 → T021 → T022. This avoids competing changes to shared state and fixtures.
- T023 and T024 can proceed together after story implementation. T025 follows both and aggregates actual validation results.
- Live evidence tasks require existing authorized disposable hosts. Missing access does not block local implementation/syntax work, but it does block checking off those acceptance tasks; no cloud creation is implied.

### Parallel examples by story

**US1**: After foundation completion, author `k3s.service.j2` (T007), binary download/verification (T008), and readiness checks (T010) in parallel. Integrate only after their prerequisites finish.

**US2**: After the fresh path is complete, refine prerequisite errors (T013), installation limits (T014), and readiness limits (T015) in their separate task files. Perform T016/T017 afterward.

**US3**: No safe code/VM parallelism is marked. The classifier, state-dependent installation, and main dispatch must agree, and both evidence tasks write the same validation record. Execute this phase sequentially.

## Requirement Coverage

| Requirement | Tasks |
| --- | --- |
| FR-001: three settings / one existing target | T001, T002, T004, T017 |
| FR-002: validated supported host | T001, T005, T006, T013, T017 |
| FR-003: explicit version / default cluster | T004, T007–T009, T012 |
| FR-004: bounded verified readiness | T002, T010, T012, T014–T017 |
| FR-005: truthful failures | T003, T011, T013–T017 |
| FR-006: preservation and conflicts | T006, T009, T018–T022 |
| FR-007: no backend credentials / independent bootstrap | T002, T005, T008, T012, T016, T025 |
| FR-008: excluded lifecycle/application work | T006–T009, T018–T020, T024–T025 |
| FR-009: clear result / limited readiness claim | T003, T011–T012, T016–T017, T020, T024–T025 |
| CI & Quality Gates | T023 |

## Implementation Strategy

1. Establish controller setup and shared guards first; unfinished installation paths must not mutate a target.
2. Deliver US1 as the fresh-VM MVP and validate it on disposable hosts. Do not advertise compatible rerun support yet.
3. Complete accurate failure reporting, then compatible reruns. All P1 stories are required before the feature is complete.
4. Integrate syntax CI and confirm the full acceptance record. Manual cloud provisioning and later platform deployment remain separate.

## Notes

- Total: 25 tasks — 6 setup/foundation, 6 US1, 5 US2, 5 US3, and 3 cross-cutting.
- Use the already established three-input interface and internal constants; do not grow a configuration framework to implement these tasks.
- Runtime-owned K3s networking and infrastructure credentials are part of the agreed base installation. They are not permission to distribute application/backend credentials or change operator cloud/firewall policy.
- Existing Vault specification changes, application manifests, and application release scripts remain outside this feature.
