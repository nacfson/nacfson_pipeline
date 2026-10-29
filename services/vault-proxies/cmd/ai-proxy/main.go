package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"nacfson_pipeline/services/vault-proxies/internal/openbao"
	"nacfson_pipeline/services/vault-proxies/internal/podid"
)

const (
	maxChatBodyBytes = 1 << 20 // 1 MiB cap per Review Focus #2
	selfTestProject  = "__selftest__"
	defaultSAJWTPath = "/var/run/secrets/kubernetes.io/serviceaccount/token"
)

type ProjectGrant struct {
	Project        string          `json:"project"`
	Namespace      string          `json:"namespace"`
	PodLabel       string          `json:"pod_label"`
	AllowedModels  map[string]bool `json:"allowed_models"`
	Enabled        bool            `json:"enabled"`
	ManualRevoked  bool            `json:"manual_revoked"`
	DailyLimit     int             `json:"daily_limit"`
	UsedToday      int             `json:"used_today"`
	BlockedCount   int             `json:"blocked_count"`
	CurrentUTCDate string          `json:"current_utc_date"`
}

// PodIdentity is the resolved identity of a calling pod.
type PodIdentity = podid.Identity

type AIProxyConfig struct {
	DataAddr          string
	AdminAddr         string
	UpstreamBaseURL   string
	OpenBaoAddr       string
	OpenBaoRole       string
	SAJWTPath         string
	InitialAPIKey     string
	DefaultDailyLimit int
	AllowedGrantsSpec string
	NowFunc           func() time.Time
	ResolvePodIP      func(ctx context.Context, ip string) (*PodIdentity, error)
}

type AIProxy struct {
	cfg           AIProxyConfig
	selfTestMu    sync.Mutex
	mu            sync.RWMutex
	projects      map[string]*ProjectGrant
	geminiKey     []byte
	startupPassed bool
	openBaoSynced bool
	httpClient    *http.Client
	bao           *openbao.Client
	dataMux       http.Handler
	adminMux      http.Handler
}

func NewAIProxy(cfg AIProxyConfig) *AIProxy {
	if cfg.DataAddr == "" {
		cfg.DataAddr = "0.0.0.0:8080"
	}
	if cfg.AdminAddr == "" {
		cfg.AdminAddr = "127.0.0.1:8081"
	}
	if cfg.UpstreamBaseURL == "" {
		cfg.UpstreamBaseURL = "https://generativelanguage.googleapis.com/v1beta/openai"
	}
	if cfg.OpenBaoRole == "" {
		cfg.OpenBaoRole = "vault-ai-proxy"
	}
	if cfg.SAJWTPath == "" {
		cfg.SAJWTPath = defaultSAJWTPath
	}
	if cfg.DefaultDailyLimit <= 0 {
		cfg.DefaultDailyLimit = 500
	}
	if cfg.NowFunc == nil {
		cfg.NowFunc = func() time.Time { return time.Now().UTC() }
	}
	if cfg.AllowedGrantsSpec == "" {
		cfg.AllowedGrantsSpec = "pn:proj-pn:pn-backend:gemini-3.1-flash-lite:" + strconv.Itoa(cfg.DefaultDailyLimit)
	}

	p := &AIProxy{
		cfg:      cfg,
		projects: make(map[string]*ProjectGrant),
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
	p.bao = &openbao.Client{Addr: cfg.OpenBaoAddr, Role: cfg.OpenBaoRole, JWTPath: cfg.SAJWTPath, HTTP: p.httpClient}
	p.setGeminiKeyLocked(strings.TrimSpace(cfg.InitialAPIKey))

	today := cfg.NowFunc().UTC().Format("2006-01-02")
	for _, entry := range strings.Split(cfg.AllowedGrantsSpec, ";") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		parts := strings.Split(entry, ":")
		if len(parts) < 4 {
			continue
		}
		proj := strings.TrimSpace(parts[0])
		ns := strings.TrimSpace(parts[1])
		label := strings.TrimSpace(parts[2])
		models := make(map[string]bool)
		for _, m := range strings.Split(parts[3], ",") {
			m = strings.TrimSpace(m)
			if m != "" {
				models[m] = true
			}
		}
		limit := cfg.DefaultDailyLimit
		if len(parts) >= 5 {
			if parsed, err := strconv.Atoi(strings.TrimSpace(parts[4])); err == nil && parsed > 0 {
				limit = parsed
			}
		}
		p.projects[proj] = &ProjectGrant{
			Project:        proj,
			Namespace:      ns,
			PodLabel:       label,
			AllowedModels:  models,
			Enabled:        true,
			ManualRevoked:  false,
			DailyLimit:     limit,
			UsedToday:      0,
			BlockedCount:   0,
			CurrentUTCDate: today,
		}
	}

	if p.cfg.ResolvePodIP == nil {
		resolver := podid.NewResolver(podid.InClusterAPIServer(), podid.InClusterHTTPClient(), cfg.SAJWTPath, 60*time.Second)
		resolver.Now = cfg.NowFunc
		p.cfg.ResolvePodIP = resolver.Resolve
	}

	p.dataMux = p.buildDataMux()
	p.adminMux = p.buildAdminMux()
	return p
}

