# Feature Specification: Project Log Contract v3 and Enforcement

**Feature Branch**: `011-project-log-contract`

**Created**: 2026-11-04

**Status**: Draft

**Input**: User description: "Define the log schema architecture for hosted projects (`proj-*`): projects print structured, self-describing JSON lines with OpenTelemetry names; the platform collector adds trusted Kubernetes identity; `service.version` is the project's release tag; the contract is enforced on every project so the error webhook pipeline can rely on it. Follow the CNCF / OpenTelemetry pattern and keep Fluent Bit as the pipeline middle layer."

**Supersedes**: [`contracts/CONTRACT_LOG_SCHEMA.md`](../../contracts/CONTRACT_LOG_SCHEMA.md) v2 (2026-10-29). Consumer of this contract: [`docs/superpowers/specs/2026-10-29-error-pipeline-design.md`](../../docs/superpowers/specs/2026-10-29-error-pipeline-design.md).

## Background

Contract v2 tells a project to print one JSON line with `time`, `level`, `msg`, and `exception.*`. It has three gaps:

1. Nothing enforces it. The hosted projects (`proj-pn`, `proj-health`, `proj-blog`) still print text, and a new project can be deployed without meeting it.
2. A line cannot answer "which release printed this?" or "which other lines belong to the same request?".
3. The stored record does not match the contract or the CNCF pattern:
   - Lines longer than 16 KB are stored as broken fragments. containerd splits them into `P` and `F` pieces, and the collector does not join them.
   - The stdout/stderr stream is dropped, so the contract's stream rules cannot be checked.
   - Platform fields use platform-specific names (`project`, `pod`, …) where OpenTelemetry names exist. Contract v2 itself rejects that.

Contract v3 fixes this by splitting responsibility the OpenTelemetry way:

- The **project** describes the service and the event: `service.*`, `level`, `msg`, `exception.*`, `trace_id`, `http.*`, `event.name`.
- The **platform** describes where the line ran: `k8s.*`, `log.iostream`, `observed_time`. The platform takes these values from sources a project cannot write to.

## Clarifications

### Session 2026-11-04

Decisions reached in the design conversation before this spec:

- Q: Should a project print its own Kubernetes identity (`k8s.namespace.name`, `k8s.pod.name`)? → A: No. The OpenTelemetry split applies. The project prints `service.name` and `service.version`. The platform adds `k8s.*` from the log file path and the node, and removes any `k8s.*` value a project prints.
- Q: Do platform fields keep their v2 names (`project`, `pod`, `container`, `pod_uid`, `node`)? → A: No. They are renamed to the OpenTelemetry names `k8s.namespace.name`, `k8s.pod.name`, `k8s.container.name`, `k8s.pod.uid`, and `k8s.node.name`.
- Q: What is `service.version`? → A: The project's release tag, e.g. `v1.4.2`. It is not an image tag or a digest. The build bakes it into the binary.
- Q: Which time is the main (sort) time? → A: The project's `time`. If a line has no valid `time` (crash output, a native third-party error log), the platform's `observed_time` is the main time. `observed_time` is always stored.
- Q: Is `span_id` part of the contract? → A: No. It has no reader until a tracing backend exists. `trace_id` stays.
- Q: Does each line get a unique ID (`log.record.uid`)? → A: Not in this version. A cited line is identified by stream + time + a short message hash, computed only when it is cited.
- Q: When is `exception.stacktrace` required? → A: It is required for `fatal` lines and recovered panics, and recommended for `error` lines. *(A size cap was added on 2026-11-05, see below.)*
- Q: What version does an untagged build print, and can it be deployed? → A: *(Superseded on 2026-11-05, see below.)* Originally: a hard-coded `v0.0.0`, pinned only in `local-k3s`.
- Q: Are secrets redacted before storage? → A: Yes. The collector replaces them with `[REDACTED:<kind>]` permanently. The detector alerts on the marker.
- Q: Should the collector parse JSON lines into fields at store time? → A: Yes, all keys (option A). This replaces the v2 "no parsing in the collector" rule. Key names must be fixed (FR-007a).
- Q: What happens to lines that break the contract? → A: They are not dropped and not written to a separate file. They go to a quarantine area inside the log store: excluded from default searches, still searchable, and counted by the detector. Containers still migrating are exempt.
- Q: What decides quarantine, and how is a quarantined line stored? → A: A line goes to quarantine when the collector cannot refine it into a complete record: plain text, broken JSON, or JSON missing a required key. Crash and native nginx error lines are recognised and stay in the main area. Lines from containers still migrating stay in the main area. A quarantined line is stored as its original text, with only secret redaction applied, plus the platform's source labels and arrival time. It is not parsed or reformatted.
- Q: Keep JSON as the line format, or use a lighter one? → A: Keep JSON. It is about 10% larger than logfmt, that difference disappears once lines are stored as compressed columns (FR-016), and every tool in the path supports it natively.
- Q: Is a separate log pipeline product added in the middle? → A: No. Fluent Bit (CNCF Graduated) stays the only collector. The OpenTelemetry Collector is reconsidered only if tracing is adopted.

