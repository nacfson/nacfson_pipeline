package detector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"nacfson_pipeline/services/error-pipeline/internal/classify"
	"nacfson_pipeline/services/error-pipeline/internal/group"
	"nacfson_pipeline/services/error-pipeline/internal/notify"
	"nacfson_pipeline/services/error-pipeline/internal/state"
	"nacfson_pipeline/services/error-pipeline/internal/vlogs"
)

const (
	channelErrors = "errors"

	burstPerMinute = 10

	queueSize = 100

	handoffQueue = 100

	handoffOverflow = 32

	tailOffset = 5 * time.Second

	crashSilence = 2 * time.Second

	catchUpMax = 15 * time.Minute

	saveInterval = 30 * time.Second

	tailReadyWindow = 30 * time.Second

	probeTimeout = 10 * time.Second

	sendTimeout = 90 * time.Second

	handoffTimeout = 10 * time.Second

	backoffMin = time.Second
	backoffMax = 30 * time.Second

	handoffAttempts = 3

	skipNoticeBatch = 10
)

var (
	shutdownDrain  = 15 * time.Second
	shutdownNotify = 20 * time.Second
)

var tickInterval = time.Second

var retryInterval = 30 * time.Second

var handoffBackoff = [...]time.Duration{time.Second, 2 * time.Second}

type Config struct {
	VLogs           *vlogs.Client
	Notify          *notify.Client
	InvestigatorURL string
	StatePath       string
	Group           group.Config
	Now             func() time.Time
}

type outbox struct {
	notifications []notify.Notification
	handoffs      []group.Group
}

type Detector struct {
	cfg Config
	log *slog.Logger

	asm      *classify.Assembler
	tracker  *group.Tracker
	queue    *notify.Queue
	limiter  *notify.Limiter
	handoffs chan group.Group
	http     *http.Client
	mux      http.Handler

	started  atomic.Bool
	tailing  atomic.Bool
	tailSeen atomic.Int64

	wake       chan struct{}
	senderDone chan struct{}

	handoffWake chan struct{}
	handoffDone chan struct{}

	handoffMu sync.Mutex
	spill     []group.Group

	skipMu     sync.Mutex
	pinned     []group.Group
	covered    []group.Group
	unresolved map[string]struct{}

	skipRun sync.Mutex

	mu         sync.Mutex
	cursor     time.Time
	burstSince time.Time
	lastSave   time.Time

	quarantineMu sync.Mutex
	quarantine   map[string]*quarantineState

	violationsMu sync.Mutex
	violations   map[string]map[string]int
	lastDigest   time.Time
}

type quarantineState struct {
	firstAlertSent bool
	lastAlert      time.Time
	pendingCount   int
	firstLine      string
}

var (
	redactionMarkerRe = regexp.MustCompile(`\[REDACTED:([a-z0-9_-]+)\]`)
	dynamicKeyRe      = regexp.MustCompile(`\d{4,}|[0-9a-fA-F]{32}`)
)

func settleKey(g group.Group) string {
	return g.Fingerprint + "@" + strconv.FormatInt(g.WindowEnd.UnixNano(), 10)
}

