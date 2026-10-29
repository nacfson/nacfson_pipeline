package classify

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestLine(t *testing.T) {
	cases := []struct {
		in     string
		level  string
		kind   LineKind
		source string
	}{
		{`{"time":"2026-10-29T09:00:00.000Z","level":"ERROR","msg":"x","exception.message":"boom"}`, "error", Error, "json"},
		{`{"time":"2026-10-29T09:00:00.000Z","level":"critical","msg":"x"}`, "fatal", Error, "json"},
		{`{"time":"2026-10-29T09:00:00.000Z","level":"fatal","msg":"x"}`, "fatal", Error, "json"},
		{`{"time":"2026-10-29T09:00:00.000Z","level":"warning","msg":"x"}`, "warn", NotError, "json"},
		{`{"time":"2026-10-29T09:00:00.000Z","level":"info","kind":"admin","msg":"x"}`, "info", NotError, "json"},
		{`{"time":"2026-10-29T09:00:00.000Z","level":"debug","msg":"x"}`, "debug", NotError, "json"},
		{`{"time":"2026-10-29T09:00:00.000Z","msg":"no level"}`, "", NotError, ""},
		{`{"time":"2026-10-29T09:00:00.000Z","level":"loud","msg":"x"}`, "", NotError, ""},
		{`{"time":"2026-10-29T09:00:00.000Z","level":5,"msg":"x"}`, "", NotError, ""},
		{`panic: runtime error: index out of range [5] with length 3`, "fatal", CrashStart, "crash"},
		{`fatal error: concurrent map writes`, "fatal", CrashStart, "crash"},
		{`Traceback (most recent call last):`, "fatal", CrashStart, "crash"},
		{`Unhandled rejection Error: nope`, "fatal", CrashStart, "crash"},
		{`Uncaught TypeError: x is undefined`, "fatal", CrashStart, "crash"},
		{`Exception in thread "main" java.lang.NullPointerException`, "fatal", CrashStart, "crash"},
		{`2026/10/29 09:00:00 [error] 12#12: *1 connect() failed`, "error", Error, "nginx"},
		{`2026/10/29 09:00:00 [crit] 12#12: something`, "fatal", Error, "nginx"},
		{`2026/10/29 09:00:00 [alert] 12#12: something`, "fatal", Error, "nginx"},
		{`2026/10/29 09:00:00 [emerg] 12#12: something`, "fatal", Error, "nginx"},
		{`2026/10/29 09:00:00 [notice] 1#1: start worker`, "", NotError, ""},
		{`2026/10/29 09:00:00 [warn] 1#1: low`, "", NotError, ""},
		{`2026/10/29 09:00:00 [info] 1#1: hi`, "", NotError, ""},
		{`2026/10/29 09:00:00 [debug] 1#1: hi`, "", NotError, ""},
		{`level=error msg=x`, "", Plain, ""}, // logfmt is not a contract format
		{`  panic: indented is not a signature`, "", Plain, ""},
		{`{broken json`, "", Plain, ""},
		{`{"level":"error","msg":"x"} trailing`, "", Plain, ""},
		{``, "", Plain, ""},
		{`goroutine 1 [running]:`, "", Plain, ""},
	}
	for _, c := range cases {
		ev, kind := Line(c.in)
		if ev.Level != c.level || kind != c.kind || ev.Source != c.source {
			t.Errorf("Line(%q) = level %q kind %v source %q; want %q %v %q",
				c.in, ev.Level, kind, ev.Source, c.level, c.kind, c.source)
		}
	}
}

func TestJSONFields(t *testing.T) {
	in := `{"time":"2026-10-29T09:00:00.000Z","level":"error","msg":"enrich failed","event.name":"enrich","trace_id":"4bf92f3577b34da6","exception.type":"TimeoutError","exception.message":"upstream timeout","exception.stacktrace":"a\nb","error.fingerprint":"fp1"}`
	ev, kind := Line(in)
	want := Event{
		Level: "error", Msg: "enrich failed", EventName: "enrich", TraceID: "4bf92f3577b34da6",
		ExceptionType: "TimeoutError", ExceptionMessage: "upstream timeout", Stacktrace: "a\nb",
		Fingerprint: "fp1", Source: "json",
	}
	if kind != Error || ev != want {
		t.Fatalf("got %+v (%v), want %+v", ev, kind, want)
	}
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]string
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"level", "msg", "event.name", "trace_id", "exception.type", "exception.message", "exception.stacktrace", "error.fingerprint", "source"} {
		if m[k] == "" {
			t.Errorf("marshalled Event is missing contract key %q: %s", k, b)
		}
	}
}

