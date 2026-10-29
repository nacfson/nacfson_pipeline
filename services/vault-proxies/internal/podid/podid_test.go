package podid

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func writeToken(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("fake-sa-jwt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestResolveReturnsIdentityAndCachesWithinTTL(t *testing.T) {
	var calls atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/api/v1/pods" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if got := r.URL.Query().Get("fieldSelector"); got != "status.podIP=10.42.0.7" {
			t.Errorf("unexpected fieldSelector %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer fake-sa-jwt" {
			t.Errorf("unexpected Authorization header %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"metadata":{"name":"pn-backend-1","namespace":"proj-pn","labels":{"app.kubernetes.io/name":"pn-backend"}}}]}`))
	}))
	defer api.Close()

	r := NewResolver(api.URL, api.Client(), writeToken(t), 60*time.Second)
	want := Identity{Namespace: "proj-pn", PodName: "pn-backend-1", AppLabel: "pn-backend"}

	for i := 0; i < 2; i++ {
		got, err := r.Resolve(context.Background(), "10.42.0.7")
		if err != nil {
			t.Fatalf("Resolve #%d: %v", i, err)
		}
		if *got != want {
			t.Fatalf("Resolve #%d = %+v, want %+v", i, *got, want)
		}
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("expected 1 API call within TTL, got %d", n)
	}
}

func TestResolveRefetchesAfterTTL(t *testing.T) {
	var calls atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"items":[{"metadata":{"name":"p","namespace":"n","labels":{}}}]}`))
	}))
	defer api.Close()

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r := NewResolver(api.URL, api.Client(), writeToken(t), time.Minute)
	r.Now = func() time.Time { return now }

	if _, err := r.Resolve(context.Background(), "10.0.0.1"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	if _, err := r.Resolve(context.Background(), "10.0.0.1"); err != nil {
		t.Fatal(err)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("expected 2 API calls across TTL expiry, got %d", n)
	}
}

func TestResolveErrors(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		token   string
		api     string
	}{
		{name: "api status", handler: func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusForbidden) }},
		{name: "empty list", handler: func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"items":[]}`)) }},
		{name: "bad json", handler: func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`nope`)) }},
		{name: "missing token", handler: func(w http.ResponseWriter, r *http.Request) {}, token: "/nonexistent/token"},
		{name: "no api server", api: "-"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			apiURL := ""
			client := http.DefaultClient
			if tc.api == "" {
				srv := httptest.NewServer(tc.handler)
				defer srv.Close()
				apiURL, client = srv.URL, srv.Client()
			}
			token := tc.token
			if token == "" {
				token = writeToken(t)
			}
			if _, err := NewResolver(apiURL, client, token, time.Minute).Resolve(context.Background(), "10.0.0.9"); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestPeerIP(t *testing.T) {
	tests := map[string]string{
		"10.42.0.7:51234": "10.42.0.7",
		" 10.42.0.7:80 ":  "10.42.0.7",
		"10.42.0.7":       "10.42.0.7",
		"[fd00::1]:8080":  "fd00::1",
		"":                "",
	}
	for in, want := range tests {
		if got := PeerIP(in); got != want {
			t.Errorf("PeerIP(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestInClusterHTTPClientHasTimeoutAndTLS(t *testing.T) {
	c := InClusterHTTPClient()
	if c.Timeout != 5*time.Second {
		t.Fatalf("unexpected timeout %v", c.Timeout)
	}
}

func TestInClusterAPIServer(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "10.43.0.1")
	t.Setenv("KUBERNETES_SERVICE_PORT", "443")
	if got := InClusterAPIServer(); got != "https://10.43.0.1:443" {
		t.Fatalf("got %q", got)
	}
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	if got := InClusterAPIServer(); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}
