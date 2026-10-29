// Package detector wires the error pipeline together. It tails VictoriaLogs,
// classifies and groups what it reads, alerts the #errors channel through the
// notify proxy, and hands closed groups to the error investigator. It keeps
// no secrets: the notify proxy owns the Discord URLs.
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
	// channelErrors is the only channel the detector posts to. The notify proxy
	// grants `errors` to observability/error-detector and nothing else.
	channelErrors = "errors"

	// burstPerMinute caps new-error notifications; the rest are reported once
	// per window as a single "+N more new errors" message.
	burstPerMinute = 10

	// queueSize bounds the pending notifications held in memory during a
	// notify proxy outage.
	queueSize = 100

	// handoffQueue bounds the groups waiting for the investigator in the
	// channel the worker reads.
	handoffQueue = 100

	// handoffOverflow bounds the second tier of handoff storage: the groups a
	// full handoffQueue could not take wait here instead of being dropped. The
	// worker and the shutdown drain both serve it, so a busy worker costs
	// latency rather than a lost investigation.
	handoffOverflow = 32

	// tailOffset is how far behind the wall clock the tail endpoint reads, so
	// the assembler watermark (and therefore crash flushes) is compared in
	// record time, not wall time.
	tailOffset = 5 * time.Second

	// crashSilence is how long a crash stack may stay silent, in record time,
	// before it is closed (contract section 4).
	crashSilence = 2 * time.Second

	// catchUpMax caps the backfill window after a restart.
	catchUpMax = 15 * time.Minute

	// saveInterval is how often the cursor and groups are written.
	saveInterval = 30 * time.Second

	// tailReadyWindow is how recently the control loop must have seen a live
	// tail for /readyz to pass.
	tailReadyWindow = 30 * time.Second

	// probeTimeout bounds the VictoriaLogs check behind /readyz.
	probeTimeout = 10 * time.Second

	// sendTimeout bounds one notification, so a slow proxy cannot pin a drain
	// pass open forever.
	sendTimeout = 90 * time.Second

	// handoffTimeout bounds one investigator attempt.
	handoffTimeout = 10 * time.Second

	// backoff bounds the tail reconnect delay.
	backoffMin = time.Second
	backoffMax = 30 * time.Second

	// handoffAttempts is the number of investigator tries for one group.
	handoffAttempts = 3

	// skipNoticeBatch is how many groups one flush still reports with a notice
	// each. Above it they are reported together as a single aggregate, the way
	// RenderOverflow collapses the alerts the burst cap refused: a shutdown can
	// leave 133 groups unposted, and 133 individual messages is a rate-limit
	// incident, not an alert.
	skipNoticeBatch = 10
)

// shutdownDrain is the budget for the shutdown handoff drain, and
// shutdownNotify the separate budget for the notification delivery that follows
// it. They are independent on purpose: one slow investigator can spend the
// whole handoff budget, and the skip notices that failure produces must still
// get their own delivery attempt. Under a single shared budget the notification
// drain returned instantly in exactly that case, so those notices were queued
// and then discarded with the process. Both are variables only so a test can
// shorten them; production never changes them. Worst-case shutdown wall time is
// their sum, 35 s, plus the cost of writing one state file.
var (
	shutdownDrain  = 15 * time.Second
	shutdownNotify = 20 * time.Second
)

// tickInterval is the control loop period. It is a variable so the end-to-end
// tests do not have to wait on the production one second; production never
// changes it.
var tickInterval = time.Second

// retryInterval is how often a queued notification is retried while the notify
// proxy is down (design section 11). Retrying on the control loop's own
// one-second tick would log a failure line per second for the length of an
// outage. It is a variable only so a test can shorten it.
var retryInterval = 30 * time.Second

// handoffBackoff is the wait before the second and third investigator attempt.
// Three tries leave two gaps; they escalate like the notify proxy's own retry
// ladder.
var handoffBackoff = [...]time.Duration{time.Second, 2 * time.Second}

// Config is the detector's dependency set. VLogs, Notify and StatePath are
// required; Group and Now fall back to the design defaults.
type Config struct {
	VLogs           *vlogs.Client
	Notify          *notify.Client
	InvestigatorURL string // empty disables the handoff
	StatePath       string
	Group           group.Config
	Now             func() time.Time
}

