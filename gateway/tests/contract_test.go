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
	"nacfson_pipeline/gateway/internal/keycloak"
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

	// An unreachable sign-in service does not confirm the user.
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

	// A rejected session leaves the visitor unsigned-in for the part that asked.
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected HTTP 401 on revoked session, got %d", rr.Code)
	}
	if rr.Header().Get("Authorization") != "" {
		t.Errorf("Authorization header must NOT be emitted on 401")
	}
}

func TestConfirmationDoesNotRequireCookie(t *testing.T) {
	cfg := &config.Config{
		CookieName:     "PLATFORM_SESSION",
		CookieDomain:   ".example.com",
		HMACSecret:     []byte("test-secret-key-that-is-at-least-32-bytes!"),
		SessionTimeout: 1 * time.Hour,
	}
	handler := auth.NewHandler(cfg, &mockSessionValidator{active: true}, nil)

	req := httptest.NewRequest(http.MethodGet, "https://pn.example.com/", nil)
	req.Header.Set("X-Forwarded-Host", "pn.example.com")
	rr := httptest.NewRecorder()
	handler.HandleForwardAuth(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected HTTP 401 without a cookie, got %d", rr.Code)
	}
	if loc := rr.Header().Get("Location"); loc != "" {
		t.Fatalf("opening the project host must not redirect to sign-in, got %q", loc)
	}
}

func TestConfirmationRejectsForeignAudience(t *testing.T) {
	cfg := &config.Config{
		CookieName:     "PLATFORM_SESSION",
		CookieDomain:   ".example.com",
		HMACSecret:     []byte("test-secret-key-that-is-at-least-32-bytes!"),
		SessionTimeout: 1 * time.Hour,
	}
	handler := auth.NewHandler(cfg, &mockSessionValidator{active: true}, nil)

	sess := auth.SessionCookieData{
		SessionID: "other-project-session",
		Subject:   "test-sub",
		Issuer:    "https://auth.example.com",
		Tokens:    map[string]string{"project-other": "token-for-other"},
		CreatedAt: time.Now().Unix(),
	}
	req := httptest.NewRequest(http.MethodGet, "/auth", nil)
	req.Header.Set("X-Forwarded-Host", "pn.example.com")
	req.AddCookie(&http.Cookie{Name: cfg.CookieName, Value: encodeTestCookie(sess, cfg.HMACSecret)})
	rr := httptest.NewRecorder()
	handler.HandleForwardAuth(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected HTTP 401 for a session signed for another project, got %d", rr.Code)
	}
	if rr.Header().Get("Authorization") != "" || rr.Header().Get("X-User-Subject") != "" {
		t.Fatal("a foreign audience must not be confirmed")
	}
}

type recordingExchanger struct {
	called bool
	token  *keycloak.TokenResponse
	err    error
}

func (e *recordingExchanger) ExchangeCode(code, codeVerifier, redirectURI string) (*keycloak.TokenResponse, error) {
	e.called = true
	return e.token, e.err
}

func signTestState(payload auth.StatePayload, secret []byte) string {
	b, _ := json.Marshal(payload)
	mac := hmac.New(sha256.New, secret)
	mac.Write(b)
	sig := mac.Sum(nil)
	return fmt.Sprintf("%s.%s",
		base64.RawURLEncoding.EncodeToString(b),
		base64.RawURLEncoding.EncodeToString(sig),
	)
}

func callbackRequest(secret []byte, target string) *http.Request {
	verifier := "test-verifier-value"
	sum := sha256.Sum256([]byte(verifier))
	state := signTestState(auth.StatePayload{
		TargetURL:      target,
		Nonce:          "1",
		Timestamp:      time.Now().Unix(),
		VerifierDigest: base64.RawURLEncoding.EncodeToString(sum[:]),
	}, secret)
	req := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=abc&state="+state, nil)
	req.AddCookie(&http.Cookie{Name: "OIDC_AUTH_STATE", Value: verifier})
	return req
}

func TestSessionReturnRejectsForeignHost(t *testing.T) {
	secret := []byte("test-secret-key-that-is-at-least-32-bytes!")
	cfg := &config.Config{
		CookieName:         "PLATFORM_SESSION",
		CookieDomain:       ".example.com",
		HMACSecret:         secret,
		SessionTimeout:     1 * time.Hour,
		AllowedReturnHosts: []string{"pn.example.com"},
		KeycloakIssuerURL:  "https://auth.example.com/realms/platform",
	}
	exchanger := &recordingExchanger{}
	handler := auth.NewHandler(cfg, nil, nil)
	handler.Exchanger = exchanger

	start := httptest.NewRequest(http.MethodGet, "/oauth/start?return=https://other.example.com/steal", nil)
	startRR := httptest.NewRecorder()
	handler.HandleSessionStart(startRR, start)
	if startRR.Code != http.StatusBadRequest {
		t.Fatalf("expected HTTP 400 for a foreign return, got %d", startRR.Code)
	}

	req := callbackRequest(secret, "https://other.example.com/steal")
	rr := httptest.NewRecorder()
	handler.HandleCallback(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected HTTP 400 for a foreign callback return, got %d", rr.Code)
	}
	if exchanger.called {
		t.Fatal("a foreign return must be rejected before the sign-in service is called")
	}
	if rr.Header().Get("Location") != "" {
		t.Fatalf("foreign return must not redirect, got %q", rr.Header().Get("Location"))
	}
}

func TestSessionReturnCookieScopedToProjectHost(t *testing.T) {
	secret := []byte("test-secret-key-that-is-at-least-32-bytes!")
	cfg := &config.Config{
		CookieName:         "PLATFORM_SESSION",
		CookieDomain:       ".example.com",
		HMACSecret:         secret,
		SessionTimeout:     1 * time.Hour,
		AllowedReturnHosts: []string{"pn.example.com"},
		KeycloakIssuerURL:  "https://auth.example.com/realms/platform",
	}
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"user-1"}`))
	exchanger := &recordingExchanger{token: &keycloak.TokenResponse{
		AccessToken:  "hdr." + payload + ".sig",
		SessionState: "sess-1",
	}}
	handler := auth.NewHandler(cfg, nil, nil)
	handler.Exchanger = exchanger

	returnTo := "https://pn.example.com/dashboard"
	req := callbackRequest(secret, returnTo)
	rr := httptest.NewRecorder()
	handler.HandleCallback(rr, req)

	if rr.Code != http.StatusFound {
		t.Fatalf("expected HTTP 302 back to the project, got %d body %s", rr.Code, rr.Body.String())
	}
	if loc := rr.Header().Get("Location"); loc != returnTo {
		t.Fatalf("expected return to %s, got %q", returnTo, loc)
	}

	var session *http.Cookie
	for _, c := range rr.Result().Cookies() {
		if c.Name == cfg.CookieName && c.Value != "" {
			session = c
			break
		}
	}
	if session == nil {
		t.Fatal("expected a project session cookie")
	}
	if session.Domain != "pn.example.com" {
		t.Fatalf("cookie must be scoped to the project host, got %q", session.Domain)
	}
	if !session.HttpOnly || !session.Secure {
		t.Fatalf("cookie must be HttpOnly and Secure, HttpOnly=%v Secure=%v", session.HttpOnly, session.Secure)
	}
}