### Session 2026-11-05

Deferred decisions reviewed with the operator (see [deferred-decisions.md](deferred-decisions.md) §0):

- Q: Where may an untagged build run? → A: Everywhere, `vps-k3s` included. To keep that safe:
  - an untagged build prints `v0.0.0-<short commit>` (e.g. `v0.0.0-3f2a9c1`), so every build is identifiable. A tagged release prints its plain tag (`v1.5.0`);
  - the conformance check runs on **every** image build, tagged or not, and the pin gate looks its result up by the image's revision commit. No image is exempt.
- Q: How large may `exception.stacktrace` be? → A: At most 8 KiB. The top (where the failure happened) is kept and the bottom is cut, with a `…[truncated N bytes]` marker. The conformance checker flags a longer trace, and the collector trims it before storage so one crash can't flood the store. Required on `fatal` only stays as decided.
- Q: May the error detector read pods in `proj-*` to compare a line's version with the pod's version label? → A: No. The detector gets no Kubernetes access, and FR-025 drops the runtime version-mismatch report. Alerts take the version from the log line, which the running program printed, so they are correct without it. The manifest's version label is verified on the PR instead, before anything reaches the cluster (FR-024a).
- Q: Should the collector's resources be raised up front? → A: No. Keep `50m` CPU / `64Mi`, verify with the load test, and stop and ask the operator if it is over (SC-009).
- Q: Is a quarantined line only reported in the daily digest? → A: No. A quarantined line from a v3 container also raises an immediate alert, because its errors are invisible to normal error alerting (FR-025).
- Q: How is the feature delivered? → A: As the agent's PR sequence: contract + checker → detector reads v2 and v3 → collector v3 → manifest gate → image-pin gate → violation reporting → per-project migrations (plan § Delivery Order).

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Trace a failure to its request and release from one search (Priority: P1)

The operator gets an error report for `proj-pn`. With one search they see:

- every log line of that same request, across the frontend and backend containers;
- the release tag that printed each line;
- the exact pod, container, and node it ran on.

They can do this without guessing by timestamp.

**Why this priority**: This is the value of the whole contract. The error webhook pipeline and the AI investigator depend on the same property.

**Independent Test**: Deploy one conforming project. Send one request that fails. Search by its `trace_id`. The result contains exactly that request's lines from every container that handled it. Each line carries `service.version` and the five `k8s.*` fields.

**Acceptance Scenarios**:

1. **Given** a conforming frontend and backend, **When** one browser request fails with a 5xx, **Then** a search by that request's `trace_id` returns the frontend access line, the backend error line, and the backend request summary, and returns no lines from other requests.
2. **Given** a stored error line, **When** the operator reads it, **Then** it shows `service.name`, `service.version` (a release tag), `exception.type`, `exception.message`, and the platform's `k8s.namespace.name`, `k8s.pod.name`, `k8s.container.name`, `k8s.pod.uid`, `k8s.node.name`, and `log.iostream`.
3. **Given** an error whose printed line is longer than 16 KB (a long stack trace), **When** it is stored, **Then** it is stored as one complete record that parses as JSON, not as fragments.
4. **Given** a Go panic, **When** it is stored, **Then** its lines carry all platform fields, and the panic line's main time is the platform's `observed_time`.

---

### User Story 2 - A project cannot print a false identity (Priority: P1)

A project prints `"k8s.namespace.name":"proj-health"` from inside `proj-pn`. This could be a copy-paste bug or a compromised container. The stored record still says `proj-pn`, because platform fields come only from the platform.

**Why this priority**: Error alerts, grouping, and the investigator all route on these fields. If a project can forge them, it can misroute alerts or hide its own errors.

