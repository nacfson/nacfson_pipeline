# Project Log Viewer & Log History Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

> **Status (2026-10-24):** Implemented and merged to `main` as #80. The checkboxes below are the original task list; they were not rewritten as done. Two facts differ from this plan: the platform reservation was not measured, so `1595`/`5312` and `1920m`/`5312` stay until a live measurement is committed; VPS access on this workstation is `scripts/open-observe.sh` (API on `127.0.0.1:16443`), not `ssh -L 6443`.

**Goal:** Store 30 days (cap 8 GiB) of `proj-*` container logs in VictoriaLogs, collected by a Fluent Bit DaemonSet, searchable in a browser through a private `kubectl port-forward`.

**Architecture:** A new `observability` Flux layer (`deploy/platform/observability/`) holds a VictoriaLogs StatefulSet (10 GiB PVC on `local-path`, 8 GiB cap) and a Fluent Bit DaemonSet that tails `/var/log/pods/proj-*_*/*/*.log` through one read-only `hostPath`. The tail bookmark is a 1 GiB PVC, not an `emptyDir`. The namespace label is `pod-security.kubernetes.io/enforce: privileged`. No project manifest changes. Shipped access is `scripts/open-observe.sh` on the VPS and `scripts/open-victorialogs-ui.sh` on `local-k3s`. Live logs stay with Headlamp/`kubectl logs` and the existing `operator-readonly` account.

**Tech Stack:** Kubernetes manifests + Kustomize + FluxCD, Fluent Bit, VictoriaLogs, Python `unittest` (manifest/preflight tests), Bash.

**Spec:** [`docs/superpowers/specs/2026-10-22-log-viewer-design.md`](../specs/2026-10-22-log-viewer-design.md) (approved 2026-10-22, amended 2026-10-24). Read it before starting; it carries the reasons behind every value below.

## Global Constraints

- Third-party images use pinned exact version tags, never `:latest`; the builder looks up current stable tags and verifies an `arm64` manifest exists for both images.
- No Kubernetes `Secret`, no `IngressRoute`/`Ingress`, no `NodePort`/`LoadBalancer`: every `observability` Service is `ClusterIP`.
- The only `hostPath` is Fluent Bit's `/var/log/pods`, `readOnly: true`; Fluent Bit has `automountServiceAccountToken: false` and no Kubernetes API access.
- Stored scope is project namespaces only: Fluent Bit tail path is exactly `/var/log/pods/proj-*_*/*/*.log`.
- Retention: `-retentionPeriod=30d` and `-retention.maxDiskSpaceUsageBytes=8589934592` (8 GiB) on a `10Gi` PVC. 8 GiB is `8 * 1024**3`.
- Tail bookmark: one `ReadWriteOnce` PVC `fluent-bit-state`, `1Gi`, mounted at `/state`. Not an `emptyDir`. Not a second `hostPath`. One node only.
- Namespace PSA: `pod-security.kubernetes.io/enforce`, `warn`, and `audit` are `privileged`. Do not copy `deploy/platform/vault/namespace.yaml`; that file is `restricted` and would reject this collector.
- Standard record fields: `project`, `pod`, `container`, `node`, `pod_uid`, `time`, `message`; VictoriaLogs stream fields are only `project,container,pod`.
- Log schema: `contracts/CONTRACT_LOG_SCHEMA.md` is the GitOps source for the envelope. The Fluent Bit ConfigMap must match its field names and stream fields. Do not add a schema-registry pod. Do not parse `message`.
- Initial resources: Fluent Bit `cpu 50m` request / `memory 64Mi`; VictoriaLogs `cpu 100m` request / `memory 384Mi` (memory request == limit for both). Reservation delta must be exactly `+150m` CPU and `+448Mi` memory.
- Same manifests on `local-k3s` and `vps-k3s`. Flux layer `observability` `dependsOn: cni, governance` only (not `vault`).
- Any change under `deploy/`, `clusters/`, `scripts/platform-preflight.py`, `.specify/memory/constitution.md` also updates `docs/platform-architecture.html` in the same change (AGENTS.md rule 8).
- Commits, pushes, and pull requests use `git` and `gh` on a feature branch. `gh-mcp` is gone. Never push to `main`. Never merge a feature branch into local `main`; the `main_PR` ruleset rejects it.
- Handbook co-update: a commit that stages a path under `deploy/`, `clusters/`, `contracts/`, `specs/`, `.specify/memory/constitution.md`, `scripts/platform-preflight.py`, `ARCHITECTURE.md`, or `DEPENDENCIES.md` must also stage `docs/platform-architecture.html`. Run `python3 scripts/check-arch-doc-sync.py --staged` and require exit 0 before the commit. The checker reads the working tree: the HTML must contain `v` plus the constitution version, every Flux layer name from `clusters/vps-k3s/layers.yaml` as `'<name>'` or `selectDagLayer('<name>')`, and the budget digits hardcoded in `check_static_invariants()` (today `1595` and `5312`). Split the handbook edits below so each commit still passes.
- Rule 4 applies to platform containers: every container sets cpu and memory requests and limits, or preflight returns `REJECTED_UNBOUNDED`. Reservation counts CPU requests and memory limits.
- Budget source: HTML `1595` is stale against `DEPENDENCIES.md` `1920m`. Do not add 150 to 1595. Task 4 measures `platformReservations`; Tasks 6 and 7 write that measured pair.

