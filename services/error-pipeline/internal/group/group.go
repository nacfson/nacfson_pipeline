// Package group tracks error fingerprints through their notification
// lifecycle: an immediate first alert, a fixed collection window, a handoff
// of the collected group, and a cooldown during which repeats are only
// counted. Time comes only from the arguments the caller passes in.
package group

import (
	"sort"
	"sync"
	"time"
	"unicode/utf8"

	"nacfson_pipeline/services/error-pipeline/internal/classify"
	"nacfson_pipeline/services/error-pipeline/internal/fingerprint"
)

// maxSampleBytes bounds one stored sample.
const maxSampleBytes = 4 << 10

// Group is the state of one fingerprint. Its JSON form is the ErrorGroup
// payload that the investigator service accepts (POST /v1/investigate), so
// the tags are a contract.
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

// Config sets the lifecycle timings.
type Config struct {
	Window     time.Duration // collection window, fixed from the first occurrence
	Cooldown   time.Duration // quiet period after the window closes
	Retention  time.Duration // how long an idle, handed-off group is kept
	MaxSamples int           // raw log lines kept per window
}

// DefaultConfig is the 3 minute window, 6 hour cooldown, 7 day retention and
// 20 samples of the error pipeline design.
func DefaultConfig() Config {
	return Config{
		Window:     3 * time.Minute,
		Cooldown:   6 * time.Hour,
		Retention:  168 * time.Hour,
		MaxSamples: 20,
	}
}

// ActionType says what the caller must do with an Action.
type ActionType int

const (
	// NotifyNew asks for an immediate first alert (or a recurrence alert).
	NotifyNew ActionType = iota
	// Handoff passes the collected group on once its window closed.
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

// Action is one thing the caller has to do. Group is a deep copy. For a
// recurrence NotifyNew (Recurred true), Group.SinceLastAlert is the number of
// occurrences counted since the previous alert; the tracker itself restarts
// that counter at zero.
type Action struct {
	Type     ActionType
	Group    Group
	Recurred bool
}

// Tracker holds the groups. It is safe for concurrent use.
type Tracker struct {
	cfg    Config
	mu     sync.Mutex
	groups map[string]*Group
}

// NewTracker returns an empty tracker.
func NewTracker(cfg Config) *Tracker {
	return &Tracker{cfg: cfg, groups: map[string]*Group{}}
}

// Observe records one classified error at time now and returns the actions it
// causes.
func (t *Tracker) Observe(c classify.Classified, now time.Time) []Action {
	fp := fingerprint.Of(c)

	t.mu.Lock()
	defer t.mu.Unlock()

	g, ok := t.groups[fp]
	if !ok {
		g = &Group{Fingerprint: fp}
		t.groups[fp] = g
		t.start(g, c, now)
		return []Action{{Type: NotifyNew, Group: clone(*g)}}
	}

	var actions []Action
	if !g.Handed && !now.Before(g.WindowEnd) {
		// The window ran out before any Tick closed it. Close it now, so the
		// handoff does not include this later occurrence.
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

	// Cooldown expired: alert again and start a new cycle.
	since := g.SinceLastAlert
	t.start(g, c, now)
	alert := clone(*g)
	alert.SinceLastAlert = since
	return append(actions, Action{Type: NotifyNew, Group: alert, Recurred: true})
}

// Tick closes every window that ended at or before now, returning a Handoff
// for each (ordered by fingerprint), then drops handed-off groups idle for
// longer than the retention.
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
		if g.Handed && g.LastSeen.Add(t.cfg.Retention).Before(now) {
			delete(t.groups, fp)
		}
	}
	return actions
}

// Export returns a deep copy of every group, sorted by fingerprint.
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

// Import replaces all state with gs.
func (t *Tracker) Import(gs []Group) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.groups = make(map[string]*Group, len(gs))
	for _, g := range gs {
		cp := clone(g)
		t.groups[cp.Fingerprint] = &cp
	}
}

// start (re)initialises g as a fresh cycle whose first occurrence is c at now.
// The caller holds t.mu.
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

// close ends g's window and starts its cooldown. The caller holds t.mu.
func (t *Tracker) close(g *Group) Action {
	g.Handed = true
	g.CooldownUntil = g.WindowEnd.Add(t.cfg.Cooldown)
	return Action{Type: Handoff, Group: clone(*g)}
}

// record counts an occurrence inside an open window.
func (g *Group) record(c classify.Classified, now time.Time, maxSamples int) {
	g.Count++
	g.LastSeen = now
	g.addPod(c.Record.Pod)
	if len(g.Samples) < maxSamples {
		g.Samples = append(g.Samples, truncate(c.Record.Message, maxSampleBytes))
	}
}

// addPod keeps Pods deduplicated and sorted.
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

// clone deep-copies g's slices. Nil slices become empty so the JSON form is
// always an array.
func clone(g Group) Group {
	g.Pods = append([]string{}, g.Pods...)
	g.Samples = append([]string{}, g.Samples...)
	return g
}

// truncate cuts s to at most max bytes without splitting a UTF-8 rune.
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