**Independent Test**: Run a test container that prints every reserved field with false values. Inspect the stored records. Every reserved field holds the platform's value.

**Acceptance Scenarios**:

1. **Given** a line that contains `k8s.namespace.name`, `k8s.pod.name`, `k8s.container.name`, `k8s.pod.uid`, `k8s.node.name`, `log.iostream`, or `observed_time`, **When** it is stored, **Then** each of those fields holds the value the platform derived, and the project's value is not stored.
2. **Given** the collector configuration, **When** the repository's tests run, **Then** a test fails if the step that removes project-supplied reserved fields is missing or runs after the step that adds the platform's values.

---

### User Story 3 - A non-conforming project cannot reach the cluster (Priority: P2)

A project author, a human or an AI agent, adds a project or releases a new image. The platform refuses it before it runs if:

- its manifest declares a log file or a log volume as its log output;
- it does not declare its log format per container;
- its image does not carry a valid release-tag version label;
- its release did not pass the contract conformance check.

**Why this priority**: Without a gate, the contract stays a document. Both current projects show this: they have not migrated.

**Independent Test**: Submit three changes:
- a project manifest with an env var whose job is a log file path;
- an image pin whose image has no version label;
- an image pin whose release failed conformance.

Each one is rejected with a message naming the rule. A conforming change passes.

**Acceptance Scenarios**:

1. **Given** a project manifest with a container that has no log-format declaration, **When** render-validate runs, **Then** it fails and names the container.
2. **Given** a project manifest that sets a log-file path env var, or mounts a volume as its log output, **When** render-validate runs, **Then** it fails, unless the volume is a declared project-owned data file (see FR-021).
3. **Given** an image whose `org.opencontainers.image.version` label is missing, is not a valid version, or is a plain `v0.0.0`, **When** an image pin is proposed, **Then** the pin is refused.
4. **Given** a release whose conformance check failed or did not run, **When** its image pin is proposed, **Then** the pin is refused.
5. **Given** a PR whose workload pins an image digest whose version label is `v1.5.0` but whose pod-template label says `v1.4.2`, **When** CI runs on the PR, **Then** the PR fails with `log-version-label-mismatch` naming the container and both versions.
6. **Given** a project still listed as `pending` in the contract migration table, **When** render-validate runs, **Then** its existing containers pass with a warning, and any new container in that project must already conform.

---

### User Story 4 - Drift in production is reported (Priority: P3)

A conforming image drifts at runtime. For example, a library starts printing text, or a line leaks a token. The operator is told in Discord without opening a log UI.

**Why this priority**: The P2 gates catch most problems before deployment. This catches what they miss. It extends the existing runtime error detector (`services/error-pipeline`) with quarantine and secret violation alerting.

**Independent Test**: With the detector running, a conforming container prints:
- a non-JSON line;
- a line missing `service.version`;
- a line containing a bearer token.

The operator receives one violation digest for the first two. For the token, the operator receives one immediate security alert, and the stored line shows `[REDACTED:bearer]` in place of the token.

**Acceptance Scenarios**:

1. **Given** contract violations from one container, **When** a day passes, **Then** the operator receives one digest grouped by project, container, and violation kind.
2. **Given** a line matching a secret pattern, **When** it is detected, **Then** the operator receives an immediate alert that is not rate-limited and does not contain the secret value.
3. **Given** a container declared at v3, **When** it prints a non-JSON line that is not a crash or a native error line, or a JSON line missing a required key, **Then** the line is stored in the quarantine area, does not appear in a default search, and is counted in the violation digest.
4. **Given** a container declared at v3 that has printed no quarantined line for an hour, **When** it prints one, **Then** the operator receives an immediate quarantine alert with the project, container, and the redacted line. Further quarantined lines in the same hour produce no new alert; the next alert carries their count.
5. **Given** a container declared at v3, **When** it panics, **Then** the panic and its stack trace are stored in the main area as one record, not in quarantine.

---

### Edge Cases

