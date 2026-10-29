package group

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"nacfson_pipeline/services/error-pipeline/internal/classify"
)

var t0 = time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

func occ(pod string) classify.Classified {
	return classify.Classified{
		Record: classify.Record{
			Project:   "proj-blog",
			Container: "web",
			Pod:       pod,
			Message:   `{"level":"error","msg":"boom"}`,
		},
		Event: classify.Event{Level: "error", Msg: "boom", Source: classify.SourceJSON},
	}
}

func types(as []Action) []ActionType {
	var out []ActionType
	for _, a := range as {
		out = append(out, a.Type)
	}
	return out
}

func only(t *testing.T, tr *Tracker) Group {
	t.Helper()
	gs := tr.Export()
	if len(gs) != 1 {
		t.Fatalf("want 1 group, got %d", len(gs))
	}
	return gs[0]
}

func TestDefaultConfig(t *testing.T) {
	c := DefaultConfig()
	if c.Window != 3*time.Minute || c.Cooldown != 6*time.Hour || c.Retention != 168*time.Hour || c.MaxSamples != 20 {
		t.Fatalf("unexpected default config: %+v", c)
	}
}

func TestFirstOccurrenceNotifiesImmediately(t *testing.T) {
	tr := NewTracker(DefaultConfig())
	as := tr.Observe(occ("p1"), t0)
	if len(as) != 1 || as[0].Type != NotifyNew || as[0].Recurred {
		t.Fatalf("want one NotifyNew, got %+v", as)
	}
	g := as[0].Group
	if g.Count != 1 || g.Project != "proj-blog" || g.Container != "web" || g.Fingerprint == "" {
		t.Fatalf("bad group: %+v", g)
	}
	if !g.FirstSeen.Equal(t0) || !g.WindowEnd.Equal(t0.Add(3*time.Minute)) || g.Handed {
		t.Fatalf("bad times: %+v", g)
	}
	if g.Event.Msg != "boom" {
		t.Fatalf("event not recorded: %+v", g.Event)
	}
}

func TestRepeatsInsideWindowAreCountedNotNotified(t *testing.T) {
	tr := NewTracker(DefaultConfig())
	tr.Observe(occ("p1"), t0)
	for i := 0; i < 5; i++ {
		pod := "p1"
		if i%2 == 1 {
			pod = "p2"
		}
		if as := tr.Observe(occ(pod), t0.Add(time.Minute)); len(as) != 0 {
			t.Fatalf("unexpected actions: %+v", as)
		}
	}
	g := only(t, tr)
	if g.Count != 6 {
		t.Fatalf("count = %d, want 6", g.Count)
	}
	if !reflect.DeepEqual(g.Pods, []string{"p1", "p2"}) {
		t.Fatalf("pods = %v", g.Pods)
	}
	if len(g.Samples) != 6 {
		t.Fatalf("samples = %d", len(g.Samples))
	}
	if !g.LastSeen.Equal(t0.Add(time.Minute)) {
		t.Fatalf("last_seen = %v", g.LastSeen)
	}
}

func TestWindowIsFixedNotSliding(t *testing.T) {
	tr := NewTracker(DefaultConfig())
	n := 0
	for d := time.Duration(0); d < 3*time.Minute; d += 30 * time.Second {
		tr.Observe(occ("p1"), t0.Add(d))
		n++
		if as := tr.Tick(t0.Add(d)); len(as) != 0 {
			t.Fatalf("early handoff at %v: %+v", d, as)
		}
	}
	as := tr.Tick(t0.Add(3 * time.Minute))
	if len(as) != 1 || as[0].Type != Handoff {
		t.Fatalf("want one Handoff, got %+v", as)
	}
	if as[0].Group.Count != n {
		t.Fatalf("count = %d, want %d", as[0].Group.Count, n)
	}
	if !as[0].Group.Handed {
		t.Fatal("handoff group should be marked handed")
	}
	if as := tr.Tick(t0.Add(4 * time.Minute)); len(as) != 0 {
		t.Fatalf("second tick re-emitted: %+v", as)
	}
}

func TestSingleOccurrenceStillHandedOff(t *testing.T) {
	tr := NewTracker(DefaultConfig())
	tr.Observe(occ("p1"), t0)
	as := tr.Tick(t0.Add(3 * time.Minute))
	if len(as) != 1 || as[0].Type != Handoff || as[0].Group.Count != 1 {
		t.Fatalf("got %+v", as)
	}
	want := t0.Add(3*time.Minute + 6*time.Hour)
	if !as[0].Group.CooldownUntil.Equal(want) {
		t.Fatalf("cooldown_until = %v, want %v", as[0].Group.CooldownUntil, want)
	}
}

