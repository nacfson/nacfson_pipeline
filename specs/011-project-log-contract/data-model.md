# Data Model: Project Log Contract v3

**Feature**: [spec.md](spec.md) · **Research**: [research.md](research.md)

## 1. Printed log line (project-owned)

One JSON object per line on stdout or stderr. Keys are flat dotted names; a nested form is equivalent. The full definition is in [contracts/log-line-v3.md](contracts/log-line-v3.md).

| Key | Type | Required | Validation |
|---|---|---|---|
| `time` | string | every line | RFC 3339, UTC `Z`, ≥ 3 fractional digits |
| `level` | string | every line | `debug`/`info`/`warn`/`error`/`fatal`, case-insensitive; aliases `warning` → `warn`, `critical` → `fatal` |
| `msg` | string | every line | non-empty |
| `service.name` | string | every line | `^[a-z][a-z0-9-]{1,62}$` |
| `service.version` | string | every line | `^v\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`. A tag build prints the plain tag; an untagged build prints `v0.0.0-<short commit>`; a plain `v0.0.0` (local build outside Git) is valid in a line but its image can't be pinned (FR-014) |
| `exception.type` | string | `error`/`fatal`: this or `exception.message` | not a generic wrapper type (`*fmt.wrapError`, `*errors.joinError`, `*errors.errorString`) |
| `exception.message` | string | `error`/`fatal`: this or `exception.type` | non-empty |
| `exception.stacktrace` | string | required on `fatal`; recommended on `error` | non-empty; ≤ 8192 bytes (top kept). The collector cuts a longer value (FR-018c) |
| `error.fingerprint` | string | optional | `^[0-9a-f]{8,64}$` |
| `trace_id` | string | lines printed while handling a request | `^[0-9a-f]{32}$`, not all zeros |
| `event.name` | string | product events | registered in contract §5 |
| `http.request.method` | string | access lines | uppercase HTTP method |
| `url.path` | string | access lines | starts with `/` |
| `http.response.status_code` | integer | access lines | 100–599 |
| `http.server.request.duration` | number | access lines | seconds, ≥ 0 |
| *(other keys)* | any | — | fixed names only (FR-007a); must not be a reserved key |

**Reserved keys.** A project must not print these. The collector strips them before adding its own values:

- `k8s.namespace.name`, `k8s.pod.name`, `k8s.pod.uid`, `k8s.container.name`, `k8s.node.name`
- `log.iostream`, `observed_time`
- `_time`, `_msg`, `_stream`, `_stream_id`, `_route`

## 2. Raw line (not JSON)

This is the text of a line that is not JSON:

- a runtime crash, grouped with its trace (R2);
- a native nginx error line;
- anything else, which is a violation.

## 3. Stored record (platform-owned)

| Field | Source | Notes |
|---|---|---|
| `_time` | the project's `time` if valid and within ±5 min of `observed_time`, else `observed_time` | FR-015 |
| `_msg` | the project's `msg`, or the raw text | FR-016 |
| `_stream` / `_stream_id` | VictoriaLogs, from the stream fields | `k8s.namespace.name`, `k8s.pod.name`, `k8s.container.name` |
| `k8s.namespace.name` | log file path | stream field |
| `k8s.pod.name` | log file path | stream field |
| `k8s.pod.uid` | log file path | |
| `k8s.container.name` | log file path | stream field |
| `k8s.node.name` | `NODE_NAME` (Downward API on Fluent Bit) | |
| `log.iostream` | CRI stream word | `stdout` / `stderr` |
| `observed_time` | containerd timestamp | RFC 3339, nanoseconds |
| `time` | the project's value | kept only when it was rejected for `_time` |
| *(all other printed keys)* | the parsed project JSON | values may be redacted. `exception.stacktrace` is cut to 8 KiB (FR-018c) |

**Redaction marker.** Any string value, or the raw text, may contain `[REDACTED:<kind>]`, where `<kind>` is one of:

- `bearer`
- `jwt`
- `google-key`
- `sk-key`
- `secret-field`

## 4. Storage area (tenant)

| Area | VictoriaLogs tenant (`AccountID:ProjectID`) | Default search | Content |
|---|---|---|---|
| main | `0:0` | yes | refined records (§3): complete JSON, grouped crashes, nginx native errors, and all lines of `pending` containers |
| quarantine | `0:1` | no | **unrefined** lines from v3 containers: plain text, broken JSON, or JSON missing a required key |

### Routing decision: "can the collector refine it?" (FR-018a)

```
container listed in legacy-containers.txt         → main
record is a grouped crash (go/python/java parser) → main
raw text matches an nginx native error line       → main
JSON object with time, level, msg,
             service.name, service.version        → main
anything else (plain text, broken JSON,
               JSON missing a required key)       → quarantine
```

