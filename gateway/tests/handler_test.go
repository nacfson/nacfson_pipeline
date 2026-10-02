package tests

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"nacfson_pipeline/gateway/internal/auth"
	"nacfson_pipeline/gateway/internal/config"
	"nacfson_pipeline/gateway/internal/keycloak"
)

type mockSessionValidator struct {
	active bool
	err    error
}

func (m *mockSessionValidator) ValidateSession(sessionID string) (bool, error) {
	return m.active, m.err
}

func (m *mockSessionValidator) ExchangeTokenForProject(refreshToken, targetAudience string) (*keycloak.TokenResponse, error) {
	return nil, nil
}

func TestHeaderSanitization(t *testing.T) {
	cfg := &config.Config{
		CookieName:   "PLATFORM_SESSION",
		CookieDomain: ".example.com",
		HMACSecret:   []byte("test-secret-key-that-is-at-least-32-bytes!"),
	}
	handler := auth.NewHandler(cfg, &mockSessionValidator{active: true}, nil)

	// Craft request with malicious client-injected headers
	req := httptest.NewRequest("GET", "/auth", nil)
	req.Header.Set("X-Forwarded-Host", "pn.example.com")
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Uri", "/api/data")
	req.Header.Set("X-User-Subject", "malicious-injected-admin")
	req.Header.Set("X-User-Issuer", "https://attacker.com")
	req.Header.Set("Authorization", "Bearer forged-token")

	rr := httptest.NewRecorder()
	handler.HandleForwardAuth(rr, req)

	// Since there is no session cookie, should redirect to login (HTTP 302)
	if rr.Code != http.StatusFound {
		t.Fatalf("expected status 302 Found, got %d", rr.Code)
	}

	// Verify inbound headers were stripped from the request
	if req.Header.Get("X-User-Subject") != "" {
		t.Errorf("expected X-User-Subject to be stripped, got %s", req.Header.Get("X-User-Subject"))
	}
	if req.Header.Get("X-User-Issuer") != "" {
		t.Errorf("expected X-User-Issuer to be stripped, got %s", req.Header.Get("X-User-Issuer"))
	}
}

func TestAuthenticatedAccess(t *testing.T) {
	cfg := &config.Config{
		CookieName:     "PLATFORM_SESSION",
		CookieDomain:   ".example.com",
		HMACSecret:     []byte("test-secret-key-that-is-at-least-32-bytes!"),
		SessionTimeout: 1 * time.Hour,
	}
	handler := auth.NewHandler(cfg, &mockSessionValidator{active: true}, nil)

	// Encode a valid session cookie
	sess := auth.SessionCookieData{
		SessionID: "test-session-uuid",
		Subject:   "verified-user-123",
		Issuer:    "https://auth.example.com/realms/platform",
		Tokens: map[string]string{
			"project-pn": "valid-jwt-token-for-pn",
		},
		CreatedAt: time.Now().Unix(),
	}

	req := httptest.NewRequest("GET", "/auth", nil)
	req.Header.Set("X-Forwarded-Host", "pn.example.com")
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Uri", "/dashboard")

	// Set cookie using helper by crafting request
	cookieVal := encodeTestCookie(sess, cfg.HMACSecret)
	req.AddCookie(&http.Cookie{
		Name:  "PLATFORM_SESSION",
		Value: cookieVal,
	})

	rr := httptest.NewRecorder()
	handler.HandleForwardAuth(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200 OK, got %d: %s", rr.Code, rr.Body.String())
	}

	if authHdr := rr.Header().Get("Authorization"); authHdr != "Bearer valid-jwt-token-for-pn" {
		t.Errorf("expected Authorization header 'Bearer valid-jwt-token-for-pn', got '%s'", authHdr)
	}

	if subHdr := rr.Header().Get("X-User-Subject"); subHdr != "verified-user-123" {
		t.Errorf("expected X-User-Subject 'verified-user-123', got '%s'", subHdr)
	}

	if issHdr := rr.Header().Get("X-User-Issuer"); issHdr != "https://auth.example.com/realms/platform" {
		t.Errorf("expected X-User-Issuer 'https://auth.example.com/realms/platform', got '%s'", issHdr)
	}
}
