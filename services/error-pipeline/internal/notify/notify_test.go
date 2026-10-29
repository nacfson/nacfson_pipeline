package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"nacfson_pipeline/services/error-pipeline/internal/classify"
	"nacfson_pipeline/services/error-pipeline/internal/group"
)

// The golden payload of Task 2 is the contract these structs must speak, so it
// is decoded, checked field by field, and re-encoded.
func TestGoldenMatchesStruct(t *testing.T) {
	raw, err := os.ReadFile("testdata/notification.json")
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	var n Notification
	if err := json.Unmarshal(raw, &n); err != nil {
		t.Fatalf("unmarshal golden: %v", err)
	}
	if n.Title != "New error in proj-pn/pn-backend" {
		t.Errorf("title = %q", n.Title)
	}
	if n.Severity != "error" {
		t.Errorf("severity = %q", n.Severity)
	}
	wantFields := []Field{
		{Name: "Project", Value: "proj-pn"},
		{Name: "Container", Value: "pn-backend"},
		{Name: "Pods", Value: "pn-backend-6f7c9d8b5-x2k4q"},
		{Name: "First seen", Value: "2026-10-29T03:14:07Z"},
		{Name: "Fingerprint", Value: "9b1c4e7a0d3f"},
	}
	if !reflect.DeepEqual(n.Fields, wantFields) {
		t.Errorf("fields = %+v, want %+v", n.Fields, wantFields)
	}
	wantBody := "```\njava.lang.IllegalStateException: connection pool exhausted\n" +
		"\tat com.pn.db.Pool.acquire(Pool.java:88)\n" +
		"\tat com.pn.api.OrderController.list(OrderController.java:41)\n```"
	if n.Body != wantBody {
		t.Errorf("body = %q, want %q", n.Body, wantBody)
	}
	if n.Attachment != nil {
		t.Errorf("attachment = %+v, want nil", n.Attachment)
	}

	// Re-encoding and comparing as generic JSON catches a renamed, missing or
	// extra key even when the decode above happened to succeed.
	b, err := json.Marshal(n)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var want, got any
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatalf("unmarshal golden as any: %v", err)
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal re-encoded as any: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip changed the payload:\n got %v\nwant %v", got, want)
	}
}

func TestClientSend(t *testing.T) {
	var method, path, ctype string
	var got Notification
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path, ctype = r.Method, r.URL.Path, r.Header.Get("Content-Type")
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL}
	var want Notification
	want.Title = "New error in proj-pn/pn-backend"
	want.Severity = "error"
	want.Fields = []Field{{Name: "Project", Value: "proj-pn"}}
	want.Body = "boom"
	if err := c.Send(context.Background(), "errors", want); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if method != http.MethodPost || path != "/v1/notify/errors" {
		t.Errorf("request = %s %s", method, path)
	}
	if ctype != "application/json" {
		t.Errorf("content-type = %q", ctype)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("body = %+v, want %+v", got, want)
	}
}