func New(cfg Config) (*Detector, error) {
	if cfg.VLogs == nil {
		return nil, errors.New("detector: VLogs client is required")
	}
	if cfg.Notify == nil {
		return nil, errors.New("detector: Notify client is required")
	}
	if strings.TrimSpace(cfg.StatePath) == "" {
		return nil, errors.New("detector: StatePath is required")
	}
	cfg.InvestigatorURL = strings.TrimSpace(cfg.InvestigatorURL)
	if err := validateInvestigatorURL(cfg.InvestigatorURL); err != nil {
		return nil, err
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Group == (group.Config{}) {
		cfg.Group = group.DefaultConfig()
	}

	d := &Detector{
		cfg:         cfg,
		log:         slog.Default(),
		asm:         classify.NewAssembler(0, crashSilence),
		tracker:     group.NewTracker(cfg.Group),
		limiter:     notify.NewLimiter(burstPerMinute),
		handoffs:    make(chan group.Group, handoffQueue),
		wake:        make(chan struct{}, 1),
		senderDone:  make(chan struct{}),
		handoffWake: make(chan struct{}, 1),
		handoffDone: make(chan struct{}),
		unresolved:  map[string]struct{}{},
		http:        &http.Client{Timeout: handoffTimeout},
		quarantine:  make(map[string]*quarantineState),
		violations:  make(map[string]map[string]int),
		lastDigest:  cfg.Now(),
	}
	d.queue = notify.NewQueue(queueSize, func(ctx context.Context, n notify.Notification) error {
		ctx, cancel := context.WithTimeout(ctx, sendTimeout)
		defer cancel()
		return cfg.Notify.Send(ctx, channelErrors, n)
	}, func(format string, args ...any) {
		d.log.Warn(fmt.Sprintf(format, args...))
	})
	d.mux = d.buildMux()
	return d, nil
}

func validateInvestigatorURL(raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("detector: InvestigatorURL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("detector: InvestigatorURL %q must be http or https", raw)
	}
	if u.Host == "" {
		return fmt.Errorf("detector: InvestigatorURL %q has no host", raw)
	}
	return nil
}

func (d *Detector) Mux() http.Handler { return d.mux }

func (d *Detector) buildMux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /startupz", d.handleStartupz)
	mux.HandleFunc("GET /readyz", d.handleReadyz)
	mux.HandleFunc("GET /healthz", d.handleHealthz)
	return mux
}

func (d *Detector) handleStartupz(w http.ResponseWriter, _ *http.Request) {
	if !d.started.Load() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "state not loaded yet"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "OK", "checks": "state loaded"})
}

func (d *Detector) handleReadyz(w http.ResponseWriter, r *http.Request) {
	if !d.started.Load() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "state not loaded yet"})
		return
	}
	if !d.tailLive() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "no live VictoriaLogs tail"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), probeTimeout)
	defer cancel()
	if err := d.cfg.VLogs.Ready(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "READY"})
}

func (d *Detector) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ALIVE"})
}

func (d *Detector) tailLive() bool {
	if !d.tailing.Load() {
		return false
	}
	seen := d.tailSeen.Load()
	if seen == 0 {
		return false
	}
	return time.Since(time.Unix(0, seen)) <= tailReadyWindow
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (d *Detector) Run(ctx context.Context) error {
	d.loadState()
	d.sweepTemp()
	d.started.Store(true)

	collected := make(chan struct{})
	go func() {
		defer close(collected)
		d.collect(ctx)
	}()
	quarantineDone := make(chan struct{})
	go func() {
		defer close(quarantineDone)
		d.collectQuarantine(ctx)
	}()
	go d.sender(ctx)
	go d.handoffWorker(ctx)

	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():

			<-collected
			<-quarantineDone

			<-d.senderDone
			<-d.handoffDone
			return d.shutdown()
		case <-ticker.C:
			d.tick()
		}
	}
}

func (d *Detector) sender(ctx context.Context) {
	defer close(d.senderDone)
	ticker := time.NewTicker(retryInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-d.wake:
			d.deliverSkips(ctx)
		case <-ticker.C:
			if d.queue.Len() > 0 || d.hasStagedSkips() {
				d.deliverSkips(ctx)
			}
		}
	}
}

func (d *Detector) wakeSender() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

func (d *Detector) loadState() {
	snap, err := state.Load(d.cfg.StatePath)
	if err != nil {
		d.log.Error("state unreadable, starting fresh", "path", d.cfg.StatePath, "error", err.Error())
		return
	}
	d.tracker.Import(snap.Groups)
	d.mu.Lock()
	d.cursor = snap.Cursor
	d.mu.Unlock()
	d.log.Info("state loaded", "path", d.cfg.StatePath,
		"cursor", snap.Cursor.UTC().Format(time.RFC3339Nano), "groups", len(snap.Groups))
}

