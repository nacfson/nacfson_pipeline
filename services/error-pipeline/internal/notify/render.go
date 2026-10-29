package notify

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"nacfson_pipeline/services/error-pipeline/internal/classify"
	"nacfson_pipeline/services/error-pipeline/internal/group"
	"nacfson_pipeline/services/error-pipeline/internal/redact"
)

// investigatorSkip is the wording the design fixes for a failed handoff.
const investigatorSkip = "AI debug skipped: investigator unavailable"

const (
	// maxTitleRunes and maxFieldRunes are the proxy's Validate limits
	// (services/vault-proxies/cmd/notify-proxy/notification.go). Those limits
	// count runes, not bytes. A violation is a permanent 400 for the message at
	// the head of the retry queue, which blocks every notification behind it,
	// so no render path may return a longer title or field value.
	maxTitleRunes = 256
	maxFieldRunes = 1024

	// ellipsis ends a value that clamping shortened. It is one rune, so it
	// costs exactly one of the budget and can never be split.
	ellipsis = "…"

	// maxSkipFingerprints bounds how many fingerprints the batch skip notice
	// names. A project-supplied error.fingerprint may be far longer than the 16
	// hex characters of a derived one, so the name list is clamped to
	// maxFieldRunes by finalize; this cap keeps it from being a list of one.
	maxSkipFingerprints = 133
)

// RenderNew renders the alert for a group that matched a NotifyNew action: an
// immediate first alert, or a recurrence after the cooldown expired.
//
// SinceLastAlert is the only recurrence signal a Group carries, because the
// tracker sets it for a recurrence and leaves it at zero on a fresh cycle. A
// recurrence with no occurrences during the cooldown therefore renders as a
// first alert. Callers that hold the Action itself use RenderNewAction, which
// reads the explicit Recurred flag instead.
func RenderNew(g group.Group) Notification {
	return RenderNewAction(group.Action{
		Type:     group.NotifyNew,
		Group:    g,
		Recurred: g.SinceLastAlert > 0,
	})
}

// RenderNewAction renders the NotifyNew alert for a. The title decides from
// a.Recurred, so a recurrence whose cooldown expired without a single
// occurrence still reads as a recurrence, and the count clause appears only
// when there is a count to report.
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

// RenderOverflow renders the single message that reports the notifications the
// burst cap refused: n of them, in the window that began at since.
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

// RenderInvestigatorUnavailable renders the notice that a group could not be
// handed to the error investigator.
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

// RenderInvestigatorUnavailableBatch renders the one notice that stands for a
// batch of groups that never reached the investigator. It names up to
// maxSkipFingerprints of them and states the count, so it covers all of them
// whether or not each is named.
//
// The count is in the body whatever happens to the name list, because a field
// value is clamped: without the count a truncated list would read as the whole
// truth. RenderInvestigatorUnavailable is used for a single group instead, so a
// shutdown that leaves a few groups unposted still says where each one was.
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

// finalize makes a rendered notification acceptable to the proxy: the title
// and every field value are redacted and then clamped to their rune limit.
// Every render path returns through here, so no path can forget a cap. A field
// can be huge before this point: classify caps Event.Fingerprint at 64 KiB and
// the tracker does not cap the Pods it collected.
func finalize(n Notification) Notification {
	n.Title = clamp(n.Title, maxTitleRunes)
	for i := range n.Fields {
		n.Fields[i].Value = clamp(n.Fields[i].Value, maxFieldRunes)
	}
	return n
}

// clamp redacts s and shortens it to at most max runes, ending in an ellipsis
// when it had to cut. It works on runes, never bytes, so it cannot split a
// multi-byte character. Redaction runs first, so the returned prefix is
// redacted as well and a secret cannot survive half-truncated.
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

// headRunes returns the first n runes of s. It stops as soon as it has them,
// so a long s costs one pass of n runes and no copy.
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

// location is the "<project>/<container>" tail of a title. Redaction happens
// in finalize, after the two parts are joined.
func location(g group.Group) string {
	return g.Project + "/" + g.Container
}

// severityOf maps a contract level onto a notification severity.
func severityOf(ev classify.Event) string {
	if ev.Level == "fatal" {
		return SeverityFatal
	}
	return SeverityError
}

// logLines is the exception line, the raw samples and the stack trace, in that
// order, each redacted and with blank entries dropped.
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

// exceptionLine is "type: message", or whichever of the two exists, falling
// back to the raw message of a line that has neither.
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

// fenced wraps lines in a markdown code block. The fence is lengthened when the
// content itself contains one, so a log line cannot end the block early.
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
