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

var base = time.Date(2026, 10, 29, 9, 0, 0, 0, time.UTC)

func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	tickInterval = 10 * time.Millisecond
	os.Exit(m.Run())
}

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

type fakeLogs struct {
	t        *testing.T
	srv      *httptest.Server
	lines    chan string
	qLines   chan string
	unready  atomic.Bool
	catchUps atomic.Int32
	lastFrom atomic.Value
}

func newFakeLogs(t *testing.T) *fakeLogs {
	f := &fakeLogs{t: t, lines: make(chan string, 128), qLines: make(chan string, 128)}
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
		if r.Header.Get("ProjectID") == "1" {
			f.tailQuarantine(w, r)
		} else {
			f.tail(w, r)
		}
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

func (f *fakeLogs) tailQuarantine(w http.ResponseWriter, r *http.Request) {
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
		case line := <-f.qLines:
			if _, err := fmt.Fprintln(w, line); err != nil {
				return
			}
			fl.Flush()
		}
	}
}

func (f *fakeLogs) send(line string) {
	f.t.Helper()
	select {
	case f.lines <- line:
	case <-time.After(5 * time.Second):
		f.t.Fatal("the tail did not accept a line within 5s")
	}
}

func (f *fakeLogs) sendQuarantine(line string) {
	f.t.Helper()
	select {
	case f.qLines <- line:
	case <-time.After(5 * time.Second):
		f.t.Fatal("the quarantine tail did not accept a line within 5s")
	}
}

type recorded struct {
	channel string
	n       notify.Notification
}

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

