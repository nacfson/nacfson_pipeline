package opsrelay

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func contractExample(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "specs", "013-oci-kafka-cluster", "contracts", "ops-relay.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	start := strings.Index(doc, "ops-relay status ·")
	end := strings.Index(doc[start:], "```")
	return doc[start : start+end]
}

func exampleBoard(now time.Time) *Board {
	nodes, err := ParseNodes("1=a1-1:broker+controller:1024,2=a1-2:broker+controller:1024,3=micro-1:broker:384,4=micro-2:controller:256")
	if err != nil {
		panic(err)
	}
	b := NewBoard(nodes)
	b.SetCluster(Cluster{Brokers: 3, Controllers: 3, Leader: 1})
	rows := []struct {
		vm                          string
		p99, in, heapMB, disk, cert float64
	}{
		{"a1-1", 6, 41000, 612, 0.71, 58}, {"a1-2", 7, 40000, 590, 0.70, 58},
		{"micro-1", 11, 40000, 301, 0.69, 58}, {"micro-2", 0, 0, 180, 0.88, 58},
	}
	for _, r := range rows {
		src := "vm/" + r.vm
		for name, value := range map[string]float64{
			"kafka.produce.latency_p99": r.p99, "kafka.bytes_in": r.in, "kafka.heap.used": r.heapMB * (1 << 20),
			"os.disk.free_ratio": r.disk, "cert.days_left.min": r.cert,
		} {
			b.Metric(Metric{Source: src, Metric: name, Value: value, Time: now.Add(-12 * time.Second)})
		}
	}
	b.SetSlot("node-a", 0)
	b.SetSlot("node-b", 1)
	b.Metric(Metric{Source: "gke/node-a", Metric: "wireguard.handshake_age", Value: 21, Time: now.Add(-12 * time.Second)})
	b.Metric(Metric{Source: "gke/node-b", Metric: "wireguard.handshake_age", Value: 9, Time: now.Add(-12 * time.Second)})
	b.Event(Event{Time: now.Add(-4 * time.Second)})
	return b
}

func TestStatusTextMatchesTheContractExample(t *testing.T) {
	now := time.Date(2026, 11, 17, 9, 20, 31, 0, time.UTC)
	got := exampleBoard(now).View(now, NewEngine(vms, 3, now)).Text()
	if got != contractExample(t) {
		t.Fatalf("status text:\n%s\nwant:\n%s", got, contractExample(t))
	}
}

func TestStatusJSONCarriesTheSameData(t *testing.T) {
	now := time.Date(2026, 11, 17, 9, 20, 31, 0, time.UTC)
	raw, err := exampleBoard(now).View(now, NewEngine(vms, 3, now)).JSON()
	if err != nil {
		t.Fatal(err)
	}
	var v View
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	if v.Cluster.Brokers != 3 || len(v.Nodes) != 4 || v.Nodes[2].HeapMaxMB != 384 || len(v.GKE) != 2 || v.LastEventAgeSec != 4 {
		t.Fatalf("json view = %+v", v)
	}
}

func TestStatusListsOpenIncidents(t *testing.T) {
	now := time.Date(2026, 11, 17, 9, 20, 31, 0, time.UTC)
	e := NewEngine(vms, 3, now)
	e.Event(Event{Kind: "broker.fenced", State: "down", Subject: "kafka/broker-3", VM: "micro-1", ObservedBy: "a1-1", Time: now.Add(-time.Minute)})
	text := exampleBoard(now).View(now, e).Text()
	if !strings.Contains(text, "DOWN vm/micro-1 since 09:19 — broker 3 fenced (observed by a1-1)") {
		t.Fatalf("incidents missing:\n%s", text)
	}
}
