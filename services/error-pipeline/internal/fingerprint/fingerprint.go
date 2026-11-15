package fingerprint

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"

	"nacfson_pipeline/services/error-pipeline/internal/classify"
)

var (
	timeRE = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})?`)
	uuidRE = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)

	hexRE = regexp.MustCompile(`(?i)\b[0-9a-f]{8,}\b`)
	numRE = regexp.MustCompile(`\d+`)
)

func Normalize(s string) string {
	s = timeRE.ReplaceAllString(s, "<time>")
	s = uuidRE.ReplaceAllString(s, "<uuid>")
	s = hexRE.ReplaceAllString(s, "<hex>")
	s = numRE.ReplaceAllString(s, "<n>")
	return strings.ToLower(s)
}

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
