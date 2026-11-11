# Tasks: GKE Platform Portability & Full-State Cutover

**Input**: Design documents from `/specs/012-gke-platform-migration/` (`plan.md`, `spec.md`, `research.md`, `data-model.md`, `contracts/`, `quickstart.md`)
**Prerequisites**: `plan.md` (required), `spec.md` (required), `research.md`, `data-model.md`, `contracts/gke-capacity-and-placement.md`, `contracts/portable-network-policy-and-gar.md`, `contracts/fluentbit-recovery-and-cutover-bundle.md`
**Target GCP Coordinates**: Project `nacfson-pipeline`, Zone `asia-northeast3-a` (Seoul; `asia-northeast1-a` Tokyo drop-in compatible)

**Tests**: Preflight, observability, and image-dispatch contract unit tests (`tests/preflight/`, `tests/observability/`, `tests/skills/`) are included for User Stories 1 and 2 because `scripts/render-validate.sh` and CI enforce them as mandatory Gate 1 offline quality gates before any cloud resource may be provisioned.

**Organization**: Tasks are grouped by user story (`US1` P1 MVP, `US2` P1, `US3` P2) so each increment can be implemented and validated independently while keeping `clusters/local-k3s/`, `clusters/gke/`, and `clusters/vps-k3s/` green during Gates 1–3.

## Format: `[ID] [P?] [Story?] Description`

- **[P]**: Can run in parallel (different files, no dependencies on incomplete tasks in the same phase)
- **[Story]**: Which user story this task belongs to (`[US1]`, `[US2]`, `[US3]`)
- Exact repository file paths and verbatim `data-model.md` / `contracts/` constraints are included in every task

---

## Phase 1: Setup (Shared Infrastructure & Directory Skeleton)

**Purpose**: Create the new GKE overlay, Traefik ingress, and GCP CI/GAR directory scaffolding required by `plan.md` without altering live manifests yet.

- [X] T001 Create directory skeleton `deploy/gcp/`, `deploy/platform/traefik/`, and `clusters/gke/{flux-system,observability,projects}/` per `specs/012-gke-platform-migration/plan.md`
- [X] T002 [P] Create declarative Google Artifact Registry cleanup policy keeping the `2` most recent tagged/pinned digests per image package and deleting unreferenced digests older than `7d` (`< 0.5 GB/mo` Free Tier guardrail) in `deploy/gcp/gar-cleanup-policy.json`
- [X] T003 [P] Create declarative Google Cloud Build multi-architecture (`linux/amd64,linux/arm64`) OCI manifest-list pipeline building first-party platform and tenant images and pushing `@sha256` digests to `asia-northeast3-docker.pkg.dev/nacfson-pipeline/nacfson-public/<image>` in `deploy/gcp/cloudbuild.yaml`

---

## Phase 2: Foundational (Blocking Governance, Constitution & Registry Retirement Prerequisites)

**Purpose**: Synchronize Constitution `v2.3.0` references, allowlist GKE control-plane identities, retire `vault-registry-proxy`, and remove K3s-only `local-path` storage assumptions that block multi-environment rendering.

**⚠️ CRITICAL**: Complete this phase before starting User Stories 1–3 so preflight rules, admission policies, and Vault proxy builds share a consistent constitutional baseline.