// outbox is what one ingest or tick produced, carried out of the state lock so
// pushes and network calls happen without holding it.
type outbox struct {
	notifications []notify.Notification
	handoffs      []group.Group
}

// Detector is the running pipeline. The assembler, the tracker, the cursor and
// the burst window are guarded by mu; everything touching the network is not.
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
	tailSeen atomic.Int64 // unix nanoseconds of the last observed live tail

	// wake asks the sender goroutine for a delivery pass. It holds one pending
	// wake, so a push never blocks and never loses a pass.
	wake       chan struct{}
	senderDone chan struct{}

	// handoffWake asks the handoff worker to look at the spill list. It holds
	// one pending wake for the same reason as wake.
	handoffWake chan struct{}
	handoffDone chan struct{}

	// handoffMu guards spill, the second tier of handoff storage. It is
	// separate from mu because route appends to the spill list while holding
	// mu and the worker pops from it without mu.
	handoffMu sync.Mutex
	spill     []group.Group

	// skipMu guards pinned, covered and unresolved: the skip-notice staging
	// area, the notices already in the notification queue, and the cycles with
	// no outcome yet.
	//
	// pinned holds groups that must be reported as not investigated. They are
	// not pushed as they arrive, because a shutdown can leave the whole handoff
	// buffers (133 groups) unposted and 133 pushes into a 100-entry queue whose
	// Push evicts the oldest entry would drop the earliest notices, leaving
	// those groups persisted as handed with nothing saying the investigation
	// never happened. flushSkips renders them instead: one notice per group
	// while the batch is small and the queue has room for all of it, one
	// aggregate above that, the way RenderOverflow collapses the alerts the
	// burst cap refused.
	//
	// covered holds the groups whose notice flushSkips just queued: they are in
	// the notification queue, and nothing pushes between a flush and the save
	// that follows it, so they are all still there when the state is written.
	// A drain that empties the queue settles them for good.
	//
	// unresolved holds the cycles (fingerprint and window end) this process
	// closed that no outcome covers: not posted to the investigator and not
	// known to be noticed. The snapshot writer clears Handed for a group whose
	// cycle is in it, so the next start retries it rather than believing a
	// handoff happened. Only cycles this process closes are added, so a group
	// loaded from state keeps the Handed bit the previous process wrote.
	skipMu     sync.Mutex
	pinned     []group.Group
	covered    []group.Group
	unresolved map[string]struct{}

	// skipRun serialises flushSkips passes, so two of them cannot render the
	// same pinned batch twice. It is always taken before skipMu, never after.
	skipRun sync.Mutex

	mu         sync.Mutex
	cursor     time.Time
	burstSince time.Time
	lastSave   time.Time
}

// settleKey identifies one closed cycle of a group: its fingerprint and the
// window that closed. Two cycles of one fingerprint differ in WindowEnd, so an
// outcome never carries over from one cycle to the next.
func settleKey(g group.Group) string {
	return g.Fingerprint + "@" + strconv.FormatInt(g.WindowEnd.UnixNano(), 10)
}

// New validates cfg and builds a detector. It performs no I/O; Run does.
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

// Mux serves the three probes. /healthz is process-local, /startupz waits for
// the state to load, and /readyz needs VictoriaLogs and a live tail.
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

// handleHealthz is strictly process-local: it never touches VictoriaLogs, the
// notify proxy or the investigator.
func (d *Detector) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ALIVE"})
}

// tailLive reports whether a tail is open and the control loop has refreshed
// its timestamp within tailReadyWindow. The refresh is what makes a wedged
// control loop fail readiness instead of looking permanently healthy.
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

