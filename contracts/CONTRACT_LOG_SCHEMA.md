# Log Schema Contract

Every hosted project (`proj-*`) uses this pattern. Project PN is the first example, not a special case. Git is the schema registry. Flux applies the collector ConfigMap. There is no schema-registry pod, and VictoriaLogs does not fetch a schema at runtime.

Version 2 (2026-10-29) adds a required line structure (§2), error fields (§3), and runtime crash signatures (§4). Field names follow the CNCF OpenTelemetry Logs Data Model and semantic conventions. The platform error detector reads these sections. It does not guess. Design: [`docs/superpowers/specs/2026-10-29-error-pipeline-design.md`](../docs/superpowers/specs/2026-10-29-error-pipeline-design.md).

## Required project pattern

A project pod prints. It does not store the log the platform collects.

| Rule | Requirement |
|---|---|
| Collection path | stdout and stderr only. containerd writes `/var/log/pods/proj-<slug>_...`. The project does not open that path and does not mount it. |
| Forbidden collection path | An in-container file, a project PVC, `logrotate`, or an env var whose job is a log file path. A file inside the container is invisible to `kubectl logs` and is lost on restart. |
| Do not close the stream | Do not point a file logger at `/dev/stdout` or `/dev/stderr`. Closing that writer closes the process stream. |
| Line format | One JSON object per line that follows §2. logfmt, slog text output, and free text are not valid project formats. The only unstructured lines allowed are the §4 crash signatures and the §4 third-party server formats. |
| Stream split | One job per stream. Do not mix an audit or admin line into a product stdout stream. Process diagnostics go to stderr. A product event stream that must be filtered later goes to stdout. |
| `event.name` | Names the event category (`analytics`, `admin`, or a new name). Each name maps to one registered schema in §5. A plain diagnostic line has no `event.name`. |
| Body schema | Stays in the project repository, next to the code that prints it. This repository does not copy the field list. |
| New event name | Pull request in the project repository first, then one row in §5. |
| Tokens | No access tokens, refresh tokens, passwords, or secrets in any printed line, including `exception.message` and `exception.stacktrace`. |

A local file that the project keeps for its own job, such as a daily learning file, is not this contract. The platform collector will not ship it unless the same event is also printed.

## 1. Platform envelope (this repo)

Fluent Bit adds these fields to every stored `proj-*` line. Stream fields are only `project`, `container`, and `pod`.

| Field | Source | Role |
|---|---|---|
| `project` | log directory namespace | stream |
| `pod` | log directory pod name | stream |
| `container` | log directory container name | stream |
| `node` | downward API `spec.nodeName` | searchable field |
| `pod_uid` | log directory pod uid | searchable field |
| `time` | containerd timestamp | event time |
| `message` | printed line, unparsed | body |

The Fluent Bit ConfigMap in `deploy/platform/observability/` is the applied copy. A manifest test must fail if those field names or the stream-field list drift from this table. A rename is a pull request to this file and that ConfigMap together. Do not hot-load a second copy from a cluster CRD.

`message` stays opaque to the collector. The collector does not parse it, does not drop a line because it breaks §2, and does not keep only one `event.name`. Consumers such as the error detector parse `message` at query time.

## 2. Line structure (project-owned, every line)

Keys are flat dotted names, as in OpenTelemetry attributes. A nested object that flattens to the same name (`{"exception":{"message":"…"}}`) is equivalent.

| JSON key | Required | OpenTelemetry field | Values |
|---|---|---|---|
| `time` | yes | `Timestamp` | RFC 3339 with a timezone and at least millisecond precision, e.g. `2026-10-28T09:00:00.123Z` |
| `level` | yes | `SeverityText`, mapped to `SeverityNumber` | `debug` (5), `info` (9), `warn` (13), `error` (17), `fatal` (21). Matched case-insensitively, so Go `slog` `ERROR` is valid. Aliases: `warning` = `warn`, `critical` = `fatal`. |
| `msg` | yes | `Body` | Short human-readable summary. |
| `event.name` | no | `EventName` | Event category. See §5. |
| `trace_id` | no | trace context in non-OTLP logs | Lowercase hex. A UUID with hyphens is accepted, so a project without tracing may put its request id here. |
| `span_id` | no | trace context in non-OTLP logs | Lowercase hex. |

