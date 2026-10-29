package classify

import (
	"sort"
	"strings"
	"time"
)

// Classified is an error event together with the record that produced it.
// For a crash the record is the signature line.
type Classified struct {
	Record Record
	Event  Event
}

// defaultMaxLines is the stack-trace line cap used when NewAssembler gets a
// non-positive maxLines.
const defaultMaxLines = 200

type openCrash struct {
	rec   Record    // signature record
	ev    Event     // signature event
	lines []string  // stack lines collected so far
	bytes int       // bytes held in lines
	count int       // lines seen, including ones not stored
	last  time.Time // Record.Time of the last appended line, or of the signature
}

// Assembler turns the record stream into Classified errors. It joins a crash
// signature with the plain lines that follow it. It is not safe for
// concurrent use.
type Assembler struct {
	maxLines int
	maxAge   time.Duration
	open     map[string]*openCrash
}

// NewAssembler returns an Assembler that keeps at most maxLines stack lines
// per crash (200 when maxLines <= 0) and that Flush closes after maxAge of
// silence.
func NewAssembler(maxLines int, maxAge time.Duration) *Assembler {
	if maxLines <= 0 {
		maxLines = defaultMaxLines
	}
	return &Assembler{maxLines: maxLines, maxAge: maxAge, open: map[string]*openCrash{}}
}

// Feed consumes one record and returns the errors it completes. The order is
// deterministic: a crash closed by this line comes first, then the event of
// this line itself.
//
// There is at most one open crash per pod+"/"+container.
//   - A CrashStart line closes the open crash of its key and opens a new one.
//   - A Plain line is appended to the open crash of its key, or dropped when
//     there is none. The maxLines-th appended line closes the crash.
//   - Any other line (a JSON line of any level, or a native nginx line)
//     closes the open crash of its key. If it is an Error it is returned too.
func (a *Assembler) Feed(r Record) []Classified {
	ev, kind := Line(r.Message)
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

// Flush closes and returns every open crash whose last appended line
// (Record.Time) is at or before now-maxAge, ordered by signature time and
// then by key.
//
// now is a watermark in record time, not the wall clock: the caller passes
// the newest Record.Time it has fully read from the log store, so a crash is
// never cut short because the reader is behind.
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
		room-- // the "\n" separator
	}
	if room <= 0 {
		return // the stacktrace field is full; still counts toward maxLines
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
