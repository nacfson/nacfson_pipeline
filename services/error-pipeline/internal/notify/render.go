package notify

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"nacfson_pipeline/services/error-pipeline/internal/classify"
	"nacfson_pipeline/services/error-pipeline/internal/group"
	"nacfson_pipeline/services/error-pipeline/internal/redact"
)

const investigatorSkip = "AI debug skipped: investigator unavailable"

const (
	maxTitleRunes = 256
	maxFieldRunes = 1024

	ellipsis = "…"

	maxSkipFingerprints = 133
)

func RenderNew(g group.Group) Notification {
	return RenderNewAction(group.Action{
		Type:     group.NotifyNew,
		Group:    g,
		Recurred: g.SinceLastAlert > 0,
	})
}

func RenderNewAction(a group.Action) Notification {
	g := a.Group
	title := "New error in " + location(g)
	intro := ""
	if a.Recurred {
		title = "Recurring error in " + location(g)
	}
	if n := g.SinceLastAlert; n > 0 {
		intro = fmt.Sprintf("Recurred; %d occurrences since last alert.\n\n", n)
	}

	var out Notification
	out.Title = title
	out.Severity = severityOf(g.Event)
	out.Fields = []Field{
		{Name: "Project", Value: g.Project},
		{Name: "Container", Value: g.Container},
		{Name: "Pods", Value: strings.Join(g.Pods, ", ")},
		{Name: "First seen", Value: g.FirstSeen.UTC().Format(time.RFC3339)},
		{Name: "Level", Value: g.Event.Level},
		{Name: "Fingerprint", Value: g.Fingerprint},
	}
	out.Body = intro + fenced(logLines(g.Event, g.Samples))
	return finalize(out)
}

func RenderOverflow(n int, since time.Time) Notification {
	since = since.UTC()
	stamp := since.Format(time.RFC3339)

	var out Notification
	out.Title = fmt.Sprintf("+%d more new errors", n)
	out.Severity = SeverityInfo
	out.Fields = []Field{
		{Name: "Suppressed", Value: fmt.Sprint(n)},
		{Name: "Since", Value: stamp},
	}
	out.Body = fmt.Sprintf("%d new errors since %s were held back by the burst cap "+
		"and are merged into this message. They stay counted in the detector's "+
		"state; only the Discord messages were dropped.", n, stamp)
	return finalize(out)
}

func RenderInvestigatorUnavailable(g group.Group) Notification {
	var out Notification
	out.Title = "AI debug skipped in " + location(g)
	out.Severity = SeverityInfo
	out.Fields = []Field{
		{Name: "Project", Value: g.Project},
		{Name: "Container", Value: g.Container},
		{Name: "Fingerprint", Value: g.Fingerprint},
	}
	out.Body = investigatorSkip + ". The error itself was already reported and the " +
		"group is still in the detector's state."
	return finalize(out)
}

func RenderInvestigatorUnavailableBatch(batch []group.Group) Notification {
	names := make([]string, 0, min(len(batch), maxSkipFingerprints))
	for _, g := range batch {
		if len(names) == maxSkipFingerprints {
			break
		}
		names = append(names, g.Fingerprint)
	}

	var out Notification
	out.Title = fmt.Sprintf("AI debug skipped for %d groups", len(batch))
	out.Severity = SeverityInfo
	out.Fields = []Field{
		{Name: "Skipped", Value: fmt.Sprint(len(batch))},
		{Name: "Fingerprints", Value: strings.Join(names, ", ")},
	}
	out.Body = fmt.Sprintf("%d groups closed without reaching the error investigator: the "+
		"investigator was unavailable, or the shutdown budget ran out first. They are saved "+
		"as not investigated, so the next start retries them.", len(batch))
	return finalize(out)
}

func RenderQuarantineAlert(project, container string, count int, firstLine string, observed time.Time) Notification {
	title := fmt.Sprintf("Quarantined log in %s/%s", project, container)
	if count > 1 {
		title = fmt.Sprintf("Quarantined logs in %s/%s (+%d occurrences)", project, container, count-1)
	}

	var out Notification
	out.Title = title
	out.Severity = SeverityError
	out.Fields = []Field{
		{Name: "Project", Value: project},
		{Name: "Container", Value: container},
		{Name: "Count", Value: fmt.Sprint(count)},
		{Name: "Observed", Value: observed.UTC().Format(time.RFC3339)},
	}
	out.Body = "Log lines from this container were non-conforming and routed to quarantine (tenant 0:1), " +
		"where normal error alerting cannot see them.\n\n" + fenced([]string{redact.String(firstLine)})
	return finalize(out)
}