func TestLegacyKindReadAsEventName(t *testing.T) {
	ev, _ := Line(`{"level":"info","kind":"admin","msg":"x"}`)
	if ev.EventName != "admin" {
		t.Fatalf("EventName = %q, want admin", ev.EventName)
	}
	ev, _ = Line(`{"level":"info","kind":"admin","event.name":"analytics","msg":"x"}`)
	if ev.EventName != "analytics" {
		t.Fatalf("event.name must win over legacy kind, got %q", ev.EventName)
	}
}

func TestNestedExceptionEqualsDotted(t *testing.T) {
	nested, _ := Line(`{"level":"error","msg":"m","exception":{"type":"T","message":"m1","stacktrace":"s"},"event":{"name":"n"}}`)
	dotted, _ := Line(`{"level":"error","msg":"m","exception.type":"T","exception.message":"m1","exception.stacktrace":"s","event.name":"n"}`)
	if nested != dotted {
		t.Fatalf("nested %+v != dotted %+v", nested, dotted)
	}
	if nested.ExceptionMessage != "m1" || nested.ExceptionType != "T" || nested.EventName != "n" {
		t.Fatalf("nested not flattened: %+v", nested)
	}
}

func TestNonStringValues(t *testing.T) {
	ev, kind := Line(`{"level":"error","msg":42,"trace_id":7,"exception.message":["a"],"exception.type":null}`)
	if kind != Error {
		t.Fatalf("kind = %v", kind)
	}
	if ev.Msg != "42" {
		t.Errorf("non-string msg should be fmt.Sprint-ed, got %q", ev.Msg)
	}
	if ev.TraceID != "" || ev.ExceptionMessage != "" || ev.ExceptionType != "" {
		t.Errorf("non-string values must be ignored: %+v", ev)
	}
}

func TestNginxMsg(t *testing.T) {
	ev, kind := Line(`2026/10/29 09:00:00 [error] 12#12: *1 connect() failed (111)`)
	if kind != Error || ev.Msg != "12#12: *1 connect() failed (111)" || ev.Source != "nginx" {
		t.Fatalf("got %+v (%v)", ev, kind)
	}
}

func TestOversizeLineTruncated(t *testing.T) {
	const limit = 64 << 10
	big := strings.Repeat("é", 1<<20) // 2 MiB of two-byte runes
	line := fmt.Sprintf(`{"level":"error","msg":%q,"exception.message":%q,"exception.stacktrace":%q}`, big, big, big)
	ev, kind := Line(line)
	if kind != Error {
		t.Fatalf("kind = %v", kind)
	}
	for name, v := range map[string]string{"msg": ev.Msg, "exception.message": ev.ExceptionMessage, "stacktrace": ev.Stacktrace} {
		if len(v) == 0 || len(v) > limit || !utf8.ValidString(v) {
			t.Errorf("%s: len %d valid=%v", name, len(v), utf8.ValidString(v))
		}
	}
	cev, ckind := Line("panic: " + big)
	if ckind != CrashStart || len(cev.Msg) > limit || !utf8.ValidString(cev.Msg) {
		t.Errorf("crash msg len %d kind %v", len(cev.Msg), ckind)
	}
}

var t0 = time.Date(2026, 10, 29, 9, 0, 0, 0, time.UTC)

func rec(pod, container, msg string, dt time.Duration) Record {
	return Record{Time: t0.Add(dt), StreamID: pod + container, Project: "proj-x", Pod: pod, Container: container, Node: "n1", Message: msg}
}

const jsonInfo = `{"time":"2026-10-29T09:00:01.000Z","level":"info","msg":"back"}`
const jsonErr = `{"time":"2026-10-29T09:00:01.000Z","level":"error","msg":"e","exception.message":"boom"}`