func (d *Detector) sweepTemp() {
	matches, err := filepath.Glob(d.cfg.StatePath + ".tmp-*")
	if err != nil {
		d.log.Warn("cannot list stale state files", "error", err.Error())
		return
	}
	for _, m := range matches {
		if err := os.Remove(m); err != nil && !errors.Is(err, fs.ErrNotExist) {
			d.log.Warn("cannot remove stale state file", "path", m, "error", err.Error())
			continue
		}
		d.log.Info("removed stale state file", "path", m)
	}
}

func (d *Detector) collect(ctx context.Context) {
	backoff := backoffMin
	for {
		if ctx.Err() != nil {
			return
		}
		d.catchUp(ctx)
		if ctx.Err() != nil {
			return
		}

		d.tailing.Store(true)
		d.markTailSeen()
		err := d.cfg.VLogs.Tail(ctx, d.ingest)
		d.tailing.Store(false)
		if ctx.Err() != nil {
			return
		}
		d.log.Warn("tail ended, reconnecting", "error", errorString(err), "in", backoff.String())
		if !sleep(ctx, backoff) {
			return
		}
		if backoff *= 2; backoff > backoffMax {
			backoff = backoffMax
		}
	}
}

func (d *Detector) collectQuarantine(ctx context.Context) {
	backoff := backoffMin
	for {
		if ctx.Err() != nil {
			return
		}
		err := d.cfg.VLogs.TailQuarantine(ctx, d.ingestQuarantine)
		if ctx.Err() != nil {
			return
		}
		d.log.Warn("quarantine tail ended, reconnecting", "error", errorString(err), "in", backoff.String())
		if !sleep(ctx, backoff) {
			return
		}
		if backoff *= 2; backoff > backoffMax {
			backoff = backoffMax
		}
	}
}

func (d *Detector) IngestQuarantine(q vlogs.QuarantineRecord) error {
	return d.ingestQuarantine(q)
}

func (d *Detector) ingestQuarantine(q vlogs.QuarantineRecord) error {
	now := d.now()
	key := q.Project + "/" + q.Container

	d.recordViolation(q.Project, q.Container, "quarantined-line")

	d.quarantineMu.Lock()
	st := d.quarantine[key]
	if st == nil {
		st = &quarantineState{}
		d.quarantine[key] = st
	}

	var toAlert bool
	var count int
	var line string

	if !st.firstAlertSent || now.Sub(st.lastAlert) >= time.Hour {
		toAlert = true
		count = st.pendingCount + 1
		line = q.Message
		if st.firstLine != "" {
			line = st.firstLine
		}
		st.firstAlertSent = true
		st.lastAlert = now
		st.pendingCount = 0
		st.firstLine = ""
	} else {
		st.pendingCount++
		if st.firstLine == "" {
			st.firstLine = q.Message
		}
	}
	d.quarantineMu.Unlock()

	if toAlert {
		n := notify.RenderQuarantineAlert(q.Project, q.Container, count, line, q.Time)
		d.dispatch(outbox{notifications: []notify.Notification{n}})
	}
	return nil
}

func (d *Detector) catchUp(ctx context.Context) {
	d.mu.Lock()
	cursor := d.cursor
	d.mu.Unlock()
	if cursor.IsZero() {
		return
	}
	now := d.now()
	from := cursor
	if oldest := now.Add(-catchUpMax); from.Before(oldest) {
		from = oldest
	}
	if !from.Before(now) {
		return
	}
	if err := d.cfg.VLogs.CatchUp(ctx, from, now, d.ingest); err != nil && ctx.Err() == nil {
		d.log.Warn("catch-up failed, tailing anyway",
			"from", from.UTC().Format(time.RFC3339Nano), "error", err.Error())
		return
	}
	d.log.Info("catch-up complete", "from", from.UTC().Format(time.RFC3339Nano))
}

func (d *Detector) Ingest(r classify.Record) error {
	return d.ingest(r)
}