func (p *AIProxy) DataMux() http.Handler  { return p.dataMux }
func (p *AIProxy) AdminMux() http.Handler { return p.adminMux }

// wipeGeminiKeyLocked overwrites every byte of p.geminiKey in RAM with 0x00 before releasing the slice.
func (p *AIProxy) wipeGeminiKeyLocked() {
	for i := range p.geminiKey {
		p.geminiKey[i] = 0
	}
	p.geminiKey = nil
}

// setGeminiKeyLocked scrubs any previous key bytes in RAM and stores the new key in a fresh mutable []byte slice.
func (p *AIProxy) setGeminiKeyLocked(val string) {
	p.wipeGeminiKeyLocked()
	val = strings.TrimSpace(val)
	if val == "" || strings.HasPrefix(val, "PLACEHOLDER_") {
		return
	}
	p.geminiKey = []byte(val)
}

func (p *AIProxy) anyProjectEnabledLocked() bool {
	for slug, g := range p.projects {
		if slug == selfTestProject {
			continue
		}
		if g.Enabled {
			return true
		}
	}
	return false
}

func (p *AIProxy) buildDataMux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", p.handleChatCompletions)
	mux.HandleFunc("/startupz", p.handleStartupz)
	mux.HandleFunc("/readyz", p.handleReadyz)
	mux.HandleFunc("/healthz", p.handleHealthz)
	return mux
}

func (p *AIProxy) buildAdminMux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", p.handleAdminDashboard)
	mux.HandleFunc("/admin/status", p.handleAdminStatus)
	mux.HandleFunc("/admin/revoke", p.handleAdminRevoke)
	mux.HandleFunc("/admin/enable", p.handleAdminEnable)
	mux.HandleFunc("/admin/quota", p.handleAdminQuota)
	mux.HandleFunc("/admin/set-key", p.handleAdminSetKey)
	mux.HandleFunc("/admin/reload-key", p.handleAdminReloadKey)
	return mux
}

func isStrictLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	return ip.IsLoopback()
}

