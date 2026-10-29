package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"nacfson_pipeline/services/vault-proxies/internal/podid"
)

const (
	// maxNotifyBodyBytes bounds a POST body: a 1 MiB body plus a 1 MiB
	// attachment plus JSON escaping overhead.
	maxNotifyBodyBytes = 2 << 20
	maxAdminBodyBytes  = 4 << 10

	selfTestNamespace = "__selftest_ns__"
	selfTestAppLabel  = "__selftest_pod__"
)

// selfTestIdentityKey carries a synthetic caller identity on the request
// context. Only in-process code can set a context value, so a network client
// cannot use it to bypass attribution.
type selfTestIdentityKey struct{}

// Grant allows pods labelled app=AppLabel in Namespace to post to Channel.
type Grant struct {
	Channel   string
	Namespace string
	AppLabel  string
}

func validChannelName(s string) bool {
	if s == "" || len(s) > 63 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
		default:
			return false
		}
	}
	return true
}

// ParseGrants parses "channel:namespace:app,channel:namespace:app". An empty
// spec yields no grants.
func ParseGrants(s string) ([]Grant, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var grants []Grant
	for _, entry := range strings.Split(s, ",") {
		parts := strings.Split(strings.TrimSpace(entry), ":")
		if len(parts) != 3 {
			return nil, fmt.Errorf("grant %q must be channel:namespace:app", entry)
		}
		g := Grant{Channel: strings.TrimSpace(parts[0]), Namespace: strings.TrimSpace(parts[1]), AppLabel: strings.TrimSpace(parts[2])}
		if !validChannelName(g.Channel) {
			return nil, fmt.Errorf("grant %q has an invalid channel name", entry)
		}
		if g.Namespace == "" || g.AppLabel == "" {
			return nil, fmt.Errorf("grant %q has an empty namespace or app", entry)
		}
		grants = append(grants, g)
	}
	return grants, nil
}

// VaultStore persists Discord webhook URLs (secrets) per channel.
type VaultStore interface {
	LoadWebhook(ctx context.Context, channel string) (string, error)
	SaveWebhook(ctx context.Context, channel, url string) error
}

type NotifyProxyConfig struct {
	DataAddr  string
	AdminAddr string
	Grants    []Grant
	Resolve   func(ctx context.Context, ip string) (*podid.Identity, error)
	Vault     VaultStore
	Send      func(ctx context.Context, url string, n Notification) error
	// Logger defaults to a discarding logger. It never receives webhook URLs.
	Logger *slog.Logger
}

type channelState struct {
	webhook string // cached secret; never logged or returned
	sent    int
	failed  int
}

type NotifyProxy struct {
	cfg      NotifyProxyConfig
	log      *slog.Logger
	dataMux  http.Handler
	adminMux http.Handler

	mu       sync.Mutex
	channels map[string]*channelState

	selfTestMu    sync.Mutex
	startupPassed bool
	preloaded     bool
}

func NewNotifyProxy(cfg NotifyProxyConfig) *NotifyProxy {
	p := &NotifyProxy{cfg: cfg, log: cfg.Logger, channels: map[string]*channelState{}}
	if p.log == nil {
		p.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	for _, g := range cfg.Grants {
		p.channels[g.Channel] = &channelState{}
	}
	p.dataMux = p.buildDataMux()
	p.adminMux = p.buildAdminMux()
	return p
}

func (p *NotifyProxy) DataMux() http.Handler  { return p.dataMux }
func (p *NotifyProxy) AdminMux() http.Handler { return p.adminMux }

func (p *NotifyProxy) buildDataMux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/notify/{channel}", p.handleNotify)
	mux.HandleFunc("GET /startupz", p.handleStartupz)
	mux.HandleFunc("GET /readyz", p.handleReadyz)
	mux.HandleFunc("GET /healthz", p.handleHealthz)
	return mux
}

func (p *NotifyProxy) buildAdminMux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /admin/set-webhook", p.handleAdminSetWebhook)
	mux.HandleFunc("GET /admin/status", p.handleAdminStatus)
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

func (p *NotifyProxy) granted(channel string, id *podid.Identity) bool {
	for _, g := range p.cfg.Grants {
		if g.Channel == channel && g.Namespace == id.Namespace && g.AppLabel == id.AppLabel {
			return true
		}
	}
	return false
}

