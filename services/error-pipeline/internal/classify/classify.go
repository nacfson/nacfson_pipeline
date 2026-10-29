// Package classify decides which project log lines are errors. It implements
// contracts/CONTRACT_LOG_SCHEMA.md version 2 (sections 2 to 4 and the legacy
// read in section 6) and nothing more: no heuristics, no logfmt, no free text.
// Redaction is not done here; it happens later, at render time.
package classify

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// maxFieldBytes bounds every string field of an Event and the raw text of a
// non-JSON line.
const maxFieldBytes = 64 << 10

// Source values of Event.Source.
const (
	SourceJSON  = "json"
	SourceCrash = "crash"
	SourceNginx = "nginx"
)

// Record is one stored log line as the error detector reads it from
// VictoriaLogs: the platform envelope of contract section 1.
type Record struct {
	Time      time.Time
	StreamID  string
	Project   string
	Pod       string
	Container string
	Node      string
	Message   string
}

// Event is the contract view of an error line. The JSON tags are the
// contract keys.
type Event struct {
	Level            string `json:"level"`
	Msg              string `json:"msg"`
	EventName        string `json:"event.name,omitempty"`
	TraceID          string `json:"trace_id,omitempty"`
	ExceptionType    string `json:"exception.type,omitempty"`
	ExceptionMessage string `json:"exception.message,omitempty"`
	Stacktrace       string `json:"exception.stacktrace,omitempty"`
	Fingerprint      string `json:"error.fingerprint,omitempty"`
	Source           string `json:"source"` // json, crash or nginx
}

// LineKind is the verdict of Line.
type LineKind int

const (
	// NotError is a recognised structured line that is not an error.
	NotError LineKind = iota
	// Error is a complete error event.
	Error
	// CrashStart is a runtime crash signature (contract section 4). The
	// following Plain lines are its stack trace.
	CrashStart
	// Plain is an unrecognised line. It is never an error by itself.
	Plain
)

func (k LineKind) String() string {
	switch k {
	case NotError:
		return "NotError"
	case Error:
		return "Error"
	case CrashStart:
		return "CrashStart"
	case Plain:
		return "Plain"
	}
	return fmt.Sprintf("LineKind(%d)", int(k))
}

// CrashSignatures are the line prefixes that mark a runtime crash
// (contract section 4, "Runtime crash signatures").
var CrashSignatures = []string{
	"panic:",
	"fatal error:",
	"Traceback (most recent call last):",
	"Unhandled rejection",
	"Uncaught",
	`Exception in thread "`,
}

// NginxLevels maps the bracketed level of a native nginx error-log line to a
// contract level (contract section 4, "Third-party server formats"). Any
// other nginx level is not an error.
var NginxLevels = map[string]string{
	"error": "error",
	"crit":  "fatal",
	"alert": "fatal",
	"emerg": "fatal",
}

var nginxLine = regexp.MustCompile(`^\d{4}/\d\d/\d\d \d\d:\d\d:\d\d \[(\w+)\] ?`)

var levelAliases = map[string]string{
	"warning":  "warn",
	"critical": "fatal",
}

var knownLevels = map[string]bool{
	"debug": true, "info": true, "warn": true, "error": true, "fatal": true,
}

// truncate cuts s to at most n bytes without splitting a UTF-8 sequence.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// Line classifies one raw message. It is pure.
//
// A message that starts with "{" and is one valid JSON object is a contract
// section 2 line: it is an Error when its level is error or fatal, and
// NotError otherwise (including a missing or unknown level). Otherwise a
// crash signature prefix gives CrashStart, a native nginx line gives Error or
// NotError by NginxLevels, and everything else is Plain.
//
// A JSON line is parsed whole, because a truncated object is not valid JSON;
// each string field of the resulting Event is then cut to 64 KiB. For every
// other format only the first 64 KiB of the message is considered.
func Line(message string) (Event, LineKind) {
	if strings.HasPrefix(message, "{") {
		if ev, kind, ok := jsonLine(message); ok {
			return ev, kind
		}
	}
	message = truncate(message, maxFieldBytes)
	for _, sig := range CrashSignatures {
		if strings.HasPrefix(message, sig) {
			return Event{Level: "fatal", Msg: message, Source: SourceCrash}, CrashStart
		}
	}
	if m := nginxLine.FindStringSubmatch(message); m != nil {
		level, ok := NginxLevels[m[1]]
		if !ok {
			return Event{}, NotError
		}
		return Event{Level: level, Msg: message[len(m[0]):], Source: SourceNginx}, Error
	}
	return Event{}, Plain
}

func jsonLine(message string) (Event, LineKind, bool) {
	dec := json.NewDecoder(strings.NewReader(message))
	dec.UseNumber()
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil {
		return Event{}, Plain, false
	}
	if _, err := dec.Token(); err != io.EOF {
		return Event{}, Plain, false // trailing data: not one JSON object
	}
	flat := map[string]any{}
	flatten("", obj, flat)

	level := strings.ToLower(str(flat, "level"))
	if alias, ok := levelAliases[level]; ok {
		level = alias
	}
	if !knownLevels[level] {
		return Event{}, NotError, true
	}
	ev := Event{
		Level:            level,
		Msg:              msgField(flat),
		EventName:        str(flat, "event.name"),
		TraceID:          str(flat, "trace_id"),
		ExceptionType:    str(flat, "exception.type"),
		ExceptionMessage: str(flat, "exception.message"),
		Stacktrace:       str(flat, "exception.stacktrace"),
		Fingerprint:      str(flat, "error.fingerprint"),
		Source:           SourceJSON,
	}
	if ev.EventName == "" {
		ev.EventName = str(flat, "kind") // contract section 6: legacy kind
	}
	if level == "error" || level == "fatal" {
		return ev, Error, true
	}
	return ev, NotError, true
}

// flatten joins nested object keys with "." into out. Arrays are kept as
// opaque values. On a name collision (a dotted key and a nested object) the
// first name in sorted key order wins, so the result is deterministic.
func flatten(prefix string, in map[string]any, out map[string]any) {
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		name := k
		if prefix != "" {
			name = prefix + "." + k
		}
		if sub, ok := in[k].(map[string]any); ok {
			flatten(name, sub, out)
			continue
		}
		if _, dup := out[name]; !dup {
			out[name] = in[k]
		}
	}
}

// str returns a string field truncated to 64 KiB. Non-strings give "".
func str(flat map[string]any, key string) string {
	s, _ := flat[key].(string)
	return truncate(s, maxFieldBytes)
}

// msgField is str for "msg", except that a number or bool is formatted.
func msgField(flat map[string]any) string {
	switch v := flat["msg"].(type) {
	case string:
		return truncate(v, maxFieldBytes)
	case json.Number, bool:
		return truncate(fmt.Sprint(v), maxFieldBytes)
	}
	return ""
}
