# Workstream 1: Error Detector, Notify Proxy, and `#errors` Webhook — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every new error in a `proj-*` log reaches Discord `#errors` within seconds, grouped by fingerprint, with a 3-minute collection window and a 6-hour cooldown, and with no secret outside the `vault` namespace.

**Architecture:** A new Go stdlib module `services/error-pipeline` holds the `error-detector` binary. It tails VictoriaLogs, classifies lines against log contract v2, redacts, fingerprints, tracks groups, persists state on a PVC, and posts destination-neutral notifications. A new `vault-notify-proxy` in `services/vault-proxies` holds the Discord webhook URLs (OpenBao), authenticates callers by pod IP the same way `vault-ai-proxy` does, and renders Discord messages. The detector-to-investigator handoff ships disabled (`INVESTIGATOR_URL` empty) until workstream 3.

**Tech Stack:** Go 1.22 standard library only; VictoriaLogs v1.53.0 `/select/logsql/tail` and `/select/logsql/query`; Discord webhooks (JSON and `multipart/form-data`); OpenBao KV v2 with Kubernetes auth; Kustomize/Flux; Cilium `toFQDNs`; Python `unittest` for preflight and manifests.

**Spec:** `docs/superpowers/specs/2026-10-29-error-pipeline-design.md` (§4, §5, §8, §11, §12, §13) and `contracts/CONTRACT_LOG_SCHEMA.md` v2.

## Global Constraints

- Go 1.22 standard library only; `go.mod` has no `reqhttps://github.com/NextKde/NextKdeuire` block in either module.
- No Discord URL, token, or key in Git, env vars, ConfigMaps, or Kubernetes `Secret`s; only in OpenBao `kv/notify/discord/<channel>` (field `url`) and in `vault-notify-proxy` memory.
- Admin listener `127.0.0.1:8081` only; the data mux on `:8080` has no `/admin/*` route.
- Images pinned by `@sha256:` digest in manifests (never `:latest`).
- 3-minute window from first occurrence (fixed, not sliding); 6-hour cooldown from window close; 20 samples per group; groups dropped 7 days after `last_seen`.
- Burst cap: 10 new-error notifications per minute; overflow merged into one "+N more new errors" message.
- Pending notification queue: 100, retry every 30 s, oldest dropped first with a stderr line per drop.
- Catch-up after restart: from the saved cursor, at most 15 minutes back; first start (no cursor) tails from now.
- Redaction before anything leaves the cluster: `Bearer …`, JWT `eyJ…`, `AIza…`, `sk-…`, and values of keys matching `(?i)(password|passwd|secret|token|api[_-]?key|authorization|cookie)`.
- Resources (`requests == limits`): `error-detector` 50m / 64Mi; `vault-notify-proxy` 25m / 32Mi.
- Any change under `deploy/`, `clusters/`, `services/`, `contracts/`, `scripts/platform-preflight.py` updates `docs/platform-architecture.html` in the same commit.
- Two PRs: **1a** (Tasks 1–11, code and image build) merges first so the images exist; **1b** (Tasks 12–14) pins those digests and deploys.

## Review Focus

1. **A crash stack interleaved with another container's lines.** Two pods panic at once; their stack lines arrive interleaved in the tail → the assembler keys by `(pod, container)`; `TestAssemblerKeepsConcurrentCrashesApart` in Task 5.
2. **Detector restart in the middle of a 3-minute window.** The window must still close and the error must not be re-announced as new → state saves `WindowEnd`; `TestRestartMidWindowClosesOnceWithoutReannounce` in Task 8.
3. **A 2 MB single log line or a 5,000-line stack.** Must not blow the 64Mi limit or Discord's limits → classify caps a line at 64 KiB and a stack at 200 lines; the notify renderer moves anything over 4,000 characters into an attachment capped at 1 MiB; `TestOversizeLineTruncated` (Task 5) and `TestLongBodyBecomesAttachment` (Task 2).
4. **Discord down for an hour.** The detector must not grow unbounded or lose errors silently → bounded queue of 100 with drop logging; `TestQueueDropsOldestAndLogs` in Task 10.
5. **A spoofed caller on the notify proxy.** A project pod that reaches `:8080` must not post to `#errors` → grant check by pod-IP attribution; `TestUngrantedPodRejected` in Task 3, plus the NetworkPolicy preflight rule in Task 12.

---

## PR 1a — code and images

### Task 1: Extract pod-IP attribution and OpenBao login into shared packages

**Files:**
- Create: `services/vault-proxies/internal/podid/podid.go`, `podid_test.go`
- Create: `services/vault-proxies/internal/openbao/openbao.go`, `openbao_test.go`
- Modify: `services/vault-proxies/cmd/ai-proxy/main.go` (`resolvePodIPFromKubeAPIServer` at :903, `buildK8sHTTPClient` at :959, `extractPeerIP` at :975, `loginOpenBaoK8s` at :711, and the KV read/write helpers become calls into the packages)
- Modify: `services/vault-proxies/Dockerfile.ai-proxy`, `Dockerfile.db-proxy`, `Dockerfile.registry-proxy` (add `COPY internal/ internal/` after `COPY cmd/ cmd/`)

