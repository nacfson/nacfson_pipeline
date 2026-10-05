# Implementation Tasks: Automated Post-Vault Workload Orchestration

**Feature**: `005-post-vault-automation` | **Date**: 2026-10-05 | **Spec**: [specs/005-post-vault-automation/spec.md](file:///home/nacfson/Projects/nacfson_pipeline/specs/005-post-vault-automation/spec.md) | **Plan**: [specs/005-post-vault-automation/plan.md](file:///home/nacfson/Projects/nacfson_pipeline/specs/005-post-vault-automation/plan.md)

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Build and CI infrastructure for in-house services and proxy components

- [X] T001 Create multi-stage Dockerfile for `vault-db-proxy` in `services/vault-proxies/Dockerfile.db-proxy`
- [X] T002 [P] Create multi-stage Dockerfile for `vault-registry-proxy` in `services/vault-proxies/Dockerfile.registry-proxy`
- [X] T003 [P] Create multi-architecture GitHub Actions workflow in `.github/workflows/build-images.yaml` for building and publishing proxy and gateway images to GHCR

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Core ServiceAccount identities and Vault Kubernetes authentication prerequisites

**⚠️ CRITICAL**: Must be completed before workloads can authenticate to Vault

- [X] T004 Define dedicated ServiceAccounts for platform workloads in `deploy/platform/identity/service-accounts.yaml` and `deploy/platform/vault/service-accounts.yaml`
- [X] T005 [P] Create OpenBao post-unseal configuration script in `scripts/vault-configure-k8s-auth.sh` to enable `auth/kubernetes`, configure token reviewer, and apply workload roles
- [X] T006 [P] Add OpenBao policy for PostgreSQL engine access in `deploy/platform/vault/policies/postgres-server.hcl` with read capability on `kv/data/database/postgres-admin`

**Checkpoint**: Core identities, policies, and Vault Kubernetes Auth ready.

---

## Phase 3: User Story 1 - Hands-Off Workload Credential Provisioning & Boot (Priority: P1) 🎯 MVP

**Goal**: Workloads (PostgreSQL, database init job, Keycloak, Gateway) automatically retrieve runtime secrets from OpenBao into in-memory `emptyDir` mounts via Kubernetes ServiceAccount tokens at startup.

**Independent Test**: Restart `postgres-0`, `keycloak`, and `gateway` pods; confirm they authenticate via ServiceAccount TokenReview, populate `/var/run/secrets/`, and reach `Ready: 1/1` without manual intervention.

### Implementation for User Story 1

- [X] T007 [P] [US1] Update `deploy/platform/database/postgres-statefulset.yaml` with `secret-fetcher` initContainer using projected ServiceAccount token to write `admin_password` (`0400`) into `database-secrets` `emptyDir`
- [X] T008 [P] [US1] Update `deploy/platform/database/postgres-init-job.yaml` with `secret-fetcher` initContainer to write `admin_password`, `keycloak_password`, `pn_password`, and `backup_password` into `database-secrets` `emptyDir`
- [X] T009 [P] [US1] Update `deploy/platform/identity/keycloak-deployment.yaml` with `secret-fetcher` initContainer using projected ServiceAccount token to write `keycloak_db_password` into `keycloak-secrets` `emptyDir`
- [X] T010 [P] [US1] Update `deploy/platform/identity/gateway-deployment.yaml` with `secret-fetcher` initContainer using projected ServiceAccount token to write `hmac`, `client_secret`, and `session_revocation_secret` into `gateway-secrets` `emptyDir`
- [X] T011 [US1] Update `deploy/platform/database/kustomization.yaml` and `deploy/platform/identity/kustomization.yaml` to include new ServiceAccount manifests and ensure role bindings

**Checkpoint**: At this point, User Story 1 is functional: all workloads boot and ingest runtime credentials automatically from Vault into memory.

---

## Phase 4: User Story 2 - Autonomous GitOps Cascade & Convergence (Priority: P2)

**Goal**: Continuous deployment reconciler automatically cascades and converges all 10 platform layers to `Ready: True` once OpenBao is unsealed, resolving `ErrImagePull` on proxies and layer dependency blocks.

**Independent Test**: Observe deployment controller status via `flux get kustomizations -A` and `kubectl get kustomizations -A`; verify all 10 layers transition sequentially to `Ready: True`.

### Implementation for User Story 2

- [X] T012 [US2] Update `deploy/platform/vault-proxies/db-proxy.yaml` and `deploy/platform/vault-proxies/registry-proxy.yaml` with ServiceAccount assignments and image references
- [X] T013 [P] [US2] Review and align layer dependencies in `deploy/clusters/platform/flux-system/` to ensure clean cascade from `vault` -> `database` -> `vault-proxies` -> `identity` -> `ingress` -> tenant layers
- [X] T014 [US2] Trigger and verify GitHub Actions workflow in `.github/workflows/build-images.yaml` publishes multi-arch images with immutable tags and digests to GHCR
- [X] T015 [US2] Reconcile Flux platform kustomizations on cluster and verify all 10 layers achieve `Ready: True`

**Checkpoint**: At this point, User Stories 1 and 2 work in unison; full GitOps cascade completes autonomously.

---

## Phase 5: User Story 3 - Automated Lifecycle Maintenance & Disaster Readiness (Priority: P3)

**Goal**: Scheduled credential rotation and automated encrypted backups run without human intervention, maintaining 7-day retention on dedicated PVCs.

**Independent Test**: Trigger one-off runs of `openbao-daily-snapshot` and `postgres-daily-backup` CronJobs; verify archives are generated, integrity verified, and files older than 7 days pruned.

### Implementation for User Story 3

- [X] T016 [P] [US3] Update `deploy/platform/vault/backup-cronjob.yaml` with automated 7-day retention pruning logic (`find /backups -name "*.snap" -mtime +7 -delete`) and non-root security context
- [X] T017 [P] [US3] Update `deploy/platform/database/backup-cronjob.yaml` with automated 7-day retention pruning logic (`find /backups -name "*.sql.gz" -mtime +7 -delete`) and non-root security context
- [X] T018 [US3] Update `scripts/vault-rotate.sh` to validate dual-key HMAC rotation and seamless credential rotation across database roles without dropping active sessions

**Checkpoint**: All three user stories are operational; automated lifecycle hygiene and disaster backups are verified.

---

## Phase 6: Polish & Cross-Cutting Concerns

**Purpose**: End-to-end verification, master platform validation script updates, and documentation

- [X] T019 [P] Update `scripts/verify-platform.sh` to include automated checks for Vault Kubernetes Auth, initContainer secret injection, and proxy health
- [X] T020 Run full preflight verification suite `./scripts/verify-platform.sh` against the target cluster
- [X] T021 [P] Update `specs/005-post-vault-automation/quickstart.md` with final end-to-end verification results

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies - can start immediately
- **Foundational (Phase 2)**: Depends on Setup completion - BLOCKS all user stories
- **User Story 1 (Phase 3)**: Depends on Phase 2 - enables database and identity workloads to boot
- **User Story 2 (Phase 4)**: Depends on Phase 1 (CI images) & Phase 3 (database/identity healthy) - enables full cluster GitOps convergence
- **User Story 3 (Phase 5)**: Depends on Phase 3 (database and vault healthy) - enables scheduled backups and rotation
- **Polish (Phase 6)**: Depends on all user stories complete

### User Story Dependencies

- **User Story 1 (P1)**: Can start after Foundational (Phase 2). No dependencies on US2 or US3.
- **User Story 2 (P2)**: Requires US1 (so database/identity don't block reconciler) and Phase 1 (proxies built in CI).
- **User Story 3 (P3)**: Requires US1 (database and vault running).

### Within Each User Story

- ServiceAccounts and policies before workload initContainers
- Workload initContainers before reconciliation
- Storage pruning before backup execution validation

### Parallel Opportunities

- T001, T002, T003 can be authored in parallel
- T005, T006 can run in parallel
- In Phase 3, all workload initContainers (T007, T008, T009, T010) edit distinct manifests and can run in parallel
- In Phase 5, T016 and T017 edit distinct CronJob manifests and can run in parallel
- In Phase 6, T019 and T021 can run in parallel

---

## Parallel Execution Examples

### User Story 1 (Workload InitContainers)
```bash
# Update all workload manifests concurrently:
Task T007: "Update deploy/platform/database/postgres-statefulset.yaml"
Task T008: "Update deploy/platform/database/postgres-init-job.yaml"
Task T009: "Update deploy/platform/identity/keycloak-deployment.yaml"
Task T010: "Update deploy/platform/identity/gateway-deployment.yaml"
```

### User Story 3 (Backup Retention)
```bash
# Update backup cronjobs concurrently:
Task T016: "Update deploy/platform/vault/backup-cronjob.yaml"
Task T017: "Update deploy/platform/database/backup-cronjob.yaml"
```

---

## Implementation Strategy

### MVP First (User Story 1 Only)
1. Complete Phase 1: Setup (`services/vault-proxies/Dockerfile.*`, `.github/workflows/build-images.yaml`)
2. Complete Phase 2: Foundational (ServiceAccounts, `scripts/vault-configure-k8s-auth.sh`, policies)
3. Complete Phase 3: User Story 1 (InitContainers in PostgreSQL, Keycloak, Gateway)
4. **STOP and VALIDATE**: Confirm PostgreSQL and Keycloak boot to `Ready: 1/1` without manual intervention.

### Incremental Delivery
1. Foundation + US1 -> Resolves `CrashLoopBackOff` on `postgres-0` (MVP)
2. Add US2 -> Publishes multi-arch images to GHCR, resolves `ErrImagePull`, converges all 10 Flux layers
3. Add US3 -> Configures scheduled backup retention and secret rotation
4. Phase 6 -> Full `./scripts/verify-platform.sh` verification pass

---

## Notes

- All tasks strictly follow `- [ ] [TaskID] [P?] [Story?] Description with file path`.
- In-memory `tmpfs` mounts (`emptyDir: { medium: Memory }`) ensure zero secrets touch persistent disk.
- Non-root security contexts (UID 999/1000/65532) ensure full Restricted PSA compliance (Constitution Principle II).
