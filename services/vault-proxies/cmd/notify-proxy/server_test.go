package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"nacfson_pipeline/services/vault-proxies/internal/podid"
)

const testWebhookURL = "https://discord.com/api/webhooks/123/SECRET-TOKEN"

const testGrants = "errors:observability:error-detector,ai-debug:observability:error-investigator"

const validNotification = `{"title":"boom","severity":"error","fields":[{"name":"ns","value":"proj-pn"}],"body":"stack"}`

type fakeVault struct {
	mu    sync.Mutex
	urls  map[string]string
	loads int
	err   error
}

func (f *fakeVault) LoadWebhook(ctx context.Context, channel string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loads++
	if f.err != nil {
		return "", f.err
	}
	return f.urls[channel], nil
}

func (f *fakeVault) SaveWebhook(ctx context.Context, channel, url string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	if f.urls == nil {
		f.urls = map[string]string{}
	}
	f.urls[channel] = url
	return nil
}

type sendRecorder struct {
	mu    sync.Mutex
	urls  []string
	notes []Notification
	err   error
}

func (s *sendRecorder) send(ctx context.Context, url string, n Notification) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.urls = append(s.urls, url)
	s.notes = append(s.notes, n)
	return s.err
}

func (s *sendRecorder) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.urls)
}

func identityResolver(ns, app string) func(context.Context, string) (*podid.Identity, error) {
	return func(ctx context.Context, ip string) (*podid.Identity, error) {
		return &podid.Identity{Namespace: ns, PodName: app + "-1", AppLabel: app}, nil
	}
}

func newTestProxy(t *testing.T, ns, app string) (*NotifyProxy, *fakeVault, *sendRecorder) {
	t.Helper()
	grants, err := ParseGrants(testGrants)
	if err != nil {
		t.Fatal(err)
	}
	v := &fakeVault{urls: map[string]string{"errors": testWebhookURL, "ai-debug": testWebhookURL}}
	s := &sendRecorder{}
	p := NewNotifyProxy(NotifyProxyConfig{
		DataAddr:  "0.0.0.0:8080",
		AdminAddr: "127.0.0.1:8081",
		Grants:    grants,
		Resolve:   identityResolver(ns, app),
		Vault:     v,
		Send:      s.send,
	})
	return p, v, s
}

func postNotify(h http.Handler, channel, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/notify/"+channel, strings.NewReader(body))
	req.RemoteAddr = "10.42.0.9:40000"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestParseGrants(t *testing.T) {
	g, err := ParseGrants(testGrants)
	if err != nil {
		t.Fatal(err)
	}
	if len(g) != 2 || g[0] != (Grant{"errors", "observability", "error-detector"}) || g[1].Channel != "ai-debug" {
		t.Fatalf("unexpected grants: %+v", g)
	}
	if g, err := ParseGrants("  "); err != nil || len(g) != 0 {
		t.Fatalf("empty spec should yield no grants, got %+v, %v", g, err)
	}
	for _, bad := range []string{"errors", "errors:ns", "errors:ns:app:x", ":ns:app", "errors::app", "errors:ns:", "Bad/Chan:ns:app", "a:b:c,,"} {
		if _, err := ParseGrants(bad); err == nil {
			t.Errorf("ParseGrants(%q) should fail", bad)
		}
	}
}

func TestGrantedPodDelivers(t *testing.T) {
	p, _, s := newTestProxy(t, "observability", "error-detector")
	rec := postNotify(p.DataMux(), "errors", validNotification)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("want 202, got %d: %s", rec.Code, rec.Body.String())
	}
	if s.calls() != 1 || s.urls[0] != testWebhookURL || s.notes[0].Title != "boom" {
		t.Fatalf("unexpected send calls: %+v", s.urls)
	}
}

func TestUngrantedPodRejected(t *testing.T) {
	p, _, s := newTestProxy(t, "proj-pn", "pn-backend")
	rec := postNotify(p.DataMux(), "errors", validNotification)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", rec.Code)
	}
	if s.calls() != 0 {
		t.Fatal("Send must not be called for an ungranted pod")
	}
}

func TestWrongChannelForCallerRejected(t *testing.T) {
	p, _, s := newTestProxy(t, "observability", "error-detector")
	rec := postNotify(p.DataMux(), "ai-debug", validNotification)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", rec.Code)
	}
	if s.calls() != 0 {
		t.Fatal("Send must not be called")
	}
}

