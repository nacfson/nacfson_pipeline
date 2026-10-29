package state

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"nacfson_pipeline/services/error-pipeline/internal/classify"
	"nacfson_pipeline/services/error-pipeline/internal/group"
)

var t0 = time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

func occ() classify.Classified {
	return classify.Classified{
		Record: classify.Record{
			Project:   "proj-blog",
			Container: "web",
			Pod:       "web-1",
			Message:   `{"level":"error","msg":"boom"}`,
		},
		Event: classify.Event{Level: "error", Msg: "boom", Source: classify.SourceJSON},
	}
}

func TestRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	tr := group.NewTracker(group.DefaultConfig())
	tr.Observe(occ(), t0)

	in := Snapshot{Version: 1, Cursor: t0.Add(time.Second), Groups: tr.Export()}
	if err := Save(path, in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	out, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !out.Cursor.Equal(in.Cursor) || out.Version != 1 {
		t.Fatalf("cursor/version mismatch: %+v", out)
	}
	if !reflect.DeepEqual(out.Groups, in.Groups) {
		t.Fatalf("groups mismatch:\n got %+v\nwant %+v", out.Groups, in.Groups)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", fi.Mode().Perm())
	}
}

func TestSaveAlwaysWritesVersion1(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := Save(path, Snapshot{Cursor: t0}); err != nil {
		t.Fatal(err)
	}
	out, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if out.Version != 1 {
		t.Fatalf("version = %d, want 1", out.Version)
	}
}

func TestMissingFileIsEmpty(t *testing.T) {
	s, err := Load(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("missing file must not error: %v", err)
	}
	if !s.Cursor.IsZero() || s.Groups != nil {
		t.Fatalf("want empty snapshot, got %+v", s)
	}
}

func TestCorruptFileReturnsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"cursor":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("want error for corrupt JSON")
	}
}

func TestUnknownVersionReturnsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(`{"version":2,"groups":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("want error for unsupported version")
	}
}

func TestSaveIsAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	for i := 0; i < 3; i++ {
		if err := Save(path, Snapshot{Cursor: t0.Add(time.Duration(i) * time.Second)}); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "state.json" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("want only state.json in dir, got %v", names)
	}
}

func TestSaveFailureLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	// A directory at the target path makes the final rename fail.
	path := filepath.Join(dir, "state.json")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Save(path, Snapshot{}); err == nil {
		t.Fatal("want error when target is a directory")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("temp file left behind: %v", entries)
	}
}

func TestRestartMidWindowClosesOnceWithoutReannounce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	cfg := group.DefaultConfig()

	before := group.NewTracker(cfg)
	if got := before.Observe(occ(), t0); len(got) != 1 || got[0].Type != group.NotifyNew {
		t.Fatalf("first observe: %+v", got)
	}
	if err := Save(path, Snapshot{Cursor: t0, Groups: before.Export()}); err != nil {
		t.Fatal(err)
	}

	snap, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	after := group.NewTracker(cfg)
	after.Import(snap.Groups)

	for _, a := range after.Observe(occ(), t0.Add(time.Minute)) {
		if a.Type == group.NotifyNew {
			t.Fatalf("restart re-announced a known error: %+v", a)
		}
	}
	acts := after.Tick(t0.Add(3 * time.Minute))
	if len(acts) != 1 || acts[0].Type != group.Handoff {
		t.Fatalf("want exactly one Handoff, got %+v", acts)
	}
	if acts[0].Group.Count != 2 {
		t.Fatalf("count = %d, want 2", acts[0].Group.Count)
	}
	if again := after.Tick(t0.Add(4 * time.Minute)); len(again) != 0 {
		t.Fatalf("handed off twice: %+v", again)
	}
}