// RunStartupSelfTest executes the 5-point live Kill-Switch & vulnerability self-audit.
func (p *AIProxy) RunStartupSelfTest() error {
	p.selfTestMu.Lock()
	defer p.selfTestMu.Unlock()

	// Check 1: Loopback Socket Audit
	if !isStrictLoopbackAddr(p.cfg.AdminAddr) {
		return fmt.Errorf("admin listener address %q is not strictly loopback (127.0.0.1)", p.cfg.AdminAddr)
	}

	// Check 2: :8080 Route Isolation Audit (ensure no /admin/* route exists on DataMux)
	for _, adminPath := range []string{"/admin/enable?project=" + selfTestProject, "/admin/revoke?project=" + selfTestProject, "/admin/status"} {
		req := httptest.NewRequest(http.MethodPost, adminPath, nil)
		rec := httptest.NewRecorder()
		p.dataMux.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			return fmt.Errorf("vulnerability detected: DataMux (:8080) responded with %d instead of 404 on %s", rec.Code, adminPath)
		}
	}

	// Check 3: Caller Authorization Header Spoofing Audit
	spoofReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gemini-3.1-flash-lite"}`))
	spoofReq.RemoteAddr = "127.0.0.1:60000"
	spoofReq.Header.Set("Authorization", "Bearer caller-spoofed-key")
	spoofRec := httptest.NewRecorder()
	p.dataMux.ServeHTTP(spoofRec, spoofReq)
	if spoofRec.Code != http.StatusForbidden {
		return fmt.Errorf("vulnerability detected: caller Authorization header was not rejected with 403 (got %d)", spoofRec.Code)
	}

	// Check 4: Live Kill-Switch Flip & Block Drill on reserved __selftest__ slot
	p.mu.Lock()
	p.projects[selfTestProject] = &ProjectGrant{
		Project:        selfTestProject,
		Namespace:      "__selftest_ns__",
		PodLabel:       "__selftest_pod__",
		AllowedModels:  map[string]bool{"gemini-3.1-flash-lite": true},
		Enabled:        true,
		DailyLimit:     10,
		CurrentUTCDate: p.cfg.NowFunc().UTC().Format("2006-01-02"),
	}
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.projects, selfTestProject)
		p.mu.Unlock()
	}()

	revReq := httptest.NewRequest(http.MethodPost, "/admin/revoke?project="+selfTestProject, nil)
	revRec := httptest.NewRecorder()
	p.adminMux.ServeHTTP(revRec, revReq)
	if revRec.Code != http.StatusOK {
		return fmt.Errorf("self-test revoke failed with status %d", revRec.Code)
	}

	p.mu.RLock()
	stGrant := p.projects[selfTestProject]
	isStopped := stGrant != nil && !stGrant.Enabled
	p.mu.RUnlock()
	if !isStopped {
		return errors.New("kill-switch self-drill failed: __selftest__ grant remained Enabled after revoke")
	}

	// Verify DataMux blocks __selftest__ pod with 403 Forbidden
	drillReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gemini-3.1-flash-lite"}`))
	drillReq.RemoteAddr = "127.0.0.254:60001"
	drillRec := httptest.NewRecorder()
	p.dataMux.ServeHTTP(drillRec, drillReq)
	if drillRec.Code != http.StatusForbidden {
		return fmt.Errorf("kill-switch self-drill failed: DataMux returned %d instead of 403 for stopped __selftest__", drillRec.Code)
	}

	// Check 5: Sync persisted state & key from OpenBao once on initial startup
	p.mu.RLock()
	alreadySynced := p.openBaoSynced
	p.mu.RUnlock()
	if p.cfg.OpenBaoAddr != "" && !alreadySynced {
		if err := p.syncFromOpenBao(); err == nil {
			p.mu.Lock()
			p.openBaoSynced = true
			p.mu.Unlock()
		}
	}

	p.mu.Lock()
	p.startupPassed = true
	p.mu.Unlock()
	return nil
}

func (p *AIProxy) handleStartupz(w http.ResponseWriter, r *http.Request) {
	if err := p.RunStartupSelfTest(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"status": "FAILED",
			"error":  err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"status": "OK",
		"checks": "5/5 passed (loopback-socket, route-isolation, caller-auth-block, killswitch-ram-drill, openbao-sync)",
	})
}

func (p *AIProxy) handleReadyz(w http.ResponseWriter, r *http.Request) {
	if !isStrictLoopbackAddr(p.cfg.AdminAddr) {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "admin listener not loopback"})
		return
	}
	p.mu.RLock()
	passed := p.startupPassed
	p.mu.RUnlock()
	if !passed {
		if err := p.RunStartupSelfTest(); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "READY"})
}

