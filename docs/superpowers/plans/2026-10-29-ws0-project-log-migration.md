# Workstream 0: Project Log Migration to Contract v2 — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make every hosted project print log lines that follow `contracts/CONTRACT_LOG_SCHEMA.md` v2, so the error detector can find errors from structure instead of guesses.

**Architecture:** Each Go backend gets one JSON `slog` logger as the process default and an `exception.*` helper for errors. PN's `analytics` and `admin` records gain `time`/`level`/`msg` and rename `kind` → `event.name`. Each nginx frontend prints a JSON access log with `level` mapped from the HTTP status and keeps its native error log (contract §4). Each project lands in its own repository first, then this repository pins the new images and marks the contract §6 row `done`.

**Tech Stack:** Go `log/slog` (`project-pn`: go 1.26; `nacfson_health` api: go 1.22), nginx 1.27 `log_format ... escape=json`, `map`.

**Spec:** `docs/superpowers/specs/2026-10-29-error-pipeline-design.md` (§6, §7) and `contracts/CONTRACT_LOG_SCHEMA.md` v2.

## Global Constraints

- Line format: one JSON object per line; required `time` (RFC 3339, timezone, ≥ millisecond precision), `level` (`debug`/`info`/`warn`/`error`/`fatal`, case-insensitive), `msg`.
- Error lines (`level` `error`/`fatal`) carry `exception.message` and/or `exception.type`; `exception.stacktrace` is recommended.
- Keys are flat dotted names (`exception.message`, `event.name`), not nested objects.
- `event.name` replaces `kind`. Values stay `analytics` and `admin`.
- No tokens, passwords, or secrets in any line, including `exception.message`.
- stdout/stderr only; never open a file or point a file logger at `/dev/stdout`.
- Project repositories: a PR in the project repo first; schema docs (`Project_PN/docs/analytics-contract.md`, the admin logging design) change in the same PN PR.
- This repo: never push to `main`; image pins go through the `update-project-images` skill.

## Review Focus

1. **A library that still writes slog text.** A package that builds its own `slog.New(slog.NewTextHandler(...))` bypasses the default → the PN test `TestNoTextHandlers` greps the module and fails on any `NewTextHandler`.
2. **An `error` attr that isn't renamed.** Existing calls pass `"error", err`; those lines have `level=ERROR` but no `exception.message` → `logx` `ReplaceAttr` maps a top-level `error` attr to `exception.message` and adds `exception.type`; tested in `TestErrorAttrBecomesException`.
3. **An analytics consumer that still reads `kind`.** A downstream reader of PN's stdout breaks silently → PN task greps for `"kind"` readers and the analytics schema doc states the rename; the detector's legacy `kind` read covers the gap.
4. **An nginx `$time_iso8601` without milliseconds.** The contract needs ms precision → the nginx format splices the `$msec` fraction into `$time_iso8601` via `map`; asserted by the `\.\d{3}` check in the nginx log test.
5. **A 5xx served by nginx but caused by the upstream.** It must be `level` `error` so the detector sees it → the `map $status $level` test covers 500, 502, 504 and a 404 (`info`).

---

### Task 1: PN backend — JSON logger package and default

**Repo:** `~/Projects/Project_PN` (branch `feat/log-contract-v2`)

**Files:**
- Create: `backend/internal/logx/logx.go`
- Create: `backend/internal/logx/logx_test.go`
- Modify: `backend/cmd/api/main.go:79`, `backend/cmd/retranslate-senses/main.go:60`, `backend/internal/words/service.go:145`, `backend/internal/enrich/openai.go:70,78` (every `slog.NewTextHandler` call)
- Modify: `backend/cmd/api/main.go`, `backend/cmd/retranslate-senses/main.go` (call `slog.SetDefault(logx.New(os.Stderr))` first thing in `main`)

**Interfaces:**
- Produces: `logx.New(w io.Writer) *slog.Logger` — JSON handler, level `debug` and up, keys `time`/`level`/`msg`, time formatted `2006-01-02T15:04:05.000Z07:00` in UTC, a top-level `error` attr rewritten to `exception.message` plus `exception.type` (`fmt.Sprintf("%T", err)`).
- Produces: `logx.Exception(err error) []any` — returns `"exception.message", err.Error(), "exception.type", fmt.Sprintf("%T", err)` for call sites that want it explicitly.

- [ ] **Step 1: Write the failing tests**

