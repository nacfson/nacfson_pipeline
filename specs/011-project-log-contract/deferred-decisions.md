# Deferred Decisions: Project Log Contract v3

**Date**: 2026-11-04 · **Feature**: [spec.md](spec.md)

On 2026-11-04 the operator said: "For now, I'll follow your intent." The operator did not choose the items below. The agent chose each one as the recommended default, and they are written into the spec, plan, and tasks.

Each item can still be changed. To change one, edit the files in its **Affects** column, then re-run `/speckit-analyze`.

For comparison, these decisions **were** made explicitly by the operator:

- JSON is parsed at store time (option A).
- Secrets are redacted before storage.
- Untagged builds print `v0.0.0`.
- `service.version` is the release tag.
- JSON stays the line format.
- Lines the collector can't refine go to a quarantine area inside VictoriaLogs, unparsed, and projects still migrating are exempt.
- C1: exact base-image versions.
- H2: a narrow gitleaks allowlist.

---

## 0. Reviewed with the operator on 2026-11-05

These items were deferred on 2026-11-04 and then decided by the operator. They are no longer agent defaults.

| # | Decision | Replaces | Written into |
|---|---|---|---|
| D1 | Untagged builds may run **everywhere**, `vps-k3s` included. To keep that safe: (a) an untagged build prints `v0.0.0-<short commit>` (a tag build prints the plain tag), and (b) the `log-contract` check runs on **every** image build, and the pin gate looks it up by revision commit. No image is exempt. A plain `v0.0.0` (no commit) can't be pinned. | "Refused in `vps-k3s`" | spec Clarifications 2026-11-05, FR-003, FR-014, FR-023, FR-024, US3-3, Edge Cases; enforcement-gates Gate 1/2/3; log-line-v3 rule 4; data-model §1, §5 |
| D6 | `exception.stacktrace` stays required on `fatal` only, and is **capped at 8 KiB: top kept, bottom cut**, with a `…[truncated N bytes]` marker. Projects should trim; `logcheck` flags `stacktrace-too-long`; the collector cuts anything longer before storage (new FR-018c). | No size limit | spec FR-004, FR-018, FR-018c, SC-004, Edge Cases; log-line-v3 rule 5; data-model §1, §3, §7; collector-pipeline stage 5 + invariant |
| D19 | **No pod access for the detector, and no runtime version comparison.** Alerts already take the version from the log line, which the running program printed. The manifest's `app.kubernetes.io/version` label is a name tag for people, and it is verified **on the PR, not in the cluster**: CI compares it with the pinned image's own version label and blocks a mismatch (FR-024a, Gate 1b). | Read-only `get`/`list` on pods in `proj-*` | spec Clarifications 2026-11-05, US3-5, US4, FR-018a, FR-024a, FR-025; enforcement-gates Gate 1b; data-model §4, §5, §7; plan PR 4/6; tasks T043a, T056, T057 |
| D22 | Delivery follows the agent's PR sequence, updated for reconciliation R-5/R-6: contract + `logcheck` → detector reads v2 and v3 → collector v3 → manifest gate → image-pin gate → violation reporting → per-project migrations. | 6-slice order | plan § Delivery Order |

| D17/D18 | Collector resources stay `50m` CPU / `64Mi`. The load test (100 lines/s, then 1,000 lines/s for 10 s) verifies it; if it is over, stop and ask. | (same as the default, now confirmed) | unchanged |
| Q1 (new) | **Immediate quarantine alert.** A quarantined line from a `v3/*` container raises an alert at once; after that, at most one per container per hour, with the count. Reason: after migration, a broken release's errors land in quarantine, where normal error alerts can't see them, and the daily digest would be up to a day late. The "at once, then hourly" rule is the agent's default and can be changed. | Daily digest only | spec Clarifications 2026-11-05, FR-025, US4-4, Edge Cases; plan PR 6; tasks T056, T057 |

Because of D1, findings **M2** and **M3** below no longer apply: no image skips the conformance check, and an untagged image needs no special `local-k3s` pin path.

---

## 1. Analysis findings resolved by the agent (MEDIUM)

| ID | What was unclear | Choice made | Alternative | Affects |
|---|---|---|---|---|
| M2 | FR-024 required a conformance check for every pin, but `v0.0.0` images can't have one | ~~`v0.0.0` images skip the check, and they can only be pinned in `local-k3s`~~ **Superseded by §0 D1:** the check runs on every build, so no image skips it | Require a check even for local test images | spec FR-024, contracts/enforcement-gates.md Gate 3 |
| M3 | Nothing helps pin a `v0.0.0` image for `local-k3s` | ~~A manual edit, documented in the image-pin skill. There is no tool support.~~ **Superseded by §0 D1:** untagged images are pinned like releases, in either cluster | Add a `--target local-k3s` mode to the helper | tasks T047 |
| M4 | A program `time` that is off by more than 5 minutes was called a violation, but nothing reported it | The error detector reports a `time-rejected` violation: a main-area record that still has a `time` field | Drop the "violation" wording and only fall back silently | spec FR-025, data-model §7 |
| M5 | Two different rules existed for detecting changing key names | One per-key rule: a key name with a run of 4 or more digits, or a 32-character hex segment | Count the distinct keys per container (needs a threshold) | spec FR-025, data-model §7, tasks T010 |
| M6 | "No key is dropped" conflicted with `time`/`msg` becoming `_time`/`_msg` | FR-016 reworded: `time` and `msg` become the main time and message, and every other key is kept | — | spec FR-016 |
| M7 | The request-tracing check ran before projects could pass it | It moved to the new task T067a, after PN migrates | Keep it in T075 and expect it to fail | tasks T067a, T075 |
| M8 | Two success criteria depend on error-pipeline workstream 1 | SC-007 and the alert half of SC-008 are marked "verified when workstream 1 lands" | Move them out of this spec | spec SC-007, SC-008 |

