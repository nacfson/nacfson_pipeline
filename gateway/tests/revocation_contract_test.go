package tests

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"nacfson_pipeline/gateway/internal/auth"
	"nacfson_pipeline/gateway/internal/config"
	"nacfson_pipeline/gateway/internal/keycloak"
)

// statefulSessionRegistry simulates Keycloak's active session state across multiple devices.
type statefulSessionRegistry struct {
	mu       sync.Mutex
	sessions map[string]bool
}

func newSessionRegistry() *statefulSessionRegistry {
	return &statefulSessionRegistry{
		sessions: make(map[string]bool),
	}
}

func (r *statefulSessionRegistry) ValidateSession(sessionID string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sessions[sessionID], nil
}

func (r *statefulSessionRegistry) ExchangeTokenForProject(refreshToken, targetAudience string) (*keycloak.TokenResponse, error) {
	return nil, nil
}

func (r *statefulSessionRegistry) RevokeSession(sessionID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.sessions, sessionID)
	return nil
}

// TestRevocationContract verifies the formal contract in contracts/session-revocation.md
func TestRevocationContract(t *testing.T) {
	cfg := &config.Config{
		CookieName:     "PLATFORM_SESSION",
		CookieDomain:   ".example.com",
		HMACSecret:     []byte("test-secret-key-that-is-at-least-32-bytes!"),
		SessionTimeout: 1 * time.Hour,
	}

	registry := newSessionRegistry()
	// Register two sessions: sess-device-1 and sess-device-2
	registry.sessions["sess-device-1"] = true
	registry.sessions["sess-device-2"] = true

	handler := auth.NewHandler(cfg, registry, nil)
	handler.SetRevoker(registry)

	// Step 1: User on Device 1 accesses Project PN -> Should succeed (HTTP 200)
	sess1 := auth.SessionCookieData{
		SessionID: "sess-device-1",
		Subject:   "alice-user",
		Issuer:    "https://auth.example.com",
		Tokens:    map[string]string{"project-pn": "token-device-1"},
		CreatedAt: time.Now().Unix(),
	}

	reqPN := httptest.NewRequest(http.MethodGet, "/auth", nil)
	reqPN.Header.Set("X-Forwarded-Host", "pn.example.com")
	reqPN.AddCookie(&http.Cookie{
		Name:  cfg.CookieName,
		Value: encodeTestCookie(sess1, cfg.HMACSecret),
	})
	rrPN := httptest.NewRecorder()
	handler.HandleForwardAuth(rrPN, reqPN)
	if rrPN.Code != http.StatusOK {
		t.Fatalf("expected active session 1 to succeed, got %d", rrPN.Code)
	}

	// Step 2: User triggers POST /auth/logout from Project PN
	formData := url.Values{}
	formData.Set("redirect_uri", "https://pn.example.com/goodbye")
	reqLogout := httptest.NewRequest(http.MethodPost, "/auth/logout", strings.NewReader(formData.Encode()))
	reqLogout.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reqLogout.Header.Set("Origin", "https://pn.example.com")
	reqLogout.AddCookie(&http.Cookie{
		Name:  cfg.CookieName,
		Value: encodeTestCookie(sess1, cfg.HMACSecret),
	})
	rrLogout := httptest.NewRecorder()
	handler.HandleLogout(rrLogout, reqLogout)

	// Verify Case A Contract: HTTP 302, Location, Set-Cookie clearing session
	if rrLogout.Code != http.StatusFound {
		t.Fatalf("expected HTTP 302 Found, got %d", rrLogout.Code)
	}
	if loc := rrLogout.Header().Get("Location"); loc != "https://pn.example.com/goodbye" {
		t.Errorf("expected Location 'https://pn.example.com/goodbye', got %q", loc)
	}

	cookies := rrLogout.Result().Cookies()
	var clearedCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == cfg.CookieName {
			clearedCookie = c
			break
		}
	}
	if clearedCookie == nil {
		t.Fatal("expected Set-Cookie clearing PLATFORM_SESSION")
	}
	if clearedCookie.MaxAge > 0 || clearedCookie.Value != "" {
		t.Errorf("expected cleared cookie, got val=%q, maxAge=%d", clearedCookie.Value, clearedCookie.MaxAge)
	}
	expectedDomain := strings.TrimPrefix(cfg.CookieDomain, ".")
	if strings.TrimPrefix(clearedCookie.Domain, ".") != expectedDomain {
		t.Errorf("expected cookie domain %q, got %q", expectedDomain, clearedCookie.Domain)
	}

	// Step 3: Next Request Guarantee - Immediate next request using revoked session cookie MUST be denied
	reqNext := httptest.NewRequest(http.MethodGet, "/auth", nil)
	reqNext.Header.Set("X-Forwarded-Host", "project-b.example.com")
	reqNext.AddCookie(&http.Cookie{
		Name:  cfg.CookieName,
		Value: encodeTestCookie(sess1, cfg.HMACSecret),
	})
	rrNext := httptest.NewRecorder()
	handler.HandleForwardAuth(rrNext, reqNext)
	if rrNext.Code != http.StatusUnauthorized {
		t.Fatalf("expected immediate next request for revoked session to be HTTP 401, got %d", rrNext.Code)
	}

	// Step 4: Device Isolation Guarantee - Independent session on Device 2 MUST remain unaffected
	sess2 := auth.SessionCookieData{
		SessionID: "sess-device-2",
		Subject:   "alice-user",
		Issuer:    "https://auth.example.com",
		Tokens:    map[string]string{"project-pn": "token-device-2"},
		CreatedAt: time.Now().Unix(),
	}
	reqDev2 := httptest.NewRequest(http.MethodGet, "/auth", nil)
	reqDev2.Header.Set("X-Forwarded-Host", "pn.example.com")
	reqDev2.AddCookie(&http.Cookie{
		Name:  cfg.CookieName,
		Value: encodeTestCookie(sess2, cfg.HMACSecret),
	})
	rrDev2 := httptest.NewRecorder()
	handler.HandleForwardAuth(rrDev2, reqDev2)
	if rrDev2.Code != http.StatusOK {
		t.Fatalf("expected Device 2 session to remain active (HTTP 200), got %d", rrDev2.Code)
	}
}