func (d *Detector) ingest(r classify.Record) error {
	now := d.now()
	var out outbox

	markers := findRedactionMarkers(r.Message)
	if r.Msg != "" && r.Msg != r.Message {
		markers = append(markers, findRedactionMarkers(r.Msg)...)
	}
	if r.ExceptionMessage != "" {
		markers = append(markers, findRedactionMarkers(r.ExceptionMessage)...)
	}
	if r.Stacktrace != "" {
		markers = append(markers, findRedactionMarkers(r.Stacktrace)...)
	}
	if r.ExceptionType != "" {
		markers = append(markers, findRedactionMarkers(r.ExceptionType)...)
	}
	seenMarker := make(map[string]bool)
	for _, kind := range markers {
		if !seenMarker[kind] {
			seenMarker[kind] = true
			n := notify.RenderRedactionMarkerAlert(r.Project, r.Container, r.ServiceVersion, kind, r.Time)
			out.notifications = append(out.notifications, n)
		}
	}

	if r.TimeRejected {
		d.recordViolation(r.Project, r.Container, "time-rejected")
	}
	if r.DynamicKey || hasDynamicKey(r.Message) {
		d.recordViolation(r.Project, r.Container, "dynamic-key")
	}

	d.mu.Lock()
	if r.Time.After(d.cursor) {
		d.cursor = r.Time
	}
	for _, c := range d.asm.Feed(r) {
		d.absorb(c, now, &out)
	}
	d.mu.Unlock()

	d.dispatch(out)
	return nil
}

func (d *Detector) absorb(c classify.Classified, now time.Time, out *outbox) {
	for _, a := range d.tracker.Observe(c, now) {
		d.route(a, now, out)
	}
}

func (d *Detector) route(a group.Action, now time.Time, out *outbox) {
	switch a.Type {
	case group.NotifyNew:

		if d.allowBurst(now, out) {
			out.notifications = append(out.notifications, notify.RenderNewAction(a))
		}
	case group.Handoff:
		if d.cfg.InvestigatorURL == "" {
			d.log.Info("handoff disabled, group not investigated", "fingerprint", a.Group.Fingerprint)
			return
		}

		d.markUnresolved(a.Group)
		out.handoffs = append(out.handoffs, a.Group)
	default:
		d.log.Warn("unknown group action", "type", a.Type.String())
	}
}

func (d *Detector) allowBurst(now time.Time, out *outbox) bool {
	if d.burstSince.IsZero() {
		d.burstSince = now
	} else if now.Sub(d.burstSince) >= time.Minute {
		if n := d.limiter.TakeOverflow(); n > 0 {
			out.notifications = append(out.notifications, notify.RenderOverflow(n, d.burstSince))
		}
		d.burstSince = now
	}
	return d.limiter.Allow(now)
}

func (d *Detector) takeOverflow(now time.Time, force bool) (notify.Notification, bool) {
	if d.burstSince.IsZero() {
		return notify.Notification{}, false
	}
	if !force && now.Sub(d.burstSince) < time.Minute {
		return notify.Notification{}, false
	}
	n := d.limiter.TakeOverflow()
	if n == 0 {
		return notify.Notification{}, false
	}
	return notify.RenderOverflow(n, d.burstSince), true
}

func (d *Detector) tick() {
	now := d.now()
	var (
		out  outbox
		snap state.Snapshot
		save bool
	)

	d.mu.Lock()

	for _, c := range d.asm.Flush(now.Add(-tailOffset)) {
		d.absorb(c, now, &out)
	}
	for _, a := range d.tracker.Tick(now) {
		d.route(a, now, &out)
	}
	if n, ok := d.takeOverflow(now, false); ok {
		out.notifications = append(out.notifications, n)
	}
	if d.lastSave.IsZero() || now.Sub(d.lastSave) >= saveInterval {
		save = true
		d.lastSave = now
		snap = state.Snapshot{Cursor: d.cursor, Groups: d.snapshotGroups()}
	}
	d.mu.Unlock()

	d.quarantineMu.Lock()
	for key, st := range d.quarantine {
		if st.pendingCount > 0 && now.Sub(st.lastAlert) >= time.Hour {
			parts := strings.SplitN(key, "/", 2)
			proj, cont := parts[0], ""
			if len(parts) > 1 {
				cont = parts[1]
			}
			line := st.firstLine
			if line == "" {
				line = "(multiple quarantined lines)"
			}
			n := notify.RenderQuarantineAlert(proj, cont, st.pendingCount, line, now)
			out.notifications = append(out.notifications, n)
			st.lastAlert = now
			st.pendingCount = 0
			st.firstLine = ""
		}
	}
	d.quarantineMu.Unlock()

	d.violationsMu.Lock()
	if now.Sub(d.lastDigest) >= 24*time.Hour {
		since := d.lastDigest
		d.lastDigest = now
		if len(d.violations) > 0 {
			snapshot := make(map[string]map[string]int)
			for pc, kinds := range d.violations {
				snapshot[pc] = make(map[string]int)
				for k, v := range kinds {
					snapshot[pc][k] = v
				}
			}
			d.violations = make(map[string]map[string]int)
			d.violationsMu.Unlock()

			n := notify.RenderViolationDigest(snapshot, since, now)
			out.notifications = append(out.notifications, n)
		} else {
			d.violationsMu.Unlock()
		}
	} else {
		d.violationsMu.Unlock()
	}

	if d.tailing.Load() {
		d.tailSeen.Store(time.Now().UnixNano())
	}
	d.dispatch(out)
	if save {
		d.saveState(snap)
	}
}