## 2. Design defaults the agent chose without an explicit confirmation

| # | Topic | Choice made | Why | Alternative | Affects |
|---|---|---|---|---|---|
| D1 | `v0.0.0` on the VPS | ~~Refused in `vps-k3s`, allowed in `local-k3s`~~ **Decided by the operator, see §0** | So the VPS only ever runs real releases. The operator asked for `v0.0.0` but didn't say where it may run. | Allow `v0.0.0` everywhere | spec FR-014, enforcement-gates Gate 1/3 |
| D2 | Who writes `k8s.*` | Only the platform. Projects must not print it (the OpenTelemetry split). | The platform's values can't be faked, and projects need no Downward API | Projects also print `k8s.*` and the detector cross-checks it | spec FR-008/FR-011, log-line-v3.md |
| D3 | Platform field names | Renamed to OpenTelemetry names (`project` → `k8s.namespace.name`, etc.) | Contract v2 itself rejects platform names where an OpenTelemetry name exists. Nothing outside this repo reads them yet. | Keep the v2 names | spec FR-011, FR-027, collector-pipeline.md |
| D4 | `span_id` | Dropped | No reader exists until a tracing backend is added | Keep it as optional | spec Clarifications |
| D5 | Per-line unique ID (`log.record.uid`) | Not added | `trace_id` plus stream + time + a message hash covers citations | A platform-generated ULID in the Lua stage | spec Clarifications |
| D6 | `exception.stacktrace` | Required on `fatal`, recommended on `error`. **Confirmed by the operator, plus an 8 KiB cap, see §0** | Go errors carry no stack, and `debug.Stack()` on every error costs CPU | Required on every `error` | spec FR-004 |
| D7 | `service.name` | Required on every line | Tells apart several programs in one container (API vs `migrate` job) | Optional, since `k8s.container.name` already differs | spec FR-002, schema v3.json |
| D8 | Time tolerance | The program's `time` is used when within ±5 min of `observed_time` | Containers share the node clock, so 5 min is generous | A tighter (e.g. 30 s) or looser bound | spec FR-015 |
| D9 | Quarantine tenant | VictoriaLogs tenant `AccountID=0, ProjectID=1`, with the main area at `0:0` | Existing data and readers already use `0:0` | Another ProjectID number | data-model §4, collector-pipeline.md |
| D10 | How the collector knows a container is migrating | A Git file `legacy-containers.txt`. A container not listed is strict. | The collector has no Kubernetes API access | Give Fluent Bit API access to read annotations | research R6, tasks T020, T038 |
| D11 | Container declaration format | Pod annotations `logs.nacfson.cloud/<container>: v3/json \| v3/nginx \| v2/legacy`, plus `.data-paths` / `.data-reason` | Sits next to the container, and preflight already reads pod templates | A per-project `logging.yaml` | research R7, enforcement-gates Gate 1 |
| D12 | Release version in manifests | Pod label `app.kubernetes.io/version`, written by the image-pin helper | Preflight is offline and can't read image labels | Read image labels over the network in CI | research R8, tasks T040–T046 |
| D13 | Proof that a release passed | A GitHub check run named `log-contract` on the image's revision commit | Already auditable, with no new tooling | Signed attestations (cosign) | research R9, enforcement-gates Gate 2/3 |
| D14 | Validator location | ~~New module `services/logcontract`~~ **Superseded by R-1:** `services/error-pipeline/internal/conform`, plus `cmd/logcheck` and its image | One implementation for project CI, the detector, and tests | Separate Python and Go validators | plan, research R10 |
| D15 | Contract logic in the collector | One Lua script (`log_contract.lua`) | Built-in filters can't test for missing keys, swap timestamps, or redact values | A WASM filter | research R4 |
| D16 | Redaction parity | Shared input/output test vectors that both the Go and Lua code must pass | Go regex and Lua patterns use different syntax | Generate the Lua code from the JSON file | research R5 |
| D17 | Collector resources | Keep `50m` CPU / `64Mi`, verified by a load test. If it's over, stop and ask. | The VPS is CPU over-committed | Raise the limits up front | research R12, tasks T072 |
| D18 | Load-test target | 100 lines/s sustained, then 1,000 lines/s for 10 s | No measurement of the current volume exists | Measure the real volume first | plan, quickstart §4 |
| D19 | Error detector access | ~~Read-only `get`/`list` on pods in `proj-*`, for the version-mismatch check~~ **Decided by the operator: no access; the label is verified on the PR, see §0** | The version label lives on the pod | Drop the runtime version check, since CI already checks it | research R13, tasks T056 |
| D20 | Crash runtimes | Go, Python, and JVM are grouped. Node.js is not, so its trace lines would be quarantined. | These are Fluent Bit's built-in parsers, and no project runs Node.js on the server | Write a custom Node.js multiline parser | spec FR-010/FR-018b, Assumptions |
| D21 | ConfigMap packaging | `configMapGenerator` with files under `deploy/platform/observability/fluent-bit/` (hash suffix, so a change restarts the pod) | The Lua script can be unit-tested as a real file | Keep the config inline in the YAML | tasks T020 |
| D22 | Delivery | ~~Six PR slices (contract and validator → collector → manifest gate → image-pin gate → docs → migrations)~~ **Decided by the operator, see §0** | Each slice can be reviewed and rolled back alone | One large PR | plan § Delivery Order |