- **Program output that isn't JSON** (crash signatures, nginx native error log): it is stored with the raw text as the message, all platform fields, and `observed_time` as the main time. It is never dropped.
- **A `time` that is missing, unparsable, or more than 5 minutes from `observed_time`**: the main time falls back to `observed_time`, and the line counts as a contract violation.
- **Buffered output** (e.g. Python stdout piped without unbuffering): the main time follows each line's own `time`, so the order stays correct. The `observed_time − time` gap shows the buffering.
- **A project that is still migrating** (`pending` row): its text lines go to the main area, not quarantine, so it stays visible in the log viewer until it migrates.
- **A container that suddenly prints only text** (a broken release): all its lines go to quarantine, where normal error alerting can't see them. The first quarantined line raises an immediate quarantine alert (FR-025), and quarantine is still searchable, so nothing is hidden.
- **Collector restart**: no line is lost or re-sent beyond the existing tail-bookmark guarantee.
- **Collector cannot reach the store**: existing retry behaviour applies. Retried lines may be stored twice. That is accepted, and readers remove duplicates by stream + time + message.
- **Untagged build**: it prints `service.version` `v0.0.0-<short commit>` and can be pinned in either cluster once its conformance check passed (FR-014, FR-024).
- **A huge stack trace** (a Go panic dumping every goroutine): `exception.stacktrace`, or the text of a grouped crash record, is cut to its first 8 KiB with a `…[truncated N bytes]` marker (FR-018c). The failing frame is at the top, so it is kept.
- **A secret inside a long stack trace or a non-JSON crash line**: it is redacted too (FR-017 applies to every stored line).
- **Over-redaction**: the redacted value of a field like `token_count` is lost. This is accepted. The marker shows what was removed, and the field key is kept.
- **A release with no tag yet** (first deployment of a new project): it can be pinned as `v0.0.0-<short commit>` once its conformance check passed. Its alerts show the commit instead of a release.
- **A project's own data file** (PN's learning log, set by `LEARNING_LOG_DIR`): allowed when declared. It is not collected.

## Requirements *(mandatory)*

### Functional Requirements

#### Line format (project-owned)

- **FR-001**: Each line a project prints to stdout or stderr MUST be one JSON object with flat, dotted OpenTelemetry key names. The only exceptions are the FR-010 lines.
- **FR-002**: Every line MUST contain:
  - `time`: RFC 3339, UTC, with millisecond precision;
  - `level`: one of `debug`, `info`, `warn`, `error`, `fatal`, matched case-insensitively, with the aliases `warning` = `warn` and `critical` = `fatal`;
  - `msg`;
  - `service.name`;
  - `service.version`.
- **FR-003**: `service.version` MUST be the project's release tag, or the FR-014 untagged form, matching `^v\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`. It MUST be fixed in the artifact at build time, not supplied by the deployment manifest.
- **FR-004**: A line with `level` `error` or `fatal` MUST contain `exception.type` or `exception.message`, or both.
  - `exception.type` MUST name the root cause's type, not a generic wrapper type (e.g. not Go's `*fmt.wrapError`).
  - `exception.stacktrace` MUST be present on `fatal` lines and recovered panics, and SHOULD be present on `error` lines.
  - `exception.stacktrace` MUST NOT exceed 8 KiB (8192 bytes). A project trims a longer trace from the bottom and keeps the top. The collector enforces the cap as well (FR-018c).
- **FR-005**: A line printed while handling an inbound request MUST contain `trace_id`: 32 lowercase hex characters, carried between services in the W3C `traceparent` header. The first service that receives the request creates it. Lines not tied to a request (startup, background jobs) MUST NOT invent one.
- **FR-006**: A line that summarises an HTTP request (an access line) MUST contain `http.request.method`, `url.path`, `http.response.status_code`, and `http.server.request.duration` in seconds. `level` MUST be `error` for status ≥ 500.
- **FR-007**: A product event line MUST contain `event.name` registered in the contract's event table, and its body schema MUST stay in the owning project's repository.
- **FR-007a**: Key names MUST be fixed names from the contract or the project's registered event schema. Variable data (IDs, user names, paths) MUST appear only in values, never in key names. Example: `"word.id":4812` is valid and `"word_4812":"ok"` is not.
- **FR-008**: A project MUST NOT print `k8s.*`, `log.iostream`, or `observed_time`. If it does, the platform overwrites them (FR-012).
- **FR-009**: No line MAY contain access tokens, refresh tokens, passwords, API keys, or other secrets. This includes `exception.message` and `exception.stacktrace`.
- **FR-010**: The only non-JSON lines allowed are:
  - runtime crash signatures (Go, Python, JVM), listed in the contract;
  - native third-party server error logs listed in the contract (nginx).

