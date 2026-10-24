# Log Schema Contract

Every hosted project (`proj-*`) uses this pattern. Project PN is the first example, not a special case. Git is the schema registry. Flux applies the collector ConfigMap. There is no schema-registry pod, and VictoriaLogs does not fetch a schema at runtime.

## Required project pattern

A project pod prints. It does not store the log the platform collects.

| Rule | Requirement |
|---|---|
| Collection path | stdout and stderr only. containerd writes `/var/log/pods/proj-<slug>_...`. The project does not open that path and does not mount it. |
| Forbidden collection path | An in-container file, a project PVC, `logrotate`, or an env var whose job is a log file path. A file inside the container is invisible to `kubectl logs` and is lost on restart. |
| Do not close the stream | Do not point a file logger at `/dev/stdout` or `/dev/stderr`. Closing that writer closes the process stream. |
| Stream split | One job per stream. Do not mix an audit or admin line into a product stdout stream. Process diagnostics go to stderr. A product event stream that must be filtered later goes to stdout as JSON. |
| `kind` | If the line is JSON, `kind` names the stream (`analytics`, `admin`, or a new name). Plain stderr text has no `kind`. |
| Body schema | Stays in the project repository, next to the code that prints it. This repository does not copy the field list. |
| New kind | Pull request in the project repository first, then one row in the table below. |
| Tokens | No access tokens, refresh tokens, passwords, or secrets in any printed line. |

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

`message` is opaque to the collector in this version. The collector does not drop a line because the body fails a producer schema. It also does not keep only one `kind`.

## 2. Registered body schemas

The body schema stays in the owning repository. This table is the platform index, not a copy of the fields.

| `kind` inside `message` | Stream | Owner | Schema |
|---|---|---|---|
| `analytics` | stdout | Project PN | `Project_PN/docs/analytics-contract.md` |
| `admin` | stderr | Project PN | `Project_PN/docs/superpowers/specs/2026-07-07-admin-logging-design.md` |
| absent | stderr slog, or any other print | the printing process | not a registered schema |

The next project adds its own rows here. It does not invent a file path, and it does not get a private collector.

## Rejected

- A schema-registry service, or a database of schemas outside Git.
- Vendoring a project's event fields into this repository.
- Parsing `message` in Fluent Bit in this version.
- A per-project log file or log PVC as the collection path.
