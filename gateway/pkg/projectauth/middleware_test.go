package projectauth_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"nacfson_pipeline/gateway/internal/auth"
	"nacfson_pipeline/gateway/internal/config"
	"nacfson_pipeline/gateway/internal/keycloak"
	"nacfson_pipeline/gateway/pkg/projectauth"
)

type mockGatewayDeps struct {
	mu       sync.Mutex
	sessions map[string]bool
}

func (m *mockGatewayDeps) ValidateSession(sessionID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[sessionID], nil
}

func (m *mockGatewayDeps) ExchangeTokenForProject(refreshToken, targetAudience string) (*keycloak.TokenResponse, error) {
	return nil, nil
}

func (m *mockGatewayDeps) RevokeSession(sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, sessionID)
	return nil
}

func (m *mockGatewayDeps) ExchangeCode(code, codeVerifier, redirectURI string) (*keycloak.TokenResponse, error) {
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"google-user-777"}`))
	return &keycloak.TokenResponse{
		AccessToken:  "hdr." + payload + ".sig",
		RefreshToken: "rt-777",
		SessionState: "kc-sess-777",
	}, nil
}

func TestSharedVerificationModuleFourLayerBFFEndToEnd(t *testing.T) {
	deps := &mockGatewayDeps{
		sessions: map[string]bool{"kc-sess-777": true},
	}

	gwCfg := &config.Config{
		CookieName:         "PLATFORM_SESSION",
		CookieDomain:       ".nacfson.cloud",
		HMACSecret:         []byte("test-secret-key-that-is-at-least-32-bytes!"),
		SessionTimeout:     8 * time.Hour,
		AllowedReturnHosts: []string{"pn.nacfson.cloud", "alpha.nacfson.cloud"},
		KeycloakPublicURL:  "https://auth.nacfson.cloud/realms/platform",
		KeycloakIssuerURL:  "http://keycloak-service.identity.svc:8080/realms/platform",
		ClientID:           "gateway-client",
	}
	gwHandler := auth.NewHandler(gwCfg, deps, nil)
	gwHandler.Exchanger = deps
	gwHandler.SetRevoker(deps)

	gwMux := http.NewServeMux()
	gwMux.HandleFunc("/oauth/start", gwHandler.HandleSessionStart)
	gwMux.HandleFunc("/oauth/callback", gwHandler.HandleCallback)
	gwMux.HandleFunc("/auth/exchange", gwHandler.HandleTicketExchange)
	gwMux.HandleFunc("/auth", gwHandler.HandleForwardAuth)
	gwMux.HandleFunc("/auth/logout", gwHandler.HandleLogout)
	gwServer := httptest.NewServer(gwMux)
	defer gwServer.Close()

	// Create Project PN Verifier and Project Alpha Verifier using the exact same shared module
	pnVerifier, err := projectauth.NewVerifier(projectauth.Config{
		PublicAppURL:       "https://pn.nacfson.cloud",
		PublicAuthURL:      gwServer.URL,
		InternalGatewayURL: gwServer.URL,
		ExpectedAudience:   "project-pn",
		UserResolver: func(ctx context.Context, issuer, subject string) (any, error) {
			return map[string]any{
				"learner_id":           "pn-learner-1",
				"issuer":               issuer,
				"subject":              subject,
				"onboarding_completed": true,
			}, nil
		},
	})
	if err != nil {
		t.Fatalf("NewVerifier(pn) failed: %v", err)
	}

	alphaVerifier, err := projectauth.NewVerifier(projectauth.Config{
		PublicAppURL:       "https://alpha.nacfson.cloud",
		PublicAuthURL:      gwServer.URL,
		InternalGatewayURL: gwServer.URL,
		ExpectedAudience:   "project-alpha",
	})
	if err != nil {
		t.Fatalf("NewVerifier(alpha) failed: %v", err)
	}

	pnMux := http.NewServeMux()
	pnVerifier.RegisterRoutes(pnMux)
	pnMux.Handle("/api/auth/me", pnVerifier.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := projectauth.FromContext(r.Context())
		if !ok {
			http.Error(w, "missing identity", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(id)
	})))

	alphaMux := http.NewServeMux()
	alphaVerifier.RegisterRoutes(alphaMux)
	alphaMux.Handle("/api/auth/me", alphaVerifier.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))

	// Step 1: Project Frontend calls Project Backend GET /api/auth/login
	loginReq := httptest.NewRequest(http.MethodGet, "https://pn.nacfson.cloud/api/auth/login?next=/dashboard", nil)
	loginRR := httptest.NewRecorder()
	pnMux.ServeHTTP(loginRR, loginReq)
	if loginRR.Code != http.StatusFound {
		t.Fatalf("expected 302 from /api/auth/login, got %d", loginRR.Code)
	}
	loginCookies := loginRR.Result().Cookies()
	if len(loginCookies) != 1 || loginCookies[0].Name != "__Host-pa_nonce" {
		t.Fatalf("expected __Host-pa_nonce cookie from /api/auth/login, got %+v", loginCookies)
	}

	// Step 2: Follow redirect to Gateway /oauth/start -> verify kc_idp_hint=google and encrypted state
	startLoc, _ := loginRR.Result().Location()
	startReq := httptest.NewRequest(http.MethodGet, startLoc.RequestURI(), nil)
	startRR := httptest.NewRecorder()
	gwMux.ServeHTTP(startRR, startReq)
	if startRR.Code != http.StatusFound {
		t.Fatalf("expected 302 from Gateway /oauth/start, got %d", startRR.Code)
	}
	kcLoc, _ := startRR.Result().Location()
	if kcLoc.Query().Get("kc_idp_hint") != "google" {
		t.Fatalf("expected kc_idp_hint=google, got %q", kcLoc.Query().Get("kc_idp_hint"))
	}
	stateParam := kcLoc.Query().Get("state")
	gwStartCookies := startRR.Result().Cookies()

	// Step 3: Google + Keycloak returns to Gateway /oauth/callback with host-only OIDC_AUTH_STATE binding cookie
	cbGwReq := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=kc-code-1&state="+stateParam, nil)
	for _, c := range gwStartCookies {
		cbGwReq.AddCookie(c)
	}
	cbGwRR := httptest.NewRecorder()
	gwMux.ServeHTTP(cbGwRR, cbGwReq)
	if cbGwRR.Code != http.StatusFound {
		t.Fatalf("expected 302 from Gateway /oauth/callback, got %d: %s", cbGwRR.Code, cbGwRR.Body.String())
	}

	// Step 4: Follow redirect to Project Backend /api/auth/callback?ticket=...&pa_bind=...
	beCbLoc, _ := cbGwRR.Result().Location()
	// Verify that an unbound Login CSRF request without __Host-pa_nonce is rejected with 400
	csrfBeReq := httptest.NewRequest(http.MethodGet, beCbLoc.String(), nil)
	csrfBeRR := httptest.NewRecorder()
	pnMux.ServeHTTP(csrfBeRR, csrfBeReq)
	if csrfBeRR.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request on unbound callback without __Host-pa_nonce, got %d", csrfBeRR.Code)
	}

	beCbReq := httptest.NewRequest(http.MethodGet, beCbLoc.String(), nil)
	for _, c := range loginCookies {
		beCbReq.AddCookie(c)
	}
	beCbRR := httptest.NewRecorder()
	pnMux.ServeHTTP(beCbRR, beCbReq)
	if beCbRR.Code != http.StatusFound {
		t.Fatalf("expected 302 from Project Backend /api/auth/callback, got %d: %s", beCbRR.Code, beCbRR.Body.String())
	}
	for _, c := range beCbRR.Result().Cookies() {
		if c.Name == "PLATFORM_SESSION" {
			t.Fatalf("expected zero PLATFORM_SESSION cookies on Project Backend callback, got %+v", c)
		}
	}

	// Step 5: Project Frontend exchanges one-time pickup code via POST /api/auth/session for JSON { session_handle, user }
	feLoc, _ := beCbRR.Result().Location()
	pickupCode := feLoc.Query().Get("code")
	if pickupCode == "" {
		t.Fatalf("expected pickup code in frontend redirect %s", feLoc.String())
	}
	sessReq := httptest.NewRequest(http.MethodPost, "https://pn.nacfson.cloud/api/auth/session", strings.NewReader(`{"code":"`+pickupCode+`"}`))
	sessReq.Header.Set("Content-Type", "application/json")
	sessRR := httptest.NewRecorder()
	pnMux.ServeHTTP(sessRR, sessReq)
	if sessRR.Code != http.StatusOK {
		t.Fatalf("expected 200 OK from POST /api/auth/session, got %d: %s", sessRR.Code, sessRR.Body.String())
	}

	var sessPayload struct {
		SessionHandle string         `json:"session_handle"`
		Subject       string         `json:"subject"`
		Issuer        string         `json:"issuer"`
		Audience      string         `json:"audience"`
		User          map[string]any `json:"user"`
	}
	if err := json.Unmarshal(sessRR.Body.Bytes(), &sessPayload); err != nil {
		t.Fatalf("failed to decode /api/auth/session response: %v", err)
	}
	if sessPayload.SessionHandle == "" || sessPayload.Subject != "google-user-777" || sessPayload.User["learner_id"] != "pn-learner-1" {
		t.Fatalf("unexpected session payload: %+v", sessPayload)
	}

	// Replaying the same pickup code MUST be rejected (single-use enforcement)
	replaySessReq := httptest.NewRequest(http.MethodPost, "https://pn.nacfson.cloud/api/auth/session", strings.NewReader(`{"code":"`+pickupCode+`"}`))
	replaySessRR := httptest.NewRecorder()
	pnMux.ServeHTTP(replaySessRR, replaySessReq)
	if replaySessRR.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 on replayed pickup code, got %d", replaySessRR.Code)
	}

	// Step 6: Project Frontend calls GET /api/auth/me with Authorization: Bearer <session_handle> (and forged headers that must be stripped)
	meReq := httptest.NewRequest(http.MethodGet, "https://pn.nacfson.cloud/api/auth/me", nil)
	meReq.Header.Set("Authorization", "Bearer "+sessPayload.SessionHandle)
	meReq.Header.Set("X-User-Subject", "spoofed-attacker")
	meRR := httptest.NewRecorder()
	pnMux.ServeHTTP(meRR, meReq)
	if meRR.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on /api/auth/me, got %d: %s", meRR.Code, meRR.Body.String())
	}
	var verifiedID projectauth.Identity
	_ = json.Unmarshal(meRR.Body.Bytes(), &verifiedID)
	if verifiedID.Subject != "google-user-777" {
		t.Fatalf("expected verified subject 'google-user-777' (spoofed header stripped), got %q", verifiedID.Subject)
	}

	// Step 7: Cross-Project Isolation — presenting Project PN's session_handle to Project Alpha MUST be denied
	crossReq := httptest.NewRequest(http.MethodGet, "https://alpha.nacfson.cloud/api/auth/me", nil)
	crossReq.Header.Set("Authorization", "Bearer "+sessPayload.SessionHandle)
	crossRR := httptest.NewRecorder()
	alphaMux.ServeHTTP(crossRR, crossReq)
	if crossRR.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized when presenting Project PN handle to Project Alpha, got %d", crossRR.Code)
	}

	// Step 8: Online Logout via POST /api/auth/logout immediately invalidates subsequent requests
	logoutReq := httptest.NewRequest(http.MethodPost, "https://pn.nacfson.cloud/api/auth/logout", nil)
	logoutReq.Header.Set("Authorization", "Bearer "+sessPayload.SessionHandle)
	logoutRR := httptest.NewRecorder()
	pnMux.ServeHTTP(logoutRR, logoutReq)
	if logoutRR.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on /api/auth/logout, got %d", logoutRR.Code)
	}

	afterLogoutReq := httptest.NewRequest(http.MethodGet, "https://pn.nacfson.cloud/api/auth/me", nil)
	afterLogoutReq.Header.Set("Authorization", "Bearer "+sessPayload.SessionHandle)
	afterLogoutRR := httptest.NewRecorder()
	pnMux.ServeHTTP(afterLogoutRR, afterLogoutReq)
	if afterLogoutRR.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized immediately after logout, got %d", afterLogoutRR.Code)
	}
}
