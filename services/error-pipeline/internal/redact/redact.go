// Package redact removes credentials from text before it leaves the cluster
// (Discord notifications, AI provider requests). It is deliberately biased
// toward over-redaction: losing a little context is cheaper than leaking a
// secret.
package redact

import "regexp"

const placeholder = "[REDACTED]"

// sensitiveKey matches key names that contain a credential word, e.g.
// db_password, X-Api-Key, refresh_token, client_secret, credentials,
// passphrase. Matching is case-insensitive via the (?i) flag on each regexp
// that embeds it. "pass" also covers password, passwd and passphrase.
const sensitiveKey = `(?:pass|secret|token|api[_-]?key|authorization|cookie|credential|private[_-]?key|access[_-]?key)`

// Separators between key and value: "=", "==", "=>", ":", ":=".
const (
	anySep    = `(?:=>|:=|==|=|:)`
	assignSep = `(?:=>|==|=)`
)

var (
	// Bearer credentials are redacted first so that `authorization=Bearer x`
	// does not leave the token behind once a key=value pass consumed "Bearer".
	bearer = regexp.MustCompile(`(?i)\bBearer\s+[^\s"']+`)

	// Quoted keys (JSON and Python-repr): "key": value, 'key': value. Group 1
	// keeps the key and separator; the value is replaced by "[REDACTED]".
	// Value forms, tried in order: complete string, unterminated string (to
	// end of line, e.g. truncated JSON), array/object (to end of line, so
	// nested content cannot leak), bare scalar (123, true, hunter2).
	quotedKV = regexp.MustCompile(`(?i)((?:"[^"\n]*` + sensitiveKey + `[^"\n]*"|'[^'\n]*` + sensitiveKey + `[^'\n]*')\s*` + anySep + `\s*)` +
		`(?:"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|"[^\n]*|'[^\n]*|[\[{][^\n]*|[^\s,}\]\[{"'][^\s,}\]]*)`)

	// Key: value header style; the value runs to the end of the line.
	headerKV = regexp.MustCompile(`(?i)([\w.-]*` + sensitiveKey + `[\w.-]*[ \t]*:[ \t]*)[^\r\n]+`)

	// key=value (query strings, env dumps, logfmt), optionally with spaces
	// around "=". The value ends at whitespace, '&', ',' or ';'. Quoted
	// values may contain those characters; an unterminated quote runs to the
	// end of the line.
	assignKV = regexp.MustCompile(`(?i)([\w.-]*` + sensitiveKey + `[\w.-]*[ \t]*` + assignSep + `[ \t]*)` +
		`(?:"(?:\\.|[^"\\\n])*"|'[^'\n]*'|"[^\n]*|'[^\n]*|[^\s&,;]+)`)

	jwt    = regexp.MustCompile(`eyJ[\w-]+\.[\w-]+\.[\w-]+`)
	google = regexp.MustCompile(`AIza[\w-]{30,}`)
	skKey  = regexp.MustCompile(`\bsk-[\w-]{16,}`)
)

// String returns s with credentials replaced by "[REDACTED]". It is pure and
// safe for concurrent use.
func String(s string) string {
	s = bearer.ReplaceAllString(s, placeholder)
	s = quotedKV.ReplaceAllString(s, `${1}"`+placeholder+`"`)
	s = headerKV.ReplaceAllString(s, `${1}`+placeholder)
	s = assignKV.ReplaceAllString(s, `${1}`+placeholder)
	s = jwt.ReplaceAllString(s, placeholder)
	s = google.ReplaceAllString(s, placeholder)
	s = skKey.ReplaceAllString(s, placeholder)
	return s
}
