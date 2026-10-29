package detector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"nacfson_pipeline/services/error-pipeline/internal/classify"
	"nacfson_pipeline/services/error-pipeline/internal/fingerprint"
	"nacfson_pipeline/services/error-pipeline/internal/group"
	"nacfson_pipeline/services/error-pipeline/internal/notify"
	"nacfson_pipeline/services/error-pipeline/internal/state"
	"nacfson_pipeline/services/error-pipeline/internal/vlogs"
)

// base is the wall clock every test starts from.
var base = time.Date(2026, 10, 29, 9, 0, 0, 0, time.UTC)

// TestMain silences the detector's own logging, because the corrupt-state path
// logs on purpose and a passing run must not print it, and shortens the control
// loop so the end-to-end tests do not wait on the production one-second ticker.
func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	tickInterval = 10 * time.Millisecond
	os.Exit(m.Run())
}

// fakeClock is a manual wall clock. Set is what moves the pipeline through the
// grouping windows; Now counts its calls so a test can see the control loop
// read the clock.
type fakeClock struct {
	mu  sync.Mutex
	t   time.Time
	hit int
}

func newFakeClock(t time.Time) *fakeClock { return &fakeClock{t: t} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.hit++
	return c.t
}

func (c *fakeClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

func (c *fakeClock) hits() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hit
}

// fakeLogs is a VictoriaLogs stand-in: /health answers readiness, the tail
// endpoint streams whatever the test sends, and the catch-up endpoint is
// counted and its start recorded so a test can prove when a backfill runs.
type fakeLogs struct {
	t        *testing.T
	srv      *httptest.Server
	lines    chan string
	unready  atomic.Bool
	catchUps atomic.Int32
	lastFrom atomic.Value // string: the catch-up window start
}

func newFakeLogs(t *testing.T) *fakeLogs {
	f := &fakeLogs{t: t, lines: make(chan string, 128)}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeLogs) handle(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/health":
		if f.unready.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	case "/select/logsql/tail":
		f.tail(w, r)
	case "/select/logsql/query":
		f.catchUps.Add(1)
		if err := r.ParseForm(); err == nil {
			f.lastFrom.Store(r.PostForm.Get("start"))
		}
		w.WriteHeader(http.StatusOK)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeLogs) tail(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		f.t.Error("response writer is not a flusher")
		return
	}
	w.WriteHeader(http.StatusOK)
	fl.Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case line := <-f.lines:
			if _, err := fmt.Fprintln(w, line); err != nil {
				return
			}
			fl.Flush()
		}
	}
}

// send queues one line for the connected tail. The channel is buffered, so a
// send never blocks on a detector that has not dialled yet.
func (f *fakeLogs) send(line string) {
	f.t.Helper()
	select {
	case f.lines <- line:
	case <-time.After(5 * time.Second):
		f.t.Fatal("the tail did not accept a line within 5s")
	}
}

// recorded is one notification the fake proxy received.
type recorded struct {
	channel string
	n       notify.Notification
}

// recorder is the fake vault-notify-proxy: it records the channel and payload
// of every post and answers 202, as the real proxy does.
type recorder struct {
	mu  sync.Mutex
	got []recorded
}

func (q *recorder) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var n notify.Notification
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&n); err != nil {
		http.Error(w, "bad notification", http.StatusBadRequest)
		return
	}
	q.mu.Lock()
	q.got = append(q.got, recorded{
		channel: strings.TrimPrefix(r.URL.Path, "/v1/notify/"),
		n:       n,
	})
	q.mu.Unlock()
	w.WriteHeader(http.StatusAccepted)
}

func (r *recorder) snapshot() []recorded {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recorded{}, r.got...)
}