func TestEndToEnd(t *testing.T) {
	logs := newFakeLogs(t)
	rec := &recorder{}
	notifySrv := httptest.NewServer(http.HandlerFunc(rec.handle))
	t.Cleanup(notifySrv.Close)

	statePath := filepath.Join(t.TempDir(), "detector.json")
	stale := statePath + ".tmp-1234567890"
	if err := os.WriteFile(stale, []byte(`{"version":1`), 0o600); err != nil {
		t.Fatalf("write stale temp file: %v", err)
	}

	clk := newFakeClock(base)
	_, stop := startDetector(t, Config{
		VLogs:           &vlogs.Client{BaseURL: logs.srv.URL},
		Notify:          &notify.Client{BaseURL: notifySrv.URL},
		InvestigatorURL: "",
		StatePath:       statePath,
		Group:           group.DefaultConfig(),
		Now:             clk.Now,
	})

	pnEvent, kind := classify.Line(pnError(uuidA))
	if kind != classify.Error {
		t.Fatalf("PN line classifies as %v, want Error", kind)
	}
	wantPN := fingerprint.Of(classify.Classified{
		Record: classify.Record{Project: "proj-pn", Container: "pn-backend"},
		Event:  pnEvent,
	})

	logs.send(recordLine(t, base, "proj-pn", "pn-backend-1", "pn-backend", pnError(uuidA)))
	got := rec.waitFor(1, 15*time.Second)
	if len(got) != 1 {
		t.Fatalf("after the first error: %d notifications, want 1: %v", len(got), titles(got))
	}
	if fp := fieldValue(got[0].n, "Fingerprint"); fp != wantPN {
		t.Errorf("first alert fingerprint = %q, want %q", fp, wantPN)
	}

	clk.Set(base.Add(60 * time.Second))
	logs.send(recordLine(t, base.Add(30*time.Second), "proj-pn", "pn-backend-1", "pn-backend", pnError(uuidB)))

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

	clk.Set(base.Add(240 * time.Second))
	for i := 1; i <= 12; i++ {
		logs.send(recordLine(t, base.Add(240*time.Second), "proj-pn", "pn-backend-1", "pn-backend", burstError(i)))
	}
	got = rec.waitFor(12, 15*time.Second)
	if len(got) != 12 {
		t.Fatalf("after the burst: %d notifications, want 12: %v", len(got), titles(got))
	}

	clk.Set(base.Add(300 * time.Second))
	got = rec.waitFor(13, 15*time.Second)
	if len(got) != 13 {
		t.Fatalf("after the burst window: %d notifications, want 13: %v", len(got), titles(got))
	}

	for _, r := range got {
		if r.channel != channelErrors {
			t.Errorf("posted to channel %q, want %q (title %q)", r.channel, channelErrors, r.n.Title)
		}
	}

	byPrint := map[string]int{}
	for _, r := range got {
		byPrint[fieldValue(r.n, "Fingerprint")]++
	}
	if n := byPrint[wantPN]; n != 1 {
		t.Errorf("PN error alerted %d times, want 1 (both UUID variants are one error)", n)
	}

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

	if got := rec.waitFor(14, 5*time.Second); len(got) != 13 {
		t.Errorf("%d notifications in total, want 13: an attempted handoff adds a skip notice per closed window: %v",
			len(got), titles(got))
	}

	if n := logs.catchUps.Load(); n != 0 {
		t.Errorf("catch-up ran %d times on a first start, want 0", n)
	}

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

func TestInvestigatorUnavailablePostsSkipNotice(t *testing.T) {
	logs := newFakeLogs(t)
	rec := &recorder{}
	notifySrv := httptest.NewServer(http.HandlerFunc(rec.handle))
	t.Cleanup(notifySrv.Close)

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

func TestDispatchNeverSendsInline(t *testing.T) {
	logs := newFakeLogs(t)
	inv := newInvestigator(t)

	arrived := make(chan struct{}, 8)
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, 1<<20))
		arrived <- struct{}{}
		<-release
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

	d.queue.Push(notify.RenderOverflow(1, base))
	go d.queue.Drain(context.Background())
	select {
	case <-arrived:
	case <-time.After(15 * time.Second):
		t.Fatal("the fake proxy never received the seeded notification")
	}
	defer close(release)

	if !returns(t, 10*time.Second, func() {
		_ = d.ingest(decodeRecord(t, recordLine(t, base, "proj-pn", "pn-backend-1", "pn-backend", pnError(uuidA))))
	}) {
		t.Fatal("ingest blocked on the notify proxy: the tail reader must never send")
	}
	if n := d.queue.Len(); n != 2 {
		t.Fatalf("queue holds %d notifications, want 2: the alert was queued, not sent", n)
	}

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
	got := rec.snapshot()
	if len(got) != 1 {
		t.Fatalf("first alert: %d notifications, want 1", len(got))
	}
	if want := "New error in proj-health/health-api"; got[0].n.Title != want {
		t.Errorf("first alert title = %q, want %q", got[0].n.Title, want)
	}

	second := base.Add(2*time.Minute + 59*time.Second)
	ingest(second, panicLine)
	ingest(second, stackLines[0])

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

	for _, r := range rec.snapshot() {
		if strings.Contains(r.n.Title, "AI debug skipped") {
			t.Errorf("skip notice posted for a delivered handoff: %q", r.n.Title)
		}
	}
}

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

	snap := readState(t, d.cfg.StatePath)
	if len(snap.Groups) != 1 {
		t.Fatalf("groups = %d, want 1", len(snap.Groups))
	}
}

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

	if n := len(rec.snapshot()); n != 0 {
		t.Fatalf("%d notifications, want 0: a spilled group is not a skip", n)
	}

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

	d.handoffMu.Lock()
	spill := append([]group.Group{}, d.spill...)
	d.handoffMu.Unlock()
	for _, g := range spill {
		if g.Fingerprint == sentinel.Fingerprint {
			t.Errorf("the spilled group is still queued after being handed off")
		}
	}
}

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

	for i := 1; i <= 12; i++ {
		if err := d.ingest(decodeRecord(t,
			recordLine(t, base, "proj-pn", "pn-backend-1", "pn-backend", burstError(i)))); err != nil {
			t.Fatalf("ingest %d: %v", i, err)
		}
	}

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
	d.queue.Drain(context.Background())

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

	if n := d.queue.Len(); n != 2 {
		t.Errorf("queue holds %d notifications, want the alert and the skip notice", n)
	}

	snap := readState(t, statePath)
	if len(snap.Groups) != 1 || !snap.Groups[0].Handed {
		t.Fatalf("state after shutdown = %+v, want one handed group", snap.Groups)
	}
}

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
		Group:     group.DefaultConfig(),
		Now:       clk.Now,
	})

	logs.send(recordLine(t, base, "proj-pn", "pn-backend-1", "pn-backend", pnError(uuidA)))
	if got := rec.waitFor(1, 15*time.Second); len(got) != 1 {
		t.Fatalf("first alert: %d notifications, want 1", len(got))
	}

	clk.Set(base.Add(3*time.Minute + time.Second))
	time.Sleep(200 * time.Millisecond)
	later := base.Add(6*time.Hour + 4*time.Minute)

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

	at := base.Add(-3500 * time.Millisecond)
	logs.send(recordLine(t, at, "proj-health", "health-api-1", "health-api", panicLine))
	time.Sleep(300 * time.Millisecond)
	if got := rec.snapshot(); len(got) != 0 {
		t.Fatalf("the crash was flushed before its stack arrived: %q", got[0].n.Body)
	}

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

