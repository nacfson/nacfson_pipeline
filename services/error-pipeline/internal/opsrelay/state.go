package opsrelay

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

type Level string

const (
	OK   Level = "OK"
	Down Level = "DOWN"
	Warn Level = "WARN"
)

const (
	HeartbeatSilence = 3 * time.Minute
	StartupWait      = 3 * time.Minute
	UnreachableAfter = 60 * time.Second
	UnreachableKey   = "cluster/unreachable"
)

type Event struct {
	Schema     int       `json:"schema"`
	Kind       string    `json:"kind"`
	State      string    `json:"state"`
	Subject    string    `json:"subject"`
	VM         string    `json:"vm"`
	ObservedBy string    `json:"observed_by"`
	Source     string    `json:"source"`
	Detail     string    `json:"detail,omitempty"`
	Time       time.Time `json:"-"`
}

type Metric struct {
	Schema int       `json:"schema"`
	Source string    `json:"source"`
	Metric string    `json:"metric"`
	Value  float64   `json:"value"`
	Unit   string    `json:"unit"`
	Time   time.Time `json:"-"`
}

type Message struct {
	Level   Level
	Subject string
	Text    string
	At      time.Time
	Delayed bool
	Direct  bool
	Tag     string
}

func (m Message) String() string {
	prefix := ""
	if m.Delayed {
		prefix = fmt.Sprintf("(delayed, event at %s UTC) ", m.At.UTC().Format("15:04"))
	}
	tag := m.Tag
	if tag == "" {
		tag = "kafka"
	}
	return fmt.Sprintf("%s[%s] %-5s %s — %s", prefix, tag, m.Level, m.Subject, m.Text)
}

type subject struct {
	level Level
	since time.Time
	text  string
	sent  Level
	tag   string
}

type incident struct {
	opened     time.Time
	causes     map[string]string
	partitions map[string]bool
}

type Engine struct {
	vms     []string
	brokers int
	started time.Time

	subjects      map[string]*subject
	incidents     map[string]*incident
	heartbeats    map[string]time.Time
	silent        map[string]bool
	connectedAt   time.Time
	disconnected  time.Time
	unreachable   bool
	brokersActive map[string]float64
	trends        *Trends
	tunnels       map[string]Level
	slotSince     map[string]time.Time

	replaying bool
	touched   map[string]bool
	out       []Message
}

func NewEngine(vms []string, brokers int, now time.Time) *Engine {
	return &Engine{
		vms:           append([]string(nil), vms...),
		brokers:       brokers,
		started:       now,
		subjects:      map[string]*subject{},
		incidents:     map[string]*incident{},
		heartbeats:    map[string]time.Time{},
		silent:        map[string]bool{},
		connectedAt:   now,
		brokersActive: map[string]float64{},
		trends:        NewTrends(),
		tunnels:       map[string]Level{},
		slotSince:     map[string]time.Time{},
	}
}

func levelOf(state string) Level {
	switch state {
	case "down":
		return Down
	case "warn":
		return Warn
	}
	return OK
}

func vmSubject(vm string) string { return "vm/" + vm }

func (e *Engine) get(key string) *subject {
	s, ok := e.subjects[key]
	if !ok {
		s = &subject{level: OK, sent: OK}
		e.subjects[key] = s
	}
	return s
}

func (e *Engine) set(key string, level Level, text string, at time.Time, direct bool) {
	e.setTagged(key, "kafka", level, text, at, direct)
}

func (e *Engine) setTagged(key, tag string, level Level, text string, at time.Time, direct bool) {
	s := e.get(key)
	if s.level == level {
		return
	}
	s.level, s.since, s.text, s.tag = level, at, text, tag
	if e.replaying {
		e.touched[key] = true
		return
	}
	if s.sent == level {
		return
	}
	s.sent = level
	e.out = append(e.out, Message{Level: level, Subject: key, Text: text, At: at, Direct: direct, Tag: tag})
}

func isObservabilityUnit(unit string) bool {
	switch unit {
	case "grafana.service", "vector.service", "loki.service", "error-detector.service":
		return true
	}
	return false
}

func (e *Engine) Drain() []Message {
	out := e.out
	e.out = nil
	return out
}

func (e *Engine) openIncident(vm string) *incident {
	inc, ok := e.incidents[vm]
	if !ok {
		inc = &incident{causes: map[string]string{}, partitions: map[string]bool{}}
		e.incidents[vm] = inc
	}
	return inc
}