func TestClientSendRejectsNon202(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no grant for channel", http.StatusForbidden)
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL}
	err := c.Send(context.Background(), "errors", Notification{Title: "t", Severity: "error"})
	if err == nil {
		t.Fatal("Send accepted a 403")
	}
	for _, want := range []string{"403", "no grant for channel"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// sampleGroup is the PN connection-pool error of the golden payload.
func sampleGroup() group.Group {
	var ev classify.Event
	ev.Level = "error"
	ev.ExceptionType = "java.lang.IllegalStateException"
	ev.ExceptionMessage = "connection pool exhausted"
	ev.Stacktrace = "at com.pn.db.Pool.acquire(Pool.java:88)\n" +
		"at com.pn.api.OrderController.list(OrderController.java:41)"

	var g group.Group
	g.Fingerprint = "9b1c4e7a0d3f"
	g.Project = "proj-pn"
	g.Container = "pn-backend"
	g.Event = ev
	g.FirstSeen = time.Date(2026, 10, 29, 3, 14, 7, 0, time.UTC)
	g.Pods = []string{"pn-backend-6f7c9d8b5-x2k4q"}
	g.Samples = []string{`{"level":"error","msg":"connection pool exhausted"}`}
	return g
}

func TestRenderNew(t *testing.T) {
	n := RenderNew(sampleGroup())

	if n.Title != "New error in proj-pn/pn-backend" {
		t.Errorf("title = %q", n.Title)
	}
	if n.Severity != "error" {
		t.Errorf("severity = %q", n.Severity)
	}
	wantFields := []Field{
		{Name: "Project", Value: "proj-pn"},
		{Name: "Container", Value: "pn-backend"},
		{Name: "Pods", Value: "pn-backend-6f7c9d8b5-x2k4q"},
		{Name: "First seen", Value: "2026-10-29T03:14:07Z"},
		{Name: "Level", Value: "error"},
		{Name: "Fingerprint", Value: "9b1c4e7a0d3f"},
	}
	if !reflect.DeepEqual(n.Fields, wantFields) {
		t.Errorf("fields = %+v, want %+v", n.Fields, wantFields)
	}
	// A rendered group has the shape of the golden body: one fenced block with
	// the exception line, then the samples, then the stack.
	wantBody := "```\n" +
		"java.lang.IllegalStateException: connection pool exhausted\n" +
		`{"level":"error","msg":"connection pool exhausted"}` + "\n" +
		"at com.pn.db.Pool.acquire(Pool.java:88)\n" +
		"at com.pn.api.OrderController.list(OrderController.java:41)\n" +
		"```"
	if n.Body != wantBody {
		t.Errorf("body = %q, want %q", n.Body, wantBody)
	}
	if strings.Contains(n.Body, "occurrences since last alert") {
		t.Errorf("a first alert mentions a recurrence: %q", n.Body)
	}
	if n.Attachment != nil {
		t.Errorf("attachment = %+v, want nil", n.Attachment)
	}
}

func TestRenderNewRedacts(t *testing.T) {
	g := sampleGroup()
	g.Samples = []string{`{"level":"error","msg":"upstream rejected","authorization":"Bearer abc"}`}
	g.Event.ExceptionMessage = "login failed: password=hunter2"
	n := RenderNew(g)

	if !strings.Contains(n.Body, "[REDACTED]") {
		t.Fatalf("body was not redacted: %q", n.Body)
	}
	for _, secret := range []string{"abc", "hunter2"} {
		if strings.Contains(n.Body, secret) {
			t.Errorf("body still carries %q: %q", secret, n.Body)
		}
	}
}

func TestRenderFatalIsFatal(t *testing.T) {
	g := sampleGroup()
	g.Event.Level = "fatal"
	if got := RenderNew(g).Severity; got != SeverityFatal {
		t.Errorf("severity = %q, want %q", got, SeverityFatal)
	}
}

func TestRenderRecurring(t *testing.T) {
	g := sampleGroup()
	g.SinceLastAlert = 3
	n := RenderNew(g)

	if n.Title != "Recurring error in proj-pn/pn-backend" {
		t.Errorf("title = %q", n.Title)
	}
	if !strings.Contains(n.Body, "3 occurrences since last alert") {
		t.Errorf("body = %q", n.Body)
	}
	// The recurrence note is outside the fence, so it stays readable as text.
	if !strings.HasPrefix(n.Body, "Recurred; 3 occurrences since last alert.\n\n```\n") {
		t.Errorf("body = %q", n.Body)
	}
}

func TestRenderNewActionRecurrenceWithoutCount(t *testing.T) {
	// A cooldown that expired with no occurrence during it yields Recurred
	// with SinceLastAlert still zero; it must not read as a first alert.
	g := sampleGroup()
	g.SinceLastAlert = 0
	n := RenderNewAction(group.Action{Type: group.NotifyNew, Group: g, Recurred: true})

	if n.Title != "Recurring error in proj-pn/pn-backend" {
		t.Errorf("title = %q, want the recurring title", n.Title)
	}
	if strings.Contains(n.Body, "occurrences since last alert") {
		t.Errorf("body = %q, want no count clause when the count is zero", n.Body)
	}
	if !strings.HasPrefix(n.Body, "```\n") {
		t.Errorf("body = %q, want the log block to start the body", n.Body)
	}
}

func TestRenderNewActionRecurrenceWithCount(t *testing.T) {
	g := sampleGroup()
	g.SinceLastAlert = 2
	n := RenderNewAction(group.Action{Type: group.NotifyNew, Group: g, Recurred: true})

	if n.Title != "Recurring error in proj-pn/pn-backend" {
		t.Errorf("title = %q, want the recurring title", n.Title)
	}
	want := "Recurred; 2 occurrences since last alert.\n\n```\n"
	if !strings.HasPrefix(n.Body, want) {
		t.Errorf("body = %q, want prefix %q", n.Body, want)
	}
}

func TestRenderNewActionFirstAlertIgnoresCount(t *testing.T) {
	// Without Recurred the tracker is starting a cycle, so even a leftover
	// count (defensive) must not turn the message into a recurrence.
	g := sampleGroup()
	g.SinceLastAlert = 0
	n := RenderNewAction(group.Action{Type: group.NotifyNew, Group: g})

	if n.Title != "New error in proj-pn/pn-backend" {
		t.Errorf("title = %q, want the first-alert title", n.Title)
	}
	if strings.Contains(n.Body, "occurrences since last alert") {
		t.Errorf("body = %q, want no count clause", n.Body)
	}
}

// RenderNew must keep deciding from the group alone, as it did before
// RenderNewAction existed.
func TestRenderNewDelegatesToAction(t *testing.T) {
	g := sampleGroup()
	got := RenderNew(g)
	want := RenderNewAction(group.Action{Type: group.NotifyNew, Group: g, Recurred: g.SinceLastAlert > 0})
	if !reflect.DeepEqual(got, want) {
		t.Errorf("RenderNew = %+v, want %+v", got, want)
	}

	g.SinceLastAlert = 4
	got = RenderNew(g)
	want = RenderNewAction(group.Action{Type: group.NotifyNew, Group: g, Recurred: true})
	if !reflect.DeepEqual(got, want) {
		t.Errorf("RenderNew with a count = %+v, want %+v", got, want)
	}
}

// A fingerprint can be maxFieldBytes long (classify caps it there), and the
// proxy rejects a field value over 1024 runes with a permanent 400.
func TestRenderTruncatesFingerprint(t *testing.T) {
	g := sampleGroup()
	g.Fingerprint = strings.Repeat("fp", 32<<10) // 64 KiB
	n := RenderNew(g)

	got := fieldValue(t, n, "Fingerprint")
	if c := utf8.RuneCountInString(got); c > 1024 {
		t.Errorf("Fingerprint has %d runes, want <= 1024", c)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("Fingerprint %q does not signal truncation", tail(got, 8))
	}
	if !strings.HasPrefix(got, "fpfpfp") {
		t.Errorf("Fingerprint %q lost its start", got)
	}
	assertProxyLimits(t, n)
}

// The tracker does not cap Pods, so a wide incident produces a value far over
// the proxy's limit.
func TestRenderTruncatesPods(t *testing.T) {
	g := sampleGroup()
	g.Pods = make([]string, 0, 40)
	for i := range 40 {
		g.Pods = append(g.Pods, fmt.Sprintf("pn-backend-6f7c9d8b5-pod%02d", i))
	}
	n := RenderNew(g)

	got := fieldValue(t, n, "Pods")
	if c := utf8.RuneCountInString(got); c > 1024 {
		t.Errorf("Pods has %d runes, want <= 1024", c)
	}
	if !strings.HasPrefix(got, "pn-backend-6f7c9d8b5-pod00, ") {
		t.Errorf("Pods %q lost its start", head(got, 48))
	}
	assertProxyLimits(t, n)
}

func TestRenderTruncatesLongTitle(t *testing.T) {
	g := sampleGroup()
	g.Project = strings.Repeat("p", 300)
	g.Container = strings.Repeat("c", 300)
	n := RenderNew(g)

	if c := utf8.RuneCountInString(n.Title); c > 256 {
		t.Errorf("title has %d runes, want <= 256", c)
	}
	if !strings.HasPrefix(n.Title, "New error in ppp") {
		t.Errorf("title = %q", n.Title)
	}
	if !strings.HasSuffix(n.Title, "…") {
		t.Errorf("title = %q, want a truncation marker", n.Title)
	}
	assertProxyLimits(t, n)

	// The investigator notice builds its own title and needs the same cap.
	g.Project, g.Container = strings.Repeat("é", 200), strings.Repeat("é", 200)
	inv := RenderInvestigatorUnavailable(g)
	if c := utf8.RuneCountInString(inv.Title); c > 256 {
		t.Errorf("investigator title has %d runes, want <= 256", c)
	}
	if !utf8.ValidString(inv.Title) {
		t.Errorf("investigator title split a rune: %q", inv.Title)
	}
	assertProxyLimits(t, inv)

	// Multi-byte runes in a field value must survive truncation intact.
	g.Fingerprint = strings.Repeat("é", 2000)
	n = RenderNew(g)
	fp := fieldValue(t, n, "Fingerprint")
	if c := utf8.RuneCountInString(fp); c > 1024 {
		t.Errorf("Fingerprint has %d runes, want <= 1024", c)
	}
	if !utf8.ValidString(fp) {
		t.Errorf("Fingerprint split a rune: %q", fp)
	}
}

// A secret must not survive truncation as a readable prefix of the value.
func TestRenderTruncatesAfterRedacting(t *testing.T) {
	g := sampleGroup()
	g.Fingerprint = "authorization=Bearer " + strings.Repeat("s", 4000)
	n := RenderNew(g)

	fp := fieldValue(t, n, "Fingerprint")
	if strings.Contains(fp, strings.Repeat("s", 20)) {
		t.Errorf("Fingerprint kept the secret: %q", fp)
	}
	if !strings.Contains(fp, "[REDACTED]") {
		t.Errorf("Fingerprint %q was not redacted", fp)
	}
	if c := utf8.RuneCountInString(fp); c > 1024 {
		t.Errorf("Fingerprint has %d runes, want <= 1024", c)
	}
}

// fieldValue returns the value of the named field.
func fieldValue(t *testing.T, n Notification, name string) string {
	t.Helper()
	for _, f := range n.Fields {
		if f.Name == name {
			return f.Value
		}
	}
	t.Fatalf("no %q field in %+v", name, n.Fields)
	return ""
}

// head returns at most the first n runes of s, so a failure message stays
// readable without slicing a string in the middle of a rune.
func head(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// tail returns at most the last n runes of s.
func tail(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[len(r)-n:])
	}
	return s
}