The envelope `time` (containerd) stays the storage order. The program's own `time` is kept as a field so a reader can order events precisely and see clock skew.

Example:

```json
{"time":"2026-10-28T09:00:00.123Z","level":"error","msg":"enrich failed","exception.type":"TimeoutError","exception.message":"upstream timeout after 10s","trace_id":"4bf92f3577b34da6"}
```

## 3. Error fields (when `level` is `error` or `fatal`)

A line with `level` `error` or `fatal` is an error. The level is the only thing that marks it.

| JSON key | Required | OpenTelemetry convention | Purpose |
|---|---|---|---|
| `exception.message` | yes, unless `exception.type` is set | exceptions in logs | Error text. |
| `exception.type` | yes, unless `exception.message` is set | exceptions in logs | Error class, e.g. `TimeoutError`. |
| `exception.stacktrace` | recommended | exceptions in logs | Stack trace as one string. |
| `error.fingerprint` | no | platform extension, not OpenTelemetry | The project's own grouping key. If absent, the detector computes one from `project`, `container`, `exception.type`, and the normalized `msg`. |

## 4. Unstructured lines the detector still reads

### Runtime crash signatures

A runtime that crashes prints its own text, not the project's logger. A line that starts with one of these is treated as `level` `fatal`. The lines that follow it up to the next §2 JSON line are its stack trace.

| Runtime | Line starts with |
|---|---|
| Go | `panic:` or `fatal error:` |
| Python | `Traceback (most recent call last):` |
| Node.js | `Unhandled rejection` or `Uncaught` |
| JVM | `Exception in thread "` |

### Third-party server formats

A third-party server binary in a project image (for example `nginx` in a frontend image) follows §2 where it can, and uses its native format where it cannot.

| Server | Access log | Error log |
|---|---|---|
| nginx | JSON `log_format ... escape=json` with `time`, `level`, `msg`. `level` is `error` for a 5xx status and `info` otherwise. | Native format. `[error]` is `error`. `[crit]`, `[alert]`, and `[emerg]` are `fatal`. |

A new server needs a new row here. Until then its non-JSON lines are stored and searchable but never detected as errors.

## 5. Registered event schemas

The body schema stays in the owning repository. This table is the platform index, not a copy of the fields.

| `event.name` | Stream | Owner | Schema |
|---|---|---|---|
| `analytics` | stdout | Project PN | `Project_PN/docs/analytics-contract.md` |
| `admin` | stderr | Project PN | `Project_PN/docs/superpowers/specs/2026-07-07-admin-logging-design.md` |
| absent | any | the printing process | §2 and §3 only |

The next project adds its own rows here. It does not invent a file path, and it does not get a private collector.

## 6. Migration from version 1

| Change | Rule |
|---|---|
| `kind` → `event.name` | During the transition the detector reads a legacy `kind` as `event.name`. A pull request to this file removes the legacy read after every project in the table below is `done`. |
| Text logs → JSON | slog text output and other non-JSON lines are not detected as errors, except §4. Each project switches to JSON in its own repository. |

| Project | Current format | Status |
|---|---|---|
| `proj-pn` backend | Go `slog` text handler; JSON `kind` lines for `analytics` and `admin` | pending |
| `proj-pn` frontend | nginx default format | pending |
| `proj-health` backend | Go `slog` default text output | pending |
| `proj-health` frontend | nginx default format | pending |
| `proj-blog` web | Go `log` package plain text (`log.Printf`) | pending |

## Rejected

- A schema-registry service, or a database of schemas outside Git.
- Vendoring a project's event fields into this repository.
- Parsing `message` in Fluent Bit. Consumers parse at query time.
- A per-project log file or log PVC as the collection path.
- logfmt or free text as a project log format.
- A platform-specific name where an OpenTelemetry name exists.
