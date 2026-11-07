// Package conform checks log lines against contracts/CONTRACT_LOG_SCHEMA.md v3.
// It is used by project CI (cmd/logcheck), the error detector, and repository tests.
package conform

// Class represents the classified type of a log line.
type Class string

const (
	ClassJSON       Class = "json"
	ClassCrashGo    Class = "crash:go"
	ClassCrashPy    Class = "crash:python"
	ClassCrashJVM   Class = "crash:jvm"
	ClassNginxError Class = "nginx-error:error"
	ClassNginxFatal Class = "nginx-error:fatal"
	ClassOther      Class = "other"
)

// Violation represents a broken log contract rule.
type Violation struct {
	Line      int    `json:"line,omitempty"`
	Project   string `json:"project,omitempty"`
	Container string `json:"container,omitempty"`
	Section   string `json:"section"`
	Kind      string `json:"kind"`
	Text      string `json:"text"`
}

// Options configures line checking.
type Options struct {
	ExpectVersion string // e.g. "v1.4.2" or "v0.0.0-3f2a9c1"
	ExpectService string // e.g. "pn-api"
	Format        string // "json" or "nginx" (default: "json")
}
