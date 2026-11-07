# Research: Project Log Contract v3 and Enforcement

**Feature**: [spec.md](spec.md) · **Date**: 2026-11-04

Each entry follows the same format: Decision, Rationale, Alternatives considered. Sources are given where an external behaviour matters.

---

## R1. Quarantine area = a separate VictoriaLogs tenant

- **Decision**: Quarantined lines go to tenant `AccountID=0, ProjectID=1`, and the main area stays the default tenant `0:0`.
  - Fluent Bit sends to each tenant through its own `http` output, which sets the headers `AccountID` and `ProjectID`.
  - A reader selects quarantine by sending the same headers on `/select/*`.
- **Rationale**:
  - VictoriaLogs has native multitenancy. A tenant is the `(AccountID, ProjectID)` pair, set per request through headers, and defaults to `0:0`.
  - The per-tenant overhead is very low.
  - The existing data and every existing reader already use `0:0`, so nothing about the main area changes.
  - Retention flags apply to the whole store, so both tenants share the 30 d / 8 GiB limits (FR-018a).
  - Source: <https://docs.victoriametrics.com/victorialogs/#multitenancy>.
- **Alternatives considered**:
  - A file on a collector PVC: rejected in the spec conversation (no visibility, a new disk, no rotation).
  - A `log.contract=violation` field in the main tenant: default searches would still return the lines unless every query added a filter.
  - A second VictoriaLogs instance: one more pod on an over-committed node.
- **Follow-up for implementation**: confirm whether the bundled vmui at v1.53.0 can select a tenant. If it can't, the quarantine is read with `curl -H 'ProjectID: 1'` or `vlogscli`, documented in the runbook. The detector uses the HTTP API either way.

## R2. Joining split lines and grouping crashes

- **Decision**:
  1. The `tail` input sets `multiline.parser cri`, which joins containerd's `P…F` partial lines (FR-013).
  2. The **first** filter is `multiline` with `multiline.key_content log`, `multiline.parser go, python, java`, `buffer on`, `flush_ms 2000`, and `emitter_mem_buf_limit 4M` (FR-018b).
  3. `tail` sets `Tag proj.*`, so each log file gets its own tag and the lines of different containers are never joined together.
- **Rationale**:
  - The Fluent Bit docs recommend `cri` on `tail` for runtime-split lines, plus the multiline filter for application stack traces on top of that.
  - The multiline filter must be the first filter, because it re-emits joined records to the head of the pipeline. A filter placed before it would run twice.
  - Built-in parsers exist for Go, Python, Ruby, and Java (GCP format).
  - JSON lines don't match any start rule, so they pass through unchanged.
  - Source: <https://docs.fluentbit.io/manual/pipeline/filters/multiline-stacktrace>.
- **Alternatives considered**:
  - Joining in the detector at query time (contract v2 §4): this leaves continuation lines as separate records, which the quarantine rule would then catch.
  - Custom regex multiline parsers: the built-ins already cover the runtimes we use.
- **Accepted limit**: the Node.js `Uncaught` trace is not grouped (spec Assumptions).

## R3. Parsing JSON at store time

- **Decision**: a `parser` filter with `Key_Name log`, a `json` parser, `Reserve_Data On`, and `Preserve_Key On` (FR-016). Keeping `log` gives the quarantine path the original text (FR-018a), and the Lua stage deletes `log` on the main path. A non-JSON record keeps `log` unchanged.
- **Rationale**:
  - The parse is lossless at field level, and nested objects are flattened by VictoriaLogs into dotted names. This matches contract v2's statement that nested and flat forms are equivalent.
  - The operator chose option A on 2026-11-04.
- **Alternatives considered**:
  - No parsing (option B): rejected by the operator, and it would make FR-015 impossible.
  - Promoting only a few fields (option C): rejected by the operator.

## R4. One Lua stage for the contract-specific logic

- **Decision**: a single Lua filter, `log_contract.lua` in the Fluent Bit ConfigMap, runs after the JSON parse. In order, it:
  1. removes project-supplied reserved keys (`k8s.*`, `log.iostream`, `observed_time`, `_time`, `_route`) (FR-012);
  2. sets the platform fields from the path keys, the tail `stream`, the record time, and `NODE_NAME` (FR-011);
  3. moves raw text into `msg` when the line isn't JSON (FR-016);
  4. chooses the main time: the project's `time` if it is valid and within ±5 min of `observed_time`, otherwise `observed_time`. It returns code `1` to replace the record timestamp (FR-015);
  5. redacts every string value (FR-017; see R5);
  6. sets `_route` to `main` or `quarantine` (FR-018a; see R6).

  Step 6 runs first in practice: it decides whether the line can be refined. For a quarantined line the script skips steps 3–4. It rebuilds the record as only the redacted original text (`msg`), the platform fields, `log.iostream`, and `observed_time`, and keeps the CRI timestamp.

  After the Lua stage, a `rewrite_tag` filter moves `_route=quarantine` records to the tag `quarantine.*`, and a final `modify` filter (`Match *`) removes `_route`.

  `rewrite_tag` re-emits records at the head of the pipeline, so every filter before it MUST use `Match proj.*`. Otherwise a quarantined record would pass through the Lua stage a second time and lose its `k8s.*` labels (analysis finding H1).
