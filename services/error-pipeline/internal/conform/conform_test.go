package conform

import (
	"strings"
	"testing"
)

func TestCheck(t *testing.T) {
	validBase := `{"time":"2026-11-04T09:00:10.125Z","level":"info","msg":"service started","service.name":"pn-api","service.version":"v1.4.2"}`

	tests := []struct {
		name         string
		line         string
		opts         Options
		wantKind     string
		wantSec      string
		expectPass   bool
	}{
		{
			name:       "valid standard json line passes",
			line:       validBase,
			expectPass: true,
		},
		{
			name:       "nested object equals flat keys",
			line:       `{"time":"2026-11-04T09:00:10.125Z","level":"info","msg":"nested test","service":{"name":"pn-api","version":"v1.4.2"}}`,
			expectPass: true,
		},
		{
			name:       "case-insensitive levels and aliases pass",
			line:       `{"time":"2026-11-04T09:00:10.125Z","level":"WARNING","msg":"warning test","service.name":"pn-api","service.version":"v1.4.2"}`,
			expectPass: true,
		},
		{
			name:       "untagged commit version passes line check",
			line:       `{"time":"2026-11-04T09:00:10.125Z","level":"info","msg":"untagged build","service.name":"pn-api","service.version":"v0.0.0-3f2a9c1"}`,
			expectPass: true,
		},
		{
			name:       "plain v0.0.0 passes line check (refused only by gate/preflight)",
			line:       `{"time":"2026-11-04T09:00:10.125Z","level":"info","msg":"local build","service.name":"pn-api","service.version":"v0.0.0"}`,
			expectPass: true,
		},
		{
			name:       "malformed version without leading v",
			line:       `{"time":"2026-11-04T09:00:10.125Z","level":"info","msg":"bad version","service.name":"pn-api","service.version":"1.4.2"}`,
			wantKind:   "bad-version",
			wantSec:    "§2",
			expectPass: false,
		},
		{
			name:       "expected version mismatch",
			line:       validBase,
			opts:       Options{ExpectVersion: "v1.5.0"},
			wantKind:   "version-mismatch",
			wantSec:    "§2",
			expectPass: false,
		},
		{
			name:       "expected service mismatch",
			line:       validBase,
			opts:       Options{ExpectService: "pn-web"},
			wantKind:   "service-mismatch",
			wantSec:    "§2",
			expectPass: false,
		},
		{
			name:       "missing required key msg",
			line:       `{"time":"2026-11-04T09:00:10.125Z","level":"info","service.name":"pn-api","service.version":"v1.4.2"}`,
			wantKind:   "missing-key:msg",
			wantSec:    "§2",
			expectPass: false,
		},
		{
			name:       "missing required key service.name",
			line:       `{"time":"2026-11-04T09:00:10.125Z","level":"info","msg":"hello","service.version":"v1.4.2"}`,
			wantKind:   "missing-key:service.name",
			wantSec:    "§2",
			expectPass: false,
		},
		{
			name:       "missing required key service.version",
			line:       `{"time":"2026-11-04T09:00:10.125Z","level":"info","msg":"hello","service.name":"pn-api"}`,
			wantKind:   "missing-key:service.version",
			wantSec:    "§2",
			expectPass: false,
		},
		{
			name:       "bad time format",
			line:       `{"time":"yesterday at 9am","level":"info","msg":"bad time","service.name":"pn-api","service.version":"v1.4.2"}`,
			wantKind:   "bad-time",
			wantSec:    "§2",
			expectPass: false,
		},
		{
			name:       "bad level string",
			line:       `{"time":"2026-11-04T09:00:10.125Z","level":"loud","msg":"bad level","service.name":"pn-api","service.version":"v1.4.2"}`,
			wantKind:   "bad-level",
			wantSec:    "§2",
			expectPass: false,
		},
		{
			name:       "valid trace_id 32 hex",
			line:       `{"time":"2026-11-04T09:00:10.125Z","level":"info","msg":"traced","service.name":"pn-api","service.version":"v1.4.2","trace_id":"4bf92f3577b34da6a3ce929d0e0e4736"}`,
			expectPass: true,
		},
		{
			name:       "uuid trace_id rejected",
			line:       `{"time":"2026-11-04T09:00:10.125Z","level":"info","msg":"uuid trace","service.name":"pn-api","service.version":"v1.4.2","trace_id":"4bf92f35-77b3-4da6-a3ce-929d0e0e4736"}`,
			wantKind:   "bad-trace-id",
			wantSec:    "§2",
			expectPass: false,
		},
		{
			name:       "reserved key k8s.pod.name rejected",
			line:       `{"time":"2026-11-04T09:00:10.125Z","level":"info","msg":"has k8s","service.name":"pn-api","service.version":"v1.4.2","k8s.pod.name":"pod-1"}`,
			wantKind:   "reserved-key",
			wantSec:    "§2",
			expectPass: false,
		},
		{
			name:       "reserved key with underscore prefix rejected",
			line:       `{"time":"2026-11-04T09:00:10.125Z","level":"info","msg":"has under","service.name":"pn-api","service.version":"v1.4.2","_time":"2026-11-04"}`,
			wantKind:   "reserved-key",
			wantSec:    "§2",
			expectPass: false,
		},
		{
			name:       "dynamic key with 4 digits rejected",
			line:       `{"time":"2026-11-04T09:00:10.125Z","level":"info","msg":"dynamic","service.name":"pn-api","service.version":"v1.4.2","order_4812":"processed"}`,
			wantKind:   "dynamic-key",
			wantSec:    "§2",
			expectPass: false,
		},
		{
			name:       "secret token in line redacted and flagged",
			line:       `{"time":"2026-11-04T09:00:10.125Z","level":"info","msg":"login auth=Bearer secret-token-xyz","service.name":"pn-api","service.version":"v1.4.2"}`,
			wantKind:   "redacted:bearer",
			wantSec:    "§2",
			expectPass: false,
		},
		{
			name:       "error level requires exception message or type",
			line:       `{"time":"2026-11-04T09:00:10.125Z","level":"error","msg":"failure","service.name":"pn-api","service.version":"v1.4.2"}`,
			wantKind:   "missing-exception",
			wantSec:    "§3",
			expectPass: false,
		},
		{
			name:       "error level with exception.message passes",
			line:       `{"time":"2026-11-04T09:00:10.125Z","level":"error","msg":"failure","service.name":"pn-api","service.version":"v1.4.2","exception.message":"disk full"}`,
			expectPass: true,
		},
		{
			name:       "error level with exception.type passes",
			line:       `{"time":"2026-11-04T09:00:10.125Z","level":"error","msg":"failure","service.name":"pn-api","service.version":"v1.4.2","exception.type":"*url.Error"}`,
			expectPass: true,
		},
		{
			name:       "generic exception type *fmt.wrapError rejected",
			line:       `{"time":"2026-11-04T09:00:10.125Z","level":"error","msg":"failure","service.name":"pn-api","service.version":"v1.4.2","exception.type":"*fmt.wrapError"}`,
			wantKind:   "generic-exception-type",
			wantSec:    "§3",
			expectPass: false,
		},
		{
			name:       "fatal level requires exception.stacktrace",
			line:       `{"time":"2026-11-04T09:00:10.125Z","level":"fatal","msg":"crash","service.name":"pn-api","service.version":"v1.4.2","exception.message":"segfault"}`,
			wantKind:   "missing-fatal-stacktrace",
			wantSec:    "§3",
			expectPass: false,
		},
		{
			name:       "fatal level with stacktrace passes",
			line:       `{"time":"2026-11-04T09:00:10.125Z","level":"fatal","msg":"crash","service.name":"pn-api","service.version":"v1.4.2","exception.message":"segfault","exception.stacktrace":"goroutine 1 [running]:\nmain.go:10"}`,
			expectPass: true,
		},
		{
			name:       "stacktrace of exactly 8192 bytes passes",
			line:       `{"time":"2026-11-04T09:00:10.125Z","level":"error","msg":"trace","service.name":"pn-api","service.version":"v1.4.2","exception.message":"err","exception.stacktrace":"` + strings.Repeat("a", 8192) + `"}`,
			expectPass: true,
		},
		{
			name:       "stacktrace of 8193 bytes reports stacktrace-too-long",
			line:       `{"time":"2026-11-04T09:00:10.125Z","level":"error","msg":"trace","service.name":"pn-api","service.version":"v1.4.2","exception.message":"err","exception.stacktrace":"` + strings.Repeat("a", 8193) + `"}`,
			wantKind:   "stacktrace-too-long",
			wantSec:    "§3",
			expectPass: false,
		},
		{
			name:       "http 502 with info level reports bad-level",
			line:       `{"time":"2026-11-04T09:00:10.125Z","level":"info","msg":"bad gateway","service.name":"pn-api","service.version":"v1.4.2","http.response.status_code":502}`,
			wantKind:   "bad-level",
			wantSec:    "§2",
			expectPass: false,
		},
		{
			name:       "http 502 with error level and exception passes",
			line:       `{"time":"2026-11-04T09:00:10.125Z","level":"error","msg":"bad gateway","service.name":"pn-api","service.version":"v1.4.2","http.response.status_code":502,"exception.message":"upstream 502"}`,
			expectPass: true,
		},
		{
			name:       "non-json line reports not-json",
			line:       "plain text line that is not json",
			wantKind:   "not-json",
			wantSec:    "§2",
			expectPass: false,
		},
		{
			name:       "go panic crash signature passes without not-json",
			line:       "panic: runtime error: index out of range",
			expectPass: true,
		},
		{
			name:       "python traceback crash signature passes without not-json",
			line:       "Traceback (most recent call last):",
			expectPass: true,
		},
		{
			name:       "jvm crash signature passes without not-json",
			line:       `Exception in thread "main" java.lang.NullPointerException`,
			expectPass: true,
		},
		{
			name:       "nginx native line rejected when format is json",
			line:       `2026/10/29 09:00:00 [error] 12#12: connect() failed`,
			opts:       Options{Format: "json"},
			wantKind:   "not-json",
			wantSec:    "§2",
			expectPass: false,
		},
		{
			name:       "nginx native line accepted when format is nginx",
			line:       `2026/10/29 09:00:00 [error] 12#12: connect() failed`,
			opts:       Options{Format: "nginx"},
			expectPass: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := Check(tc.line, tc.opts)
			if tc.expectPass {
				if len(v) > 0 {
					t.Fatalf("expected pass, got %d violations: %+v", len(v), v)
				}
			} else {
				if len(v) == 0 {
					t.Fatalf("expected failure with kind %q, got 0 violations", tc.wantKind)
				}
				found := false
				for _, item := range v {
					if item.Kind == tc.wantKind {
						found = true
						if tc.wantSec != "" && item.Section != tc.wantSec {
							t.Errorf("violation section mismatch: want %q, got %q", tc.wantSec, item.Section)
						}
					}
				}
				if !found {
					t.Fatalf("expected violation kind %q, but got kinds: %+v", tc.wantKind, v)
				}
			}
		})
	}
}