func handedTracker(t *testing.T) *Tracker {
	t.Helper()
	tr := NewTracker(DefaultConfig())
	tr.Observe(occ("p1"), t0)
	if as := tr.Tick(t0.Add(3 * time.Minute)); len(as) != 1 {
		t.Fatalf("setup handoff: %+v", as)
	}
	return tr
}

func TestCooldownSuppresses(t *testing.T) {
	tr := handedTracker(t)
	for _, d := range []time.Duration{time.Hour, 5 * time.Hour} {
		if as := tr.Observe(occ("p1"), t0.Add(d)); len(as) != 0 {
			t.Fatalf("actions during cooldown: %+v", as)
		}
	}
	g := only(t, tr)
	if g.SinceLastAlert != 2 || g.Count != 3 {
		t.Fatalf("since=%d count=%d", g.SinceLastAlert, g.Count)
	}
	if !g.LastSeen.Equal(t0.Add(5 * time.Hour)) {
		t.Fatalf("last_seen = %v", g.LastSeen)
	}
	if as := tr.Tick(t0.Add(5 * time.Hour)); len(as) != 0 {
		t.Fatalf("tick during cooldown: %+v", as)
	}
}

func TestRecurrenceAfterCooldown(t *testing.T) {
	tr := handedTracker(t)
	tr.Observe(occ("p1"), t0.Add(time.Hour))
	tr.Observe(occ("p1"), t0.Add(5*time.Hour))

	at := t0.Add(3*time.Minute + 6*time.Hour + time.Second)
	as := tr.Observe(occ("p9"), at)
	if len(as) != 1 || as[0].Type != NotifyNew || !as[0].Recurred {
		t.Fatalf("want recurred NotifyNew, got %+v", as)
	}
	if as[0].Group.SinceLastAlert != 2 {
		t.Fatalf("action since_last_alert = %d, want 2", as[0].Group.SinceLastAlert)
	}
	if a := as[0].Group; a.Count != 1 || !a.FirstSeen.Equal(at) || a.Handed || a.Event.Msg != "boom" {
		t.Fatalf("action group should be the new cycle: %+v", a)
	}
	g := only(t, tr)
	if g.Count != 1 || g.SinceLastAlert != 0 || g.Handed || !g.CooldownUntil.IsZero() {
		t.Fatalf("not reset: %+v", g)
	}
	if !g.FirstSeen.Equal(at) || !g.WindowEnd.Equal(at.Add(3*time.Minute)) {
		t.Fatalf("new window wrong: %+v", g)
	}
	if !reflect.DeepEqual(g.Pods, []string{"p9"}) || len(g.Samples) != 1 {
		t.Fatalf("pods/samples not reset: %+v", g)
	}
	// The new window hands off again.
	hs := tr.Tick(at.Add(3 * time.Minute))
	if len(hs) != 1 || hs[0].Type != Handoff || hs[0].Group.Count != 1 {
		t.Fatalf("second cycle handoff: %+v", hs)
	}
}

func TestObservePastWindowBeforeTickClosesFirst(t *testing.T) {
	tr := NewTracker(DefaultConfig())
	tr.Observe(occ("p1"), t0)
	as := tr.Observe(occ("p1"), t0.Add(10*time.Minute))
	if got := types(as); !reflect.DeepEqual(got, []ActionType{Handoff}) {
		t.Fatalf("got %v", got)
	}
	if as[0].Group.Count != 1 {
		t.Fatalf("handoff must not include the late occurrence: %+v", as[0].Group)
	}
	g := only(t, tr)
	if !g.Handed || g.Count != 2 || g.SinceLastAlert != 1 {
		t.Fatalf("state: %+v", g)
	}
}

func TestObservePastWindowAndCooldownBeforeTick(t *testing.T) {
	tr := NewTracker(DefaultConfig())
	tr.Observe(occ("p1"), t0)
	at := t0.Add(7 * time.Hour)
	as := tr.Observe(occ("p1"), at)
	if got := types(as); !reflect.DeepEqual(got, []ActionType{Handoff, NotifyNew}) {
		t.Fatalf("got %v", got)
	}
	if !as[1].Recurred || as[1].Group.SinceLastAlert != 0 {
		t.Fatalf("recurrence: %+v", as[1])
	}
}

func TestSamplesCappedAt20(t *testing.T) {
	tr := NewTracker(DefaultConfig())
	for i := 0; i < 30; i++ {
		c := occ("p1")
		c.Record.Message = strings.Repeat("x", i+1)
		tr.Observe(c, t0.Add(time.Duration(i)*time.Second))
	}
	g := only(t, tr)
	if len(g.Samples) != 20 || g.Count != 30 {
		t.Fatalf("samples=%d count=%d", len(g.Samples), g.Count)
	}
	if len(g.Samples[19]) != 20 {
		t.Fatalf("later samples should be dropped, last sample len %d", len(g.Samples[19]))
	}
}