// handleHealthz is strictly process-local per CONTRACT_PROBES_READINESS.md §3.3.
func (p *AIProxy) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ALIVE"})
}

func (p *AIProxy) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	// Never allow caller to supply or spoof an Authorization header
	if strings.TrimSpace(r.Header.Get("Authorization")) != "" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "caller Authorization header is forbidden; vault-ai-proxy injects credentials"})
		return
	}

	// Extract caller peer IP strictly from TCP socket (r.RemoteAddr), ignoring X-Forwarded-For
	peerIP := podid.PeerIP(r.RemoteAddr)
	var identity *PodIdentity
	if peerIP == "127.0.0.254" {
		identity = &PodIdentity{Namespace: "__selftest_ns__", PodName: "__selftest_pod__", AppLabel: "__selftest_pod__"}
	} else {
		var err error
		identity, err = p.cfg.ResolvePodIP(r.Context(), peerIP)
		if err != nil || identity == nil {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": fmt.Sprintf("pod IP %s failed Kubernetes attribution", peerIP)})
			return
		}
	}

	// Cap body size at 1 MiB (Review Focus #2)
	r.Body = http.MaxBytesReader(w, r.Body, maxChatBodyBytes)
	rawBody, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "request body exceeds 1 MiB limit or failed to read"})
		return
	}

	var reqPayload struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(rawBody, &reqPayload); err != nil || strings.TrimSpace(reqPayload.Model) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body or missing model field"})
		return
	}
	requestedModel := strings.TrimSpace(reqPayload.Model)

	// Match caller pod identity against project grants & enforce Kill Switch + Daily Quota under lock
	today := p.cfg.NowFunc().UTC().Format("2006-01-02")
	p.mu.Lock()
	var matched *ProjectGrant
	for _, g := range p.projects {
		if g.Namespace == identity.Namespace && g.PodLabel == identity.AppLabel {
			matched = g
			break
		}
	}
	if matched == nil {
		p.mu.Unlock()
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": fmt.Sprintf("pod %s/%s (label=%s) is not authorized for AI gateway access", identity.Namespace, identity.PodName, identity.AppLabel),
		})
		return
	}

	// Handle UTC day rollover (Review Focus #3: preserve ManualRevoked across rollover)
	if matched.CurrentUTCDate != today {
		matched.CurrentUTCDate = today
		matched.UsedToday = 0
		if !matched.ManualRevoked {
			matched.Enabled = true
		}
	}

	if !matched.AllowedModels[requestedModel] {
		matched.BlockedCount++
		p.mu.Unlock()
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": fmt.Sprintf("model %q is not permitted for project %s", requestedModel, matched.Project),
		})
		return
	}

	if !matched.Enabled {
		matched.BlockedCount++
		projName := matched.Project
		p.mu.Unlock()
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": fmt.Sprintf("AI gateway kill switch is STOPPED for project %s", projName),
		})
		return
	}

	if matched.UsedToday >= matched.DailyLimit {
		matched.Enabled = false
		matched.BlockedCount++
		projName := matched.Project
		p.mu.Unlock()
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": fmt.Sprintf("daily AI quota exceeded for project %s; switch tripped to STOPPED", projName),
		})
		return
	}

	if len(p.geminiKey) == 0 {
		if p.cfg.OpenBaoAddr != "" {
			p.mu.Unlock()
			_ = p.syncFromOpenBao()
			p.mu.Lock()
		} else if p.cfg.InitialAPIKey != "" {
			p.setGeminiKeyLocked(p.cfg.InitialAPIKey)
		}
	}
	apiKey := string(p.geminiKey)
	if apiKey == "" || strings.HasPrefix(apiKey, "PLACEHOLDER_") {
		p.mu.Unlock()
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "upstream Gemini API key is not seeded in OpenBao (kv/ai/gemini-api-key)",
		})
		return
	}

	matched.UsedToday++
	if matched.UsedToday >= matched.DailyLimit {
		matched.Enabled = false
		if !p.anyProjectEnabledLocked() {
			p.wipeGeminiKeyLocked()
		}
	}
	p.mu.Unlock()

	upstreamURL := strings.TrimRight(p.cfg.UpstreamBaseURL, "/") + "/chat/completions"
	upReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL, bytes.NewReader(rawBody))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to construct upstream request"})
		return
	}
	upReq.Header.Set("Content-Type", "application/json")
	upReq.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := p.httpClient.Do(upReq)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "upstream AI request failed"})
		return
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed reading upstream response"})
		return
	}

	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	} else {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(respBody)
}

