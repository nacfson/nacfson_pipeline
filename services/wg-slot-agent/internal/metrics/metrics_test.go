package metrics

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseLatestHandshakes(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	out := []byte("pubA\t999990\npubB\t999700\npubC\t0\n")
	h := ParseLatestHandshakes(out, now)
	if h.Stale || h.Peers != 3 || h.MaxAge != 300*time.Second {
		t.Fatalf("got %+v, want 3 peers, not stale, max age 300s", h)
	}
	stale := ParseLatestHandshakes([]byte("pubA\t999700\npubB\t0\n"), now)
	if !stale.Stale {
		t.Fatalf("every handshake older than 180s must be stale: %+v", stale)
	}
	if !ParseLatestHandshakes(nil, now).Stale {
		t.Fatal("no peers must count as stale")
	}
}

func TestBufferBytesSumsChunks(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "tail.0")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, size := range map[string]int{"a.flb": 100, "tail.0/b.flb": 23} {
		if err := os.WriteFile(filepath.Join(dir, name), make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got := BufferBytes(dir); got != 123 {
		t.Fatalf("BufferBytes = %d, want 123", got)
	}
	if got := BufferBytes(filepath.Join(dir, "missing")); got != 0 {
		t.Fatalf("missing dir = %d, want 0", got)
	}
}

func capturedHandshakes(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestCapturedHandshakesFromRealWireGuard(t *testing.T) {
	capturedAt := time.Unix(1795095119, 0)
	fresh := ParseLatestHandshakes(capturedHandshakes(t, "latest-handshakes.recent.txt"), capturedAt)
	if fresh.Peers != 1 || fresh.Stale || fresh.MaxAge != time.Second {
		t.Fatalf("fresh handshake parsed as %+v", fresh)
	}
	mixed := ParseLatestHandshakes(capturedHandshakes(t, "latest-handshakes.one-never.txt"), capturedAt)
	if mixed.Peers != 2 || mixed.Stale {
		t.Fatalf("one live peer keeps the tunnel healthy: %+v", mixed)
	}
	later := ParseLatestHandshakes(capturedHandshakes(t, "latest-handshakes.one-never.txt"), capturedAt.Add(4*time.Minute))
	if !later.Stale {
		t.Fatalf("every handshake older than 180 s must be stale: %+v", later)
	}
	annotations := mixed.Annotations(4096)
	if annotations[BufferAnnotation] != "4096" || annotations[HandshakeAnnotation] == "" {
		t.Fatalf("annotations = %v", annotations)
	}
}