## Review Focus

1. A namespace that merely contains `proj` or a platform namespace (`identity`, `vault`, `kube-system`, `observability`) must **not** be collected → Task 3 glob test.
2. Pod/container/uid extraction from the log path for pod names with dots and hyphens, and uids with hyphens → Task 3 regex test.
3. Disk cap must stay below the PVC size, or a heavy project fills the volume → Task 2 test.
4. VictoriaLogs down or slow: Fluent Bit memory must stay bounded below its container limit and it must not restart in a loop → Task 3 config test (`Mem_Buf_Limit`, `Retry_Limit`).
5. A Fluent Bit restart must keep its tail position on PVC `fluent-bit-state` and must not add a second `hostPath`. Exactly one `hostPath`, read-only `/var/log/pods` → Task 3 test.
6. A future edit must not expose the UI: no `IngressRoute`/`Ingress`/non-ClusterIP in the render → Task 2 and Task 3 tests.

Files changed by pre-existing uncommitted work in the main checkout (`contracts/CONTRACT_AUTH_DOOR.md`, `docs/platform-architecture.html`, `specs/001-project-platform/contracts/session-revocation.md`, and the untracked `kubectl` binary) are **not** part of this plan; work in a separate worktree so they are never staged.

---

### Task 1: Preflight recognizes `observability` as a platform namespace

**Files:**
- Modify: `scripts/platform-preflight.py:24` (`PLATFORM_NAMESPACES`)
- Test: `tests/preflight/test_platform_preflight.py` (append `test_25_observability_platform_hostpath_accepted`)

**Interfaces:**
- Produces: `observability` ∈ `PLATFORM_NAMESPACES`; platform `hostPath`/root pods in that namespace are exempt from project Rule 2/3.

- [ ] **Step 1: Use the existing worktree.** `feat/project-log-history` and `../nacfson_pipeline_log-history` already exist. Do not run `git worktree add -b`. All following work happens in that directory. If the spec and this plan still have uncommitted decision edits, commit those two files first with `git_commit` (message `docs(observability): record 2026-10-24 log history decisions`).

- [ ] **Step 2: Write the failing test** `test_25_observability_platform_hostpath_accepted(self)` using `self._run_yaml(text)`. Input: one `DaemonSet` named `fluent-bit` in namespace `observability` with a read-only `hostPath` volume `/var/log/pods`, `runAsUser: 0`, and cpu and memory requests and limits all set. Assert `rc == 0` and `data["validationStatus"] == "PASSED"`.

- [ ] **Step 3: Run it and see it fail.** `python3 -m unittest tests.preflight.test_platform_preflight -k test_25 -v` → FAIL with `REJECTED_ISOLATION` (treated as a project namespace).

- [ ] **Step 4: Add `"observability"` to `PLATFORM_NAMESPACES` in `scripts/platform-preflight.py`.**

- [ ] **Step 5: Run the preflight unit tests.** `python3 -m unittest discover tests/preflight` → all pass, including `test_05_project_pod_hostpath_rejected`.

