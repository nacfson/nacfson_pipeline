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
	"strings"
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

func TestSessionReturnHandoffTicketAndExchangeZeroCookies(t *testing.T) {
	secret := []byte("test-secret-key-that-is-at-least-32-bytes!")
	cfg := &config.Config{
		CookieName:         "PLATFORM_SESSION",
		CookieDomain:       ".example.com",
		HMACSecret:         secret,
		SessionTimeout:     1 * time.Hour,
		AllowedReturnHosts: []string{"pn.example.com"},
		KeycloakPublicURL:  "https://auth.example.com/realms/platform",
		KeycloakIssuerURL:  "http://keycloak-service.identity.svc:8080/realms/platform",
	}
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"user-1"}`))
	exchanger := &recordingExchanger{token: &keycloak.TokenResponse{
		AccessToken:  "hdr." + payload + ".sig",
		RefreshToken: "refresh-tok-1",
		SessionState: "sess-1",
	}}
	handler := auth.NewHandler(cfg, &mockSessionValidator{active: true}, nil)
	handler.Exchanger = exchanger

	returnTo := "https://pn.example.com/api/auth/callback"
	req := callbackRequest(secret, returnTo)
	rr := httptest.NewRecorder()
	handler.HandleCallback(rr, req)

	if rr.Code != http.StatusFound {
		t.Fatalf("expected HTTP 302 back to the project backend callback, got %d body %s", rr.Code, rr.Body.String())
	}
	locURL, err := rr.Result().Location()
	if err != nil {
		t.Fatalf("invalid redirect Location: %v", err)
	}
	if locURL.Host != "pn.example.com" || locURL.Path != "/api/auth/callback" {
		t.Fatalf("expected return to %s, got %q", returnTo, locURL.String())
	}
	ticket := locURL.Query().Get("ticket")
	if ticket == "" {
		t.Fatal("expected one-time handoff ticket query parameter on callback redirect")
	}
	for _, c := range rr.Result().Cookies() {
		if c.Name == "PLATFORM_SESSION" {
			t.Fatalf("expected zero PLATFORM_SESSION cookies emitted on callback redirect, got %+v", c)
		}
	}

	// Exchange ticket via POST /auth/exchange from Project Backend
	exchBody := fmt.Sprintf(`{"ticket":%q}`, ticket)
	exchReq := httptest.NewRequest(http.MethodPost, "/auth/exchange", strings.NewReader(exchBody))
	exchReq.Header.Set("Content-Type", "application/json")
	exchReq.Header.Set("X-Forwarded-Host", "pn.example.com")
	exchRR := httptest.NewRecorder()
	handler.HandleTicketExchange(exchRR, exchReq)

	if exchRR.Code != http.StatusOK {
		t.Fatalf("expected 200 OK from /auth/exchange, got %d: %s", exchRR.Code, exchRR.Body.String())
	}
	var exchResp struct {
		SessionHandle string `json:"session_handle"`
		Subject       string `json:"subject"`
		Issuer        string `json:"issuer"`
		Audience      string `json:"audience"`
	}
	if err := json.Unmarshal(exchRR.Body.Bytes(), &exchResp); err != nil {
		t.Fatalf("failed to decode exchange response: %v", err)
	}
	if exchResp.SessionHandle == "" || exchResp.Subject != "user-1" || exchResp.Audience != "project-pn" {
		t.Fatalf("unexpected exchange payload: %+v", exchResp)
	}
	if exchResp.Issuer != "https://auth.example.com/realms/platform" {
		t.Fatalf("expected canonical public issuer, got %q", exchResp.Issuer)
	}

	// Replaying the same one-time ticket MUST be rejected with 401
	replayReq := httptest.NewRequest(http.MethodPost, "/auth/exchange", strings.NewReader(exchBody))
	replayReq.Header.Set("X-Forwarded-Host", "pn.example.com")
	replayRR := httptest.NewRecorder()
	handler.HandleTicketExchange(replayRR, replayReq)
	if replayRR.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized on replayed ticket, got %d", replayRR.Code)
	}

	// Verify GET /auth with Authorization: Bearer <session_handle> (zero cookies)
	verifyReq := httptest.NewRequest(http.MethodGet, "/auth", nil)
	verifyReq.Header.Set("X-Forwarded-Host", "pn.example.com")
	verifyReq.Header.Set("Authorization", "Bearer "+exchResp.SessionHandle)
	verifyRR := httptest.NewRecorder()
	handler.HandleForwardAuth(verifyRR, verifyReq)

	if verifyRR.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for Bearer session_handle, got %d: %s", verifyRR.Code, verifyRR.Body.String())
	}
	if verifyRR.Header().Get("X-User-Subject") != "user-1" {
		t.Fatalf("expected X-User-Subject 'user-1', got %q", verifyRR.Header().Get("X-User-Subject"))
	}
}

func TestTransparentTokenRefreshAndRealKeycloakIntrospection(t *testing.T) {
	// Mock Keycloak server that rejects the expired access token on /token/introspect
	// and issues a fresh access token on /token (grant_type=refresh_token).
	var introspectedTokens []string
	mockKC := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		switch r.URL.Path {
		case "/realms/platform/protocol/openid-connect/token/introspect":
			tok := r.FormValue("token")
			introspectedTokens = append(introspectedTokens, tok)
			w.Header().Set("Content-Type", "application/json")
			if tok == "fresh-access-token" {
				_, _ = w.Write([]byte(`{"active":true}`))
			} else {
				_, _ = w.Write([]byte(`{"active":false}`))
			}
		case "/realms/platform/protocol/openid-connect/token":
			if r.FormValue("grant_type") == "refresh_token" && r.FormValue("refresh_token") == "valid-refresh-token" {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"access_token":"fresh-access-token","refresh_token":"rotated-refresh-token","expires_in":300}`))
				return
			}
			w.WriteHeader(http.StatusBadRequest)
		default:
			http.NotFound(w, r)
		}
	}))
	defer mockKC.Close()

	cfg := &config.Config{
		CookieName:        "PLATFORM_SESSION",
		CookieDomain:      ".nacfson.cloud",
		HMACSecret:        []byte("test-secret-key-that-is-at-least-32-bytes!"),
		SessionTimeout:    8 * time.Hour,
		KeycloakPublicURL: "https://auth.nacfson.cloud/realms/platform",
		KeycloakIssuerURL: mockKC.URL + "/realms/platform",
		KeycloakAdminURL:  mockKC.URL,
		ClientID:          "gateway-client",
		ClientSecret:      "gateway-secret",
	}
	kcClient := keycloak.NewClient(cfg.KeycloakIssuerURL, cfg.KeycloakAdminURL, cfg.ClientID, cfg.ClientSecret)
	handler := auth.NewHandler(cfg, kcClient, kcClient)

	handle, err := handler.SealSessionHandle(auth.SessionHandleData{
		SessionID:    "session-uuid-not-a-token",
		Subject:      "learner-42",
		Issuer:       "https://auth.nacfson.cloud/realms/platform",
		Audience:     "project-pn",
		Tokens:       map[string]string{"project-pn": "expired-access-token"},
		RefreshToken: "valid-refresh-token",
		CreatedAt:    time.Now().Unix(),
	})
	if err != nil {
		t.Fatalf("SealSessionHandle failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/auth", nil)
	req.Header.Set("X-Forwarded-Host", "pn.nacfson.cloud")
	req.Header.Set("Authorization", "Bearer "+handle)
	rr := httptest.NewRecorder()
	handler.HandleForwardAuth(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 OK after transparent refresh, got %d: %s", rr.Code, rr.Body.String())
	}
	if len(introspectedTokens) == 0 || introspectedTokens[0] != "expired-access-token" {
		t.Fatalf("expected Keycloak introspection to receive the OAuth access token (not session UUID), got %v", introspectedTokens)
	}
	if rr.Header().Get("Authorization") != "Bearer fresh-access-token" {
		t.Fatalf("expected refreshed access token in Authorization header, got %q", rr.Header().Get("Authorization"))
	}
	if rr.Header().Get("X-Refreshed-Session-Handle") == "" {
		t.Fatal("expected X-Refreshed-Session-Handle header when token was rotated")
	}
}

