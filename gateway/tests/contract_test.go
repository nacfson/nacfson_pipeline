package tests

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"nacfson_pipeline/gateway/internal/auth"
	"nacfson_pipeline/gateway/internal/config"
)

func encodeTestCookie(data auth.SessionCookieData, secret []byte) string {
	b, _ := json.Marshal(data)
	mac := hmac.New(sha256.New, secret)
	mac.Write(b)
	sig := mac.Sum(nil)
	return fmt.Sprintf("%s.%s",
		base64.RawURLEncoding.EncodeToString(b),
		base64.RawURLEncoding.EncodeToString(sig),
	)
}

func TestForwardAuthContractFailClosed(t *testing.T) {
	cfg := &config.Config{
		CookieName:     "PLATFORM_SESSION",
		CookieDomain:   ".example.com",
		HMACSecret:     []byte("test-secret-key-that-is-at-least-32-bytes!"),
		SessionTimeout: 1 * time.Hour,
	}

	// Mock validator that returns network error simulating Keycloak outage
	mockVal := &mockSessionValidator{
		active: false,
		err:    errors.New("connection refused: keycloak is down"),
	}
	handler := auth.NewHandler(cfg, mockVal, nil)

	sess := auth.SessionCookieData{
		SessionID: "test-session-uuid",
		Subject:   "test-sub",
		Issuer:    "https://auth.example.com",
		Tokens:    map[string]string{"default": "some-token"},
		CreatedAt: time.Now().Unix(),
	}

	req := httptest.NewRequest("GET", "/auth", nil)
	req.Header.Set("X-Forwarded-Host", "pn.example.com")
	req.AddCookie(&http.Cookie{
		Name:  "PLATFORM_SESSION",
		Value: encodeTestCookie(sess, cfg.HMACSecret),
	})

	rr := httptest.NewRecorder()
	handler.HandleForwardAuth(rr, req)

	// Contract requires HTTP 503 Service Unavailable (Fail-Closed)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected HTTP 503 on dependency outage, got %d", rr.Code)
	}

	// Verify no upstream injection headers are leaked
	if rr.Header().Get("Authorization") != "" {
		t.Errorf("Authorization header must NOT be emitted on 503")
	}
}

func TestForwardAuthContractRevokedSession(t *testing.T) {
	cfg := &config.Config{
		CookieName:     "PLATFORM_SESSION",
		CookieDomain:   ".example.com",
		HMACSecret:     []byte("test-secret-key-that-is-at-least-32-bytes!"),
		SessionTimeout: 1 * time.Hour,
	}

	// Mock validator reporting session is revoked
	mockVal := &mockSessionValidator{
		active: false,
		err:    nil,
	}
	handler := auth.NewHandler(cfg, mockVal, nil)

	sess := auth.SessionCookieData{
		SessionID: "revoked-session-uuid",
		Subject:   "test-sub",
		Issuer:    "https://auth.example.com",
		Tokens:    map[string]string{"default": "old-token"},
		CreatedAt: time.Now().Unix(),
	}

	req := httptest.NewRequest("GET", "/auth", nil)
	req.Header.Set("X-Forwarded-Host", "pn.example.com")
	req.AddCookie(&http.Cookie{
		Name:  "PLATFORM_SESSION",
		Value: encodeTestCookie(sess, cfg.HMACSecret),
	})

	rr := httptest.NewRecorder()
	handler.HandleForwardAuth(rr, req)

	// Contract requires HTTP 401 Unauthorized
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected HTTP 401 on revoked session, got %d", rr.Code)
	}
}