// waitFor waits until n notifications have arrived and returns what it saw,
// which is everything that arrived when the deadline passed.
func (r *recorder) waitFor(n int, timeout time.Duration) []recorded {
	deadline := time.Now().Add(timeout)
	for {
		got := r.snapshot()
		if len(got) >= n || time.Now().After(deadline) {
			return got
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// investigator is the fake error-investigator: it counts the handoff requests,
// so a test can prove the handoff was disabled or that it was tried.
type investigator struct {
	srv   *httptest.Server
	posts atomic.Int32
	fail  atomic.Bool
}

func newInvestigator(t *testing.T) *investigator {
	inv := &investigator{}
	inv.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inv.posts.Add(1)
		_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, 1<<20))
		if inv.fail.Load() {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(inv.srv.Close)
	return inv
}

func (inv *investigator) count() int { return int(inv.posts.Load()) }

// recordLine encodes one VictoriaLogs record as the tail endpoint sends it.
func recordLine(t *testing.T, at time.Time, project, pod, container, message string) string {
	t.Helper()
	raw, err := json.Marshal(map[string]string{
		"_time":      at.UTC().Format(time.RFC3339Nano),
		"_stream_id": "stream-1",
		"project":    project,
		"pod":        pod,
		"container":  container,
		"node":       "node-1",
		"message":    message,
	})
	if err != nil {
		t.Fatalf("encode record: %v", err)
	}
	return string(raw)
}

const (
	uuidA = "550e8400-e29b-41d4-a716-446655440000"
	uuidB = "6ba7b810-9dad-11d1-80b4-00c04fd430c8"

	panicLine = "panic: runtime error: invalid memory address or nil pointer dereference"
)

// pnError is the PN order error with the given UUID. The two occurrences of the
// end-to-end test differ only in that UUID, so they must share a fingerprint.
func pnError(uuid string) string {
	return `{"level":"error","msg":"order lookup failed",` +
		`"exception.type":"OrderError",` +
		`"exception.message":"order ` + uuid + ` not found"}`
}

var stackLines = []string{
	"goroutine 1 [running]:",
	"main.main()",
	"\t/app/cmd/api/main.go:42 +0x1d",
}

func burstError(i int) string {
	return fmt.Sprintf(`{"level":"error","msg":"burst failure %02d","error.fingerprint":"burst-%02d"}`, i, i)
}

func fieldValue(n notify.Notification, name string) string {
	for _, f := range n.Fields {
		if f.Name == name {
			return f.Value
		}
	}
	return ""
}

func titles(got []recorded) []string {
	out := make([]string, 0, len(got))
	for _, r := range got {
		out = append(out, r.channel+": "+r.n.Title)
	}
	return out
}

// startDetector runs cfg in the background and returns a stop function that
// cancels it, waits for Run to return and reports its error. It is idempotent,
// so a failing test can still rely on the cleanup.
func startDetector(t *testing.T, cfg Config) (d *Detector, stop func() error) {
	t.Helper()
	d, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	var (
		once sync.Once
		err2 error
	)
	stop = func() error {
		once.Do(func() {
			cancel()
			select {
			case err2 = <-done:
			case <-time.After(60 * time.Second):
				err2 = errors.New("Run did not return within 60s of cancel")
			}
		})
		return err2
	}
	t.Cleanup(func() { _ = stop() })
	return d, stop
}

func readState(t *testing.T, path string) state.Snapshot {
	t.Helper()
	snap, err := state.Load(path)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	return snap
}

// TestEndToEnd drives the whole pipeline against a fake VictoriaLogs tail, a
// fake notify proxy and a manual clock: two UUID variants of one error, a Go
// panic with a stack, and a burst that overruns the notification cap.
func TestEndToEnd(t *testing.T) {
	logs := newFakeLogs(t)
	rec := &recorder{}
	notifySrv := httptest.NewServer(http.HandlerFunc(rec.handle))
	t.Cleanup(notifySrv.Close)

	statePath := filepath.Join(t.TempDir(), "detector.json")
	stale := statePath + ".tmp-1234567890" // left behind by a killed Save
	if err := os.WriteFile(stale, []byte(`{"version":1`), 0o600); err != nil {
		t.Fatalf("write stale temp file: %v", err)
	}

	clk := newFakeClock(base)
	_, stop := startDetector(t, Config{
		VLogs:           &vlogs.Client{BaseURL: logs.srv.URL},
		Notify:          &notify.Client{BaseURL: notifySrv.URL},
		InvestigatorURL: "", // workstream 3 has not landed: the handoff is off
		StatePath:       statePath,
		Group:           group.DefaultConfig(),
		Now:             clk.Now,
	})

	// The fingerprint both PN occurrences must share.
	pnEvent, kind := classify.Line(pnError(uuidA))
	if kind != classify.Error {
		t.Fatalf("PN line classifies as %v, want Error", kind)
	}
	wantPN := fingerprint.Of(classify.Classified{
		Record: classify.Record{Project: "proj-pn", Container: "pn-backend"},
		Event:  pnEvent,
	})

	// Phase 1: the first PN error. Its alert synchronises the test: it proves
	// the tail is connected and the line was classified, fingerprinted and
	// grouped.
	logs.send(recordLine(t, base, "proj-pn", "pn-backend-1", "pn-backend", pnError(uuidA)))
	got := rec.waitFor(1, 15*time.Second)
	if len(got) != 1 {
		t.Fatalf("after the first error: %d notifications, want 1: %v", len(got), titles(got))
	}
	if fp := fieldValue(got[0].n, "Fingerprint"); fp != wantPN {
		t.Errorf("first alert fingerprint = %q, want %q", fp, wantPN)
	}

	// Phase 2: the same error with another UUID, 30 s later. It joins the open
	// window, so it must not produce a second alert.
	clk.Set(base.Add(60 * time.Second))
	logs.send(recordLine(t, base.Add(30*time.Second), "proj-pn", "pn-backend-1", "pn-backend", pnError(uuidB)))

	// Phase 3: a Go panic from proj-health with three stack lines. A JSON line
	// ends a stack, so the trailing info line closes the crash deterministically
	// with all three lines. Every record carries the current wall clock, which
	// is ahead of the flush watermark, so no partial stack can be flushed out
	// from under the test.
	clk.Set(base.Add(120 * time.Second))
	at := base.Add(120 * time.Second)
	logs.send(recordLine(t, at, "proj-health", "health-api-1", "health-api", panicLine))
	for _, line := range stackLines {
		logs.send(recordLine(t, at, "proj-health", "health-api-1", "health-api", line))
	}
	logs.send(recordLine(t, at, "proj-health", "health-api-1", "health-api", `{"level":"info","msg":"listening on :8080"}`))

	got = rec.waitFor(2, 15*time.Second)
	if len(got) != 2 {
		t.Fatalf("after the panic: %d notifications, want 2: %v", len(got), titles(got))
	}
	panicPost := got[1]
	if body := panicPost.n.Body; !strings.Contains(body, "main.main()") || !strings.Contains(body, "/app/cmd/api/main.go:42") {
		t.Errorf("panic alert lost its stack trace: %q", body)
	}
	if sev := panicPost.n.Severity; sev != notify.SeverityFatal {
		t.Errorf("panic severity = %q, want %q", sev, notify.SeverityFatal)
	}

	// Phase 4: twelve distinct new errors in one second, after the previous
	// burst window ended, so ten alerts fit under the cap.
	clk.Set(base.Add(240 * time.Second))
	for i := 1; i <= 12; i++ {
		logs.send(recordLine(t, base.Add(240*time.Second), "proj-pn", "pn-backend-1", "pn-backend", burstError(i)))
	}
	got = rec.waitFor(12, 15*time.Second)
	if len(got) != 12 {
		t.Fatalf("after the burst: %d notifications, want 12: %v", len(got), titles(got))
	}

	// Phase 5: one minute on, the burst window has ended, so the two refused
	// alerts are reported once as a single summary.
	clk.Set(base.Add(300 * time.Second))
	got = rec.waitFor(13, 15*time.Second)
	if len(got) != 13 {
		t.Fatalf("after the burst window: %d notifications, want 13: %v", len(got), titles(got))
	}

	// Nothing is ever posted anywhere but #errors.
	for _, r := range got {
		if r.channel != channelErrors {
			t.Errorf("posted to channel %q, want %q (title %q)", r.channel, channelErrors, r.n.Title)
		}
	}

	// The PN error is one alert for two occurrences.
	byPrint := map[string]int{}
	for _, r := range got {
		byPrint[fieldValue(r.n, "Fingerprint")]++
	}
	if n := byPrint[wantPN]; n != 1 {
		t.Errorf("PN error alerted %d times, want 1 (both UUID variants are one error)", n)
	}

	// The burst: exactly ten of the twelve were alerted, each at most once.
	alerted := 0
	for i := 1; i <= 12; i++ {
		switch byPrint[fmt.Sprintf("burst-%02d", i)] {
		case 0:
		case 1:
			alerted++
		default:
			t.Errorf("burst-%02d alerted %d times, want at most 1", i, byPrint[fmt.Sprintf("burst-%02d", i)])
		}
	}
	if alerted != 10 {
		t.Errorf("alerted %d of the 12 burst errors, want 10", alerted)
	}

	// And one summary for the two the cap refused.
	var overflow []recorded
	for _, r := range got {
		if strings.HasPrefix(r.n.Title, "+") {
			overflow = append(overflow, r)
		}
	}
	if len(overflow) != 1 {
		t.Fatalf("got %d overflow messages, want 1: %v", len(overflow), titles(got))
	}
	if want := "+2 more new errors"; overflow[0].n.Title != want {
		t.Errorf("overflow title = %q, want %q", overflow[0].n.Title, want)
	}
	if since := fieldValue(overflow[0].n, "Since"); since != base.Add(240*time.Second).Format(time.RFC3339) {
		t.Errorf("overflow since = %q, want %q", since, base.Add(240*time.Second).Format(time.RFC3339))
	}

	// The handoff is disabled by an empty InvestigatorURL: no closed window may
	// produce a handoff, and the only way that shows up is a skip notice after
	// the retry ladder (about 3 s: instant dial/URL failure plus the 1 s and 2 s
	// backoffs). Waiting past that ladder makes "nothing more arrives" a real
	// assertion instead of a race with it.
	if got := rec.waitFor(14, 5*time.Second); len(got) != 13 {
		t.Errorf("%d notifications in total, want 13: an attempted handoff adds a skip notice per closed window: %v",
			len(got), titles(got))
	}
	// A first start tails from now rather than backfilling.
	if n := logs.catchUps.Load(); n != 0 {
		t.Errorf("catch-up ran %d times on a first start, want 0", n)
	}

	// Cancelling saves the state: cursor and groups survive, so a restart
	// neither replays everything nor re-announces a known error.
	if err := stop(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	snap := readState(t, statePath)
	if want := base.Add(240 * time.Second); !snap.Cursor.Equal(want) {
		t.Errorf("cursor = %v, want %v", snap.Cursor, want)
	}
	var pn *group.Group
	for i := range snap.Groups {
		if snap.Groups[i].Fingerprint == wantPN {
			pn = &snap.Groups[i]
		}
	}
	if pn == nil {
		t.Fatalf("state lost the PN group: %+v", snap.Groups)
	}
	if pn.Count != 2 {
		t.Errorf("PN group count = %d, want 2 (both UUID variants)", pn.Count)
	}
	if !pn.Handed {
		t.Error("PN group handed = false, want true: its window closed and it was handed off")
	}
	if _, err := os.Stat(stale); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("stale temp file %s survived startup: %v", stale, err)
	}
	left, err := filepath.Glob(statePath + ".tmp-*")
	if err != nil {
		t.Fatalf("glob temp files: %v", err)
	}
	if len(left) != 0 {
		t.Errorf("temp state files left behind: %v", left)
	}
}

// TestCorruptStateStartsFresh proves a truncated state file is logged and
// ignored rather than fatal: the first alert still goes out and the file is
// replaced with a valid one.
func TestCorruptStateStartsFresh(t *testing.T) {
	logs := newFakeLogs(t)
	rec := &recorder{}
	notifySrv := httptest.NewServer(http.HandlerFunc(rec.handle))
	t.Cleanup(notifySrv.Close)

	statePath := filepath.Join(t.TempDir(), "detector.json")
	if err := os.WriteFile(statePath, []byte(`{"version":1,"cursor":`), 0o600); err != nil {
		t.Fatalf("write corrupt state: %v", err)
	}

	clk := newFakeClock(base)
	d, stop := startDetector(t, Config{
		VLogs:     &vlogs.Client{BaseURL: logs.srv.URL},
		Notify:    &notify.Client{BaseURL: notifySrv.URL},
		StatePath: statePath,
		Group:     group.DefaultConfig(),
		Now:       clk.Now,
	})

	logs.send(recordLine(t, base, "proj-blog", "blog-web-1", "blog-server", `{"level":"fatal","msg":"cannot bind :8080"}`))
	got := rec.waitFor(1, 15*time.Second)
	if len(got) != 1 {
		t.Fatalf("a corrupt state file suppressed the first alert: %d notifications: %v", len(got), titles(got))
	}
	if got[0].channel != channelErrors {
		t.Errorf("channel = %q, want %q", got[0].channel, channelErrors)
	}

	// The corrupt file must not block startup either.
	if code, body := get(t, d.Mux(), "/startupz"); code != http.StatusOK {
		t.Errorf("/startupz after a corrupt state = %d, want 200: %s", code, body)
	}
	if n := logs.catchUps.Load(); n != 0 {
		t.Errorf("catch-up ran %d times without a cursor, want 0", n)
	}

	if err := stop(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	snap := readState(t, statePath)
	if !snap.Cursor.Equal(base) {
		t.Errorf("cursor = %v, want %v: the fresh start overwrote the corrupt file", snap.Cursor, base)
	}
	if len(snap.Groups) != 1 {
		t.Errorf("groups = %d, want 1: the fresh state holds the alert it just sent", len(snap.Groups))
	}
}

// TestCatchUpFromCursor proves a restart backfills from the stored cursor before
// it tails, instead of dropping everything logged while the detector was down.
func TestCatchUpFromCursor(t *testing.T) {
	logs := newFakeLogs(t)
	rec := &recorder{}
	notifySrv := httptest.NewServer(http.HandlerFunc(rec.handle))
	t.Cleanup(notifySrv.Close)

	statePath := filepath.Join(t.TempDir(), "detector.json")
	cursor := base.Add(-5 * time.Minute)
	if err := state.Save(statePath, state.Snapshot{Cursor: cursor}); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	clk := newFakeClock(base)
	_, stop := startDetector(t, Config{
		VLogs:     &vlogs.Client{BaseURL: logs.srv.URL},
		Notify:    &notify.Client{BaseURL: notifySrv.URL},
		StatePath: statePath,
		Group:     group.DefaultConfig(),
		Now:       clk.Now,
	})

	logs.send(recordLine(t, base, "proj-pn", "pn-backend-1", "pn-backend", pnError(uuidA)))
	if got := rec.waitFor(1, 15*time.Second); len(got) != 1 {
		t.Fatalf("first alert: %d notifications, want 1: %v", len(got), titles(got))
	}
	if err := stop(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if n := logs.catchUps.Load(); n != 1 {
		t.Fatalf("catch-up ran %d times with a cursor, want 1", n)
	}
	if from, _ := logs.lastFrom.Load().(string); from != cursor.Format(time.RFC3339Nano) {
		t.Errorf("catch-up started at %q, want the cursor %q", from, cursor.Format(time.RFC3339Nano))
	}
}

// TestCatchUpWindowIsCapped proves an old cursor does not open a wider window
// than the 15 minutes the design allows.
func TestCatchUpWindowIsCapped(t *testing.T) {
	logs := newFakeLogs(t)
	rec := &recorder{}
	notifySrv := httptest.NewServer(http.HandlerFunc(rec.handle))
	t.Cleanup(notifySrv.Close)

	statePath := filepath.Join(t.TempDir(), "detector.json")
	if err := state.Save(statePath, state.Snapshot{Cursor: base.Add(-24 * time.Hour)}); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	clk := newFakeClock(base)
	_, stop := startDetector(t, Config{
		VLogs:     &vlogs.Client{BaseURL: logs.srv.URL},
		Notify:    &notify.Client{BaseURL: notifySrv.URL},
		StatePath: statePath,
		Group:     group.DefaultConfig(),
		Now:       clk.Now,
	})
	logs.send(recordLine(t, base, "proj-pn", "pn-backend-1", "pn-backend", pnError(uuidA)))
	if got := rec.waitFor(1, 15*time.Second); len(got) != 1 {
		t.Fatalf("first alert: %d notifications, want 1: %v", len(got), titles(got))
	}
	if err := stop(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if n := logs.catchUps.Load(); n != 1 {
		t.Fatalf("catch-up ran %d times, want 1", n)
	}
	if from, _ := logs.lastFrom.Load().(string); from != base.Add(-15*time.Minute).Format(time.RFC3339Nano) {
		t.Errorf("catch-up started at %q, want %q", from, base.Add(-15*time.Minute).Format(time.RFC3339Nano))
	}
}

// TestInvestigatorUnavailablePostsSkipNotice proves an unreachable investigator
// costs one #errors notice instead of losing the group silently.
func TestInvestigatorUnavailablePostsSkipNotice(t *testing.T) {
	logs := newFakeLogs(t)
	rec := &recorder{}
	notifySrv := httptest.NewServer(http.HandlerFunc(rec.handle))
	t.Cleanup(notifySrv.Close)

	// A port nothing listens on: every attempt fails at the dial, so the three
	// tries cost only their backoff.
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a port: %v", err)
	}
	closed := lis.Addr().String()
	if err := lis.Close(); err != nil {
		t.Fatalf("close the reserved port: %v", err)
	}

	clk := newFakeClock(base)
	_, stop := startDetector(t, Config{
		VLogs:           &vlogs.Client{BaseURL: logs.srv.URL},
		Notify:          &notify.Client{BaseURL: notifySrv.URL},
		InvestigatorURL: "http://" + closed,
		StatePath:       filepath.Join(t.TempDir(), "detector.json"),
		Group:           group.DefaultConfig(),
		Now:             clk.Now,
	})

	logs.send(recordLine(t, base, "proj-pn", "pn-backend-1", "pn-backend", pnError(uuidA)))
	if got := rec.waitFor(1, 15*time.Second); len(got) != 1 {
		t.Fatalf("first alert: %d notifications, want 1", len(got))
	}

	// One second past the 3 minute window: the tracker closes the group and
	// hands it to the investigator, which refuses.
	clk.Set(base.Add(3*time.Minute + time.Second))
	got := rec.waitFor(2, 60*time.Second)
	if len(got) != 2 {
		t.Fatalf("got %d notifications, want the alert and the skip notice: %v", len(got), titles(got))
	}
	skip := got[1]
	if want := "AI debug skipped in proj-pn/pn-backend"; skip.n.Title != want {
		t.Errorf("skip notice title = %q, want %q", skip.n.Title, want)
	}
	if !strings.Contains(skip.n.Body, "AI debug skipped: investigator unavailable") {
		t.Errorf("skip notice body = %q", skip.n.Body)
	}
	if skip.channel != channelErrors {
		t.Errorf("skip notice channel = %q, want %q", skip.channel, channelErrors)
	}
	if n := len(rec.snapshot()); n != 2 {
		t.Errorf("%d notifications in total, want 2: one alert and one notice", n)
	}

	if err := stop(); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// TestHandoffRetriesThreeTimes proves the handoff is retried three times before
// the skip notice is queued, not attempted once.
func TestHandoffRetriesThreeTimes(t *testing.T) {
	logs := newFakeLogs(t)
	rec := &recorder{}
	notifySrv := httptest.NewServer(http.HandlerFunc(rec.handle))
	t.Cleanup(notifySrv.Close)
	inv := newInvestigator(t)
	inv.fail.Store(true)

	clk := newFakeClock(base)
	_, stop := startDetector(t, Config{
		VLogs:           &vlogs.Client{BaseURL: logs.srv.URL},
		Notify:          &notify.Client{BaseURL: notifySrv.URL},
		InvestigatorURL: inv.srv.URL,
		StatePath:       filepath.Join(t.TempDir(), "detector.json"),
		Group:           group.DefaultConfig(),
		Now:             clk.Now,
	})

	logs.send(recordLine(t, base, "proj-pn", "pn-backend-1", "pn-backend", pnError(uuidA)))
	if got := rec.waitFor(1, 15*time.Second); len(got) != 1 {
		t.Fatalf("first alert: %d notifications, want 1", len(got))
	}
	clk.Set(base.Add(3*time.Minute + time.Second))
	if got := rec.waitFor(2, 60*time.Second); len(got) != 2 {
		t.Fatalf("got %d notifications, want the alert and the skip notice: %v", len(got), titles(got))
	}
	if n := inv.count(); n != 3 {
		t.Errorf("investigator tried %d times, want 3", n)
	}
	if err := stop(); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// TestHandoffIsDelivered proves a reachable investigator gets the group, and
// that no skip notice follows it.
func TestHandoffIsDelivered(t *testing.T) {
	logs := newFakeLogs(t)
	rec := &recorder{}
	notifySrv := httptest.NewServer(http.HandlerFunc(rec.handle))
	t.Cleanup(notifySrv.Close)

	var (
		mu   sync.Mutex
		got  []group.Group
		path string
	)
	invSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		path = r.URL.Path
		var g group.Group
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&g); err != nil {
			mu.Unlock()
			http.Error(w, "bad group", http.StatusBadRequest)
			return
		}
		got = append(got, g)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(invSrv.Close)

	clk := newFakeClock(base)
	_, stop := startDetector(t, Config{
		VLogs:           &vlogs.Client{BaseURL: logs.srv.URL},
		Notify:          &notify.Client{BaseURL: notifySrv.URL},
		InvestigatorURL: invSrv.URL,
		StatePath:       filepath.Join(t.TempDir(), "detector.json"),
		Group:           group.DefaultConfig(),
		Now:             clk.Now,
	})

	logs.send(recordLine(t, base, "proj-pn", "pn-backend-1", "pn-backend", pnError(uuidA)))
	if notifs := rec.waitFor(1, 15*time.Second); len(notifs) != 1 {
		t.Fatalf("first alert: %d notifications, want 1", len(notifs))
	}
	clk.Set(base.Add(3*time.Minute + time.Second))

	// The handoff is the outcome to wait for; the notify count must then stay
	// at one, so give the detector a moment before checking it.
	deadline := time.Now().Add(15 * time.Second)
	var handed []group.Group
	for time.Now().Before(deadline) {
		mu.Lock()
		handed = append([]group.Group{}, got...)
		mu.Unlock()
		if len(handed) > 0 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if len(handed) != 1 {
		t.Fatalf("investigator got %d groups, want 1", len(handed))
	}
	if path != "/v1/investigate" {
		t.Errorf("handoff path = %q, want /v1/investigate", path)
	}
	if handed[0].Project != "proj-pn" || handed[0].Container != "pn-backend" {
		t.Errorf("handed group = %+v", handed[0])
	}
	if handed[0].Count < 1 || len(handed[0].Samples) < 1 {
		t.Errorf("handed group lost its evidence: count=%d samples=%d", handed[0].Count, len(handed[0].Samples))
	}
	if !handed[0].Handed {
		t.Error("handed group is not marked handed")
	}
	time.Sleep(100 * time.Millisecond)
	if n := len(rec.snapshot()); n != 1 {
		t.Errorf("%d notifications, want 1: a delivered handoff posts no skip notice", n)
	}
	if err := stop(); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// TestProbes covers the three probe contracts: /healthz is process-local,
// /startupz waits for the state, /readyz needs VictoriaLogs and a live tail.
func TestProbes(t *testing.T) {
	logs := newFakeLogs(t)
	rec := &recorder{}
	notifySrv := httptest.NewServer(http.HandlerFunc(rec.handle))
	t.Cleanup(notifySrv.Close)

	clk := newFakeClock(base)
	d, err := New(Config{
		VLogs:     &vlogs.Client{BaseURL: logs.srv.URL},
		Notify:    &notify.Client{BaseURL: notifySrv.URL},
		StatePath: filepath.Join(t.TempDir(), "detector.json"),
		Group:     group.DefaultConfig(),
		Now:       clk.Now,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Before Run: alive, but neither started nor ready.
	if code, body := get(t, d.Mux(), "/healthz"); code != http.StatusOK {
		t.Errorf("/healthz before Run = %d, want 200 (process-local): %s", code, body)
	}
	if code, body := get(t, d.Mux(), "/startupz"); code != http.StatusServiceUnavailable {
		t.Errorf("/startupz before Run = %d, want 503: %s", code, body)
	}
	if code, body := get(t, d.Mux(), "/readyz"); code != http.StatusServiceUnavailable {
		t.Errorf("/readyz before Run = %d, want 503: %s", code, body)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	logs.send(recordLine(t, base, "proj-pn", "pn-backend-1", "pn-backend", pnError(uuidA)))
	if got := rec.waitFor(1, 15*time.Second); len(got) != 1 {
		t.Fatalf("first alert: %d notifications, want 1", len(got))
	}

	if code, body := get(t, d.Mux(), "/startupz"); code != http.StatusOK {
		t.Errorf("/startupz while running = %d, want 200: %s", code, body)
	}
	if code, body := get(t, d.Mux(), "/readyz"); code != http.StatusOK {
		t.Errorf("/readyz while running = %d, want 200: %s", code, body)
	}

	// VictoriaLogs down: readiness fails, liveness must not.
	logs.unready.Store(true)
	if code, _ := get(t, d.Mux(), "/readyz"); code != http.StatusServiceUnavailable {
		t.Errorf("/readyz with VictoriaLogs down = %d, want 503", code)
	}
	if code, _ := get(t, d.Mux(), "/healthz"); code != http.StatusOK {
		t.Errorf("/healthz with VictoriaLogs down = %d, want 200 (liveness is process-local)", code)
	}
	if hits := clk.hits(); hits == 0 {
		t.Error("the control loop never read the wall clock")
	}
}

// TestDispatchNeverSendsInline proves the ingest and tick paths only queue, and
// that neither waits for a network send: a notify proxy that blocks a send must
// not stall the tail reader or the one-second control loop. Both calls below
// would hang forever if either path drained the queue itself.
func TestDispatchNeverSendsInline(t *testing.T) {
	logs := newFakeLogs(t)
	inv := newInvestigator(t)

	arrived := make(chan struct{}, 8)
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, 1<<20))
		arrived <- struct{}{}
		<-release // hold the send, as a capped Discord 429 wait would
		w.WriteHeader(http.StatusAccepted)
	}))
	defer slow.Close()

	clk := newFakeClock(base)
	d, err := New(Config{
		VLogs:           &vlogs.Client{BaseURL: logs.srv.URL},
		Notify:          &notify.Client{BaseURL: slow.URL},
		InvestigatorURL: inv.srv.URL,
		StatePath:       filepath.Join(t.TempDir(), "detector.json"),
		Group:           group.DefaultConfig(),
		Now:             clk.Now,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Nothing is running but the test, so a send can only happen if a caller
	// performs it. Put one notification in flight first, blocked, and then
	// exercise the two producer paths.
	d.queue.Push(notify.RenderOverflow(1, base))
	go d.queue.Drain(context.Background())
	select {
	case <-arrived:
	case <-time.After(15 * time.Second):
		t.Fatal("the fake proxy never received the seeded notification")
	}
	defer close(release)

	// Path 1: the tail reader. It classifies and pushes; it must not send.
	if !returns(t, 10*time.Second, func() {
		_ = d.ingest(decodeRecord(t, recordLine(t, base, "proj-pn", "pn-backend-1", "pn-backend", pnError(uuidA))))
	}) {
		t.Fatal("ingest blocked on the notify proxy: the tail reader must never send")
	}
	if n := d.queue.Len(); n != 2 {
		t.Fatalf("queue holds %d notifications, want 2: the alert was queued, not sent", n)
	}

	// Path 2: the control loop. One second past the window it must close the
	// group and queue the handoff, without waiting for the stuck send.
	clk.Set(base.Add(3*time.Minute + time.Second))
	if !returns(t, 10*time.Second, func() { d.tick() }) {
		t.Fatal("tick blocked on the notify proxy: the control loop must never send")
	}
	if n := len(d.handoffs); n != 1 {
		t.Errorf("tick queued %d handoffs, want 1", n)
	}
	if n := inv.count(); n != 0 {
		t.Errorf("tick posted the handoff itself (%d requests); the worker owns that", n)
	}
}

// returns reports whether fn finished within d.
func returns(t *testing.T, d time.Duration, fn func()) bool {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

// decodeRecord turns a tail line into the record the client would deliver.
func decodeRecord(t *testing.T, line string) classify.Record {
	t.Helper()
	var w struct {
		Time      string `json:"_time"`
		StreamID  string `json:"_stream_id"`
		Project   string `json:"project"`
		Pod       string `json:"pod"`
		Container string `json:"container"`
		Node      string `json:"node"`
		Message   string `json:"message"`
	}
	if err := json.Unmarshal([]byte(line), &w); err != nil {
		t.Fatalf("decode record: %v", err)
	}
	at, err := time.Parse(time.RFC3339Nano, w.Time)
	if err != nil {
		t.Fatalf("parse record time: %v", err)
	}
	return classify.Record{
		Time:      at,
		StreamID:  w.StreamID,
		Project:   w.Project,
		Pod:       w.Pod,
		Container: w.Container,
		Node:      w.Node,
		Message:   w.Message,
	}
}

// TestShutdownKeepsOpenWindowHandedOff proves a group whose window has not
// closed when the process stops is saved un-handed. Marking it handed at
// shutdown would lose the investigation forever, because the worker that posts
// it is already gone.
func TestShutdownKeepsOpenWindow(t *testing.T) {
	logs := newFakeLogs(t)
	rec := &recorder{}
	notifySrv := httptest.NewServer(http.HandlerFunc(rec.handle))
	t.Cleanup(notifySrv.Close)

	statePath := filepath.Join(t.TempDir(), "detector.json")
	clk := newFakeClock(base)
	_, stop := startDetector(t, Config{
		VLogs:           &vlogs.Client{BaseURL: logs.srv.URL},
		Notify:          &notify.Client{BaseURL: notifySrv.URL},
		InvestigatorURL: "http://investigator.invalid:8080",
		StatePath:       statePath,
		Group:           group.DefaultConfig(),
		Now:             clk.Now,
	})

	logs.send(recordLine(t, base, "proj-pn", "pn-backend-1", "pn-backend", pnError(uuidA)))
	if got := rec.waitFor(1, 15*time.Second); len(got) != 1 {
		t.Fatalf("first alert: %d notifications, want 1", len(got))
	}

	// Stop one second into the three minute window: it is still open.
	clk.Set(base.Add(time.Second))
	if err := stop(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	snap := readState(t, statePath)
	if len(snap.Groups) != 1 {
		t.Fatalf("groups = %d, want 1", len(snap.Groups))
	}
	g := snap.Groups[0]
	if g.Handed {
		t.Error("group saved as handed at shutdown: its investigation would be lost")
	}
	if !g.WindowEnd.After(base) {
		t.Errorf("window_end = %v, want after the first occurrence %v", g.WindowEnd, base)
	}
}

// TestShutdownDeliversDueHandoff proves a crash flushed at shutdown whose
// fingerprint's window had already expired still reaches the investigator.
// The second occurrence of the same panic is only assembled at shutdown, so the
// control loop never runs Observe for it; dropping that action would leave the
// group saved as handed and never investigated.
func TestShutdownDeliversDueHandoff(t *testing.T) {
	rec := &recorder{}
	notifySrv := httptest.NewServer(http.HandlerFunc(rec.handle))
	t.Cleanup(notifySrv.Close)
	inv := newInvestigator(t)

	clk := newFakeClock(base)
	d, err := New(Config{
		VLogs:           &vlogs.Client{BaseURL: "http://127.0.0.1:1"},
		Notify:          &notify.Client{BaseURL: notifySrv.URL},
		InvestigatorURL: inv.srv.URL,
		StatePath:       filepath.Join(t.TempDir(), "detector.json"),
		Group:           group.DefaultConfig(), // 3m window
		Now:             clk.Now,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ingest := func(at time.Time, msg string) {
		t.Helper()
		if err := d.ingest(decodeRecord(t,
			recordLine(t, at, "proj-health", "health-api-1", "health-api", msg))); err != nil {
			t.Fatalf("ingest: %v", err)
		}
	}

	// The first crash is complete: a JSON line closes its stack, so the group
	// exists, is alerted, and its window ends three minutes later. No sender
	// goroutine runs here, so the test drains the queue itself.
	ingest(base, panicLine)
	for _, line := range stackLines {
		ingest(base, line)
	}
	ingest(base, `{"level":"info","msg":"listening on :8080"}`)
	d.queue.Drain(context.Background())
	got := rec.snapshot()
	if len(got) != 1 {
		t.Fatalf("first alert: %d notifications, want 1", len(got))
	}
	if want := "New error in proj-health/health-api"; got[0].n.Title != want {
		t.Errorf("first alert title = %q, want %q", got[0].n.Title, want)
	}

	// The same panic again, 2 m 59 s in: it opens a second crash whose stack is
	// still arriving, so it is only assembled by the shutdown flush.
	second := base.Add(2*time.Minute + 59*time.Second)
	ingest(second, panicLine)
	ingest(second, stackLines[0])

	// Shut down past the window: the flush completes the crash, Observe sees an
	// expired, un-handed group and returns the handoff.
	clk.Set(base.Add(3*time.Minute + time.Second))
	if err := d.shutdown(); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	if n := inv.count(); n != 1 {
		t.Errorf("investigator got %d handoffs at shutdown, want 1: a due group was dropped", n)
	}
	snap := readState(t, d.cfg.StatePath)
	if len(snap.Groups) != 1 {
		t.Fatalf("groups = %d, want 1", len(snap.Groups))
	}
	if !snap.Groups[0].Handed {
		t.Error("group saved un-handed after its handoff was delivered")
	}
}

// TestShutdownDrainsQueuedHandoff proves a group the control loop already closed
// and queued is still posted at shutdown. The tracker marked it handed before
// the worker was cancelled, so dropping it here would leave the state recording
// a handoff that was never posted and never retried.
func TestShutdownDrainsQueuedHandoff(t *testing.T) {
	rec := &recorder{}
	notifySrv := httptest.NewServer(http.HandlerFunc(rec.handle))
	t.Cleanup(notifySrv.Close)
	inv := newInvestigator(t)

	clk := newFakeClock(base)
	d, err := New(Config{
		VLogs:           &vlogs.Client{BaseURL: "http://127.0.0.1:1"},
		Notify:          &notify.Client{BaseURL: notifySrv.URL},
		InvestigatorURL: inv.srv.URL,
		StatePath:       filepath.Join(t.TempDir(), "detector.json"),
		Group:           group.DefaultConfig(),
		Now:             clk.Now,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ingest := func(at time.Time, msg string) {
		t.Helper()
		if err := d.ingest(decodeRecord(t,
			recordLine(t, at, "proj-health", "health-api-1", "health-api", msg))); err != nil {
			t.Fatalf("ingest: %v", err)
		}
	}
	ingest(base, panicLine)
	for _, line := range stackLines {
		ingest(base, line)
	}
	ingest(base, `{"level":"info","msg":"listening on :8080"}`)
	d.queue.Drain(context.Background())

	// The window closes normally, so the control loop queues the handoff; no
	// worker is running, so it sits in the channel exactly as it would while a
	// shutdown is in flight.
	clk.Set(base.Add(3*time.Minute + time.Second))
	d.tick()
	if n := len(d.handoffs); n != 1 {
		t.Fatalf("tick queued %d handoffs, want 1", n)
	}
	if n := inv.count(); n != 0 {
		t.Fatalf("investigator got %d requests before shutdown, want 0", n)
	}

	if err := d.shutdown(); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if n := inv.count(); n != 1 {
		t.Errorf("investigator got %d handoffs, want 1: the queued handoff was lost at shutdown", n)
	}

	snap := readState(t, d.cfg.StatePath)
	if len(snap.Groups) != 1 {
		t.Fatalf("groups = %d, want 1", len(snap.Groups))
	}
	if !snap.Groups[0].Handed {
		t.Fatal("group saved un-handed, want handed")
	}
	// Nothing was posted about the group being skipped, because it really was
	// handed to the investigator.
	for _, r := range rec.snapshot() {
		if strings.Contains(r.n.Title, "AI debug skipped") {
			t.Errorf("skip notice posted for a delivered handoff: %q", r.n.Title)
		}
	}
}

// TestShutdownFailedHandoffPostsSkipNotice proves a group whose investigator
// refuses during the shutdown drain still gets its skip notice, and that the
// notice is queued before the state is saved. The tracker marked the group
// handed, so the notice is the only thing that stops the loss being silent.
func TestShutdownFailedHandoffPostsSkipNotice(t *testing.T) {
	rec := &recorder{}
	notifySrv := httptest.NewServer(http.HandlerFunc(rec.handle))
	t.Cleanup(notifySrv.Close)
	inv := newInvestigator(t)
	inv.fail.Store(true)

	clk := newFakeClock(base)
	d, err := New(Config{
		VLogs:           &vlogs.Client{BaseURL: "http://127.0.0.1:1"},
		Notify:          &notify.Client{BaseURL: notifySrv.URL},
		InvestigatorURL: inv.srv.URL,
		StatePath:       filepath.Join(t.TempDir(), "detector.json"),
		Group:           group.DefaultConfig(),
		Now:             clk.Now,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ingest := func(at time.Time, msg string) {
		t.Helper()
		if err := d.ingest(decodeRecord(t,
			recordLine(t, at, "proj-pn", "pn-backend-1", "pn-backend", msg))); err != nil {
			t.Fatalf("ingest: %v", err)
		}
	}
	ingest(base, pnError(uuidA))
	d.queue.Drain(context.Background())
	before := len(rec.snapshot())

	clk.Set(base.Add(3*time.Minute + time.Second))
	d.tick()
	if n := len(d.handoffs); n != 1 {
		t.Fatalf("tick queued %d handoffs, want 1", n)
	}

	if err := d.shutdown(); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if n := inv.count(); n != 3 {
		t.Errorf("investigator tried %d times at shutdown, want 3", n)
	}

	got := rec.snapshot()
	if len(got) != before+1 {
		t.Fatalf("%d notifications, want the alert and one skip notice: %v", len(got), titles(got))
	}
	skip := got[len(got)-1]
	if want := "AI debug skipped in proj-pn/pn-backend"; skip.n.Title != want {
		t.Errorf("skip notice title = %q, want %q", skip.n.Title, want)
	}
	if skip.channel != channelErrors {
		t.Errorf("skip notice channel = %q, want %q", skip.channel, channelErrors)
	}
	if !strings.Contains(skip.n.Body, "AI debug skipped: investigator unavailable") {
		t.Errorf("skip notice body = %q", skip.n.Body)
	}

	// The group is still saved as handed, which is only honest because the skip
	// notice above said it was not investigated.
	snap := readState(t, d.cfg.StatePath)
	if len(snap.Groups) != 1 {
		t.Fatalf("groups = %d, want 1", len(snap.Groups))
	}
}

// TestFullHandoffBufferLosesNothing proves a group is never dropped when the
// handoff channel is full: it moves to the spill list, and the worker that
// serves that list posts it. Before the spill list existed this group was only
// logged and then lost for good, because the tracker had already marked it
// handed.
// TestSaveBetweenCloseAndHandoffKeepsGroupUnhanded proves the window between
// the tracker closing a group and the investigator accepting it cannot persist
// the group as investigated. The tracker marks a group handed as it closes it,
// so a save in that window would otherwise record an investigation that never
// happened, and the next start would never retry it.
func TestSaveBetweenCloseAndHandoffKeepsGroupUnhanded(t *testing.T) {
	rec := &recorder{}
	notifySrv := httptest.NewServer(http.HandlerFunc(rec.handle))
	t.Cleanup(notifySrv.Close)
	inv := newInvestigator(t)

	clk := newFakeClock(base)
	d, err := New(Config{
		VLogs:           &vlogs.Client{BaseURL: "http://127.0.0.1:1"},
		Notify:          &notify.Client{BaseURL: notifySrv.URL},
		InvestigatorURL: inv.srv.URL,
		StatePath:       filepath.Join(t.TempDir(), "detector.json"),
		Group:           group.DefaultConfig(),
		Now:             clk.Now,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := d.ingest(decodeRecord(t,
		recordLine(t, base, "proj-pn", "pn-backend-1", "pn-backend", pnError(uuidA)))); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	d.queue.Drain(context.Background())

	// Close the window. tick routes the handoff but no worker runs it, which is
	// exactly the moment a save must not believe the investigation happened.
	clk.Set(base.Add(3*time.Minute + time.Second))
	d.tick()
	if n := len(d.handoffs); n != 1 {
		t.Fatalf("tick queued %d handoffs, want 1", n)
	}

	d.mu.Lock()
	pending := d.snapshotGroups()
	d.mu.Unlock()
	if len(pending) != 1 {
		t.Fatalf("groups = %d, want 1", len(pending))
	}
	if pending[0].Handed {
		t.Fatal("group saved as handed before the investigator accepted it: the next start would not retry")
	}

	// Once the investigator accepts it, the same snapshot keeps Handed.
	g, ok := d.takeHandoffNow()
	if !ok {
		t.Fatal("no handoff waiting")
	}
	if err := d.deliverHandoff(context.Background(), g); err != nil {
		t.Fatalf("deliverHandoff: %v", err)
	}
	if n := inv.count(); n != 1 {
		t.Errorf("investigator tried %d times, want 1", n)
	}
	d.mu.Lock()
	delivered := d.snapshotGroups()
	d.mu.Unlock()
	if len(delivered) != 1 || !delivered[0].Handed {
		t.Fatalf("groups after delivery = %+v, want one handed group", delivered)
	}
}

// TestShutdownStagesOneAggregateForManySkips proves a shutdown that leaves far
// more groups unposted than the notification queue holds reports them once, not
// 133 times, and keeps every group covered: the aggregate's count stands for all
// of them, including the ones whose fingerprint it does not name.
func TestShutdownStagesOneAggregateForManySkips(t *testing.T) {
	rec := &recorder{}
	notifySrv := httptest.NewServer(http.HandlerFunc(rec.handle))
	t.Cleanup(notifySrv.Close)

	d, err := New(Config{
		VLogs:     &vlogs.Client{BaseURL: "http://127.0.0.1:1"},
		Notify:    &notify.Client{BaseURL: notifySrv.URL},
		StatePath: filepath.Join(t.TempDir(), "detector.json"),
		Group:     group.DefaultConfig(),
		Now:       newFakeClock(base).Now,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	const total = 133
	// The groups must be in the tracker, or the snapshot cannot express their
	// Handed bit at all: snapshotGroups prunes cycles it does not hold, so
	// synthetic groups would pass the assertion for the wrong reason.
	seeded := make([]group.Group, total)
	for i := range seeded {
		seeded[i] = group.Group{
			Fingerprint: fmt.Sprintf("skip-%03d", i),
			Project:     "proj-pn",
			Container:   "pn-backend",
			WindowEnd:   base.Add(3 * time.Minute),
			Handed:      true,
		}
	}
	d.tracker.Import(seeded)
	for _, g := range seeded {
		d.markUnresolved(g)
		d.stageSkip(g)
	}
	if n := d.uncoveredGroups(); n != total {
		t.Fatalf("uncovered = %d, want %d", n, total)
	}

	if err := d.shutdown(); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	got := rec.snapshot()
	if len(got) != 1 {
		t.Fatalf("%d notifications, want one aggregate: %v", len(got), titles(got))
	}
	if want := fmt.Sprintf("AI debug skipped for %d groups", total); got[0].n.Title != want {
		t.Errorf("title = %q, want %q", got[0].n.Title, want)
	}
	if !strings.Contains(got[0].n.Body, fmt.Sprint(total)) {
		t.Errorf("body %q lost the group count", got[0].n.Body)
	}
	if got[0].channel != channelErrors {
		t.Errorf("channel = %q, want %q", got[0].channel, channelErrors)
	}
	// Every group's notice was in the queue at the save, so no cycle is left
	// unresolved: the snapshot may keep them handed, and no retry is promised
	// for an investigation that was already reported as skipped.
	if n := d.uncoveredGroups(); n != 0 {
		t.Errorf("uncovered after shutdown = %d, want 0", n)
	}
	snap := readState(t, d.cfg.StatePath)
	if len(snap.Groups) != total {
		t.Fatalf("groups saved = %d, want %d", len(snap.Groups), total)
	}
	for _, g := range snap.Groups {
		if !g.Handed {
			t.Errorf("group %q saved un-handed: its skip notice was in the queue at the save",
				g.Fingerprint)
		}
	}
}

func TestFullHandoffBufferLosesNothing(t *testing.T) {
	rec := &recorder{}
	notifySrv := httptest.NewServer(http.HandlerFunc(rec.handle))
	t.Cleanup(notifySrv.Close)

	var (
		mu   sync.Mutex
		seen []string
	)
	invSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var g group.Group
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&g); err != nil {
			http.Error(w, "bad group", http.StatusBadRequest)
			return
		}
		mu.Lock()
		seen = append(seen, g.Fingerprint)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(invSrv.Close)

	clk := newFakeClock(base)
	d, err := New(Config{
		VLogs:           &vlogs.Client{BaseURL: "http://127.0.0.1:1"},
		Notify:          &notify.Client{BaseURL: notifySrv.URL},
		InvestigatorURL: invSrv.URL,
		StatePath:       filepath.Join(t.TempDir(), "detector.json"),
		Group:           group.DefaultConfig(),
		Now:             clk.Now,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Fill the channel exactly, so the next enqueue has nowhere to go but the
	// spill list.
	for i := range cap(d.handoffs) {
		d.enqueueHandoff(group.Group{Fingerprint: fmt.Sprintf("filler-%03d", i)})
	}
	if n := len(d.handoffs); n != cap(d.handoffs) {
		t.Fatalf("channel holds %d groups, want %d", n, cap(d.handoffs))
	}

	sentinel := group.Group{
		Fingerprint: "sentinel",
		Project:     "proj-pn",
		Container:   "pn-backend",
		Handed:      true,
	}
	d.enqueueHandoff(sentinel)
	if n := len(d.handoffs); n != cap(d.handoffs) {
		t.Fatalf("the overflowing group entered a full channel: %d entries", n)
	}
	d.handoffMu.Lock()
	spilled := len(d.spill)
	first := ""
	if spilled > 0 {
		first = d.spill[0].Fingerprint
	}
	d.handoffMu.Unlock()
	if spilled != 1 {
		t.Fatalf("spill holds %d groups, want 1: the overflowing group was dropped", spilled)
	}
	if first != sentinel.Fingerprint {
		t.Fatalf("spill holds %q, want the overflowing %q", first, sentinel.Fingerprint)
	}
	// Nothing was posted about it being skipped: it is queued, not lost.
	if n := len(rec.snapshot()); n != 0 {
		t.Fatalf("%d notifications, want 0: a spilled group is not a skip", n)
	}

	// The worker serves the spill list before the channel, so the group the
	// channel refused is the first one the investigator receives.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.handoffWorker(ctx)
	}()
	deadline := time.Now().Add(15 * time.Second)
	for {
		mu.Lock()
		n := len(seen)
		mu.Unlock()
		if n > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	cancel()
	<-done

	mu.Lock()
	got := append([]string{}, seen...)
	mu.Unlock()
	if len(got) == 0 {
		t.Fatal("investigator got nothing: the spilled group was lost")
	}
	if got[0] != sentinel.Fingerprint {
		t.Errorf("first handoff = %q, want the spilled %q (spill is served before the channel)",
			got[0], sentinel.Fingerprint)
	}
	// The sentinel left the spill list for the investigator. Any group still
	// there is a filler the cancellation cut short and put back, which the
	// shutdown drain owns, never the sentinel.
	d.handoffMu.Lock()
	spill := append([]group.Group{}, d.spill...)
	d.handoffMu.Unlock()
	for _, g := range spill {
		if g.Fingerprint == sentinel.Fingerprint {
			t.Errorf("the spilled group is still queued after being handed off")
		}
	}
}

// TestOverflowReportedWhenWindowRollsUnderLoad proves the "+N more new errors"
// summary is not starved by a steady stream of new errors. The limiter keeps
// its refused count across a window roll, so the roll itself must harvest it:
// otherwise the summary waits for a quiet minute that continuous errors never
// produce, and the count is then attributed to the wrong window.
func TestOverflowReportedWhenWindowRollsUnderLoad(t *testing.T) {
	rec := &recorder{}
	notifySrv := httptest.NewServer(http.HandlerFunc(rec.handle))
	t.Cleanup(notifySrv.Close)

	clk := newFakeClock(base)
	d, err := New(Config{
		VLogs:     &vlogs.Client{BaseURL: "http://127.0.0.1:1"},
		Notify:    &notify.Client{BaseURL: notifySrv.URL},
		StatePath: filepath.Join(t.TempDir(), "detector.json"),
		Group:     group.DefaultConfig(),
		Now:       clk.Now,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Twelve distinct errors in the first window: ten pass the cap, two are
	// refused.
	for i := 1; i <= 12; i++ {
		if err := d.ingest(decodeRecord(t,
			recordLine(t, base, "proj-pn", "pn-backend-1", "pn-backend", burstError(i)))); err != nil {
			t.Fatalf("ingest %d: %v", i, err)
		}
	}
	// One more error one second past the window boundary rolls the burst
	// window while the stream continues, which is the case that used to defer
	// the summary indefinitely.
	roll := base.Add(time.Minute + time.Second)
	clk.Set(roll)
	if err := d.ingest(decodeRecord(t,
		recordLine(t, roll, "proj-pn", "pn-backend-1", "pn-backend", burstError(13)))); err != nil {
		t.Fatalf("ingest 13: %v", err)
	}

	d.queue.Drain(context.Background())
	got := rec.snapshot()

	var overflow []recorded
	for _, r := range got {
		if strings.HasPrefix(r.n.Title, "+") {
			overflow = append(overflow, r)
		}
	}
	if len(overflow) != 1 {
		t.Fatalf("got %d overflow messages, want 1: the roll did not harvest the previous window: %v",
			len(overflow), titles(got))
	}
	if want := "+2 more new errors"; overflow[0].n.Title != want {
		t.Errorf("overflow title = %q, want %q", overflow[0].n.Title, want)
	}
	if want := base.Format(time.RFC3339); fieldValue(overflow[0].n, "Since") != want {
		t.Errorf("overflow since = %q, want the first window start %q",
			fieldValue(overflow[0].n, "Since"), want)
	}
}

// TestShutdownSkipNoticeSurvivesProxyOutage proves the skip notice a failed
// shutdown handoff earns is already in the notification queue when the final
// state is written. Nothing else can hold it: the group is saved handed, so the
// notice is the only record that its investigation never happened. The proxy
// refuses every post here, so what the test finds still queued at return is
// exactly what the final drain had to attempt.
func TestShutdownSkipNoticeSurvivesProxyOutage(t *testing.T) {
	var posts atomic.Int32
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts.Add(1)
		_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, 1<<20))
		http.Error(w, "proxy down", http.StatusServiceUnavailable)
	}))
	t.Cleanup(down.Close)
	inv := newInvestigator(t)
	inv.fail.Store(true)

	clk := newFakeClock(base)
	statePath := filepath.Join(t.TempDir(), "detector.json")
	d, err := New(Config{
		VLogs:           &vlogs.Client{BaseURL: "http://127.0.0.1:1"},
		Notify:          &notify.Client{BaseURL: down.URL},
		InvestigatorURL: inv.srv.URL,
		StatePath:       statePath,
		Group:           group.DefaultConfig(),
		Now:             clk.Now,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := d.ingest(decodeRecord(t,
		recordLine(t, base, "proj-pn", "pn-backend-1", "pn-backend", pnError(uuidA)))); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	d.queue.Drain(context.Background()) // fails; the alert stays queued

	clk.Set(base.Add(3*time.Minute + time.Second))
	d.tick()
	if n := len(d.handoffs); n != 1 {
		t.Fatalf("tick queued %d handoffs, want 1", n)
	}

	if err := d.shutdown(); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if n := inv.count(); n != 3 {
		t.Errorf("investigator tried %d times at shutdown, want 3", n)
	}
	if posts.Load() == 0 {
		t.Error("the notify proxy was never asked to deliver: nothing was queued")
	}

	// The alert and the skip notice are both still queued: the proxy never
	// accepted either, and neither was dropped.
	if n := d.queue.Len(); n != 2 {
		t.Errorf("queue holds %d notifications, want the alert and the skip notice", n)
	}
	// The state was written anyway, and it records the group as handed, which
	// is exactly why the notice above must not be lost.
	snap := readState(t, statePath)
	if len(snap.Groups) != 1 || !snap.Groups[0].Handed {
		t.Fatalf("state after shutdown = %+v, want one handed group", snap.Groups)
	}
}

// TestFailedNotificationIsRetried proves a proxy that refuses once does not cost
// the message: it stays queued and the retry cadence delivers it.
func TestFailedNotificationIsRetried(t *testing.T) {
	logs := newFakeLogs(t)

	var (
		mu    sync.Mutex
		tries int
		got   []notify.Notification
	)
	flaky := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var n notify.Notification
		_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&n)
		mu.Lock()
		tries++
		first := tries == 1
		if !first {
			got = append(got, n)
		}
		mu.Unlock()
		if first {
			http.Error(w, "proxy restarting", http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(flaky.Close)

	// The retry cadence is 30 s in production; shorten it for the test.
	oldRetry := retryInterval
	retryInterval = 50 * time.Millisecond
	t.Cleanup(func() { retryInterval = oldRetry })

	clk := newFakeClock(base)
	_, stop := startDetector(t, Config{
		VLogs:     &vlogs.Client{BaseURL: logs.srv.URL},
		Notify:    &notify.Client{BaseURL: flaky.URL},
		StatePath: filepath.Join(t.TempDir(), "detector.json"),
		Group:     group.DefaultConfig(),
		Now:       clk.Now,
	})

	logs.send(recordLine(t, base, "proj-pn", "pn-backend-1", "pn-backend", pnError(uuidA)))
	deadline := time.Now().Add(15 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if tries < 2 {
		t.Fatalf("the notify proxy saw %d attempts, want the refused one and a retry", tries)
	}
	if len(got) != 1 {
		t.Fatalf("delivered %d notifications, want 1", len(got))
	}
	if want := "New error in proj-pn/pn-backend"; got[0].Title != want {
		t.Errorf("retried notification title = %q, want %q", got[0].Title, want)
	}
	if err := stop(); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// TestCooldownRecurrenceRendersAsRecurring proves the detector renders a
// NotifyNew action through RenderNewAction: a recurrence whose cooldown expired
// without a single counted occurrence has SinceLastAlert 0, so RenderNew would
// call it "New error" while the action says it recurred.
func TestCooldownRecurrenceRendersAsRecurring(t *testing.T) {
	logs := newFakeLogs(t)
	rec := &recorder{}
	notifySrv := httptest.NewServer(http.HandlerFunc(rec.handle))
	t.Cleanup(notifySrv.Close)

	clk := newFakeClock(base)
	_, stop := startDetector(t, Config{
		VLogs:     &vlogs.Client{BaseURL: logs.srv.URL},
		Notify:    &notify.Client{BaseURL: notifySrv.URL},
		StatePath: filepath.Join(t.TempDir(), "detector.json"),
		Group:     group.DefaultConfig(), // 3m window, 6h cooldown
		Now:       clk.Now,
	})

	logs.send(recordLine(t, base, "proj-pn", "pn-backend-1", "pn-backend", pnError(uuidA)))
	if got := rec.waitFor(1, 15*time.Second); len(got) != 1 {
		t.Fatalf("first alert: %d notifications, want 1", len(got))
	}

	// Close the window, then wait out the whole cooldown without a single
	// occurrence, so nothing is counted since the last alert.
	clk.Set(base.Add(3*time.Minute + time.Second))
	time.Sleep(200 * time.Millisecond)
	later := base.Add(6*time.Hour + 4*time.Minute)

	// The recurrence: the next occurrence, in the same second as the clock.
	clk.Set(later)
	logs.send(recordLine(t, later, "proj-pn", "pn-backend-1", "pn-backend", pnError(uuidB)))
	got := rec.waitFor(2, 15*time.Second)
	if len(got) != 2 {
		t.Fatalf("got %d notifications, want the first alert and the recurrence: %v", len(got), titles(got))
	}
	if want := "Recurring error in proj-pn/pn-backend"; got[1].n.Title != want {
		t.Errorf("recurrence title = %q, want %q", got[1].n.Title, want)
	}
	if strings.Contains(got[1].n.Body, "occurrences since last alert") {
		t.Errorf("a recurrence with no counted occurrences reported a count: %q", got[1].n.Body)
	}
	if err := stop(); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// TestCrashFlushUsesRecordTime proves the assembler watermark is the wall clock
// minus the tail offset: a panic whose stack is still within that offset must
// not be flushed with an empty stack.
func TestCrashFlushUsesRecordTime(t *testing.T) {
	logs := newFakeLogs(t)
	rec := &recorder{}
	notifySrv := httptest.NewServer(http.HandlerFunc(rec.handle))
	t.Cleanup(notifySrv.Close)

	clk := newFakeClock(base)
	_, stop := startDetector(t, Config{
		VLogs:     &vlogs.Client{BaseURL: logs.srv.URL},
		Notify:    &notify.Client{BaseURL: notifySrv.URL},
		StatePath: filepath.Join(t.TempDir(), "detector.json"),
		Group:     group.DefaultConfig(),
		Now:       clk.Now,
	})

	// The signature is 3.5 s old: inside the five second tail lag, so a flush
	// against wall time minus only the 2 s silence would already cut it off,
	// while the correct watermark (wall time minus the 5 s tail offset, minus
	// the 2 s silence) keeps it open for its stack.
	at := base.Add(-3500 * time.Millisecond)
	logs.send(recordLine(t, at, "proj-health", "health-api-1", "health-api", panicLine))
	time.Sleep(300 * time.Millisecond)
	if got := rec.snapshot(); len(got) != 0 {
		t.Fatalf("the crash was flushed before its stack arrived: %q", got[0].n.Body)
	}

	// The stack follows: it must travel with the alert.
	for _, line := range stackLines {
		logs.send(recordLine(t, at, "proj-health", "health-api-1", "health-api", line))
	}
	logs.send(recordLine(t, at, "proj-health", "health-api-1", "health-api", `{"level":"info","msg":"listening on :8080"}`))
	got := rec.waitFor(1, 15*time.Second)
	if len(got) != 1 {
		t.Fatalf("got %d notifications, want 1", len(got))
	}
	for _, line := range stackLines {
		if !strings.Contains(got[0].n.Body, strings.TrimSpace(line)) {
			t.Errorf("alert body lost %q: %q", line, got[0].n.Body)
		}
	}
	if err := stop(); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// get performs one GET against mux and returns the status and body.
func get(t *testing.T, mux http.Handler, path string) (int, string) {
	t.Helper()
	srv := httptest.NewServer(mux)
	defer srv.Close()
	resp, err := http.Get(srv.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return resp.StatusCode, strings.TrimSpace(string(body))
}

// TestNewRejectsIncompleteConfig keeps a misconfigured process from starting and
// silently dropping everything it reads.
func TestNewRejectsIncompleteConfig(t *testing.T) {
	ok := Config{
		VLogs:     &vlogs.Client{BaseURL: "http://localhost:9428"},
		Notify:    &notify.Client{BaseURL: "http://localhost:8080"},
		StatePath: filepath.Join(t.TempDir(), "detector.json"),
	}
	if _, err := New(ok); err != nil {
		t.Fatalf("New with a complete config: %v", err)
	}

	noLogs := ok
	noLogs.VLogs = nil
	if _, err := New(noLogs); err == nil {
		t.Error("New accepted a nil VLogs client")
	}

	noNotify := ok
	noNotify.Notify = nil
	if _, err := New(noNotify); err == nil {
		t.Error("New accepted a nil Notify client")
	}

	noState := ok
	noState.StatePath = " "
	if _, err := New(noState); err == nil {
		t.Error("New accepted an empty StatePath")
	}
}