- [X] T004 Synchronize Constitution `v2.3.0` (`Amendment 2.3.0`, already ratified in `.specify/memory/constitution.md`) version references and `ServiceAccount/openbao` definition (`automountServiceAccountToken: false` in base) in `deploy/platform/vault/service-accounts.yaml` and `CLAUDE.md`
- [X] T005 [P] Allowlist GKE control-plane principals (`system:gke-common-webhooks`, `system:gcp-controller-manager`, `system:addon-manager`, `system:cluster-autoscaler`, `request.userInfo.username.startsWith('system:serviceaccount:gke-managed-')`) and `.gke.io`, `.cloud.google.com`, `.networking.gke.io` cluster-scoped CRD suffixes in `deploy/platform/governance/manual-change-policy.yaml`
- [X] T006 [P] Retire `vault-registry-proxy` across all environments by deleting `deploy/platform/vault-proxies/registry-proxy.yaml`, `deploy/platform/vault/policies/registry-proxy.hcl`, `deploy/platform/vault/registries.yaml.template`, `services/vault-proxies/cmd/registry-proxy/`, and `services/vault-proxies/Dockerfile.registry-proxy`, and removing `ServiceAccount/vault-registry-proxy` and `registry-proxy` references from `deploy/platform/vault/service-accounts.yaml`, `deploy/platform/vault-proxies/kustomization.yaml`, `deploy/platform/vault/kustomization.yaml`, and `.github/workflows/build-images.yaml`
- [X] T007 [P] Update Vault proxy verification scripts to remove retired `cmd/registry-proxy` build and drill steps in `tests/vault/test_registry_proxy.sh` and `tests/vault/test_rotation_drill.sh`
- [X] T008 [P] Update image-bump dispatch handler, pinning skill, and unit tests to accept `[a-z0-9-]+-docker.pkg.dev/nacfson-pipeline/nacfson-public/<name>@sha256:<64-hex>` digests alongside `ghcr.io/nacfson/` in `scripts/image-bump-request.py`, `.agents/skills/update-project-images/scripts/update_project_images.py`, `.agents/skills/update-project-images/SKILL.md`, and `tests/skills/test_image_bump_request.py`
- [X] T009 [P] Remove hardcoded `storageClassName: local-path` from `PVC/error-detector-state` so `local-k3s`, `vps-k3s`, and `gke` resolve their native default `StorageClass` in `deploy/platform/observability/error-detector.yaml`

**Checkpoint**: Governance baseline, Constitution `v2.3.0`, public GAR digest conventions, and `vault-registry-proxy` retirement are ready for User Story phases.

---

## Phase 3: User Story 1 - Offline GKE Environment Overlay & Multi-Node Preflight Validation (Priority: P1) 🎯 MVP

**Goal**: Deliver the declarative `clusters/gke/` Design 1 overlay (`1 × e2-medium` On-Demand + `1 × e2-small` Spot in `nacfson-pipeline` / `asia-northeast3-a`, `Traefik` `hostPort: 80/443` with zero GCP LoadBalancer), portable `networking.k8s.io/v1` `NetworkPolicy` manifests shared with `clusters/local-k3s/`, and multi-node per-pool preflight enforcement (`303m` CPU / `418Mi` RAM $1/3$ project budget).

**Independent Test**: Run `(cd services/vault-proxies && go test -v ./... && go vet ./...)`, `python3 -m unittest discover tests/preflight`, `./scripts/render-validate.sh local-k3s`, and `./scripts/render-validate.sh gke` offline; verify 100% of layers render cleanly, pass schema checks, multiply `DaemonSet` reservations across `nodeCount: 2`, verify On-Demand vs. Spot per-pool CPU/memory fit, and enforce portable `NetworkPolicy` rules identically across `local-k3s` and `gke`.

### Contract Tests for User Story 1

- [X] T010 [P] [US1] Add unit tests for `ClusterCapacityProfile` (`environment` in `local-k3s|vps-k3s|gke`, `nodeCount: 2`, `sum(pool.allocatable) == allocatable`, `DaemonSet` fan-out multiplier, `NodePoolAllocation.provisioning` `enum(standard, spot)` fit, zero `RWO` PVC or `keycloak`/`proj-*` on `spot`, `>= 50m` `requests.cpu` and `>= 192 MiB` `requests.memory` backup headroom on `ondemand-core`, and GAR `@sha256` digest validation) in `tests/preflight/test_platform_preflight.py`
- [X] T011 [P] [US1] Update outbound HTTPS egress contract tests to assert `PortableEgressPolicy` (`networking.k8s.io/v1` `NetworkPolicy` permitting `0.0.0.0/0` TCP `:443` with mandatory `except` CIDRs `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`, `169.254.169.254/32` on `pn-backend`, `vault-ai-proxy`, and `vault-notify-proxy`, and `0.0.0.0/0` forbidden on `vault/openbao`) in `tests/preflight/test_translation_egress.py`, `tests/preflight/test_ai_gateway_contract.py`, and `tests/preflight/test_notify_gateway_contract.py`

