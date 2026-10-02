package tests

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"nacfson_pipeline/gateway/internal/auth"
	"nacfson_pipeline/gateway/internal/config"
)

type mockSessionTerminator struct {
	revokedSessions []string
	err             error
}

func (m *mockSessionTerminator) RevokeSession(sessionID string) error {
	m.revokedSessions = append(m.revokedSessions, sessionID)
	return m.err
}

func setupLogoutHandler() (*auth.Handler, *config.Config, *mockSessionTerminator) {
	cfg := &config.Config{
		CookieName:     "PLATFORM_SESSION",
		CookieDomain:   ".example.com",
		HMACSecret:     []byte("test-secret-key-that-is-at-least-32-bytes!"),
		SessionTimeout: 1 * time.Hour,
	}
	terminator := &mockSessionTerminator{}
	handler := auth.NewHandler(cfg, nil, nil)
	handler.SetRevoker(terminator)
	return handler, cfg, terminator
}

func TestLogoutUnauthorizedWhenNoCookie(t *testing.T) {
	handler, _, terminator := setupLogoutHandler()

	req := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	rr := httptest.NewRecorder()
	handler.HandleLogout(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected HTTP 401 Unauthorized, got %d", rr.Code)
	}
	if len(terminator.revokedSessions) != 0 {
		t.Errorf("expected 0 revocation calls, got %d", len(terminator.revokedSessions))
	}
}

func TestLogoutCSRFProtectionRejectsWithoutTokenOrOrigin(t *testing.T) {
	handler, cfg, terminator := setupLogoutHandler()

	sess := auth.SessionCookieData{
		SessionID: "sess-abc-123",
		Subject:   "user-1",
		CreatedAt: time.Now().Unix(),
	}

	req := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	req.AddCookie(&http.Cookie{
		Name:  cfg.CookieName,
		Value: encodeTestCookie(sess, cfg.HMACSecret),
	})

	rr := httptest.NewRecorder()
	handler.HandleLogout(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected HTTP 403 Forbidden for missing CSRF token/Origin, got %d", rr.Code)
	}

	var errResp map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("failed to parse JSON response: %v", err)
	}
	if errResp["error"] != "invalid_csrf_token" {
		t.Errorf("expected error 'invalid_csrf_token', got %q", errResp["error"])
	}
	if len(terminator.revokedSessions) != 0 {
		t.Errorf("expected 0 Keycloak revocation calls on CSRF rejection, got %d", len(terminator.revokedSessions))
	}
}

func TestLogoutCSRFProtectionRejectsWithTamperedToken(t *testing.T) {
	handler, cfg, terminator := setupLogoutHandler()

	sess := auth.SessionCookieData{
		SessionID: "sess-abc-123",
		Subject:   "user-1",
		CreatedAt: time.Now().Unix(),
	}

	req := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	req.Header.Set("X-CSRF-Token", "forged-invalid-csrf-token")
	req.AddCookie(&http.Cookie{
		Name:  cfg.CookieName,
		Value: encodeTestCookie(sess, cfg.HMACSecret),
	})

	rr := httptest.NewRecorder()
	handler.HandleLogout(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected HTTP 403 Forbidden, got %d", rr.Code)
	}
	if len(terminator.revokedSessions) != 0 {
		t.Errorf("expected 0 Keycloak calls on tampered CSRF, got %d", len(terminator.revokedSessions))
	}
}

func TestLogoutSuccessWithValidCSRFToken(t *testing.T) {
	handler, cfg, terminator := setupLogoutHandler()

	sess := auth.SessionCookieData{
		SessionID: "sess-valid-456",
		Subject:   "user-2",
		CreatedAt: time.Now().Unix(),
	}

	req := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	csrfToken := handler.GenerateCSRFToken(sess.SessionID)
	req.Header.Set("X-CSRF-Token", csrfToken)
	req.AddCookie(&http.Cookie{
		Name:  cfg.CookieName,
		Value: encodeTestCookie(sess, cfg.HMACSecret),
	})

	rr := httptest.NewRecorder()
	handler.HandleLogout(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200 OK, got %d: %s", rr.Code, rr.Body.String())
	}
	if len(terminator.revokedSessions) != 1 || terminator.revokedSessions[0] != "sess-valid-456" {
		t.Errorf("expected Keycloak revocation for 'sess-valid-456', got %v", terminator.revokedSessions)
	}

	// Verify cookie clearing
	cookies := rr.Result().Cookies()
	var sessionCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == cfg.CookieName {
			sessionCookie = c
			break
		}
	}
	if sessionCookie == nil {
		t.Fatal("expected Set-Cookie header clearing session")
	}
	if sessionCookie.MaxAge > 0 || sessionCookie.Value != "" {
		t.Errorf("expected session cookie to be expired and emptied, got value=%q, maxAge=%d", sessionCookie.Value, sessionCookie.MaxAge)
	}
}

func TestLogoutSuccessWithValidOriginAndRedirect(t *testing.T) {
	handler, cfg, terminator := setupLogoutHandler()

	sess := auth.SessionCookieData{
		SessionID: "sess-redirect-789",
		Subject:   "user-3",
		CreatedAt: time.Now().Unix(),
	}

	formData := url.Values{}
	formData.Set("redirect_uri", "https://pn.example.com/goodbye")

	req := httptest.NewRequest(http.MethodPost, "/auth/logout", strings.NewReader(formData.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://pn.example.com")
	req.AddCookie(&http.Cookie{
		Name:  cfg.CookieName,
		Value: encodeTestCookie(sess, cfg.HMACSecret),
	})

	rr := httptest.NewRecorder()
	handler.HandleLogout(rr, req)

	if rr.Code != http.StatusFound {
		t.Fatalf("expected HTTP 302 Found, got %d", rr.Code)
	}
	if loc := rr.Header().Get("Location"); loc != "https://pn.example.com/goodbye" {
		t.Errorf("expected redirect Location 'https://pn.example.com/goodbye', got %q", loc)
	}
	if len(terminator.revokedSessions) != 1 || terminator.revokedSessions[0] != "sess-redirect-789" {
		t.Errorf("expected revocation for 'sess-redirect-789', got %v", terminator.revokedSessions)
	}
}