func TestQuarantineImmediateAlertAndRateLimiting(t *testing.T) {
	rec := &recorder{}
	notifySrv := httptest.NewServer(http.HandlerFunc(rec.handle))
	defer notifySrv.Close()

	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	clk := newFakeClock(now)

	d, err := New(Config{
		VLogs:     &vlogs.Client{BaseURL: "http://localhost:9428"},
		Notify:    &notify.Client{BaseURL: notifySrv.URL},
		StatePath: filepath.Join(t.TempDir(), "detector.json"),
		Now:       clk.Now,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	q1 := vlogs.QuarantineRecord{
		Time:      now,
		Project:   "proj-pn",
		Container: "backend",
		Message:   "raw broken text line 1",
	}
	if err := d.IngestQuarantine(q1); err != nil {
		t.Fatalf("IngestQuarantine: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	d.queue.Drain(ctx)

	got := rec.snapshot()
	if len(got) != 1 {
		t.Fatalf("expected 1 immediate quarantine alert, got %d", len(got))
	}
	if got[0].n.Title != "Quarantined log in proj-pn/backend" {
		t.Errorf("title = %q, want 'Quarantined log in proj-pn/backend'", got[0].n.Title)
	}
	if !strings.Contains(got[0].n.Body, "raw broken text line 1") {
		t.Errorf("body missing first line: %q", got[0].n.Body)
	}

	clk.Set(now.Add(10 * time.Minute))
	q2 := vlogs.QuarantineRecord{
		Time:      now.Add(10 * time.Minute),
		Project:   "proj-pn",
		Container: "backend",
		Message:   "raw broken text line 2",
	}
	if err := d.IngestQuarantine(q2); err != nil {
		t.Fatalf("IngestQuarantine: %v", err)
	}
	d.queue.Drain(ctx)
	if len(rec.snapshot()) != 1 {
		t.Fatalf("quarantine alert was not throttled, got %d alerts", len(rec.snapshot()))
	}

	clk.Set(now.Add(65 * time.Minute))
	d.tick()
	d.queue.Drain(ctx)

	got = rec.snapshot()
	if len(got) != 2 {
		t.Fatalf("expected 2 quarantine alerts after 1h tick, got %d", len(got))
	}
	if got[1].n.Title != "Quarantined logs in proj-pn/backend (+0 occurrences)" && !strings.Contains(got[1].n.Title, "Quarantined") {
		t.Errorf("throttled alert title = %q", got[1].n.Title)
	}
}

func TestRedactionMarkerImmediateAlert(t *testing.T) {
	rec := &recorder{}
	notifySrv := httptest.NewServer(http.HandlerFunc(rec.handle))
	defer notifySrv.Close()

	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	clk := newFakeClock(now)

	d, err := New(Config{
		VLogs:     &vlogs.Client{BaseURL: "http://localhost:9428"},
		Notify:    &notify.Client{BaseURL: notifySrv.URL},
		StatePath: filepath.Join(t.TempDir(), "detector.json"),
		Now:       clk.Now,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	r := classify.Record{
		Time:           now,
		Project:        "proj-pn",
		Container:      "backend",
		ServiceVersion: "v1.4.2",
		Message:        `{"level":"info","msg":"connecting with Bearer [REDACTED:bearer]"}`,
	}
	if err := d.Ingest(r); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	d.queue.Drain(ctx)

	got := rec.snapshot()
	if len(got) != 1 {
		t.Fatalf("expected 1 redaction marker alert, got %d", len(got))
	}
	if got[0].n.Title != "Secret redacted in proj-pn/backend@v1.4.2" {
		t.Errorf("title = %q", got[0].n.Title)
	}
	if fieldValue(got[0].n, "Marker Kind") != "bearer" {
		t.Errorf("marker kind = %q, want 'bearer'", fieldValue(got[0].n, "Marker Kind"))
	}
}

func TestDailyViolationDigest(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	clk := newFakeClock(now)

	d, err := New(Config{
		VLogs:     &vlogs.Client{BaseURL: "http://localhost:9428"},
		Notify:    &notify.Client{BaseURL: "http://localhost:8080"},
		StatePath: filepath.Join(t.TempDir(), "detector.json"),
		Now:       clk.Now,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	r1 := classify.Record{
		Time:         now,
		Project:      "proj-pn",
		Container:    "backend",
		TimeRejected: true,
		Message:      `{"level":"info","msg":"hello"}`,
	}
	_ = d.Ingest(r1)

	r2 := classify.Record{
		Time:      now,
		Project:   "proj-pn",
		Container: "backend",
		Message:   `{"level":"info","msg":"hello","user_1234_id":"val"}`,
	}
	_ = d.Ingest(r2)

	r3 := classify.Record{
		Time:       now,
		Project:    "proj-pn",
		Container:  "backend",
		DynamicKey: true,
		Message:    `{"level":"info","msg":"dynamic"}`,
	}
	_ = d.Ingest(r3)

	q := vlogs.QuarantineRecord{
		Time:      now,
		Project:   "proj-pn",
		Container: "backend",
		Message:   "raw unparsed line",
	}
	_ = d.IngestQuarantine(q)

	violations := d.Violations()
	pnV := violations["proj-pn/backend"]
	if pnV["time-rejected"] != 1 {
		t.Errorf("time-rejected = %d, want 1", pnV["time-rejected"])
	}
	if pnV["dynamic-key"] != 2 {
		t.Errorf("dynamic-key = %d, want 2", pnV["dynamic-key"])
	}
	if pnV["quarantined-line"] != 1 {
		t.Errorf("quarantined-line = %d, want 1", pnV["quarantined-line"])
	}

	digest, ok := d.FlushViolationsDigest(now.Add(24 * time.Hour))
	if !ok {
		t.Fatalf("FlushViolationsDigest returned false, want true")
	}
	if digest.Title != "Daily log contract violation digest" {
		t.Errorf("digest title = %q", digest.Title)
	}
	if !strings.Contains(digest.Body, "time-rejected = 1") || !strings.Contains(digest.Body, "dynamic-key = 2") {
		t.Errorf("digest body = %q", digest.Body)
	}
}

func TestRedactionMarkerInExceptionFields(t *testing.T) {
	rec := &recorder{}
	notifySrv := httptest.NewServer(http.HandlerFunc(rec.handle))
	defer notifySrv.Close()

	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	clk := newFakeClock(now)

	d, err := New(Config{
		VLogs:     &vlogs.Client{BaseURL: "http://localhost:9428"},
		Notify:    &notify.Client{BaseURL: notifySrv.URL},
		StatePath: filepath.Join(t.TempDir(), "detector.json"),
		Now:       clk.Now,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	r := classify.Record{
		Time:             now,
		Project:          "proj-pn",
		Container:        "backend",
		ServiceVersion:   "v1.4.2",
		ExceptionMessage: "failed connecting with password [REDACTED:password]",
		Stacktrace:       "trace at line 42 with token [REDACTED:token]",
		ExceptionType:    "Err[REDACTED:secret]",
		Message:          `{"level":"error","msg":"error occurred"}`,
	}
	if err := d.Ingest(r); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	d.queue.Drain(ctx)

	got := rec.snapshot()
	if len(got) < 3 {
		t.Fatalf("expected at least 3 redaction marker alerts, got %d", len(got))
	}
	kinds := make(map[string]bool)
	for _, n := range got {
		kinds[fieldValue(n.n, "Marker Kind")] = true
	}
	for _, expected := range []string{"password", "token", "secret"} {
		if !kinds[expected] {
			t.Errorf("missing expected marker kind %q in alerts: %v", expected, got)
		}
	}
}

func TestQuarantineAlertRedactsSecrets(t *testing.T) {
	rec := &recorder{}
	notifySrv := httptest.NewServer(http.HandlerFunc(rec.handle))
	defer notifySrv.Close()

	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	clk := newFakeClock(now)

	d, err := New(Config{
		VLogs:     &vlogs.Client{BaseURL: "http://localhost:9428"},
		Notify:    &notify.Client{BaseURL: notifySrv.URL},
		StatePath: filepath.Join(t.TempDir(), "detector.json"),
		Now:       clk.Now,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	q := vlogs.QuarantineRecord{
		Time:      now,
		Project:   "proj-pn",
		Container: "backend",
		Message:   "raw bad line with Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.e30.t-IDcSemACt8x4iTMCda8Yhe3iZaWbvV5XKSTbuAn0M",
	}
	if err := d.IngestQuarantine(q); err != nil {
		t.Fatalf("IngestQuarantine: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	d.queue.Drain(ctx)

	got := rec.snapshot()
	if len(got) != 1 {
		t.Fatalf("expected 1 quarantine alert, got %d", len(got))
	}
	if strings.Contains(got[0].n.Body, "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9") {
		t.Errorf("raw secret was leaked in quarantine alert body: %q", got[0].n.Body)
	}
	if !strings.Contains(got[0].n.Body, "[REDACTED]") {
		t.Errorf("expected redaction marker in quarantine alert body: %q", got[0].n.Body)
	}
}

type fakeNotifySender struct {
	mu      sync.Mutex
	sent    []notify.Notification
	failErr error
}

func (f *fakeNotifySender) Send(_ context.Context, _ string, n notify.Notification) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failErr != nil {
		return f.failErr
	}
	f.sent = append(f.sent, n)
	return nil
}

func (f *fakeNotifySender) snapshot() []notify.Notification {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]notify.Notification, len(f.sent))
	copy(out, f.sent)
	return out
}

func (f *fakeNotifySender) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failErr = err
}

type fakeLogSource struct {
	mu       sync.Mutex
	readyErr error
	runFn    func(ctx context.Context, onRecord func(classify.Record) error, onQuarantine func(vlogs.QuarantineRecord) error) error
}

func (f *fakeLogSource) setReadyErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.readyErr = err
}

func (f *fakeLogSource) Ready(_ context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.readyErr
}

func (f *fakeLogSource) Run(ctx context.Context, onRecord func(classify.Record) error, onQuarantine func(vlogs.QuarantineRecord) error) error {
	if f.runFn != nil {
		return f.runFn(ctx, onRecord, onQuarantine)
	}
	<-ctx.Done()
	return ctx.Err()
}

func TestDisklessKafkaSourceSyncOutboxCrashesOutOfOrderAndDedup(t *testing.T) {
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	clk := newFakeClock(now)
	sender := &fakeNotifySender{}
	src := &fakeLogSource{readyErr: errors.New("connecting")}

	d, err := New(Config{
		Source:             src,
		Notify:             sender,
		StateDisabled:      true,
		SyncNotify:         true,
		QuarantineThrottle: 5 * time.Second,
		GrafanaBaseURL:     "http://10.200.0.11:3000",
		Group: group.Config{
			Window:     0,
			Cooldown:   10 * time.Minute,
			Retention:  20 * time.Minute,
			MaxSamples: 5,
			MaxEntries: 10000,
		},
		Now: clk.Now,
	})
	if err != nil {
		t.Fatalf("New diskless kafka detector: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for !d.started.Load() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	w := httptest.NewRecorder()
	d.Mux().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz before kafka ready = %d, want 503", w.Code)
	}

	src.setReadyErr(nil)
	w = httptest.NewRecorder()
	d.Mux().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("readyz after kafka ready = %d, want 200", w.Code)
	}

	if err := d.Ingest(classify.Record{
		Time:      now,
		Project:   "proj-pn",
		Container: "backend",
		Pod:       "pn-1",
		Parsed:    true,
		Level:     "error",
		Msg:       "db pool exhausted for id 1234",
		Message:   "db pool exhausted for id 1234",
	}); err != nil {
		t.Fatalf("Ingest first: %v", err)
	}
	if len(sender.snapshot()) != 1 {
		t.Fatalf("expected immediate sync delivery, got %d", len(sender.snapshot()))
	}

	if err := d.Ingest(classify.Record{
		Time:      now.Add(-2 * time.Minute),
		Project:   "proj-pn",
		Container: "backend",
		Pod:       "pn-1",
		Parsed:    true,
		Level:     "error",
		Msg:       "delayed out-of-order failure",
		Message:   "delayed out-of-order failure",
	}); err != nil {
		t.Fatalf("Ingest delayed: %v", err)
	}
	if len(sender.snapshot()) != 2 {
		t.Fatalf("out-of-order delayed error did not alert, got %d", len(sender.snapshot()))
	}

	clk.Set(now.Add(5 * time.Minute))
	if err := d.Ingest(classify.Record{
		Time:      now.Add(5 * time.Minute),
		Project:   "proj-pn",
		Container: "backend",
		Pod:       "pn-1",
		Parsed:    true,
		Level:     "error",
		Msg:       "db pool exhausted for id 9999",
		Message:   "db pool exhausted for id 9999",
	}); err != nil {
		t.Fatalf("Ingest duplicate: %v", err)
	}
	if len(sender.snapshot()) != 2 {
		t.Fatalf("duplicate inside 10m ttl alerted, got %d", len(sender.snapshot()))
	}

	clk.Set(now.Add(10 * time.Minute))
	if err := d.Ingest(classify.Record{
		Time:      now.Add(10 * time.Minute),
		Project:   "proj-pn",
		Container: "backend",
		Pod:       "pn-1",
		Parsed:    true,
		Level:     "error",
		Msg:       "db pool exhausted for id 4242",
		Message:   "db pool exhausted for id 4242",
	}); err != nil {
		t.Fatalf("Ingest after ttl: %v", err)
	}
	if len(sender.snapshot()) != 3 {
		t.Fatalf("error at exact 10m ttl did not re-alert, got %d", len(sender.snapshot()))
	}

	if err := d.Ingest(classify.Record{
		Time:      now.Add(10 * time.Minute),
		Project:   "proj-pn",
		Container: "frontend",
		Pod:       "fe-1",
		Parsed:    false,
		Message:   "2026/11/21 12:10:00 [error] 10#0: *1 upstream refused connection",
	}); err != nil {
		t.Fatalf("Ingest nginx: %v", err)
	}
	if len(sender.snapshot()) != 4 {
		t.Fatalf("raw nginx error did not alert, got %d", len(sender.snapshot()))
	}

	if err := d.Ingest(classify.Record{
		Time:      now.Add(10 * time.Minute),
		Project:   "proj-pn",
		Container: "worker",
		Pod:       "w-1",
		Parsed:    false,
		Message:   "panic: runtime error: nil pointer token=secret123",
	}); err != nil {
		t.Fatalf("Ingest crash: %v", err)
	}
	clk.Set(now.Add(10*time.Minute + 15*time.Second))
	d.tick()

	if err := d.IngestQuarantine(vlogs.QuarantineRecord{
		Time:      now.Add(10*time.Minute + 15*time.Second),
		Project:   "proj-health",
		Container: "api",
		Message:   "broken non-json line password=hidden",
	}); err != nil {
		t.Fatalf("IngestQuarantine: %v", err)
	}

	sent := sender.snapshot()
	if len(sent) != 6 {
		t.Fatalf("total alerts = %d, want 6", len(sent))
	}
	crashAlert := sent[4]
	if strings.Contains(crashAlert.Body, "secret123") || !strings.Contains(crashAlert.Body, "panic:") {
		t.Errorf("crash alert body = %q", crashAlert.Body)
	}
	quarantineAlert := sent[5]
	if strings.Contains(quarantineAlert.Body, "hidden") || strings.Contains(quarantineAlert.Body, "tenant 0:1") {
		t.Errorf("quarantine alert body = %q", quarantineAlert.Body)
	}
	hasGrafana := false
	for _, f := range sent[0].Fields {
		if f.Name == "Grafana" && strings.HasPrefix(f.Value, "http://10.200.0.11:3000/explore?") {
			hasGrafana = true
		}
	}
	if !hasGrafana {
		t.Errorf("alert missing Grafana Explore field: %+v", sent[0].Fields)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run shutdown err = %v", err)
	}
}

func TestSyncNotifyRetriesWithoutLeakingBurstQuotaAndShutdownFlushesRecentCrash(t *testing.T) {
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	clk := newFakeClock(now)

	var attempts atomic.Int32
	var sent []notify.Notification
	var mu sync.Mutex
	flakySender := &flakyNotifySender{
		sendFn: func(_ context.Context, _ string, n notify.Notification) error {
			if attempts.Add(1) <= 15 {
				return errors.New("transient kafka outbox outage")
			}
			mu.Lock()
			sent = append(sent, n)
			mu.Unlock()
			return nil
		},
	}

	var committed atomic.Int32
	src := &commitTrackSource{
		onCommitPending: func() { committed.Add(1) },
	}

	d, err := New(Config{
		Source:         src,
		Notify:         flakySender,
		StateDisabled:  true,
		SyncNotify:     true,
		SyncRetryMin:   time.Millisecond,
		SyncRetryMax:   2 * time.Millisecond,
		GrafanaBaseURL: "http://10.200.0.11:3000",
		Group:          group.Config{Window: 0, Cooldown: 10 * time.Minute, Retention: 20 * time.Minute, MaxSamples: 5, MaxEntries: 10000},
		Now:            clk.Now,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	delayedAt := now.Add(-2 * time.Hour)
	rec := classify.Record{
		Time:      delayedAt,
		Project:   "proj-pn",
		Container: "backend",
		Pod:       "pn-1",
		Parsed:    true,
		Level:     "error",
		Msg:       "transient outbox failure beyond burst cap",
		Message:   "transient outbox failure beyond burst cap",
	}
	if err := d.Ingest(rec); err != nil {
		t.Fatalf("Ingest failed after retries: %v", err)
	}
	if attempts.Load() != 16 {
		t.Fatalf("attempts = %d, want 16 (15 failures > burstPerMinute=10 + 1 success)", attempts.Load())
	}
	mu.Lock()
	if len(sent) != 1 {
		t.Fatalf("sent = %d, want 1", len(sent))
	}
	wantFrom := delayedAt.Add(-15 * time.Minute).UnixMilli()
	var grafanaURL string
	for _, f := range sent[0].Fields {
		if f.Name == "Grafana" {
			grafanaURL = f.Value
		}
	}
	mu.Unlock()
	if !strings.Contains(grafanaURL, fmt.Sprintf("%d", wantFrom)) {
		t.Errorf("Explore URL %q did not center on delayed log observed_time (%d)", grafanaURL, wantFrom)
	}

	if err := d.Ingest(classify.Record{
		Time:      now,
		Project:   "proj-pn",
		Container: "worker",
		Pod:       "w-1",
		Parsed:    false,
		Message:   "panic: recent crash right before shutdown",
	}); err != nil {
		t.Fatalf("Ingest recent crash: %v", err)
	}
	if d.CanCommit() {
		t.Fatal("CanCommit must be false while crash is buffered in Assembler")
	}

	if err := d.shutdown(); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	mu.Lock()
	totalSent := len(sent)
	mu.Unlock()
	if totalSent != 2 {
		t.Fatalf("shutdown did not flush recent crash (<2s old), totalSent = %d, want 2", totalSent)
	}
	if committed.Load() != 1 {
		t.Fatalf("shutdown did not commit pending offsets after FlushAll, committed = %d", committed.Load())
	}

	if _, err := New(Config{
		Source:         &fakeLogSource{},
		Notify:         &fakeNotifySender{},
		StateDisabled:  true,
		GrafanaBaseURL: "http://admin:secret@10.200.0.11:3000",
	}); err == nil {
		t.Fatal("New accepted GrafanaBaseURL with embedded credentials")
	}
}

type flakyNotifySender struct {
	sendFn func(ctx context.Context, channel string, n notify.Notification) error
}

func (f *flakyNotifySender) Send(ctx context.Context, channel string, n notify.Notification) error {
	return f.sendFn(ctx, channel, n)
}

type commitTrackSource struct {
	fakeLogSource
	canCommit       func() bool
	onCommitPending func()
}

func (c *commitTrackSource) SetCanCommit(fn func() bool) { c.canCommit = fn }

func (c *commitTrackSource) CommitPending(_ context.Context) error {
	if c.onCommitPending != nil {
		c.onCommitPending()
	}
	return nil
}