// Run loads the state, starts the collector and the investigator worker, then
// drives the once-per-second control loop until ctx is cancelled. It returns
// nil after a clean shutdown: the caller's cancellation is not an error.
func (d *Detector) Run(ctx context.Context) error {
	d.loadState()
	d.sweepTemp()
	d.started.Store(true)

	collected := make(chan struct{})
	go func() {
		defer close(collected)
		d.collect(ctx)
	}()
	go d.sender(ctx)
	go d.handoffWorker(ctx)

	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			// Let the collector finish its current read so no cursor
			// position is lost, then flush what is left.
			<-collected
			// The sender and the worker stop on their own ctx; the final
			// drain below owns the queue and every remaining handoff from
			// here.
			<-d.senderDone
			<-d.handoffDone
			return d.shutdown()
		case <-ticker.C:
			d.tick()
		}
	}
}

// sender delivers queued notifications and the pinned skip outbox. It runs when
// a producer wakes it and again every retryInterval while anything is still
// queued or pinned, always off the control loop goroutine, so a slow proxy never
// delays a flush, a window close or a state save. Queue.Drain serialises
// overlapping passes itself, and flushSkips serialises its own, so a wake
// during a pass is safe and nothing is sent twice.
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

// wakeSender asks for a delivery pass without blocking.
func (d *Detector) wakeSender() {
	select {
	case d.wake <- struct{}{}:
	default: // a pass is already pending, which is exactly as good
	}
}

// loadState restores the cursor and the group table. A state file that cannot
// be read or parsed is logged and ignored: losing deduplication history is far
// better than not starting, because the alerts are the product.
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

// sweepTemp removes the state.json.tmp-* files a killed Save leaves behind.
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

// collect backfills from the cursor and then tails, reconnecting with an
// exponential backoff until ctx is cancelled.
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

// catchUp replays [max(cursor, now-15m), now) so a restart neither drops what
// was logged while the detector was down nor floods Discord with old errors.
// A first start has no cursor and tails from now.
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

// ingest feeds one record through the assembler and the tracker. It is the
// only writer of the cursor and the assembler.
func (d *Detector) ingest(r classify.Record) error {
	now := d.now()
	var out outbox

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

// absorb records one classified error and queues the notifications it caused.
// The caller holds mu.
func (d *Detector) absorb(c classify.Classified, now time.Time, out *outbox) {
	for _, a := range d.tracker.Observe(c, now) {
		d.route(a, now, out)
	}
}

// route turns a tracker action into a queued notification or a handoff. The
// caller holds mu.
func (d *Detector) route(a group.Action, now time.Time, out *outbox) {
	switch a.Type {
	case group.NotifyNew:
		// RenderNewAction, never RenderNew: only Action.Recurred knows that a
		// cooldown-expired recurrence with no counted occurrences is still a
		// recurrence.
		if d.allowBurst(now, out) {
			out.notifications = append(out.notifications, notify.RenderNewAction(a))
		}
	case group.Handoff:
		if d.cfg.InvestigatorURL == "" {
			d.log.Info("handoff disabled, group not investigated", "fingerprint", a.Group.Fingerprint)
			return
		}
		// The tracker has already marked the group handed. Record that its
		// outcome is still open, so a save between here and the worker's post
		// cannot persist it as investigated. deliverHandoff resolves it on
		// success and stageSkip covers it on failure.
		d.markUnresolved(a.Group)
		out.handoffs = append(out.handoffs, a.Group)
	default:
		d.log.Warn("unknown group action", "type", a.Type.String())
	}
}

// allowBurst applies the per-minute cap and tracks the window it counts in, so
// the refused total can be reported against the window it belonged to. The
// caller holds mu, which makes the detector the only Allow caller and keeps
// its window in step with the limiter's own.
//
// When the window rolls over, the previous window's refusals are harvested
// before burstSince moves. notify.Limiter keeps its refused count across a
// roll and only TakeOverflow clears it, so harvesting later would either
// attribute the count to the new window or, worse, sit on it until a quiet
// minute that a steady stream of new errors never produces. Reporting here
// keeps the summary prompt under exactly the load the cap exists for.
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

// takeOverflow returns the single summary message for the notifications the cap
// refused once their window has ended. It covers the quiet path, where the
// window ended and no new error arrived to roll it over (allowBurst covers the
// busy path). force reports a partial window at shutdown instead of dropping
// the count. The caller holds mu.
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

// tick is one control loop pass: flush the crash assembler, close the windows
// that ended, report the burst overflow and save the state when due. Pushing
// and network work happen after the lock is released.
func (d *Detector) tick() {
	now := d.now()
	var (
		out  outbox
		snap state.Snapshot
		save bool
	)

	d.mu.Lock()
	// Flush compares in record time: the tail endpoint reads tailOffset behind
	// the wall clock, so a crash is never cut short because the reader lags.
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

	if d.tailing.Load() {
		d.tailSeen.Store(time.Now().UnixNano())
	}
	d.dispatch(out)
	if save {
		d.saveState(snap)
	}
}

// dispatch pushes the notifications, queues the handoffs and starts a delivery
// pass. Pushing is cheap and ordered here; sending is not.
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

// enqueueHandoff queues a group for the investigator without ever blocking the
// caller. Groups go to the worker's channel first; when it is full they go to
// the bounded spill list the worker and the shutdown drain also serve. Neither
// tier ever discards a group, because the tracker has already marked it handed
// and there is no other path back to a handoff.
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
	// Both tiers are full. The group is refused rather than lost: the skip
	// notice tells #errors it was not investigated, which is the same outcome
	// a failed investigator produces. It goes to the skip outbox rather than
	// straight into the notify queue, so a burst of refusals cannot evict the
	// earlier notices and leave their groups looking investigated.
	d.log.Warn("handoff buffers full, staging skip notice", "fingerprint", g.Fingerprint)
	d.stageSkip(g)
}

