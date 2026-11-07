package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestLogcheck(t *testing.T) {
	validLine := `{"time":"2026-11-04T09:00:10.125Z","level":"info","msg":"started","service.name":"pn-api","service.version":"v1.4.2"}`

	t.Run("exit code 0 on valid conforming logs", func(t *testing.T) {
		stdin := strings.NewReader(validLine + "\n")
		var stdout, stderr bytes.Buffer

		code := run([]string{"--format=json"}, stdin, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("expected exit code 0, got %d, stderr: %s", code, stderr.String())
		}
		if !strings.Contains(stdout.String(), "0 violation(s) found across 1 line(s)") {
			t.Errorf("unexpected stdout: %s", stdout.String())
		}
	})

	t.Run("exit code 1 on violations and redacts secrets", func(t *testing.T) {
		violatingLine := `{"time":"2026-11-04T09:00:10.125Z","level":"info","msg":"bad token Bearer super-secret-token","service.name":"pn-api","service.version":"v1.4.2"}`
		stdin := strings.NewReader(violatingLine + "\n")
		var stdout, stderr bytes.Buffer

		code := run([]string{"--expect-version=v1.5.0"}, stdin, &stdout, &stderr)
		if code != 1 {
			t.Fatalf("expected exit code 1, got %d, stderr: %s", code, stderr.String())
		}
		out := stdout.String()
		if strings.Contains(out, "super-secret-token") {
			t.Errorf("secret leaked in violation output: %s", out)
		}
		if !strings.Contains(out, "[REDACTED:bearer]") {
			t.Errorf("expected [REDACTED:bearer] marker, got: %s", out)
		}
		if !strings.Contains(out, `"kind":"version-mismatch"`) {
			t.Errorf("expected version-mismatch violation, got: %s", out)
		}
	})

	t.Run("exit code 2 on flag usage or invalid format", func(t *testing.T) {
		stdin := strings.NewReader(validLine + "\n")
		var stdout, stderr bytes.Buffer

		code := run([]string{"--unknown-flag"}, stdin, &stdout, &stderr)
		if code != 2 {
			t.Fatalf("expected exit code 2 on unknown flag, got %d", code)
		}

		code = run([]string{"--format=invalid"}, stdin, &stdout, &stderr)
		if code != 2 {
			t.Fatalf("expected exit code 2 on invalid format, got %d", code)
		}
	})

	t.Run("validates expect-service", func(t *testing.T) {
		stdin := strings.NewReader(validLine + "\n")
		var stdout, stderr bytes.Buffer

		code := run([]string{"--expect-service=pn-web"}, stdin, &stdout, &stderr)
		if code != 1 {
			t.Fatalf("expected exit code 1 on service mismatch, got %d", code)
		}
		if !strings.Contains(stdout.String(), `"kind":"service-mismatch"`) {
			t.Errorf("expected service-mismatch violation, got: %s", stdout.String())
		}
	})

	t.Run("handles lines larger than 64KB without buffer overflow", func(t *testing.T) {
		largeLine := `{"time":"2026-11-04T09:00:10.125Z","level":"info","msg":"` + strings.Repeat("x", 80*1024) + `","service.name":"pn-api","service.version":"v1.4.2"}`
		stdin := strings.NewReader(largeLine + "\n")
		var stdout, stderr bytes.Buffer

		code := run([]string{}, stdin, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("expected exit code 0 for large line, got %d, stderr: %s", code, stderr.String())
		}
	})
}