// webhookFor returns the cached webhook URL, loading it from the vault on the
// first use. The result is cached only when non-empty.
func (p *NotifyProxy) webhookFor(ctx context.Context, channel string) (string, error) {
	p.mu.Lock()
	cs := p.channels[channel]
	cached := ""
	if cs != nil {
		cached = cs.webhook
	}
	p.mu.Unlock()
	if cached != "" {
		return cached, nil
	}
	if p.cfg.Vault == nil {
		return "", errors.New("no vault configured")
	}
	loaded, err := p.cfg.Vault.LoadWebhook(ctx, channel)
	if err != nil {
		return "", err
	}
	if loaded == "" {
		return "", errors.New("webhook not configured")
	}
	p.mu.Lock()
	if cs := p.channels[channel]; cs != nil {
		cs.webhook = loaded
	}
	p.mu.Unlock()
	return loaded, nil
}

func (p *NotifyProxy) count(channel string, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if cs := p.channels[channel]; cs != nil {
		if ok {
			cs.sent++
		} else {
			cs.failed++
		}
	}
}

func (p *NotifyProxy) handleNotify(w http.ResponseWriter, r *http.Request) {
	channel := r.PathValue("channel")
	p.mu.Lock()
	_, known := p.channels[channel]
	p.mu.Unlock()
	if !known {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown channel"})
		return
	}

	// Identity comes only from the TCP peer address, never from headers.
	var id *podid.Identity
	if v, ok := r.Context().Value(selfTestIdentityKey{}).(*podid.Identity); ok {
		id = v
	} else if p.cfg.Resolve != nil {
		if resolved, err := p.cfg.Resolve(r.Context(), podid.PeerIP(r.RemoteAddr)); err == nil {
			id = resolved
		}
	}
	if id == nil || !p.granted(channel, id) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "caller is not granted for this channel"})
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxNotifyBodyBytes)
	var n Notification
	if err := json.NewDecoder(r.Body).Decode(&n); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body or body exceeds 2 MiB"})
		return
	}
	if err := n.Validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	webhook, err := p.webhookFor(r.Context(), channel)
	if err != nil {
		p.log.Warn("webhook unavailable", "channel", channel, "error", err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "channel webhook not configured"})
		return
	}
	if p.cfg.Send == nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "delivery failed"})
		return
	}
	if err := p.cfg.Send(r.Context(), webhook, n); err != nil {
		p.count(channel, false)
		p.log.Warn("delivery failed", "channel", channel, "namespace", id.Namespace, "pod", id.PodName, "error", err.Error())
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "delivery failed"})
		return
	}
	p.count(channel, true)
	p.log.Info("notification delivered", "channel", channel, "namespace", id.Namespace, "pod", id.PodName, "severity", n.Severity)
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
}

// validateWebhookURL accepts only https://discord.com/api/webhooks/... URLs.
func validateWebhookURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("url is not parseable")
	}
	if u.Scheme != "https" {
		return errors.New("url scheme must be https")
	}
	if u.Host != "discord.com" || u.User != nil {
		return errors.New("url host must be discord.com")
	}
	if !strings.HasPrefix(u.Path, "/api/webhooks/") {
		return errors.New("url path must start with /api/webhooks/")
	}
	return nil
}

func (p *NotifyProxy) handleAdminSetWebhook(w http.ResponseWriter, r *http.Request) {
	channel := r.URL.Query().Get("channel")
	p.mu.Lock()
	_, known := p.channels[channel]
	p.mu.Unlock()
	if !known {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown channel"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAdminBodyBytes)
	var body struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	if err := validateWebhookURL(body.URL); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if p.cfg.Vault == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "no vault configured"})
		return
	}
	if err := p.cfg.Vault.SaveWebhook(r.Context(), channel, body.URL); err != nil {
		p.log.Warn("save webhook failed", "channel", channel, "error", err.Error())
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to persist webhook"})
		return
	}
	p.mu.Lock()
	p.channels[channel].webhook = body.URL
	p.mu.Unlock()
	p.log.Info("webhook updated", "channel", channel)
	writeJSON(w, http.StatusOK, map[string]any{"channel": channel, "configured": true})
}