// takeSpill pops the oldest spilled group.
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

// wakeHandoff asks the worker to look at the spill list without blocking.
func (d *Detector) wakeHandoff() {
	select {
	case d.handoffWake <- struct{}{}:
	default:
	}
}

// handoffWorker posts queued groups one at a time until ctx is cancelled. The
// loop head serves the spill list before the channel, so a group whose channel
// slot was refused is not starved by a steady stream of channel arrivals.
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
		case <-d.handoffWake: // the loop head does the work
		case g := <-d.handoffs:
			if !d.deliverOne(ctx, g) {
				return
			}
		}
	}
}

// deliverOne posts one group and reports whether the worker should keep going.
// A group whose attempt shutdown cut short goes back on the spill list, which
// Run waits for and shutdown() owns, so it is posted on a live context instead
// of being lost with the worker.
func (d *Detector) deliverOne(ctx context.Context, g group.Group) bool {
	if err := d.deliverHandoff(ctx, g); err != nil && ctx.Err() != nil {
		d.requeueHandoff(g)
		return false
	}
	return true
}

// requeueHandoff returns the one group the worker was holding when its context
// ended to the spill list. Only the worker calls it, so it needs no bound of
// its own, and the shutdown drain owns the list from then on.
func (d *Detector) requeueHandoff(g group.Group) {
	d.handoffMu.Lock()
	defer d.handoffMu.Unlock()
	d.spill = append(d.spill, g)
}

// takeHandoffNow pops the next waiting group, the spill list first so a group
// whose channel slot was refused is not starved by later arrivals. It never
// blocks, so both the shutdown drain and the worker's last pass stay bounded.
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

// drainHandoffs posts every group still waiting for the investigator, on ctx,
// which the caller does not cancel. deliverHandoff already stages the skip
// notice when the investigator refuses; a group left over only because the
// shutdown budget ran out gets its notice staged here instead. Every closed
// group therefore ends up either investigated or staged.
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
		// The budget is gone: this group, and every one behind it, gets its
		// notice without another attempt.
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

// deliverHandoff posts one group to the investigator, and on failure stages the
// skip notice the design fixes in place of a silent loss. It reports the
// investigator error; the notice is staged when it does.
func (d *Detector) deliverHandoff(ctx context.Context, g group.Group) error {
	err := d.investigate(ctx, g)
	if err == nil {
		d.resolve(g)
		return nil
	}
	if ctx.Err() != nil {
		// The attempt was cut short by shutdown, not by the investigator. The
		// caller keeps the group for the shutdown drain, which posts on a
		// context that is not cancelled.
		return err
	}
	d.log.Warn("investigator unavailable, staging skip notice",
		"fingerprint", g.Fingerprint, "error", err.Error())
	d.stageSkip(g)
	return err
}

