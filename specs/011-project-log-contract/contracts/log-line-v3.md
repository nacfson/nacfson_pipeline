# Contract: Printed Log Line v3

**Audience**: anyone who writes a hosted project (`proj-*`), human or AI agent.
**Becomes**: [`contracts/CONTRACT_LOG_SCHEMA.md`](../../../contracts/CONTRACT_LOG_SCHEMA.md) v3. Field types and validation are in [data-model.md §1](../data-model.md#1-printed-log-line-project-owned).

## Rules

1. Print to stdout or stderr only. Never write a log file or mount a log volume.
2. Print one JSON object per line, using flat dotted OpenTelemetry key names.
3. Every line must have `time`, `level`, `msg`, `service.name`, and `service.version`.
4. `service.version` is your release tag (`v1.5.0`), baked into the build. An untagged build prints `v0.0.0-<short commit>` (`v0.0.0-3f2a9c1`). The source default `v0.0.0` is only for local builds outside Git, and such an image can't be pinned.
5. On a line with `level` `error` or `fatal`:
   - include `exception.type` (the root-cause type, not a generic wrapper) and/or `exception.message`;
   - on `fatal`, also include `exception.stacktrace`. On `error` it is optional;
   - keep `exception.stacktrace` at 8 KiB or less: keep the top and cut the bottom. The platform cuts anything longer and appends `…[truncated N bytes]`.
6. While handling a request, include `trace_id`. Take it from the incoming W3C `traceparent` header, or create one if the header is absent. Pass it on in `traceparent` to every service you call.
7. On a request summary (an access line), include `http.request.method`, `url.path`, `http.response.status_code`, and `http.server.request.duration` (seconds). Set `level` to `error` when the status is 500 or above.
8. Key names must be fixed. Put variable data in values only.
9. Never print `k8s.*`, `log.iostream`, `observed_time`, or keys starting with `_`. The platform owns them.
10. Never print secrets. The platform redacts known patterns (`[REDACTED:<kind>]`) and alerts the operator.
11. The only allowed non-JSON output is:
    - Go, Python, or JVM crash output;
    - the native nginx error log.

## Example lines

Backend error while handling a request:

```json
{"time":"2026-11-04T09:00:10.125Z","level":"error","msg":"enrich failed","service.name":"pn-api","service.version":"v1.4.2","trace_id":"4bf92f3577b34da6a3ce929d0e0e4736","exception.type":"*url.Error","exception.message":"enrich word 4812: context deadline exceeded","exception.stacktrace":"goroutine 41 [running]:\n…"}
```

nginx access line:

```json
{"time":"2026-11-04T09:00:10.130Z","level":"error","msg":"POST /api/words/4812/enrich 502","service.name":"pn-web","service.version":"v1.4.2","trace_id":"4bf92f3577b34da6a3ce929d0e0e4736","http.request.method":"POST","url.path":"/api/words/4812/enrich","http.response.status_code":502,"http.server.request.duration":10.009}
```

Product event (stdout):

```json
{"time":"2026-11-04T09:01:30.502Z","level":"info","msg":"word enriched","service.name":"pn-api","service.version":"v1.4.2","trace_id":"9a1e0c47d2b84f6e8b3c5d7f1a2e4c60","event.name":"analytics","word.id":4812}
```

## Crash signatures (stored in the main area, grouped with their trace)

| Runtime | First line starts with |
|---|---|
| Go | `panic:` or `fatal error:` |
| Python | `Traceback (most recent call last):` |
| JVM | `Exception in thread "` |

## nginx native error levels

| Text | Treated as |
|---|---|
| `[error]` | `error` |
| `[crit]`, `[alert]`, `[emerg]` | `fatal` |

## Registered events (§5)

| `event.name` | Stream | Owner | Schema |
|---|---|---|---|
| `analytics` | stdout | Project PN | `Project_PN/docs/analytics-contract.md` |
| `admin` | stderr | Project PN | `Project_PN/docs/superpowers/specs/2026-07-07-admin-logging-design.md` |

## Migration from v2

| v2 | v3 |
|---|---|
| `kind` | `event.name` (the detector reads `kind` until every row is `done`) |
| text / slog text | JSON (lines from a `pending` container stay in the main area; after migration they are quarantined) |
| `trace_id` may be a UUID | 32 lowercase hex characters |
| platform fields `project`, `pod`, `container`, `pod_uid`, `node` | `k8s.namespace.name`, `k8s.pod.name`, `k8s.container.name`, `k8s.pod.uid`, `k8s.node.name` |
| `stream` dropped | `log.iostream` stored |
| message stored opaque | parsed into fields |