func TestSampleTruncatedRuneSafe(t *testing.T) {
	tr := NewTracker(DefaultConfig())
	c := occ("p1")
	c.Record.Message = strings.Repeat("가", 5000) // 3 bytes each
	tr.Observe(c, t0)
	s := only(t, tr).Samples[0]
	if len(s) > 4096 || len(s) < 4096-3 {
		t.Fatalf("len = %d", len(s))
	}
	if strings.ContainsRune(s, '�') {
		t.Fatal("split a rune")
	}
}

func TestRetentionDropsGroupsAfter7Days(t *testing.T) {
	tr := handedTracker(t)
	if tr.Tick(t0.Add(7 * 24 * time.Hour)); len(tr.Export()) != 1 {
		t.Fatal("dropped too early")
	}
	tr.Tick(t0.Add(7*24*time.Hour + time.Second))
	if n := len(tr.Export()); n != 0 {
		t.Fatalf("groups left: %d", n)
	}
}

func TestRetentionNeverDropsOpenWindow(t *testing.T) {
	tr := NewTracker(Config{Window: time.Hour, Cooldown: time.Hour, Retention: time.Minute, MaxSamples: 20})
	tr.Observe(occ("p1"), t0)
	tr.Tick(t0.Add(30 * time.Minute)) // window (1h) still open, but idle longer than retention
	if len(tr.Export()) != 1 {
		t.Fatal("dropped a group mid-window")
	}
}

func TestDistinctFingerprintsAreSeparateGroups(t *testing.T) {
	tr := NewTracker(DefaultConfig())
	a := occ("p1")
	b := occ("p1")
	b.Event.Msg = "different failure"
	if as := tr.Observe(a, t0); len(as) != 1 {
		t.Fatal("a")
	}
	if as := tr.Observe(b, t0); len(as) != 1 {
		t.Fatal("b")
	}
	gs := tr.Export()
	if len(gs) != 2 || gs[0].Fingerprint >= gs[1].Fingerprint {
		t.Fatalf("export not sorted/complete: %+v", gs)
	}
	hs := tr.Tick(t0.Add(3 * time.Minute))
	if len(hs) != 2 || hs[0].Group.Fingerprint >= hs[1].Group.Fingerprint {
		t.Fatalf("handoffs not sorted: %+v", hs)
	}
}

func TestExportImportRoundTripAndIsolation(t *testing.T) {
	tr := NewTracker(DefaultConfig())
	tr.Observe(occ("p1"), t0)
	tr.Observe(occ("p2"), t0.Add(time.Second))
	ex := tr.Export()
	ex[0].Pods[0] = "mutated"
	ex[0].Samples[0] = "mutated"
	if g := only(t, tr); g.Pods[0] != "p1" || g.Samples[0] == "mutated" {
		t.Fatalf("export leaked state: %+v", g)
	}

	tr2 := NewTracker(DefaultConfig())
	tr2.Observe(classify.Classified{Record: classify.Record{Project: "x", Container: "y"}, Event: classify.Event{Msg: "old"}}, t0)
	tr2.Import(tr.Export())
	if !reflect.DeepEqual(tr2.Export(), tr.Export()) {
		t.Fatalf("round trip mismatch:\n%+v\n%+v", tr2.Export(), tr.Export())
	}
	// Imported state continues the lifecycle.
	if hs := tr2.Tick(t0.Add(3 * time.Minute)); len(hs) != 1 || hs[0].Type != Handoff {
		t.Fatalf("imported group did not hand off: %+v", hs)
	}
}

func TestActionGroupIsACopy(t *testing.T) {
	tr := NewTracker(DefaultConfig())
	as := tr.Observe(occ("p1"), t0)
	as[0].Group.Pods[0] = "mutated"
	as[0].Group.Samples[0] = "mutated"
	if g := only(t, tr); g.Pods[0] != "p1" || g.Samples[0] == "mutated" {
		t.Fatalf("action leaked state: %+v", g)
	}
}

func TestGroupJSONContract(t *testing.T) {
	tr := handedTracker(t)
	b, err := json.Marshal(only(t, tr))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"fingerprint", "project", "container", "event", "first_seen", "last_seen",
		"window_end", "cooldown_until", "count", "since_last_alert", "pods", "samples", "handed"} {
		if _, ok := m[k]; !ok {
			t.Errorf("missing key %q in %s", k, b)
		}
	}
	if len(m) != 13 {
		t.Errorf("unexpected keys: %s", b)
	}
}

func TestConcurrentObserveAndTick(t *testing.T) {
	tr := NewTracker(DefaultConfig())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 500; i++ {
			tr.Tick(t0.Add(time.Duration(i) * time.Second))
			tr.Export()
		}
	}()
	for i := 0; i < 500; i++ {
		tr.Observe(occ("p1"), t0.Add(time.Duration(i)*time.Second))
	}
	<-done
}