#### Collection and storage (platform-owned)

- **FR-011**: The platform MUST add these fields to every stored `proj-*` line, from sources a project cannot write to:
  - `k8s.namespace.name`, `k8s.pod.name`, `k8s.pod.uid`, and `k8s.container.name`, from the log file path;
  - `k8s.node.name`, from the node;
  - `log.iostream` (`stdout`/`stderr`) and `observed_time` (nanoseconds), from the container runtime's line.
- **FR-012**: The platform MUST remove any project-supplied value of an FR-011 field before adding its own. A test MUST enforce this order.
- **FR-013**: The platform MUST join a line that the container runtime split into partial pieces (lines over 16 KB) back into one record before parsing it.
- **FR-014**: A build that is not made from a release tag MUST carry the version `v0.0.0-<commit>`, where `<commit>` is the first 7 or more hex characters of the commit it was built from (e.g. `v0.0.0-3f2a9c1`). The build sets the suffix. A build with no commit available (a local build outside Git) falls back to the hard-coded source default `v0.0.0`. A release build sets the plain tag. An untagged image MAY be pinned in either cluster, under the same FR-024 conditions as a release. A plain `v0.0.0` image (no commit) MUST NOT be pinned.
- **FR-015**: The main time of a record stored in the main area MUST be the line's `time` if it is valid and within 5 minutes of `observed_time`. Otherwise it MUST be `observed_time`. Quarantined records always use `observed_time` (FR-018a).
- **FR-016**: For a line routed to the main area, the collector MUST store the parsed JSON as separately searchable fields:
  - the stored message is the line's `msg`;
  - the line's `time` becomes the main time when it is used (FR-015);
  - every other key becomes its own field, and no key is dropped.

  A recognised FR-010 line (a crash or a native nginx error line) is stored with its raw text as the message. Quarantined lines are not parsed (FR-018a). This replaces the v2 rule "do not parse `message` in the collector".
- **FR-017**: The collector MUST replace secrets with a marker before a line is stored, and the change is permanent. The marker is `[REDACTED:<kind>]`, where `<kind>` is one of `bearer`, `jwt`, `google-key`, `sk-key`, or `secret-field`.
  - The patterns reuse the existing `services/error-pipeline/internal/redact` rules:
    - `bearer`: `(?i)\bBearer\s+[^\s"']+`;
    - `jwt`: `eyJ[\w-]+\.[\w-]+\.[\w-]+`;
    - `google-key`: `AIza[\w-]{30,}`;
    - `sk-key`: `\bsk-[\w-]{16,}`;
    - `secret-field`: keys matching `(?i)(?:pass|secret|token|api[_-]?key|authorization|cookie|credential|private[_-]?key|access[_-]?key)` in quoted KV, header, or assignment syntax.
  - The pattern list MUST be defined once, and the collector and the detector's outbound redaction MUST use that same list. A test MUST fail if they differ. Shared test vectors are stored at `services/error-pipeline/internal/redact/testdata/vectors.json`.
  - The unredacted secret MUST NOT reach the store.
- **FR-018**: The collector MUST NOT drop a line or change its project-supplied content because it breaks the contract. It only:
  - adds, removes, and renames the FR-011 fields;
  - applies the FR-013, FR-015, FR-016, FR-017, and FR-018c behaviour;
  - routes the line to the main or quarantine area (FR-018a).
- **FR-018a**: The store MUST keep a separate **quarantine area** next to the **main area**, in the same store.
  - **Search:** a default search MUST NOT return quarantined lines. A search that names the quarantine area MUST return them.
  - **Limits:** both areas share FR-019's retention and size limits.
  - **Routing rule ("can the collector refine it?"):** after crash grouping (FR-018b) and the JSON parse attempt (FR-016), a line goes to the **main area** if any of these hold:
    - it is a JSON object containing every key required by FR-002;
    - it is a recognised FR-010 line (a grouped crash, or a native nginx error line);
    - its container is still listed as `pending` (migrating).

    Every other line goes to **quarantine**: plain text, broken JSON, and JSON missing a required key.
  - **Quarantine record content:** a quarantined line is stored **unrefined**:
    - its message is the original printed text (after FR-013 joining), with only FR-017 redaction applied;
    - the platform adds the FR-011 fields;
    - its main time is `observed_time`.

    No project JSON is parsed into fields, and FR-015 time selection is not applied.
  - **Deeper checks:** checks beyond the routing rule (FR-004 to FR-007a) are not done by the collector. Those lines stay in the main area and are reported by the detector (FR-025).
