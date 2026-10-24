package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestStartupSelfAuditPassesAndCatchesVulnerabilities(t *testing.T) {
	p := NewAIProxy(AIProxyConfig{
		DataAddr:          "0.0.0.0:8080",
		AdminAddr:         "127.0.0.1:8081",
		InitialAPIKey:     "test-key",
		DefaultDailyLimit: 500,
		ResolvePodIP: func(ctx context.Context, ip string) (*PodIdentity, error) {
			return &PodIdentity{Namespace: "proj-pn", PodName: "pn-backend-1", AppLabel: "pn-backend"}, nil
		},
	})

	// 1. Healthy self-test via /startupz
	req := httptest.NewRequest(http.MethodGet, "/startupz", nil)
	rec := httptest.NewRecorder()
	p.DataMux().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected /startupz 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify /readyz and /healthz also return 200 OK after startup self-test passed
	for _, path := range []string{"/readyz", "/healthz"} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		p.DataMux().ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("expected %s 200 OK, got %d: %s", path, w.Code, w.Body.String())
		}
	}

	// 2. Vulnerability #1: Non-loopback AdminAddr (0.0.0.0:8081) must fail /startupz & /readyz with 500
	badLoopback := NewAIProxy(AIProxyConfig{
		DataAddr:      "0.0.0.0:8080",
		AdminAddr:     "0.0.0.0:8081",
		InitialAPIKey: "test-key",
	})
	rec2 := httptest.NewRecorder()
	badLoopback.DataMux().ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/startupz", nil))
	if rec2.Code != http.StatusInternalServerError {
		t.Fatalf("expected /startupz 500 when AdminAddr is 0.0.0.0:8081, got %d", rec2.Code)
	}

	// 3. Vulnerability #2: Injected admin route leak on DataMux must fail /startupz with 500
	badMux := NewAIProxy(AIProxyConfig{
		DataAddr:      "0.0.0.0:8080",
		AdminAddr:     "127.0.0.1:8081",
		InitialAPIKey: "test-key",
	})
	badMux.dataMux.(*http.ServeMux).HandleFunc("/admin/enable", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	rec3 := httptest.NewRecorder()
	badMux.DataMux().ServeHTTP(rec3, httptest.NewRequest(http.MethodGet, "/startupz", nil))
	if rec3.Code != http.StatusInternalServerError {
		t.Fatalf("expected /startupz 500 when admin route leaks onto DataMux, got %d", rec3.Code)
	}

	// 4. Vulnerability #3: Broken kill-switch self-drill must fail /startupz with 500
	badSwitch := NewAIProxy(AIProxyConfig{
		DataAddr:      "0.0.0.0:8080",
		AdminAddr:     "127.0.0.1:8081",
		InitialAPIKey: "test-key",
	})
	badSwitch.adminMux = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate broken revoke handler that returns 200 OK without flipping Enabled=false
		w.WriteHeader(http.StatusOK)
	})
	rec4 := httptest.NewRecorder()
	badSwitch.DataMux().ServeHTTP(rec4, httptest.NewRequest(http.MethodGet, "/startupz", nil))
	if rec4.Code != http.StatusInternalServerError {
		t.Fatalf("expected /startupz 500 when kill switch fails self-drill, got %d", rec4.Code)
	}

	// 5. Concurrent /startupz calls must serialize cleanly on __selftest__ without races
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := httptest.NewRecorder()
			p.DataMux().ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/startupz", nil))
			if r.Code != http.StatusOK {
				t.Errorf("concurrent /startupz expected 200 OK, got %d: %s", r.Code, r.Body.String())
			}
		}()
	}
	wg.Wait()
}

