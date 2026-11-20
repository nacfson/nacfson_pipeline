package opsrelay

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	TrendWindow      = 6 * time.Hour
	TrendHorizon     = 72 * time.Hour
	DiskFloor        = 0.20
	BufferCapBytes   = 256 << 20
	BufferWarnRatio  = 0.5
	trendRetainExtra = 10 * time.Minute
)

type point struct {
	at    time.Time
	value float64
}

type Verdict struct {
	Subject string
	Level   Level
	Text    string
}

type Trends struct {
	disk   map[string][]point
	buffer map[string]float64
}

func NewTrends() *Trends {
	return &Trends{disk: map[string][]point{}, buffer: map[string]float64{}}
}

func (t *Trends) Add(m Metric) {
	switch {
	case m.Metric == "os.disk.free_ratio" && strings.HasPrefix(m.Source, "vm/"):
		vm := strings.TrimPrefix(m.Source, "vm/")
		series := append(t.disk[vm], point{m.Time, m.Value})
		cut := 0
		for cut < len(series) && m.Time.Sub(series[cut].at) > TrendWindow+trendRetainExtra {
			cut++
		}
		t.disk[vm] = series[cut:]
	case m.Metric == "fluentbit.buffer.bytes" && strings.HasPrefix(m.Source, "gke/"):
		t.buffer[strings.TrimPrefix(m.Source, "gke/")] = m.Value
	}
}

func (t *Trends) Forget(node string) {
	delete(t.buffer, node)
}

func slopePerHour(series []point) float64 {
	n := float64(len(series))
	var sx, sy, sxx, sxy float64
	t0 := series[0].at
	for _, p := range series {
		x := p.at.Sub(t0).Hours()
		sx += x
		sy += p.value
		sxx += x * x
		sxy += x * p.value
	}
	den := n*sxx - sx*sx
	if den == 0 {
		return 0
	}
	return (n*sxy - sx*sy) / den
}

func (t *Trends) diskVerdict(vm string, now time.Time) (Verdict, bool) {
	series := t.disk[vm]
	if len(series) < 2 || now.Sub(series[0].at) < TrendWindow {
		return Verdict{}, false
	}
	var window []point
	for _, p := range series {
		if now.Sub(p.at) <= TrendWindow {
			window = append(window, p)
		}
	}
	if len(window) < 2 {
		return Verdict{}, false
	}
	current := window[len(window)-1].value
	slope := slopePerHour(window)
	subject := "disk/" + vm
	if current <= DiskFloor {
		return Verdict{subject, Warn, fmt.Sprintf("%.0f%% free, below 20%%", current*100)}, true
	}
	if current > DiskFloor && slope < 0 {
		hours := (current - DiskFloor) / -slope
		if hours <= TrendHorizon.Hours() {
			return Verdict{subject, Warn, fmt.Sprintf("%.0f%% free, at the current rate crosses 20%% in %.1f days", current*100, hours/24)}, true
		}
	}
	return Verdict{subject, OK, fmt.Sprintf("%.0f%% free, no crossing of 20%% within 3 days", current*100)}, true
}

func (t *Trends) Verdicts(now time.Time) []Verdict {
	var out []Verdict
	vms := make([]string, 0, len(t.disk))
	for vm := range t.disk {
		vms = append(vms, vm)
	}
	sort.Strings(vms)
	for _, vm := range vms {
		if v, ok := t.diskVerdict(vm, now); ok {
			out = append(out, v)
		}
	}
	nodes := make([]string, 0, len(t.buffer))
	for n := range t.buffer {
		nodes = append(nodes, n)
	}
	sort.Strings(nodes)
	for _, node := range nodes {
		used := t.buffer[node]
		subject := "gke/" + node + "/buffer"
		if used > BufferWarnRatio*BufferCapBytes {
			out = append(out, Verdict{subject, Warn, fmt.Sprintf("Fluent Bit buffer %.0f MiB of 256 MiB", used/(1<<20))})
		} else {
			out = append(out, Verdict{subject, OK, fmt.Sprintf("Fluent Bit buffer %.0f MiB of 256 MiB", used/(1<<20))})
		}
	}
	return out
}