// assertProxyLimits checks a rendered notification against the notify proxy's
// Validate limits, so a new render path cannot quietly break them.
func assertProxyLimits(t *testing.T, n Notification) {
	t.Helper()
	if c := utf8.RuneCountInString(n.Title); c < 1 || c > 256 {
		t.Errorf("title has %d runes, want 1-256", c)
	}
	if len(n.Fields) > 25 {
		t.Errorf("%d fields, want <= 25", len(n.Fields))
	}
	for _, f := range n.Fields {
		if c := utf8.RuneCountInString(f.Value); c > 1024 {
			t.Errorf("field %q has %d runes, want <= 1024", f.Name, c)
		}
	}
}

func TestRenderOverflow(t *testing.T) {
	since := time.Date(2026, 10, 29, 3, 14, 7, 0, time.UTC)
	n := RenderOverflow(12, since)

	if n.Title != "+12 more new errors" {
		t.Errorf("title = %q", n.Title)
	}
	if n.Severity != SeverityInfo {
		t.Errorf("severity = %q", n.Severity)
	}
	for _, want := range []string{"12", "2026-10-29T03:14:07Z"} {
		if !strings.Contains(n.Body, want) {
			t.Errorf("body %q does not mention %q", n.Body, want)
		}
	}
	if n.Attachment != nil {
		t.Errorf("attachment = %+v, want nil", n.Attachment)
	}
}