func TestDataMuxAllowsVerifiedPodAndInjectsOpenBaoKey(t *testing.T) {
	var upstreamCalls atomic.Int32
	var receivedAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		receivedAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/v1beta/openai/chat/completions" {
			t.Errorf("unexpected upstream path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"translated"}}]}`))
	}))
	defer upstream.Close()

	p := NewAIProxy(AIProxyConfig{
		DataAddr:          "0.0.0.0:8080",
		AdminAddr:         "127.0.0.1:8081",
		UpstreamBaseURL:   upstream.URL + "/v1beta/openai",
		InitialAPIKey:     "test-openbao-gemini-key",
		DefaultDailyLimit: 500,
		ResolvePodIP: func(ctx context.Context, ip string) (*PodIdentity, error) {
			if ip == "10.42.0.12" {
				return &PodIdentity{Namespace: "proj-pn", PodName: "pn-backend-abc", AppLabel: "pn-backend"}, nil
			}
			return nil, fmt.Errorf("unknown pod IP %s", ip)
		},
	})

	body := `{"model":"gemini-3.1-flash-lite","messages":[{"role":"user","content":"hello"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.RemoteAddr = "10.42.0.12:44120"
	rec := httptest.NewRecorder()
	p.DataMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}
	if upstreamCalls.Load() != 1 {
		t.Fatalf("expected 1 upstream call, got %d", upstreamCalls.Load())
	}
	if receivedAuth != "Bearer test-openbao-gemini-key" {
		t.Fatalf("expected upstream Authorization 'Bearer test-openbao-gemini-key', got %q", receivedAuth)
	}

	// Check /admin/status counter
	sRec := httptest.NewRecorder()
	p.AdminMux().ServeHTTP(sRec, httptest.NewRequest(http.MethodGet, "/admin/status", nil))
	if sRec.Code != http.StatusOK {
		t.Fatalf("expected /admin/status 200, got %d", sRec.Code)
	}
	var status map[string]ProjectGrant
	if err := json.Unmarshal(sRec.Body.Bytes(), &status); err != nil {
		t.Fatalf("unmarshal status: %v", err)
	}
	if status["pn"].UsedToday != 1 || !status["pn"].Enabled {
		t.Fatalf("unexpected pn status: %+v", status["pn"])
	}
}

func TestKillSwitchRevokeAndEnableInRAM(t *testing.T) {
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	p := NewAIProxy(AIProxyConfig{
		DataAddr:          "0.0.0.0:8080",
		AdminAddr:         "127.0.0.1:8081",
		UpstreamBaseURL:   upstream.URL,
		InitialAPIKey:     "test-openbao-gemini-key",
		DefaultDailyLimit: 500,
		ResolvePodIP: func(ctx context.Context, ip string) (*PodIdentity, error) {
			return &PodIdentity{Namespace: "proj-pn", PodName: "pn-backend-1", AppLabel: "pn-backend"}, nil
		},
	})

	// Capture backing []byte slice before revoke to prove RAM bytes are overwritten with 0x00 on stop
	backingBytesBeforeRevoke := p.geminiKey
	if len(backingBytesBeforeRevoke) == 0 {
		t.Fatal("expected non-empty geminiKey []byte in RAM while active")
	}

	// 1. Flip Kill Switch to OFF via AdminMux
	revReq := httptest.NewRequest(http.MethodPost, "/admin/revoke?project=pn", nil)
	revRec := httptest.NewRecorder()
	p.AdminMux().ServeHTTP(revRec, revReq)
	if revRec.Code != http.StatusOK {
		t.Fatalf("expected revoke 200 OK, got %d: %s", revRec.Code, revRec.Body.String())
	}

	// Verify every byte of the previous backing slice in RAM was scrubbed to 0x00
	for i, b := range backingBytesBeforeRevoke {
		if b != 0 {
			t.Fatalf("expected RAM credential byte[%d] to be zeroed (0x00) on revoke, got %#x", i, b)
		}
	}
	if p.geminiKey != nil {
		t.Fatalf("expected p.geminiKey == nil while stopped, got len=%d", len(p.geminiKey))
	}

	// 2. Concurrent calls & immediate block check (-race)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			body := `{"model":"gemini-3.1-flash-lite","messages":[{"role":"user","content":"hi"}]}`
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
			req.RemoteAddr = "10.42.0.12:50000"
			rec := httptest.NewRecorder()
			p.DataMux().ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Errorf("expected 403 Forbidden while revoked, got %d", rec.Code)
			}
		}()
	}
	wg.Wait()

	if upstreamCalls.Load() != 0 {
		t.Fatalf("expected 0 upstream calls while revoked, got %d", upstreamCalls.Load())
	}

	// 3. Re-enable via AdminMux
	enReq := httptest.NewRequest(http.MethodPost, "/admin/enable?project=pn", nil)
	enRec := httptest.NewRecorder()
	p.AdminMux().ServeHTTP(enRec, enReq)
	if enRec.Code != http.StatusOK {
		t.Fatalf("expected enable 200 OK, got %d", enRec.Code)
	}

	body := `{"model":"gemini-3.1-flash-lite","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.RemoteAddr = "10.42.0.12:50001"
	rec := httptest.NewRecorder()
	p.DataMux().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || upstreamCalls.Load() != 1 {
		t.Fatalf("expected 200 OK and 1 upstream call after re-enable, got code=%d calls=%d", rec.Code, upstreamCalls.Load())
	}
}

