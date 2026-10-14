package tests

import (
	"encoding/base64"
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

	// Opening confirmation without a cookie leaves the visitor unsigned-in.
	// It does not redirect to the sign-in host.
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401 Unauthorized, got %d", rr.Code)
	}
	if loc := rr.Header().Get("Location"); loc != "" {
		t.Fatalf("unsigned-in confirmation must not redirect, got Location %q", loc)
	}
	if rr.Header().Get("Authorization") != "" {
		t.Errorf("Authorization must not be emitted when the visitor is unsigned-in")
	}

	// Verify inbound headers were stripped from the request
	if req.Header.Get("X-User-Subject") != "" {
		t.Errorf("expected X-User-Subject to be stripped, got %s", req.Header.Get("X-User-Subject"))
	}
	if req.Header.Get("X-User-Issuer") != "" {
		t.Errorf("expected X-User-Issuer to be stripped, got %s", req.Header.Get("X-User-Issuer"))
	}
	if req.Header.Get("Authorization") != "" {
		t.Errorf("expected Authorization to be stripped, got %s", req.Header.Get("Authorization"))
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

func TestDirectGoogleSignIn(t *testing.T) {
	cfg := &config.Config{
		CookieName:         "PLATFORM_SESSION",
		CookieDomain:       ".nacfson.cloud",
		HMACSecret:         []byte("test-secret-key-that-is-at-least-32-bytes!"),
		AllowedReturnHosts: []string{"pn.nacfson.cloud"},
		KeycloakPublicURL:  "https://auth.nacfson.cloud/realms/platform",
		KeycloakIssuerURL:  "http://keycloak-service.identity.svc:8080/realms/platform",
		ClientID:           "gateway-client",
	}
	handler := auth.NewHandler(cfg, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/oauth/start?return=https://pn.nacfson.cloud/api/auth/callback", nil)
	rr := httptest.NewRecorder()
	handler.HandleSessionStart(rr, req)

	if rr.Code != http.StatusFound {
		t.Fatalf("expected 302 Found, got %d", rr.Code)
	}
	loc := rr.Header().Get("Location")
	if loc == "" {
		t.Fatal("expected Location header on /oauth/start")
	}
	if want := "kc_idp_hint=google"; !containsSubstring(loc, want) {
		t.Fatalf("expected %q in redirect URL, got %s", want, loc)
	}
	if len(rr.Result().Cookies()) != 1 || rr.Result().Cookies()[0].Name != "OIDC_AUTH_STATE" || rr.Result().Cookies()[0].Domain != "" {
		t.Fatalf("expected 1 host-only OIDC_AUTH_STATE cookie on /oauth/start, got %+v", rr.Result().Cookies())
	}
}

func TestStatelessEncryptedStateAndCallbackReloadGracefulRestart(t *testing.T) {
	secret := []byte("test-secret-key-that-is-at-least-32-bytes!")
	cfg := &config.Config{
		CookieName:         "PLATFORM_SESSION",
		CookieDomain:       ".nacfson.cloud",
		HMACSecret:         secret,
		AllowedReturnHosts: []string{"pn.nacfson.cloud"},
		KeycloakPublicURL:  "https://auth.nacfson.cloud/realms/platform",
		KeycloakIssuerURL:  "http://keycloak-service.identity.svc:8080/realms/platform",
		SessionTimeout:     8 * time.Hour,
	}
	handler := auth.NewHandler(cfg, nil, nil)
	handler.Exchanger = &recordingExchanger{
		token: &keycloak.TokenResponse{
			AccessToken:  "hdr." + base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"user-google-1"}`)) + ".sig",
			RefreshToken: "refresh-token-1",
			SessionState: "kc-session-1",
		},
	}

	// 1. Start login and capture encrypted state parameter + host-only OIDC_AUTH_STATE binding cookie
	startReq := httptest.NewRequest(http.MethodGet, "/oauth/start?return=https://pn.nacfson.cloud/api/auth/callback", nil)
	startRR := httptest.NewRecorder()
	handler.HandleSessionStart(startRR, startReq)
	if startRR.Code != http.StatusFound {
		t.Fatalf("expected 302 Found on start, got %d", startRR.Code)
	}
	locURL, err := startRR.Result().Location()
	if err != nil {
		t.Fatalf("failed to parse start redirect URL: %v", err)
	}
	stateParam := locURL.Query().Get("state")
	if stateParam == "" {
		t.Fatal("expected encrypted state query param")
	}
	startCookies := startRR.Result().Cookies()
	if len(startCookies) != 1 || startCookies[0].Name != "OIDC_AUTH_STATE" {
		t.Fatalf("expected host-only OIDC_AUTH_STATE binding cookie, got %+v", startCookies)
	}

	// 2. Callback WITHOUT the OIDC_AUTH_STATE binding cookie must be rejected (prevents Login CSRF)
	csrfReq := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=auth-code-1&state="+stateParam, nil)
	csrfRR := httptest.NewRecorder()
	handler.HandleCallback(csrfRR, csrfReq)
	if csrfRR.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request on unbound Login CSRF callback, got %d", csrfRR.Code)
	}

	// 3. Callback WITH the host-only OIDC_AUTH_STATE binding cookie succeeds and emits zero PLATFORM_SESSION cookies
	cbReq := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=auth-code-1&state="+stateParam, nil)
	cbReq.AddCookie(startCookies[0])
	cbRR := httptest.NewRecorder()
	handler.HandleCallback(cbRR, cbReq)
	if cbRR.Code != http.StatusFound {
		t.Fatalf("expected 302 Found on bound callback, got %d: %s", cbRR.Code, cbRR.Body.String())
	}
	cbLoc, err := cbRR.Result().Location()
	if err != nil {
		t.Fatalf("failed to parse callback redirect URL: %v", err)
	}
	if cbLoc.Host != "pn.nacfson.cloud" || cbLoc.Path != "/api/auth/callback" {
		t.Fatalf("expected redirect to https://pn.nacfson.cloud/api/auth/callback, got %s", cbLoc.String())
	}
	if cbLoc.Query().Get("ticket") == "" {
		t.Fatalf("expected one-time handoff ticket in callback redirect URL, got %s", cbLoc.String())
	}
	for _, c := range cbRR.Result().Cookies() {
		if c.Name == "PLATFORM_SESSION" {
			t.Fatalf("expected zero PLATFORM_SESSION cookies on callback, got %+v", c)
		}
	}

	// 4. Expired callback on a valid return URL restarts login gracefully (302) instead of 400
	expiredState := signTestState(auth.StatePayload{
		TargetURL: "https://pn.nacfson.cloud/api/auth/callback",
		Nonce:     "expired-1",
		Timestamp: time.Now().Add(-15 * time.Minute).Unix(),
	}, secret)
	reloadReq := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=old&state="+expiredState, nil)
	reloadRR := httptest.NewRecorder()
	handler.HandleCallback(reloadRR, reloadReq)
	if reloadRR.Code != http.StatusFound {
		t.Fatalf("expected 302 graceful restart on expired callback, got %d: %s", reloadRR.Code, reloadRR.Body.String())
	}
	if !containsSubstring(reloadRR.Header().Get("Location"), "/oauth/start?return=") {
		t.Fatalf("expected graceful redirect to /oauth/start?return=..., got %q", reloadRR.Header().Get("Location"))
	}
}

func containsSubstring(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexString(s, sub) >= 0)
}

func indexString(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
