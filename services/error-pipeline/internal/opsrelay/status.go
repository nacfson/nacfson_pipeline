package opsrelay

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

type Node struct {
	ID         int    `json:"id"`
	VM         string `json:"vm"`
	Broker     bool   `json:"broker"`
	HeapMB     int    `json:"heap_mb"`
	Controller bool   `json:"controller"`
}

func ParseNodes(s string) ([]Node, error) {
	var nodes []Node
	for _, item := range strings.Split(s, ",") {
		var n Node
		var roles string
		if _, err := fmt.Sscanf(strings.ReplaceAll(strings.ReplaceAll(item, "=", " "), ":", " "), "%d %s %s %d", &n.ID, &n.VM, &roles, &n.HeapMB); err != nil {
			return nil, fmt.Errorf("node %q: want id=vm:roles:heapMB", item)
		}
		n.Broker = strings.Contains(roles, "broker")
		n.Controller = strings.Contains(roles, "controller")
		nodes = append(nodes, n)
	}
	return nodes, nil
}

func (b *Board) VMs() []string {
	out := make([]string, 0, len(b.nodes))
	for _, n := range b.nodes {
		out = append(out, n.VM)
	}
	return out
}

func (b *Board) Brokers() int {
	n := 0
	for _, node := range b.nodes {
		if node.Broker {
			n++
		}
	}
	return n
}

type Cluster struct {
	Brokers         int `json:"brokers"`
	Controllers     int `json:"controllers"`
	Leader          int `json:"leader"`
	UnderReplicated int `json:"under_replicated"`
	Offline         int `json:"offline"`
}

type Board struct {
	nodes      []Node
	cluster    Cluster
	slots      map[string]int
	metrics    map[string]map[string]float64
	seen       map[string]time.Time
	lastEvent  time.Time
	lastMetric time.Time
}

func NewBoard(nodes []Node) *Board {
	return &Board{nodes: nodes, slots: map[string]int{}, metrics: map[string]map[string]float64{}, seen: map[string]time.Time{}}
}

func (b *Board) Event(ev Event) { b.lastEvent = ev.Time }

func (b *Board) Metric(m Metric) {
	if b.metrics[m.Source] == nil {
		b.metrics[m.Source] = map[string]float64{}
	}
	b.metrics[m.Source][m.Metric] = m.Value
	b.seen[m.Source] = m.Time
	if m.Time.After(b.lastMetric) {
		b.lastMetric = m.Time
	}
}

func (b *Board) SetCluster(c Cluster) { b.cluster = c }

func (b *Board) Controllers(now time.Time) (alive int, leader int) {
	for _, n := range b.nodes {
		if !n.Controller {
			continue
		}
		src := "vm/" + n.VM
		if now.Sub(b.seen[src]) <= 90*time.Second {
			alive++
		}
		if b.metric(src, "kafka.controller.active") == 1 {
			leader = n.ID
		}
	}
	return alive, leader
}
func (b *Board) SetSlot(node string, slot int)   { b.slots[node] = slot }
func (b *Board) ForgetSlot(node string)          { delete(b.slots, node) }
func (b *Board) metric(src, name string) float64 { return b.metrics[src][name] }

type NodeView struct {
	Role         string  `json:"role"`
	ID           int     `json:"id"`
	VM           string  `json:"vm"`
	ProduceP99Ms float64 `json:"produce_p99_ms,omitempty"`
	BytesInKBs   float64 `json:"bytes_in_kb_s,omitempty"`
	HeapUsedMB   float64 `json:"heap_used_mb"`
	HeapMaxMB    int     `json:"heap_max_mb"`
	DiskFreePct  float64 `json:"disk_free_pct"`
	CertDays     float64 `json:"cert_days_left"`
}

type GKEView struct {
	Node         string  `json:"node"`
	Slot         int     `json:"slot"`
	BufferBytes  float64 `json:"buffer_bytes"`
	HandshakeSec float64 `json:"handshake_age_s"`
}

type View struct {
	At               time.Time  `json:"at"`
	LastEventAgeSec  float64    `json:"last_event_age_s"`
	LastMetricAgeSec float64    `json:"last_metric_age_s"`
	Cluster          Cluster    `json:"cluster"`
	BrokersTotal     int        `json:"brokers_total"`
	ControllersTotal int        `json:"controllers_total"`
	Nodes            []NodeView `json:"nodes"`
	GKE              []GKEView  `json:"gke"`
	Incidents        []string   `json:"incidents"`
}

