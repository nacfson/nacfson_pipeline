package audit

import (
	"encoding/json"
	"io"
	"sync"
	"time"

	"nacfson_pipeline/services/debug-mcp/internal/redact"
)

type Entry struct {
	Timestamp     string   `json:"ts"`
	Tool          string   `json:"tool"`
	Namespace     string   `json:"namespace,omitempty"`
	Target        string   `json:"target,omitempty"`
	DurationMs    int64    `json:"duration_ms"`
	Status        string   `json:"status"`
	BytesOut      int      `json:"bytes_out"`
	RedactedKinds []string `json:"redacted_kinds,omitempty"`
	Error         string   `json:"error,omitempty"`
}

type Logger struct {
	mu sync.Mutex
	w  io.Writer
}

func NewLogger(w io.Writer) *Logger {
	return &Logger{w: w}
}

func (l *Logger) Log(entry Entry) {
	if l == nil || l.w == nil {
		return
	}
	if entry.Timestamp == "" {
		entry.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if entry.Error != "" {
		cleanErr, _ := redact.Redact(entry.Error)
		entry.Error = cleanErr
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return
	}
	line = append(line, '\n')
	l.mu.Lock()
	_, _ = l.w.Write(line)
	l.mu.Unlock()
}
