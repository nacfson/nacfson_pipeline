package notify

import (
	"context"
	"sync"
	"time"
)

// Queue is a bounded FIFO of notifications waiting for a retry. When it is
// full, Push drops the oldest entry it may lose and logs one line, so a long
// proxy outage costs messages instead of memory. An entry already copied for
// an in-flight send is never dropped, so a failed send still leaves the
// notification queued. It is safe for concurrent use. A Queue must come from
// NewQueue; its zero value has no sender.
//
// Drain performs one pass and stops at the first failure: the notification
// that failed, and everything behind it, stays queued for the next pass. It
// logs one line per failed pass, never one per queued message. Two passes
// never overlap: a Drain that arrives during another pass waits for it to
// finish and then runs its own, so a ticker and a shutdown can both call it
// without sending a notification twice and without the second call silently
// delivering nothing.
type Queue struct {
	limit int
	send  func(context.Context, Notification) error
	logf  func(string, ...any)

	mu       sync.Mutex
	items    []entry
	seq      uint64
	draining bool
	passDone chan struct{} // closed when the running pass ends
	inflight uint64        // seq copied for the running send, 0 when none
}

// entry pairs a notification with the sequence number Push gave it, so Drain
// can tell which entries are still waiting even if a concurrent Push dropped
// some meanwhile.
type entry struct {
	seq uint64
	n   Notification
}

// NewQueue returns a queue holding at most max notifications. send delivers
// one notification; logf receives one line per dropped notification and one
// per failed pass. A nil logf discards them. logf is never called while the
// queue is locked.
func NewQueue(max int, send func(context.Context, Notification) error, logf func(string, ...any)) *Queue {
	if max < 0 {
		max = 0
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Queue{limit: max, send: send, logf: logf}
}

// Push appends n, dropping the oldest notification the queue may lose when it
// is full. A notification copied for an in-flight send is still queued until
// that send succeeds, so it is not a candidate: Push drops the entry behind it,
// or, when that was the only entry, n itself. Either way the drop line names a
// notification that really will not be delivered.
func (q *Queue) Push(n Notification) {
	if q.limit == 0 {
		q.logf("notify: queue disabled, dropped %q", n.Title)
		return
	}

	q.mu.Lock()
	dropped, droppedTitle, keep := false, "", true
	if len(q.items) >= q.limit {
		dropped = true
		if i := q.droppableLocked(); i >= 0 {
			droppedTitle = q.items[i].n.Title
			q.removeLocked(i)
		} else {
			// Every slot is held by a send that may still succeed, so the
			// arrival is what has to go.
			droppedTitle, keep = n.Title, false
		}
	}
	if keep {
		q.seq++
		q.items = append(q.items, entry{seq: q.seq, n: n})
	}
	q.mu.Unlock()

	if dropped {
		q.logf("notify: queue full, dropped %q", droppedTitle)
	}
}

// Len returns how many notifications are waiting.
func (q *Queue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

// Drain sends queued notifications in order until the queue is empty, a send
// fails or ctx is done. A call that finds another pass running waits for it and
// then runs its own pass, so a shutdown drain cannot be swallowed by an
// overlapping ticker drain.
func (q *Queue) Drain(ctx context.Context) {
	if !q.begin(ctx) {
		return
	}
	defer q.end()

	for {
		if ctx.Err() != nil {
			return
		}

		q.mu.Lock()
		if len(q.items) == 0 {
			q.mu.Unlock()
			return
		}
		head := q.items[0]
		q.inflight = head.seq
		q.mu.Unlock()

		err := q.send(ctx, head.n)

		q.mu.Lock()
		if err == nil {
			q.dropThrough(head.seq)
		}
		q.inflight = 0
		q.mu.Unlock()

		if err != nil {
			q.logf("notify: send %q failed, %d still queued: %v", head.n.Title, q.Len(), err)
			return
		}
	}
}

// begin starts a pass for this call. It waits while another pass runs, so the
// caller always gets a pass of its own afterwards, and reports false when ctx
// is done before the pass could start.
func (q *Queue) begin(ctx context.Context) bool {
	q.mu.Lock()
	for q.draining {
		if ctx.Err() != nil {
			q.mu.Unlock()
			return false
		}
		done := q.passDone
		q.mu.Unlock()
		select {
		case <-done:
		case <-ctx.Done():
			return false
		}
		q.mu.Lock()
	}
	q.draining = true
	q.passDone = make(chan struct{})
	q.mu.Unlock()
	return true
}

// end releases the pass that begin took and wakes every waiting Drain.
func (q *Queue) end() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.draining = false
	close(q.passDone)
	q.passDone = nil
}

// droppableLocked returns the index of the oldest entry no in-flight send
// holds, or -1 when every entry is held. The caller holds q.mu.
func (q *Queue) droppableLocked() int {
	if len(q.items) == 0 {
		return -1
	}
	// inflight is 0 between sends and sequence numbers start at 1.
	if q.items[0].seq != q.inflight {
		return 0
	}
	if len(q.items) > 1 {
		return 1
	}
	return -1
}

// removeLocked drops the entry at i without growing the backing array, clearing
// the vacated slot so a dropped notification can be collected. The caller holds
// q.mu.
func (q *Queue) removeLocked(i int) {
	copy(q.items[i:], q.items[i+1:])
	q.items[len(q.items)-1] = entry{}
	q.items = q.items[:len(q.items)-1]
}

// dropThrough removes every entry up to and including seq. The caller holds
// q.mu. The sequence comparison also covers entries a concurrent Push dropped
// while the send was in flight.
func (q *Queue) dropThrough(seq uint64) {
	i := 0
	for i < len(q.items) && q.items[i].seq <= seq {
		i++
	}
	q.shift(i)
}

// shift drops the first n entries without growing the backing array, clearing
// the vacated slots so a dropped notification can be collected. The caller
// holds q.mu.
func (q *Queue) shift(n int) {
	if n <= 0 {
		return
	}
	kept := copy(q.items, q.items[n:])
	for i := kept; i < len(q.items); i++ {
		q.items[i] = entry{}
	}
	q.items = q.items[:kept]
}

// Limiter caps new-error notifications per minute and counts the ones it
// refuses in a fixed window, so the overflow can be reported as one message.
// It is safe for concurrent use and takes the time from its caller.
type Limiter struct {
	perMinute int

	mu          sync.Mutex
	windowStart time.Time
	count       int
	overflow    int
}

// NewLimiter returns a limiter allowing perMinute notifications per minute. A
// non-positive perMinute allows every notification.
func NewLimiter(perMinute int) *Limiter {
	return &Limiter{perMinute: perMinute}
}

// Allow reports whether one more notification may be sent at now. Every
// refused call increments the overflow count.
func (l *Limiter) Allow(now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.perMinute <= 0 {
		return true
	}
	if l.windowStart.IsZero() || now.Sub(l.windowStart) >= time.Minute {
		l.windowStart = now
		l.count = 0
	}
	if l.count < l.perMinute {
		l.count++
		return true
	}
	l.overflow++
	return false
}

// TakeOverflow returns how many notifications were refused since the previous
// call and resets the counter.
func (l *Limiter) TakeOverflow() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := l.overflow
	l.overflow = 0
	return n
}