- [ ] **Step 6: Touch the handbook and commit.** Add one sentence to `docs/platform-architecture.html`: `observability` is a platform namespace for the preflight exemption only. Do not add a Flux DAG entry, and do not change `1595`, `5312`, or `v2.2.0`. Run `python3 scripts/check-arch-doc-sync.py --staged` → exit 0. Commit with `git_commit` (files: `scripts/platform-preflight.py`, the test, `docs/platform-architecture.html`; message `feat(preflight): treat observability as a platform namespace`).

---

### Task 2: Namespace, VictoriaLogs store, and default-deny network policy

**Files:**
- Create: `deploy/platform/observability/kustomization.yaml`, `namespace.yaml`, `victorialogs-statefulset.yaml`, `victorialogs-service.yaml`, `network-policy.yaml`
- Create: `tests/observability/test_observability_manifests.py` (shared render helper + this task's tests)

**Interfaces:**
- Produces: Service `victorialogs.observability.svc.cluster.local:9428` (ClusterIP); pod label `app.kubernetes.io/name: victorialogs`; Fluent Bit pods will be labeled `app.kubernetes.io/name: fluent-bit` (Task 3).
- Test helper: `render() -> list[dict]` runs `kubectl kustomize deploy/platform/observability` and returns the parsed documents (used by Task 3 tests).

- [ ] **Step 1: Write failing tests** in `tests/observability/test_observability_manifests.py` (class `TestVictoriaLogsStore`):
  - `test_namespace_is_privileged`: Namespace `observability` exists, annotation `kustomize.toolkit.fluxcd.io/prune: disabled`, and labels `pod-security.kubernetes.io/enforce`, `warn`, and `audit` are all `privileged`. No label value is `restricted` or `baseline`.
  - `test_retention_flags_and_disk_cap_below_pvc`: StatefulSet `victorialogs` args contain `-retentionPeriod=30d` and `-retention.maxDiskSpaceUsageBytes=8589934592`; `volumeClaimTemplates[0]` requests `10Gi`; assert `8589934592 == 8 * 1024**3` and `8589934592 < 10 * 1024**3`.
  - `test_victorialogs_hardening_and_resources`: `automountServiceAccountToken` is `False`; container `allowPrivilegeEscalation: false`, `capabilities.drop == ["ALL"]`, `runAsNonRoot: true`; requests `cpu 100m`, `memory 384Mi`; limits `cpu 500m`, `memory 384Mi`.
  - `test_service_clusterip_9428_and_nothing_public`: Service `victorialogs` is `ClusterIP` on port 9428; render has no `Secret`, `IngressRoute`, `Ingress`, and every `Service` is `ClusterIP`.
  - `test_network_policies`: a policy with `podSelector: {}`, `policyTypes == ["Ingress","Egress"]`, empty `ingress`, and egress only DNS (UDP/TCP 53 to `kube-system` / `k8s-app: kube-dns`, copied from `vault-default-deny`); no `0.0.0.0/0`. A policy selecting `victorialogs` whose only ingress source is pods labeled `app.kubernetes.io/name: fluent-bit` on TCP 9428.
  - `test_image_is_pinned`: the VictoriaLogs image tag is not `latest` and contains a version number.

- [ ] **Step 2: Run to see failure.** `python3 -m unittest tests.observability.test_observability_manifests -v` → FAIL (directory missing).

- [ ] **Step 3: Write the manifests.** Copy the DNS egress rule from `vault-default-deny` and the StatefulSet hardening from `deploy/platform/vault/openbao-statefulset.yaml`. Do not copy `deploy/platform/vault/namespace.yaml`: its `enforce: restricted` label rejects this collector. Namespace labels are `app.kubernetes.io/part-of: platform-observability` plus `pod-security.kubernetes.io/enforce: privileged`, `warn: privileged`, and `audit: privileged`. `kustomization.yaml` sets `namespace: observability`. VictoriaLogs runs the single-node binary with `-storageDataPath` on the PVC mount, `-httpListenAddr=:9428`, the two retention flags above, a non-root `runAsUser`/`fsGroup` the builder verifies against the image, `readinessProbe`/`livenessProbe` on `/health`, requests `cpu 100m` / `memory 384Mi`, limits `cpu 500m` / `memory 384Mi`. Omit `storageClassName`. If SSH to the VPS works, record `df -B1 /` before writing the PVC. Stop and report if VPS free space is under 10 GiB. Do not check `local-k3s` disk, and do not change the 10 GiB PVC or the 8 GiB cap.

- [ ] **Step 4: Verify the flag names against the pinned image.** Run the image with `--help` (or read its docs for the pinned tag) and confirm `-retentionPeriod`, `-retention.maxDiskSpaceUsageBytes` and `-storageDataPath` exist; adjust the flags and tests together if a name differs.

- [ ] **Step 5: Run the tests.** Same command → PASS.

- [ ] **Step 6: Commit** (`git_commit`, files: `deploy/platform/observability/*`, `tests/observability/test_observability_manifests.py`, `docs/platform-architecture.html`). Handbook touch: name VictoriaLogs. Do not add the Flux DAG entry yet, and do not change `1595`, `5312`, or `v2.2.0`. `python3 scripts/check-arch-doc-sync.py --staged` → exit 0. Message: `feat(observability): add VictoriaLogs store and default-deny policy`.

---

### Task 3: Fluent Bit collector

**Files:**
- Create: `deploy/platform/observability/fluent-bit-configmap.yaml`, `fluent-bit-serviceaccount.yaml`, `fluent-bit-daemonset.yaml`, `fluent-bit-state-pvc.yaml`
- Modify: `deploy/platform/observability/kustomization.yaml`, `deploy/platform/observability/network-policy.yaml` (add Fluent Bit egress)
- Modify: `tests/observability/test_observability_manifests.py` (class `TestFluentBitCollector` + reservation test)

**Interfaces:**
- Consumes: `render()` from Task 2; Service `victorialogs.observability.svc.cluster.local:9428`; `observability` ∈ `PLATFORM_NAMESPACES` (Task 1).
- Produces: DaemonSet `fluent-bit` (pods labeled `app.kubernetes.io/name: fluent-bit`) in `observability`.

- [ ] **Step 1: Write failing tests** (class `TestFluentBitCollector`):
  - `test_only_one_readonly_hostpath_and_hardening`: exactly one volume has `hostPath`, path `/var/log/pods`, and its mount is `readOnly: true`; exactly one other volume is a `persistentVolumeClaim` named `fluent-bit-state`; every remaining volume is `emptyDir` or `configMap`; `serviceAccountName: fluent-bit`; `automountServiceAccountToken: false` on both the pod and that ServiceAccount; container `runAsUser: 0`, `allowPrivilegeEscalation: false`, `readOnlyRootFilesystem: true`, `capabilities.drop == ["ALL"]`, and no `capabilities.add`; env `NODE_NAME` from `fieldRef: spec.nodeName`; requests `cpu 50m`, `memory 64Mi`; limits `cpu 50m`, `memory 64Mi`; no `secret`/`secretKeyRef` anywhere. PVC `fluent-bit-state` is `ReadWriteOnce` and requests `1Gi`.
  - `test_tail_glob_matches_only_project_namespaces`: read `Path` from the ConfigMap's `fluent-bit.conf`; with `fnmatch`, these match: `/var/log/pods/proj-pn_backend-7d9f_3f2c-aa/backend/0.log`, `/var/log/pods/proj-health_web-1_uid-1/web/0.log`; these do **not**: paths starting `identity_`, `vault_`, `platform_`, `kube-system_`, `flux-system_`, `cert-manager_`, `governance_`, `observability_`, and `projx_x_uid/c/0.log`.
  - `test_path_regex_extracts_fields`: read the `pod_path` regex from `parsers.conf`. It must use Fluent Bit named groups `(?<project>...)`. Before `re.compile`, translate `(?<` to `(?P<`. Do not write `(?P<` into the ConfigMap. For `/var/log/pods/proj-pn_openbao-0.my-set_3f2c-aa-bb/backend/12.log` assert `project=proj-pn`, `pod=openbao-0.my-set`, `pod_uid=3f2c-aa-bb`, `container=backend`.
  - `test_output_to_victorialogs_and_bounded_memory`: config has `Host victorialogs.observability.svc.cluster.local`, `Port 9428`, `URI /insert/jsonline?_stream_fields=project,container,pod&_msg_field=message&_time_field=time`, `Format json_lines`, `json_date_format iso8601`, `json_date_key time`; the tail input has `Mem_Buf_Limit` and `DB /state/tail.db`; that path is on the `fluent-bit-state` PVC mount, not an `emptyDir`; the output has `Retry_Limit`; `Log_Level warn`.
  - `test_fluent_bit_network_policy`: a policy selecting `fluent-bit` with egress only to DNS (UDP/TCP 53 in `kube-system`) and to pods `victorialogs` on TCP 9428; no `0.0.0.0/0`.
  - `test_platform_reservation_delta`: pipe `render()` through `scripts/platform-preflight.py` with `tests/preflight/fixtures/valid_capacity.yaml` (same pattern as `test_24` in `tests/preflight/test_platform_preflight.py`); assert `validationStatus == "PASSED"`, `platformReservations.cpuMillicores == 350` and `memoryMib == 960` (200m/512Mi system reserved + 150m/448Mi).

- [ ] **Step 2: Run to see failures.** Same unittest command → the new tests FAIL.

- [ ] **Step 3: Write the manifests.** ConfigMap `fluent-bit-config` with `fluent-bit.conf` and `parsers.conf`; the DaemonSet mounts it at `/fluent-bit/etc/`, the hostPath at `/var/log/pods` (read-only), and PVC `fluent-bit-state` at `/state`. `serviceAccountName: fluent-bit`. Stay on `runAsUser: 0`; do not switch to `DAC_READ_SEARCH` in this version (the test pins root). Limits `cpu 50m` and `memory 64Mi` (Rule 4). Pin from the spec:
  - Input: `Name tail`, `Path /var/log/pods/proj-*_*/*/*.log`, `Path_Key filepath`, `Parser cri`, `Read_from_Head On`, `DB /state/tail.db`, `Mem_Buf_Limit 5MB`, `Skip_Long_Lines On`. A multiline parser for stack traces is optional and must not change this path, the `pod_path` regex, or the stream fields. `Read_from_Head` applies to new files. The PVC bookmark stops a collector restart from re-reading files already sent.
  - A `parser` filter on key `filepath` with parser `pod_path`, regex `^/var/log/pods/(?<project>[^_]+)_(?<pod>[^_]+)_(?<pod_uid>[^/]+)/(?<container>[^/]+)/[^/]+\.log$`, `Reserve_Data On`.
  - A `record_modifier` adding `node ${NODE_NAME}`, and a `modify` renaming `log` → `message`; drop `filepath`, `stream`, `logtag`.
  - Output: `Name http` with the exact values in the test above; `Retry_Limit` set to a finite number (builder's choice).
  - The builder also defines the `cri` parser (containerd format) in `parsers.conf` because mounting a custom ConfigMap replaces the image's default file.
  Add a `fluent-bit` policy whose egress is only DNS (same kube-dns rule) and `victorialogs` TCP 9428; list the new files in `kustomization.yaml`.

- [ ] **Step 4: Optional dry run.** If Docker access works, run `fluent-bit --dry-run -c fluent-bit.conf` from the pinned image against the ConfigMap files; expected output `configuration test is successful`. If not, rely on Task 5's live `--verify` (record this in the PR description).

- [ ] **Step 5: Run all observability and preflight tests.** `python3 -m unittest discover tests/observability && python3 -m unittest discover tests/preflight` → PASS.

- [ ] **Step 6: Commit** (`git_commit`, files: the new/changed manifests, the test file, `docs/platform-architecture.html`). Handbook touch: name Fluent Bit. Do not add the Flux DAG entry yet, and do not change `1595`, `5312`, or `v2.2.0`. `python3 scripts/check-arch-doc-sync.py --staged` → exit 0. Message: `feat(observability): add Fluent Bit DaemonSet for project logs`.

---

### Task 4: Flux layer wiring, admission policy and budget measurement

**Files:**
- Modify: `clusters/local-k3s/layers.yaml`, `clusters/vps-k3s/layers.yaml` (append layer `observability`)
- Modify: `deploy/platform/governance/manual-change-policy.yaml` (add `observability` to the `gitops-managed-namespaces` binding values)
- Modify: `tests/observability/test_observability_manifests.py` (class `TestLayerWiring`)

**Interfaces:**
- Consumes: manifests path `./deploy/platform/observability`.
- Produces: Flux layer name `observability` in both environments; recorded baseline and new platform reservation numbers (used by Tasks 6 and 7).

- [ ] **Step 1: Record the baseline.** Before editing, run `./scripts/render-validate.sh vps-k3s | tail -n 1` and the preflight (commands in `specs/010-cilium-hubble-observability/quickstart.md` §1); note `platformReservations` for both environments (expected `1920m` / `5312Mi` per `DEPENDENCIES.md`). Write the numbers in the PR description draft.

- [ ] **Step 2: Write failing tests** (class `TestLayerWiring`), for both `clusters/local-k3s/layers.yaml` and `clusters/vps-k3s/layers.yaml`:
  - `test_observability_layer`: a `Kustomization` named `observability` in `flux-system` with `path: ./deploy/platform/observability`, `wait: true`, `prune: true`, `dependsOn` exactly `[cni, governance]`, and `healthChecks` for `DaemonSet/fluent-bit` and `StatefulSet/victorialogs` in `observability`.
  - `test_manual_change_policy_lists_observability`: `observability` ∈ the `gitops-managed-namespaces` binding's `namespaceSelector` values.

- [ ] **Step 3: Run to see failure.** Same unittest command → FAIL.

- [ ] **Step 4: Add the layer** to both `layers.yaml` files following the `vault` layer's shape (`interval: 5m`, `retryInterval: 1m`, `timeout: 5m`, `sourceRef: flux-system`), and add `observability` to the namespace list in `manual-change-policy.yaml` (render-invariants Invariant 8 requires this set to equal `{default, flux-system}` ∪ every Namespace under `deploy/`). In the same commit, add `'observability'` to `DAG_LAYERS` in `docs/platform-architecture.html` (quoted, so the sync check sees it). Leave `1595`, `5312`, and `v2.2.0` unchanged.

- [ ] **Step 5: Run the full render gate.** `./scripts/render-validate.sh local-k3s` and `./scripts/render-validate.sh vps-k3s` → both exit 0 with preflight `PASSED` and all render invariants passing. Re-run the preflight and write down `platformReservations` for both environments. The delta from the Task 4 Step 1 baseline must be exactly `+150m` CPU and `+448Mi` memory. Those measured totals, not `1595+150`, are the numbers Tasks 6 and 7 write. If `flux`/`kubeconform` are not installed locally, install them or run this step in CI and record the evidence.

- [ ] **Step 6: Commit** (`git_commit`, files: both `layers.yaml`, `manual-change-policy.yaml`, the test file, `docs/platform-architecture.html`). `python3 scripts/check-arch-doc-sync.py --staged` → exit 0 (the new layer name is in the HTML; budget digits are still `1595`/`5312`). Message: `feat(flux): add observability layer and managed-namespace policy entry`.

---

### Task 5: Access helper script

**Files:**
- Create: `scripts/open-victorialogs-ui.sh` (executable)
- Create: `tests/observability/test_open_victorialogs_ui.py`

**Interfaces:**
- Produces: `scripts/open-victorialogs-ui.sh [--port <local-port>] [--verify]`; default local port `9428` bound to `127.0.0.1`; target `svc/victorialogs` port 9428 in `observability`.

- [ ] **Step 1: Write failing tests** (`unittest`, run the script with `subprocess`): `bash -n` passes; `--help` exits 0 and prints `--verify`; an unknown argument exits 2; `--port` with no value exits 2; with `PATH` set so `kubectl` is not found, the script exits 1 with `kubectl is required`; if `shellcheck` is installed, it reports no findings (otherwise `skipTest`).

- [ ] **Step 2: Run to see failure.** `python3 -m unittest tests.observability.test_open_victorialogs_ui -v` → FAIL (script missing).

- [ ] **Step 3: Write the script** modeled on `scripts/open-hubble-ui.sh` (argument parsing, `kubectl` check, rollout wait, `exec kubectl -n observability port-forward --address 127.0.0.1 svc/victorialogs "${PORT}:9428"`, URL printout, SSH hint). `--verify` runs these live checks and prints `✓`/`FAIL` for each:
  1. `rollout status` for `daemonset/fluent-bit` and `statefulset/victorialogs`.
  2. Services in `observability` are `ClusterIP`, and no `IngressRoute` references `victorialogs` or `fluent-bit`.
  3. The tunnel serves HTTP 200 at `/select/vmui/`.
  4. `POST /select/logsql/query` with `query=*` and `limit=50`. An empty body passes. If any JSON line is returned, every line must contain `"project":"proj-`.
  5. Fluent Bit logs contain no repeated `[error]` output lines in the last 5 minutes.

- [ ] **Step 4: Run the tests.** Same command → PASS.

- [ ] **Step 5: Commit** (`git_commit`, files: the script and test; message `feat(observability): add VictoriaLogs tunnel and verify script`).

---

### Task 6: Constitution note, sync gate and architecture handbook

**Files:**
- Modify: `.specify/memory/constitution.md` (clarifying note after Amendment 2.2.0; footer to `**Version**: 2.2.1 | **Ratified**: 2026-10-01 | **Last Amended**: 2026-10-22`)
- Modify: `scripts/check-arch-doc-sync.py` (budget constants at lines ~142–146)
- Modify: `docs/platform-architecture.html`

**Interfaces:**
- Produces: a handbook that references `v2.2.1`, layer `observability`, the new budget, Fluent Bit and VictoriaLogs.

- Consumes: the measured `platformReservations` from Task 4. Write those digits into the checker and the HTML. `1745`/`5760` is valid only if the measured baseline was `1595`/`5312`. `DEPENDENCIES.md` currently says `1920m`/`5312Mi`, so expect about `2070`/`5760` and use the measurement if it differs.

- [ ] **Step 1: Write the failing check.** Set the constants in `check_static_invariants()` of `scripts/check-arch-doc-sync.py`, and the matching error text, to the measured Task 4 totals. Run `python3 scripts/check-arch-doc-sync.py --worktree` → FAIL (budget digits and `v2.2.1` missing from the HTML; the `'observability'` layer is already present from Task 4).

- [ ] **Step 2: Add the constitution note** under a heading `## Clarification 2.2.1` with the exact sentence: "The log collector in the platform namespace `observability` may mount `/var/log/pods` read-only through `hostPath`. It is not a project workload, so Principle II and the `hostPath` rejection rule apply to project namespaces and manifests only." Include a one-line rationale and impact (Security: one read-only node mount for one platform pod; Resource: counted in the platform reservation; Portability: unchanged). Maintainer-approval line: the operator accepted this note on 2026-10-22 with the design. Bump the footer to `**Version**: 2.2.1 | **Ratified**: 2026-10-01 | **Last Amended**: 2026-10-22`.

- [ ] **Step 3: Update `docs/platform-architecture.html`:** keep the Task 4 `observability` DAG entry; add Fluent Bit and VictoriaLogs to the topology and the OSS catalog (pinned versions); add a log-collection flow; replace the budget calculator constants and the `calc-plat` text with the measured Task 4 totals; replace the Constitution version with `v2.2.1`.

- [ ] **Step 4: Run the sync gate.** `python3 scripts/check-arch-doc-sync.py --worktree` → exit 0.

- [ ] **Step 5: Commit** (`git_commit`, files: `.specify/memory/constitution.md`, `scripts/check-arch-doc-sync.py`, `docs/platform-architecture.html`). `python3 scripts/check-arch-doc-sync.py --staged` → exit 0. Message: `docs(constitution): clarify observability hostPath and sync architecture handbook`.

---

### Task 7: Runbook, architecture docs, dependencies and index

**Files:**
- Modify: `docs/operations-runbook.md` (new section `## 15. Project Log History (VictoriaLogs)`; add a Headlamp live-logs paragraph to §6)
- Modify: `ARCHITECTURE.md` (new §3.7), `DEPENDENCIES.md` (§4 budget table numbers + pinned versions of Fluent Bit and VictoriaLogs), `INDEX.md` (new paths, including `contracts/CONTRACT_LOG_SCHEMA.md`), `contracts/CONTRACT_ISOLATION_BUDGET.md` (line 22: `hostPath` ban is for project namespaces; platform `observability` collector is the documented exception), `specs/004-flux-gitops-reconciliation/contracts/platform-preflight.md` (Rule 2 note and `PLATFORM_NAMESPACES` now includes `observability`). `contracts/CONTRACT_LOG_SCHEMA.md` already exists; do not invent a second schema file.

**Interfaces:**
- Consumes: measured `platformReservations` from Task 4. Per-project slice is `(4000 - measured CPU millicores)` and `(24576 - measured memory MiB)` for `vps-k3s`, `(8192 - measured memory MiB)` for `local-k3s`. If the measured reservation is `2070`/`5760`, the slices are `1930m`/`18816 MiB` and `1930m`/`2432 MiB`. Do not compute slices from `1745`.

- [ ] **Step 1: Write the runbook section.** Cover: starting the tunnel (`./scripts/open-victorialogs-ui.sh`, SSH forward for `vps-k3s`), the browser URL, example LogsQL (`{project="proj-pn"}`, time range, `container`/`pod` filters), `--verify`, the 30-day / 8 GiB limit and silent drop, the tail PVC (survives a collector restart, not node-disk loss), the **accepted risks** (disk loss, residual volume fill, viewer down with node, local-k3s disk not checked, only `proj-*` stored; platform logs live-only), and the first step up (off-node copy). In §6 add: desktop Headlamp loading the kubeconfig from `./scripts/operator-kubeconfig.sh`, and `kubectl logs --previous`.

- [ ] **Step 2: Update the other docs** listed above with the values from the spec and the measured numbers. In `ARCHITECTURE.md` §3.7 include a Mermaid flow `project stdout → containerd file → Fluent Bit → VictoriaLogs → port-forward UI`.

- [ ] **Step 3: Verify.** `python3 scripts/check-arch-doc-sync.py --worktree` → exit 0; `grep -rn "1920m" DEPENDENCIES.md` shows no stale numbers; every new relative link resolves (`ls` each path).

- [ ] **Step 4: Commit** (`git_commit`, the files above plus `docs/platform-architecture.html`). Handbook touch in this commit: one sentence pointing at the new runbook section (the flow and budget digits are already in the HTML from Task 6). `python3 scripts/check-arch-doc-sync.py --staged` → exit 0. Message: `docs(observability): runbook, architecture, dependencies and index`.

---

### Task 8: Full verification, review and pull request

**Files:** none new.

- [ ] **Step 1: Run every gate from `AGENTS.md` §5** in the worktree: gateway and vault-proxies Go tests/vet, `python3 -m unittest discover tests/preflight`, `python3 -m unittest discover tests/observability`, both `./scripts/render-validate.sh` runs, `python3 scripts/check-arch-doc-sync.py --worktree`, `python3 -m unittest discover tests/review_cycle`, `python3 scripts/review-cycle.py verify --scope staged`, `python3 scripts/review-cycle.py classify-risk --scope branch`, `python3 scripts/review-cycle.py eval`, `./scripts/rollback-change.sh --dry-run HEAD`. Expected: all exit 0. Record any gate that cannot run locally (for example missing `flux`, `kubeconform`, `shellcheck`) in the PR description.

- [ ] **Step 2: Live check (needs a cluster).** After Flux reconciles, run `./scripts/open-victorialogs-ui.sh --verify` against `vps-k3s`; expected `ALL CHECKS PASSED`, including an empty store. Run it against `local-k3s` too. If a local PVC is pending because the disk was not checked, record that and do not change the sizes. Also run `./scripts/open-hubble-ui.sh --verify`. That script greps `grafana|prometheus|loki` only, so `fluent-bit-*` and `victorialogs-*` do not fail it. Confirm `observability` has `pod-security.kubernetes.io/enforce: privileged`. `git diff --name-only main...HEAD` must not include `deploy/projects/`.

- [ ] **Step 3: Push and open the PR** with `git_push` (branch `feat/project-log-history`) then `pr_create` (title `feat(observability): project log history with Fluent Bit and VictoriaLogs`; body: summary, the spec link, measured budget numbers, accepted risks, and gates that could not run locally). Do not merge or push to `main`.

- [ ] **Step 4: Clean up the main checkout.** Tell the operator that the design doc staged in the main checkout (`AM`) is now committed on the branch, so `git restore --staged docs/superpowers/specs/2026-10-22-log-viewer-design.md` there avoids a duplicate.