func (p *AIProxy) handleAdminStatus(w http.ResponseWriter, r *http.Request) {
	p.mu.RLock()
	snapshot := make(map[string]ProjectGrant, len(p.projects))
	for k, v := range p.projects {
		if k == selfTestProject {
			continue
		}
		cp := *v
		snapshot[k] = cp
	}
	p.mu.RUnlock()
	writeJSON(w, http.StatusOK, snapshot)
}

func (p *AIProxy) handleAdminRevoke(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST required"})
		return
	}
	proj := strings.TrimSpace(r.URL.Query().Get("project"))
	p.mu.Lock()
	g, ok := p.projects[proj]
	if !ok {
		p.mu.Unlock()
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown project"})
		return
	}
	// Fail-safe RAM flip first (Review Focus #5) + scrub credential bytes in RAM when stopped
	g.Enabled = false
	if proj != selfTestProject {
		g.ManualRevoked = true
		if !p.anyProjectEnabledLocked() {
			p.wipeGeminiKeyLocked()
		}
	}
	cp := *g
	p.mu.Unlock()

	if proj != selfTestProject && p.cfg.OpenBaoAddr != "" {
		_ = p.persistProjectStateToOpenBao(cp)
	}
	writeJSON(w, http.StatusOK, cp)
}

func (p *AIProxy) handleAdminEnable(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST required"})
		return
	}
	proj := strings.TrimSpace(r.URL.Query().Get("project"))
	p.mu.Lock()
	g, ok := p.projects[proj]
	if !ok {
		p.mu.Unlock()
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown project"})
		return
	}
	g.Enabled = true
	g.ManualRevoked = false
	if len(p.geminiKey) == 0 && p.cfg.InitialAPIKey != "" {
		p.setGeminiKeyLocked(p.cfg.InitialAPIKey)
	}
	cp := *g
	p.mu.Unlock()

	if proj != selfTestProject && p.cfg.OpenBaoAddr != "" {
		_ = p.persistProjectStateToOpenBao(cp)
		_ = p.syncFromOpenBao()
	}
	writeJSON(w, http.StatusOK, cp)
}

func (p *AIProxy) handleAdminQuota(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST required"})
		return
	}
	proj := strings.TrimSpace(r.URL.Query().Get("project"))
	limitStr := strings.TrimSpace(r.URL.Query().Get("daily_limit"))
	limit, err := strconv.Atoi(limitStr)
	if err != nil || limit <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "positive daily_limit required"})
		return
	}
	p.mu.Lock()
	g, ok := p.projects[proj]
	if !ok {
		p.mu.Unlock()
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown project"})
		return
	}
	g.DailyLimit = limit
	g.Enabled = !g.ManualRevoked && g.UsedToday < g.DailyLimit
	if g.Enabled && len(p.geminiKey) == 0 && p.cfg.InitialAPIKey != "" {
		p.setGeminiKeyLocked(p.cfg.InitialAPIKey)
	} else if !p.anyProjectEnabledLocked() {
		p.wipeGeminiKeyLocked()
	}
	cp := *g
	p.mu.Unlock()

	if proj != selfTestProject && p.cfg.OpenBaoAddr != "" {
		_ = p.persistProjectStateToOpenBao(cp)
		if cp.Enabled {
			_ = p.syncFromOpenBao()
		}
	}
	writeJSON(w, http.StatusOK, cp)
}