- **FR-018b**: Before routing, the collector MUST group a runtime crash and its following stack-trace lines (Go, Python, JVM) into one record, so continuation lines are never quarantined as non-JSON.
- **FR-018c**: The collector MUST cut a value longer than 8 KiB to its first 8 KiB, at the last line break inside the limit, and append `…[truncated N bytes]`, where N is the number of bytes removed. The cap applies to `exception.stacktrace` in a main-area record and to the message of a grouped FR-018b crash record. This is the only change the collector makes to project-supplied content besides FR-017 redaction.
- **FR-019**: Only `proj-*` namespaces are collected and stored. Retention and size limits stay as they are now: 30 days, 8 GiB cap, 10 Gi volume.

#### Enforcement

- **FR-020**: Each container in a project workload MUST declare its log format (`json` or `nginx`) and contract version in its manifest. Render-validate MUST fail on a container without a declaration.
- **FR-021**: Render-validate MUST fail on a project container that:
  - sets an environment variable or argument whose job is a log-file path or a non-JSON log format;
  - mounts a volume as its log output.

  A project-owned data file (not a log stream) is allowed only when it is declared in the manifest with its purpose.
- **FR-022**: Render-validate MUST fail if a `deploy/projects/<slug>` has no row in the contract migration table, or if a row marked `done` has a container declared at an older contract version.
- **FR-023**: The platform MUST provide one contract conformance checker, used by:
  - project build pipelines, which feed it the real output of every image they build, tagged or not;
  - the runtime detector;
  - this repository's tests.

  It MUST report each violation with its contract section. A test MUST fail if the checker and the contract text disagree.
- **FR-024**: Proposing a project image pin, in either cluster, MUST be refused unless every condition below holds. No image is exempt.
  - the image's `org.opencontainers.image.version` label is a valid version (FR-003): a release tag, or `v0.0.0-<commit>` (FR-014). A plain `v0.0.0` is refused;
  - for an untagged image, `<commit>` is a prefix of the image's `org.opencontainers.image.revision`;
  - the conformance check passed on the image's revision commit.
- **FR-024a**: Every pull request in this repository that changes a project workload MUST be checked in CI, before merge, so that each container's pod-template label `app.kubernetes.io/version` equals the `org.opencontainers.image.version` label of the image digest it pins. A mismatch fails the PR and names the container, the label value, and the image's real version. The check reads the image from the registry; it MUST NOT query the cluster.
- **FR-025**: The runtime detector (error-pipeline workstream 1) MUST report:
  - missing required fields;
  - non-JSON lines outside FR-010, and every quarantined line (FR-018a), counted from the quarantine area;
  - key names that break FR-007a. A key name breaks it if it contains a run of 4 or more digits or a 32-character hex segment. This is the same per-key rule the conformance checker uses (FR-023);
  - main-area records that still carry a `time` field, which means the line's own time was rejected (missing, unparsable, or more than 5 minutes from `observed_time`; FR-015);
  - stored lines that contain a `[REDACTED:<kind>]` marker (FR-017), i.e. a project printed a secret.

  Violations go in a daily digest per project, container, and kind. Redaction markers go in an immediate alert that is not rate-limited and names the project, container, release, and marker kind. It never names the secret value, which was never stored.

  **Quarantine alert.** A quarantined line from a container declared `v3/*` MUST also raise an immediate alert, because its errors can't reach normal error alerting. The first quarantined line from a container raises the alert at once. After that, the container gets at most one alert per hour, carrying the count since the last one. The alert names the project, container, and count, and includes the first quarantined line, already redacted. Lines from `v2/legacy` containers never go to quarantine, so they never raise it.

#### Migration and readers

- **FR-026**: Every hosted project container (`proj-pn` backend/frontend, `proj-health` backend/frontend, and `proj-blog` web) MUST migrate to v3. Its row in the migration table moves to `done` when its pinned image conforms.
- **FR-027**: Every in-repository reader of stored logs MUST use the v3 field names in the same change that renames them. This covers scripts, verification checks, documented example queries, runbooks, and the architecture handbook. The detector's design MUST be updated to the v3 names before it is built.
- **FR-028**: During migration, the detector MUST keep reading v2 lines (legacy `kind` as `event.name`). A pull request removes that legacy read after every migration row is `done`.

