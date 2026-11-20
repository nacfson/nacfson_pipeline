package opsrelay

import (
	"sort"
	"time"
)

type SlotChange struct {
	Node  string
	Slot  int
	Holds bool
	Since time.Time
}

type HeldLease struct {
	Holder   string
	Slot     int
	Acquired time.Time
	Expires  time.Time
}

type SlotHolders struct {
	leases   map[string]HeldLease
	reported map[string]HeldLease
}

func NewSlotHolders() *SlotHolders {
	return &SlotHolders{leases: map[string]HeldLease{}, reported: map[string]HeldLease{}}
}

func leaseTime(s *string) time.Time {
	if s == nil {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, *s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func LeaseHeld(l Lease) (HeldLease, bool) {
	slot, ok := SlotOf(l)
	if !ok {
		return HeldLease{}, false
	}
	h := HeldLease{Slot: slot, Acquired: leaseTime(l.Spec.AcquireTime)}
	if l.Spec.HolderIdentity != nil {
		h.Holder = *l.Spec.HolderIdentity
	}
	if renewed := leaseTime(l.Spec.RenewTime); !renewed.IsZero() {
		d := 90 * time.Second
		if l.Spec.LeaseDurationSeconds != nil {
			d = time.Duration(*l.Spec.LeaseDurationSeconds) * time.Second
		}
		h.Expires = renewed.Add(d)
	}
	return h, true
}

func (s *SlotHolders) Set(lease string, h HeldLease, now time.Time) []SlotChange {
	if h.Holder == "" {
		delete(s.leases, lease)
	} else {
		s.leases[lease] = h
	}
	return s.Sync(now)
}

func (s *SlotHolders) Remove(lease string, now time.Time) []SlotChange {
	delete(s.leases, lease)
	return s.Sync(now)
}

func (s *SlotHolders) Replace(leases map[string]HeldLease, now time.Time) []SlotChange {
	s.leases = map[string]HeldLease{}
	for name, h := range leases {
		if h.Holder != "" {
			s.leases[name] = h
		}
	}
	return s.Sync(now)
}

func (s *SlotHolders) Sync(now time.Time) []SlotChange {
	holding := map[string]HeldLease{}
	names := make([]string, 0, len(s.leases))
	for name := range s.leases {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		l := s.leases[name]
		if !l.Expires.IsZero() && !now.Before(l.Expires) {
			continue
		}
		if _, ok := holding[l.Holder]; !ok {
			holding[l.Holder] = l
		}
	}
	var changes []SlotChange
	for node, l := range holding {
		if was, ok := s.reported[node]; !ok || was.Slot != l.Slot || !was.Acquired.Equal(l.Acquired) {
			changes = append(changes, SlotChange{Node: node, Slot: l.Slot, Holds: true, Since: l.Acquired})
		}
	}
	for node, l := range s.reported {
		if _, ok := holding[node]; !ok {
			changes = append(changes, SlotChange{Node: node, Slot: l.Slot, Holds: false})
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Node < changes[j].Node })
	s.reported = holding
	return changes
}