// TestRevocationContractCSRFRejection verifies Case B Contract: Cross-site request rejected
func TestRevocationContractCSRFRejection(t *testing.T) {
	cfg := &config.Config{
		CookieName:     "PLATFORM_SESSION",
		CookieDomain:   ".example.com",
		HMACSecret:     []byte("test-secret-key-that-is-at-least-32-bytes!"),
		SessionTimeout: 1 * time.Hour,
	}

	registry := newSessionRegistry()
	registry.sessions["sess-active-victim"] = true

	handler := auth.NewHandler(cfg, registry, nil)
	handler.SetRevoker(registry)

	sess := auth.SessionCookieData{
		SessionID: "sess-active-victim",
		Subject:   "victim-user",
		CreatedAt: time.Now().Unix(),
	}

	// Attacker site sends cross-site POST /auth/logout
	req := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	req.Header.Set("Origin", "https://attacker.evil.com")
	req.AddCookie(&http.Cookie{
		Name:  cfg.CookieName,
		Value: encodeTestCookie(sess, cfg.HMACSecret),
	})

	rr := httptest.NewRecorder()
	handler.HandleLogout(rr, req)

	// Case B Contract: HTTP 403 Forbidden with exact payload
	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected HTTP 403 Forbidden, got %d", rr.Code)
	}

	var res map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &res); err != nil {
		t.Fatalf("failed to decode json body: %v", err)
	}
	if res["error"] != "invalid_csrf_token" {
		t.Errorf("expected error 'invalid_csrf_token', got %q", res["error"])
	}
	if res["message"] != "Cross-site request forgery validation failed." {
		t.Errorf("expected message 'Cross-site request forgery validation failed.', got %q", res["message"])
	}

	// Active victim session MUST remain untouched in Keycloak
	if !registry.sessions["sess-active-victim"] {
		t.Errorf("victim session was unexpectedly revoked during CSRF attack")
	}
}
