package conform

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"nacfson_pipeline/services/error-pipeline/internal/classify"
	"nacfson_pipeline/services/error-pipeline/internal/redact"
)

// RequiredKeys defines the keys required on every log line per contract §2.
var RequiredKeys = []string{
	"time",
	"level",
	"msg",
	"service.name",
	"service.version",
}

// ValidLevels defines recognized log levels and aliases per contract §2.
var ValidLevels = map[string]string{
	"debug":    "debug",
	"info":     "info",
	"warn":     "warn",
	"warning":  "warn",
	"error":    "error",
	"fatal":    "fatal",
	"critical": "fatal",
}

var (
	versionRegex    = regexp.MustCompile(`^v\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`)
	traceIDRegex    = regexp.MustCompile(`^[0-9a-f]{32}$`)
	dynamicKeyRegex = regexp.MustCompile(`\d{4,}|[0-9a-fA-F]{32}`)
	nginxLogRegex   = regexp.MustCompile(`^\d{4}/\d\d/\d\d \d\d:\d\d:\d\d \[(\w+)\] ?`)
)

var genericExceptionTypes = map[string]bool{
	"*fmt.wrapError":       true,
	"*errors.errorString":  true,
	"*errors.joinError":    true,
	"error":                true,
	"Exception":            true,
	"Error":                true,
	"java.lang.Exception":  true,
}

// Classify determines the log line class using contract signatures.
func Classify(line string) Class {
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}") {
		var js map[string]any
		if err := json.Unmarshal([]byte(trimmed), &js); err == nil {
			return ClassJSON
		}
	}
	if strings.HasPrefix(trimmed, "panic:") || strings.HasPrefix(trimmed, "fatal error:") {
		return ClassCrashGo
	}
	if strings.HasPrefix(trimmed, "Traceback (most recent call last):") {
		return ClassCrashPy
	}
	if strings.HasPrefix(trimmed, `Exception in thread "`) {
		return ClassCrashJVM
	}
	if m := nginxLogRegex.FindStringSubmatch(trimmed); m != nil {
		lvl := m[1]
		if mapped, ok := classify.NginxLevels[lvl]; ok {
			if mapped == "fatal" {
				return ClassNginxFatal
			}
			return ClassNginxError
		}
	}
	return ClassOther
}

