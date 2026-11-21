package group

import (
	"container/list"
	"sort"
	"sync"
	"time"
	"unicode/utf8"

	"nacfson_pipeline/services/error-pipeline/internal/classify"
	"nacfson_pipeline/services/error-pipeline/internal/fingerprint"
)

const maxSampleBytes = 4 << 10

type Group struct {
	Fingerprint    string         `json:"fingerprint"`
	Project        string         `json:"project"`
	Container      string         `json:"container"`
	Event          classify.Event `json:"event"`
	FirstSeen      time.Time      `json:"first_seen"`
	LastSeen       time.Time      `json:"last_seen"`
	WindowEnd      time.Time      `json:"window_end"`
	CooldownUntil  time.Time      `json:"cooldown_until"`
	Count          int            `json:"count"`
	SinceLastAlert int            `json:"since_last_alert"`
	Pods           []string       `json:"pods"`
	Samples        []string       `json:"samples"`
	Handed         bool           `json:"handed"`
}

type Config struct {
	Window     time.Duration
	Cooldown   time.Duration
	Retention  time.Duration
	MaxSamples int
	MaxEntries int
}

func DefaultConfig() Config {
	return Config{
		Window:     3 * time.Minute,
		Cooldown:   6 * time.Hour,
		Retention:  168 * time.Hour,
		MaxSamples: 20,
	}
}

type ActionType int

const (
	NotifyNew ActionType = iota

	Handoff
)

func (t ActionType) String() string {
	switch t {
	case NotifyNew:
		return "NotifyNew"
	case Handoff:
		return "Handoff"
	}
	return "Unknown"
}

type Action struct {
	Type     ActionType
	Group    Group
	Recurred bool
}

type Tracker struct {
	cfg      Config
	mu       sync.Mutex
	groups   map[string]*Group
	lru      *list.List
	lruIndex map[string]*list.Element
}

func NewTracker(cfg Config) *Tracker {
	return &Tracker{
		cfg:      cfg,
		groups:   map[string]*Group{},
		lru:      list.New(),
		lruIndex: map[string]*list.Element{},
	}
}

func (t *Tracker) touchLocked(fp string) {
	if elem, ok := t.lruIndex[fp]; ok {
		t.lru.MoveToFront(elem)
		return
	}
	t.lruIndex[fp] = t.lru.PushFront(fp)
}

func (t *Tracker) deleteLocked(fp string) {
	delete(t.groups, fp)
	if elem, ok := t.lruIndex[fp]; ok {
		t.lru.Remove(elem)
		delete(t.lruIndex, fp)
	}
}

func (t *Tracker) evictLocked() {
	if t.cfg.MaxEntries <= 0 {
		return
	}
	for len(t.groups) >= t.cfg.MaxEntries {
		back := t.lru.Back()
		if back == nil {
			return
		}
		fp := back.Value.(string)
		t.deleteLocked(fp)
	}
}

func (t *Tracker) Observe(c classify.Classified, now time.Time) []Action {
	fp := fingerprint.Of(c)

	t.mu.Lock()
	defer t.mu.Unlock()

	g, ok := t.groups[fp]
	if !ok {
		t.evictLocked()
		g = &Group{Fingerprint: fp}
		t.groups[fp] = g
		t.touchLocked(fp)
		t.start(g, c, now)
		return []Action{{Type: NotifyNew, Group: clone(*g)}}
	}
	t.touchLocked(fp)

	var actions []Action
	if !g.Handed && !now.Before(g.WindowEnd) {

		actions = append(actions, t.close(g))
	}

	if !g.Handed {
		g.record(c, now, t.cfg.MaxSamples)
		return actions
	}

	if now.Before(g.CooldownUntil) {
		g.Count++
		g.SinceLastAlert++
		g.LastSeen = now
		return actions
	}

	since := g.SinceLastAlert
	t.start(g, c, now)
	alert := clone(*g)
	alert.SinceLastAlert = since
	return append(actions, Action{Type: NotifyNew, Group: alert, Recurred: true})
}

func (t *Tracker) Tick(now time.Time) []Action {
	t.mu.Lock()
	defer t.mu.Unlock()

	var due []string
	for fp, g := range t.groups {
		if !g.Handed && !now.Before(g.WindowEnd) {
			due = append(due, fp)
		}
	}
	sort.Strings(due)
	actions := make([]Action, 0, len(due))
	for _, fp := range due {
		actions = append(actions, t.close(t.groups[fp]))
	}

	for fp, g := range t.groups {
		if g.Handed && t.cfg.Retention > 0 && g.LastSeen.Add(t.cfg.Retention).Before(now) {
			t.deleteLocked(fp)
		}
	}
	return actions
}

func (t *Tracker) Export() []Group {
	t.mu.Lock()
	defer t.mu.Unlock()

	out := make([]Group, 0, len(t.groups))
	for _, g := range t.groups {
		out = append(out, clone(*g))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Fingerprint < out[j].Fingerprint })
	return out
}

func (t *Tracker) Import(gs []Group) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.groups = make(map[string]*Group, len(gs))
	t.lru = list.New()
	t.lruIndex = make(map[string]*list.Element, len(gs))
	ordered := make([]Group, len(gs))
	copy(ordered, gs)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].LastSeen.Before(ordered[j].LastSeen) })
	for _, g := range ordered {
		t.evictLocked()
		cp := clone(g)
		t.groups[cp.Fingerprint] = &cp
		t.touchLocked(cp.Fingerprint)
	}
}

func (t *Tracker) start(g *Group, c classify.Classified, now time.Time) {
	g.Project = c.Record.Project
	g.Container = c.Record.Container
	g.Event = c.Event
	g.FirstSeen = now
	g.LastSeen = now
	g.WindowEnd = now.Add(t.cfg.Window)
	g.CooldownUntil = time.Time{}
	g.Count = 0
	g.SinceLastAlert = 0
	g.Handed = false
	g.Pods = nil
	g.Samples = nil
	g.record(c, now, t.cfg.MaxSamples)
}

func (t *Tracker) close(g *Group) Action {
	g.Handed = true
	g.CooldownUntil = g.WindowEnd.Add(t.cfg.Cooldown)
	return Action{Type: Handoff, Group: clone(*g)}
}

func (g *Group) record(c classify.Classified, now time.Time, maxSamples int) {
	g.Count++
	g.LastSeen = now
	g.addPod(c.Record.Pod)
	if len(g.Samples) < maxSamples {
		g.Samples = append(g.Samples, truncate(c.Record.Message, maxSampleBytes))
	}
}

func (g *Group) addPod(pod string) {
	if pod == "" {
		return
	}
	i := sort.SearchStrings(g.Pods, pod)
	if i < len(g.Pods) && g.Pods[i] == pod {
		return
	}
	g.Pods = append(g.Pods, "")
	copy(g.Pods[i+1:], g.Pods[i:])
	g.Pods[i] = pod
}

func clone(g Group) Group {
	g.Pods = append([]string{}, g.Pods...)
	g.Samples = append([]string{}, g.Samples...)
	return g
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
