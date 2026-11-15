package notify

import (
	"context"
	"sync"
	"time"
)

type Queue struct {
	limit int
	send  func(context.Context, Notification) error
	logf  func(string, ...any)

	mu       sync.Mutex
	items    []entry
	seq      uint64
	draining bool
	passDone chan struct{}
	inflight uint64
}

type entry struct {
	seq uint64
	n   Notification
}

func NewQueue(max int, send func(context.Context, Notification) error, logf func(string, ...any)) *Queue {
	if max < 0 {
		max = 0
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Queue{limit: max, send: send, logf: logf}
}

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

func (q *Queue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

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

func (q *Queue) end() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.draining = false
	close(q.passDone)
	q.passDone = nil
}

func (q *Queue) droppableLocked() int {
	if len(q.items) == 0 {
		return -1
	}

	if q.items[0].seq != q.inflight {
		return 0
	}
	if len(q.items) > 1 {
		return 1
	}
	return -1
}

func (q *Queue) removeLocked(i int) {
	copy(q.items[i:], q.items[i+1:])
	q.items[len(q.items)-1] = entry{}
	q.items = q.items[:len(q.items)-1]
}

func (q *Queue) dropThrough(seq uint64) {
	i := 0
	for i < len(q.items) && q.items[i].seq <= seq {
		i++
	}
	q.shift(i)
}

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

type Limiter struct {
	perMinute int

	mu          sync.Mutex
	windowStart time.Time
	count       int
	overflow    int
}

func NewLimiter(perMinute int) *Limiter {
	return &Limiter{perMinute: perMinute}
}

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

func (l *Limiter) TakeOverflow() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := l.overflow
	l.overflow = 0
	return n
}
