# Error Pipeline: Detection, Discord Webhooks, and AI Investigator — Design

Date: 2026-10-29 · Size: project (two new platform services, one new vault proxy, AI gateway extension, log contract v2, cross-repo project migration) · Status: requirements approved 2026-10-29; sections marked **(my call)** are open to change

Marking: **(operator)** = decided by the operator. **(my call)** = my proposal, can be changed.

## 1. Intent

The operator wants to know when a hosted project fails, and to get a reasoned conclusion about why, without opening a log UI. Two separate features share one detection core:

1. **Error webhook.** Every new error is sent to Discord `#errors` as raw error logs. It works even when everything AI-related is off or broken. **(operator)**
2. **AI debug webhook.** The same errors are investigated by an AI agent, and the conclusion is sent to Discord `#ai-debug`. **(operator)**

Detection only works if logs have a known structure, so the log contract becomes a real, OpenTelemetry-aligned line schema, and every hosted project migrates to it. **(operator)**

## 2. Goals

- Detect errors in `proj-*` logs from the structure defined in [`contracts/CONTRACT_LOG_SCHEMA.md`](../../../contracts/CONTRACT_LOG_SCHEMA.md) v2, not from guessed patterns. **(operator)**
  - `level` `error` and `fatal` are errors. **(operator)**
  - §4 runtime crash signatures and nginx native error lines are errors too.
- Post each **new** error to `#errors` immediately. **(operator)**
- After the first occurrence, collect the same error for **3 minutes**, then run the AI investigator once on everything collected. **(operator)**
- The investigator is an **agent investigator**: an AI loop with read-only tools that ends in a cited conclusion. **(operator)**
- The investigator runs automatically only. There is no manual re-run command. **(operator)**
- The AI side supports many models: Chinese vendor APIs (DeepSeek, Qwen/DashScope, Kimi/Moonshot, GLM/Zhipu) and multi-model subscriptions (OpenCode Go/Zen, Synthetic, Ollama Cloud). **(operator)**
- An investigation tries an **ordered fallback chain** of models. It reports failure only when every model fails. **(operator)**
- If AI analysis fails for any reason, `#ai-debug` still receives the error logs and the failure reason. **(operator)**
- Migrate every hosted project (`proj-pn`, `proj-health`, `proj-blog`; backends and nginx frontends) to contract v2. **(operator)**
- Field names follow the CNCF OpenTelemetry recommendation. **(operator)**

## 3. Non-goals

- A manual or on-demand investigation trigger. **(operator)**
- Any write action by the investigator: restart, rollback, scale, or edit.
- Reading project source code from GitHub (future option; needs a GitHub token in OpenBao).
- Platform namespace logs (`vault`, `identity`, …). Collection stays `proj-*` only, as decided in the log-viewer design.
- Kubernetes-only failures that print no log line, such as an OOMKill. The investigator *reads* pod status, but detection is log-driven in this version.
- Slack, Telegram, or a generic webhook destination. The notify proxy's payload is destination-neutral so one can be added later.
- Self-hosted models (the 2-OCPU VPS cannot run them).

## 4. Constraints

- Go 1.22 standard library only for every new service, the same rule as `gateway/` and `services/vault-proxies/`.
- Zero plaintext secrets. Discord webhook URLs and AI provider keys live only in OpenBao and in the memory of a `vault`-namespace proxy, never in `observability`, Git, env vars, or Kubernetes `Secret`s.
- Flux layer order is unchanged. `observability` must start and run even when `vault-proxies` is not ready, and it retries.
- Pinned image references only: `sha-<commit>` tags or digests, never `:latest`, for the new images.
- Any change under `deploy/`, `clusters/`, `services/`, `contracts/`, or `scripts/platform-preflight.py` also updates `docs/platform-architecture.html`.
- Error logs may contain user data. A redaction pass runs before anything leaves the cluster (Discord or an AI provider).
- **Open item for the operator:** confirm that the OpenCode Go, Synthetic, and Ollama Cloud terms allow calls from an automated server-side service before their keys are seeded.

## 5. Architecture