func (d *Detector) recordViolation(project, container, kind string) {
	d.violationsMu.Lock()
	defer d.violationsMu.Unlock()
	key := project + "/" + container
	if d.violations[key] == nil {
		d.violations[key] = make(map[string]int)
	}
	d.violations[key][kind]++
}

func (d *Detector) Violations() map[string]map[string]int {
	d.violationsMu.Lock()
	defer d.violationsMu.Unlock()
	out := make(map[string]map[string]int)
	for k, v := range d.violations {
		out[k] = make(map[string]int)
		for kind, count := range v {
			out[k][kind] = count
		}
	}
	return out
}

func (d *Detector) FlushViolationsDigest(now time.Time) (notify.Notification, bool) {
	d.violationsMu.Lock()
	defer d.violationsMu.Unlock()
	if len(d.violations) == 0 {
		return notify.Notification{}, false
	}
	snapshot := make(map[string]map[string]int)
	for pc, kinds := range d.violations {
		snapshot[pc] = make(map[string]int)
		for k, v := range kinds {
			snapshot[pc][k] = v
		}
	}
	d.violations = make(map[string]map[string]int)
	since := d.lastDigest
	d.lastDigest = now
	return notify.RenderViolationDigest(snapshot, since, now), true
}

func findRedactionMarkers(text string) []string {
	matches := redactionMarkerRe.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]bool)
	var kinds []string
	for _, m := range matches {
		if len(m) > 1 && !seen[m[1]] {
			seen[m[1]] = true
			kinds = append(kinds, m[1])
		}
	}
	return kinds
}

func hasDynamicKey(jsonStr string) bool {
	if !strings.HasPrefix(strings.TrimSpace(jsonStr), "{") {
		return false
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(jsonStr), &m); err != nil {
		return false
	}
	for k := range m {
		if dynamicKeyRe.MatchString(k) {
			return true
		}
	}
	return false
}

func (d *Detector) dispatch(out outbox) {
	for _, n := range out.notifications {
		d.queue.Push(n)
	}
	for _, g := range out.handoffs {
		d.enqueueHandoff(g)
	}
	if len(out.notifications) > 0 {
		d.wakeSender()
	}
}

func (d *Detector) enqueueHandoff(g group.Group) {
	select {
	case d.handoffs <- g:
		return
	default:
	}
	d.handoffMu.Lock()
	if len(d.spill) < handoffOverflow {
		d.spill = append(d.spill, g)
		d.handoffMu.Unlock()
		d.wakeHandoff()
		return
	}
	d.handoffMu.Unlock()

	d.log.Warn("handoff buffers full, staging skip notice", "fingerprint", g.Fingerprint)
	d.stageSkip(g)
}

func (d *Detector) takeSpill() (group.Group, bool) {
	d.handoffMu.Lock()
	defer d.handoffMu.Unlock()
	if len(d.spill) == 0 {
		return group.Group{}, false
	}
	g := d.spill[0]
	copy(d.spill, d.spill[1:])
	d.spill[len(d.spill)-1] = group.Group{}
	d.spill = d.spill[:len(d.spill)-1]
	return g, true
}

