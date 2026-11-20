package slots

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	"nacfson_pipeline/services/wg-slot-agent/internal/kube"
)

const (
	LeaseDuration = 90 * time.Second
	RenewInterval = 30 * time.Second
	MaxSlot       = 13
)

var (
	ErrNoFreeSlot = errors.New("no free slot")
	ErrLost       = errors.New("slot lease lost")
)

type API interface {
	ListLeases(ctx context.Context) ([]kube.Lease, error)
	GetLease(ctx context.Context, name string) (kube.Lease, error)
	CreateLease(ctx context.Context, l kube.Lease) (kube.Lease, error)
	UpdateLease(ctx context.Context, l kube.Lease) (kube.Lease, error)
	NodeExists(ctx context.Context, name string) (bool, error)
}

func LeaseName(slot int) string {
	return "wg-slot-" + strconv.Itoa(slot)
}

func ParsePool(s string) ([]int, error) {
	seen := map[int]bool{}
	var out []int
	for _, f := range strings.Split(s, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		n, err := strconv.Atoi(f)
		if err != nil || n < 0 || n > MaxSlot {
			return nil, fmt.Errorf("slot %q is not a number within 0-%d", f, MaxSlot)
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Ints(out)
	return out, nil
}

func parseTime(s *string) (time.Time, bool) {
	if s == nil {
		return time.Time{}, false
	}
	t, err := time.Parse(kube.MicroTimeLayout, *s)
	if err != nil {
		t, err = time.Parse(time.RFC3339Nano, *s)
	}
	return t, err == nil
}

func formatTime(t time.Time) *string {
	s := t.UTC().Format(kube.MicroTimeLayout)
	return &s
}

func Holder(l kube.Lease) string {
	if l.Spec.HolderIdentity == nil {
		return ""
	}
	return *l.Spec.HolderIdentity
}

func Live(l kube.Lease, now time.Time) bool {
	if Holder(l) == "" {
		return false
	}
	renewed, ok := parseTime(l.Spec.RenewTime)
	if !ok {
		return false
	}
	d := LeaseDuration
	if l.Spec.LeaseDurationSeconds != nil {
		d = time.Duration(*l.Spec.LeaseDurationSeconds) * time.Second
	}
	return now.Before(renewed.Add(d))
}

type Claimer struct {
	API  API
	Node string
	Now  func() time.Time
}

func (c *Claimer) free(ctx context.Context, l kube.Lease) (bool, error) {
	holder := Holder(l)
	if holder == "" || holder == c.Node || !Live(l, c.Now()) {
		return true, nil
	}
	exists, err := c.API.NodeExists(ctx, holder)
	if err != nil {
		return false, err
	}
	return !exists, nil
}

func (c *Claimer) hold(l *kube.Lease) {
	now := c.Now()
	d := int32(LeaseDuration / time.Second)
	node := c.Node
	l.Spec.HolderIdentity = &node
	l.Spec.LeaseDurationSeconds = &d
	l.Spec.AcquireTime = formatTime(now)
	l.Spec.RenewTime = formatTime(now)
}

func (c *Claimer) Claim(ctx context.Context, eligible []int) (int, error) {
	tried := map[int]bool{}
	for {
		leases, err := c.API.ListLeases(ctx)
		if err != nil {
			return -1, err
		}
		byName := map[string]kube.Lease{}
		for _, l := range leases {
			byName[l.Metadata.Name] = l
		}
		retry := false
		for _, slot := range eligible {
			if tried[slot] {
				continue
			}
			l, ok := byName[LeaseName(slot)]
			if !ok {
				l = kube.Lease{Metadata: kube.ObjectMeta{Name: LeaseName(slot)}}
				c.hold(&l)
				if _, err := c.API.CreateLease(ctx, l); err != nil {
					if errors.Is(err, kube.ErrConflict) {
						tried[slot] = true
						retry = true
						break
					}
					return -1, err
				}
				return slot, nil
			}
			free, err := c.free(ctx, l)
			if err != nil {
				return -1, err
			}
			if !free {
				continue
			}
			c.hold(&l)
			if _, err := c.API.UpdateLease(ctx, l); err != nil {
				if errors.Is(err, kube.ErrConflict) {
					tried[slot] = true
					retry = true
					break
				}
				return -1, err
			}
			return slot, nil
		}
		if !retry {
			return -1, ErrNoFreeSlot
		}
	}
}

func (c *Claimer) Renew(ctx context.Context, slot int, annotations map[string]string) error {
	l, err := c.API.GetLease(ctx, LeaseName(slot))
	if err != nil {
		if errors.Is(err, kube.ErrNotFound) {
			return ErrLost
		}
		return err
	}
	if Holder(l) != c.Node {
		return ErrLost
	}
	l.Spec.RenewTime = formatTime(c.Now())
	if len(annotations) > 0 && l.Metadata.Annotations == nil {
		l.Metadata.Annotations = map[string]string{}
	}
	for k, v := range annotations {
		l.Metadata.Annotations[k] = v
	}
	if _, err := c.API.UpdateLease(ctx, l); err != nil {
		if errors.Is(err, kube.ErrConflict) {
			return ErrLost
		}
		return err
	}
	return nil
}

func (c *Claimer) Release(ctx context.Context, slot int) error {
	l, err := c.API.GetLease(ctx, LeaseName(slot))
	if err != nil {
		if errors.Is(err, kube.ErrNotFound) {
			return nil
		}
		return err
	}
	if Holder(l) != c.Node {
		return nil
	}
	l.Spec.HolderIdentity = nil
	l.Spec.RenewTime = nil
	_, err = c.API.UpdateLease(ctx, l)
	return err
}

type Tunnel interface {
	Up(slot int) error
	Down() error
}

type Observation struct {
	Annotations map[string]string
	Stale       bool
}

type Agent struct {
	Claimer *Claimer
	Pool    func() ([]int, error)
	Tunnel  Tunnel
	Event   func(ctx context.Context, eventType, reason, message string) error
	Observe func() Observation
	Sleep   func(ctx context.Context, d time.Duration) error
}

func contains(xs []int, x int) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func (a *Agent) emit(ctx context.Context, eventType, reason, message string) {
	if err := a.Event(ctx, eventType, reason, message); err != nil {
		log.Printf("event %s: %v", reason, err)
	}
}

func (a *Agent) Run(ctx context.Context) error {
	slot := -1
	stale := false
	var renewed time.Time
	defer func() {
		if slot < 0 {
			return
		}
		if err := a.Tunnel.Down(); err != nil {
			log.Printf("tunnel down: %v", err)
		}
		rctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := a.Claimer.Release(rctx, slot); err != nil {
			log.Printf("release slot %d: %v", slot, err)
		}
	}()
	for {
		if ctx.Err() != nil {
			return nil
		}
		pool, err := a.Pool()
		if err != nil {
			log.Printf("read pool: %v", err)
		}
		switch {
		case err != nil:
		case slot < 0:
			s, err := a.Claimer.Claim(ctx, pool)
			if errors.Is(err, ErrNoFreeSlot) {
				a.emit(ctx, "Warning", "NoFreeSlot", fmt.Sprintf("node %s found no free WireGuard slot in pool %v", a.Claimer.Node, pool))
				break
			}
			if err != nil {
				log.Printf("claim: %v", err)
				break
			}
			if err := a.Tunnel.Up(s); err != nil {
				log.Printf("tunnel up for slot %d: %v", s, err)
				if rerr := a.Claimer.Release(ctx, s); rerr != nil {
					log.Printf("release slot %d: %v", s, rerr)
				}
				break
			}
			slot = s
			stale = false
			renewed = a.Claimer.Now()
			log.Printf("node %s holds slot %d", a.Claimer.Node, slot)
			continue
		case !contains(pool, slot):
			log.Printf("slot %d left the pool, moving", slot)
			if err := a.Tunnel.Down(); err != nil {
				log.Printf("tunnel down: %v", err)
			}
			if err := a.Claimer.Release(ctx, slot); err != nil {
				log.Printf("release slot %d: %v", slot, err)
			}
			slot = -1
			continue
		default:
			obs := a.Observe()
			if err := a.Claimer.Renew(ctx, slot, obs.Annotations); err != nil {
				log.Printf("renew slot %d: %v", slot, err)
				expiring := a.Claimer.Now().Sub(renewed)+RenewInterval >= LeaseDuration
				if errors.Is(err, ErrLost) || expiring {
					if expiring {
						log.Printf("slot %d not renewed for %s, giving it up before another node can claim it", slot, a.Claimer.Now().Sub(renewed))
					}
					if derr := a.Tunnel.Down(); derr != nil {
						log.Printf("tunnel down: %v", derr)
					}
					slot = -1
					continue
				}
			} else {
				renewed = a.Claimer.Now()
			}
			if obs.Stale && !stale {
				a.emit(ctx, "Warning", "TunnelStale", fmt.Sprintf("node %s slot %d: every peer handshake is older than 180s", a.Claimer.Node, slot))
			}
			if !obs.Stale && stale {
				a.emit(ctx, "Normal", "TunnelRecovered", fmt.Sprintf("node %s slot %d: handshakes resumed", a.Claimer.Node, slot))
			}
			stale = obs.Stale
		}
		if err := a.Sleep(ctx, RenewInterval); err != nil {
			return nil
		}
	}
}