func TestRenderInvestigatorUnavailable(t *testing.T) {
	n := RenderInvestigatorUnavailable(sampleGroup())

	if n.Title != "AI debug skipped in proj-pn/pn-backend" {
		t.Errorf("title = %q", n.Title)
	}
	if !strings.Contains(n.Body, "AI debug skipped: investigator unavailable") {
		t.Errorf("body = %q", n.Body)
	}
	wantFields := []Field{
		{Name: "Project", Value: "proj-pn"},
		{Name: "Container", Value: "pn-backend"},
		{Name: "Fingerprint", Value: "9b1c4e7a0d3f"},
	}
	if !reflect.DeepEqual(n.Fields, wantFields) {
		t.Errorf("fields = %+v, want %+v", n.Fields, wantFields)
	}
}

// TestRenderInvestigatorUnavailableBatchStaysWithinProxyLimits proves the one
// notice that stands in for a large batch is still acceptable to the notify
// proxy. The batch is the worst case a shutdown can produce: 133 groups whose
// fingerprints are project-supplied, so they can be far longer than a derived
// one. An over-long title or field is a permanent 400 for the message at the
// head of the retry queue, which would block every notification behind it, so
// the caps have to hold without losing the count.
func TestRenderInvestigatorUnavailableBatchStaysWithinProxyLimits(t *testing.T) {
	batch := make([]group.Group, 133)
	for i := range batch {
		batch[i] = group.Group{Fingerprint: strings.Repeat("a", 4096) + fmt.Sprint(i)}
	}
	n := RenderInvestigatorUnavailableBatch(batch)

	if n.Title != "AI debug skipped for 133 groups" {
		t.Errorf("title = %q", n.Title)
	}
	if n.Severity != SeverityInfo {
		t.Errorf("severity = %q", n.Severity)
	}
	if got := utf8.RuneCountInString(n.Title); got > maxTitleRunes {
		t.Errorf("title is %d runes, limit %d", got, maxTitleRunes)
	}
	for _, f := range n.Fields {
		if got := utf8.RuneCountInString(f.Value); got > maxFieldRunes {
			t.Errorf("field %q is %d runes, limit %d", f.Name, got, maxFieldRunes)
		}
	}
	// The count must survive the clamp: a truncated name list that no longer
	// says how many groups are missing reads as the whole truth.
	if !strings.Contains(n.Body, "133") {
		t.Errorf("body %q lost the group count", n.Body)
	}
	if n.Attachment != nil {
		t.Errorf("attachment = %+v, want nil", n.Attachment)
	}
}