func (d *Detector) wakeHandoff() {
	select {
	case d.handoffWake <- struct{}{}:
	default:
	}
}

func (d *Detector) handoffWorker(ctx context.Context) {
	defer close(d.handoffDone)
	for {
		if ctx.Err() != nil {
			return
		}
		if g, ok := d.takeSpill(); ok {
			if !d.deliverOne(ctx, g) {
				return
			}
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-d.handoffWake:
		case g := <-d.handoffs:
			if !d.deliverOne(ctx, g) {
				return
			}
		}
	}
}

func (d *Detector) deliverOne(ctx context.Context, g group.Group) bool {
	if err := d.deliverHandoff(ctx, g); err != nil && ctx.Err() != nil {
		d.requeueHandoff(g)
		return false
	}
	return true
}

func (d *Detector) requeueHandoff(g group.Group) {
	d.handoffMu.Lock()
	defer d.handoffMu.Unlock()
	d.spill = append(d.spill, g)
}

func (d *Detector) takeHandoffNow() (group.Group, bool) {
	if g, ok := d.takeSpill(); ok {
		return g, true
	}
	select {
	case g := <-d.handoffs:
		return g, true
	default:
		return group.Group{}, false
	}
}

func (d *Detector) drainHandoffs(ctx context.Context) {
	for {
		g, ok := d.takeHandoffNow()
		if !ok {
			return
		}
		err := d.deliverHandoff(ctx, g)
		if err == nil || ctx.Err() == nil {
			continue
		}

		d.log.Warn("shutdown drain budget exhausted, staging skip notices",
			"fingerprint", g.Fingerprint)
		d.stageSkip(g)
		for {
			rest, ok := d.takeHandoffNow()
			if !ok {
				return
			}
			d.stageSkip(rest)
		}
	}
}

func (d *Detector) deliverHandoff(ctx context.Context, g group.Group) error {
	err := d.investigate(ctx, g)
	if err == nil {
		d.resolve(g)
		return nil
	}
	if ctx.Err() != nil {

		return err
	}
	d.log.Warn("investigator unavailable, staging skip notice",
		"fingerprint", g.Fingerprint, "error", err.Error())
	d.stageSkip(g)
	return err
}

func (d *Detector) markUnresolved(g group.Group) {
	d.skipMu.Lock()
	d.unresolved[settleKey(g)] = struct{}{}
	d.skipMu.Unlock()
}

func (d *Detector) resolve(g group.Group) {
	d.skipMu.Lock()
	delete(d.unresolved, settleKey(g))
	d.skipMu.Unlock()
}

func (d *Detector) stageSkip(g group.Group) {
	d.skipMu.Lock()
	d.pinned = append(d.pinned, g)
	d.skipMu.Unlock()
	d.wakeSender()
}

func (d *Detector) flushSkips() []group.Group {
	d.skipRun.Lock()
	defer d.skipRun.Unlock()

	d.skipMu.Lock()
	batch := d.pinned
	d.pinned = nil
	if len(batch) > 0 {
		d.covered = append(d.covered, batch...)
	}
	d.skipMu.Unlock()
	if len(batch) == 0 {
		return nil
	}

	if len(batch) <= skipNoticeBatch && d.queue.Len()+len(batch) <= queueSize {
		for _, g := range batch {
			d.queue.Push(notify.RenderInvestigatorUnavailable(g))
		}
	} else {
		d.queue.Push(renderSkipAggregate(batch))
		d.log.Warn("queued one aggregate skip notice", "groups", len(batch))
	}
	return batch
}

func (d *Detector) deliverSkips(ctx context.Context) {
	d.flushSkips()
	d.queue.Drain(ctx)
	if d.queue.Len() == 0 {
		d.settleCovered()
	}
}

func (d *Detector) settleCovered() {
	d.skipMu.Lock()
	defer d.skipMu.Unlock()
	for _, g := range d.covered {
		delete(d.unresolved, settleKey(g))
	}
	d.covered = nil
}