func (b *Board) View(now time.Time, e *Engine) View {
	v := View{At: now, Cluster: b.cluster, Incidents: []string{}}
	v.LastEventAgeSec = now.Sub(b.lastEvent).Seconds()
	v.LastMetricAgeSec = now.Sub(b.lastMetric).Seconds()
	for _, n := range b.nodes {
		src := "vm/" + n.VM
		role := "ctrl"
		if n.Broker {
			role = "broker"
			v.BrokersTotal++
		} else {
			v.ControllersTotal++
		}
		nv := NodeView{Role: role, ID: n.ID, VM: n.VM, HeapMaxMB: n.HeapMB,
			HeapUsedMB:  b.metric(src, "kafka.heap.used") / (1 << 20),
			DiskFreePct: b.metric(src, "os.disk.free_ratio") * 100,
			CertDays:    b.metric(src, "cert.days_left.min")}
		if n.Broker {
			nv.ProduceP99Ms = b.metric(src, "kafka.produce.latency_p99")
			nv.BytesInKBs = b.metric(src, "kafka.bytes_in") / 1000
		}
		v.Nodes = append(v.Nodes, nv)
	}
	v.ControllersTotal = 3
	nodes := make([]string, 0, len(b.slots))
	for n := range b.slots {
		nodes = append(nodes, n)
	}
	sort.Strings(nodes)
	for _, n := range nodes {
		src := "gke/" + n
		v.GKE = append(v.GKE, GKEView{Node: n, Slot: b.slots[n], BufferBytes: b.metric(src, "fluentbit.buffer.bytes"), HandshakeSec: b.metric(src, "wireguard.handshake_age")})
	}
	if e != nil {
		keys := make([]string, 0, len(e.subjects))
		for k, s := range e.subjects {
			if s.level != OK {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			s := e.subjects[k]
			v.Incidents = append(v.Incidents, fmt.Sprintf("%s %s since %s — %s", s.level, k, s.since.UTC().Format("15:04"), s.text))
		}
	}
	return v
}

func humanBytes(n float64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MiB", n/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KiB", n/(1<<10))
	}
	return fmt.Sprintf("%.0f B", n)
}

func (v View) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "ops-relay status · %s UTC · last event %.0f s ago · last metric %.0f s ago\n",
		v.At.UTC().Format("2006-01-02 15:04:05"), v.LastEventAgeSec, v.LastMetricAgeSec)
	fmt.Fprintf(&b, "cluster   brokers %d/%d   controllers %d/%d (leader %d)   under-replicated %d   offline %d\n",
		v.Cluster.Brokers, v.BrokersTotal, v.Cluster.Controllers, v.ControllersTotal, v.Cluster.Leader, v.Cluster.UnderReplicated, v.Cluster.Offline)
	for _, n := range v.Nodes {
		heap := fmt.Sprintf("heap %.0f/%d MB", n.HeapUsedMB, n.HeapMaxMB)
		tail := fmt.Sprintf("disk %.0f%% free   cert %.0f d", n.DiskFreePct, n.CertDays)
		if n.Role == "broker" {
			fmt.Fprintf(&b, "%-9s %d %-8s %-18s %-12s %-18s %s\n", n.Role, n.ID, n.VM,
				fmt.Sprintf("produce p99 %.0f ms", n.ProduceP99Ms), fmt.Sprintf("in %.0f KB/s", n.BytesInKBs), heap, tail)
		} else {
			fmt.Fprintf(&b, "%-9s %d %-8s %-18s %s\n", n.Role, n.ID, n.VM, heap, tail)
		}
	}
	for _, g := range v.GKE {
		fmt.Fprintf(&b, "%-9s %s slot %d  buffer %s / 256 MiB   handshake %.0f s\n", "gke", g.Node, g.Slot, humanBytes(g.BufferBytes), g.HandshakeSec)
	}
	if len(v.Incidents) == 0 {
		b.WriteString("incidents none open\n")
	} else {
		b.WriteString("incidents\n")
		for _, i := range v.Incidents {
			b.WriteString("  " + i + "\n")
		}
	}
	return b.String()
}

func (v View) JSON() ([]byte, error) { return json.MarshalIndent(v, "", "  ") }