func TestLimiterTenPerMinute(t *testing.T) {
	t0 := time.Date(2026, 10, 29, 9, 0, 0, 0, time.UTC)
	l := NewLimiter(10)

	allowed := 0
	for range 11 {
		if l.Allow(t0) {
			allowed++
		}
	}
	if allowed != 10 {
		t.Errorf("allowed %d of 11 at t0, want 10", allowed)
	}
	if got := l.TakeOverflow(); got != 1 {
		t.Errorf("TakeOverflow = %d, want 1", got)
	}
	if got := l.TakeOverflow(); got != 0 {
		t.Errorf("TakeOverflow did not reset: %d", got)
	}
	if !l.Allow(t0.Add(61 * time.Second)) {
		t.Error("the next minute was still capped")
	}
}

func TestQueueDropsOldestAndLogs(t *testing.T) {
	var sent []string
	send := func(_ context.Context, n Notification) error {
		sent = append(sent, n.Title)
		return nil
	}
	var logs []string
	logf := func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}
	q := NewQueue(100, send, logf)

	for i := range 101 {
		q.Push(Notification{Title: fmt.Sprintf("t%d", i), Severity: "error"})
	}
	if got := q.Len(); got != 100 {
		t.Fatalf("Len = %d, want 100", got)
	}
	if len(logs) != 1 {
		t.Fatalf("log lines = %q, want exactly one drop line", logs)
	}
	if !strings.Contains(logs[0], "t0") {
		t.Errorf("drop line %q does not name the dropped title", logs[0])
	}

	q.Drain(context.Background())
	want := make([]string, 0, 100)
	for i := 1; i <= 100; i++ {
		want = append(want, fmt.Sprintf("t%d", i))
	}
	if !reflect.DeepEqual(sent, want) {
		t.Fatalf("drained %q, want t1..t100 in order", sent)
	}
	if got := q.Len(); got != 0 {
		t.Errorf("Len = %d after a clean drain", got)
	}
}

