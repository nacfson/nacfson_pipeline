package opsrelay

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)

var vms = []string{"a1-1", "a1-2", "micro-1", "micro-2"}

func capturedEvents(t *testing.T, name string, start time.Time) []Event {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "tests", "fixtures", "ops-events", "expected", name))
	if err != nil {
		t.Fatal(err)
	}
	var events []Event
	if err := json.Unmarshal(raw, &events); err != nil {
		t.Fatal(err)
	}
	for i := range events {
		events[i].Time = start.Add(time.Duration(i) * time.Second)
	}
	return events
}

func heartbeatAll(e *Engine, at time.Time) {
	for _, vm := range vms {
		e.Event(Event{Kind: "agent.heartbeat", State: "ok", Subject: vmSubject(vm), VM: vm, ObservedBy: vm, Time: at})
	}
}

func subjects(msgs []Message) []string {
	var out []string
	for _, m := range msgs {
		out = append(out, string(m.Level)+" "+m.Subject)
	}
	return out
}

func assertMessages(t *testing.T, got []Message, want ...string) {
	t.Helper()
	g := subjects(got)
	if strings.Join(g, " | ") != strings.Join(want, " | ") {
		t.Fatalf("messages:\n got  %q\n want %q\n full %v", g, want, got)
	}
}

func TestCapturedFenceAndUnfenceSendOneDownAndOneOK(t *testing.T) {
	e := NewEngine(vms, 3, t0)
	for _, ev := range capturedEvents(t, "broker-fence-unfence.controller-2.log.json", t0.Add(time.Minute)) {
		e.Event(ev)
	}
	msgs := e.Drain()
	assertMessages(t, msgs, "DOWN vm/micro-1", "OK vm/micro-1")
	if !strings.Contains(msgs[0].Text, "broker 3 fenced (observed by a1-1)") {
		t.Fatalf("down text = %q", msgs[0].Text)
	}
	if !strings.Contains(msgs[1].Text, "all partitions 3/3") {
		t.Fatalf("ok text = %q", msgs[1].Text)
	}
}

func TestStoppedActiveControllerStillAlertsOnce(t *testing.T) {
	e := NewEngine(vms, 3, t0)
	for _, ev := range capturedEvents(t, "controller-leader-change.controller-1.log.json", t0.Add(time.Minute)) {
		e.Event(ev)
	}
	assertMessages(t, e.Drain(), "DOWN vm/a1-2", "OK vm/a1-2")
}

func TestRepeatsInTheSameStateSendNothing(t *testing.T) {
	e := NewEngine(vms, 3, t0)
	down := Event{Kind: "broker.fenced", State: "down", Subject: "kafka/broker-3", VM: "micro-1", ObservedBy: "a1-1", Time: t0}
	for i := 0; i < 3; i++ {
		down.Time = t0.Add(time.Duration(i) * time.Second)
		e.Event(down)
	}
	assertMessages(t, e.Drain(), "DOWN vm/micro-1")
}

func TestEventsOfAnOpenIncidentAreGrouped(t *testing.T) {
	e := NewEngine(vms, 3, t0)
	e.Event(Event{Kind: "broker.fenced", State: "down", Subject: "kafka/broker-3", VM: "micro-1", ObservedBy: "a1-1", Time: t0})
	e.Event(Event{Kind: "unit.failed", State: "down", Subject: "unit/micro-1/kafka.service", VM: "micro-1", ObservedBy: "micro-1", Time: t0.Add(time.Second)})
	for _, p := range []string{"logs.proj.valid-0", "logs.proj.valid-1"} {
		e.Event(Event{Kind: "partition.isr_shrink", State: "down", Subject: "kafka/partition/" + p, VM: "a1-1", ObservedBy: "a1-1", Time: t0.Add(2 * time.Second)})
	}
	assertMessages(t, e.Drain(), "DOWN vm/micro-1")
	e.Event(Event{Kind: "unit.active", State: "up", Subject: "unit/micro-1/kafka.service", VM: "micro-1", ObservedBy: "micro-1", Time: t0.Add(10 * time.Minute)})
	if got := e.Drain(); len(got) != 0 {
		t.Fatalf("incident closed while broker 3 still fenced: %v", got)
	}
	for _, p := range []string{"logs.proj.valid-0", "logs.proj.valid-1"} {
		e.Event(Event{Kind: "partition.isr_expand", State: "up", Subject: "kafka/partition/" + p, VM: "a1-1", ObservedBy: "a1-1", Time: t0.Add(13 * time.Minute)})
	}
	e.Event(Event{Kind: "broker.unfenced", State: "up", Subject: "kafka/broker-3", VM: "micro-1", ObservedBy: "a1-1", Time: t0.Add(14 * time.Minute)})
	msgs := e.Drain()
	assertMessages(t, msgs, "OK vm/micro-1")
	if !strings.Contains(msgs[0].Text, "recovered after 14 min · all partitions 3/3") {
		t.Fatalf("ok text = %q", msgs[0].Text)
	}
}