func (e *Engine) anyIncident() *incident {
	keys := make([]string, 0, len(e.incidents))
	for k := range e.incidents {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if len(e.incidents[k].causes) > 0 {
			return e.incidents[k]
		}
	}
	return nil
}

func (e *Engine) causeDown(vm, cause, text string, at time.Time) {
	inc := e.openIncident(vm)
	if len(inc.causes) == 0 {
		inc.opened = at
	}
	inc.causes[cause] = text
	if e.get(vmSubject(vm)).level != Down {
		e.set(vmSubject(vm), Down, e.incidentText(vm, inc, text), at, false)
	}
}

func (e *Engine) causeUp(vm, cause string, at time.Time) {
	inc, ok := e.incidents[vm]
	if !ok {
		return
	}
	delete(inc.causes, cause)
	if len(inc.causes) > 0 {
		return
	}
	minutes := int(at.Sub(inc.opened).Round(time.Minute) / time.Minute)
	text := fmt.Sprintf("recovered after %d min", minutes)
	if open := e.openPartitions(); open > 0 {
		text += fmt.Sprintf(" · %d partitions still below 3/3", open)
	} else {
		text += " · all partitions 3/3"
	}
	delete(e.incidents, vm)
	e.set(vmSubject(vm), OK, text, at, false)
}

func (e *Engine) incidentText(vm string, inc *incident, text string) string {
	if n := len(inc.partitions); n > 0 {
		return fmt.Sprintf("%s · %d partitions with 2/3 copies", text, n)
	}
	return text
}

func (e *Engine) openPartitions() int {
	n := 0
	for key, s := range e.subjects {
		if strings.HasPrefix(key, "kafka/partition/") && s.level == Down {
			n++
		}
	}
	return n
}

func (e *Engine) Event(ev Event) {
	at := ev.Time
	switch {
	case ev.Kind == "agent.heartbeat":
		e.heartbeats[ev.VM] = at
		if e.silent[ev.VM] {
			delete(e.silent, ev.VM)
			e.causeUp(ev.VM, "silence", at)
		}
	case strings.HasPrefix(ev.Kind, "broker."):
		if levelOf(ev.State) == Down {
			e.causeDown(ev.VM, ev.Subject, fmt.Sprintf("%s fenced (observed by %s)", strings.Replace(strings.TrimPrefix(ev.Subject, "kafka/"), "-", " ", 1), ev.ObservedBy), at)
		} else {
			e.causeUp(ev.VM, ev.Subject, at)
		}
	case strings.HasPrefix(ev.Kind, "unit."):
		unit := ev.Subject[strings.LastIndex(ev.Subject, "/")+1:]
		if isObservabilityUnit(unit) {
			obsKey := "obs/" + ev.VM + "/" + unit
			if levelOf(ev.State) == Down || ev.Kind == "unit.failed" {
				e.setTagged(obsKey, "obs", Down, fmt.Sprintf("%s left active", unit), at, false)
			} else {
				e.setTagged(obsKey, "obs", OK, fmt.Sprintf("%s is active", unit), at, false)
			}
			return
		}
		if levelOf(ev.State) == Down {
			e.causeDown(ev.VM, ev.Subject, fmt.Sprintf("%s left active", unit), at)
		} else {
			e.causeUp(ev.VM, ev.Subject, at)
		}
	case ev.Kind == "loki.disk.low" || ev.Kind == "loki.disk.ok":
		subj := ev.Subject
		if subj == "" {
			subj = "loki/disk"
		}
		obsKey := "obs/" + ev.VM + "/" + subj
		if ev.Kind == "loki.disk.low" {
			text := shortDetail(ev.Detail)
			if text == "" {
				text = "loki disk free space below 15%"
			}
			e.setTagged(obsKey, "obs", Warn, text, at, false)
		} else {
			text := shortDetail(ev.Detail)
			if text == "" {
				text = "loki disk free space recovered"
			}
			e.setTagged(obsKey, "obs", OK, text, at, false)
		}
	case strings.HasPrefix(ev.Kind, "partition."):
		level := levelOf(ev.State)
		if inc := e.anyIncident(); inc != nil {
			s := e.get(ev.Subject)
			s.level, s.since = level, at
			if level == Down {
				inc.partitions[ev.Subject] = true
			} else {
				delete(inc.partitions, ev.Subject)
			}
			return
		}
		e.set(ev.Subject, level, fmt.Sprintf("in-sync replicas changed (observed by %s)", ev.ObservedBy), at, false)
	default:
		e.set(ev.Subject, levelOf(ev.State), strings.TrimSpace(ev.Kind+" "+shortDetail(ev.Detail)), at, false)
	}
}

