// Package fingerprint derives a stable identity for an error so that repeated
// occurrences of the same error collapse into one alert. Volatile parts of the
// message (timestamps, IDs, numbers) are normalised away; the project and
// container are always part of the identity.
package fingerprint

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"

	"nacfson_pipeline/services/error-pipeline/internal/classify"
)

var (
	// RFC 3339 timestamp: date T time, optional fraction, optional Z or offset.
	timeRE = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})?`)
	uuidRE = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	// A run of 8 or more hex digits bounded by non-word characters.
	hexRE = regexp.MustCompile(`(?i)\b[0-9a-f]{8,}\b`)
	numRE = regexp.MustCompile(`\d+`)
)

// Normalize replaces the volatile parts of s with placeholders, in order:
// RFC 3339 timestamps with <time>, UUIDs with <uuid>, hex runs of 8 or more
// characters with <hex>, digit runs with <n>; then it lowercases the result.
// The placeholders contain no digits and are already lowercase, so later
// passes leave them intact.
func Normalize(s string) string {
	s = timeRE.ReplaceAllString(s, "<time>")
	s = uuidRE.ReplaceAllString(s, "<uuid>")
	s = hexRE.ReplaceAllString(s, "<hex>")
	s = numRE.ReplaceAllString(s, "<n>")
	return strings.ToLower(s)
}

// Of returns the fingerprint of a classified error. A fingerprint set by the
// project (error.fingerprint) wins, trimmed of surrounding whitespace.
// Otherwise it is the first 16 hex characters of
// sha256(project|container|exceptionType|Normalize(text)), where text is the
// exception message, falling back to msg. For a runtime crash classify puts
// the signature line in msg, so that fallback covers it.
func Of(c classify.Classified) string {
	if fp := strings.TrimSpace(c.Event.Fingerprint); fp != "" {
		return fp
	}
	text := c.Event.ExceptionMessage
	if text == "" {
		text = c.Event.Msg
	}
	key := c.Record.Project + "|" + c.Record.Container + "|" + c.Event.ExceptionType + "|" + Normalize(text)
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:8])
}