```
 proj-* pods ──stdout/stderr (contract v2)──► Fluent Bit ──► VictoriaLogs
                                                                 │ /select/logsql/tail
                                         observability ns        ▼
                              ┌──────────────────────────────────────────┐
                              │ error-detector                           │
                              │  classify (§2–§4) → redact → fingerprint │
                              │  ├─► notify #errors (immediately)        │
                              │  └─► 3-min window → POST /v1/investigate │
                              │ error-investigator                       │
                              │  agent loop + read-only tools            │
                              └────┬───────────────────┬─────────────────┘
                     vault ns      ▼                   ▼
                    vault-ai-proxy (multi-provider)   vault-notify-proxy (NEW)
                    routes by model, holds keys       holds Discord URLs
```

| Component | Namespace | Holds secrets | Purpose |
|---|---|---|---|
| `error-detector` | `observability` | none | Tail, classify, group, notify `#errors`, hand off to the investigator |
| `error-investigator` | `observability` | none | Agent loop and read-only tools, report to `#ai-debug` |
| `vault-notify-proxy` | `vault` | Discord webhook URLs | Destination-neutral notify API in front of Discord |
| `vault-ai-proxy` (extended) | `vault` | AI provider keys | Model routing for two wire protocols, quotas, kill switch |

The detector and investigator are separate pods. If the investigator crashes or is out of memory, `#errors` keeps working. **(operator: separate features)**

Delivery workstreams (one plan each, separate PRs):

| # | Workstream | Repos | Depends on |
|---|---|---|---|
| 0 | Project log migration to contract v2 | `Project_PN`, `nacfson_health`, `nacfson_blog`, this repo (§6 status table) | contract v2 (done) |
| 1 | `error-detector` + `vault-notify-proxy` + `#errors` | this repo | — |
| 2 | `vault-ai-proxy` multi-provider routing | this repo | — |
| 3 | `error-investigator` + `#ai-debug` | this repo | 1, 2 |

## 6. Log contract v2

Already written: [`contracts/CONTRACT_LOG_SCHEMA.md`](../../../contracts/CONTRACT_LOG_SCHEMA.md).

- **Line format:** one JSON object per line with required `time`, `level`, `msg`; optional `event.name`, `trace_id`, `span_id`.
- **Error fields:** `exception.message` / `exception.type` (at least one), `exception.stacktrace`, and the platform extension `error.fingerprint`.
- **Unstructured exceptions:** §4 crash signatures (Go, Python, Node.js, JVM) and nginx native error lines.
- **Migration:** `kind` → `event.name`, with a legacy read during the transition.

The collector still does not parse `message`. The detector parses it at query time.

## 7. Workstream 0: project log migration