The collector applies only these rules. Every other line rule (FR-004 to FR-007a) is checked downstream by the detector. The manifest's version label is checked on the PR, not at runtime (FR-024a).

### Quarantine record (unrefined)

| Field | Source |
|---|---|
| `_time` | `observed_time` (the project's `time` is never used) |
| `_msg` | the **original printed text** (joined if it was split, FR-013), with only FR-017 redaction applied |
| `k8s.namespace.name`, `k8s.pod.name`, `k8s.pod.uid`, `k8s.container.name`, `k8s.node.name` | the platform (log file path, `NODE_NAME`) |
| `log.iostream`, `observed_time` | the CRI line |

No other field is stored. The project's JSON keys, if any, are **not** parsed into fields: they stay inside `_msg` as text.

Example: PN backend prints `user login failed token=Bearer eyJhbGciOi…`. The quarantine stores:

```json
{"_time":"2026-11-04T09:00:10.125481302Z","_msg":"user login failed token=[REDACTED:bearer]","k8s.namespace.name":"proj-pn","k8s.pod.name":"pn-backend-7d9f5c6b8-q4wzn","k8s.pod.uid":"3f1c…","k8s.container.name":"backend","k8s.node.name":"vps-node-1","log.iostream":"stderr","observed_time":"2026-11-04T09:00:10.125481302Z"}
```

## 5. Container log declaration (manifest)

Pod-template annotations, one set per container:

| Annotation | Values | Rule |
|---|---|---|
| `logs.nacfson.cloud/<container>` | `v3/json`, `v3/nginx`, `v2/legacy` | required (FR-020) |
| `logs.nacfson.cloud/<container>.data-paths` | comma-separated absolute paths | optional; each path must equal a `volumeMount.mountPath` (FR-021) |
| `logs.nacfson.cloud/<container>.data-reason` | one line | required when `data-paths` is set |

Pod-template label:

| Label | Value | Rule |
|---|---|---|
| `app.kubernetes.io/version` | release tag, or `v0.0.0-<commit>` | written by the image-pin helper. It must match the version regex, in both clusters. A plain `v0.0.0` is refused (FR-014). It must equal the pinned image's own version label, checked on every PR (FR-024a, Gate 1b). It is a readable name tag for people; no program reads it. |

## 6. Migration row (contract §6)

Columns: `project | container | current format | status`.

Status changes:

```
pending ──(pinned image passes log-contract and annotation = v3/*)──► done
```

- `done` can't change back. Preflight fails if a `done` row's container is declared `v2/legacy` (FR-022).
- When every row is `done`, a follow-up PR removes the detector's legacy `kind` read (FR-028).

## 7. Conformance violation

| Field | Example |
|---|---|
| `project` | `proj-pn` |
| `container` | `backend` |
| `section` | `§2` (contract section) |
| `kind` | `not-json`, `missing-key:service.version`, `bad-level`, `missing-exception`, `generic-exception-type`, `bad-trace-id`, `dynamic-key` (a key name with a run of ≥ 4 digits or a 32-hex segment), `reserved-key`, `stacktrace-too-long` (> 8 KiB), `version-mismatch` (`logcheck --expect-version` only, at build time), `time-rejected` (detector only), `redacted:<kind>` |
| `line` | the offending line, already redacted |

Three tools emit violations in this shape:

- `logcheck`, in project CI;
- the detector, at runtime;
- preflight, for manifest rules. Preflight uses its own existing `{rule, object, detail}` shape.

## 8. Redaction kind and test vectors

The redaction kinds match the patterns defined in `services/error-pipeline/internal/redact` and the Lua filter:

| Kind | Purpose | Go Pattern / Logic | Lua Pattern / Logic |
|---|---|---|---|
| `bearer` | Bearer tokens | `(?i)\bBearer\s+[^\s"']+` | `%f[%a][Bb][Ee][Aa][Rr][Ee][Rr]%s+[^%s"']+` |
| `jwt` | JSON Web Tokens | `eyJ[\w-]+\.[\w-]+\.[\w-]+` | `eyJ[%w_-]+%.[%w_-]+%.[%w_-]+` |
| `google-key` | Google API keys | `AIza[\w-]{30,}` | `AIza[%w_-]+` (len ≥ 34) |
| `sk-key` | OpenAI / Provider sk- keys | `\bsk-[\w-]{16,}` | `sk%-[%w_-]+` (len ≥ 19) |
| `secret-field` | Sensitive keys | key matches `(?i)(?:pass|secret|token|api[_-]?key|authorization|cookie|credential|private[_-]?key|access[_-]?key)` | key matches sensitive pattern |

Shared test vectors (`services/error-pipeline/internal/redact/testdata/vectors.json`) contain `{input, output}` pairs. Both the Go `redact` package and Lua collector implementation must reproduce them exactly.