**Interfaces:**
- Produces:
  - `podid.Identity{Namespace, PodName, AppLabel string}` (same JSON tags as today's `PodIdentity`)
  - `podid.NewResolver(apiServer string, client *http.Client, tokenPath string, ttl time.Duration) *podid.Resolver`; `(*Resolver).Resolve(ctx context.Context, ip string) (*podid.Identity, error)` (60 s cache by IP, as today)
  - `podid.PeerIP(remoteAddr string) string`
  - `podid.InClusterHTTPClient() *http.Client`
  - `openbao.Client{Addr, Role, JWTPath string; HTTP *http.Client}`; `(*Client).Login(ctx) (token string, err error)`; `(*Client).ReadKV(ctx, token, path string) (map[string]any, error)`; `(*Client).WriteKV(ctx, token, path string, data map[string]any) error` (KV v2, `path` like `notify/discord/errors` under mount `kv`)

- [x] **Step 1: Write the failing tests** — `podid_test.go`: an `httptest` API server returns a pod list for `fieldSelector=status.podIP=10.42.0.7`; `Resolve` returns `{proj-pn, pn-backend-1, pn-backend}`; a second call within the TTL makes no HTTP request (counter). `openbao_test.go`: a fake OpenBao asserts `POST /v1/auth/kubernetes/login` with `{"role":..,"jwt":..}`, then `GET /v1/kv/data/notify/discord/errors` with `X-Vault-Token`; `ReadKV` returns `{"url": "https://discord.com/api/webhooks/1/x"}`; `WriteKV` sends `{"data":{...}}`.
- [x] **Step 2: Run them to verify they fail** — `cd services/vault-proxies && go test ./internal/...` → FAIL (packages missing).
- [x] **Step 3: Move the code** from `cmd/ai-proxy/main.go` into the packages without changing behavior; `ai-proxy` keeps `PodIdentity` as a type alias `type PodIdentity = podid.Identity` so its tests compile unchanged.
- [x] **Step 4: Run every test** — `cd services/vault-proxies && go test ./... && go vet ./...` → PASS (all existing `ai-proxy` tests unchanged and green); `docker build -f Dockerfile.ai-proxy .` → builds.
- [x] **Step 5: Commit**

```bash
git add services/vault-proxies
git commit -m "refactor(vault-proxies): share pod-IP attribution and OpenBao login"
```

### Task 2: Notify proxy — notification model and Discord renderer

**Files:**
- Create: `services/vault-proxies/cmd/notify-proxy/notification.go`, `discord.go`, `discord_test.go`
- Create: `services/error-pipeline/internal/notify/testdata/notification.json` (golden payload, shared with Task 10)

**Interfaces:**
- Produces:
  - `Notification{Title string "title"; Severity string "severity"; Fields []Field "fields"; Body string "body"; Attachment *Attachment "attachment,omitempty"}`, `Field{Name, Value string}`, `Attachment{Name, Content string}`
  - `(Notification).Validate() error` — `title` 1–256 chars; `severity` ∈ `error`, `fatal`, `info`; ≤ 25 fields; field name ≤ 256, value ≤ 1024; attachment content ≤ 1 MiB
  - `RenderDiscord(n Notification) (payload []byte, contentType string, err error)` — one embed (`title`, `description` = `body`, `color` 0xE74C3C for `error`, 0x8E44AD for `fatal`, 0x3498DB for `info`, `fields` inline=false). If `body` > 4,000 chars, the description becomes the first 3,900 chars + `"\n… (full text attached)"` and the full body is attached as `body.md`. With any attachment the result is `multipart/form-data` with `payload_json` and `files[0]`.
  - `SendDiscord(ctx context.Context, client *http.Client, webhookURL string, n Notification) error` — on `429` sleeps `retry_after` (seconds, from the JSON body) up to 3 tries; on `5xx` backs off 1 s, 2 s, 4 s; then returns an error.

- [x] **Step 1: Write the failing tests**

```go
func TestGoldenNotificationValidates(t *testing.T) {
	b, _ := os.ReadFile("../../../error-pipeline/internal/notify/testdata/notification.json")
	var n Notification
	if err := json.Unmarshal(b, &n); err != nil || n.Validate() != nil { t.Fatal(err, n.Validate()) }
}
func TestLongBodyBecomesAttachment(t *testing.T) {
	n := Notification{Title: "t", Severity: "error", Body: strings.Repeat("x", 5000)}
	p, ct, _ := RenderDiscord(n)
	if !strings.HasPrefix(ct, "multipart/form-data") || !bytes.Contains(p, []byte(`filename="body.md"`)) { t.Fatal(ct) }
}
func TestDiscord429RetriesAfter(t *testing.T) { /* httptest server: 429 {"retry_after":0.01} then 204; expect 2 calls, nil error */ }
func TestDiscord5xxGivesUpAfterThreeBackoffs(t *testing.T) { /* always 503; expect 4 calls and an error; backoff via injectable sleep */ }
```

The golden file is a realistic new-error notification (title `New error in proj-pn/pn-backend`, severity `error`, fields `Project`, `Container`, `Pods`, `First seen`, `Fingerprint`, a fenced log body).

- [x] **Step 2: Run them to verify they fail** — `go test ./cmd/notify-proxy/...` → FAIL.
- [x] **Step 3: Implement** the types and both functions. `SendDiscord` takes its sleep function from a package variable `sleep = time.Sleep` so tests replace it.
- [x] **Step 4: Run the tests** → PASS.
- [x] **Step 5: Commit**

```bash
git add services/vault-proxies/cmd/notify-proxy services/error-pipeline/internal/notify/testdata
git commit -m "feat(notify-proxy): destination-neutral notification and Discord renderer"
```

### Task 3: Notify proxy — server, grants, OpenBao webhook store, self-test

**Files:**
- Create: `services/vault-proxies/cmd/notify-proxy/server.go`, `server_test.go`, `main.go`
- Create: `services/vault-proxies/Dockerfile.notify-proxy` (copy of `Dockerfile.ai-proxy` with `./cmd/notify-proxy`, binary `/vault-notify-proxy`)
- Modify: `.github/workflows/build-images.yaml` (matrix entry `vault-notify-proxy`)

**Interfaces:**
- Consumes: `podid.Resolver`, `podid.PeerIP`, `openbao.Client` (Task 1); `Notification`, `SendDiscord` (Task 2).
- Produces:
  - `Grant{Channel, Namespace, AppLabel string}`; `ParseGrants(s string) ([]Grant, error)` from env `NOTIFY_GRANTS="errors:observability:error-detector,ai-debug:observability:error-investigator"`
  - `NotifyProxyConfig{DataAddr, AdminAddr string; Grants []Grant; Resolve func(ctx, ip string) (*podid.Identity, error); Vault VaultStore; Send func(ctx, url string, n Notification) error}`
  - `VaultStore` interface: `LoadWebhook(ctx, channel string) (string, error)`; `SaveWebhook(ctx, channel, url string) error`
  - `NewNotifyProxy(cfg NotifyProxyConfig) *NotifyProxy`; `DataMux() http.Handler`; `AdminMux() http.Handler`; `RunStartupSelfTest() error`
  - Data routes: `POST /v1/notify/{channel}` → `202` on delivery, `400` invalid body (≤ 2 MiB), `403` caller not granted for that channel, `404` unknown channel, `502` delivery failed, `503` webhook not configured; `GET /startupz` `/readyz` `/healthz`
  - Admin routes: `POST /admin/set-webhook?channel=<c>` (body `{"url":"https://discord.com/api/webhooks/..."}`; reject any host other than `discord.com`), `GET /admin/status` (`[{channel, configured bool, sent, failed int}]`); never echo the URL

- [x] **Step 1: Write the failing tests** — `TestGrantedPodDelivers` (resolver returns `observability/error-detector`, channel `errors`, fake `Send` records the URL loaded from a fake `VaultStore`; expect `202`); `TestUngrantedPodRejected` (`proj-pn/pn-backend` → `403`, `Send` not called); `TestWrongChannelForCallerRejected` (`error-detector` posting to `ai-debug` → `403`); `TestAdminRoutesAbsentOnDataMux` (`POST /admin/set-webhook` on `DataMux()` → `404`); `TestSetWebhookRejectsNonDiscordHost` (`https://evil.example/x` → `400`); `TestStatusNeverLeaksURL`; `TestStartupSelfTestFailsOnNonLoopbackAdmin` (`AdminAddr` `0.0.0.0:8081` → `/startupz` `500`).
- [x] **Step 2: Run them to verify they fail** — `go test ./cmd/notify-proxy/...` → FAIL.
- [x] **Step 3: Implement** following `cmd/ai-proxy` structure: two muxes, `isStrictLoopbackAddr`, the startup self-test (loopback admin, no admin route on data mux, ungranted caller gets `403`), webhooks cached in memory after the first `LoadWebhook`, `main` wiring env `DATA_ADDR`, `ADMIN_ADDR`, `OPENBAO_ADDR`, `OPENBAO_ROLE=vault-notify-proxy`, `NOTIFY_GRANTS`. Add the build matrix entry.
- [x] **Step 4: Run** — `cd services/vault-proxies && go test -race ./... && go vet ./...` → PASS; `docker build -f Dockerfile.notify-proxy .` → builds.
- [x] **Step 5: Commit**

```bash
git add services/vault-proxies .github/workflows/build-images.yaml
git commit -m "feat(notify-proxy): pod-attributed notify API with loopback webhook admin"
```

### Task 4: error-pipeline module and redaction

**Files:**
- Create: `services/error-pipeline/go.mod` (`module nacfson_pipeline/services/error-pipeline`, `go 1.22`)
- Create: `services/error-pipeline/internal/redact/redact.go`, `redact_test.go`

**Interfaces:**
- Produces: `redact.String(s string) string`

- [x] **Step 1: Write the failing table test** `TestRedact` with these cases (input → output):
  - `Authorization: Bearer abc.def.ghi` → `Authorization: [REDACTED]`
  - `token=eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.sig` → `token=[REDACTED]`
  - `key AIzaSyA1234567890abcdefghijklmnopqrstuv` → `key [REDACTED]`
  - `sk-proj-abcdefghijklmnopqrstuvwxyz` → `[REDACTED]`
  - `{"password":"hunter2","user":"a"}` → `{"password":"[REDACTED]","user":"a"}`
  - `password=hunter2 user=a` → `password=[REDACTED] user=a`
  - `no secrets here` → unchanged
- [x] **Step 2: Run it to verify it fails** — `cd services/error-pipeline && go test ./internal/redact/...` → FAIL.
- [x] **Step 3: Implement** with precompiled `regexp`s applied in order: key-value (JSON `"k":"v"` and `k=v`) first, then `Bearer \S+`, JWT `eyJ[\w-]+\.[\w-]+\.[\w-]+`, `AIza[\w-]{30,}`, `sk-[\w-]{16,}`.
- [x] **Step 4: Run** → PASS.
- [x] **Step 5: Commit**

```bash
git add services/error-pipeline
git commit -m "feat(error-pipeline): module scaffold and outbound redaction"
```

### Task 5: Classification against contract v2

**Files:**
- Create: `services/error-pipeline/internal/classify/classify.go`, `assembler.go`, `classify_test.go`, `contract_test.go`

**Interfaces:**
- Consumes: `vlogs.Record` — define it here first as `classify.Record{Time time.Time; StreamID, Project, Pod, Container, Node, Message string}`; Task 9 decodes into this type.
- Produces:
  - `Event{Level, Msg, EventName, TraceID, ExceptionType, ExceptionMessage, Stacktrace, Fingerprint string; Source string}` with JSON tags matching the contract keys; `Source` ∈ `json`, `crash`, `nginx`
  - `Line(message string) (ev Event, kind LineKind)` — pure; `LineKind` ∈ `NotError`, `Error`, `CrashStart`, `Plain`
  - `CrashSignatures []string` and `NginxLevels map[string]string` — exported tables, read by `contract_test.go`
  - `Classified{Record Record; Event Event}`
  - `NewAssembler(maxLines int, maxAge time.Duration) *Assembler`; `(*Assembler).Feed(r Record) []Classified`; `(*Assembler).Flush(now time.Time) []Classified`

- [x] **Step 1: Write the failing tests**

```go
func TestLine(t *testing.T) {
	cases := []struct{ in string; level string; kind LineKind }{
		{`{"time":"2026-10-29T09:00:00.000Z","level":"ERROR","msg":"x","exception.message":"boom"}`, "error", Error},
		{`{"time":"…","level":"critical","msg":"x"}`, "fatal", Error},
		{`{"time":"…","level":"warning","msg":"x"}`, "warn", NotError},
		{`{"time":"…","level":"info","kind":"admin","msg":"x"}`, "info", NotError},
		{`panic: runtime error: index out of range [5] with length 3`, "fatal", CrashStart},
		{`fatal error: concurrent map writes`, "fatal", CrashStart},
		{`Traceback (most recent call last):`, "fatal", CrashStart},
		{`Exception in thread "main" java.lang.NullPointerException`, "fatal", CrashStart},
		{`2026/10/29 09:00:00 [error] 12#12: *1 connect() failed`, "error", Error},
		{`2026/10/29 09:00:00 [crit] 12#12: something`, "fatal", Error},
		{`2026/10/29 09:00:00 [notice] 1#1: start worker`, "", NotError},
		{`level=error msg=x`, "", Plain}, // logfmt is not a contract format
	}
	// assert ev.Level == c.level && kind == c.kind
}
func TestLegacyKindReadAsEventName(t *testing.T) { /* {"kind":"admin",...} → ev.EventName == "admin" */ }
func TestNestedExceptionEqualsDotted(t *testing.T) { /* {"exception":{"message":"m"}} → ExceptionMessage == "m" */ }
func TestOversizeLineTruncated(t *testing.T) { /* 2 MB message → Msg/ExceptionMessage ≤ 64 KiB */ }
func TestAssemblerCollectsStackUntilJSON(t *testing.T) { /* panic line + 3 goroutine lines + JSON line → one Classified with 3-line Stacktrace */ }
func TestAssemblerKeepsConcurrentCrashesApart(t *testing.T) { /* pod a and pod b interleaved → two Classified, each with only its own lines */ }
func TestAssemblerFlushesAfterMaxAge(t *testing.T) { /* panic + 1 line, Flush(now+2s) → emitted */ }
func TestAssemblerCapsAt200Lines(t *testing.T) {}
```

`contract_test.go` reads `../../../../contracts/CONTRACT_LOG_SCHEMA.md`, extracts the §4 runtime table's backticked prefixes and the nginx error-log mapping, and asserts they equal `CrashSignatures` and `NginxLevels`.

- [x] **Step 2: Run them to verify they fail** — `go test ./internal/classify/...` → FAIL.
- [x] **Step 3: Implement.** `Line`: if the message starts with `{`, unmarshal into `map[string]any`, flatten nested objects with `.`-joined keys, then read keys; `level` lowercased with aliases `warning`→`warn`, `critical`→`fatal`; `error`/`fatal` → `Error`. Else, check `CrashSignatures` by prefix → `CrashStart`. Else, match nginx `^\d{4}/\d\d/\d\d \d\d:\d\d:\d\d \[(\w+)\]` and map with `NginxLevels`. Else `Plain`. `Assembler` keeps one open crash per `pod+"/"+container`, appends `Plain` lines from that key, and closes on a JSON line, `maxLines` (200), or `maxAge` (2 s).
- [x] **Step 4: Run** → PASS.
- [x] **Step 5: Commit**

```bash
git add services/error-pipeline/internal/classify
git commit -m "feat(error-pipeline): classify log lines against contract v2"
```

### Task 6: Fingerprint

**Files:**
- Create: `services/error-pipeline/internal/fingerprint/fingerprint.go`, `fingerprint_test.go`

**Interfaces:**
- Consumes: `classify.Classified`.
- Produces: `fingerprint.Normalize(s string) string`; `fingerprint.Of(c classify.Classified) string` — `c.Event.Fingerprint` if set, else first 16 hex of `sha256(project + "|" + container + "|" + exceptionType + "|" + Normalize(text))`, where `text` is `ExceptionMessage`, else `Msg`, else (crash) the first signature line.

- [x] **Step 1: Write the failing tests** — `TestNormalize` cases: `user 8f14e45f-ceea-467f-a9a0-4b8ad3e2f1a3 not found` → `user <uuid> not found`; `timeout after 1503ms` → `timeout after <n>ms`; `at 2026-10-29T09:00:00.123Z` → `at <time>`; `tx deadbeefcafebabe` → `tx <hex>`; `Index Out Of Range [5]` → `index out of range [<n>]`. `TestSameErrorDifferentIDsSameFingerprint`; `TestDifferentContainerDifferentFingerprint`; `TestProjectFingerprintWins`; `TestFingerprintIs16Hex`.
- [x] **Step 2: Run them to verify they fail.**
- [x] **Step 3: Implement** — apply in order: RFC 3339 timestamps → `<time>`, UUIDs → `<uuid>`, hex runs of 8 or more characters → `<hex>`, digit runs → `<n>`, then lowercase.
- [x] **Step 4: Run** → PASS.
- [x] **Step 5: Commit**

```bash
git add services/error-pipeline/internal/fingerprint
git commit -m "feat(error-pipeline): stable error fingerprints"
```

### Task 7: Group tracker (window, cooldown, recurrence)

**Files:**
- Create: `services/error-pipeline/internal/group/group.go`, `group_test.go`

**Interfaces:**
- Consumes: `classify.Classified`, `fingerprint.Of`.
- Produces:
  - `Group{Fingerprint, Project, Container string; Event classify.Event; FirstSeen, LastSeen, WindowEnd, CooldownUntil time.Time; Count, SinceLastAlert int; Pods []string; Samples []string; Handed bool}` with snake_case JSON tags. This JSON is the `ErrorGroup` that workstream 3's `POST /v1/investigate` accepts.
  - `Config{Window, Cooldown, Retention time.Duration; MaxSamples int}`; `DefaultConfig()` = 3m, 6h, 168h, 20
  - `NewTracker(cfg Config) *Tracker`
  - `(*Tracker).Observe(c classify.Classified, now time.Time) []Action`
  - `(*Tracker).Tick(now time.Time) []Action`
  - `Action{Type ActionType; Group Group; Recurred bool}`, where `ActionType` ∈ `NotifyNew`, `Handoff`
  - `(*Tracker).Export() []Group`; `(*Tracker).Import(gs []Group)`

- [x] **Step 1: Write the failing tests** (a fixed `t0` drives time):
  - `TestFirstOccurrenceNotifiesImmediately` — `Observe` at `t0` → one `NotifyNew`, `Recurred` false.
  - `TestRepeatsInsideWindowAreCountedNotNotified` — 5 more at `t0+1m` → no actions; `Count` 6; `Pods` deduplicated.
  - `TestWindowIsFixedNotSliding` — occurrences every 30 s; `Tick(t0+3m)` → exactly one `Handoff` with every occurrence counted.
  - `TestSingleOccurrenceStillHandedOff` — one occurrence, `Tick(t0+3m)` → `Handoff`.
  - `TestCooldownSuppresses` — after handoff, occurrences at `+1h`, `+5h` → no actions; `SinceLastAlert` 2.
  - `TestRecurrenceAfterCooldown` — occurrence at `t0+3m+6h+1s` → `NotifyNew` with `Recurred` true and `SinceLastAlert` 2, then a new window.
  - `TestSamplesCappedAt20`.
  - `TestRetentionDropsGroupsAfter7Days`.
- [x] **Step 2: Run them to verify they fail.**
- [x] **Step 3: Implement** the state table in spec §8.4 with a `map[string]*Group`. `Tick` returns a `Handoff` for every group with `!Handed && now >= WindowEnd`, sets `Handed`, `CooldownUntil = WindowEnd + Cooldown`, and drops groups whose `LastSeen + Retention < now`.
- [x] **Step 4: Run** → PASS.
- [x] **Step 5: Commit**

```bash
git add services/error-pipeline/internal/group
git commit -m "feat(error-pipeline): fingerprint groups with 3m window and 6h cooldown"
```

### Task 8: State persistence

**Files:**
- Create: `services/error-pipeline/internal/state/state.go`, `state_test.go`

**Interfaces:**
- Consumes: `group.Group`, `(*Tracker).Export/Import`.
- Produces: `Snapshot{Version int "version"; Cursor time.Time "cursor"; Groups []group.Group "groups"}`; `state.Load(path string) (Snapshot, error)`, which returns an empty snapshot and no error when the file is missing; `state.Save(path string, s Snapshot) error`, which writes `path+".tmp"`, fsyncs, then renames.

- [x] **Step 1: Write the failing tests** — `TestRoundTrip`; `TestMissingFileIsEmpty`; `TestCorruptFileReturnsError` (and the detector starts fresh after logging it — asserted in Task 11); `TestSaveIsAtomic` (`.tmp` absent after `Save`); `TestRestartMidWindowClosesOnceWithoutReannounce` — tracker observes at `t0`, export → save → load → import into a new tracker, observe the same fingerprint at `t0+1m` → no `NotifyNew`; `Tick(t0+3m)` → one `Handoff`.
- [x] **Step 2: Run them to verify they fail.**
- [x] **Step 3: Implement** with `encoding/json`, `os.CreateTemp` in the same directory, `f.Sync()`, `os.Rename`.
- [x] **Step 4: Run** → PASS.
- [x] **Step 5: Commit**

```bash
git add services/error-pipeline/internal/state
git commit -m "feat(error-pipeline): atomic detector state on disk"
```

### Task 9: VictoriaLogs client (tail and catch-up)

**Files:**
- Create: `services/error-pipeline/internal/vlogs/vlogs.go`, `vlogs_test.go`

**Interfaces:**
- Consumes: `classify.Record`.
- Produces:
  - `Client{BaseURL string; HTTP *http.Client}`
  - `const Query = "{project=~\"proj-.*\"} | fields _time, _stream_id, project, pod, container, node, message"`
  - `(*Client).CatchUp(ctx context.Context, from, to time.Time, fn func(classify.Record) error) error` — `POST /select/logsql/query` form `query`, `start`, `end` (RFC 3339), with ` | sort by (_time)` appended
  - `(*Client).Tail(ctx context.Context, fn func(classify.Record) error) error` — `GET /select/logsql/tail?query=…&offset=5s`; reads JSON lines until EOF or `ctx` is cancelled
  - `(*Client).Ready(ctx) error` — `GET /health`

- [x] **Step 1: Write the failing tests** — an `httptest` server returns JSON lines `{"_time":"2026-10-29T09:00:00.123Z","_stream_id":"s1","project":"proj-pn","pod":"p","container":"c","node":"n","message":"{…}"}`; `TestTailDecodesRecords`; `TestCatchUpSendsRangeAndSort` (asserts form values); `TestTailStopsOnContextCancel`; `TestMalformedLineSkippedNotFatal`.
- [x] **Step 2: Run them to verify they fail.**
- [x] **Step 3: Implement** with `bufio.Scanner`, buffer max 1 MiB; a line over that is skipped and counted.
- [x] **Step 4: Run** → PASS.
- [x] **Step 5: Commit**

```bash
git add services/error-pipeline/internal/vlogs
git commit -m "feat(error-pipeline): VictoriaLogs tail and catch-up client"
```

### Task 10: Notify client, burst limiter, pending queue, message rendering

**Files:**
- Create: `services/error-pipeline/internal/notify/notify.go`, `queue.go`, `render.go`, `notify_test.go`

**Interfaces:**
- Consumes: `group.Group`, `redact.String`; golden `testdata/notification.json` (Task 2).
- Produces:
  - `Notification`, `Field`, `Attachment` — same JSON shape as Task 2 (the golden file proves it)
  - `Client{BaseURL string; HTTP *http.Client}`; `(*Client).Send(ctx, channel string, n Notification) error` (`POST {BaseURL}/v1/notify/{channel}`; non-`202` is an error)
  - `NewQueue(max int, send func(context.Context, Notification) error, logf func(string, ...any)) *Queue`; `(*Queue).Push(n Notification)`; `(*Queue).Drain(ctx)` (one pass, stops at the first failure); `(*Queue).Len() int`
  - `NewLimiter(perMinute int) *Limiter`; `(*Limiter).Allow(now time.Time) bool`; `(*Limiter).TakeOverflow() int`
  - `RenderNew(g group.Group) Notification` — title `New error in <project>/<container>` (or `Recurring error in …` with "N occurrences since last alert"); fields `Project`, `Container`, `Pods`, `First seen` (RFC 3339), `Level`, `Fingerprint`; body = redacted `exception.type: exception.message`, then the samples and the stack trace in a fenced block
  - `RenderOverflow(n int, since time.Time) Notification`
  - `RenderInvestigatorUnavailable(g group.Group) Notification` (`AI debug skipped: investigator unavailable`)

- [x] **Step 1: Write the failing tests** — `TestGoldenMatchesStruct` (round-trip the golden file through `Notification` with no field loss); `TestRenderNewRedacts` (a sample containing `Bearer abc` → body contains `[REDACTED]`, not `abc`); `TestRenderRecurring`; `TestLimiterTenPerMinute` (11 calls at `t0` → 10 true, `TakeOverflow()` 1; at `t0+61s` → true); `TestQueueDropsOldestAndLogs` (max 100, push 101 → `Len` 100, one log line naming the dropped title); `TestDrainStopsAtFirstFailure`.
- [x] **Step 2: Run them to verify they fail.**
- [x] **Step 3: Implement.**
- [x] **Step 4: Run** → PASS.
- [x] **Step 5: Commit**

```bash
git add services/error-pipeline/internal/notify
git commit -m "feat(error-pipeline): notify client, burst cap, bounded retry queue"
```

### Task 11: Detector wiring, probes, main, image

**Files:**
- Create: `services/error-pipeline/internal/detector/detector.go`, `detector_test.go`
- Create: `services/error-pipeline/cmd/error-detector/main.go`
- Create: `services/error-pipeline/Dockerfile.error-detector` (same shape as `Dockerfile.ai-proxy`: copies `go.mod`, `cmd/`, `internal/`; builds `./cmd/error-detector`; distroless nonroot)
- Modify: `.github/workflows/build-images.yaml` (matrix entry `error-detector`, context `services/error-pipeline`; add `services/error-pipeline/**` to the `push.paths` trigger)
- Modify: `.github/workflows/gitops.yaml` (add `cd services/error-pipeline && go test -race ./... && go vet ./...` next to the vault-proxies job)

**Interfaces:**
- Consumes: every package above.
- Produces:
  - `Config{VLogs *vlogs.Client; Notify *notify.Client; InvestigatorURL string; StatePath string; Group group.Config; Now func() time.Time}`
  - `New(cfg Config) (*Detector, error)`; `(*Detector).Run(ctx context.Context) error`
  - `(*Detector).Mux() http.Handler` — `/startupz` (state loaded), `/readyz` (VictoriaLogs `Ready` OK and tail connected within the last 30 s), `/healthz` (process-local)
  - env: `LISTEN_ADDR=:8080`, `VLOGS_URL=http://victorialogs.observability.svc:9428`, `NOTIFY_URL=http://notify-gateway.vault.svc:8080`, `INVESTIGATOR_URL=` (empty disables the handoff), `STATE_PATH=/state/detector.json`

- [x] **Step 1: Write the failing end-to-end test** — a fake VictoriaLogs tail streams: one PN JSON error, the same error with a different UUID 30 s later, a Go panic with 3 stack lines from `proj-health`, and 12 distinct new errors in the same second. A fake notify server records posts. A fake clock is advanced to `+3m`. Assert:
  - `#errors` got the PN error once, the panic once (with its stack), 10 of the burst, and one "+2 more new errors"
  - nothing was posted to `ai-debug`
  - with `InvestigatorURL` empty, no handoff request is made
  - after `cancel()` the state file exists and contains the cursor

  Plus `TestCorruptStateStartsFresh` and `TestInvestigatorUnavailablePostsSkipNotice` (`InvestigatorURL` points at a closed port → after 3 tries, `#errors` gets the skip notice).
- [x] **Step 2: Run it to verify it fails.**
- [x] **Step 3: Implement `Run`:**
  - load state (on a corrupt file, log and start fresh)
  - catch up from `max(cursor, now-15m)` when a cursor exists
  - loop `Tail` with backoff 1 s → 30 s
  - feed each record through `Assembler` → `Tracker.Observe`
  - a 1 s ticker drives `Assembler.Flush`, `Tracker.Tick`, `Queue.Drain`, and the overflow message
  - save state every 30 s and on context cancel; `main` cancels on SIGTERM
  - `NotifyNew` goes through `Limiter`, then the `Queue` to channel `errors`
  - `Handoff` POSTs the `group.Group` JSON to `{InvestigatorURL}/v1/investigate` with 3 tries (1 s, 2 s, 4 s); on failure, `RenderInvestigatorUnavailable` is queued to `errors`
- [x] **Step 4: Run** — `cd services/error-pipeline && go test -race ./... && go vet ./...` → PASS. The `docker build` half did **not** run: this machine's daemon socket is `root:docker 0660` and the user is not in the `docker` group, and `docker build` is not in the CLAUDE.md gate list. Mitigated by cross-compiling the target directly (`CGO_ENABLED=0 GOOS=linux GOARCH=amd64|arm64 go build ./cmd/error-detector` → static binaries), which is the only part a code change can break; `build-images.yaml` builds and pushes both images in CI.
- [x] **Step 5: Commit, then open PR 1a**

```bash
git add services/error-pipeline .github/workflows
git commit -m "feat(error-detector): tail, classify, group, and notify #errors"
```

Run the full gate list from `CLAUDE.md` before opening the PR. After merge, `build-images.yaml` publishes `vault-notify-proxy` and `error-detector`. Record both `@sha256:` digests from the workflow run for PR 1b.

---

## PR 1b — deploy (after the PR 1a images exist)

### Task 12: Deploy vault-notify-proxy

**Files:**
- Create: `deploy/platform/vault-proxies/notify-proxy.yaml` — `Deployment/vault-notify-proxy` (pinned digest, SA `vault-notify-proxy`, three probes on `:8080`, `restartPolicy: Always`, 25m/32Mi `requests == limits`, `readOnlyRootFilesystem`, drop `ALL`), `Service/notify-gateway` (ClusterIP, port 8080 only)
- Modify: `deploy/platform/vault-proxies/kustomization.yaml` (add `notify-proxy.yaml`)
- Modify: `deploy/platform/vault/service-accounts.yaml` (SA `vault-notify-proxy`; ClusterRole `vault-notify-proxy-pod-reader` get/list pods; binding)
- Create: `deploy/platform/vault/policies/notify-proxy.hcl` (`kv/data/notify/*` create/read/update; `kv/metadata/notify/*` read/list)
- Modify: `scripts/vault-configure-k8s-auth.sh` (`create_k8s_role "vault-notify-proxy" "vault-notify-proxy" "vault" "notify-proxy"`)
- Modify: `deploy/platform/vault/network-policy.yaml` — `NetworkPolicy/notify-proxy-policy` (ingress `:8080` only from `observability` + `app.kubernetes.io/name` ∈ `error-detector`, `error-investigator`; egress openbao 8200, API server fallback as `ai-proxy-policy`), `CiliumNetworkPolicy/notify-proxy-egress` (DNS, openbao, `kube-apiserver`, `toFQDNs: [{matchName: discord.com}]` on 443)
- Modify: `scripts/platform-preflight.py` — `check_notify_gateway_contract(platform_docs)`, called next to `check_ai_gateway_contract`
- Create: `tests/preflight/test_notify_gateway_contract.py`
- Create: `scripts/notify-ctl.sh` (`status`, `set-webhook <channel>`; reads the URL silently with `read -rs`; the loopback tunnel pattern of `scripts/ai-gateway-ctl.sh` on local port `18082`)
- Modify: `scripts/universal-agent-hook.py:66-80` (`"notify-ctl.sh"` in `SSH_HELPER_SCRIPTS`), `tests/hooks/test_ssh_command_gate.py` (a case for `./scripts/notify-ctl.sh status`), `AGENTS.md` rule 9 list
- Modify: `docs/platform-architecture.html`, `ARCHITECTURE.md`, `DEPENDENCIES.md` (new image and version), `docs/operations-runbook.md` (a section on seeding webhooks)

**Interfaces:**
- Produces: `Service/notify-gateway.vault.svc:8080`, consumed by Task 13 and workstream 3.

- [x] **Step 1: Write the failing preflight tests** — `test_valid_notify_gateway_passes`; `test_service_exposing_8081_rejected`; `test_missing_probe_rejected`; `test_unrestricted_ingress_rejected` (empty `podSelector`); `test_egress_fqdn_other_than_discord_rejected` (`matchPattern: "*.discord.com"` and an extra `matchName` both rejected); `test_latest_tag_rejected`.
- [x] **Step 2: Run them to verify they fail** — `python3 -m unittest tests.preflight.test_notify_gateway_contract` → FAIL.
- [x] **Step 3: Implement** the manifests, the preflight rule, the script, and the docs. The plan's `scripts/universal-agent-hook.py` `SSH_HELPER_SCRIPTS` entry, `tests/hooks/test_ssh_command_gate.py` case, and `AGENTS.md` rule 9 do not exist in this repository, so `notify-ctl.sh` is not registered with a hook gate.
- [x] **Step 4: Run** — `python3 -m unittest discover tests/preflight && python3 -m unittest discover tests/hooks && ./scripts/render-validate.sh local-k3s && ./scripts/render-validate.sh vps-k3s && python3 scripts/check-arch-doc-sync.py --worktree` → all PASS.
- [x] **Step 5: Commit**

```bash
git add deploy/platform/vault-proxies deploy/platform/vault scripts tests AGENTS.md docs ARCHITECTURE.md DEPENDENCIES.md
git commit -m "feat(notify-proxy): deploy vault-notify-proxy with discord.com-only egress"
```

### Task 13: Deploy error-detector

**Files:**
- Create: `deploy/platform/observability/error-detector.yaml` — `Deployment/error-detector` (1 replica, `strategy: Recreate` because the PVC is RWO; pinned digest; SA `error-detector` with `automountServiceAccountToken: false`; non-root 10001, `readOnlyRootFilesystem`, drop `ALL`, `seccompProfile: RuntimeDefault`; 50m/64Mi `requests == limits`; probes `/startupz` `/readyz` `/healthz` on `:8080`; volume `/state` from PVC `error-detector-state`), `ServiceAccount/error-detector`, `PersistentVolumeClaim/error-detector-state` (1Gi, `local-path`)
- Modify: `deploy/platform/observability/kustomization.yaml` (add `error-detector.yaml`)
- Modify: `deploy/platform/observability/network-policy.yaml` — `NetworkPolicy/error-detector-egress` (DNS; `victorialogs:9428`; namespace `vault` pod `vault-notify-proxy` `:8080`; pod `error-investigator` `:8080`); `victorialogs-ingress` also allows from `error-detector`
- Modify: `clusters/local-k3s/layers.yaml`, `clusters/vps-k3s/layers.yaml` (observability `healthChecks` add `Deployment/error-detector`)
- Modify: `tests/observability/test_observability_manifests.py` — new class `TestErrorDetector`
- Modify: `docs/platform-architecture.html`, `ARCHITECTURE.md`, `DEPENDENCIES.md`, `docs/operations-runbook.md` §15 (how to read detector state and logs)

**Interfaces:**
- Consumes: `notify-gateway.vault.svc:8080` (Task 12), `victorialogs.observability.svc:9428`.

- [ ] **Step 1: Write the failing manifest tests** — `test_detector_has_no_hostpath_and_no_sa_token`; `test_detector_guaranteed_qos` (50m/64Mi, `requests == limits`); `test_detector_image_pinned_by_digest`; `test_detector_egress_only_vlogs_notify_investigator_dns`; `test_victorialogs_ingress_allows_detector`; `test_layer_health_checks_include_detector` (both environments); `test_investigator_url_empty_until_ws3`.
- [ ] **Step 2: Run them to verify they fail** — `python3 -m unittest tests.observability.test_observability_manifests` → FAIL.
- [ ] **Step 3: Implement** the manifests and docs.
- [ ] **Step 4: Run the full gate list from `CLAUDE.md`** (Go tests in both modules, preflight, hooks, render-validate for both environments, arch-doc sync) → all PASS.
- [ ] **Step 5: Commit and open PR 1b**

```bash
git add deploy/platform/observability clusters tests/observability docs ARCHITECTURE.md DEPENDENCIES.md
git commit -m "feat(error-detector): deploy detector in observability with PVC state"
```

### Task 14: Live verification on the VPS (operator runs; the agent never SSHes)

- [ ] **Step 1: Configure OpenBao** — `./scripts/vault-configure-k8s-auth.sh` (SSH helper; needs the operator's allowance) → prints `Configuring role: vault-notify-proxy`.
- [ ] **Step 2: Seed the webhook** — `./scripts/notify-ctl.sh set-webhook errors` (paste the Discord channel webhook URL) → `status` shows `errors configured: true`.
- [ ] **Step 3: Confirm health** — `kubectl -n observability get deploy error-detector` and `kubectl -n vault get deploy vault-notify-proxy` → both `1/1` Ready.
- [ ] **Step 4: Trigger a known error** — `kubectl -n proj-pn exec deploy/pn-backend -- sh -c 'echo "{\"time\":\"$(date -u +%Y-%m-%dT%H:%M:%S.000Z)\",\"level\":\"error\",\"msg\":\"error-pipeline smoke test\",\"exception.type\":\"SmokeTest\",\"exception.message\":\"synthetic\"}" > /proc/1/fd/2'` (if the image has no shell, use the next real error after the workstream 0 rollout) → `#errors` shows "New error in proj-pn/pn-backend" within about 10 s.
- [ ] **Step 5: Confirm grouping** — repeat Step 4 twice within 3 minutes → no new Discord message; the detector's stderr shows the window closing at about 3 minutes with the handoff disabled.