func TestDataMuxRejectsAdminRoutesAndCallerAuthHeader(t *testing.T) {
	p := NewAIProxy(AIProxyConfig{
		DataAddr:      "0.0.0.0:8080",
		AdminAddr:     "127.0.0.1:8081",
		InitialAPIKey: "test-key",
		ResolvePodIP: func(ctx context.Context, ip string) (*PodIdentity, error) {
			return &PodIdentity{Namespace: "proj-pn", PodName: "pn-backend-1", AppLabel: "pn-backend"}, nil
		},
	})

	// Calling /admin/* on DataMux (:8080) must return 404 Not Found
	for _, path := range []string{"/admin/enable?project=pn", "/admin/revoke?project=pn", "/admin/status", "/admin/set-key"} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		rec := httptest.NewRecorder()
		p.DataMux().ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("expected 404 Not Found on DataMux for %s, got %d", path, rec.Code)
		}
	}

	// Calling /v1/chat/completions with caller Authorization header must return 403 Forbidden
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gemini-3.1-flash-lite"}`))
	req.RemoteAddr = "10.42.0.12:41000"
	req.Header.Set("Authorization", "Bearer caller-supplied-secret")
	rec := httptest.NewRecorder()
	p.DataMux().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden when caller sends Authorization header, got %d", rec.Code)
	}
}

func TestSpoofedXForwardedForAndUnauthorizedPodRejected(t *testing.T) {
	p := NewAIProxy(AIProxyConfig{
		DataAddr:      "0.0.0.0:8080",
		AdminAddr:     "127.0.0.1:8081",
		InitialAPIKey: "test-key",
		ResolvePodIP: func(ctx context.Context, ip string) (*PodIdentity, error) {
			if ip == "10.42.0.12" {
				return &PodIdentity{Namespace: "proj-pn", PodName: "pn-backend-1", AppLabel: "pn-backend"}, nil
			}
			if ip == "10.42.0.88" {
				return &PodIdentity{Namespace: "proj-pn", PodName: "pn-frontend-1", AppLabel: "pn-frontend"}, nil
			}
			return nil, fmt.Errorf("unknown pod IP")
		},
	})

	// 1. pn-frontend (10.42.0.88) spoofing X-Forwarded-For: 10.42.0.12 must be rejected with 403
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gemini-3.1-flash-lite"}`))
	req.RemoteAddr = "10.42.0.88:55111"
	req.Header.Set("X-Forwarded-For", "10.42.0.12")
	req.Header.Set("X-Project-ID", "pn")
	rec := httptest.NewRecorder()
	p.DataMux().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden for pn-frontend spoofing X-Forwarded-For, got %d", rec.Code)
	}

	// 2. Unpermitted model (gemini-2.5-pro) from authorized pod must be rejected with 403
	req2 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gemini-2.5-pro"}`))
	req2.RemoteAddr = "10.42.0.12:55112"
	rec2 := httptest.NewRecorder()
	p.DataMux().ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden for unpermitted model, got %d", rec2.Code)
	}

	// 3. Oversized payload (> 1 MiB) must be rejected with 400 Bad Request
	bigPayload := `{"model":"gemini-3.1-flash-lite","padding":"` + strings.Repeat("A", (1<<20)+1024) + `"}`
	req3 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(bigPayload))
	req3.RemoteAddr = "10.42.0.12:55113"
	rec3 := httptest.NewRecorder()
	p.DataMux().ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for >1 MiB body, got %d", rec3.Code)
	}
}

