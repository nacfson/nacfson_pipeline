package tests

import (
	"encoding/json"
	"errors"
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
		CookieName:         "PLATFORM_SESSION",
		CookieDomain:       ".example.com",
		HMACSecret:         []byte("test-secret-key-that-is-at-least-32-bytes!"),
		SessionTimeout:     1 * time.Hour,
		AllowedReturnHosts: []string{"pn.example.com"},
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

func TestLogoutSuccessWithBearerSessionHandle(t *testing.T) {
	handler, _, terminator := setupLogoutHandler()

	handle, err := handler.SealSessionHandle(auth.SessionHandleData{
		SessionID: "sess-bearer-999",
		Subject:   "user-bearer",
		CreatedAt: time.Now().Unix(),
	})
	if err != nil {
		t.Fatalf("failed to seal session handle: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	req.Header.Set("Authorization", "Bearer "+handle)
	rr := httptest.NewRecorder()
	handler.HandleLogout(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200 OK for Bearer session_handle logout, got %d: %s", rr.Code, rr.Body.String())
	}
	if len(terminator.revokedSessions) != 1 || terminator.revokedSessions[0] != "sess-bearer-999" {
		t.Fatalf("expected revocation for 'sess-bearer-999', got %v", terminator.revokedSessions)
	}
}

func TestLogoutAllowlistedQueryRedirect(t *testing.T) {
	handler, cfg, terminator := setupLogoutHandler()
	sess := auth.SessionCookieData{
		SessionID: "sess-query-redirect",
		Subject:   "user-query",
		CreatedAt: time.Now().Unix(),
	}
	q := url.Values{}
	q.Set("redirect_uri", "https://pn.example.com/goodbye")
	req := httptest.NewRequest(http.MethodPost, "/auth/logout?"+q.Encode(), nil)
	req.Header.Set("Origin", "https://pn.example.com")
	req.AddCookie(&http.Cookie{
		Name:  cfg.CookieName,
		Value: encodeTestCookie(sess, cfg.HMACSecret),
	})

	rr := httptest.NewRecorder()
	handler.HandleLogout(rr, req)

	if rr.Code != http.StatusFound {
		t.Fatalf("expected HTTP 302 Found, got %d: %s", rr.Code, rr.Body.String())
	}
	if loc := rr.Header().Get("Location"); loc != "https://pn.example.com/goodbye" {
		t.Errorf("expected redirect Location 'https://pn.example.com/goodbye', got %q", loc)
	}
	if len(terminator.revokedSessions) != 1 || terminator.revokedSessions[0] != "sess-query-redirect" {
		t.Errorf("expected revocation for 'sess-query-redirect', got %v", terminator.revokedSessions)
	}
}

func TestLogoutFailedRevokeReturns502(t *testing.T) {
	handler, cfg, terminator := setupLogoutHandler()
	terminator.err = errors.New("kc down")
	sess := auth.SessionCookieData{
		SessionID: "sess-fail-502",
		Subject:   "user-502",
		CreatedAt: time.Now().Unix(),
	}
	form := url.Values{}
	form.Set("redirect_uri", "https://pn.example.com/goodbye")
	req := httptest.NewRequest(http.MethodPost, "/auth/logout", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://pn.example.com")
	req.AddCookie(&http.Cookie{
		Name:  cfg.CookieName,
		Value: encodeTestCookie(sess, cfg.HMACSecret),
	})

	rr := httptest.NewRecorder()
	handler.HandleLogout(rr, req)

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("expected HTTP 502, got %d: %s", rr.Code, rr.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode json: %v", err)
	}
	if body["error"] != "revocation_failed" {
		t.Errorf("error = %q, want revocation_failed", body["error"])
	}
	if strings.Contains(rr.Body.String(), "revoked") {
		t.Errorf("body must not contain revoked: %s", rr.Body.String())
	}
	assertNoPlatformSessionCookie(t, rr)
	if loc := rr.Header().Get("Location"); loc != "" {
		t.Errorf("Location = %q, want empty", loc)
	}
	if len(terminator.revokedSessions) != 1 {
		t.Errorf("revoke calls = %d, want 1", len(terminator.revokedSessions))
	}
}

func TestLogoutWithoutRevokerReturns503(t *testing.T) {
	handler, cfg, terminator := setupLogoutHandler()
	handler.SetRevoker(nil)
	sess := auth.SessionCookieData{
		SessionID: "sess-no-revoker",
		Subject:   "user-503",
		CreatedAt: time.Now().Unix(),
	}
	req := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	req.Header.Set("Origin", "https://pn.example.com")
	req.AddCookie(&http.Cookie{
		Name:  cfg.CookieName,
		Value: encodeTestCookie(sess, cfg.HMACSecret),
	})

	rr := httptest.NewRecorder()
	handler.HandleLogout(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected HTTP 503, got %d: %s", rr.Code, rr.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode json: %v", err)
	}
	if body["error"] != "revocation_unavailable" {
		t.Errorf("error = %q, want revocation_unavailable", body["error"])
	}
	assertNoPlatformSessionCookie(t, rr)
	if loc := rr.Header().Get("Location"); loc != "" {
		t.Errorf("Location = %q, want empty", loc)
	}
	if len(terminator.revokedSessions) != 0 {
		t.Errorf("revoke calls = %d, want 0", len(terminator.revokedSessions))
	}
}

func TestLogoutEmptySessionIDRejected(t *testing.T) {
	handler, cfg, terminator := setupLogoutHandler()
	sess := auth.SessionCookieData{
		SessionID: "",
		Subject:   "user-empty",
		CreatedAt: time.Now().Unix(),
	}
	cookieReq := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	cookieReq.Header.Set("Origin", "https://pn.example.com")
	cookieReq.AddCookie(&http.Cookie{
		Name:  cfg.CookieName,
		Value: encodeTestCookie(sess, cfg.HMACSecret),
	})
	cookieRR := httptest.NewRecorder()
	handler.HandleLogout(cookieRR, cookieReq)
	assertInvalidSessionNoRevoke(t, cookieRR, terminator)

	handle, err := handler.SealSessionHandle(auth.SessionHandleData{
		SessionID: "",
		Subject:   "user-empty-bearer",
		CreatedAt: time.Now().Unix(),
	})
	if err != nil {
		t.Fatalf("seal handle: %v", err)
	}
	bearerReq := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	bearerReq.Header.Set("Authorization", "Bearer "+handle)
	bearerRR := httptest.NewRecorder()
	handler.HandleLogout(bearerRR, bearerReq)
	assertInvalidSessionNoRevoke(t, bearerRR, terminator)
}

func TestLogoutIgnoresDisallowedRedirect(t *testing.T) {
	cases := []string{
		"//evil.test/x",
		"javascript:alert(1)",
		"http://pn.example.com/x",
		"https://evil.test/",
		"https://pn.example.com@evil.test/",
		"https://pn.example.com.evil.test/",
		"https://user:pass@pn.example.com/",
	}
	for _, redirectURI := range cases {
		for _, mode := range []string{"form", "query"} {
			t.Run(mode+" "+redirectURI, func(t *testing.T) {
				handler, cfg, terminator := setupLogoutHandler()
				sess := auth.SessionCookieData{
					SessionID: "sess-disallow",
					Subject:   "user-disallow",
					CreatedAt: time.Now().Unix(),
				}
				var req *http.Request
				if mode == "form" {
					form := url.Values{}
					form.Set("redirect_uri", redirectURI)
					req = httptest.NewRequest(http.MethodPost, "/auth/logout", strings.NewReader(form.Encode()))
					req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				} else {
					q := url.Values{}
					q.Set("redirect_uri", redirectURI)
					req = httptest.NewRequest(http.MethodPost, "/auth/logout?"+q.Encode(), nil)
				}
				req.Header.Set("Origin", "https://pn.example.com")
				req.AddCookie(&http.Cookie{
					Name:  cfg.CookieName,
					Value: encodeTestCookie(sess, cfg.HMACSecret),
				})
				rr := httptest.NewRecorder()
				handler.HandleLogout(rr, req)

				if rr.Code != http.StatusOK {
					t.Fatalf("expected HTTP 200, got %d: %s", rr.Code, rr.Body.String())
				}
				loc := rr.Header().Get("Location")
				if loc != "" {
					t.Errorf("Location = %q, want empty", loc)
				}
				if strings.Contains(loc, "evil") || strings.Contains(loc, "javascript") {
					t.Errorf("Location contains disallowed target: %q", loc)
				}
				var body map[string]string
				if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
					t.Fatalf("decode json: %v", err)
				}
				if body["status"] != "revoked" {
					t.Errorf("status = %q, want revoked", body["status"])
				}
				if len(terminator.revokedSessions) != 1 {
					t.Errorf("revoke calls = %d, want 1", len(terminator.revokedSessions))
				}
			})
		}
	}
}

func TestLogoutSignedOutRedirectStays401(t *testing.T) {
	handler, _, terminator := setupLogoutHandler()
	q := url.Values{}
	q.Set("redirect_uri", "https://evil.test/")
	req := httptest.NewRequest(http.MethodPost, "/auth/logout?"+q.Encode(), nil)
	rr := httptest.NewRecorder()
	handler.HandleLogout(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected HTTP 401, got %d: %s", rr.Code, rr.Body.String())
	}
	if loc := rr.Header().Get("Location"); loc != "" {
		t.Errorf("Location = %q, want empty", loc)
	}
	if len(terminator.revokedSessions) != 0 {
		t.Errorf("revoke calls = %d, want 0", len(terminator.revokedSessions))
	}
}

func assertNoPlatformSessionCookie(t *testing.T, rr *httptest.ResponseRecorder) {
	t.Helper()
	for _, c := range rr.Result().Cookies() {
		if c.Name == "PLATFORM_SESSION" {
			t.Errorf("PLATFORM_SESSION Set-Cookie must be absent, got %s", c.String())
		}
	}
}

func assertInvalidSessionNoRevoke(t *testing.T, rr *httptest.ResponseRecorder, terminator *mockSessionTerminator) {
	t.Helper()
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected HTTP 400, got %d: %s", rr.Code, rr.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode json: %v", err)
	}
	if body["error"] != "invalid_session" {
		t.Errorf("error = %q, want invalid_session", body["error"])
	}
	assertNoPlatformSessionCookie(t, rr)
	if loc := rr.Header().Get("Location"); loc != "" {
		t.Errorf("Location = %q, want empty", loc)
	}
	if len(terminator.revokedSessions) != 0 {
		t.Errorf("revoke calls = %d, want 0", len(terminator.revokedSessions))
	}
}