## 2b. Reconciliation with `main` (2026-11-04, approved by the operator: "Yes")

The plan was first written against the local branch `fix/vps-cpu-budget-and-observe`, which was about 30 commits behind `main`. `main` already had:

- the error detector (`services/error-pipeline`, deployed in `observability`);
- its `classify` and `redact` packages;
- a third project, `proj-blog`;
- the image-pin automation `scripts/image-pin.py`, with a strict digest-only diff check.

The operator approved updating the plan so it **extends the existing code instead of duplicating it**.

| # | Change | Why | Affects |
|---|---|---|---|
| R-1 | No new Go module. The validator (`internal/conform`) and `cmd/logcheck` are built inside `services/error-pipeline` and reuse `internal/classify` and `internal/redact`. | One checker and one redactor (FR-017, FR-023) | plan, research R5/R10, tasks |
| R-2 | No `schema/v3.json`. The contract Markdown stays the single source, checked by Go drift tests (the existing `classify/contract_test.go` pattern) and, from the collector PR on, by a Python test against the Lua tables. | Matches the existing pattern, and avoids a second source of truth | research R10, tasks |
| R-3 | Redaction reuses the existing `redact` patterns, which are broader and already tested. Its marker changes from `[REDACTED]` to `[REDACTED:<kind>]`, with kinds `bearer`, `jwt`, `google-key`, `sk-key`, `secret-field`. Shared vectors live at `services/error-pipeline/internal/redact/testdata/vectors.json`. | Same list for the collector and the detector | spec FR-017, research R5, tasks |
| R-4 | The gitleaks allowlist follows the repository's existing style (exact strings in `.gitleaks.toml`), not a path allowlist | It's narrower, and it's the existing convention | tasks T007 |
| R-5 | The detector switch is split. First the detector learns to read **both** v2 and v3 records (it's built and pinned after merge). Only then does the collector switch to v3. Finally the v2 read is removed. | The detector image is built on merge and pinned by digest, so it can't change in the same PR as the collector. Otherwise alerts would stop silently. | plan § Delivery, tasks Phase 3 |
| R-6 | FR-025 (the violation digest, marker alerts, `time-rejected`, `dynamic-key`, version mismatch) is now built **in this feature** (PR 6), because the detector exists. SC-007 and SC-008 are verified here. | Workstream 1 already landed | spec SC-007/SC-008, tasks Phase 6 |
| R-7 | `proj-blog` (container `blog-web`, Go `log` plain text) is added: the legacy list now has 5 containers, plus annotations, a migration, and a pin. | It's a hosted project on `main` | tasks |
| R-8 | The image-pin automation's diff check (`scripts/image-pin.py` `diff_check`) must also allow the `app.kubernetes.io/version` label line | Otherwise the version label from D12 would be rejected | tasks Phase 5 |
| R-9 | Contract v3 lands in two steps. PR 1 rewrites the project-facing sections §2–§6. §1 (the platform fields) changes in the collector PR, because a manifest test ties §1 to the live Fluent Bit config. | The tests must stay green in each PR | tasks |

## 3. Still open (needs a check during implementation, not a decision)

| Item | Check | Task |
|---|---|---|
| Whether vmui v1.53.0 can select a tenant | If it can't, read the quarantine with `curl -H 'ProjectID: 1'` | T055 |
| Whether `render-validate.sh` applies the per-cluster Flux patches before preflight | If it doesn't, add that to the script | T043 |
| Which release tags the currently pinned images came from | If an image has no version label, ask the operator | T040–T042 |
| Whether this repository's CI can read the GHCR project images (Gate 1b) | If the packages are private and `GITHUB_TOKEN` can't read them, grant the repository read access to each package, or ask the operator for a read-only token | T043a |