func (d *Detector) hasStagedSkips() bool {
	d.skipMu.Lock()
	defer d.skipMu.Unlock()
	return len(d.pinned) > 0
}

func (d *Detector) uncoveredGroups() int {
	d.skipMu.Lock()
	defer d.skipMu.Unlock()
	return len(d.unresolved)
}

func renderSkipAggregate(batch []group.Group) notify.Notification {
	return notify.RenderInvestigatorUnavailableBatch(batch)
}

func (d *Detector) investigate(ctx context.Context, g group.Group) error {
	body, err := json.Marshal(g)
	if err != nil {
		return fmt.Errorf("encode group: %w", err)
	}
	endpoint := strings.TrimRight(d.cfg.InvestigatorURL, "/") + "/v1/investigate"

	var lastErr error
	for attempt := range handoffAttempts {
		if attempt > 0 && !sleep(ctx, handoffBackoff[attempt-1]) {
			return ctx.Err()
		}
		lastErr = d.postGroup(ctx, endpoint, body)
		if lastErr == nil {
			d.log.Info("group handed to the investigator",
				"fingerprint", g.Fingerprint, "attempts", attempt+1)
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return lastErr
}

func (d *Detector) postGroup(ctx context.Context, endpoint string, body []byte) error {
	ctx, cancel := context.WithTimeout(ctx, handoffTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("investigator: unexpected status %d", resp.StatusCode)
	}
	return nil
}

func (d *Detector) saveState(snap state.Snapshot) {
	if err := state.Save(d.cfg.StatePath, snap); err != nil {
		d.log.Error("state save failed", "path", d.cfg.StatePath, "error", err.Error())
		return
	}
	d.log.Info("state saved", "path", d.cfg.StatePath,
		"cursor", snap.Cursor.UTC().Format(time.RFC3339Nano), "groups", len(snap.Groups))
}

func (d *Detector) shutdown() error {
	now := d.now()
	var out outbox

	d.mu.Lock()

	for _, c := range d.asm.Flush(now) {
		d.absorb(c, now, &out)
	}
	if n, ok := d.takeOverflow(now, true); ok {
		out.notifications = append(out.notifications, n)
	}
	d.mu.Unlock()

	for _, n := range out.notifications {
		d.queue.Push(n)
	}
	for _, g := range out.handoffs {
		d.enqueueHandoff(g)
	}

	handoffCtx, cancelHandoff := context.WithTimeout(context.Background(), shutdownDrain)

	d.drainHandoffs(handoffCtx)
	cancelHandoff()

	d.flushSkips()
	d.settleCovered()

	d.mu.Lock()
	snap := state.Snapshot{Cursor: d.cursor, Groups: d.snapshotGroups()}
	d.mu.Unlock()
	d.saveState(snap)
	if n := d.uncoveredGroups(); n > 0 {
		d.log.Warn("shutdown with groups saved as not investigated", "groups", n)
	}

	notifyCtx, cancelNotify := context.WithTimeout(context.Background(), shutdownNotify)
	defer cancelNotify()
	d.queue.Drain(notifyCtx)
	if n := d.queue.Len(); n > 0 {
		d.log.Warn("shutdown with undelivered notifications", "queued", n)
	}
	return nil
}

func (d *Detector) snapshotGroups() []group.Group {
	groups := d.tracker.Export()
	d.skipMu.Lock()
	defer d.skipMu.Unlock()
	current := make(map[string]struct{}, len(groups))
	for i := range groups {
		key := settleKey(groups[i])
		current[key] = struct{}{}
		if _, ok := d.unresolved[key]; ok {
			groups[i].Handed = false
		}
	}
	for key := range d.unresolved {
		if _, ok := current[key]; !ok {
			delete(d.unresolved, key)
		}
	}
	return groups
}

func (d *Detector) now() time.Time { return d.cfg.Now() }

func (d *Detector) markTailSeen() { d.tailSeen.Store(time.Now().UnixNano()) }

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func errorString(err error) string {
	if err == nil {
		return "stream closed"
	}
	return err.Error()
}