```go
func TestLineHasRequiredKeys(t *testing.T) {
	var buf bytes.Buffer
	logx.New(&buf).Info("hello")
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil { t.Fatal(err) }
	for _, k := range []string{"time", "level", "msg"} {
		if _, ok := m[k]; !ok { t.Fatalf("missing %s in %s", k, buf.String()) }
	}
	if !regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z$`).MatchString(m["time"].(string)) {
		t.Fatalf("time %q is not RFC 3339 UTC with milliseconds", m["time"])
	}
}

func TestErrorAttrBecomesException(t *testing.T) {
	var buf bytes.Buffer
	logx.New(&buf).Error("enrich failed", "error", context.DeadlineExceeded)
	var m map[string]any
	_ = json.Unmarshal(buf.Bytes(), &m)
	if m["level"] != "ERROR" || m["exception.message"] != "context deadline exceeded" || m["exception.type"] == nil {
		t.Fatalf("got %v", m)
	}
	if _, ok := m["error"]; ok { t.Fatal("error attr must be renamed") }
}

func TestNoTextHandlers(t *testing.T) {
	out, _ := exec.Command("grep", "-rn", "--include=*.go", "NewTextHandler", "../..").CombinedOutput()
	if len(bytes.TrimSpace(out)) > 0 { t.Fatalf("slog text handlers remain:\n%s", out) }
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd backend && go test ./internal/logx/...`
Expected: FAIL (package `logx` does not exist).

- [ ] **Step 3: Implement `logx.New` and `logx.Exception`**

Use `slog.NewJSONHandler` with `HandlerOptions{Level: slog.LevelDebug, ReplaceAttr: ...}`. `ReplaceAttr` formats `slog.TimeKey` as UTC `.000Z` and, when `len(groups)==0 && a.Key=="error"`, returns `exception.message`. Add `exception.type` by wrapping the handler: a small `Handle` override that inspects the record's `error` attr before `ReplaceAttr` runs. Then replace each `slog.NewTextHandler(os.Stdout, nil)` with `logx.New(os.Stderr)` (diagnostics go to stderr per the contract's stream-split rule) and set the default in both `main` functions.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd backend && go test ./... && go vet ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/logx backend/cmd backend/internal/words backend/internal/enrich
git commit -m "feat(logs): print contract v2 JSON lines through one slog default"
```

### Task 2: PN backend — analytics and admin records follow contract v2

**Repo:** `~/Projects/Project_PN`

**Files:**
- Modify: `backend/internal/analytics/events.go:51-55` (`Record`)
- Modify: `backend/internal/admin/logger.go:28-33` (`auditLine`) and `Log`
- Modify: tests next to both files
- Modify: `docs/analytics-contract.md`, `docs/superpowers/specs/2026-07-07-admin-logging-design.md`

**Interfaces:**
- Consumes: nothing from Task 1 (records are marshalled directly).
- Produces: analytics line `{"time","level":"info","msg":"analytics event","event.name":"analytics", ...Event fields}`; admin line `{"time","level":"info","msg":<message>,"event.name":"admin","category"}`.

- [ ] **Step 1: Write the failing tests**

```go
// analytics
func TestRecordFollowsContractV2(t *testing.T) {
	b, _ := json.Marshal(analytics.NewRecord(validEvent(), fixedNow))
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if m["event.name"] != "analytics" || m["level"] != "info" || m["msg"] != "analytics event" || m["time"] != "2026-10-29T09:00:00.000Z" {
		t.Fatalf("got %s", b)
	}
	if _, ok := m["kind"]; ok { t.Fatal("kind must be gone") }
}

// admin
func TestAuditLineFollowsContractV2(t *testing.T) {
	var buf bytes.Buffer
	l := admin.NewWriterLogger(&buf)
	l.Log("role", "user role changed")
	var m map[string]any
	_ = json.Unmarshal(buf.Bytes(), &m)
	if m["event.name"] != "admin" || m["level"] != "info" || m["msg"] != "user role changed" || m["category"] != "role" {
		t.Fatalf("got %s", buf.String())
	}
	if !strings.Contains(m["time"].(string), ".") { t.Fatal("time needs millisecond precision") }
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd backend && go test ./internal/analytics/... ./internal/admin/...`
Expected: FAIL (`NewRecord` undefined; `kind` still present).

- [ ] **Step 3: Implement**

`analytics.Record` becomes `{Time string "time"; Level string "level"; Msg string "msg"; EventName string "event.name"; Event}` with `NewRecord(e Event, now time.Time) Record` (`Level` `"info"`, `Msg` `"analytics event"`, `EventName` `"analytics"`). Every place that built `Record{Kind: "analytics", ...}` calls `NewRecord`. `auditLine` becomes `{Time "time"; Level "level"; Msg "msg"; EventName "event.name"; Category "category"}`, and `time` uses `"2006-01-02T15:04:05.000Z07:00"` in UTC. Update both schema docs: the rename, the three new keys, and a pointer to the platform contract v2.

- [ ] **Step 4: Run all tests**

Run: `cd backend && go test ./... && go vet ./...`
Expected: PASS. Also run `grep -rn '"kind"' backend docs` and confirm only historical notes remain.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/analytics backend/internal/admin docs
git commit -m "feat(logs): analytics and admin lines follow platform log contract v2"
```

### Task 3: PN frontend — nginx JSON access log

**Repo:** `~/Projects/Project_PN`

**Files:**
- Modify: `frontend/nginx/docker-entrypoint.sh:82-85` (the `log_format main` block and `access_log`)
- Create: `frontend/nginx/test-log-format.sh`

**Interfaces:**
- Produces: access line `{"time":"<iso8601>.<ms>","level":"info|warn|error","msg":"<request>","http.response.status_code":<status>,"http.request.method":"<method>","url.path":"<uri>","user_agent.original":"<ua>","client.address":"<addr>"}` (OpenTelemetry HTTP attribute names).

- [ ] **Step 1: Write the failing test script**

`test-log-format.sh` builds the image, runs it with `docker run -d -p 18080:8080`, requests `/`, `/does-not-exist`, and reads `docker logs`. It asserts with `jq` that every access line parses as JSON, has `time` matching `\.\d{3}`, `level` `info` for 200, `warn` for 404, and that the `map` table in the rendered `nginx.conf` sends `500`, `502`, `504` to `error` (`docker exec ... nginx -T | grep`).

- [ ] **Step 2: Run it to verify it fails**

Run: `bash frontend/nginx/test-log-format.sh`
Expected: FAIL (access line is not JSON).

- [ ] **Step 3: Implement**

In the heredoc `http {}` block add:

```nginx
map $status $log_level { ~^5 error; ~^4 warn; default info; }
# $time_iso8601 has no milliseconds: splice the fraction of $msec between the
# seconds and the timezone, e.g. 2026-10-29T09:00:00 + .123 + +00:00
map $time_iso8601 $log_t_main { ~^(?<p>\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d) $p; }
map $time_iso8601 $log_t_tz   { ~(?<z>[+-]\d\d:\d\d|Z)$ $z; }
map $msec         $log_t_ms   { ~\.(?<ms>\d{3}) $ms; }
log_format contract escape=json '{"time":"$log_t_main.$log_t_ms$log_t_tz","level":"$log_level","msg":"$request",'
  '"http.response.status_code":$status,"http.request.method":"$request_method",'
  '"url.path":"$uri","user_agent.original":"$http_user_agent","client.address":"$remote_addr"}';
access_log /dev/stdout contract;
```

Escape `$` for the heredoc exactly as the existing block does. Keep `error_log /dev/stderr notice;` (native format, contract §4).

- [ ] **Step 4: Run the test to verify it passes**

Run: `bash frontend/nginx/test-log-format.sh`
Expected: PASS.

- [ ] **Step 5: Commit, push, open the PN PR**

```bash
git add frontend/nginx
git commit -m "feat(frontend): nginx access log follows platform log contract v2"
git push -u origin feat/log-contract-v2
gh pr create --title "feat(logs): adopt platform log contract v2" --body "..."
```

### Task 4: Health API — JSON logger default

**Repo:** `~/Projects/nacfson_health` (branch `feat/log-contract-v2`)

**Files:**
- Create: `apps/health-web/api/internal/logx/logx.go`, `logx_test.go` (same interface and tests as Task 1, module path `github.com/nacfson/nacfson_health/apps/health-web/api/internal/logx`; `TestNoTextHandlers` greps `../..`)
- Modify: `apps/health-web/api/cmd/api/main.go`, `apps/health-web/api/cmd/migrate/main.go` (first line of `main`: `slog.SetDefault(logx.New(os.Stderr))`)

**Interfaces:**
- Produces: the same `logx.New` and `logx.Exception` as Task 1. Two copies are intentional: the projects are separate repositories and must not share a module.

- [ ] **Step 1: Write the failing tests** (copy Task 1's three tests with the module import path changed)
- [ ] **Step 2: Run them to verify they fail** — `cd apps/health-web/api && go test ./internal/logx/...` → FAIL.
- [ ] **Step 3: Implement** — as Task 1 Step 3; Go 1.22 has `log/slog`.
- [ ] **Step 4: Run all tests** — `go test ./... && go vet ./...` → PASS.
- [ ] **Step 5: Commit**

```bash
git add apps/health-web/api
git commit -m "feat(logs): health api prints platform log contract v2 JSON lines"
```

### Task 5: Health frontend — nginx JSON access log

**Repo:** `~/Projects/nacfson_health`

**Files:**
- Modify: `apps/health-web/frontend/nginx.conf` (inside `http {}`)
- Create: `apps/health-web/frontend/test-log-format.sh` (same assertions as Task 3)

- [ ] **Step 1: Write the failing test script** (Task 3 Step 1, against `apps/health-web/frontend/Dockerfile`)
- [ ] **Step 2: Run it to verify it fails**
- [ ] **Step 3: Implement** — the same `map`/`log_format`/`access_log` as Task 3, unescaped (plain config file). Keep the native `error_log` on stderr.
- [ ] **Step 4: Run it to verify it passes**
- [ ] **Step 5: Commit, push, open the health PR**

```bash
git add apps/health-web/frontend
git commit -m "feat(frontend): nginx access log follows platform log contract v2"
git push -u origin feat/log-contract-v2
gh pr create --title "feat(logs): adopt platform log contract v2" --body "..."
```

### Task 6: This repo — pin migrated images and mark the contract rows done

**Repo:** this repository, after the Task 1–5 PRs are merged and their images are built.

**Files:**
- Modify: `deploy/projects/pn/workloads/*-deployment.yaml`, `deploy/projects/health/workloads/*-deployment.yaml` (through the `update-project-images` skill)
- Modify: `contracts/CONTRACT_LOG_SCHEMA.md` §6 status table (each row `pending` → `done`)
- Modify: `docs/platform-architecture.html` (one-line note that all projects follow v2)
- Test: `tests/observability/test_log_contract_status.py`

- [ ] **Step 1: Write the failing test**

```python
def test_every_project_namespace_has_a_status_row(self):
    contract = open(CONTRACT).read()
    section = contract.split("## 6. Migration from version 1", 1)[1].split("## Rejected", 1)[0]
    rows = dict(re.findall(r"^\| `(proj-[a-z0-9-]+)` [a-z]+ \| .* \| (pending|done) \|$", section, re.M))
    namespaces = {d for d in os.listdir(os.path.join(BASE, "deploy", "projects"))}
    self.assertEqual({f"proj-{n}" for n in namespaces}, set(rows))
```

This test fails if a new project is added without a status row. It passes now; it guards the future.

- [ ] **Step 2: Run it** — `python3 -m unittest tests.observability.test_log_contract_status` → PASS (guard in place).
- [ ] **Step 3: Pin images** — invoke the `update-project-images` skill with the merged PN and health image links; it opens one PR per project.
- [ ] **Step 4: Mark rows `done` and sync the handbook** in the same PR as the last pin; run `python3 scripts/check-arch-doc-sync.py --worktree` → PASS, `./scripts/render-validate.sh vps-k3s` → PASS.
- [ ] **Step 5: Verify on the VPS** (operator runs; the agent does not SSH): open VictoriaLogs with `scripts/open-observe.sh` and run `{project=~"proj-.*"} | unpack_json from message | level:*` over the last 15 minutes; every line from a migrated container has `time`, `level`, `msg`.
- [ ] **Step 6: Commit**

```bash
git add contracts/CONTRACT_LOG_SCHEMA.md docs/platform-architecture.html tests/observability/test_log_contract_status.py
git commit -m "docs(contracts): all hosted projects follow log contract v2"
```

### Task 7: Remove the legacy `kind` read

**Precondition:** every §6 row is `done` and workstream 1 is merged.

**Files:**
- Modify: `contracts/CONTRACT_LOG_SCHEMA.md` §6 (drop the `kind` → `event.name` transition row)
- Modify: `services/error-pipeline/internal/classify/classify.go` (drop the legacy `kind` branch) and its test `TestLegacyKindReadAsEventName` → becomes `TestLegacyKindIgnored`

- [ ] **Step 1: Change the test to expect `kind` to be ignored**; run `cd services/error-pipeline && go test ./internal/classify/...` → FAIL.
- [ ] **Step 2: Remove the branch**; run again → PASS.
- [ ] **Step 3: Update the contract and handbook**; run `python3 scripts/check-arch-doc-sync.py --worktree` → PASS.
- [ ] **Step 4: Commit**

```bash
git add contracts services/error-pipeline/internal/classify docs/platform-architecture.html
git commit -m "chore(logs): drop legacy kind read after every project migrated"
```