func TestDrainStopsAtFirstFailure(t *testing.T) {
	var sent []string
	failing := true
	send := func(_ context.Context, n Notification) error {
		sent = append(sent, n.Title)
		if failing && n.Title == "b" {
			return errors.New("proxy down")
		}
		return nil
	}
	q := NewQueue(10, send, func(string, ...any) {})

	for _, title := range []string{"a", "b", "c"} {
		q.Push(Notification{Title: title, Severity: "error"})
	}
	q.Drain(context.Background())

	if want := []string{"a", "b"}; !reflect.DeepEqual(sent, want) {
		t.Fatalf("sent %q, want %q: a drain must stop at the first failure", sent, want)
	}
	if got := q.Len(); got != 2 {
		t.Fatalf("Len = %d, want 2: the failed message and the one behind it", got)
	}

	failing = false
	q.Drain(context.Background())
	if want := []string{"a", "b", "b", "c"}; !reflect.DeepEqual(sent, want) {
		t.Errorf("retry sent %q, want %q", sent, want)
	}
	if got := q.Len(); got != 0 {
		t.Errorf("Len = %d after a successful drain, want 0", got)
	}
}

func TestQueueConcurrentPushIsSafe(t *testing.T) {
	q := NewQueue(100, func(context.Context, Notification) error { return nil }, func(string, ...any) {})
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := range 50 {
				q.Push(Notification{Title: fmt.Sprintf("g%d-i%d", g, i), Severity: "error"})
			}
		}(g)
	}
	wg.Wait()
	if got := q.Len(); got != 100 {
		t.Errorf("Len = %d after 400 pushes into a queue of 100, want 100", got)
	}
}