- **Rationale**:
  - The built-in filters can't express "is a key missing", "choose between two timestamps", or "redact inside every value".
  - One script is one place to test.
  - Removing reserved keys and then setting them inside the same function makes the FR-012 order impossible to break by reordering config blocks. A manifest test still checks the filter order around the script.
- **Alternatives considered**:
  - `modify` and `nest` filters for the reserved fields plus `grep` for routing: they can't test for missing keys, and `modify Rename` won't overwrite an existing key.
  - A WASM filter: heavier, and it needs a build toolchain.
  - Doing the routing in the detector: then the main area would no longer be clean.

## R5. One redaction list, two implementations, one proof

*Updated 2026-11-04 (reconciliation R-3).*

- **Decision**:
  - The existing Go package `services/error-pipeline/internal/redact` stays the reference implementation. Its patterns are already tested and broader than the original plan's list:
    - `Bearer` tokens;
    - quoted JSON/Python key-value pairs;
    - header `Key: value` lines;
    - `key=value` pairs;
    - JWTs;
    - `AIza…` keys;
    - `sk-…` keys.

    The sensitive key words are `pass`, `secret`, `token`, `api[_-]?key`, `authorization`, `cookie`, `credential`, `private[_-]?key`, and `access[_-]?key`.
  - The marker changes from `[REDACTED]` to `[REDACTED:<kind>]`:
    - `bearer`;
    - `jwt`;
    - `google-key`;
    - `sk-key`;
    - `secret-field`, which covers the quoted, header, and assign pairs.
  - The existing test cases move into `services/error-pipeline/internal/redact/testdata/vectors.json` as `{input, output}` pairs. The Go test reads them, and so does the collector's Lua test (from the collector PR on). Both must reproduce every vector exactly.
- **Rationale**:
  - FR-017 requires one list shared by the collector and the detector.
  - Go regular expressions and Lua patterns differ in syntax, so shared vectors are the proof of parity.
  - The Lua side implements alternation by looping over the key-word list.
- **Alternatives considered**:
  - A new, narrower list: it would duplicate the existing package and redact less.
  - Generating Lua from Go: there is no stdlib way, and it adds a generator.
- **Risk**: matching the quoted-pair rule exactly in Lua is the hardest part. The vectors expose any gap, and if exact parity is not reachable, stop and ask (task note).
- **Test runtime**: CI installs `luajit` from the collector PR on. Fluent Bit embeds LuaJIT.

## R6. How the collector knows a container is still migrating

- **Decision**:
  - Pending containers are listed in a file in the Fluent Bit ConfigMap, `legacy-containers.txt`, one `<namespace>/<container>` per line. The Lua script loads it at start.
  - A container that isn't listed is treated as v3 (strict by default).
  - Preflight fails if the file differs from the set of containers whose annotation declares `v2` (R7) or whose migration row is `pending`.
- **Rationale**:
  - The collector has no Kubernetes API access (`automountServiceAccountToken: false`), so it can't read pod annotations. The `kubernetes` filter would need a token and RBAC.
  - A Git-owned list keeps the collector API-free.
  - Strict-by-default means a new project gets quarantine routing without anyone having to remember to add it.
- **Alternatives considered**:
  - The `kubernetes` filter with annotations: needs an API token in the privileged namespace.
  - A per-namespace label read through the API: same problem.

## R7. How a container declares its log contract in the manifest

- **Decision**: annotations on the pod template.
  - `logs.nacfson.cloud/<container>: "v3/json" | "v3/nginx" | "v2/legacy"` (FR-020).
  - `logs.nacfson.cloud/<container>.data-paths: "<abs path>[,<abs path>]"` declares project-owned data files (FR-021). The annotation `logs.nacfson.cloud/<container>.data-reason` gives the one-line purpose.
- **Rationale**:
  - The declaration sits next to the container it describes, and preflight already reads pod templates.
  - The annotation key form (prefix plus a name of at most 63 characters with dots) is valid Kubernetes syntax.
  - Using the platform domain `nacfson.cloud` avoids collisions.
- **Alternatives considered**:
  - A per-project `logging.yaml` like `sign-in.yaml`: a second file that can drift from the workloads.
  - Labels: these are free-form strings, not selectors.