func TestHeartbeatSilenceLabels(t *testing.T) {
	e := NewEngine(vms, 3, t0)
	heartbeatAll(e, t0.Add(4*time.Minute))
	e.Metric(Metric{Source: "vm/a1-1", Metric: "kafka.brokers.active", Value: 3, Time: t0.Add(4 * time.Minute)})
	for _, vm := range []string{"a1-1", "a1-2", "micro-1"} {
		e.Event(Event{Kind: "agent.heartbeat", State: "ok", Subject: vmSubject(vm), VM: vm, Time: t0.Add(7 * time.Minute)})
	}
	e.Tick(t0.Add(7*time.Minute + 30*time.Second))
	msgs := e.Drain()
	assertMessages(t, msgs, "DOWN vm/micro-2")
	if !strings.HasPrefix(msgs[0].Text, "agent silent, broker healthy") {
		t.Fatalf("label = %q", msgs[0].Text)
	}

	e2 := NewEngine(vms, 3, t0)
	heartbeatAll(e2, t0.Add(4*time.Minute))
	e2.Metric(Metric{Source: "vm/a1-1", Metric: "kafka.brokers.active", Value: 2, Time: t0.Add(5 * time.Minute)})
	for _, vm := range []string{"a1-1", "a1-2", "micro-2"} {
		e2.Event(Event{Kind: "agent.heartbeat", State: "ok", Subject: vmSubject(vm), VM: vm, Time: t0.Add(7 * time.Minute)})
	}
	e2.Tick(t0.Add(7*time.Minute + 30*time.Second))
	msgs = e2.Drain()
	assertMessages(t, msgs, "DOWN vm/micro-1")
	if !strings.HasPrefix(msgs[0].Text, "broker down") {
		t.Fatalf("label = %q", msgs[0].Text)
	}
	e2.Event(Event{Kind: "agent.heartbeat", State: "ok", Subject: vmSubject("micro-1"), VM: "micro-1", Time: t0.Add(9 * time.Minute)})
	assertMessages(t, e2.Drain(), "OK vm/micro-1")
}

func TestNoSilenceVerdictDuringStartupWait(t *testing.T) {
	e := NewEngine(vms, 3, t0)
	e.Tick(t0.Add(2*time.Minute + 59*time.Second))
	assertMessages(t, e.Drain())
	e.Tick(t0.Add(3 * time.Minute))
	if len(e.Drain()) != 4 {
		t.Fatal("after the start-up wait, four silent VMs must each raise DOWN")
	}
}

func TestUnreachableHoldsBackSilenceAndTunnels(t *testing.T) {
	e := NewEngine(vms, 3, t0)
	heartbeatAll(e, t0.Add(4*time.Minute))
	e.Disconnected(t0.Add(5 * time.Minute))
	e.Tick(t0.Add(5*time.Minute + 59*time.Second))
	assertMessages(t, e.Drain())
	e.Tick(t0.Add(6 * time.Minute))
	msgs := e.Drain()
	assertMessages(t, msgs, "DOWN cluster/unreachable")
	if !msgs[0].Direct {
		t.Fatal("unreachable must be sent directly")
	}
	e.KubeEvent("TunnelStale", "node-a", "every peer handshake is older than 180s", t0.Add(7*time.Minute))
	e.Tick(t0.Add(20 * time.Minute))
	assertMessages(t, e.Drain())
	e.Connected(t0.Add(21 * time.Minute))
	assertMessages(t, e.Drain(), "OK cluster/unreachable", "DOWN gke/node-a/tunnel")
	e.Tick(t0.Add(23 * time.Minute))
	assertMessages(t, e.Drain())
}

func TestTunnelEventsAndHandshakeMetric(t *testing.T) {
	e := NewEngine(vms, 3, t0)
	e.Metric(Metric{Source: "gke/node-a", Metric: "wireguard.handshake_age", Value: 9999, Time: t0})
	assertMessages(t, e.Drain())
	e.KubeEvent("TunnelStale", "node-a", "stale", t0.Add(time.Minute))
	e.KubeEvent("TunnelStale", "node-a", "stale", t0.Add(2*time.Minute))
	e.KubeEvent("TunnelRecovered", "node-a", "recovered", t0.Add(3*time.Minute))
	assertMessages(t, e.Drain(), "DOWN gke/node-a/tunnel", "OK gke/node-a/tunnel")
}

func TestDeployEvents(t *testing.T) {
	e := NewEngine(vms, 3, t0)
	e.KubeEvent("DeployFailed", "", "site.yml 4f2c9e1 failed on micro-1 at kafka/restart", t0)
	e.KubeEvent("DeployBlocked", "", "site.yml 4f2c9e1 blocked: under-replicated partitions", t0.Add(time.Minute))
	e.KubeEvent("DeployOK", "", "site.yml 7a10b3d applied to 4 VMs in 31 min", t0.Add(time.Hour))
	assertMessages(t, e.Drain(), "DOWN deploy/site.yml", "OK deploy/site.yml")
}