func RenderRedactionMarkerAlert(project, container, release, kind string, observed time.Time) Notification {
	title := fmt.Sprintf("Secret redacted in %s/%s", project, container)
	if release != "" {
		title = fmt.Sprintf("Secret redacted in %s/%s@%s", project, container, release)
	}

	var out Notification
	out.Title = title
	out.Severity = SeverityError
	out.Fields = []Field{
		{Name: "Project", Value: project},
		{Name: "Container", Value: container},
		{Name: "Release", Value: release},
		{Name: "Marker Kind", Value: kind},
		{Name: "Observed", Value: observed.UTC().Format(time.RFC3339)},
	}
	out.Body = fmt.Sprintf("A log line printed by this container contained a secret matching pattern %q. "+
		"The platform redacted the value before storage. Fix the workload so secrets are not printed.", kind)
	return finalize(out)
}

func RenderViolationDigest(violations map[string]map[string]int, since, until time.Time) Notification {
	var out Notification
	out.Title = "Daily log contract violation digest"
	out.Severity = SeverityInfo
	out.Fields = []Field{
		{Name: "Since", Value: since.UTC().Format(time.RFC3339)},
		{Name: "Until", Value: until.UTC().Format(time.RFC3339)},
	}

	var sb strings.Builder
	sb.WriteString("Log contract violations recorded in the last 24h:\n\n")
	total := 0

	pcs := make([]string, 0, len(violations))
	for pc := range violations {
		pcs = append(pcs, pc)
	}
	sort.Strings(pcs)

	for _, pc := range pcs {
		kinds := violations[pc]
		ks := make([]string, 0, len(kinds))
		for k := range kinds {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		for _, kind := range ks {
			count := kinds[kind]
			total += count
			sb.WriteString(fmt.Sprintf("- **%s**: %s = %d\n", pc, kind, count))
		}
	}
	out.Fields = append(out.Fields, Field{Name: "Total Violations", Value: fmt.Sprint(total)})
	out.Body = sb.String()
	return finalize(out)
}

func finalize(n Notification) Notification {
	n.Title = clamp(n.Title, maxTitleRunes)
	for i := range n.Fields {
		n.Fields[i].Value = clamp(n.Fields[i].Value, maxFieldRunes)
	}
	return n
}

func clamp(s string, max int) string {
	s = redact.String(s)
	if max <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return headRunes(s, max-utf8.RuneCountInString(ellipsis)) + ellipsis
}

func headRunes(s string, n int) string {
	count := 0
	for i := range s {
		if count == n {
			return s[:i]
		}
		count++
	}
	return s
}

func location(g group.Group) string {
	return g.Project + "/" + g.Container
}

func severityOf(ev classify.Event) string {
	if ev.Level == "fatal" {
		return SeverityFatal
	}
	return SeverityError
}

func logLines(ev classify.Event, samples []string) []string {
	lines := make([]string, 0, len(samples)+2)
	if l := exceptionLine(ev); l != "" {
		lines = append(lines, l)
	}
	for _, s := range samples {
		if s = redact.String(s); strings.TrimSpace(s) != "" {
			lines = append(lines, s)
		}
	}
	if st := redact.String(strings.TrimSpace(ev.Stacktrace)); st != "" {
		lines = append(lines, st)
	}
	return lines
}

func exceptionLine(ev classify.Event) string {
	typ := redact.String(strings.TrimSpace(ev.ExceptionType))
	msg := redact.String(strings.TrimSpace(ev.ExceptionMessage))
	switch {
	case typ != "" && msg != "":
		return typ + ": " + msg
	case typ != "":
		return typ
	case msg != "":
		return msg
	}
	return redact.String(strings.TrimSpace(ev.Msg))
}

func fenced(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	content := strings.Join(lines, "\n")
	fence := "```"
	for strings.Contains(content, fence) {
		fence += "`"
	}
	return fence + "\n" + content + "\n" + fence
}