// markUnresolved records that g's cycle was closed and nothing has decided its
// outcome yet, so a save must not persist it as handed.
func (d *Detector) markUnresolved(g group.Group) {
	d.skipMu.Lock()
	d.unresolved[settleKey(g)] = struct{}{}
	d.skipMu.Unlock()
}

// resolve records that g reached the investigator, which is the one outcome
// that needs no skip notice.
func (d *Detector) resolve(g group.Group) {
	d.skipMu.Lock()
	delete(d.unresolved, settleKey(g))
	d.skipMu.Unlock()
}

// stageSkip pins g in the skip outbox. Nothing evicts the outbox, which is what
// a plain Push into the bounded notification queue could not promise: 133
// unposted groups pushed there evict each other's notices and leave those groups
// persisted as handed with nothing saying the investigation never happened.
func (d *Detector) stageSkip(g group.Group) {
	d.skipMu.Lock()
	d.pinned = append(d.pinned, g)
	d.skipMu.Unlock()
	d.wakeSender()
}

// flushSkips renders the pinned outbox into the notification queue and returns
// the groups it covered. A notice per group is used while the batch is small and
// the queue has room for all of it, so no push in the batch can evict another;
// past that the batch becomes one aggregate whose count covers all of it,
// mirroring how RenderOverflow collapses the alerts the burst cap refused. One
// push into a full queue evicts the oldest entry, never the arrival, so the
// notice is in the queue when this returns.
//
// The covered groups move to d.covered, which settleCovered settles once their
// notices are known to be in the queue. That is what distinguishes "queued" from
// "delivered": a save before the settle leaves them un-handed for the next start
// to retry. It is also why skipRun exists: two flushes must not append the same
// pinned batch to covered twice.
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

// deliverSkips renders the pinned outbox into the notification queue, attempts
// one delivery pass, and settles the cycles it covered once the queue is empty.
// An empty queue is the only evidence that every queued notice was accepted.
func (d *Detector) deliverSkips(ctx context.Context) {
	d.flushSkips()
	d.queue.Drain(ctx)
	if d.queue.Len() == 0 {
		d.settleCovered()
	}
}

// settleCovered settles the cycles whose notices flushSkips put in the
// notification queue, so a save no longer writes them as not investigated.
//
// The evidence differs by caller. On the sender goroutine it is an empty queue:
// a drain that emptied it accepted every notice. In shutdown it is the snapshot
// itself, taken right after flushSkips, because nothing pushes between a flush
// and the save that follows it — so every covered group has its notice sitting
// in the queue at the moment the state is written.
func (d *Detector) settleCovered() {
	d.skipMu.Lock()
	defer d.skipMu.Unlock()
	for _, g := range d.covered {
		delete(d.unresolved, settleKey(g))
	}
	d.covered = nil
}

// hasStagedSkips reports whether a group is waiting for its skip notice.
func (d *Detector) hasStagedSkips() bool {
	d.skipMu.Lock()
	defer d.skipMu.Unlock()
	return len(d.pinned) > 0
}

// uncoveredGroups reports how many groups still have no outcome, for the
// shutdown warning.
func (d *Detector) uncoveredGroups() int {
	d.skipMu.Lock()
	defer d.skipMu.Unlock()
	return len(d.unresolved)
}

// renderSkipAggregate renders the one notice that stands for every group in
// batch. notify owns the wording and the proxy's Validate limits, so the
// detector cannot drift from them.
func renderSkipAggregate(batch []group.Group) notify.Notification {
	return notify.RenderInvestigatorUnavailableBatch(batch)
}

// investigate posts the group to the investigator. It ends on the first
// success: three tries at most, waiting 1 s and then 2 s between them, which is
// the first two rungs of the notify proxy's own 1/2/4 s ladder.
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

// saveState writes the snapshot atomically. A write failure is logged and
// retried on the next pass; it must not stop the pipeline.
func (d *Detector) saveState(snap state.Snapshot) {
	if err := state.Save(d.cfg.StatePath, snap); err != nil {
		d.log.Error("state save failed", "path", d.cfg.StatePath, "error", err.Error())
		return
	}
	d.log.Info("state saved", "path", d.cfg.StatePath,
		"cursor", snap.Cursor.UTC().Format(time.RFC3339Nano), "groups", len(snap.Groups))
}