func TestRebuildSendsOnlyOpenIncidentsAsDelayed(t *testing.T) {
	e := NewEngine(vms, 3, t0.Add(30*time.Hour))
	events := []Event{
		{Kind: "broker.fenced", State: "down", Subject: "kafka/broker-1", VM: "a1-1", ObservedBy: "a1-2", Time: t0},
		{Kind: "broker.unfenced", State: "up", Subject: "kafka/broker-1", VM: "a1-1", ObservedBy: "a1-2", Time: t0.Add(5 * time.Minute)},
		{Kind: "broker.fenced", State: "down", Subject: "kafka/broker-3", VM: "micro-1", ObservedBy: "a1-1", Time: t0.Add(9*time.Hour + 12*time.Minute)},
	}
	e.Rebuild(events, nil)
	msgs := e.Drain()
	assertMessages(t, msgs, "DOWN vm/micro-1")
	if !msgs[0].Delayed || !strings.HasPrefix(msgs[0].String(), "(delayed, event at 18:12 UTC) [kafka] DOWN") {
		t.Fatalf("delayed message = %q", msgs[0].String())
	}
	e.Event(Event{Kind: "broker.fenced", State: "down", Subject: "kafka/broker-3", VM: "micro-1", ObservedBy: "a1-2", Time: t0.Add(30 * time.Hour)})
	assertMessages(t, e.Drain())
}

func TestMessageFormat(t *testing.T) {
	m := Message{Level: Down, Subject: "vm/micro-1", Text: "broker 3 fenced (observed by a1-1)", At: t0}
	if got := m.String(); got != "[kafka] DOWN  vm/micro-1 — broker 3 fenced (observed by a1-1)" {
		t.Fatalf("format = %q", got)
	}
}

func TestPartitionRecoveryAfterAGroupedIncidentSendsNothing(t *testing.T) {
	e := NewEngine(vms, 3, t0)
	e.Event(Event{Kind: "broker.fenced", State: "down", Subject: "kafka/broker-2", VM: "a1-2", ObservedBy: "a1-1", Time: t0})
	e.Event(Event{Kind: "partition.isr_shrink", State: "down", Subject: "kafka/partition/logs.proj.valid-0", VM: "a1-1", ObservedBy: "a1-1", Time: t0.Add(time.Second)})
	e.Event(Event{Kind: "broker.unfenced", State: "up", Subject: "kafka/broker-2", VM: "a1-2", ObservedBy: "a1-1", Time: t0.Add(time.Minute)})
	e.Event(Event{Kind: "partition.isr_expand", State: "up", Subject: "kafka/partition/logs.proj.valid-0", VM: "a1-1", ObservedBy: "a1-1", Time: t0.Add(2 * time.Minute)})
	assertMessages(t, e.Drain(), "DOWN vm/a1-2", "OK vm/a1-2")
}

func TestReplayDoesNotResendPartitionsFoldedIntoAnIncident(t *testing.T) {
	e := NewEngine(vms, 3, t0)
	e.Event(Event{Kind: "broker.fenced", State: "down", Subject: "kafka/broker-3", VM: "micro-1", ObservedBy: "a1-1", Time: t0})
	e.Event(Event{Kind: "partition.isr_shrink", State: "down", Subject: "kafka/partition/p-0", VM: "a1-1", ObservedBy: "a1-1", Time: t0.Add(time.Second)})
	assertMessages(t, e.Drain(), "DOWN vm/micro-1")
	e.Replay(func() {})
	assertMessages(t, e.Drain())
}

func TestNodeSlotEntriesClearWhenTheNodeLeaves(t *testing.T) {
	e := NewEngine(vms, 3, t0)
	e.KubeEvent("NoFreeSlot", "node-c", "no free slot", t0)
	e.NodeHoldsSlot("node-c", 2, t0.Add(30*time.Second), t0.Add(30*time.Second))
	e.KubeEvent("TunnelStale", "node-c", "stale", t0.Add(time.Minute))
	e.Metric(Metric{Source: "gke/node-c", Metric: "fluentbit.buffer.bytes", Value: 200 << 20, Time: t0.Add(time.Minute)})
	assertMessages(t, e.Drain(), "WARN gke/node-c/slot", "OK gke/node-c/slot", "DOWN gke/node-c/tunnel", "WARN gke/node-c/buffer")
	e.NodeLeft("node-c", t0.Add(5*time.Minute))
	assertMessages(t, e.Drain(), "OK gke/node-c/tunnel", "OK gke/node-c/buffer")
	e.Metric(Metric{Source: "gke/node-a", Metric: "fluentbit.buffer.bytes", Value: 1, Time: t0.Add(6 * time.Minute)})
	for _, m := range e.Drain() {
		if strings.Contains(m.Subject, "node-c") {
			t.Fatalf("node-c came back after leaving: %v", m)
		}
	}
}
