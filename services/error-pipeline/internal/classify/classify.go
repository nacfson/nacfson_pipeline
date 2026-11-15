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

const maxFieldBytes = 64 << 10

const (
	SourceJSON  = "json"
	SourceCrash = "crash"
	SourceNginx = "nginx"
)

type Record struct {
	Time      time.Time
	StreamID  string
	Project   string
	Pod       string
	Container string
	Node      string
	Message   string

	Parsed           bool
	Level            string
	Msg              string
	EventName        string
	ServiceVersion   string
	TraceID          string
	ExceptionType    string
	ExceptionMessage string
	Stacktrace       string
	Fingerprint      string
	TimeRejected     bool
	DynamicKey       bool
}

type Event struct {
	Level            string `json:"level"`
	Msg              string `json:"msg"`
	EventName        string `json:"event.name,omitempty"`
	TraceID          string `json:"trace_id,omitempty"`
	ExceptionType    string `json:"exception.type,omitempty"`
	ExceptionMessage string `json:"exception.message,omitempty"`
	Stacktrace       string `json:"exception.stacktrace,omitempty"`
	Fingerprint      string `json:"error.fingerprint,omitempty"`
	Source           string `json:"source"`
}

type LineKind int

const (
	NotError LineKind = iota

	Error

	CrashStart

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

var CrashSignatures = []string{
	"panic:",
	"fatal error:",
	"Traceback (most recent call last):",
	`Exception in thread "`,
}

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

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

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

func EventFromRecord(r Record) (Event, LineKind) {
	level := strings.ToLower(r.Level)
	if alias, ok := levelAliases[level]; ok {
		level = alias
	}
	if !knownLevels[level] {
		return Event{}, NotError
	}
	ev := Event{
		Level:            level,
		Msg:              truncate(r.Msg, maxFieldBytes),
		EventName:        truncate(r.EventName, maxFieldBytes),
		TraceID:          truncate(r.TraceID, maxFieldBytes),
		ExceptionType:    truncate(r.ExceptionType, maxFieldBytes),
		ExceptionMessage: truncate(r.ExceptionMessage, maxFieldBytes),
		Stacktrace:       truncate(r.Stacktrace, maxFieldBytes),
		Fingerprint:      truncate(r.Fingerprint, maxFieldBytes),
		Source:           SourceJSON,
	}
	if level == "error" || level == "fatal" {
		return ev, Error
	}
	return ev, NotError
}

func jsonLine(message string) (Event, LineKind, bool) {
	dec := json.NewDecoder(strings.NewReader(message))
	dec.UseNumber()
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil {
		return Event{}, Plain, false
	}
	if _, err := dec.Token(); err != io.EOF {
		return Event{}, Plain, false
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
		ev.EventName = str(flat, "kind")
	}
	if level == "error" || level == "fatal" {
		return ev, Error, true
	}
	return ev, NotError, true
}

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

func str(flat map[string]any, key string) string {
	s, _ := flat[key].(string)
	return truncate(s, maxFieldBytes)
}

func msgField(flat map[string]any) string {
	switch v := flat["msg"].(type) {
	case string:
		return truncate(v, maxFieldBytes)
	case json.Number, bool:
		return truncate(fmt.Sprint(v), maxFieldBytes)
	}
	return ""
}