// Check validates a single log line against Contract v3 rules.
func Check(line string, opts Options) []Violation {
	var violations []Violation

	redactedText, kindsHit := redact.Redact(line)
	for _, k := range kindsHit {
		violations = append(violations, Violation{
			Section: "§2",
			Kind:    "redacted:" + k,
			Text:    redactedText,
		})
	}

	cls := Classify(line)
	if cls == ClassCrashGo || cls == ClassCrashPy || cls == ClassCrashJVM {
		return violations
	}
	if opts.Format == "nginx" && (cls == ClassNginxError || cls == ClassNginxFatal || nginxLogRegex.MatchString(strings.TrimSpace(line))) {
		return violations
	}

	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "{") || !strings.HasSuffix(trimmed, "}") {
		violations = append(violations, Violation{
			Section: "§2",
			Kind:    "not-json",
			Text:    redactedText,
		})
		return violations
	}

	var rawObj map[string]any
	if err := json.Unmarshal([]byte(trimmed), &rawObj); err != nil {
		violations = append(violations, Violation{
			Section: "§2",
			Kind:    "not-json",
			Text:    redactedText,
		})
		return violations
	}

	flat := make(map[string]any)
	flatten("", rawObj, flat)

	// Check required keys
	for _, req := range RequiredKeys {
		val, ok := flat[req]
		if !ok || val == nil || fmt.Sprint(val) == "" {
			violations = append(violations, Violation{
				Section: "§2",
				Kind:    "missing-key:" + req,
				Text:    redactedText,
			})
		}
	}

	// Check time RFC3339
	if tVal, ok := flat["time"]; ok && tVal != nil && fmt.Sprint(tVal) != "" {
		tStr := fmt.Sprint(tVal)
		if _, err := time.Parse(time.RFC3339Nano, tStr); err != nil {
			violations = append(violations, Violation{
				Section: "§2",
				Kind:    "bad-time",
				Text:    redactedText,
			})
		}
	}

	// Check level
	var normalizedLevel string
	if lvlVal, ok := flat["level"]; ok && lvlVal != nil && fmt.Sprint(lvlVal) != "" {
		lvlStr := strings.ToLower(fmt.Sprint(lvlVal))
		if mapped, ok := ValidLevels[lvlStr]; ok {
			normalizedLevel = mapped
		} else {
			violations = append(violations, Violation{
				Section: "§2",
				Kind:    "bad-level",
				Text:    redactedText,
			})
		}
	}

	// Check service.version
	if verVal, ok := flat["service.version"]; ok && verVal != nil && fmt.Sprint(verVal) != "" {
		verStr := fmt.Sprint(verVal)
		if !versionRegex.MatchString(verStr) {
			violations = append(violations, Violation{
				Section: "§2",
				Kind:    "bad-version",
				Text:    redactedText,
			})
		}
		if opts.ExpectVersion != "" && verStr != opts.ExpectVersion {
			violations = append(violations, Violation{
				Section: "§2",
				Kind:    "version-mismatch",
				Text:    redactedText,
			})
		}
	}

	// Check service.name
	if svcVal, ok := flat["service.name"]; ok && svcVal != nil && fmt.Sprint(svcVal) != "" {
		svcStr := fmt.Sprint(svcVal)
		if opts.ExpectService != "" && svcStr != opts.ExpectService {
			violations = append(violations, Violation{
				Section: "§2",
				Kind:    "service-mismatch",
				Text:    redactedText,
			})
		}
	}

	// Check reserved and dynamic keys
	seenReserved := false
	seenDynamic := false
	checkKey := func(k string) {
		if !seenReserved {
			if strings.HasPrefix(k, "_") || strings.HasPrefix(k, "k8s.") || k == "log.iostream" || k == "observed_time" {
				violations = append(violations, Violation{
					Section: "§2",
					Kind:    "reserved-key",
					Text:    redactedText,
				})
				seenReserved = true
			}
		}
		if !seenDynamic {
			if dynamicKeyRegex.MatchString(k) {
				violations = append(violations, Violation{
					Section: "§2",
					Kind:    "dynamic-key",
					Text:    redactedText,
				})
				seenDynamic = true
			}
		}
	}

	for k := range rawObj {
		checkKey(k)
	}
	for k := range flat {
		checkKey(k)
	}

	// Check trace_id
	if tidVal, ok := flat["trace_id"]; ok && tidVal != nil && fmt.Sprint(tidVal) != "" {
		tidStr := fmt.Sprint(tidVal)
		if !traceIDRegex.MatchString(tidStr) {
			violations = append(violations, Violation{
				Section: "§2",
				Kind:    "bad-trace-id",
				Text:    redactedText,
			})
		}
	}

	// Section 3: Error fields when level is error or fatal
	if normalizedLevel == "error" || normalizedLevel == "fatal" {
		msgVal, hasMsg := flat["exception.message"]
		typeVal, hasType := flat["exception.type"]
		hasMsgStr := hasMsg && msgVal != nil && fmt.Sprint(msgVal) != ""
		hasTypeStr := hasType && typeVal != nil && fmt.Sprint(typeVal) != ""

		if !hasMsgStr && !hasTypeStr {
			violations = append(violations, Violation{
				Section: "§3",
				Kind:    "missing-exception",
				Text:    redactedText,
			})
		}

		if hasTypeStr {
			typeStr := fmt.Sprint(typeVal)
			if genericExceptionTypes[typeStr] {
				violations = append(violations, Violation{
					Section: "§3",
					Kind:    "generic-exception-type",
					Text:    redactedText,
				})
			}
		}

		stackVal, hasStack := flat["exception.stacktrace"]
		hasStackStr := hasStack && stackVal != nil && fmt.Sprint(stackVal) != ""

		if normalizedLevel == "fatal" && !hasStackStr {
			violations = append(violations, Violation{
				Section: "§3",
				Kind:    "missing-fatal-stacktrace",
				Text:    redactedText,
			})
		}

		if hasStackStr {
			stackBytes := len([]byte(fmt.Sprint(stackVal)))
			if stackBytes > 8192 {
				violations = append(violations, Violation{
					Section: "§3",
					Kind:    "stacktrace-too-long",
					Text:    redactedText,
				})
			}
		}
	}

	// Check HTTP status code >= 500 requires error or fatal
	if statusVal, ok := flat["http.response.status_code"]; ok && statusVal != nil {
		status := toInt(statusVal)
		if status >= 500 && normalizedLevel != "error" && normalizedLevel != "fatal" {
			violations = append(violations, Violation{
				Section: "§2",
				Kind:    "bad-level",
				Text:    redactedText,
			})
		}
	}

	return violations
}

func flatten(prefix string, val any, out map[string]any) {
	switch v := val.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			sub := v[k]
			p := k
			if prefix != "" {
				p = prefix + "." + k
			}
			flatten(p, sub, out)
		}
	default:
		if prefix != "" {
			if _, exists := out[prefix]; !exists {
				out[prefix] = v
			}
		}
	}
}

func toInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	case string:
		i, _ := strconv.Atoi(n)
		return i
	}
	return 0
}