func TestDailyQuotaAutoBreakerAndUTCRollover(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	p := NewAIProxy(AIProxyConfig{
		DataAddr:          "0.0.0.0:8080",
		AdminAddr:         "127.0.0.1:8081",
		UpstreamBaseURL:   upstream.URL,
		InitialAPIKey:     "test-key",
		DefaultDailyLimit: 2,
		NowFunc:           func() time.Time { return now },
		ResolvePodIP: func(ctx context.Context, ip string) (*PodIdentity, error) {
			return &PodIdentity{Namespace: "proj-pn", PodName: "pn-backend-1", AppLabel: "pn-backend"}, nil
		},
	})

	call := func() int {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gemini-3.1-flash-lite"}`))
		req.RemoteAddr = "10.42.0.12:40000"
		rec := httptest.NewRecorder()
		p.DataMux().ServeHTTP(rec, req)
		return rec.Code
	}

	if c := call(); c != http.StatusOK {
		t.Fatalf("call 1 expected 200, got %d", c)
	}
	if c := call(); c != http.StatusOK {
		t.Fatalf("call 2 expected 200, got %d", c)
	}
	// Call 3 exceeds DailyLimit (2) -> auto-breaker trips to 403 Forbidden
	if c := call(); c != http.StatusForbidden {
		t.Fatalf("call 3 expected 403 Forbidden from daily quota breaker, got %d", c)
	}

	// Raising daily_limit via /admin/quota must re-enable a quota-tripped (non-manually-revoked) project
	qRec := httptest.NewRecorder()
	p.AdminMux().ServeHTTP(qRec, httptest.NewRequest(http.MethodPost, "/admin/quota?project=pn&daily_limit=3", nil))
	if qRec.Code != http.StatusOK {
		t.Fatalf("expected /admin/quota 200 OK, got %d", qRec.Code)
	}
	if c := call(); c != http.StatusOK {
		t.Fatalf("call after raising daily_limit expected 200 OK, got %d", c)
	}
	if c := call(); c != http.StatusForbidden {
		t.Fatalf("call exceeding new daily_limit (3) expected 403 Forbidden, got %d", c)
	}

	// Advance UTC day -> quota-tripped project resets UsedToday=0 and re-opens
	now = now.Add(24 * time.Hour)
	if c := call(); c != http.StatusOK {
		t.Fatalf("call after UTC rollover expected 200 OK, got %d", c)
	}

	// Manual revoke MUST stay STOPPED across UTC rollover
	revRec := httptest.NewRecorder()
	p.AdminMux().ServeHTTP(revRec, httptest.NewRequest(http.MethodPost, "/admin/revoke?project=pn", nil))
	now = now.Add(24 * time.Hour)
	if c := call(); c != http.StatusForbidden {
		t.Fatalf("manual revoke must stay 403 Forbidden across UTC day rollover, got %d", c)
	}
}