func TestJWKSProxyAllowedPaths(t *testing.T) {
	mockKC := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/realms/platform/protocol/openid-connect/certs" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"keys":[{"kty":"RSA","kid":"1"}]}`))
			return
		}
		if r.URL.Path == "/realms/platform/.well-known/openid-configuration" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"issuer":"https://auth.example.com"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer mockKC.Close()

	cfg := &config.Config{
		KeycloakIssuerURL: mockKC.URL + "/realms/platform",
	}
	client := keycloak.NewClient(cfg.KeycloakIssuerURL, mockKC.URL, "client", "secret")
	handler := auth.NewHandler(cfg, nil, client)

	// Test certs proxy
	req := httptest.NewRequest("GET", "/realms/platform/protocol/openid-connect/certs", nil)
	rr := httptest.NewRecorder()
	handler.HandleJWKSProxy(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for certs proxy, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"keys"`) {
		t.Fatalf("expected keys in body, got %s", rr.Body.String())
	}

	// Test discovery proxy
	req2 := httptest.NewRequest("GET", "/realms/platform/.well-known/openid-configuration", nil)
	rr2 := httptest.NewRecorder()
	handler.HandleJWKSProxy(rr2, req2)

	if rr2.Code != http.StatusOK {
		t.Fatalf("expected 200 for discovery proxy, got %d", rr2.Code)
	}
	if !strings.Contains(rr2.Body.String(), `"issuer"`) {
		t.Fatalf("expected issuer in body, got %s", rr2.Body.String())
	}
}

func TestJWKSProxyForbiddenPaths(t *testing.T) {
	cfg := &config.Config{
		KeycloakIssuerURL: "http://localhost:9999/realms/platform",
	}
	handler := auth.NewHandler(cfg, nil, nil)

	forbidden := []string{
		"/realms/platform/users",
		"/realms/platform/admin",
		"/admin",
		"/realms/master",
	}

	for _, p := range forbidden {
		req := httptest.NewRequest("GET", p, nil)
		rr := httptest.NewRecorder()
		handler.HandleJWKSProxy(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Errorf("expected 403 Forbidden for path %s, got %d", p, rr.Code)
		}
	}

	// Test POST method not allowed
	postReq := httptest.NewRequest("POST", "/realms/platform/protocol/openid-connect/certs", nil)
	rrPost := httptest.NewRecorder()
	handler.HandleJWKSProxy(rrPost, postReq)
	if rrPost.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 Method Not Allowed for POST, got %d", rrPost.Code)
	}
}