## R8. The release version in manifests, untagged builds, and CI verification

*Updated 2026-11-05 (operator decisions D1, D19).*

- **Decision**:
  - The image-pin helper writes the image's `org.opencontainers.image.version` into the pod-template label `app.kubernetes.io/version`, the Kubernetes recommended label.
  - Untagged builds may run everywhere, `vps-k3s` included (D1). An untagged build prints `v0.0.0-<short commit>` (e.g. `v0.0.0-3f2a9c1`), matching a prefix of its image revision SHA. Tagged releases print their plain tag (`vX.Y.Z`).
  - The conformance check (`log-contract`) runs on every image build, tagged or not. The pin gate looks it up by revision commit. A plain `v0.0.0` (outside Git) cannot be pinned in either cluster.
  - Preflight validates manifest version syntax against `^v\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$` and blocks plain `v0.0.0` (Gate 1).
  - Manifest label `app.kubernetes.io/version` is verified on PRs by Gate 1b (`scripts/check-version-labels.py`, FR-024a) reading the image registry with `skopeo inspect`. The detector has NO pod access and performs NO runtime version check (D19).
- **Rationale**:
  - Preflight and CI checks run offline or in CI before deploy, preventing invalid versions from reaching either cluster.
  - Alerts take the version from the log line, which the running container printed.
- **Alternatives considered**:
  - Detector querying pod API at runtime: rejected by operator (D19; no cluster RBAC).
  - Restricting untagged builds to `local-k3s` only: rejected by operator (D1; untagged allowed everywhere with short commit and CI check).

- **Implementation Note (T043)**:
  - Confirmed: `render-validate.sh` executes `flux build kustomization ... --kustomization-file <filepath>` on each file in `clusters/<env>/projects/`, which applies Flux Kustomization `spec.patches` prior to preflight evaluation.

## R9. Proof that an image passed the conformance check

*Updated 2026-11-05 (operator decision D1).*

- **Decision**:
  - Each project repository runs a CI job named **`log-contract`** on **every** image build (tagged or untagged). The job:
    1. builds the image;
    2. runs the project's log smoke scenario (startup, one request, one handled error);
    3. pipes stdout and stderr through `logcheck` (R10) with `--expect-version <version>`.
  - The image-pin helper reads the image's `org.opencontainers.image.revision` label and refuses the pin unless the `log-contract` check run on that commit concluded `success`. It queries `gh api repos/<owner>/<repo>/commits/<sha>/check-runs` (FR-024).
- **Rationale**:
  - The helper already resolves the revision label and uses `gh`.
  - A check run is an existing, auditable GitHub record. Nothing new has to be stored.
- **Alternatives considered**:
  - Signed attestations (cosign): new tooling and keys.
  - An OCI label `log-contract=passed` set by the build: the build can't prove a check that runs after it.

## R10. The shared validator and the `logcheck` CLI

*Updated 2026-11-04 (reconciliation R-1, R-2).*

- **Decision**: build inside the existing module `services/error-pipeline` (Go 1.22, standard library only).
  - `internal/classify` is updated to v3: the crash signature list drops Node.js. Its existing contract-drift test (`contract_test.go`) is extended to the v3 tables for required keys, reserved keys, and levels.
  - New `internal/conform` provides `Check(line, opts) []Violation`. It uses `classify` to parse and `redact` for the reported text, and implements the data-model §7 kinds.
  - New `cmd/logcheck`: stdin lines → JSON violations, with exit codes `0`/`1`/`2`. It is built as the image `ghcr.io/nacfson/logcheck` from `services/error-pipeline/Dockerfile.logcheck`, added to the `build-images.yaml` matrix.
  - The contract Markdown is the single source of truth. There is no separate JSON schema file.
- **Rationale**:
  - The detector and `logcheck` share one classifier and one redactor.
  - The existing CI job `error-pipeline-tests` (`gitops.yaml`) already runs `go test ./...` for this module.
- **Alternatives considered**:
  - A new module `services/logcontract`: it duplicates `classify` and `redact`.
  - A `schema/v3.json`: a second source of truth next to the contract.

## R11. Fields on the wire to VictoriaLogs

- **Decision**: both outputs use the URI
  `/insert/jsonline?_stream_fields=k8s.namespace.name,k8s.pod.name,k8s.container.name&_msg_field=msg&_time_field=_time`
  with `json_date_key _time` and `json_date_format iso8601`.
  - `observed_time` is written by Lua as an RFC 3339 string with nanoseconds.
  - When the project's `time` is used, the key is removed (it now equals `_time`). When it is rejected, it is kept as `time` for evidence.
