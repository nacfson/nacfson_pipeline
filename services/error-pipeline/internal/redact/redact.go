package redact

import (
	"regexp"
	"slices"
	"sort"
	"strings"
)

const placeholder = "[REDACTED]"

const (
	KindBearer      = "bearer"
	KindJWT         = "jwt"
	KindGoogleKey   = "google-key"
	KindSKKey       = "sk-key"
	KindSecretField = "secret-field"
)

var Kinds = []string{
	KindBearer,
	KindGoogleKey,
	KindJWT,
	KindSecretField,
	KindSKKey,
}

const sensitiveKey = `(?:pass|secret|token|api[_-]?key|authorization|cookie|credential|private[_-]?key|access[_-]?key)`

const (
	anySep    = `(?:=>|:=|==|=|:)`
	assignSep = `(?:=>|==|=)`
)

var (
	bearer = regexp.MustCompile(`(?i)\bBearer\s+[^\s"']+`)

	quotedKV = regexp.MustCompile(`(?i)((?:"[^"\n]*` + sensitiveKey + `[^"\n]*"|'[^'\n]*` + sensitiveKey + `[^'\n]*')\s*` + anySep + `\s*)` +
		`(?:"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|"[^\n]*|'[^\n]*|[\[{][^\n]*|[^\s,}\]\[{"'][^\s,}\]]*)`)

	headerKV = regexp.MustCompile(`(?i)([\w.-]*` + sensitiveKey + `[\w.-]*[ \t]*:[ \t]*)[^\r\n]+`)

	assignKV = regexp.MustCompile(`(?i)([\w.-]*` + sensitiveKey + `[\w.-]*[ \t]*` + assignSep + `[ \t]*)` +
		`(?:"(?:\\.|[^"\\\n])*"|'[^'\n]*'|"[^\n]*|'[^\n]*|[^\s&,;]+)`)

	jwt    = regexp.MustCompile(`eyJ[\w-]+\.[\w-]+\.[\w-]+`)
	google = regexp.MustCompile(`AIza[\w-]{30,}`)
	skKey  = regexp.MustCompile(`\bsk-[\w-]{16,}`)
)

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