// shutdown completes the pipeline after the tail has stopped. It closes every
// crash still open, reports the burst overflow of a partial window, delivers
// every handoff still waiting or produced by the final flush, stages a skip
// notice for any it cannot deliver, flushes those notices into the notification
// queue, saves the final state and then makes one bounded notification delivery
// attempt on a budget of its own.
//
// It deliberately does not run tracker.Tick: that would close windows whose
// handoff needs the investigator worker, which is already cancelled, and mark
// them handed without ever posting them. Windows still open are saved as they
// are, so the next start hands them off.
//
// The ordering below is what makes the save honest:
//
//  1. drain handoffs on shutdownDrain, staging a skip notice for every group it
//     cannot post;
//  2. flushSkips puts each staged notice in the notification queue;
//  3. build the snapshot, clearing Handed for any cycle still unresolved, and
//     save it;
//  4. drain the notification queue on shutdownNotify.
//
// The save happens after the notices are queued and before they are drained,
// because the invariant is "a notice is present at saveState", not "a notice was
// delivered". A drain before the save would deliver some notices and leave the
// queue empty for others if the proxy failed midway, and the save would then
// have to work out which groups were actually covered. Queueing everything
// first makes coverage a property of the snapshot itself: every group left
// Handed has a notice sitting in the queue at that moment. If the proxy refuses,
// the notices stay queued and are retried on the next drain within this process;
// if the process exits first, the group is still saved Handed, and the
// notification queue is in memory only — which is why step 4 gets a budget that
// step 1 cannot consume.
func (d *Detector) shutdown() error {
	now := d.now()
	var out outbox

	d.mu.Lock()
	// Nothing more will be read, so every open crash is complete: flush them
	// with the wall clock itself rather than the tail-lagged watermark.
	for _, c := range d.asm.Flush(now) {
		d.absorb(c, now, &out)
	}
	if n, ok := d.takeOverflow(now, true); ok {
		out.notifications = append(out.notifications, n)
	}
	d.mu.Unlock()

	// Queue first, deliver second: a notification pushed here must be in the
	// queue before its drain pass, and a skip notice staged during the handoff
	// drain must be in it before the final save.
	for _, n := range out.notifications {
		d.queue.Push(n)
	}
	for _, g := range out.handoffs {
		d.enqueueHandoff(g)
	}

	// The handoff drain and the notification drain get separate budgets. One
	// slow investigator can spend the whole handoff budget, and the notices it
	// produces still need their own delivery attempt: sharing one budget made
	// queue.Drain see an already-done context and return instantly in exactly
	// that case, so the notice staged a moment earlier was thrown away with the
	// process.
	handoffCtx, cancelHandoff := context.WithTimeout(context.Background(), shutdownDrain)
	// The tail and the control loop have stopped, so nothing can add to the
	// handoff channel or the spill list while this drains.
	d.drainHandoffs(handoffCtx)
	cancelHandoff()

	// Every group the drain could not post is now staged; render it into the
	// queue before the snapshot, so the snapshot may keep its Handed bit. The
	// notices are in the queue at this instant, which is exactly the evidence
	// settleCovered needs: nothing pushes between here and the save.
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

// snapshotGroups returns the tracker's groups as they must be persisted: a
// closed cycle with no outcome yet is written with Handed false, so the next
// start hands it off again instead of treating an investigation that never
// happened as done. Every other group keeps its tracker state, including a
// group loaded as handed by a previous process.
//
// It also prunes unresolved keys for cycles the tracker no longer holds, which
// is what keeps the set from growing across cycles of a recurring fingerprint:
// a cycle that ended and was replaced by a new one has no group left to correct.
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

// markTailSeen records that a tail is open right now, which is what /readyz
// reads beside the tail's own timestamp.
func (d *Detector) markTailSeen() { d.tailSeen.Store(time.Now().UnixNano()) }

// sendSleep waits for d or ctx. It reports false when ctx ended first.
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