func (p *NotifyProxy) handleAdminStatus(w http.ResponseWriter, r *http.Request) {
	type row struct {
		Channel    string `json:"channel"`
		Configured bool   `json:"configured"`
		Sent       int    `json:"sent"`
		Failed     int    `json:"failed"`
	}
	p.mu.Lock()
	rows := make([]row, 0, len(p.channels))
	for name, cs := range p.channels {
		rows = append(rows, row{Channel: name, Configured: cs.webhook != "", Sent: cs.sent, Failed: cs.failed})
	}
	p.mu.Unlock()
	sort.Slice(rows, func(i, j int) bool { return rows[i].Channel < rows[j].Channel })
	writeJSON(w, http.StatusOK, rows)
}

// RunStartupSelfTest audits the listener and routing invariants: the admin
// listener is loopback, the data mux exposes no admin route, and a caller that
// holds no grant is refused with 403. It never calls Send and never depends on
// Discord.
func (p *NotifyProxy) RunStartupSelfTest() error {
	p.selfTestMu.Lock()
	defer p.selfTestMu.Unlock()

	// Check 1: loopback socket audit.
	if !isStrictLoopbackAddr(p.cfg.AdminAddr) {
		return fmt.Errorf("admin listener address %q is not strictly loopback (127.0.0.1)", p.cfg.AdminAddr)
	}

	// Check 2: route isolation (no admin route on the data mux).
	for _, probe := range []struct{ method, path string }{
		{http.MethodPost, "/admin/set-webhook?channel=errors"},
		{http.MethodGet, "/admin/status"},
	} {
		rec := httptest.NewRecorder()
		p.dataMux.ServeHTTP(rec, httptest.NewRequest(probe.method, probe.path, nil))
		if rec.Code != http.StatusNotFound {
			return fmt.Errorf("vulnerability detected: DataMux (:8080) responded with %d instead of 404 on %s", rec.Code, probe.path)
		}
	}

	// Check 3: an ungranted caller is refused with 403 on every channel. The
	// synthetic identity is injected through the request context, which a
	// network client cannot set, so the check works whatever the configured
	// resolver would say and needs no magic peer IP.
	ungranted := &podid.Identity{Namespace: selfTestNamespace, PodName: selfTestAppLabel, AppLabel: selfTestAppLabel}
	p.mu.Lock()
	channels := make([]string, 0, len(p.channels))
	for name := range p.channels {
		channels = append(channels, name)
	}
	p.mu.Unlock()
	sort.Strings(channels)
	for _, ch := range channels {
		req := httptest.NewRequest(http.MethodPost, "/v1/notify/"+ch, strings.NewReader(`{}`))
		req = req.WithContext(context.WithValue(req.Context(), selfTestIdentityKey{}, ungranted))
		rec := httptest.NewRecorder()
		p.dataMux.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			return fmt.Errorf("vulnerability detected: ungranted caller was not rejected with 403 on channel %q (got %d)", ch, rec.Code)
		}
	}

	// Best-effort one-time warm-up of the webhook cache. Failures are fine:
	// readiness must not depend on OpenBao or Discord.
	if !p.preloaded && p.cfg.Vault != nil {
		p.preloaded = true
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		for _, ch := range channels {
			if _, err := p.webhookFor(ctx, ch); err != nil {
				p.log.Warn("webhook preload failed", "channel", ch, "error", err.Error())
			}
		}
		cancel()
	}

	p.mu.Lock()
	p.startupPassed = true
	p.mu.Unlock()
	return nil
}

func (p *NotifyProxy) handleStartupz(w http.ResponseWriter, r *http.Request) {
	if err := p.RunStartupSelfTest(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"status": "FAILED", "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"status": "OK",
		"checks": "3/3 passed (loopback-socket, route-isolation, ungranted-caller-block)",
	})
}

func (p *NotifyProxy) handleReadyz(w http.ResponseWriter, r *http.Request) {
	if !isStrictLoopbackAddr(p.cfg.AdminAddr) {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "admin listener not loopback"})
		return
	}
	p.mu.Lock()
	passed := p.startupPassed
	p.mu.Unlock()
	if !passed {
		if err := p.RunStartupSelfTest(); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "READY"})
}

// handleHealthz is strictly process-local per CONTRACT_PROBES_READINESS.md §3.3.
func (p *NotifyProxy) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ALIVE"})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