func TestAssemblerCollectsStackUntilJSON(t *testing.T) {
	a := NewAssembler(200, 2*time.Second)
	sig := rec("a", "app", "panic: boom", 0)
	for i, r := range []Record{
		sig,
		rec("a", "app", "goroutine 1 [running]:", 1),
		rec("a", "app", "main.main()", 2),
		rec("a", "app", "\t/app/main.go:10 +0x1d", 3),
	} {
		if got := a.Feed(r); len(got) != 0 {
			t.Fatalf("feed %d emitted early: %+v", i, got)
		}
	}
	got := a.Feed(rec("a", "app", jsonInfo, 4))
	if len(got) != 1 {
		t.Fatalf("got %d classified, want 1", len(got))
	}
	c := got[0]
	if c.Record != sig {
		t.Errorf("Record should be the signature record: %+v", c.Record)
	}
	if c.Event.Level != "fatal" || c.Event.Source != "crash" || c.Event.Msg != "panic: boom" {
		t.Errorf("event = %+v", c.Event)
	}
	if want := "goroutine 1 [running]:\nmain.main()\n\t/app/main.go:10 +0x1d"; c.Event.Stacktrace != want {
		t.Errorf("stacktrace = %q, want %q", c.Event.Stacktrace, want)
	}
	if got := a.Flush(t0.Add(time.Hour)); len(got) != 0 {
		t.Errorf("crash must be closed, Flush emitted %+v", got)
	}
}

func TestAssemblerJSONErrorClosesCrashThenEmitsItself(t *testing.T) {
	a := NewAssembler(200, 2*time.Second)
	a.Feed(rec("a", "app", "fatal error: x", 0))
	a.Feed(rec("a", "app", "goroutine 1:", 1))
	got := a.Feed(rec("a", "app", jsonErr, 2))
	if len(got) != 2 || got[0].Event.Source != "crash" || got[1].Event.Source != "json" {
		t.Fatalf("want [crash json], got %+v", got)
	}
	if got[1].Record.Message != jsonErr {
		t.Errorf("json Classified carries wrong record: %+v", got[1].Record)
	}
}

func TestAssemblerKeepsConcurrentCrashesApart(t *testing.T) {
	a := NewAssembler(200, 2*time.Second)
	feed := []Record{
		rec("a", "app", "panic: A", 0),
		rec("b", "app", "panic: B", 0),
		rec("a", "app", "a-line-1", 1),
		rec("b", "app", "b-line-1", 1),
		rec("a", "app", "a-line-2", 2),
		rec("b", "app", "b-line-2", 2),
	}
	for _, r := range feed {
		if got := a.Feed(r); len(got) != 0 {
			t.Fatalf("early emit %+v", got)
		}
	}
	var all []Classified
	all = append(all, a.Feed(rec("a", "app", jsonInfo, 3))...)
	all = append(all, a.Feed(rec("b", "app", jsonInfo, 3))...)
	if len(all) != 2 {
		t.Fatalf("got %d, want 2", len(all))
	}
	if all[0].Event.Msg != "panic: A" || all[0].Event.Stacktrace != "a-line-1\na-line-2" {
		t.Errorf("a: %+v", all[0].Event)
	}
	if all[1].Event.Msg != "panic: B" || all[1].Event.Stacktrace != "b-line-1\nb-line-2" {
		t.Errorf("b: %+v", all[1].Event)
	}
}

func TestAssemblerSameKeyDifferentContainerApart(t *testing.T) {
	a := NewAssembler(200, 2*time.Second)
	a.Feed(rec("a", "app", "panic: A", 0))
	a.Feed(rec("a", "sidecar", "not part of the crash", 1))
	got := a.Flush(t0.Add(time.Hour))
	if len(got) != 1 || got[0].Event.Stacktrace != "" {
		t.Fatalf("got %+v", got)
	}
}

func TestAssemblerCrashStartClosesPrevious(t *testing.T) {
	a := NewAssembler(200, 2*time.Second)
	a.Feed(rec("a", "app", "panic: first", 0))
	a.Feed(rec("a", "app", "l1", 1))
	got := a.Feed(rec("a", "app", "panic: second", 2))
	if len(got) != 1 || got[0].Event.Msg != "panic: first" || got[0].Event.Stacktrace != "l1" {
		t.Fatalf("got %+v", got)
	}
	got = a.Flush(t0.Add(time.Hour))
	if len(got) != 1 || got[0].Event.Msg != "panic: second" {
		t.Fatalf("second crash lost: %+v", got)
	}
}