### Implementation for User Story 1

- [X] T012 [P] [US1] Replace `CiliumNetworkPolicy/pn-backend-translation-egress` with standard `networking.k8s.io/v1` `NetworkPolicy` (`ipBlock: {cidr: 0.0.0.0/0, except: [10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 169.254.169.254/32, 100.64.0.0/10]}` on TCP `:443` scoped to `app.kubernetes.io/name: pn-backend`) in `deploy/projects/pn/boundary/translation-egress.yaml`
- [X] T013 [P] [US1] Replace the 3 `CiliumNetworkPolicy` blocks (`openbao-apiserver-egress`, `ai-proxy-egress`, `notify-proxy-egress`) and remove `registry-proxy-policy` using standard `networking.k8s.io/v1` `NetworkPolicy` rules (`0.0.0.0/0` forbidden on `vault/openbao`; TCP `:443` minus `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`, `169.254.169.254/32`, `100.64.0.0/10` on `vault-ai-proxy` and `vault-notify-proxy`) in `deploy/platform/vault/network-policy.yaml`
- [X] T014 [P] [US1] Configure `policy-cidr-match-mode: "nodes"` on `ConfigMap/cilium-config` so standard `NetworkPolicy` `ipBlock` rules match the K3s single-node `kube-apiserver` IP on `local-k3s` in `deploy/platform/cni/kustomization.yaml`
- [X] T015 [P] [US1] Synchronize canonical isolation and $1/n$ budget contract documentation for portable `networking.k8s.io/v1` `NetworkPolicy` and GKE 2-pool `ClusterCapacityProfile` in `contracts/CONTRACT_ISOLATION_BUDGET.md`
- [X] T016 [P] [US1] Create non-root `Traefik v3.3.4` edge ingress manifests (`Deployment/traefik` in `platform` with `replicas: 1`, `strategy.type: Recreate`, `runAsNonRoot: true`, `runAsUser: 65532`, `capabilities.drop: ["ALL"]`, `resources: {requests: {cpu: 30m, memory: 64Mi}, limits: {cpu: 200m, memory: 96Mi}}`, `--entrypoints.web.address=:8000` -> `hostPort: 80`, `--entrypoints.websecure.address=:8443` -> `hostPort: 443`, `nodeSelector: {"cloud.google.com/gke-provisioning": "standard"}`, and `Service/traefik` `type: ClusterIP`) in `deploy/platform/traefik/{crds.yaml,rbac.yaml,deployment.yaml,service.yaml,kustomization.yaml}`
- [X] T017 [P] [US1] Create GKE Design 1 `ClusterCapacityProfile` (`environment: gke`, `nodeName: gke-zonal-standard`, `nodeCount: 2`, `allocatable: {cpuMillicores: 1880, memoryMib: 4408}`, `systemReserved: {cpuMillicores: 350, memoryMib: 576}`, `ondemand-core` `e2-medium` `940m/2972Mi` `provisioning: standard` and `spot-controllers` `e2-small` `940m/1436Mi` `provisioning: spot` tainted `cloud.google.com/gke-spot=true:NoSchedule`) in `clusters/gke/capacity.yaml`
- [X] T018 [P] [US1] Create GKE `flux-system` bootstrap manifests patching `source-controller` (`requests: {cpu: 50m, memory: 64Mi}, limits: {cpu: 500m, memory: 128Mi}`) and `kustomize-controller` (`requests: {cpu: 100m, memory: 64Mi}, limits: {cpu: 500m, memory: 128Mi}`) with `replicas: 1`, `nodeSelector: {"cloud.google.com/gke-spot": "true"}`, and `cloud.google.com/gke-spot=true:NoSchedule` toleration syncing `./clusters/gke` in `clusters/gke/flux-system/{gotk-components.yaml,gotk-sync.yaml,kustomization.yaml}`
- [X] T019 [P] [US1] Create GKE tenant project Flux `Kustomization` overlays (`pn.nacfson.cloud`, `health.nacfson.cloud`, `blog.nacfson.cloud`, right-sizing `pn-backend` `requests/limits` to `100m/256Mi`, `health-backend` `requests/limits` to `100m/256Mi`, and `blog-web` `requests/limits` to `50m/128Mi` on GKE while preserving `clusters/local-k3s/` baseline limits) and index manifest in `clusters/gke/projects/{kustomization.yaml,proj-pn.yaml,proj-health.yaml,proj-blog.yaml}`
- [X] T020 [US1] Create GKE Flux layer DAG (`governance` -> `cert-manager` / `vault` / `project-boundaries` -> `vault-proxies` / `database` / `projects` -> `identity` -> `traefik` -> `ingress` -> `tls` -> `observability`, patching `ResourceQuota/project-pn-quota` to `416Mi` on `project-boundaries`, patching `cert-manager` ×3 onto `spot-controllers` with `128Mi/64Mi/48Mi` limits, right-sizing `keycloak` `requests/limits` to `100m/832Mi` · `500m/832Mi` and `postgres`/`gateway`/`openbao`/`vault-*-proxy`/`fluent-bit`/`error-detector`/backup `CronJob` CPU requests to yield exact `platformReservations = (970m, 3152Mi)`, scoping `observability` `healthChecks` to `DaemonSet/fluent-bit`, annotating `ServiceAccount/openbao` with `iam.gke.io/gcp-service-account: openbao-kms@nacfson-pipeline.iam.gserviceaccount.com` + setting `serviceAccountName: openbao` (`automountServiceAccountToken: true`) and `seal "gcpckms"` on `openbao`, and patching `openbao-ingress-policy` with GKE control-plane `:443`, Workload Identity `169.254.169.254/32:80,988` for `nacfson-pipeline.svc.id.goog`, and Private Google Access `199.36.153.8/30:443` for Cloud KMS) in `clusters/gke/layers.yaml` and verify `clusters/local-k3s/layers.yaml` lockstep
- [X] T021 [US1] Implement `ClusterCapacityProfile` multi-node `DaemonSet` multiplier, `max(replicas, 1)` platform `Deployment` reservation (keeping paused `error-detector`'s `25m/64Mi` slot pre-reserved for Spec 014), `NodePoolAllocation` (`standard` vs. `spot`) schedulability & zero-`RWO`-on-Spot checks, `PortableEgressPolicy` validation, and `[a-z0-9-]+-docker.pkg.dev/nacfson-pipeline/nacfson-public/<name>@sha256:<64-hex>` digest validation in `scripts/platform-preflight.py`
- [X] T022 [US1] Add `gke` environment support and zero-`LoadBalancer` (`Service` `type: ClusterIP` + `Traefik` `hostPort: 80/443`) invariant checks in `scripts/render-invariants.py` and `scripts/render-validate.sh`

**Checkpoint**: User Story 1 is complete when `./scripts/render-validate.sh local-k3s`, `./scripts/render-validate.sh gke`, and `python3 -m unittest discover tests/preflight` pass offline (`SC-001`).

---

## Phase 4: User Story 2 - Crash- & Outage-Resilient Log Collector State on Multi-Node Clusters (Priority: P1)

**Goal**: Replace the single-node `ReadWriteOnce` `fluent-bit-state` PVC with per-node `CollectorRecoveryState` (`hostPath: /var/lib/fluent-bit-state`, `tail.db` SQLite WAL + `/state/chunks` filesystem buffering capped at `256 MiB`/node) across `local-k3s` and `gke`, and wire `clusters/gke/observability/` directly to OCI Kafka (`logs.proj.valid` `192M` + `logs.proj.quarantine` `64M`) while removing in-cluster `StatefulSet/victorialogs` + `NetworkPolicy/victorialogs-ingress` and pausing `Deployment/error-detector` (`replicas: 0`) on GKE until Spec 014.

**Independent Test**: Run `python3 -m unittest discover tests/observability`, `python3 -m unittest discover tests/preflight`, `./scripts/render-validate.sh local-k3s`, and `./scripts/render-validate.sh gke`; on `local-k3s` (Gate 2), scale `victorialogs` to `0`, restart the `fluent-bit` pod while emitting 200 log lines, scale `victorialogs` back to `1`, and verify zero lost or duplicated records (`SC-002`).

### Contract Tests for User Story 2

- [X] T023 [P] [US2] Update observability and preflight unit tests to assert `PVC/fluent-bit-state` removal, `DaemonSet/fluent-bit` `hostPath` `/var/lib/fluent-bit-state` (`type: DirectoryOrCreate`, `readOnly: false` at `/state`) + `/var/log/pods` (`readOnly: true`), `cloud.google.com/gke-spot=true:NoSchedule` toleration, `256 MiB` filesystem chunk cap, and GKE `victorialogs`/`victorialogs-ingress` deletion + `error-detector` `replicas: 0` pause + `[OUTPUT] kafka` configuration in `tests/observability/test_observability_manifests.py` and `tests/preflight/test_platform_preflight.py`

### Implementation for User Story 2

- [X] T024 [US2] Delete `deploy/platform/observability/fluent-bit-state-pvc.yaml`, remove its entry from `deploy/platform/observability/kustomization.yaml`, and update `DaemonSet/fluent-bit` to mount `hostPath: {path: /var/lib/fluent-bit-state, type: DirectoryOrCreate}` (`readOnly: false` at `/state`) and tolerate `cloud.google.com/gke-spot=true:NoSchedule` in `deploy/platform/observability/fluent-bit-daemonset.yaml`
- [X] T025 [US2] Configure `CollectorRecoveryState` filesystem persistence (`[SERVICE]` `storage.path /state/chunks`, `storage.sync normal`, `storage.checksum off`, `storage.max_chunks_up 128`; `[INPUT] tail` `DB /state/tail.db`, `storage.type filesystem`; `[FILTER] multiline` `emitter_storage.type filesystem`; `[FILTER] rewrite_tag` `Emitter_Name rt_quarantine`, `Emitter_Storage.type filesystem`; `[OUTPUT]` `storage.total_limit_size 256M`) in `deploy/platform/observability/fluent-bit/fluent-bit.conf`
- [X] T026 [US2] Create GKE observability Kustomize overlay deleting `StatefulSet/victorialogs`, `Service/victorialogs`, and `NetworkPolicy/victorialogs-ingress`, pausing `Deployment/error-detector` at `replicas: 0` (until Spec 014 switches it to Kafka `logs.proj.errors`), permitting WireGuard Kafka broker egress (`10.200.0.11:9092,10.200.0.12:9092,10.200.0.13:9092`), and patching `fluent-bit-config` with two `[OUTPUT] kafka` blocks (`rdkafka.request.required.acks -1`, `rdkafka.enable.idempotence true`, `Retry_Limit False`, `Match proj.*` -> `Topics logs.proj.valid` `storage.total_limit_size 192M` and `Match quarantine.*` -> `Topics logs.proj.quarantine` `storage.total_limit_size 64M` = `256 MiB max per node`) in `clusters/gke/observability/{kustomization.yaml,fluent-bit-kafka.conf}`
- [X] T027 [US2] Update `hostPath` preflight rule to permit `/var/lib/fluent-bit-state` (`readOnly: false`) alongside `/var/log/pods` (`readOnly: true`) exclusively on `DaemonSet/fluent-bit` in namespace `observability` (rejecting any other `hostPath` in `observability` or `proj-*`) in `scripts/platform-preflight.py` and synchronize `contracts/CONTRACT_LOG_SCHEMA.md`

**Checkpoint**: User Stories 1 and 2 both render and pass offline preflight + observability contract suites cleanly across `local-k3s` and `gke`.

---

## Phase 5: User Story 3 - Full-State Backup Bundle & Single-Shot GKE Cutover with Identity Continuity (Priority: P2)

**Goal**: Deliver `FullStateMigrationBundle` extraction, SHA-256 verification, local restore rehearsal, OpenBao cross-cluster K8s auth re-binding, `keycloak` persistent DB session continuity, and Gate 4 `gcpckms` (`nacfson-pipeline` / `asia-northeast3`) seal migration.

**Independent Test**: Run `bash -n scripts/db-migrate-dump.sh scripts/vault-configure-k8s-auth.sh scripts/cutover-bundle.sh` offline; at Gate 3, run `./scripts/cutover-bundle.sh export`, `verify`, and `restore --target local-k3s` followed by `./scripts/smoke-auth-chain.sh --env local-k3s` to verify preserved `(issuer, subject)` user identities and zero-warning TLS (`SC-003`, `SC-004`).

### Implementation for User Story 3

- [X] T028 [P] [US3] Enable database-persisted user sessions (`--features=persistent-user-sessions`) on `Deployment/keycloak` so active OIDC sessions reside in `postgres-0` (`keycloak` DB) across pod/node restarts in `deploy/platform/identity/keycloak-deployment.yaml`
- [X] T029 [P] [US3] Extend PostgreSQL dump/restore helper with `--keycloak` (`TARGET_DB="keycloak"`, `TARGET_ROLE="${KEYCLOAK_DB_ROLE:-keycloak_user}"`, SHA-256 `.sha256` sidecar generation/verification, and zero-plaintext `kubectl exec -i` streaming alongside `--project <slug>`) in `scripts/db-migrate-dump.sh`
- [X] T030 [P] [US3] Update OpenBao Kubernetes auth configuration script to extract the cluster CA from `ConfigMap/kube-root-ca.crt` in `kube-system` (with `kubectl config view --raw` fallback), support GKE cluster-admin RBAC without K3s-only `ssh` assumptions, and drop retired `vault-registry-proxy` role binding in `scripts/vault-configure-k8s-auth.sh`
- [X] T031 [US3] Create `FullStateMigrationBundle` orchestrator (`export` | `verify` | `restore`) enforcing an out-of-git `chmod 0700` staging directory, `manifest.sha256` verification across all 6 required artifacts (`openbao.snap`, `keycloak.dump.gz`, `proj_pn.dump.gz`, `proj_health.dump.gz`, `edge-tls-secrets.yaml`, `victoria-logs-data.tar.gz`), `flux suspend kustomization vault` Shamir-first snapshot restore, automatic post-snapshot `vault-configure-k8s-auth.sh` + `vault-reconcile.sh --confirm` execution, and `flux resume kustomization vault` + Gate 4 `gcpckms` (`project=nacfson-pipeline`, `region=asia-northeast3`, `bao operator unseal -migrate`) seal migration in `scripts/cutover-bundle.sh`
- [X] T032 [US3] Document Gate 3 local full-state restore drill, Gate 4 single-shot GKE cutover (`nacfson-pipeline`, `asia-northeast3-a`, `hostPort: 80/443` static IP, `ServiceAccount/openbao` Workload Identity IAM binding, `flux suspend`/`resume` Shamir->`gcpckms` migration), and immediate Gate 4 repurposing of `vps-k3s` into OCI Kafka Broker VM 1 in `docs/operations-runbook.md`

**Checkpoint**: All 3 user stories are implemented with offline syntax/unit verification complete and ready for Gate 2–4 execution.

---

## Phase 6: Polish & Cross-Cutting Concerns

**Purpose**: Synchronize repository-wide documentation, the hook-enforced interactive architecture handbook, and run the complete Gate 1 verification matrix.

- [X] T033 [P] Update system topology, pinned versions, capacity envelopes, and master repository index for `clusters/gke/` Design 1 (`nacfson-pipeline` / `asia-northeast3-a`), public GAR, portable `NetworkPolicy`, and Fluent Bit Solution B in `ARCHITECTURE.md`, `DEPENDENCIES.md`, and `INDEX.md`
- [X] T034 Synchronize interactive topology nodes, protocol flows, NetworkPolicy simulator, Flux DAG, OSS catalog, and $1/n$ budget calculator for Spec 012 in `docs/platform-architecture.html`
- [X] T035 Execute the complete Gate 1 offline validation suite (`gateway` and `services/vault-proxies` `go test`/`go vet`, `tests/preflight`, `tests/observability`, `tests/skills`, `tests/hooks`, `./scripts/render-validate.sh local-k3s`, `./scripts/render-validate.sh gke`, `./scripts/render-validate.sh vps-k3s`, and `python3 scripts/check-arch-doc-sync.py --worktree`) per `specs/012-gke-platform-migration/quickstart.md`

---

## Dependencies & Execution Order

### Phase Dependencies

- **Phase 1 (Setup — `T001`–`T003`)**: No dependencies — can start immediately.
- **Phase 2 (Foundational — `T004`–`T009`)**: Depends on `T001` — **BLOCKS** all user stories so Constitution `v2.3.0`, GKE governance admission rules, and `vault-registry-proxy` retirement are in place before full environment renders run.
- **Phase 3 (User Story 1 — `T010`–`T022`, P1 MVP)**: Depends on Phase 2 completion. Establishes `clusters/gke/` overlay, portable `NetworkPolicy`, `Traefik` `hostPort`, and multi-node preflight math.
- **Phase 4 (User Story 2 — `T023`–`T027`, P1)**: Depends on Phase 2 completion; `T026` (`clusters/gke/observability/`) and `T027` (`scripts/platform-preflight.py`) integrate cleanly after `T020`–`T021` so `scripts/platform-preflight.py` edits do not conflict.
- **Phase 5 (User Story 3 — `T028`–`T032`, P2)**: Depends on Phase 2 completion and is file-disjoint from Phases 3–4 (`deploy/platform/identity/keycloak-deployment.yaml`, `scripts/db-migrate-dump.sh`, `scripts/vault-configure-k8s-auth.sh`, `scripts/cutover-bundle.sh`, `docs/operations-runbook.md`), so `T028`–`T030` can run in parallel with Phases 3–4.
- **Phase 6 (Polish — `T033`–`T035`)**: Depends on Phases 3, 4, and 5 completion.

### User Story Dependencies

```text
Phase 1: Setup (T001–T003)
    │
    ▼
Phase 2: Foundational (T004–T009)
    │
    ├──────────────────────────────────────────┬──────────────────────────────────────────┐
    ▼                                          ▼                                          ▼
Phase 3: US1 (P1 MVP, T010–T022)       Phase 4: US2 (P1, T023–T027)               Phase 5: US3 (P2, T028–T032)
GKE Overlay, Portable NetPol & 1/n     Fluent Bit /var/lib/fluent-bit-state       Full-State Bundle, K8s Auth Re-Bind
    │                                          │                                  & gcpckms Auto-Unseal
    └──────────────────────────────────────────┴──────────────────────────────────────────┘
                                               │
                                               ▼
                                 Phase 6: Polish & Gate 1 Suite (T033–T035)
```

### Parallel Opportunities

- **Setup (`Phase 1`)**: `T002` (`deploy/gcp/gar-cleanup-policy.json`) and `T003` (`deploy/gcp/cloudbuild.yaml`) run in parallel after `T001`.
- **Foundational (`Phase 2`)**: `T005`, `T006`, `T007`, `T008`, and `T009` touch completely separate subsystems and can run in parallel alongside `T004`.
- **User Story 1 (`Phase 3`)**: Contract tests `T010` and `T011` run in parallel; manifest tasks `T012`, `T013`, `T014`, `T015`, `T016`, `T017`, `T018`, and `T019` touch disjoint files and can all be authored in parallel before `T020` (`clusters/gke/layers.yaml`) and `T021`–`T022` (`platform-preflight.py` / `render-invariants.py`).
- **User Story 2 (`Phase 4`)**: `T023` runs first, then `T024`, `T025`, and `T026` touch separate files before `T027`.
- **User Story 3 (`Phase 5`)**: `T028` (`keycloak-deployment.yaml`), `T029` (`db-migrate-dump.sh`), and `T030` (`vault-configure-k8s-auth.sh`) can all run in parallel before `T031` (`cutover-bundle.sh`) and `T032` (`operations-runbook.md`).

---

## Parallel Example: User Story 1 (MVP)

```bash
# 1. Author US1 preflight & portable NetworkPolicy contract tests in parallel:
Task: "T010 [P] [US1] Add unit tests for ClusterCapacityProfile in tests/preflight/test_platform_preflight.py"
Task: "T011 [P] [US1] Update outbound HTTPS egress contract tests in tests/preflight/test_translation_egress.py, tests/preflight/test_ai_gateway_contract.py, and tests/preflight/test_notify_gateway_contract.py"

# 2. Author disjoint base & GKE overlay manifests in parallel:
Task: "T012 [P] [US1] Replace CiliumNetworkPolicy in deploy/projects/pn/boundary/translation-egress.yaml"
Task: "T013 [P] [US1] Replace CiliumNetworkPolicies in deploy/platform/vault/network-policy.yaml"
Task: "T016 [P] [US1] Create non-root Traefik v3.3.4 edge ingress manifests in deploy/platform/traefik/"
Task: "T017 [P] [US1] Create GKE Design 1 ClusterCapacityProfile in clusters/gke/capacity.yaml"
Task: "T018 [P] [US1] Create GKE flux-system bootstrap manifests in clusters/gke/flux-system/"
Task: "T019 [P] [US1] Create GKE tenant project Flux Kustomization overlays in clusters/gke/projects/"
```

---

## Implementation Strategy

### MVP First (User Story 1 — Offline GKE Overlay & Preflight Parity)

1. Complete **Phase 1: Setup** (`T001`–`T003`) and **Phase 2: Foundational** (`T004`–`T009`).
2. Complete **Phase 3: User Story 1** (`T010`–`T022`).
3. **STOP and VALIDATE**: Run `./scripts/render-validate.sh local-k3s`, `./scripts/render-validate.sh gke`, and `python3 -m unittest discover tests/preflight` to prove 100% offline render, portable `NetworkPolicy`, and 2-node $1/n$ capacity parity (`SC-001`).

### Incremental Delivery Across Gates 1–4

1. **Gate 1 (Offline Code & Preflight — Phases 1–6)**: Complete `US1` + `US2` + `US3` + `Polish` with zero cloud resources touched; pass every test and render gate across `local-k3s`, `gke`, and `vps-k3s`.
2. **Gate 2 (Local Collector Outage Drill)**: Rehearse `US2` (`/var/lib/fluent-bit-state` crash + sink-outage recovery) on `local-k3s` (`SC-002`).
3. **Gate 3 (Local Full-State Restore Drill)**: Rehearse `US3` (`cutover-bundle.sh export/verify/restore --target local-k3s` + `smoke-auth-chain.sh`) using a read-only snapshot from `vps-k3s` (`SC-003`).
4. **Gate 4 (Single-Shot GKE Cutover & `vps-k3s` Repurposing)**: Provision Zonal GKE Standard in `nacfson-pipeline` (`asia-northeast3-a`), restore bundle, verify HTTPS + OIDC (`SC-004`), flip DNS, migrate `openbao-0` to `gcpckms`, and immediately shrink/recreate `vps-k3s` into OCI Kafka Broker VM 1 for Spec 013.