- **Rationale**: VictoriaLogs reads the message field, the time field, and the stream fields from query arguments. The record timestamp chosen in R4 becomes `_time`.

## R12. Collector resources

- **Decision**:
  - Keep `50m` CPU and `64Mi` memory, with requests equal to limits.
  - Cap the buffers: tail `Mem_Buf_Limit 5MB`, multiline emitter `4M`.
  - Verify with the quickstart load scenario: 100 lines/s sustained and 1,000 lines/s for 10 s, mixed JSON, crash, and secret lines (SC-009).
  - If the measurement exceeds the limits, raise memory first. Any change goes through preflight.
- **Rationale**:
  - The VPS is CPU over-committed (`TROUBLE_SHOOTING.md`).
  - Constitution Clarification 2.2.1 says observability resources are declared but not added to the platform reservation by hand.
- **Alternatives considered**: raising the limits up front, without a measurement.

## R13. Error-detector changes (built in this feature)

*Updated 2026-11-05 (operator decisions D19, Q1, and reconciliation R-5, R-6).*

The detector already runs in production. It reads the v2 fields (`{project=~"proj-.*"} | fields _time, _stream_id, project, pod, container, node, message`) and parses `message` itself. The switch happens in three steps:

1. **Dual read (before the collector changes; PR 2).**
   - `internal/vlogs` asks for all fields, and maps a record from either v2 (`project`, `pod`, `container`, `node`, `message`) or v3 (`k8s.*`, `_msg`, plus parsed fields).
   - `classify` accepts a record that is already parsed into fields: it builds the event from the fields instead of re-parsing `message`.
   - The query becomes `{project=~"proj-.*"} OR {k8s.namespace.name=~"proj-.*"}`.
   - Built on merge, then its digest is pinned in `deploy/platform/observability/error-detector.yaml`.
2. **Collector v3 (PR 3)**: deployed only after step 1 is pinned.
3. **v3 features and reporting (PR 6; FR-025)**:
   - quarantine counts from tenant `0:1`;
   - immediate quarantine alert: first quarantined line from a `v3/*` container alerts at once; then at most one alert per container per hour with the count (Q1);
   - immediate `[REDACTED:<kind>]` marker alerts (not rate-limited);
   - daily digest of contract violations (`time-rejected`, `dynamic-key`, missing fields, quarantined line count);
   - NO Kubernetes access or runtime version-mismatch comparison (operator decision D19; manifest versions are verified in CI by Gate 1b);
   - removing the v2 read after every project row is `done` (FR-028).

- **Rationale**: the detector image is built on merge and pinned by digest, so it can't change in the same PR as the collector. Without the dual read, alerts would stop.

## R14. Project-side implementation patterns (for the migration tasks)

- **Go backends** (`proj-pn`, `proj-health`, `proj-blog`):
  - **Logger:** one JSON `slog` logger. A `ReplaceAttr` function renames `time` to RFC 3339 with milliseconds in UTC. Every record carries `service.name` and `service.version`.
  - **Version:** `var version = "v0.0.0"` is overridden at build with `-ldflags "-X main.version=${GITHUB_REF_NAME}"` on tag builds, or `-ldflags "-X main.version=v0.0.0-${GITHUB_SHA::7}"` on untagged builds (FR-014, D1).
  - **`exception.type`:** the type of the innermost error, found by unwrapping with `errors.Unwrap` (single wrap) or `errors.Join` (join); `%T` of the wrapper is never used (FR-004).
  - **`exception.stacktrace`:** `debug.Stack()` on `fatal` lines and in panic recovery, capped at 8 KiB (D6).
  - **`trace_id`:** HTTP middleware reads `traceparent` and stores its trace-id in the context, creating a random 16-byte value if the header is absent.
- **nginx frontends**:
  - **Access log:** `log_format … escape=json` with `time`, `level`, `msg`, `service.name`, `service.version`, `trace_id`, and the `http.*` keys.
    - `time` is `$time_iso8601` plus a millisecond fraction from `$msec`.
    - `level` comes from `map $status` (≥ 500 → `error`).
    - `trace_id` is `$request_id`, which is 32 hex characters.
    - `service.version` is substituted at image build time from `ARG SERVICE_VERSION`.
  - **`traceparent`:** `proxy_set_header traceparent "00-$request_id-$tp_parent-01"`. `$tp_parent` is the first 16 characters of `$request_id`, extracted with a `map` regex.
  - **Error log:** stays native.
- **Release and build workflow**:
  - `org.opencontainers.image.version` = the tag (`vX.Y.Z`), or `v0.0.0-<short commit>` on untagged builds (D1).
  - `org.opencontainers.image.revision` = the commit SHA.
  - The `log-contract` job runs on every image build (R9, D1).