// Push must not report a drop for the head an in-flight send already copied:
// if that send fails, the notification is still queued and will be retried.
func TestPushDoesNotLogDropForInflightHead(t *testing.T) {
	sent := make(chan struct{})
	release := make(chan struct{})
	var sentTitles []string
	var attempts int
	var mu sync.Mutex
	send := func(_ context.Context, n Notification) error {
		mu.Lock()
		sentTitles = append(sentTitles, n.Title)
		first := n.Title == "t1" && attempts == 0
		if n.Title == "t1" {
			attempts++
		}
		mu.Unlock()
		if first {
			close(sent)
			<-release
			return errors.New("proxy down")
		}
		return nil
	}
	var logs []string
	var logMu sync.Mutex
	logf := func(format string, args ...any) {
		logMu.Lock()
		logs = append(logs, fmt.Sprintf(format, args...))
		logMu.Unlock()
	}
	q := NewQueue(2, send, logf)
	q.Push(Notification{Title: "t1", Severity: "error"})
	q.Push(Notification{Title: "t2", Severity: "error"})

	done := make(chan struct{})
	go func() {
		defer close(done)
		q.Drain(context.Background())
	}()
	<-sent // t1 is copied for the send and still queued.

	// The queue is full at 2. Dropping t1 here would silently lose it when the
	// send fails, so t2 must be the one dropped.
	q.Push(Notification{Title: "t3", Severity: "error"})
	close(release)
	<-done

	logMu.Lock()
	var dropLines []string
	for _, l := range logs {
		if strings.Contains(l, "queue full, dropped") {
			dropLines = append(dropLines, l)
		}
	}
	logMu.Unlock()
	if len(dropLines) != 1 {
		t.Fatalf("drop lines = %q, want exactly one", dropLines)
	}
	if !strings.Contains(dropLines[0], `"t2"`) {
		t.Errorf("drop line %q does not name t2, the entry that really goes", dropLines[0])
	}
	if strings.Contains(dropLines[0], `"t1"`) {
		t.Errorf("drop line %q names the in-flight head", dropLines[0])
	}

	q.Drain(context.Background())
	mu.Lock()
	got := append([]string(nil), sentTitles...)
	mu.Unlock()
	// t1 failed once and was retried, so the delivered order is t1, t1, t3.
	if want := []string{"t1", "t1", "t3"}; !reflect.DeepEqual(got, want) {
		t.Errorf("sent %q, want %q: the failed head must stay queued", got, want)
	}
	if got := q.Len(); got != 0 {
		t.Errorf("Len = %d, want 0", got)
	}
}

// A shutdown drain that overlaps a ticker drain must run its own pass rather
// than return immediately and deliver nothing.
func TestDrainWaitsForRunningPass(t *testing.T) {
	sent := make(chan struct{})
	release := make(chan struct{})
	var sentTitles []string
	var mu sync.Mutex
	send := func(_ context.Context, n Notification) error {
		mu.Lock()
		sentTitles = append(sentTitles, n.Title)
		mu.Unlock()
		if n.Title == "first" {
			close(sent)
			<-release
		}
		return nil
	}
	q := NewQueue(10, send, func(string, ...any) {})
	q.Push(Notification{Title: "first", Severity: "error"})

	tickerDone := make(chan struct{})
	go func() {
		defer close(tickerDone)
		q.Drain(context.Background())
	}()
	<-sent

	// Arrive mid-pass; the queue is non-empty because the head is in flight.
	q.Push(Notification{Title: "second", Severity: "error"})
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		q.Drain(context.Background())
	}()

	close(release)
	<-tickerDone
	<-shutdownDone

	mu.Lock()
	got := append([]string(nil), sentTitles...)
	mu.Unlock()
	if want := []string{"first", "second"}; !reflect.DeepEqual(got, want) {
		t.Errorf("sent %q, want %q: the waiting drain must deliver the rest", got, want)
	}
	if got := q.Len(); got != 0 {
		t.Errorf("Len = %d, want 0", got)
	}
}

// A drain waiting on another pass must obey its context.
func TestDrainWaitingHonoursContext(t *testing.T) {
	sent := make(chan struct{})
	release := make(chan struct{})
	q := NewQueue(10, func(_ context.Context, n Notification) error {
		if n.Title == "first" {
			close(sent)
			<-release
		}
		return nil
	}, func(string, ...any) {})
	q.Push(Notification{Title: "first", Severity: "error"})
	q.Push(Notification{Title: "second", Severity: "error"})

	done := make(chan struct{})
	go func() {
		defer close(done)
		q.Drain(context.Background())
	}()
	<-sent

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	before := time.Now()
	q.Drain(ctx) // Both entry points block on the running pass.
	if waited := time.Since(before); waited > time.Second {
		t.Errorf("Drain waited %v, want an immediate return", waited)
	}

	close(release)
	<-done
}
