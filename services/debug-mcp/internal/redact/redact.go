package redact

import (
	"regexp"
	"sort"
	"strings"
)

const Placeholder = "[REDACTED]"

const (
	KindBearer      = "bearer"
	KindJWT         = "jwt"
	KindGoogleKey   = "google-key"
	KindSKKey       = "sk-key"
	KindSecretField = "secret-field"
)

const sensitiveKey = `(?:pass|secret|token|api[_-]?key|authorization|cookie|credential|private[_-]?key|access[_-]?key)`

const (
	anySep    = `(?:=>|:=|==|=|:)`
	assignSep = `(?:=>|==|=)`
)

var (
	bearer   = regexp.MustCompile(`(?i)\bBearer\s+[^\s"']+`)
	quotedKV = regexp.MustCompile(`(?i)((?:"[^"\n]*` + sensitiveKey + `[^"\n]*"|'[^'\n]*` + sensitiveKey + `[^'\n]*')\s*` + anySep + `\s*)` +
		`(?:"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|"[^\n]*|'[^\n]*|[\[{][^\n]*|[^\s,}\]\[{"'][^\s,}\]]*)`)
	headerKV = regexp.MustCompile(`(?i)([\w.-]*` + sensitiveKey + `[\w.-]*[ \t]*:[ \t]*)[^\r\n]+`)
	assignKV = regexp.MustCompile(`(?i)([\w.-]*` + sensitiveKey + `[\w.-]*[ \t]*` + assignSep + `[ \t]*)` +
		`(?:"(?:\\.|[^"\\\n])*"|'[^'\n]*'|"[^\n]*|'[^\n]*|[^\s&,;]+)`)
	jwt    = regexp.MustCompile(`eyJ[\w-]+\.[\w-]+\.[\w-]+`)
	google = regexp.MustCompile(`AIza[\w-]{30,}`)
	skKey  = regexp.MustCompile(`\bsk-[\w-]{16,}`)
)

type EnvVarInput struct {
	Name      string
	Value     string
	ValueFrom *EnvVarSourceInput
}

type EnvVarSourceInput struct {
	ConfigMapKeyRef  *ObjectKeySelector
	SecretKeyRef     *ObjectKeySelector
	FieldRef         *FieldSelector
	ResourceFieldRef *ResourceFieldSelector
}

type ObjectKeySelector struct {
	Name string
	Key  string
}

type FieldSelector struct {
	FieldPath string
}

type ResourceFieldSelector struct {
	Resource string
}

type EnvFromInput struct {
	Prefix       string
	ConfigMapRef *NamedObjectRef
	SecretRef    *NamedObjectRef
}

type NamedObjectRef struct {
	Name string
}

type RedactedEnvVar struct {
	Name   string `json:"name"`
	Source string `json:"source"`
}

func Redact(s string) (string, []string) {
	seen := make(map[string]struct{})
	out := s

	record := func(kind string) {
		seen[kind] = struct{}{}
	}

	replaceKV := func(re *regexp.Regexp) {
		out = re.ReplaceAllStringFunc(out, func(match string) string {
			loc := re.FindStringSubmatchIndex(match)
			if len(loc) < 4 {
				return match
			}
			prefix := match[:loc[3]]
			val := match[loc[3]:]
			if strings.HasPrefix(strings.TrimLeft(val, `"'`), "[REDACTED") {
				return match
			}
			record(KindSecretField)
			if len(val) > 0 && val[0] == '"' {
				return prefix + `"` + Placeholder + `:` + KindSecretField + `"`
			}
			if len(val) > 0 && val[0] == '\'' {
				return prefix + `'` + Placeholder + `:` + KindSecretField + `'`
			}
			return prefix + Placeholder + ":" + KindSecretField
		})
	}

	replaceKV(quotedKV)
	replaceKV(headerKV)
	replaceKV(assignKV)

	out = bearer.ReplaceAllStringFunc(out, func(match string) string {
		if strings.Contains(match, "[REDACTED") {
			return match
		}
		record(KindBearer)
		return "Bearer " + Placeholder + ":" + KindBearer
	})

	for _, rule := range []struct {
		re   *regexp.Regexp
		kind string
	}{
		{jwt, KindJWT},
		{google, KindGoogleKey},
		{skKey, KindSKKey},
	} {
		out = rule.re.ReplaceAllStringFunc(out, func(match string) string {
			if strings.HasPrefix(match, "[REDACTED") {
				return match
			}
			record(rule.kind)
			return Placeholder + ":" + rule.kind
		})
	}

	if len(seen) == 0 {
		return out, nil
	}
	kinds := make([]string, 0, len(seen))
	for k := range seen {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	return out, kinds
}

func StripEnv(vars []EnvVarInput) []RedactedEnvVar {
	if len(vars) == 0 {
		return nil
	}
	out := make([]RedactedEnvVar, 0, len(vars))
	for _, v := range vars {
		source := "literal"
		if v.ValueFrom != nil {
			switch {
			case v.ValueFrom.SecretKeyRef != nil:
				source = "secretKeyRef"
			case v.ValueFrom.ConfigMapKeyRef != nil:
				source = "configMapKeyRef"
			case v.ValueFrom.FieldRef != nil:
				source = "fieldRef"
			case v.ValueFrom.ResourceFieldRef != nil:
				source = "resourceFieldRef"
			default:
				source = "valueFrom"
			}
		}
		out = append(out, RedactedEnvVar{
			Name:   v.Name,
			Source: source,
		})
	}
	return out
}

func StripEnvFrom(sources []EnvFromInput) []string {
	if len(sources) == 0 {
		return nil
	}
	out := make([]string, 0, len(sources))
	for _, s := range sources {
		switch {
		case s.SecretRef != nil:
			out = append(out, "secretRef")
		case s.ConfigMapRef != nil:
			out = append(out, "configMapRef")
		default:
			out = append(out, "envFrom")
		}
	}
	return out
}
