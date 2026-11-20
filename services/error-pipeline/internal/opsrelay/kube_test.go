package opsrelay

import (
	"encoding/json"
	"testing"
	"time"
)

func TestListedEventsReplayInTimeOrder(t *testing.T) {
	items := []json.RawMessage{
		json.RawMessage(`{"reason":"TunnelRecovered","message":"ok","lastTimestamp":"2026-11-19T10:02:00Z","source":{"host":"node-a"}}`),
		json.RawMessage(`{"reason":"TunnelStale","message":"stale","lastTimestamp":"2026-11-19T10:00:00Z","source":{"host":"node-a"}}`),
	}
	e := NewEngine(vms, 3, t0)
	e.Replay(func() {
		for _, raw := range SortEventsByTime(items) {
			var ev KubeEvent
			if err := json.Unmarshal(raw, &ev); err != nil {
				t.Fatal(err)
			}
			e.KubeEvent(ev.Reason, ev.Source.Host, ev.Message, ev.Time())
		}
	})
	assertMessages(t, e.Drain())
	if got := e.get("gke/node-a/tunnel").level; got != OK {
		t.Fatalf("tunnel level after replay = %s, want OK", got)
	}
	var first KubeEvent
	_ = json.Unmarshal(SortEventsByTime(items)[0], &first)
	if first.Reason != "TunnelStale" || !first.Time().Equal(time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("first replayed event = %+v", first)
	}
}
