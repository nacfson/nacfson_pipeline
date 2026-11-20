package opsrelay

import (
	"sort"
	"time"
)

const (
	RebuildEvents  = 24 * time.Hour
	RebuildMetrics = 6 * time.Hour
)

func (e *Engine) Rebuild(events []Event, metrics []Metric) {
	type item struct {
		at     time.Time
		event  *Event
		metric *Metric
	}
	items := make([]item, 0, len(events)+len(metrics))
	for i := range events {
		items = append(items, item{at: events[i].Time, event: &events[i]})
	}
	for i := range metrics {
		items = append(items, item{at: metrics[i].Time, metric: &metrics[i]})
	}
	sort.SliceStable(items, func(a, b int) bool { return items[a].at.Before(items[b].at) })
	e.Replay(func() {
		for _, it := range items {
			if it.event != nil {
				e.Event(*it.event)
			} else {
				e.Metric(*it.metric)
			}
		}
	})
}

func (e *Engine) Replay(fn func()) {
	e.replaying = true
	e.touched = map[string]bool{}
	fn()
	e.replaying = false
	keys := make([]string, 0, len(e.touched))
	for k := range e.touched {
		keys = append(keys, k)
	}
	e.touched = nil
	sort.Strings(keys)
	for _, key := range keys {
		s := e.subjects[key]
		if s.sent == s.level {
			continue
		}
		s.sent = s.level
		e.out = append(e.out, Message{Level: s.level, Subject: key, Text: s.text, At: s.since, Delayed: true})
	}
}