func shortDetail(d string) string {
	if len(d) > 160 {
		return d[:160] + "…"
	}
	return d
}

func (e *Engine) Metric(m Metric) {
	if m.Metric == "kafka.brokers.active" {
		e.brokersActive[m.Source] = m.Value
	}
	e.trends.Add(m)
	for _, v := range e.trends.Verdicts(m.Time) {
		e.set(v.Subject, v.Level, v.Text, m.Time, false)
	}
}

func (e *Engine) KubeEvent(reason, node, message string, at time.Time) {
	switch reason {
	case "TunnelStale", "TunnelRecovered", "NoFreeSlot":
		if since, ok := e.slotSince[node]; ok && at.Before(since) {
			return
		}
	}
	switch reason {
	case "TunnelStale", "TunnelRecovered":
		level := OK
		if reason == "TunnelStale" {
			level = Down
		}
		key := "gke/" + node + "/tunnel"
		e.tunnels[key] = level
		if e.unreachable {
			s := e.get(key)
			s.level, s.since = level, at
			return
		}
		e.set(key, level, message, at, false)
	case "NoFreeSlot":
		e.set("gke/"+node+"/slot", Warn, message, at, false)
	case "DeployOK", "DeployFailed", "DeployBlocked":
		e.kubeDeploy(reason, message, at)
	}
}

func (e *Engine) NodeHoldsSlot(node string, slot int, since, at time.Time) {
	if !since.IsZero() {
		e.slotSince[node] = since
	}
	e.set("gke/"+node+"/slot", OK, fmt.Sprintf("holds slot %d", slot), at, false)
}

func (e *Engine) NodeLeft(node string, at time.Time) {
	e.slotSince[node] = at
	e.trends.Forget(node)
	delete(e.tunnels, "gke/"+node+"/tunnel")
	for _, part := range []string{"slot", "tunnel", "buffer"} {
		e.set("gke/"+node+"/"+part, OK, "node no longer holds a slot", at, false)
	}
}

func (e *Engine) kubeDeploy(reason, message string, at time.Time) {
	playbook := strings.Fields(message + " ?")[0]
	level := OK
	if reason != "DeployOK" {
		level = Down
	}
	e.set("deploy/"+playbook, level, message, at, false)
}

func (e *Engine) Connected(at time.Time) {
	e.connectedAt = at
	e.disconnected = time.Time{}
	if e.unreachable {
		e.unreachable = false
		e.set(UnreachableKey, OK, "Kafka reachable again from GKE", at, true)
		for key, level := range e.tunnels {
			s := e.get(key)
			if s.sent != level && !e.replaying {
				s.sent = level
				e.out = append(e.out, Message{Level: level, Subject: key, Text: "tunnel state while Kafka was unreachable", At: at})
			}
		}
		for vm, hb := range e.heartbeats {
			if at.Sub(hb) < HeartbeatSilence {
				continue
			}
			e.heartbeats[vm] = at
		}
	}
}

func (e *Engine) Disconnected(at time.Time) {
	if e.disconnected.IsZero() {
		e.disconnected = at
	}
}

func (e *Engine) brokerDropped() bool {
	for _, v := range e.brokersActive {
		if int(v) < e.brokers {
			return true
		}
	}
	return false
}

func (e *Engine) Tick(now time.Time) {
	if !e.disconnected.IsZero() && !e.unreachable && now.Sub(e.disconnected) >= UnreachableAfter {
		e.unreachable = true
		e.set(UnreachableKey, Down, "no working Kafka connection from GKE for 60 s (sent directly)", now, true)
	}
	if e.unreachable || now.Sub(e.started) < StartupWait {
		return
	}
	for _, vm := range e.vms {
		last, ok := e.heartbeats[vm]
		if !ok {
			last = e.started
		}
		if e.silent[vm] || now.Sub(last) < HeartbeatSilence {
			continue
		}
		e.silent[vm] = true
		label := "agent silent, broker healthy"
		if e.brokerDropped() {
			label = "broker down"
		}
		e.causeDown(vm, "silence", label+" (no heartbeat for 3 min)", now)
	}
}