func TestResolveFailureRejected(t *testing.T) {
	p, _, s := newTestProxy(t, "observability", "error-detector")
	p.cfg.Resolve = func(ctx context.Context, ip string) (*podid.Identity, error) {
		return nil, errors.New("no such pod")
	}
	if rec := postNotify(p.DataMux(), "errors", validNotification); rec.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", rec.Code)
	}
	if s.calls() != 0 {
		t.Fatal("Send must not be called")
	}
}

func TestUnknownChannel404(t *testing.T) {
	p, _, _ := newTestProxy(t, "observability", "error-detector")
	if rec := postNotify(p.DataMux(), "nope", validNotification); rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
}

func TestInvalidBodyRejected(t *testing.T) {
	p, _, s := newTestProxy(t, "observability", "error-detector")
	for name, body := range map[string]string{
		"not json":     `{`,
		"no title":     `{"severity":"error"}`,
		"bad severity": `{"title":"x","severity":"loud"}`,
		"oversize":     `{"title":"x","severity":"error","body":"` + strings.Repeat("a", 3<<20) + `"}`,
	} {
		if rec := postNotify(p.DataMux(), "errors", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: want 400, got %d", name, rec.Code)
		}
	}
	if s.calls() != 0 {
		t.Fatal("Send must not be called for invalid bodies")
	}
}

func TestWebhookNotConfigured503(t *testing.T) {
	p, v, s := newTestProxy(t, "observability", "error-detector")
	v.urls = map[string]string{}
	if rec := postNotify(p.DataMux(), "errors", validNotification); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d", rec.Code)
	}
	v.err = errors.New("openbao down")
	if rec := postNotify(p.DataMux(), "errors", validNotification); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 on load error, got %d", rec.Code)
	}
	if s.calls() != 0 {
		t.Fatal("Send must not be called")
	}
}

func TestDeliveryFailure502(t *testing.T) {
	p, _, s := newTestProxy(t, "observability", "error-detector")
	s.err = errors.New("discord webhook returned status 400")
	rec := postNotify(p.DataMux(), "errors", validNotification)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("want 502, got %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "discord") {
		t.Fatalf("502 body must stay destination-neutral: %s", rec.Body.String())
	}
}

func TestWebhookCachedAfterFirstLoad(t *testing.T) {
	p, v, s := newTestProxy(t, "observability", "error-detector")
	for i := 0; i < 3; i++ {
		if rec := postNotify(p.DataMux(), "errors", validNotification); rec.Code != http.StatusAccepted {
			t.Fatalf("want 202, got %d", rec.Code)
		}
	}
	if v.loads != 1 || s.calls() != 3 {
		t.Fatalf("loads=%d sends=%d, want 1 and 3", v.loads, s.calls())
	}
}

