---

description: "Task list for Project Log Contract v3 and Enforcement"
---

# Tasks: Project Log Contract v3 and Enforcement

**Input**: Design documents from `specs/011-project-log-contract/`

**Prerequisites**: [plan.md](plan.md), [spec.md](spec.md), [research.md](research.md), [data-model.md](data-model.md), [contracts/](contracts/), [quickstart.md](quickstart.md)

**Tests**: Test tasks are included. The spec requires them:

- FR-012: "A test MUST enforce this order".
- FR-017: "A test MUST fail if they differ".
- FR-023: "A test MUST fail if the checker and the contract text disagree".
- SC-003, SC-004, SC-004a, and SC-009 define test sets.

**Organization**: Tasks are grouped by user story, so each story can be implemented and tested on its own. The PR slices in [plan.md § Delivery Order](plan.md#delivery-order-pr-slices) are listed under each phase.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: can run in parallel (different files, no dependency on an incomplete task)
- **[Story]**: the user story the task belongs to (US1–US4)

## Path Conventions

These paths are relative to the repository root, `nacfson_pipeline/`. Tasks in other repositories give the repository name first, for example `Project_PN: backend/internal/logx/logx.go`.

## Global rules (apply to every task)

- **Branches:** never push to `main` or merge locally. Land every change through a PR from a feature branch (AGENTS.md).
- **Handbook:** any PR that touches `deploy/`, `clusters/`, `services/`, `contracts/`, `specs/`, `scripts/platform-preflight.py`, `ARCHITECTURE.md`, or `DEPENDENCIES.md` also updates `docs/platform-architecture.html` (AGENTS.md rule 8; checked by `python3 scripts/check-arch-doc-sync.py --worktree`).
- **Go dependencies:** Go code in `services/error-pipeline` uses the Go 1.22 standard library only.
- **SSH helpers:** do not run `scripts/open-observe.sh` or any other SSH helper without the user's explicit allowance (AGENTS.md rule 9).

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Package layout, CI hooks, and test folders. These are part of PR 1.

- [x] T001 Set up the validator package layout inside `services/error-pipeline/` (R-1): create empty package directories `services/error-pipeline/internal/conform/` and `services/error-pipeline/cmd/logcheck/`
- [x] T002 [P] Verify and ensure `.github/workflows/gitops.yaml` runs `go test ./...` and `go vet ./...` in `services/error-pipeline/`
- [x] T003 [P] Add a `lua-contract` job to `.github/workflows/lint.yaml`: install `luajit` (`sudo apt-get install -y luajit`), then run `luajit tests/observability/lua/run_log_contract_tests.lua`
- [x] T004 [P] Create the test folders `tests/observability/lua/`, `tests/observability/fixtures/cri/`, and `tests/preflight/fixtures/log_contract/`, each with a short `README.md` stating what the folder holds

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: The shared validator, redaction kinds, shared test vectors, and contract v3 §2–§6. Every story depends on these. They are part of PR 1.

**⚠️ CRITICAL**: no user story work can begin until this phase is complete.

- [x] T005 [P] Implement `services/error-pipeline/internal/conform/contract_test.go`: a contract-drift test parsing the Markdown tables in `specs/011-project-log-contract/contracts/log-line-v3.md` (and `contracts/CONTRACT_LOG_SCHEMA.md` §2–§5) for required keys, reserved keys, levels, and crash signatures, following the pattern of `classify/contract_test.go` (R-2).
- [x] T006 [P] Update `services/error-pipeline/internal/redact` to use marker `[REDACTED:<kind>]` where `<kind>` is one of `bearer`, `jwt`, `google-key`, `sk-key`, or `secret-field` (R-3).
- [x] T007 [P] Write `services/error-pipeline/internal/redact/testdata/vectors.json`: at least 15 `{input, output}` pairs. Cover:
  - each kind;
  - two secrets in one string;
  - a secret inside an escaped JSON string;
  - a secret inside a multi-line stack trace;
  - `token_count` over-redaction, which is accepted;
  - three non-secrets that must stay unchanged.

  The marker format is `[REDACTED:<kind>]`.
- [x] T007a Add narrow exact-string allowlist entries to `.gitleaks.toml` (matching repo style, R-4) so the secret scan accepts fake secrets in test data:
  - Every fake secret in test data MUST be obviously fake:
    - JWTs end in `.invalidsig` (the same pattern as the existing allowlist entry);
    - keys use repeated filler, e.g. `AIza` + `FAKE…` and `sk-FAKE…`.
  - Verify with a local `gitleaks detect` run, and confirm that a real-looking key outside testdata is still flagged.
- [x] T008 Implement `services/error-pipeline/internal/conform/types.go`: define `Violation`, `Class`, and `Options` types per [data-model.md §7](data-model.md#7-conformance-violation).
- [x] T009 Implement `services/error-pipeline/internal/redact/redact.go` extensions: `Redact(s string) (string, []string)` returning the redacted text and the kinds hit, keeping `String(s string) string` backward-compatible.
- [x] T010 Implement `services/error-pipeline/internal/conform/conform.go`:
  - `Classify(line string) Class` returns `json`, `crash:<runtime>`, `nginx-error:<level>`, or `other`, reusing `internal/classify`.
  - `Check(line string, opts Options) []Violation` checks FR-002 to FR-008 and FR-010.
  - `Violation` is `{Section, Kind, Text}`. Its kinds are exactly the list in [data-model.md §7](data-model.md#7-conformance-violation). `Text` is redacted with `Redact`.
  - `Options` is `{ExpectVersion, ExpectService string; Format string}`.
  - Dynamic-key detection (`dynamic-key`) flags a key that contains a run of 4 or more digits, or a 32-hex-character segment.
  - `stacktrace-too-long` flags an `exception.stacktrace` longer than 8192 bytes (FR-004, operator decision D6).
- [x] T011 [P] Write `services/error-pipeline/internal/redact/redact_test.go` vector test. It runs every vector in `testdata/vectors.json` through `Redact` and requires an exact match.
- [x] T012 [P] Write `services/error-pipeline/internal/conform/conform_test.go`, a table test with one passing and one failing case for each rule in [contracts/log-line-v3.md § Rules](contracts/log-line-v3.md#rules). It also covers:
  - nested-equals-flat;
  - case-insensitive levels and aliases;
  - `v1.4.2`, `v0.0.0-3f2a9c1`, and plain `v0.0.0` accepted in a line (pinning a plain `v0.0.0` is refused by the gates, not by the line check);
  - an `exception.stacktrace` of 8192 bytes passes, and one of 8193 bytes reports `stacktrace-too-long`;
  - a UUID `trace_id` rejected;
  - `*fmt.wrapError` rejected.
- [x] T013 Rewrite `contracts/CONTRACT_LOG_SCHEMA.md` for PR 1 (R-9):
  - Rewrites §2–§6 to v3 from [contracts/log-line-v3.md](contracts/log-line-v3.md); keeps §1 platform fields as v2 until PR 3 (collector PR).
  - Migration table §6 has five rows, all `pending` (R-7):
    - `proj-pn` / `backend`
    - `proj-pn` / `frontend`
    - `proj-health` / `backend`
    - `proj-health` / `frontend`
    - `proj-blog` / `web`
  - "Rejected" list: replace "Parsing `message` in Fluent Bit" with the v3 rule, and add "A separate dead-letter file".
- [x] T014 Update `contracts/README.md` to point at v3: the summary row for `CONTRACT_LOG_SCHEMA.md` and its enforcement column (preflight, `logcheck`, the collector tests)
- [x] T015 Write `tests/observability/test_log_contract_schema.py` (R-2). It fails when any of these drift:
  - the tables in `contracts/CONTRACT_LOG_SCHEMA.md` §2–§5;
  - the `REQUIRED` and `RESERVED` tables in `deploy/platform/observability/fluent-bit/log_contract.lua` (skipped with a clear message until T019 creates the file).

  This is FR-023.

**Checkpoint**: the validator builds, its tests pass, and contract v3 §2–§6 is written. User stories can start.

---

## Phase 2b: Detector Dual-Read (PR 2)

**Purpose**: Allow the detector to read both v2 and v3 fields before the collector switches (R-5). Built on merge, then its digest is pinned in `deploy/platform/observability/error-detector.yaml` *before* PR 3 lands.

- [x] T015a [P] [US4] In `services/error-pipeline/internal/vlogs`: support dual read for v2 (`project`, `pod`, `container`, `node`, `message`) and v3 (`k8s.namespace.name`, `k8s.pod.name`, `k8s.container.name`, `k8s.node.name`, `_msg`, plus parsed fields).
- [x] T015b [P] [US4] In `services/error-pipeline/internal/classify`: accept pre-parsed records and map them directly into events without re-parsing `message`.
- [x] T015c [US4] In `services/error-pipeline/internal/detector`: update VictoriaLogs query to `{project=~"proj-.*"} OR {k8s.namespace.name=~"proj-.*"}`.
- [x] T015d [US4] Write unit tests in `services/error-pipeline` verifying that records in either v2 format or v3 format produce identical event classification.
- [ ] T015e [US4] Build image on PR 2 merge, and pin new digest in `deploy/platform/observability/error-detector.yaml` before PR 3 begins.

---

## Phase 3: User Story 1 - Trace a failure to its request and release from one search (Priority: P1) 🎯 MVP

**Goal**: Every stored `proj-*` record carries the seven platform fields, parsed project fields, the program's time as `_time`, and joined long lines and crashes. One `trace_id` search returns one request across containers.

**Independent Test**: [quickstart.md §3](quickstart.md#3-collector-end-to-end-local-docker-no-cluster). With the fixtures, the conforming, split, and panic records are stored with all platform fields, `_time` equals the printed `time`, and a 40 KB line comes back as one JSON record.

**PR slice**: PR 3 (Collector v3), shipped together with US2 and the collector half of US4. Requires PR 2 deployed.

### Tests for User Story 1

- [x] T016 [P] [US1] Add CRI fixtures to `tests/observability/fixtures/cri/`. Each is a CRI-format file (`<RFC3339Nano> <stdout|stderr> <P|F> <text>`) under a path laid out like `proj-test_app-0_<uid>/<container>/0.log`:
  - `conforming.log`: two JSON lines with the same `trace_id`, one per container (`api`, `web`)
  - `split.log`: one 40 KB JSON line split into `P,P,F` pieces
  - `panic.log`: a Go panic with a 12-line trace
  - `bad-time.log`: JSON whose `time` is 10 minutes from the CRI timestamp
- [x] T017 [P] [US1] Write `tests/observability/lua/run_log_contract_tests.lua`, a plain LuaJIT runner with no framework. It loads `deploy/platform/observability/fluent-bit/log_contract.lua`, calls `process(tag, ts, record)` with table records, and asserts:
  - the platform fields are set from the path keys and `NODE_NAME`;
  - `msg` falls back to `log` for non-JSON lines;
  - `_time` is the project `time` when it is within ±5 min, and otherwise `observed_time`, keeping the project `time`;
  - FR-018c: an `exception.stacktrace` of 20 KiB, and a grouped Go panic message of 20 KiB, are cut to ≤ 8192 bytes at a line break, keep their first line, and end with `…[truncated N bytes]` with the correct N. A trace of exactly 8192 bytes is unchanged.

  It exits non-zero on any failure.
- [x] T018 [US1] Rewrite `tests/observability/test_observability_manifests.py` and `contracts/CONTRACT_LOG_SCHEMA.md` §1 for v3 (R-9):
  - In `contracts/CONTRACT_LOG_SCHEMA.md` §1: update platform fields table to list `k8s.namespace.name`, `k8s.pod.name`, `k8s.pod.uid`, `k8s.container.name`, `k8s.node.name`, `log.iostream`, `observed_time`, and stream fields `k8s.namespace.name,k8s.pod.name,k8s.container.name`.
  - The output URI asserts `_stream_fields=k8s.namespace.name,k8s.pod.name,k8s.container.name&_msg_field=msg&_time_field=_time`.
  - The first `[FILTER]` is `multiline`, with `multiline.parser go, python, java` and `buffer on`.
  - `tail` has `multiline.parser cri` and `Tag proj.*`.
  - The Lua filter comes after the JSON `parser` filter.
  - Every `[FILTER]` uses `Match proj.*`. The only exception is the final `modify Remove _route`, added in T054, which uses `Match *`. This is analysis H1: re-emitted `quarantine.*` records must never pass through the Lua stage again.
  - The JSON `parser` filter sets `Preserve_Key On`.
  - There is no `grep` or `throttle` filter.
  - Resources stay `50m`/`64Mi`, with requests equal to limits.
  - Replace `test_record_fields_match_log_schema_contract` with a check against the v3 §1 table.

### Implementation for User Story 1

- [x] T019 [US1] Create `deploy/platform/observability/fluent-bit/log_contract.lua`, implementing stages 1–4 of [research.md R4](research.md#r4-one-lua-stage-for-the-contract-specific-logic) in this order:
  1. Strip the reserved keys (listed in a `RESERVED` table).
  2. Set `k8s.*` from the temporary path keys, `log.iostream` from `stream`, `k8s.node.name` from `os.getenv("NODE_NAME")`, and `observed_time` (RFC 3339, nanoseconds) from the record timestamp. Delete the temporary keys and `stream`. Keep `log` for now: the quarantine path (T053) needs the original text, and the main path deletes it at the end.
  3. If there is no `msg`, copy the raw `log` into `msg`.
  4. Choose `_time` per FR-015 and return code `1` with the new timestamp.
  5. Cut `exception.stacktrace`, or the message of a grouped crash record, to 8 KiB per FR-018c: keep the top, cut at the last line break inside the limit, append `…[truncated N bytes]`.

  Also define `REQUIRED = {"time","level","msg","service.name","service.version"}` for later stages.
- [x] T020 [US1] Move the Fluent Bit config from the inline `deploy/platform/observability/fluent-bit-configmap.yaml` to files under `deploy/platform/observability/fluent-bit/`:
  - `fluent-bit.conf`
  - `parsers.conf`
  - `log_contract.lua`
  - `legacy-containers.txt`, containing the five `pending` containers (R-7): `proj-pn/backend`, `proj-pn/frontend`, `proj-health/backend`, `proj-health/frontend`, `proj-blog/web`

  Generate the ConfigMap `fluent-bit-config` with a `configMapGenerator` in `deploy/platform/observability/kustomization.yaml`, and delete `fluent-bit-configmap.yaml`.
- [x] T021 [US1] Write the stages from [contracts/collector-pipeline.md](contracts/collector-pipeline.md) into `deploy/platform/observability/fluent-bit/fluent-bit.conf`:
  - stage 1: `tail` with `Tag proj.*`, `Path_Key filepath`, and `multiline.parser cri`;
  - stage 2: `multiline` filter, first, `Match proj.*`;
  - stage 3: path parser on `filepath` to temporary keys, `Match proj.*`;
  - stage 4: `parser` with `json` on `log`, `Reserve_Data On`, `Preserve_Key On`, `Match proj.*`;
  - stage 5: `lua` with `script /fluent-bit/etc/log_contract.lua`, `call process`, `Match proj.*`;
  - stage 8a: one `http` output, `Match proj.*`, headers `AccountID 0` and `ProjectID 0`, the v3 URI, `json_date_key _time`.

  Keep `DB /state/tail.db`, `Mem_Buf_Limit 5MB`, and `Skip_Long_Lines On`, and set `emitter_mem_buf_limit 4M` on the multiline filter.
- [x] T022 [US1] Add the `json` parser to `deploy/platform/observability/fluent-bit/parsers.conf`, and keep the path regex parser (`^/var/log/pods/(?<ns>[^_]+)_(?<pod>[^_]+)_(?<uid>[^/]+)/(?<container>[^/]+)/[^/]+\.log$`). Remove the old custom `cri` regex parser; `multiline.parser cri` replaces it.
- [x] T023 [US1] Point `deploy/platform/observability/fluent-bit-daemonset.yaml` at the generated ConfigMap. The mount path stays `/fluent-bit/etc/`. The `hostPath`, security context, and resources stay unchanged.
- [x] T024 [P] [US1] Update `scripts/open-victorialogs-ui.sh`:
  - the verify step 4 checks `"k8s.namespace.name":"proj-` instead of `"project":"proj-`;
  - the printed example query is `{k8s.namespace.name="proj-pn"}`.

  Update `tests/observability/test_open_victorialogs_ui.py` to match.
- [x] T025 [P] [US1] Update the example LogsQL line in `scripts/open-observe.sh` (currently `{project="proj-pn"}`) to `{k8s.namespace.name="proj-pn"}`, and the matching assertion in `tests/observability/test_open_observe.py`
- [x] T026 [US1] Update `docs/operations-runbook.md` §15:
  - the v3 field names;
  - the example queries from [contracts/collector-pipeline.md § Readers](contracts/collector-pipeline.md#readers);
  - a note that records stored before the switch keep the v2 names until they age out (30 days).
- [x] T027 [US1] Run the [quickstart.md §3](quickstart.md#3-collector-end-to-end-local-docker-no-cluster) local Docker end-to-end test with the US1 fixtures, and record the results in `specs/011-project-log-contract/checklists/collector-e2e.md`: which queries were run, and pass/fail per expected row

**Checkpoint**: US1 works with the local fixtures. The collector is ready to merge with US2.

---

## Phase 4: User Story 2 - A project cannot print a false identity (Priority: P1)

**Goal**: A project-supplied value of any reserved field never reaches the store.

**Independent Test**: the `forged.log` fixture, which prints every reserved field with false values, is stored with the platform's values only (SC-003).

**PR slice**: PR 2, together with US1.

### Tests for User Story 2

- [x] T028 [P] [US2] Add `tests/observability/fixtures/cri/forged.log`, a JSON line from `proj-test/api` that sets every reserved key from [data-model.md §1](data-model.md#1-printed-log-line-project-owned) to a false value (for example `"k8s.namespace.name":"proj-health"`)
- [x] T029 [P] [US2] Add a forged-record case to `tests/observability/lua/run_log_contract_tests.lua`. After `process`, every reserved key must hold the platform-derived value or be absent (`_time`, `_msg`, `_stream`, `_stream_id`, `_route` are absent), and no project value survives.
- [x] T030 [P] [US2] Add `test_reserved_keys_stripped_before_platform_fields` to `tests/observability/test_observability_manifests.py`. It parses `log_contract.lua` and asserts that the call that strips `RESERVED` comes before the first assignment to a `k8s.` key inside `process`. This covers FR-012: "A test MUST enforce this order".

### Implementation for User Story 2

- [x] T031 [US2] Make sure stage 1 of `deploy/platform/observability/fluent-bit/log_contract.lua` removes every `RESERVED` key, including nested forms that VictoriaLogs would flatten to the same name (a table value under the key `k8s`). Then re-run T027 with `forged.log` and add the result to `specs/011-project-log-contract/checklists/collector-e2e.md`.

**Checkpoint**: US1 and US2 pass together. PR 2's collector core is complete.

---

## Phase 5: User Story 3 - A non-conforming project cannot reach the cluster (Priority: P2)

**Goal**: Preflight, `logcheck`, and the image-pin helper reject non-conforming manifests, releases, and images.

**Independent Test**: [quickstart.md §2 and §5](quickstart.md#2-manifest-gates). Each fixture fails with its rule ID, each blocked image-pin case is refused, and conforming cases pass.

**PR slices**:

- PR 1: `logcheck` CLI and image (T032–T035).
- PR 4: preflight, version-label PR check, and workload annotations (T036–T043a).
- PR 5: image-pin gate and helper (T044–T047).

### logcheck CLI (Gate 2)

- [x] T032 [P] [US3] Implement `services/error-pipeline/cmd/logcheck/main.go` per [contracts/enforcement-gates.md § Gate 2](contracts/enforcement-gates.md#gate-2-logcheck-project-repositories-every-release):
  - flags `--format json|nginx`, `--expect-version`, `--expect-service`;
  - reads stdin;
  - prints one JSON violation per line (`{"line":N,"section":"§2","kind":"…","text":"…redacted…"}`), then a summary line;
  - exits `0` with no violations, `1` with violations, `2` on a usage or IO error.
- [x] T033 [P] [US3] Write `services/error-pipeline/cmd/logcheck/main_test.go`. It runs `main` against fixed inputs for exit codes 0, 1, and 2, and checks that secrets in reported `text` are redacted.
- [x] T034 [US3] Add `services/error-pipeline/Dockerfile.logcheck`: a multi-stage build with `FROM --platform=$BUILDPLATFORM`, cross-compiling with `CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH`, a non-root user, and `ENTRYPOINT ["/logcheck"]`. Both base images MUST be explicit, never a moving name (constitution Principle I):
  - builder: an exact Go patch tag, e.g. `golang:1.22.<patch>-alpine`. Look up the newest 1.22 patch at implementation time. `golang:1.22` and `golang:1.22-alpine` are not allowed.
  - final: `gcr.io/distroless/static@sha256:<digest>`, resolved with `skopeo inspect docker://gcr.io/distroless/static:nonroot`. Record the tag it was taken from in a comment.

  A test in `tests/observability/test_log_contract_schema.py` fails if any `FROM` line in `services/error-pipeline/Dockerfile.logcheck` has neither an exact `x.y.z` tag nor an `@sha256:` digest.
- [x] T035 [US3] Add a `logcheck` entry to the matrix in `.github/workflows/build-images.yaml` (`context: services/error-pipeline`, `dockerfile: services/error-pipeline/Dockerfile.logcheck`, `image_name: logcheck`), and add `services/error-pipeline/**` to `on.push.paths`.

### Preflight manifest gate (Gate 1)

- [x] T036 [P] [US3] Create preflight fixtures under `tests/preflight/fixtures/log_contract/`. Each is a minimal rendered `proj-test` Deployment YAML, plus a matching `legacy-containers.txt` where needed:
  - `missing-annotation`
  - `bad-annotation-value`
  - `log-file-env` (`LOG_FILE=/var/log/app.log`)
  - `log-format-text` (`LOG_FORMAT=text`)
  - `undeclared-log-volume` (mount `/var/log/app`)
  - `declared-data-path-ok` (mount `/var/log/project-pn` with `data-paths` and `data-reason`)
  - `data-reason-missing`
  - `version-label-missing`
  - `version-label-malformed` (`1.4.2` without `v`)
  - `v0-plain` (`v0.0.0` with no commit, rejected in both clusters)
  - `v0-commit-in-vps-ok` (`v0.0.0-3f2a9c1`, passes in `vps-k3s`; operator decision D1)
  - `legacy-list-drift`
  - `done-regressed`
  - `new-container-legacy`
- [x] T037 [US3] Write `tests/preflight/test_log_contract.py`. There is one test per fixture, asserting the rule ID from [contracts/enforcement-gates.md § Gate 1](contracts/enforcement-gates.md#gate-1-preflight-check_log_contract-this-repository-every-pr):
  - `log-declaration-missing`
  - `log-file-output`
  - `log-volume-undeclared`
  - `log-data-reason-missing`
  - `log-migration-row-missing`
  - `log-migration-regressed`
  - `log-legacy-list-drift`
  - `log-version-label` (also for `v0-plain`)

  `declared-data-path-ok` and `v0-commit-in-vps-ok` must pass.
- [x] T038 [US3] Implement `check_log_contract(project_docs, platform_docs, environment, repo_root)` in `scripts/platform-preflight.py` with the eight rules from Gate 1. Details:
  - Annotation values must be exactly `v3/json`, `v3/nginx`, or `v2/legacy`.
  - Env rule regex: `(?i)^LOG_(FILE|PATH|DIR)$`. Arg regex: `(?i)--?log[-_]?(file|path|dir)`.
  - The volume rule looks for a path segment `log` or `logs`.
  - The version regex is `^v\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`. A plain `v0.0.0` is rejected in both clusters. `v0.0.0-<commit>` is allowed in both (D1).
  - Migration rows are read from the §6 table of `contracts/CONTRACT_LOG_SCHEMA.md`.
  - The legacy list is read from the rendered `fluent-bit-config` ConfigMap data key `legacy-containers.txt`.
  - A `pending` container declared `v2/legacy` produces a warning, not a violation.
- [x] T039 [US3] Wire `check_log_contract` into `main()` in `scripts/platform-preflight.py`, next to the `check_ai_gateway_contract` call (around line 1205), and append its findings to `violations` in the existing `{rule, object, detail}` shape
- [x] T040 [P] [US3] Add these annotations to the pod template of `deploy/projects/pn/workloads/backend-deployment.yaml`, plus the label `app.kubernetes.io/version`, set to the tag that produced the current digest (resolve it with `skopeo inspect` on the pinned digest, reading `org.opencontainers.image.version`; if that label is missing, use the git tag of the image's revision, and record which one was used in the PR body):
  - `logs.nacfson.cloud/backend: "v2/legacy"`
  - `logs.nacfson.cloud/backend.data-paths: "/var/log/project-pn"`
  - `logs.nacfson.cloud/backend.data-reason: "PN learning log (LEARNING_LOG_DIR), project-owned data, not a log stream"`
- [x] T041 [P] [US3] Add `logs.nacfson.cloud/frontend: "v2/legacy"` and the `app.kubernetes.io/version` label (same rule as T040) to `deploy/projects/pn/workloads/frontend-deployment.yaml`
- [x] T042 [P] [US3] Add `logs.nacfson.cloud/<container>: "v2/legacy"` and the `app.kubernetes.io/version` label (same rule as T040) to `deploy/projects/health/workloads/backend-deployment.yaml` and `deploy/projects/health/workloads/frontend-deployment.yaml`. First check each container's mounts for undeclared `/log` paths, and declare or remove them.
- [x] T042a [P] [US3] Add `logs.nacfson.cloud/web: "v2/legacy"` and the `app.kubernetes.io/version` label (same rule as T040) to `deploy/projects/blog/workloads/web-deployment.yaml` (R-7).
- [x] T043 [US3] Run `./scripts/render-validate.sh local-k3s` and `./scripts/render-validate.sh vps-k3s`. Confirm the Flux Kustomization `patches` are applied before preflight (research R8 follow-up). If they aren't, add rendering of `clusters/<env>/projects/*.yaml` patches to `scripts/render-validate.sh`, and record the finding in `specs/011-project-log-contract/research.md` R8.

### Version-label PR check (Gate 1b)

- [x] T043a [US3] Add `scripts/check-version-labels.py` and a CI step that runs it on every PR touching `deploy/projects/**` or `clusters/*/projects/**`, per [contracts/enforcement-gates.md § Gate 1b](contracts/enforcement-gates.md#gate-1b-version-label-check-this-repository-every-pr-that-changes-a-project-workload) (FR-024a):
  - render each cluster's `proj-*` workloads (reuse the `render-validate.sh` render);
  - for each container, read the pinned digest's `org.opencontainers.image.version` with `skopeo inspect` and compare it with the pod-template label `app.kubernetes.io/version`;
  - report `log-version-label-mismatch` or `log-version-image-unreadable`, and exit non-zero;
  - never call the Kubernetes API.

  Tests in `tests/preflight/test_check_version_labels.py`, with `skopeo` mocked: matching label passes, mismatched label fails and names both versions, unreadable image fails. First confirm that this repository's CI can read the GHCR project images (see deferred-decisions §3).

### Image-pin gate (Gate 3)

- [x] T044 [P] [US3] Extend `tests/skills/test_update_project_images.py`, mocking `skopeo` and `gh`:
  - version label missing → blocked;
  - version label malformed → blocked;
  - plain `v0.0.0` → blocked;
  - `v0.0.0-3f2a9c1` whose image revision starts with a different commit → blocked;
  - `log-contract` check run missing → blocked, for a release **and** for an untagged image;
  - `log-contract` check run `failure` → blocked;
  - passing release case → the digest is replaced and the pod-template `app.kubernetes.io/version` is set in the same file;
  - passing untagged case (`v0.0.0-3f2a9c1`, matching revision, `log-contract` success) → pinned into the base workloads.
- [x] T044a [US3] Update `diff_check()` in `scripts/image-pin.py` to allow the pod-template `app.kubernetes.io/version` label line alongside the image digest line (R-8). Test in `tests/scripts/test_image_pin.py`.
- [x] T045 [US3] In `validate_image()` in `.agents/skills/update-project-images/scripts/update_project_images.py`:
  - read `org.opencontainers.image.version` and validate it against `^v\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$` (blocking);
  - block a plain `v0.0.0`;
  - for `v0.0.0-<commit>`, block unless `<commit>` is a prefix of `org.opencontainers.image.revision`;
  - for **every** version, call `gh api repos/<owner>/<repo>/commits/<revision>/check-runs` and require a run named `log-contract` with `conclusion == "success"` (blocking);
  - add `version` and `log_contract_run_url` to `evidence`.
- [x] T046 [US3] In `replace_image()` (or a new helper next to it) in `.agents/skills/update-project-images/scripts/update_project_images.py`, set `spec.template.metadata.labels["app.kubernetes.io/version"]` to the validated version in the same edit as the digest, preserving the file's formatting
- [x] T047 [US3] Update `.agents/skills/update-project-images/SKILL.md`:
  - step 4 lists the version-label, untagged-commit, and `log-contract` checks;
  - the PR body table adds the version and the `log-contract` run URL;
  - red flags add "pinning a plain `v0.0.0`" and "pinning an image whose `log-contract` check didn't pass";
  - a note that an untagged image (`v0.0.0-<commit>`) is pinned exactly like a release, in either cluster (operator decision D1). No manual `local-k3s` path is needed.

**Checkpoint**: all three gates reject the fixture set and pass conforming inputs. The current PN and health workloads pass with warnings as `v2/legacy`.

---

## Phase 6: User Story 4 - Drift in production is reported (Priority: P3)

**Goal**:

- The collector quarantines non-conforming lines from v3 containers into tenant `0:1` and redacts secrets before storage.
- The error-pipeline design is updated, so that workstream 1 reports violations and redaction markers.

**Independent Test**: [quickstart.md §3](quickstart.md#3-collector-end-to-end-local-docker-no-cluster) rows 2–4. Exactly the text line and the missing-`service.version` line are in tenant `0:1`; the panic is in `0:0`; `_msg:"eyJ"` returns 0 results in both tenants; the legacy-container text line is in `0:0`. The alerting half of the test runs when workstream 1 lands.

**PR slices**:

- PR 3: collector routing and redaction, T048–T055.
- PR 6: violation reporting and quarantine alerts (detector implementation and docs), T056a–T059.

### Tests for User Story 4

- [x] T048 [P] [US4] Add CRI fixtures to `tests/observability/fixtures/cri/`:
  - `text-v3.log`: a plain text line from `proj-test/api`
  - `missing-version.log`: JSON without `service.version`
  - `secret.log`: JSON with `"http.request.header.authorization":"Bearer eyJhbGciOiJIUzI1NiJ9.e30.sig"` and the same token inside an `exception.stacktrace`
  - `legacy-text.log`: plain text from a container listed in a test `legacy-containers.txt`
  - `nginx-error.log`: a native `[error]` line
- [x] T049 [P] [US4] Add routing cases to `tests/observability/lua/run_log_contract_tests.lua`, following the rules in [data-model.md § Routing decision](data-model.md#routing-decision-can-the-collector-refine-it-fr-018a):
  - `text-v3` → `_route=quarantine`
  - `missing-version` → `quarantine`
  - a grouped Go panic → `main`
  - an nginx `[error]` line → `main`
  - a legacy-listed container → `main`
  - a conforming line → `main`
  - broken JSON (`{"level":"error",`) → `quarantine`
  - the quarantined `text-v3` record holds exactly these keys: `msg`, the five `k8s.*` keys, `log.iostream`, `observed_time`, `_route`. Its `msg` equals the original text, and its timestamp equals the CRI timestamp.
  - the quarantined `missing-version` record has **no** parsed keys: no `level` and no `service.name` fields. Its `msg` is the original JSON text.
  - the quarantined `secret.log` variant (as plain text) has `msg` containing `[REDACTED:bearer]` and not `eyJ`.
- [x] T050 [P] [US4] Add a redaction-parity case to `tests/observability/lua/run_log_contract_tests.lua`. It loads `services/error-pipeline/internal/redact/testdata/vectors.json` and requires the Lua redaction to reproduce every `output` exactly. It also asserts that every kind in `services/error-pipeline/internal/redact` exists in the Lua `PATTERNS` table (FR-017 parity, R-3).
- [x] T051 [P] [US4] Add to `tests/observability/test_observability_manifests.py`:
  - exactly two `http` outputs, `Match proj.*` with `ProjectID 0` and `Match quarantine.*` with `ProjectID 1`, both `AccountID 0`, and the same URI;
  - a `rewrite_tag` filter on `$_route ^quarantine$` with `keep false` and `Match proj.*`, after the Lua filter;
  - a `modify Remove _route` filter after it, with `Match *`. This is the only filter allowed to match `quarantine.*`.

### Implementation for User Story 4

- [x] T052 [US4] Add stage 5 to `deploy/platform/observability/fluent-bit/log_contract.lua`. It applies the `PATTERNS` table (matching `services/error-pipeline/internal/redact`, R-3) to every string value and to the raw `msg`, and applies the `secret-field` rule to values whose key matches `password|passwd|secret|token|api[_-]?key|authorization|cookie`, case-insensitively. The marker is `[REDACTED:<kind>]`.
- [x] T053 [US4] Add stage 6 to `deploy/platform/observability/fluent-bit/log_contract.lua`:
  - at start, load `/fluent-bit/etc/legacy-containers.txt` into a set;
  - decide "can it be refined?" **before** stages 3–4, per [data-model.md § Routing decision](data-model.md#routing-decision-can-the-collector-refine-it-fr-018a). A grouped crash is recognised by the `crashSignatures` prefixes on the first line; an nginx native error is recognised by `^\d{4}/\d\d/\d\d \d\d:\d\d:\d\d \[(error|crit|alert|emerg)\]`.
  - **main:** continue with stages 3–5, delete `log`, set `_route="main"`.
  - **quarantine:** build a new record containing only:
    - `msg` = the redacted original `log` text;
    - the five `k8s.*` fields;
    - `log.iostream`;
    - `observed_time`;
    - `_route="quarantine"`.

    Every parsed project key is dropped. Return code `2` (record changed, timestamp kept), so the CRI time stays the main time.
- [x] T054 [US4] Add to `deploy/platform/observability/fluent-bit/fluent-bit.conf`:
  - stage 6: a `rewrite_tag` filter (`Match proj.*`, `Rule $_route ^quarantine$ quarantine.$TAG false`, `Emitter_Mem_Buf_Limit 4M`);
  - stage 7: `modify Remove _route` with `Match *`, the only filter that sees `quarantine.*`;
  - stage 8b: a second `http` output with `Match quarantine.*` and headers `AccountID 0`, `ProjectID 1`.
- [x] T055 [US4] Re-run the [quickstart.md §3](quickstart.md#3-collector-end-to-end-local-docker-no-cluster) end-to-end test with every fixture, querying tenant `0:0` and tenant `0:1` (`-H 'ProjectID: 1'`). Record the results in `specs/011-project-log-contract/checklists/collector-e2e.md`. Also record whether the vmui at v1.53.0 can select a tenant (research R1 follow-up), and document how to read the quarantine in `docs/operations-runbook.md` §15.
- [x] T056a [US4] Implement quarantine reader in `services/error-pipeline/internal/vlogs`: query tenant `AccountID 0, ProjectID 1` (`_stream_id, _time, msg, k8s.namespace.name, k8s.container.name`) (R-6).
- [x] T056b [US4] Implement immediate quarantine alert in `services/error-pipeline/internal/detector`: when a quarantined line from a container declared `v3/*` is observed, alert immediately on the first occurrence with redacted line; throttle subsequent alerts for that container to at most once per hour with the count (FR-025, Q1).
- [x] T056c [US4] Implement immediate alert in `services/error-pipeline/internal/detector` on stored lines containing `[REDACTED:<kind>]` markers (FR-025). Not rate-limited; names project, container, release, and marker kind without leaking secret.
- [x] T056d [US4] Implement daily violation digest in `services/error-pipeline/internal/detector`: group by project, container, and violation kind (`time-rejected`, `dynamic-key`, missing fields, quarantined line count).
- [x] T056e [US4] Add unit and integration tests in `services/error-pipeline` for quarantine reader/alerting, marker alerts, and daily digest.
- [x] T056 [P] [US4] Update `docs/superpowers/specs/2026-10-29-error-pipeline-design.md` per [research.md R13](research.md#r13-what-the-error-pipeline-must-change-dependency-not-built-here):
  - v3 field names;
  - parsed fields instead of `unpack_json`;
  - quarantine counts from tenant `0:1`;
  - immediate alerts on the `[REDACTED:` marker;
  - an immediate quarantine alert per FR-025: the first quarantined line from a `v3/*` container alerts at once, then at most one alert per container per hour with the count since the last one;
  - no runtime version-mismatch check and no Kubernetes access for the detector (operator decision D19; the label is verified on the PR by Gate 1b);
  - classification through the `internal/conform` package;
  - violation digest grouped by project, container, and kind.
- [x] T057 [P] [US4] Rewrite `docs/superpowers/plans/2026-10-29-ws1-error-detector-and-notify-proxy.md` to match T056. It imports `services/error-pipeline/internal/conform`; its queries use `k8s.*` fields; it adds the quarantine reader, the marker alert, and the quarantine alert (with a test: first line alerts, a second line within the hour doesn't, the next alert carries the count). It adds no RBAC and no version-mismatch check (D19).
- [x] T058 [P] [US4] Rewrite `docs/superpowers/plans/2026-10-29-ws0-project-log-migration.md` for v3. It follows the R14 patterns:
  - `service.name` and `service.version` on every line;
  - `exception.type` from the innermost unwrapped error, not `%T` of the wrapper;
  - `traceparent` propagation;
  - nginx `$request_id` → `trace_id`;
  - a release workflow with OCI version/revision labels and a `log-contract` job;
  - no `k8s.*` printed by projects.
- [x] T059 [US4] Record in `contracts/CONTRACT_LOG_SCHEMA.md` §6 (migration) and §1 that the detector keeps reading legacy `kind` as `event.name` until every row is `done` (FR-028)

**Checkpoint**: the collector routes and redacts correctly with the local fixtures, and the error-pipeline docs are ready for workstream 1.

---

## Phase 7: Project Migrations (FR-026; cross-repository)

**Purpose**: move the four hosted containers from `pending` to `done`. Each project lands in its own repository first, then pins back here. This is PR slice 6+.

**Prerequisites**: Phase 5 (gates) and the `logcheck` image (T035) published.

### Project_PN

- [ ] T060 [P] Project_PN: create `backend/internal/logx/logx.go`:
  - `New(w io.Writer, service, version string) *slog.Logger`, a JSON handler whose `ReplaceAttr` sets `time` in the format `2006-01-02T15:04:05.000Z07:00` (UTC), and which adds `service.name` and `service.version` to every record;
  - `Exception(err error) []any`, which returns `exception.type` of the innermost unwrapped error and `exception.message`.

  Tests go in `backend/internal/logx/logx_test.go`.
- [ ] T061 Project_PN: replace every `slog.NewTextHandler` in:
  - `backend/cmd/api/main.go`
  - `backend/cmd/retranslate-senses/main.go`
  - `backend/internal/words/service.go`
  - `backend/internal/enrich/openai.go`

  with the `logx` default. Add `var version = "v0.0.0"` in `backend/cmd/api/main.go`, set by `-ldflags` (the plain default only survives in a local build outside Git; CI always sets a tag or `v0.0.0-<commit>`). Rename `kind` to `event.name` in the analytics and admin records, and update `docs/analytics-contract.md` and the admin logging design in the same PR.
- [ ] T062 Project_PN: add HTTP middleware in `backend/internal/httpx/trace.go`. It parses `traceparent`, puts `trace_id` (32 lowercase hex) into the request logger, creates one if the header is absent, and emits one access line per request with the four `http.*` keys (`level=error` for status ≥ 500).
- [ ] T063 Project_PN: update the frontend nginx config:
  - `log_format` with `escape=json` and the keys from [contracts/log-line-v3.md](contracts/log-line-v3.md):
    - `time`: `$time_iso8601` with milliseconds from `$msec`;
    - `level`: `map $status`;
    - `trace_id`: `$request_id`;
    - `service.name` = `pn-web`;
    - `service.version` from `ARG SERVICE_VERSION`.
  - `proxy_set_header traceparent "00-$request_id-$tp_parent-01"`, where `$tp_parent` is the first 16 characters of `$request_id`.
  - The error log stays native.
- [ ] T064 Project_PN: update the image build workflow:
  - pass the version via `-ldflags` / `ARG`: the plain tag on a tag build, `v0.0.0-<short commit>` (`${GITHUB_SHA::7}`) on every other build;
  - set the OCI labels `org.opencontainers.image.version` and `org.opencontainers.image.revision`;
  - add a `log-contract` job that runs on **every** image build, tagged or not (D1). It runs the log smoke scenario (startup, one request, one handled error) and pipes the output to `ghcr.io/nacfson/logcheck@sha256:<digest>`, with `--expect-version <the version above>` and `--expect-service`;
  - trim `exception.stacktrace` to 8 KiB in `logx`, keeping the top (D6).

### nacfson_health

- [ ] T065 [P] nacfson_health: apply T060–T062 to `cmd/api` and `cmd/migrate`, with `service.name` = `health-api` / `health-migrate`
- [ ] T066 nacfson_health: apply T063–T064 to the frontend nginx config (`service.name` = `health-web`) and the release workflow

### nacfson_blog

- [ ] T066a [P] nacfson_blog: update Go log handler to JSON with `service.name` = `blog-web`, `service.version`, and request access log; configure release workflow to run `log-contract` on every build (D1, R-7).

### Pin back (this repository, one PR per project via `update-project-images`)

- [ ] T067 Pin the first passing PN release, per role, with the `update-project-images` skill. In the same PR:
  - set `logs.nacfson.cloud/backend` and `logs.nacfson.cloud/frontend` to `v3/json` / `v3/nginx` in `deploy/projects/pn/workloads/*-deployment.yaml`;
  - remove `proj-pn/*` from `deploy/platform/observability/fluent-bit/legacy-containers.txt`;
  - set both PN rows to `done` in `contracts/CONTRACT_LOG_SCHEMA.md` §6.
- [ ] T067a After T067 is deployed, the user runs `./scripts/open-observe.sh` themselves, or explicitly allows it (an SSH helper). Then:
  - trigger one failing PN request;
  - search `trace_id:=<id>`;
  - confirm it returns the frontend and backend lines of that request only (SC-001).

  Record the result in `specs/011-project-log-contract/checklists/collector-e2e.md`.
- [ ] T068 Do the same as T067 for health (`deploy/projects/health/workloads/*-deployment.yaml`, `proj-health/*` entries, health rows)
- [ ] T068a Do the same as T067 for blog (`deploy/projects/blog/workloads/web-deployment.yaml`, `proj-blog/*` entry, blog row) (R-7)
- [ ] T069 After T067, T068, and T068a are merged and every §6 row is `done`, open a follow-up PR that removes the legacy `kind` and v2 read from the detector code and from `contracts/CONTRACT_LOG_SCHEMA.md` (FR-028)

---

## Phase 8: Polish & Cross-Cutting Concerns

- [ ] T070 [P] Update `docs/platform-architecture.html` (AGENTS.md rule 8):
  - the observability topology, with the Lua stage and the two tenants;
  - the log flow, with v3 fields;
  - the OSS catalog entry for `logcheck`;
  - the collector budget, unchanged.

  Then run `python3 scripts/check-arch-doc-sync.py --worktree`. This task is repeated in every PR that touches the rule-8 paths.
- [ ] T071 [P] Update the references:
  - `DEPENDENCIES.md`: the `logcheck` image, the Lua stage, and the tenants;
  - `INDEX.md`: `specs/011-project-log-contract/` and `services/error-pipeline/internal/conform/`;
  - `ARCHITECTURE.md`: the log path diagram.
- [ ] T072 Run the [quickstart.md §4](quickstart.md#4-collector-load-sc-009) load test with `--cpus 0.05 --memory 64m`. Record the CPU, memory, and record counts in `specs/011-project-log-contract/checklists/collector-e2e.md`. If a limit is exceeded, stop and raise it with the user before changing `deploy/platform/observability/fluent-bit-daemonset.yaml` (research R12).
- [ ] T073 Run the full verification set from AGENTS.md §5 and record the results:
  - `(cd services/error-pipeline && go test ./... && go vet ./...)`
  - `python3 -m unittest discover tests/observability`
  - `python3 -m unittest discover tests/preflight`
  - `python3 -m unittest discover -s tests/skills`
  - `luajit tests/observability/lua/run_log_contract_tests.lua`
  - `./scripts/render-validate.sh local-k3s`
  - `./scripts/render-validate.sh vps-k3s`
  - `python3 scripts/check-arch-doc-sync.py --worktree`
- [ ] T074 Before each commit, run the `code-review` skill on the staged diff (AGENTS.md); then commit on a feature branch and open the PR for that slice
- [ ] T075 After PR 3 is deployed: the user runs `./scripts/open-observe.sh` themselves, or explicitly allows it to be run. Check the first two [quickstart.md §6](quickstart.md#6-after-deployment-operator-read-only) rows (fields present, and `--verify` passes), and record them in `specs/011-project-log-contract/checklists/collector-e2e.md`. The `trace_id` row needs migrated projects and runs in T067a.

---

## Dependencies & Execution Order

### Phase dependencies

```
Phase 1 Setup ─► Phase 2 Foundational ─► Phase 2b Detector Dual-Read (PR 2) ─► Phase 3 US1 / Phase 4 US2 / Phase 6 US4 (Collector v3, PR 3) ─┬─► Phase 5 US3 Manifest & Version Gates (PR 4) ─┐
     │                                                                                                                                        ├─► Phase 5 US3 Image-Pin Gate (PR 5) ────────────┼─► Phase 7 Migrations (PR 7+) ─► T069
     └────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────┼─► Phase 6 US4 Violation Reporting (PR 6) ───────┤
                                                                                                                                              └─► Phase 8 Polish ◄──────────────────────────────┘
```

### User story dependencies

- **US1 (P1)**: needs Phase 2 and PR 2 (detector dual-read deployed first).
- **US2 (P1)**: needs US1's `log_contract.lua` (T019). Same file, so the tasks run in sequence.
- **US3 (P2)**:
  - the `logcheck` tasks (T032–T035) need only Phase 2 (delivered in PR 1);
  - the preflight tasks need T020 (`legacy-containers.txt`);
  - the image-pin tasks need Gate 1b (T043a) and T044a.
- **US4 (P3)**:
  - the collector routing and redaction need US1 and US2 (same Lua file and config);
  - runtime reporting and quarantine alerting are delivered in PR 6.
- **Migrations**: need US3 (the gates) and the published `logcheck` image.

### Within each story

The tests come first and must fail before the implementation, then the implementation, then the end-to-end record. Tasks that edit `log_contract.lua` or `fluent-bit.conf` run in sequence:

- `log_contract.lua`: T019 → T031 → T052 → T053
- `fluent-bit.conf`: T021 → T054

---

## Parallel Opportunities

| When | Run together |
|---|---|
| Phase 1 | T002, T003, T004 |
| Phase 2 | T006, T007 (after T005); T011, T012 (after T008–T010) |
| Phase 2b | T015a, T015b |
| US1 tests | T016, T017; then T024, T025 alongside T019–T023 |
| US2 tests | T028, T029, T030 |
| US3 | the `logcheck` group (T032, T033), the preflight fixtures (T036), the workload annotations (T040, T041, T042, T042a), and the image-pin tests (T044), each in a different file |
| US4 | T048–T051 (tests); T056a–T056e (detector); T056, T057, T058 (docs) |
| Migrations | T060 (PN), T065 (health), and T066a (blog) in different repositories |
| Polish | T070, T071 |

### Parallel example: User Story 3

```text
Task: "T032 [US3] Implement services/error-pipeline/cmd/logcheck/main.go"
Task: "T036 [US3] Create preflight fixtures under tests/preflight/fixtures/log_contract/"
Task: "T040 [US3] Annotate deploy/projects/pn/workloads/backend-deployment.yaml"
Task: "T044 [US3] Extend tests/skills/test_update_project_images.py"
```

### Parallel example: User Story 4 docs

```text
Task: "T056 [US4] Update docs/superpowers/specs/2026-10-29-error-pipeline-design.md"
Task: "T057 [US4] Rewrite docs/superpowers/plans/2026-10-29-ws1-error-detector-and-notify-proxy.md"
Task: "T058 [US4] Rewrite docs/superpowers/plans/2026-10-29-ws0-project-log-migration.md"
```

---

## Implementation Strategy

### Delivery Order (PR slices, operator decision D22)

1. **PR 1**: Contract v3 §2–§6, `services/error-pipeline/internal/conform`, `cmd/logcheck`, `logcheck` image (T001–T015, T032–T035).
2. **PR 2**: Detector reads v2 and v3 (dual-read) (T015a–T015e). Built on merge, pinned by digest before PR 3 begins (R-5).
3. **PR 3**: Collector v3, covering US1, US2, and the collector half of US4 (T016–T031, T048–T055, legacy list with 5 containers, contract §1, readers, plus T070 and T071 for this slice).
4. **PR 4**: Manifest gate (preflight `check_log_contract`), version-label PR check (Gate 1b, `check-version-labels.py`), and workload annotations (T036–T043a, T042a).
5. **PR 5**: Image-pin gate (helper, skill, and `image-pin.py` diff check allows version label) (T044–T047, T044a).
6. **PR 6**: Violation reporting and quarantine alert in detector (T056a–T056e, T056–T059).
7. **PR 7+**: Project migrations (PN, health, blog: T060–T068a), then removal of the detector's legacy v2 read (T069, FR-028).

### Notes

- Every PR runs T070, T073, and T074 for its own slice.
- Never stage unrelated working-tree changes. Start each slice from `origin/main` in its own worktree or branch.
- Stop and ask the user if:
  - a measurement exceeds the collector limits (T072);
  - an image has no version label for T040–T042a;
  - `render-validate.sh` needs changes beyond adding the patch rendering (T043).
