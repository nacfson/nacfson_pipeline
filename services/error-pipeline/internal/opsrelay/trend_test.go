package opsrelay

import (
	"strings"
	"testing"
	"time"
)

func diskSeries(e *Engine, vm string, start time.Time, hours float64, from, perHour float64) time.Time {
	at := start
	for m := 0.0; m <= hours*60; m += 0.5 {
		at = start.Add(time.Duration(m * float64(time.Minute)))
		e.Metric(Metric{Source: "vm/" + vm, Metric: "os.disk.free_ratio", Value: from + perHour*m/60, Unit: "ratio", Time: at})
	}
	return at
}

func TestDiskTrendWarnsOnceWhenCrossingWithin72h(t *testing.T) {
	e := NewEngine(vms, 3, t0)
	diskSeries(e, "a1-2", t0, 7, 0.26, -0.001)
	msgs := e.Drain()
	assertMessages(t, msgs, "WARN disk/a1-2")
	if !strings.Contains(msgs[0].Text, "crosses 20% in 2.") {
		t.Fatalf("text = %q", msgs[0].Text)
	}
}

func TestDiskTrendSilentForSlowDecline(t *testing.T) {
	e := NewEngine(vms, 3, t0)
	diskSeries(e, "a1-1", t0, 7, 0.70, -0.001)
	assertMessages(t, e.Drain())
}

func TestDiskTrendNeedsSixHoursOfSamples(t *testing.T) {
	e := NewEngine(vms, 3, t0)
	diskSeries(e, "micro-1", t0, 5.9, 0.30, -0.02)
	assertMessages(t, e.Drain())
}

func TestBufferAboveHalfWarnsAndClears(t *testing.T) {
	e := NewEngine(vms, 3, t0)
	e.Metric(Metric{Source: "gke/node-a", Metric: "fluentbit.buffer.bytes", Value: 100 << 20, Time: t0})
	e.Metric(Metric{Source: "gke/node-a", Metric: "fluentbit.buffer.bytes", Value: 140 << 20, Time: t0.Add(30 * time.Second)})
	e.Metric(Metric{Source: "gke/node-a", Metric: "fluentbit.buffer.bytes", Value: 150 << 20, Time: t0.Add(time.Minute)})
	e.Metric(Metric{Source: "gke/node-a", Metric: "fluentbit.buffer.bytes", Value: 1 << 20, Time: t0.Add(2 * time.Minute)})
	assertMessages(t, e.Drain(), "WARN gke/node-a/buffer", "OK gke/node-a/buffer")
}

func TestDiskBelowTheFloorStaysWarnUntilItRecovers(t *testing.T) {
	e := NewEngine(vms, 3, t0)
	at := diskSeries(e, "micro-1", t0, 6.5, 0.24, -0.01)
	e.Event(Event{Kind: "disk.low", State: "warn", Subject: "disk/micro-1", VM: "micro-1", ObservedBy: "micro-1", Time: at})
	for i := 1; i <= 4; i++ {
		e.Metric(Metric{Source: "vm/micro-1", Metric: "os.disk.free_ratio", Value: 0.18, Time: at.Add(time.Duration(i) * 30 * time.Second)})
	}
	msgs := e.Drain()
	for _, m := range msgs {
		if m.Level == OK {
			t.Fatalf("OK sent while the disk is below 20%%: %v", msgs)
		}
	}
}