func TestAdminRoutesAbsentOnDataMux(t *testing.T) {
	p, _, _ := newTestProxy(t, "observability", "error-detector")
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/admin/set-webhook?channel=errors"},
		{http.MethodGet, "/admin/status"},
	} {
		rec := httptest.NewRecorder()
		p.DataMux().ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{}`)))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s on DataMux: want 404, got %d", tc.method, tc.path, rec.Code)
		}
	}
}

func setWebhook(p *NotifyProxy, channel, url string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]string{"url": url})
	req := httptest.NewRequest(http.MethodPost, "/admin/set-webhook?channel="+channel, strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	p.AdminMux().ServeHTTP(rec, req)
	return rec
}

func TestSetWebhookRejectsNonDiscordHost(t *testing.T) {
	p, v, _ := newTestProxy(t, "observability", "error-detector")
	for _, u := range []string{
		"https://evil.example/x",
		"https://evil.example/api/webhooks/1/x",
		"https://discord.com.evil.example/api/webhooks/1/x",
		"https://user@evil.example/api/webhooks/1/x",
		"http://discord.com/api/webhooks/1/x",
		"https://discord.com/other/path",
		"https://discordapp.com/api/webhooks/1/x",
		"not a url",
		"",
	} {
		if rec := setWebhook(p, "errors", u); rec.Code != http.StatusBadRequest {
			t.Errorf("url %q: want 400, got %d", u, rec.Code)
		}
	}
	if v.urls["errors"] != testWebhookURL {
		t.Fatal("rejected URL must not be stored")
	}
}

func TestSetWebhookStoresAndUpdatesCache(t *testing.T) {
	p, v, s := newTestProxy(t, "observability", "error-detector")
	newURL := "https://discord.com/api/webhooks/999/NEW-SECRET"
	rec := setWebhook(p, "errors", newURL)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "NEW-SECRET") {
		t.Fatal("set-webhook must not echo the URL")
	}
	if v.urls["errors"] != newURL {
		t.Fatal("URL not saved to vault")
	}
	postNotify(p.DataMux(), "errors", validNotification)
	if s.calls() != 1 || s.urls[0] != newURL {
		t.Fatalf("send should use the cached new URL, got %v", s.urls)
	}
	if v.loads != 0 {
		t.Fatalf("cache should be populated by SaveWebhook, loads=%d", v.loads)
	}
}

func TestSetWebhookUnknownChannelAndVaultFailure(t *testing.T) {
	p, v, _ := newTestProxy(t, "observability", "error-detector")
	if rec := setWebhook(p, "nope", testWebhookURL); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown channel: want 404, got %d", rec.Code)
	}
	v.err = errors.New("openbao down")
	if rec := setWebhook(p, "errors", "https://discord.com/api/webhooks/5/x"); rec.Code != http.StatusBadGateway {
		t.Fatalf("vault failure: want 502, got %d", rec.Code)
	}
}

func TestStatusNeverLeaksURL(t *testing.T) {
	p, _, s := newTestProxy(t, "observability", "error-detector")
	postNotify(p.DataMux(), "errors", validNotification)
	s.err = errors.New("fail")
	postNotify(p.DataMux(), "errors", validNotification)

	rec := httptest.NewRecorder()
	p.AdminMux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	raw := rec.Body.String()
	for _, leak := range []string{"SECRET-TOKEN", "discord.com", "api/webhooks"} {
		if strings.Contains(raw, leak) {
			t.Fatalf("status leaked %q: %s", leak, raw)
		}
	}
	var rows []struct {
		Channel    string `json:"channel"`
		Configured bool   `json:"configured"`
		Sent       int    `json:"sent"`
		Failed     int    `json:"failed"`
	}
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		t.Fatalf("status must be a JSON array: %v: %s", err, raw)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 channels, got %+v", rows)
	}
	for _, r := range rows {
		if r.Channel == "errors" && (!r.Configured || r.Sent != 1 || r.Failed != 1) {
			t.Fatalf("unexpected errors row: %+v", r)
		}
		if r.Channel == "ai-debug" && (r.Sent != 0 || r.Failed != 0) {
			t.Fatalf("unexpected ai-debug row: %+v", r)
		}
	}
}

func TestStartupSelfTestPassesAndProbes(t *testing.T) {
	// Resolver deliberately returns a GRANTED identity for every IP: the
	// self-test must still prove an ungranted caller is refused.
	p, _, s := newTestProxy(t, "observability", "error-detector")
	for _, path := range []string{"/startupz", "/readyz", "/healthz"} {
		rec := httptest.NewRecorder()
		p.DataMux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: want 200, got %d: %s", path, rec.Code, rec.Body.String())
		}
	}
	if s.calls() != 0 {
		t.Fatal("self-test must make zero Send calls")
	}
	if err := p.RunStartupSelfTest(); err != nil {
		t.Fatal(err)
	}
}

func TestStartupSelfTestFailsOnNonLoopbackAdmin(t *testing.T) {
	p := NewNotifyProxy(NotifyProxyConfig{DataAddr: "0.0.0.0:8080", AdminAddr: "0.0.0.0:8081"})
	for _, path := range []string{"/startupz", "/readyz"} {
		rec := httptest.NewRecorder()
		p.DataMux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("%s: want 500, got %d", path, rec.Code)
		}
	}
}

func TestReadyzDoesNotDependOnVault(t *testing.T) {
	p, v, _ := newTestProxy(t, "observability", "error-detector")
	v.err = errors.New("openbao down")
	rec := httptest.NewRecorder()
	p.DataMux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 with vault down, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestIsStrictLoopbackAddr(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1:8081": true, "localhost:8081": true, "[::1]:8081": true,
		"0.0.0.0:8081": false, ":8081": false, "10.0.0.1:8081": false, "garbage": false,
	} {
		if got := isStrictLoopbackAddr(addr); got != want {
			t.Errorf("isStrictLoopbackAddr(%q)=%v want %v", addr, got, want)
		}
	}
}
