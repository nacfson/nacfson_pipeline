package opsrelay

import (
	"reflect"
	"testing"
	"time"
)

func held(node string, slot int, acquired time.Time) HeldLease {
	return HeldLease{Holder: node, Slot: slot, Acquired: acquired, Expires: acquired.Add(90 * time.Second)}
}

func TestSlotHoldersReportJoinsMovesAndDepartures(t *testing.T) {
	s := NewSlotHolders()
	a := held("node-a", 0, t0)
	if got := s.Set("wg-slot-0", a, t0); !reflect.DeepEqual(got, []SlotChange{{"node-a", 0, true, t0}}) {
		t.Fatalf("join = %v", got)
	}
	renewed := a
	renewed.Expires = t0.Add(120 * time.Second)
	if got := s.Set("wg-slot-0", renewed, t0); got != nil {
		t.Fatalf("renewal reported %v", got)
	}
	if got := s.Set("wg-slot-1", held("node-a", 1, t0), t0); got != nil {
		t.Fatalf("second lease of the same node reported %v", got)
	}
	if got := s.Set("wg-slot-0", HeldLease{Slot: 0}, t0); !reflect.DeepEqual(got, []SlotChange{{"node-a", 1, true, t0}}) {
		t.Fatalf("move = %v, want node-a now on slot 1", got)
	}
	if got := s.Remove("wg-slot-1", t0); !reflect.DeepEqual(got, []SlotChange{{Node: "node-a", Slot: 1}}) {
		t.Fatalf("deleted lease = %v", got)
	}
}

func TestSlotHoldersForgetAVanishedNodeWhenItsLeaseExpires(t *testing.T) {
	s := NewSlotHolders()
	s.Set("wg-slot-2", held("node-c", 2, t0), t0)
	if got := s.Sync(t0.Add(89 * time.Second)); got != nil {
		t.Fatalf("live lease reported %v", got)
	}
	if got := s.Sync(t0.Add(90 * time.Second)); !reflect.DeepEqual(got, []SlotChange{{Node: "node-c", Slot: 2}}) {
		t.Fatalf("expired lease = %v", got)
	}
}

func TestSlotHoldersRelistReplacesStaleCacheBeforeJudgingExpiry(t *testing.T) {
	s := NewSlotHolders()
	s.Set("wg-slot-0", held("node-a", 0, t0), t0)
	s.Set("wg-slot-1", held("node-b", 1, t0), t0)
	later := t0.Add(10 * time.Minute)
	fresh := held("node-a", 0, t0)
	fresh.Expires = later.Add(60 * time.Second)
	if got := s.Replace(map[string]HeldLease{"wg-slot-0": fresh}, later); !reflect.DeepEqual(got, []SlotChange{{Node: "node-b", Slot: 1}}) {
		t.Fatalf("relist = %v, want only node-b gone", got)
	}
}

func TestStaleKubeEventsDoNotOverrideNewerSlotState(t *testing.T) {
	e := NewEngine(vms, 3, t0)
	e.KubeEvent("NoFreeSlot", "node-a", "no free slot", t0)
	e.NodeHoldsSlot("node-a", 1, t0.Add(time.Minute), t0.Add(time.Minute))
	assertMessages(t, e.Drain(), "WARN gke/node-a/slot", "OK gke/node-a/slot")
	e.Replay(func() { e.KubeEvent("NoFreeSlot", "node-a", "no free slot", t0) })
	assertMessages(t, e.Drain())

	e.KubeEvent("TunnelStale", "node-b", "stale", t0.Add(2*time.Minute))
	e.NodeLeft("node-b", t0.Add(5*time.Minute))
	e.Drain()
	e.Replay(func() { e.KubeEvent("TunnelStale", "node-b", "stale", t0.Add(2*time.Minute)) })
	assertMessages(t, e.Drain())
}

func TestTunnelEventsAfterTheSlotWasTakenStillCount(t *testing.T) {
	e := NewEngine(vms, 3, t0)
	e.NodeHoldsSlot("node-a", 0, t0, t0.Add(time.Minute))
	e.Drain()
	e.Replay(func() { e.KubeEvent("TunnelStale", "node-a", "stale", t0.Add(30*time.Second)) })
	assertMessages(t, e.Drain(), "DOWN gke/node-a/tunnel")
}

func TestReplaySendsARecoveryForANotifiedSubject(t *testing.T) {
	e := NewEngine(vms, 3, t0)
	e.KubeEvent("TunnelStale", "node-a", "stale", t0)
	assertMessages(t, e.Drain(), "DOWN gke/node-a/tunnel")
	e.Replay(func() { e.KubeEvent("TunnelRecovered", "node-a", "back", t0.Add(time.Minute)) })
	assertMessages(t, e.Drain(), "OK gke/node-a/tunnel")
}