func TestSetKeyPersistsToOpenBaoViaK8sAuthAndHotReloads(t *testing.T) {
	var loginCalled, keySaved atomic.Bool
	var savedKeyVal string
	var persistedPnState map[string]any
	openbaoFail := atomic.Bool{}

	mockOpenBao := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if openbaoFail.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if r.URL.Path == "/v1/auth/kubernetes/login" && r.Method == http.MethodPost {
			loginCalled.Store(true)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"auth":{"client_token":"s.mock-k8s-token"}}`))
			return
		}
		if r.URL.Path == "/v1/kv/data/ai/gemini-api-key" && r.Method == http.MethodPost {
			if r.Header.Get("X-Vault-Token") != "s.mock-k8s-token" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			raw, _ := io.ReadAll(r.Body)
			var payload struct {
				Data map[string]string `json:"data"`
			}
			_ = json.Unmarshal(raw, &payload)
			savedKeyVal = payload.Data["value"]
			keySaved.Store(true)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
			return
		}
		if r.URL.Path == "/v1/kv/data/ai/gemini-api-key" && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			val := savedKeyVal
			if val == "" {
				val = "PLACEHOLDER_RUN_AI_GATEWAY_CTL_SET_KEY"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"data": map[string]string{"value": val},
				},
			})
			return
		}
		if r.URL.Path == "/v1/kv/data/ai/projects/pn" && r.Method == http.MethodPost {
			raw, _ := io.ReadAll(r.Body)
			var payload struct {
				Data map[string]any `json:"data"`
			}
			_ = json.Unmarshal(raw, &payload)
			persistedPnState = payload.Data
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
			return
		}
		if r.URL.Path == "/v1/kv/data/ai/projects/pn" && r.Method == http.MethodGet {
			if persistedPnState == nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"data": persistedPnState,
				},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockOpenBao.Close()

	var upstreamAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	jwtPath := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(jwtPath, []byte("eyJ-mock-k8s-sa-jwt"), 0o600); err != nil {
		t.Fatal(err)
	}

	p := NewAIProxy(AIProxyConfig{
		DataAddr:        "0.0.0.0:8080",
		AdminAddr:       "127.0.0.1:8081",
		UpstreamBaseURL: upstream.URL,
		OpenBaoAddr:     mockOpenBao.URL,
		OpenBaoRole:     "vault-ai-proxy",
		SAJWTPath:       jwtPath,
		ResolvePodIP: func(ctx context.Context, ip string) (*PodIdentity, error) {
			return &PodIdentity{Namespace: "proj-pn", PodName: "pn-backend-1", AppLabel: "pn-backend"}, nil
		},
	})

	// 0. Before real key is set (OpenBao returns PLACEHOLDER_*), /startupz succeeds and AI call returns 503 without burning UsedToday
	sRec0 := httptest.NewRecorder()
	p.DataMux().ServeHTTP(sRec0, httptest.NewRequest(http.MethodGet, "/startupz", nil))
	if sRec0.Code != http.StatusOK {
		t.Fatalf("expected /startupz 200 OK with placeholder key, got %d", sRec0.Code)
	}
	unseededReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gemini-3.1-flash-lite"}`))
	unseededReq.RemoteAddr = "10.42.0.12:41230"
	unseededRec := httptest.NewRecorder()
	p.DataMux().ServeHTTP(unseededRec, unseededReq)
	if unseededRec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 ServiceUnavailable when OpenBao holds PLACEHOLDER_* key, got %d", unseededRec.Code)
	}
	if p.projects["pn"].UsedToday != 0 {
		t.Fatalf("expected 0 UsedToday after 503 unconfigured key, got %d", p.projects["pn"].UsedToday)
	}

	// 1. Set key via AdminMux (zero human Vault token required)
	setReq := httptest.NewRequest(http.MethodPost, "/admin/set-key", bytes.NewBufferString(`{"api_key":"new-key"}`))
	setRec := httptest.NewRecorder()
	p.AdminMux().ServeHTTP(setRec, setReq)
	if setRec.Code != http.StatusOK {
		t.Fatalf("expected /admin/set-key 200 OK, got %d: %s", setRec.Code, setRec.Body.String())
	}
	if !loginCalled.Load() || !keySaved.Load() || savedKeyVal != "new-key" {
		t.Fatalf("expected K8s auth login & key saved to OpenBao, got login=%v saved=%v val=%q", loginCalled.Load(), keySaved.Load(), savedKeyVal)
	}

	// 2. Verify hot-reloaded key is used immediately on next AI request
	aiReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gemini-3.1-flash-lite"}`))
	aiReq.RemoteAddr = "10.42.0.12:41234"
	aiRec := httptest.NewRecorder()
	p.DataMux().ServeHTTP(aiRec, aiReq)
	if aiRec.Code != http.StatusOK || upstreamAuth != "Bearer new-key" {
		t.Fatalf("expected hot-reloaded key 'Bearer new-key', got code=%d auth=%q", aiRec.Code, upstreamAuth)
	}

	// 3. Revoke pn while OpenBao is reachable -> persists manual_revoked=true to OpenBao
	revPersistRec := httptest.NewRecorder()
	p.AdminMux().ServeHTTP(revPersistRec, httptest.NewRequest(http.MethodPost, "/admin/revoke?project=pn", nil))
	if revPersistRec.Code != http.StatusOK {
		t.Fatalf("expected revoke 200 OK, got %d", revPersistRec.Code)
	}

	// 4. Simulate pod restart: create a brand-new AIProxy instance and run /startupz -> must restore ManualRevoked=true & Enabled=false from OpenBao
	restarted := NewAIProxy(AIProxyConfig{
		DataAddr:        "0.0.0.0:8080",
		AdminAddr:       "127.0.0.1:8081",
		UpstreamBaseURL: upstream.URL,
		OpenBaoAddr:     mockOpenBao.URL,
		OpenBaoRole:     "vault-ai-proxy",
		SAJWTPath:       jwtPath,
		ResolvePodIP: func(ctx context.Context, ip string) (*PodIdentity, error) {
			return &PodIdentity{Namespace: "proj-pn", PodName: "pn-backend-1", AppLabel: "pn-backend"}, nil
		},
	})
	startRec := httptest.NewRecorder()
	restarted.DataMux().ServeHTTP(startRec, httptest.NewRequest(http.MethodGet, "/startupz", nil))
	if startRec.Code != http.StatusOK {
		t.Fatalf("expected restarted /startupz 200 OK, got %d", startRec.Code)
	}
	restartedCall := httptest.NewRecorder()
	restartedReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gemini-3.1-flash-lite"}`))
	restartedReq.RemoteAddr = "10.42.0.12:41239"
	restarted.DataMux().ServeHTTP(restartedCall, restartedReq)
	if restartedCall.Code != http.StatusForbidden {
		t.Fatalf("expected restarted proxy to keep pn STOPPED (403 Forbidden) from OpenBao persisted state, got %d", restartedCall.Code)
	}

	// 5. Even if OpenBao returns 503 during /admin/revoke, RAM switch MUST still flip to false (fail-safe stop)
	openbaoFail.Store(true)
	revRec := httptest.NewRecorder()
	p.AdminMux().ServeHTTP(revRec, httptest.NewRequest(http.MethodPost, "/admin/revoke?project=pn", nil))
	if revRec.Code != http.StatusOK {
		t.Fatalf("expected fail-safe RAM revoke 200 OK even when OpenBao is 503, got %d", revRec.Code)
	}
	aiRec2 := httptest.NewRecorder()
	p.DataMux().ServeHTTP(aiRec2, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gemini-3.1-flash-lite"}`)))
	if aiRec2.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden after fail-safe RAM revoke, got %d", aiRec2.Code)
	}
}