func TestAssemblerFlushesAfterMaxAge(t *testing.T) {
	a := NewAssembler(200, 2*time.Second)
	a.Feed(rec("a", "app", "panic: boom", 0))
	a.Feed(rec("a", "app", "line", time.Second))
	// last appended line is at +1s; now-maxAge must be >= +1s to flush.
	if got := a.Flush(t0.Add(2 * time.Second)); len(got) != 0 {
		t.Fatalf("flushed too early: %+v", got)
	}
	got := a.Flush(t0.Add(3 * time.Second))
	if len(got) != 1 || got[0].Event.Stacktrace != "line" {
		t.Fatalf("got %+v", got)
	}
	if got := a.Flush(t0.Add(time.Hour)); len(got) != 0 {
		t.Fatalf("flushed twice: %+v", got)
	}
}

func TestAssemblerFlushOrderDeterministic(t *testing.T) {
	a := NewAssembler(200, time.Second)
	a.Feed(rec("z", "app", "panic: z", 0))
	a.Feed(rec("a", "app", "panic: a", 0))
	a.Feed(rec("m", "app", "panic: m", -time.Second))
	got := a.Flush(t0.Add(time.Hour))
	var order []string
	for _, c := range got {
		order = append(order, c.Event.Msg)
	}
	if strings.Join(order, ",") != "panic: m,panic: a,panic: z" {
		t.Fatalf("order = %v", order)
	}
}

func TestAssemblerCapsAt200Lines(t *testing.T) {
	a := NewAssembler(200, 2*time.Second)
	a.Feed(rec("a", "app", "panic: deep", 0))
	var emitted []Classified
	for i := 1; i <= 250; i++ {
		got := a.Feed(rec("a", "app", fmt.Sprintf("frame %d", i), time.Duration(i)*time.Millisecond))
		if i < 200 && len(got) != 0 {
			t.Fatalf("emitted early at line %d", i)
		}
		emitted = append(emitted, got...)
	}
	if len(emitted) != 1 {
		t.Fatalf("got %d classified, want 1", len(emitted))
	}
	lines := strings.Split(emitted[0].Event.Stacktrace, "\n")
	if len(lines) != 200 || lines[0] != "frame 1" || lines[199] != "frame 200" {
		t.Fatalf("stacktrace has %d lines (%q..%q)", len(lines), lines[0], lines[len(lines)-1])
	}
	if got := a.Flush(t0.Add(time.Hour)); len(got) != 0 {
		t.Fatalf("cap must close the crash: %+v", got)
	}
}

func TestAssemblerDropsPlainWithoutCrash(t *testing.T) {
	a := NewAssembler(200, 2*time.Second)
	if got := a.Feed(rec("a", "app", "just text", 0)); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
	if got := a.Flush(t0.Add(time.Hour)); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestAssemblerNginxErrorImmediate(t *testing.T) {
	a := NewAssembler(200, 2*time.Second)
	r := rec("web", "nginx", `2026/10/29 09:00:00 [emerg] 1#1: bind() failed`, 0)
	got := a.Feed(r)
	if len(got) != 1 || got[0].Event.Source != "nginx" || got[0].Event.Level != "fatal" || got[0].Record != r {
		t.Fatalf("got %+v", got)
	}
	if got := a.Feed(rec("web", "nginx", `2026/10/29 09:00:00 [notice] 1#1: ok`, 1)); len(got) != 0 {
		t.Fatalf("notice emitted: %+v", got)
	}
}

func TestAssemblerNonErrorJSONNotEmitted(t *testing.T) {
	a := NewAssembler(200, 2*time.Second)
	if got := a.Feed(rec("a", "app", jsonInfo, 0)); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestAssemblerStacktraceByteCap(t *testing.T) {
	a := NewAssembler(200, 2*time.Second)
	a.Feed(rec("a", "app", "panic: big", 0))
	huge := strings.Repeat("é", 1<<20)
	for i := 0; i < 5; i++ {
		a.Feed(rec("a", "app", huge, time.Duration(i)))
	}
	got := a.Flush(t0.Add(time.Hour))
	if len(got) != 1 {
		t.Fatalf("got %d", len(got))
	}
	if st := got[0].Event.Stacktrace; len(st) == 0 || len(st) > 64<<10 || !utf8.ValidString(st) {
		t.Fatalf("stacktrace len %d valid %v", len(st), utf8.ValidString(st))
	}
}
