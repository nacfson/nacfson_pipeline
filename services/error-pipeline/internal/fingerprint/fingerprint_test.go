package fingerprint

import (
	"regexp"
	"testing"

	"nacfson_pipeline/services/error-pipeline/internal/classify"
)

func TestNormalize(t *testing.T) {
	cases := []struct{ in, want string }{
		{"user 8f14e45f-ceea-467f-a9a0-4b8ad3e2f1a3 not found", "user <uuid> not found"},
		{"user 8F14E45F-CEEA-467F-A9A0-4B8AD3E2F1A3 not found", "user <uuid> not found"},
		{"timeout after 1503ms", "timeout after <n>ms"},
		{"at 2026-10-29T09:00:00.123Z", "at <time>"},
		{"at 2026-10-29T09:00:00+09:00 done", "at <time> done"},
		{"at 2026-10-29T09:00:00Z", "at <time>"},
		{"tx deadbeefcafebabe", "tx <hex>"},
		{"tx deadbeefcafebabe.", "tx <hex>."},
		{"Index Out Of Range [5]", "index out of range [<n>]"},
		{"short abc123 stays", "short abc<n> stays"},
		{"", ""},
	}
	for _, c := range cases {
		if got := Normalize(c.in); got != c.want {
			t.Errorf("Normalize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func classified(project, container, exType, exMsg, msg string) classify.Classified {
	return classify.Classified{
		Record: classify.Record{Project: project, Container: container},
		Event: classify.Event{
			Level:            "error",
			Msg:              msg,
			ExceptionType:    exType,
			ExceptionMessage: exMsg,
			Source:           classify.SourceJSON,
		},
	}
}

func TestSameErrorDifferentIDsSameFingerprint(t *testing.T) {
	a := classified("blog", "web", "NotFound", "user 8f14e45f-ceea-467f-a9a0-4b8ad3e2f1a3 not found after 1503ms at 2026-10-29T09:00:00.123Z", "")
	b := classified("blog", "web", "NotFound", "user 11111111-2222-3333-4444-555555555555 not found after 98ms at 2026-10-30T10:11:12Z", "")
	if Of(a) != Of(b) {
		t.Errorf("fingerprints differ: %s vs %s", Of(a), Of(b))
	}

	// Crash: signature line in Msg, no exception fields.
	c := classified("blog", "web", "", "", "panic: runtime error: index out of range [5] with length 3")
	d := classified("blog", "web", "", "", "panic: runtime error: index out of range [9] with length 12")
	if Of(c) != Of(d) {
		t.Errorf("crash fingerprints differ: %s vs %s", Of(c), Of(d))
	}
}

func TestDifferentContainerDifferentFingerprint(t *testing.T) {
	a := classified("blog", "web", "E", "boom", "")
	b := classified("blog", "worker", "E", "boom", "")
	if Of(a) == Of(b) {
		t.Errorf("different containers share fingerprint %s", Of(a))
	}
	c := classified("shop", "web", "E", "boom", "")
	if Of(a) == Of(c) {
		t.Errorf("different projects share fingerprint %s", Of(a))
	}
}

func TestProjectFingerprintWins(t *testing.T) {
	c := classified("blog", "web", "E", "boom", "")
	c.Event.Fingerprint = "  my-own-key  "
	if got := Of(c); got != "my-own-key" {
		t.Errorf("Of = %q, want my-own-key", got)
	}
	c.Event.Fingerprint = "   "
	if got := Of(c); !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(got) {
		t.Errorf("blank project fingerprint should fall back to computed, got %q", got)
	}
}

func TestFingerprintIs16Hex(t *testing.T) {
	got := Of(classified("blog", "web", "E", "boom", ""))
	if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(got) {
		t.Errorf("fingerprint %q is not 16 lowercase hex", got)
	}
}

func TestMsgFallbackWhenNoExceptionMessage(t *testing.T) {
	a := classified("blog", "web", "", "", "db down")
	b := classified("blog", "web", "", "", "db up")
	if Of(a) == Of(b) {
		t.Error("different messages must differ")
	}
}
