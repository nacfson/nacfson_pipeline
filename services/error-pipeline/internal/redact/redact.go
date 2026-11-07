// Package redact removes credentials from text before it leaves the cluster
// (Discord notifications, AI provider requests) and before storage in VictoriaLogs.
// It is deliberately biased toward over-redaction: losing a little context is cheaper
// than leaking a secret.
package redact

import (
	"regexp"
	"slices"
	"sort"
	"strings"
)

const placeholder = "[REDACTED]"

// Redaction kinds per spec FR-017 and data-model §8.
const (
	KindBearer      = "bearer"
	KindJWT         = "jwt"
	KindGoogleKey   = "google-key"
	KindSKKey       = "sk-key"
	KindSecretField = "secret-field"
)

// Kinds lists all valid redaction kinds in canonical sorted order.
var Kinds = []string{
	KindBearer,
	KindGoogleKey,
	KindJWT,
	KindSecretField,
	KindSKKey,
}

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

// Redact returns s with credentials replaced by "[REDACTED:<kind>]" and a sorted,
// deduplicated slice of all kind markers that were hit.
func Redact(s string) (string, []string) {
	var kinds []string
	addKind := func(k string) {
		if !slices.Contains(kinds, k) {
			kinds = append(kinds, k)
		}
	}

	if bearer.MatchString(s) {
		s = bearer.ReplaceAllString(s, "[REDACTED:"+KindBearer+"]")
		addKind(KindBearer)
	}

	// quotedKV: replace value unless already redacted with a kind or placeholder
	s = quotedKV.ReplaceAllStringFunc(s, func(match string) string {
		sub := quotedKV.FindStringSubmatch(match)
		if len(sub) < 2 {
			return match
		}
		prefix := sub[1]
		val := match[len(prefix):]
		if strings.HasPrefix(val, `"[REDACTED`) || strings.HasPrefix(val, `'[REDACTED`) {
			return match
		}
		addKind(KindSecretField)
		return prefix + `"[REDACTED:` + KindSecretField + `]"`
	})

	// headerKV: replace value unless already redacted
	s = headerKV.ReplaceAllStringFunc(s, func(match string) string {
		sub := headerKV.FindStringSubmatch(match)
		if len(sub) < 2 {
			return match
		}
		prefix := sub[1]
		val := strings.TrimSpace(match[len(prefix):])
		if strings.HasPrefix(val, `[REDACTED`) {
			return match
		}
		addKind(KindSecretField)
		return prefix + `[REDACTED:` + KindSecretField + `]`
	})

	// assignKV: replace value unless already redacted
	s = assignKV.ReplaceAllStringFunc(s, func(match string) string {
		sub := assignKV.FindStringSubmatch(match)
		if len(sub) < 2 {
			return match
		}
		prefix := sub[1]
		val := match[len(prefix):]
		if strings.HasPrefix(val, `[REDACTED`) || strings.HasPrefix(val, `"[REDACTED`) || strings.HasPrefix(val, `'[REDACTED`) {
			return match
		}
		addKind(KindSecretField)
		return prefix + `[REDACTED:` + KindSecretField + `]`
	})

	if jwt.MatchString(s) {
		s = jwt.ReplaceAllString(s, "[REDACTED:"+KindJWT+"]")
		addKind(KindJWT)
	}
	if google.MatchString(s) {
		s = google.ReplaceAllString(s, "[REDACTED:"+KindGoogleKey+"]")
		addKind(KindGoogleKey)
	}
	if skKey.MatchString(s) {
		s = skKey.ReplaceAllString(s, "[REDACTED:"+KindSKKey+"]")
		addKind(KindSKKey)
	}

	sort.Strings(kinds)
	return s, kinds
}