### Key Entities

- **Log line**: One event printed by a program. It has project-owned fields (FR-002 to FR-007) or is a raw FR-010 line.
- **Platform fields**: Trusted identity and timing added by the collector (FR-011). They are reserved and cannot be supplied by a project.
- **Stored record**: A log line plus its platform fields. It is identified by stream (`k8s.namespace.name`, `k8s.pod.name`, `k8s.container.name`) and main time.
- **Release version**: The release tag in `service.version` and in the image's version label. It links an error to a release and to a pinned image digest.
- **Container log declaration**: A per-container manifest statement of log format and contract version. It is the unit that gates enforce.
- **Conformance violation**: A rule broken by a line or a manifest. It has a project, container, contract section, and kind.
- **Quarantine area**: A separate area of the log store that holds lines breaking the minimal routing rule (FR-018a). It is excluded from default searches, kept under the same limits as the main area, and read by the detector for violation counts.
- **Migration row**: Per project container: current format and status (`pending` / `done`).

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: For a failed request through a conforming project, one search returns 100% of that request's lines from every container that handled it, and 0 lines from other requests.
- **SC-002**: 100% of stored `proj-*` records carry all seven platform fields, including crash lines and non-JSON lines.
- **SC-003**: In the forged-identity test, 0 stored records carry a project-supplied value in a reserved field.
- **SC-004**: A printed line of up to 64 KB is stored as one record that parses back to the printed JSON. The only difference allowed is an `exception.stacktrace` over 8 KiB, cut per FR-018c.
- **SC-004a**: In a test set mixing conforming lines, crash output, and non-conforming lines from a v3 container:
  - 100% of the non-conforming lines are in the quarantine area;
  - 0 of them appear in a default search;
  - 0 crash records are in quarantine;
  - 0 lines are lost.
- **SC-005**: From the moment enforcement lands, 0 non-conforming containers or images reach a cluster: every attempt in the US3 test set is rejected, and every conforming change passes.
- **SC-006**: Within one release cycle per project, all five hosted project containers are `done` in the migration table.
- **SC-007**: From an error alert, the operator can name the release that printed it without leaving the alert (100% of alerts show `service.version`). Verified by detector tests in PR 6; this feature provides the field and updates the detector.
- **SC-008**: A search of the store for a printed secret value returns 0 records (verified by this feature). The same secret causes an operator alert within 2 minutes of being printed (verified by detector tests in PR 6).
- **SC-009**: The collector stays within its current resource limits (50m CPU, 64 Mi memory) at the current project log volume.

## Assumptions

- Fluent Bit stays the collector and VictoriaLogs stays the store, as the CNCF unification design (2026-10-24) decided. No component is added to the log path.
- Each cluster has its own store, so a `deployment.environment` field is not needed.
- Containers share the node clock, so `time` and `observed_time` differ only by output delay. The 5-minute bound in FR-015 is generous.
- Project repositories (`Project_PN`, `nacfson_health`, `nacfson_blog`) accept changes to their logger, nginx log format, release workflow (version label, conformance check), and request-ID propagation. Project_PN already releases with `vX.Y.Z` tags.
- The PN backend's `/var/log/project-pn` volume holds the PN learning log (`LEARNING_LOG_DIR`), a project-owned data file and not a log stream. It is the first FR-021 declared exception.
- The runtime error detector (`services/error-pipeline`) is updated in this feature: dual-reading v2 and v3 records (PR 2), reading quarantine tenant 0:1, and emitting immediate alerts for `[REDACTED:<kind>]` and quarantine (PR 6).
- Renaming the platform fields is a breaking change for saved queries. Nothing outside this repository reads them today.
- Platform namespaces stay live-only, and are not collected.
- Crash grouping (FR-018b) covers Go, Python, and JVM, the runtimes with grouping built into the collector. A Node.js crash's continuation lines would be quarantined. No hosted project runs Node.js on the server today, and adding it needs a contract row first.

## Out of Scope

- The notify proxy and AI investigator themselves (error-pipeline design workstreams 2–3).
- A tracing backend, `span_id`, and OTLP export from projects.
- A per-line unique ID.
- Changing the store, retention, or an off-node copy.
- Collecting platform-namespace logs.
- Replacing the custom detector with vmalert and Alertmanager. That comparison belongs to the error-pipeline design.