| Project | Change |
|---|---|
| `proj-pn` backend | A shared JSON slog logger (`slog.NewJSONHandler`) set as the default, replacing every `slog.NewTextHandler` call. Errors use an `exception.*` helper. `analytics` and `admin` records gain `time` (millisecond precision), `level`, and `msg`, and rename `kind` → `event.name` (`admin`'s `message` → `msg`). The PN analytics and admin schema docs are updated in the same PN PR. |
| `proj-pn` frontend | nginx `log_format` JSON (`escape=json`) with `time`, `level`, `msg`, where `level` comes from `map $status`. The error log stays native. |
| `proj-health` backend | The same JSON slog default and `exception.*` helper in `cmd/api` and `cmd/migrate`. |
| `proj-health` frontend | The same nginx JSON access log. |
| `proj-blog` web | Replace `log.Printf`/`log.Fatalf` with the same JSON slog default and `exception.*` helper (blog-server serves its own web assets, so there is no nginx). |

Each project PR lands in its own repository first. The image-pin update in this repo then follows the existing `update-project-images` flow. The §6 status table in the contract moves each row to `done`. When all rows are `done`, a final PR removes the legacy `kind` read.

## 8. Workstream 1: error-detector and vault-notify-proxy

### 8.1 Input

- A streaming `GET /select/logsql/tail` against `victorialogs.observability.svc:9428`. The query is `{project=~"proj-.*"} | fields _time, _stream_id, project, pod, container, node, message`, with `offset=5s`.
- **Restart without losing or replaying everything.** The last processed `_time` is the cursor. On reconnect, the detector first catches up with `/select/logsql/query` over `[cursor, now)`, capped at 15 minutes back, then tails. On a first start with no cursor it tails from now only (no backfill flood). **(my call)**

### 8.2 Classification (pure function, table-tested)

1. **JSON line:** `level` is matched case-insensitively, with `warning` = `warn` and `critical` = `fatal`. A line is an error when its level is `error` or `fatal`. A legacy `kind` is read as `event.name`.
2. **§4 crash signature:** the line is a `fatal` error. The following non-JSON lines from the same `(pod, container)` are its stack trace, until a JSON line arrives, 200 lines, or 2 s pass.
3. **nginx native error line** (`YYYY/MM/DD HH:MM:SS [level] …`): `[error]` is `error`; `[crit]`, `[alert]`, and `[emerg]` are `fatal`.
4. Anything else is not an error.

### 8.3 Fingerprint

`error.fingerprint` if the line sets it. Otherwise the first 16 hex characters of `sha256(project | container | exception.type | normalize(exception.message or msg))`. For a crash, the normalized text is the first signature line. `normalize` replaces UUIDs, hex runs of 8 or more characters, RFC 3339 timestamps, and digit runs with placeholders, then lowercases. **(my call)**

### 8.4 Group lifecycle (per fingerprint) **(operator: 3-min window; my call: cooldown and caps)**

| State | Trigger | Action |
|---|---|---|
| new | first occurrence, or the first after the cooldown expired | Send `#errors` now. Open the window (`first_seen + 3m`). |
| window open | same fingerprint | Count it. Keep up to 20 sample lines and the set of pods. |
| window closed | `first_seen + 3m` | Hand the group to the investigator. Start the cooldown. |
| cooldown (6 h from window close) | same fingerprint | Count only. No messages. |
| cooldown expired | next occurrence | Treated as new. The `#errors` message says "recurred; N occurrences since last alert". |

- **Burst cap:** at most 10 new-error notifications per minute. Overflow is merged into one "+N more new errors" message.
- **Handoff:** the detector sends `POST http://error-investigator.observability.svc:8080/v1/investigate` with an `ErrorGroup`. If the investigator is unreachable after 3 tries, the detector posts "AI debug skipped: investigator unavailable" to `#errors`. Until workstream 3 lands, the handoff is disabled by config (`INVESTIGATOR_URL` empty).
- **Skip notices are never evicted:** a group that cannot reach the investigator is staged in an unbounded skip outbox, not pushed straight into the bounded notification queue. Pushing would let a burst of skips evict the earlier notices, leaving those groups persisted as handed with nothing saying the investigation never happened. The outbox is rendered into the queue on the sender goroutine: one notice per group while the batch is small and the queue has room for all of it, one "AI debug skipped for N groups" aggregate above that, mirroring how the burst cap collapses overflow. The aggregate names up to 133 fingerprints and always states the count, so a clamped name list never reads as the whole truth.
- **Handed is saved only against evidence:** the tracker marks a group handed as it closes the cycle, so a save between the close and the investigator's acceptance would record an investigation that never happened. Closed cycles with no outcome are tracked per cycle (fingerprint + window end) and written with `Handed: false`, so the next start retries them. At shutdown the notices are rendered into the queue before the snapshot and drained after it, on a budget of their own (`shutdownNotify`) that the handoff drain (`shutdownDrain`) cannot consume.

### 8.5 State

- A JSON file on a 1 GiB `local-path` PVC. It holds the cursor and the per-fingerprint group table.
- It is written atomically (temp file + rename) every 30 s and on SIGTERM. Groups are dropped 7 days after `last_seen`. **(my call)**

### 8.6 Redaction (shared package, also used by workstream 3)

Before anything leaves the cluster, the detector replaces with `[REDACTED]`:
- `Bearer …` tokens
- JWTs (`eyJ…`)
- `AIza…` keys
- `sk-…` keys
- the values of keys matching `(?i)(password|passwd|secret|token|api[_-]?key|authorization|cookie)`

### 8.7 vault-notify-proxy

- **Data listener `:8080`:**
  - `POST /v1/notify/{channel}` with a destination-neutral `Notification` JSON: `title`, `severity`, `fields` (ordered key/value pairs), `body` (markdown), and an optional `attachment` (`name`, `content`).
  - `GET /startupz`, `/readyz`, `/healthz`.
- **Admin listener `127.0.0.1:8081` only:**
  - `POST /admin/set-webhook?channel=…` writes `kv/notify/discord/<channel>` (field `url`) to OpenBao and hot-loads it.
  - `GET /admin/status`.
- **Caller identity:** the same two layers as `vault-ai-proxy`: a NetworkPolicy, plus pod-IP attribution through the API server. Grants are channel → allowed `(namespace, app label)`:
  - `errors` ← `observability/error-detector`
  - `ai-debug` ← `observability/error-investigator`
- **Discord rendering:**
  - One embed per notification. The description is capped at 4,000 characters. Longer bodies or attachments are sent as a multipart file (`.log` or `.md`).
  - On `429`: wait `retry_after`, up to 3 tries.
  - On `5xx`: back off 1 s, 2 s, 4 s.
  - Then return `502` to the caller, who logs it.
- **Network:** egress only to OpenBao, the API server, DNS, and the exact FQDN `discord.com` (CiliumNetworkPolicy).
- **Startup self-test:** the same pattern as the AI proxy. Admin is loopback-only, and the data mux has no admin routes.
- **Operator CLI:** `scripts/notify-ctl.sh set-webhook <channel>` and `status`. It is an SSH-capable helper, so it is added to rule 9 in AGENTS.md and to `scripts/universal-agent-hook.py`.

### 8.8 Shared code

Pod-IP attribution and the OpenBao Kubernetes login move out of `cmd/ai-proxy/main.go` into `services/vault-proxies/internal/podid` and `internal/openbao`. Both proxies use them, and the Dockerfiles copy `internal/`.

## 9. Workstream 2: vault-ai-proxy multi-provider

- **Routing table** in ConfigMap `ai-proxy-routing` (JSON), replacing `UPSTREAM_AI_BASE_URL` and `ALLOWED_PROJECT_GRANTS`:
  - `providers`: `name`, `base_url`, `fqdn`, `key_path` (`kv/ai/providers/<name>`).
  - `models`: `id` as `provider/model`, `upstream_model`, `protocol` (`openai-chat` or `anthropic-messages`), `tools` (bool), `context_tokens`.
  - `aliases`: an unprefixed `gemini-3.1-flash-lite` maps to `gemini/gemini-3.1-flash-lite`, so `pn-backend` keeps working unchanged.
  - `grants`: `name`, `namespace`, `pod_label`, `models`, `daily_limit`.
- **Endpoints:**
  - `POST /v1/chat/completions` for `openai-chat` models and `POST /v1/messages` for `anthropic-messages` models. A protocol mismatch returns `400`.
  - The proxy rewrites `model` to `upstream_model`, adds the provider key, and forwards the body unchanged. There is no protocol translation in the proxy.
- **Typed errors**, so callers can decide on fallback:

| Condition | Status | `error` |
|---|---|---|
| Per-grant quota reached | 403 | `quota_exceeded` |
| Kill switch stopped | 403 | `disabled` |
| Model not allowed | 403 | `model_not_allowed` |
| Provider `429` | 429 | `provider_limit` (+ `retry_after`) |
| Provider `5xx`, timeout, or unreachable | 502 | `provider_error` |
| Provider key not seeded | 503 | `provider_unconfigured` |

- **Egress:** `ai-proxy-egress` lists every provider FQDN with exact `matchName`. The preflight rule is changed from "Gemini only" to "exactly the FQDNs in `ai-proxy-routing`".
- **Admin:**
  - `set-key` takes `?provider=`.
  - The dashboard shows each provider's key status and per-grant usage.

## 10. Workstream 3: error-investigator

- **Input:** `POST /v1/investigate` with an `ErrorGroup`. The bounded queue holds 8 groups and the investigator runs one at a time. When the queue is full, the group is reported to `#ai-debug` as "analysis skipped: queue full".
- **Evidence bundle** (built by code, no AI):
  - the group's samples, count, and pods
  - ±50 lines around the first occurrence from the same pod
  - pod status (restarts, last termination reason)
  - pod events for the last 30 minutes
  - ReplicaSet rollout history (image and creation time) for the owning Deployment
- **Agent loop:** up to **8** model calls, 5 minutes wall clock, and an evidence cap of 200 KB per request. Read-only tools:

| Tool | Reads |
|---|---|
| `query_logs(query, start, end, limit≤200)` | VictoriaLogs, limited to the group's project unless the query names another `proj-*` |
| `get_pod_status(pod)` | Pods in `proj-*` |
| `get_events(pod or deployment, since)` | Events in `proj-*` |
| `get_rollout_history(deployment)` | ReplicaSets in `proj-*` |
| `compare_windows(query, before, after)` | Counts from VictoriaLogs |
| `finish(report)` | Ends the loop with the structured report |

- **RBAC:** a ClusterRole with `get`/`list` on `pods`, `events`, and `replicasets`, bound per project namespace (by a RoleBinding in each `proj-*` namespace). It has no access to `vault`, `identity`, or `Secret`s.
- **Models:** an ordered fallback chain in a ConfigMap. A model with `tools: false` gets the evidence bundle and a single call. Fallback triggers only on `429`, `5xx`, timeouts, `provider_*`, `quota_exceeded`, or `disabled`. The next model continues from the transcript so far. **(operator: fallback chain; my call: continuation)** A protocol adapter in the investigator speaks both `openai-chat` and `anthropic-messages`.
- **Report:** summary, likely cause, confidence (`high`, `medium`, `low`), suggested fix, cited evidence (log lines or events, quoted), checks it couldn't run, the model that answered, and the models skipped with their reasons.
- **`#ai-debug` outcomes** **(operator)**:
  - **Success:** the conclusion embed plus the full report as `.md`.
  - **Every model failed:** the error logs plus `analysis failed: <reason per model>`.
- **AI grant:** `error-investigator` in namespace `observability`, with its own daily limit and kill switch in the AI proxy.
- **Redaction:** the shared package runs before every model call.

## 11. Error handling summary

| Failure | Effect |
|---|---|
| VictoriaLogs down | The detector's readiness fails. It reconnects with backoff and catches up from the cursor on recovery (≤ 15 min). |
| Notify proxy down or Discord down | The detector keeps up to 100 pending notifications in memory and retries every 30 s. The oldest are dropped first, each drop is logged to the detector's stderr, and nothing is lost silently. |
| Investigator down | `#errors` gets "AI debug skipped: investigator unavailable". A shutdown that leaves many groups unposted sends one "AI debug skipped for N groups" aggregate instead of one message per group. |
| All models fail | `#ai-debug` gets the error logs plus the failure reasons. |
| Detector restart | State is loaded from the PVC, so there's no duplicate "new" alert for a known fingerprint. |

## 12. Testing

- **Go unit tests per package:**
  - classify (table tests per contract section)
  - fingerprint and normalize
  - the group state machine (a fake clock drives the 3-min window and the 6-h cooldown)
  - state persistence (round trip and atomic write)
  - redaction
  - notify proxy (identity grants, loopback admin, Discord 429 and multipart, self-test)
  - AI proxy routing (both protocols, typed errors, aliases)
  - investigator loop (a fake model with scripted tool calls, the step cap, fallback continuation, all-fail report)
- **Contract guard:** a Go test in the detector's `classify` package reads contract §4 and fails if its crash-signature or nginx tables drift. A `tests/observability` test fails if a `deploy/projects/<slug>` has no §6 status row. The existing envelope test stays.
- **Preflight:** rules for `vault-notify-proxy` (loopback admin, probes, egress `discord.com` only) and an updated AI egress rule.
- **render-validate** for both environments, plus the handbook sync check.

## 13. Resources **(my call)**

Each new pod sets `requests == limits`. The observability reservation is not yet measured; these are counted by preflight from the manifests.

| Pod | CPU | Memory |
|---|---|---|
| `error-detector` | 50m | 64Mi |
| `error-investigator` | 50m | 96Mi |
| `vault-notify-proxy` | 25m | 32Mi |

## 14. Rejected

- **vmalert + Alertmanager for detection.** Two more components, and a webhook URL in Alertmanager config conflicts with zero-plaintext.
- **Projects POSTing errors to an ingest endpoint (Sentry-style).** Misses hard crashes, needs project credentials, and conflicts with the stdout-only collection contract.
- **LiteLLM as the AI gateway.** Python, around 300–500 Mi, and keys in env or Secrets.
- **Protocol translation inside the AI proxy.** Only the investigator needs it.
- **Detector reading Discord URLs from OpenBao directly.** Breaks the "credentials only in vault proxies" pattern.
- **A manual investigation trigger.** **(operator)**
