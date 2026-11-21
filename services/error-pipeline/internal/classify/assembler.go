package classify

import (
	"sort"
	"strings"
	"time"
)

type Classified struct {
	Record Record
	Event  Event
}

const defaultMaxLines = 200

type openCrash struct {
	rec   Record
	ev    Event
	lines []string
	bytes int
	count int
	last  time.Time
}

type Assembler struct {
	maxLines int
	maxAge   time.Duration
	open     map[string]*openCrash
}

func NewAssembler(maxLines int, maxAge time.Duration) *Assembler {
	if maxLines <= 0 {
		maxLines = defaultMaxLines
	}
	return &Assembler{maxLines: maxLines, maxAge: maxAge, open: map[string]*openCrash{}}
}

func (a *Assembler) Feed(r Record) []Classified {
	var ev Event
	var kind LineKind
	if r.Parsed && r.Level != "" {
		ev, kind = EventFromRecord(r)
	} else {
		ev, kind = Line(r.Message)
	}
	key := r.Pod + "/" + r.Container
	var out []Classified

	switch kind {
	case Plain:
		c := a.open[key]
		if c == nil {
			return nil
		}
		a.append(c, r)
		if c.count >= a.maxLines {
			out = append(out, a.close(key))
		}
		return out
	case CrashStart:
		if c := a.open[key]; c != nil {
			out = append(out, a.close(key))
		}
		a.open[key] = &openCrash{rec: r, ev: ev, last: r.Time}
		return out
	}

	if a.open[key] != nil {
		out = append(out, a.close(key))
	}
	if kind == Error {
		out = append(out, Classified{Record: r, Event: ev})
	}
	return out
}

func (a *Assembler) OpenCount() int {
	return len(a.open)
}

func (a *Assembler) FlushAll() []Classified {
	keys := make([]string, 0, len(a.open))
	for k := range a.open {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		ci, cj := a.open[keys[i]], a.open[keys[j]]
		if !ci.rec.Time.Equal(cj.rec.Time) {
			return ci.rec.Time.Before(cj.rec.Time)
		}
		return keys[i] < keys[j]
	})
	var out []Classified
	for _, k := range keys {
		out = append(out, a.close(k))
	}
	return out
}

func (a *Assembler) Flush(now time.Time) []Classified {
	cutoff := now.Add(-a.maxAge)
	var keys []string
	for k, c := range a.open {
		if !c.last.After(cutoff) {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		ci, cj := a.open[keys[i]], a.open[keys[j]]
		if !ci.rec.Time.Equal(cj.rec.Time) {
			return ci.rec.Time.Before(cj.rec.Time)
		}
		return keys[i] < keys[j]
	})
	var out []Classified
	for _, k := range keys {
		out = append(out, a.close(k))
	}
	return out
}

func (a *Assembler) append(c *openCrash, r Record) {
	c.count++
	c.last = r.Time
	room := maxFieldBytes - c.bytes
	if len(c.lines) > 0 {
		room--
	}
	if room <= 0 {
		return
	}
	line := truncate(r.Message, room)
	c.lines = append(c.lines, line)
	c.bytes += len(line)
	if len(c.lines) > 1 {
		c.bytes++
	}
}

func (a *Assembler) close(key string) Classified {
	c := a.open[key]
	delete(a.open, key)
	ev := c.ev
	ev.Stacktrace = truncate(strings.Join(c.lines, "\n"), maxFieldBytes)
	return Classified{Record: c.rec, Event: ev}
}
