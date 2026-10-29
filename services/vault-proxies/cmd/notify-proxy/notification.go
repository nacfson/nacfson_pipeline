package main

import (
	"errors"
	"fmt"
	"unicode/utf8"
)

const (
	maxTitleLen       = 256
	maxFields         = 25
	maxFieldNameLen   = 256
	maxFieldValueLen  = 1024
	maxAttachmentSize = 1 << 20 // 1 MiB
)

// Notification is the destination-neutral message accepted by the notify
// proxy. Callers never see the destination (Discord today).
type Notification struct {
	Title      string      `json:"title"`
	Severity   string      `json:"severity"`
	Fields     []Field     `json:"fields"`
	Body       string      `json:"body"`
	Attachment *Attachment `json:"attachment,omitempty"`
}

type Field struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type Attachment struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

// Validate enforces the limits shared with the Discord renderer.
func (n Notification) Validate() error {
	if l := utf8.RuneCountInString(n.Title); l < 1 || l > maxTitleLen {
		return fmt.Errorf("title must be 1-%d characters", maxTitleLen)
	}
	switch n.Severity {
	case "error", "fatal", "info":
	default:
		return fmt.Errorf("severity %q must be error, fatal or info", n.Severity)
	}
	if len(n.Fields) > maxFields {
		return fmt.Errorf("at most %d fields allowed", maxFields)
	}
	for i, f := range n.Fields {
		if utf8.RuneCountInString(f.Name) > maxFieldNameLen {
			return fmt.Errorf("field %d name exceeds %d characters", i, maxFieldNameLen)
		}
		if utf8.RuneCountInString(f.Value) > maxFieldValueLen {
			return fmt.Errorf("field %d value exceeds %d characters", i, maxFieldValueLen)
		}
	}
	// An oversize body is uploaded as body.md, so it shares the attachment cap.
	if len(n.Body) > maxAttachmentSize {
		return errors.New("body exceeds 1 MiB")
	}
	if n.Attachment != nil {
		if len(n.Attachment.Content) > maxAttachmentSize {
			return errors.New("attachment content exceeds 1 MiB")
		}
	}
	return nil
}