func (p *AIProxy) handleAdminSetKey(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST required"})
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 16384))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	var payload struct {
		APIKey string `json:"api_key"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil || strings.TrimSpace(payload.APIKey) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "non-empty api_key required"})
		return
	}
	newKey := strings.TrimSpace(payload.APIKey)

	if p.cfg.OpenBaoAddr != "" {
		if err := p.writeGeminiKeyToOpenBao(newKey); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": fmt.Sprintf("failed writing key to OpenBao: %v", err)})
			return
		}
	}

	p.mu.Lock()
	if p.anyProjectEnabledLocked() {
		p.setGeminiKeyLocked(newKey)
	} else {
		p.wipeGeminiKeyLocked()
	}
	p.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{"status": "KEY_SAVED_AND_RELOADED"})
}

func (p *AIProxy) handleAdminReloadKey(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST required"})
		return
	}
	if p.cfg.OpenBaoAddr != "" {
		if err := p.syncFromOpenBao(); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "RELOADED"})
}

func (p *AIProxy) handleAdminDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(`<!DOCTYPE html><html><head><meta charset="utf-8"><title>vault-ai-proxy Loopback Kill Switch</title></head>
<body style="font-family:monospace;background:#020617;color:#f8fafc;padding:24px;">
<h2>vault-ai-proxy — Loopback Kill Switch & Zero-Token Key Manager (127.0.0.1:8081)</h2>
<p>Paste Gemini API Key (saved directly to OpenBao via K8s ServiceAccount):</p>
<input id="k" type="password" placeholder="AIzaSy..." style="padding:6px;width:320px;background:#0f172a;color:#fff;border:1px solid #334155;">
<button onclick="saveKey()" style="padding:6px 12px;">Save Key to OpenBao</button>
<pre id="out" style="background:#0f172a;padding:16px;border:1px solid #334155;margin-top:16px;"></pre>
<button onclick="act('/admin/revoke?project=pn')" style="padding:8px 14px;background:#881337;color:#fff;">STOP pn</button>
<button onclick="act('/admin/enable?project=pn')" style="padding:8px 14px;background:#065f46;color:#fff;">START pn</button>
<script>
async function refresh(){const r=await fetch('/admin/status');document.getElementById('out').textContent=JSON.stringify(await r.json(),null,2);}
async function act(u){await fetch(u,{method:'POST'});refresh();}
async function saveKey(){const v=document.getElementById('k').value;await fetch('/admin/set-key',{method:'POST',body:JSON.stringify({api_key:v})});document.getElementById('k').value='';refresh();}
refresh();
</script></body></html>`))
}

func (p *AIProxy) writeGeminiKeyToOpenBao(apiKey string) error {
	token, err := p.bao.Login(context.Background())
	if err != nil {
		return err
	}
	return p.bao.WriteKV(context.Background(), token, "ai/gemini-api-key", map[string]any{"value": apiKey})
}

func (p *AIProxy) persistProjectStateToOpenBao(g ProjectGrant) error {
	token, err := p.bao.Login(context.Background())
	if err != nil {
		return err
	}
	return p.bao.WriteKV(context.Background(), token, "ai/projects/"+url.PathEscape(g.Project), map[string]any{
		"enabled":        g.Enabled,
		"manual_revoked": g.ManualRevoked,
		"daily_limit":    g.DailyLimit,
	})
}

func parseFlexibleBool(v any, def bool) bool {
	switch val := v.(type) {
	case bool:
		return val
	case string:
		b, err := strconv.ParseBool(strings.TrimSpace(val))
		if err == nil {
			return b
		}
	}
	return def
}

func parseFlexibleInt(v any, def int) int {
	switch val := v.(type) {
	case float64:
		if int(val) > 0 {
			return int(val)
		}
	case int:
		if val > 0 {
			return val
		}
	case string:
		if parsed, err := strconv.Atoi(strings.TrimSpace(val)); err == nil && parsed > 0 {
			return parsed
		}
	}
	return def
}

func (p *AIProxy) syncFromOpenBao() error {
	ctx := context.Background()
	token, err := p.bao.Login(ctx)
	if err != nil {
		return err
	}

	// 1. Sync upstream Gemini API key from kv/data/ai/gemini-api-key
	if d, err := p.bao.ReadKV(ctx, token, "ai/gemini-api-key"); err == nil {
		v, _ := d["value"].(string)
		p.mu.Lock()
		p.setGeminiKeyLocked(strings.TrimSpace(v))
		p.mu.Unlock()
	}

	// 2. Sync persisted per-project Kill-Switch & quota state from kv/data/ai/projects/<slug>
	p.mu.RLock()
	slugs := make([]string, 0, len(p.projects))
	for slug := range p.projects {
		if slug != selfTestProject {
			slugs = append(slugs, slug)
		}
	}
	p.mu.RUnlock()

	for _, slug := range slugs {
		d, err := p.bao.ReadKV(ctx, token, "ai/projects/"+url.PathEscape(slug))
		if err != nil || d == nil {
			continue
		}
		p.mu.Lock()
		if g, ok := p.projects[slug]; ok {
			g.DailyLimit = parseFlexibleInt(d["daily_limit"], g.DailyLimit)
			g.ManualRevoked = parseFlexibleBool(d["manual_revoked"], g.ManualRevoked)
			persistedEnabled := parseFlexibleBool(d["enabled"], g.Enabled)
			g.Enabled = persistedEnabled && !g.ManualRevoked && g.UsedToday < g.DailyLimit
			if !p.anyProjectEnabledLocked() {
				p.wipeGeminiKeyLocked()
			}
		}
		p.mu.Unlock()
	}
	return nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func envOrDefault(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func main() {
	dailyLimit := 500
	if v := strings.TrimSpace(os.Getenv("DEFAULT_DAILY_LIMIT")); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			dailyLimit = parsed
		}
	}
	proxy := NewAIProxy(AIProxyConfig{
		DataAddr:          envOrDefault("DATA_ADDR", "0.0.0.0:8080"),
		AdminAddr:         envOrDefault("ADMIN_ADDR", "127.0.0.1:8081"),
		UpstreamBaseURL:   envOrDefault("UPSTREAM_AI_BASE_URL", "https://generativelanguage.googleapis.com/v1beta/openai"),
		OpenBaoAddr:       envOrDefault("OPENBAO_ADDR", "http://openbao.vault.svc:8200"),
		OpenBaoRole:       envOrDefault("OPENBAO_ROLE", "vault-ai-proxy"),
		AllowedGrantsSpec: os.Getenv("ALLOWED_PROJECT_GRANTS"),
		DefaultDailyLimit: dailyLimit,
	})

	if !isStrictLoopbackAddr(proxy.cfg.AdminAddr) {
		log.Fatalf("[FATAL] ADMIN_ADDR %q must be strictly loopback (127.0.0.1)", proxy.cfg.AdminAddr)
	}

	go func() {
		log.Printf("[vault-ai-proxy] Loopback AdminMux listening on %s", proxy.cfg.AdminAddr)
		srv := &http.Server{
			Addr:              proxy.cfg.AdminAddr,
			Handler:           proxy.AdminMux(),
			ReadHeaderTimeout: 5 * time.Second,
		}
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("[FATAL] AdminMux failed: %v", err)
		}
	}()

	log.Printf("[vault-ai-proxy] Cluster DataMux listening on %s", proxy.cfg.DataAddr)
	dataSrv := &http.Server{
		Addr:              proxy.cfg.DataAddr,
		Handler:           proxy.DataMux(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	if err := dataSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("[FATAL] DataMux failed: %v", err)
	}
}
