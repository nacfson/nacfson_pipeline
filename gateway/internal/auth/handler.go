package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"nacfson_pipeline/gateway/internal/config"
	"nacfson_pipeline/gateway/internal/keycloak"
)

// StatePayload carries state information across OIDC redirection.
type StatePayload struct {
	TargetURL      string `json:"target_url"`
	Nonce          string `json:"nonce"`
	Timestamp      int64  `json:"ts"`
	VerifierDigest string `json:"vd"`
}

// SessionCookieData stores authenticated session state in the encrypted/signed cookie.
type SessionCookieData struct {
	SessionID    string            `json:"sid"`
	Subject      string            `json:"sub"`
	Issuer       string            `json:"iss"`
	Tokens       map[string]string `json:"tokens"` // audience -> project access token
	RefreshToken string            `json:"rt"`
	CreatedAt    int64             `json:"iat"`
}

// Handler handles Traefik ForwardAuth and OIDC callbacks.
type Handler struct {
	Config    *config.Config
	Validator keycloak.SessionValidator
	Client    *keycloak.Client
	Revoker   keycloak.SessionTerminator
}

// NewHandler creates a new Auth Handler.
func NewHandler(cfg *config.Config, validator keycloak.SessionValidator, client *keycloak.Client) *Handler {
	var revoker keycloak.SessionTerminator
	if client != nil {
		revoker = client
	}
	return &Handler{
		Config:    cfg,
		Validator: validator,
		Client:    client,
		Revoker:   revoker,
	}
}

// SetRevoker overrides the default session revoker.
func (h *Handler) SetRevoker(revoker keycloak.SessionTerminator) {
	h.Revoker = revoker
}

// HandleForwardAuth implements Traefik ForwardAuth protocol on GET /auth.
func (h *Handler) HandleForwardAuth(w http.ResponseWriter, r *http.Request) {
	// 1. Inbound Header Sanitization: Strip client-supplied headers
	r.Header.Del("X-User-Subject")
	r.Header.Del("X-User-Issuer")

	targetHost := r.Header.Get("X-Forwarded-Host")
	if targetHost == "" {
		targetHost = r.Host
	}
	targetProto := r.Header.Get("X-Forwarded-Proto")
	if targetProto == "" {
		targetProto = "https"
	}
	targetURI := r.Header.Get("X-Forwarded-Uri")
	if targetURI == "" {
		targetURI = "/"
	}
	targetURL := fmt.Sprintf("%s://%s%s", targetProto, targetHost, targetURI)

	// 2. Check for PLATFORM_SESSION cookie
	cookie, err := r.Cookie(h.Config.CookieName)
	if err != nil || cookie.Value == "" {
		h.initiateLogin(w, r, targetURL, targetHost)
		return
	}

	session, err := h.decodeSessionCookie(cookie.Value)
	if err != nil {
		h.initiateLogin(w, r, targetURL, targetHost)
		return
	}

	// 3. Online Session Check against Keycloak (Fail-Closed)
	if h.Validator != nil {
		active, err := h.Validator.ValidateSession(session.SessionID)
		if err != nil {
			// Fail-closed on dependency failure
			http.Error(w, `{"error":"service_unavailable","message":"Authentication dependency unavailable"}`, http.StatusServiceUnavailable)
			return
		}
		if !active {
			// Session revoked or expired
			h.clearSessionCookie(w)
			http.Error(w, `{"error":"session_revoked","message":"Session revoked or expired"}`, http.StatusUnauthorized)
			return
		}
	}

	// 4. Determine Project Audience & Inject Token
	projectAudience := deriveProjectAudience(targetHost)
	projectToken := session.Tokens[projectAudience]
	if projectToken == "" {
		// Fallback to primary session token if audience mapping not separately cached
		projectToken = session.Tokens["default"]
	}

	// Success: Inject headers for Traefik to forward upstream
	w.Header().Set("Authorization", "Bearer "+projectToken)
	w.Header().Set("X-User-Subject", session.Subject)
	w.Header().Set("X-User-Issuer", session.Issuer)
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) initiateLogin(w http.ResponseWriter, r *http.Request, targetURL, targetHost string) {
	verifier, challenge, err := keycloak.GeneratePKCE()
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	now := time.Now().Unix()
	vHash := sha256.Sum256([]byte(verifier))
	statePayload := StatePayload{
		TargetURL:      targetURL,
		Nonce:          strconv.FormatInt(now, 10),
		Timestamp:      now,
		VerifierDigest: base64.RawURLEncoding.EncodeToString(vHash[:]),
	}

	stateBytes, _ := json.Marshal(statePayload)
	mac := hmac.New(sha256.New, h.Config.HMACSecret)
	mac.Write(stateBytes)
	sig := mac.Sum(nil)

	signedState := fmt.Sprintf("%s.%s",
		base64.RawURLEncoding.EncodeToString(stateBytes),
		base64.RawURLEncoding.EncodeToString(sig),
	)

	// Save verifier in temporary cookie (5-minute TTL)
	http.SetCookie(w, &http.Cookie{
		Name:     "OIDC_AUTH_STATE",
		Value:    verifier,
		Path:     "/",
		Expires:  time.Now().Add(5 * time.Minute),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})

	projectAudience := deriveProjectAudience(targetHost)
	redirectURI := fmt.Sprintf("https://auth%s/oauth/callback", h.Config.CookieDomain)

	loginURL := fmt.Sprintf("%s/protocol/openid-connect/auth?client_id=%s&response_type=code&scope=openid+profile+email&redirect_uri=%s&state=%s&code_challenge=%s&code_challenge_method=S256",
		h.Config.KeycloakIssuerURL,
		url.QueryEscape(projectAudience),
		url.QueryEscape(redirectURI),
		url.QueryEscape(signedState),
		url.QueryEscape(challenge),
	)

	http.Redirect(w, r, loginURL, http.StatusFound)
}

// HandleCallback processes OIDC authorization code return from Keycloak.
func (h *Handler) HandleCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	stateParam := r.URL.Query().Get("state")
	if code == "" || stateParam == "" {
		http.Error(w, "missing code or state", http.StatusBadRequest)
		return
	}

	stateCookie, err := r.Cookie("OIDC_AUTH_STATE")
	if err != nil || stateCookie.Value == "" {
		http.Error(w, "missing or expired auth state", http.StatusBadRequest)
		return
	}
	verifier := stateCookie.Value

	// Validate signed state
	parts := strings.Split(stateParam, ".")
	if len(parts) != 2 {
		http.Error(w, "invalid state format", http.StatusBadRequest)
		return
	}

	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		http.Error(w, "invalid state payload", http.StatusBadRequest)
		return
	}

	sigBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		http.Error(w, "invalid state signature", http.StatusBadRequest)
		return
	}

	mac := hmac.New(sha256.New, h.Config.HMACSecret)
	mac.Write(payloadBytes)
	expectedSig := mac.Sum(nil)
	if !hmac.Equal(sigBytes, expectedSig) {
		http.Error(w, "tampered state signature", http.StatusBadRequest)
		return
	}

	var state StatePayload
	if err := json.Unmarshal(payloadBytes, &state); err != nil {
		http.Error(w, "corrupt state payload", http.StatusBadRequest)
		return
	}

	// Verify expiration (< 300 seconds)
	if time.Now().Unix()-state.Timestamp > 300 {
		http.Error(w, "state expired", http.StatusBadRequest)
		return
	}

	// Verify code_verifier match
	vHash := sha256.Sum256([]byte(verifier))
	if base64.RawURLEncoding.EncodeToString(vHash[:]) != state.VerifierDigest {
		http.Error(w, "code_verifier mismatch", http.StatusBadRequest)
		return
	}

	// Exchange Code with Keycloak
	redirectURI := fmt.Sprintf("https://auth%s/oauth/callback", h.Config.CookieDomain)
	tokenResp, err := h.Client.ExchangeCode(code, verifier, redirectURI)
	if err != nil {
		http.Error(w, "token exchange error: "+err.Error(), http.StatusBadGateway)
		return
	}

	// Build session data
	sess := SessionCookieData{
		SessionID: tokenResp.SessionState,
		Subject:   parseSubjectFromJWT(tokenResp.AccessToken),
		Issuer:    h.Config.KeycloakIssuerURL,
		Tokens: map[string]string{
			"default":    tokenResp.AccessToken,
			"project-pn": tokenResp.AccessToken,
		},
		RefreshToken: tokenResp.RefreshToken,
		CreatedAt:    time.Now().Unix(),
	}

	cookieVal := h.encodeSessionCookie(sess)

	// Set session cookie
	http.SetCookie(w, &http.Cookie{
		Name:     h.Config.CookieName,
		Value:    cookieVal,
		Path:     "/",
		Domain:   h.Config.CookieDomain,
		Expires:  time.Now().Add(h.Config.SessionTimeout),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})

	// Clear temporary auth cookie
	http.SetCookie(w, &http.Cookie{
		Name:     "OIDC_AUTH_STATE",
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(0, 0),
		HttpOnly: true,
		Secure:   true,
	})

	targetRedirect := state.TargetURL
	if targetRedirect == "" {
		targetRedirect = "/"
	}
	http.Redirect(w, r, targetRedirect, http.StatusFound)
}

func (h *Handler) encodeSessionCookie(data SessionCookieData) string {
	b, _ := json.Marshal(data)
	mac := hmac.New(sha256.New, h.Config.HMACSecret)
	mac.Write(b)
	sig := mac.Sum(nil)
	return fmt.Sprintf("%s.%s",
		base64.RawURLEncoding.EncodeToString(b),
		base64.RawURLEncoding.EncodeToString(sig),
	)
}

func (h *Handler) decodeSessionCookie(raw string) (*SessionCookieData, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 2 {
		return nil, fmt.Errorf("malformed cookie")
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, err
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, err
	}

	mac := hmac.New(sha256.New, h.Config.HMACSecret)
	mac.Write(b)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return nil, fmt.Errorf("invalid cookie signature")
	}

	var data SessionCookieData
	if err := json.Unmarshal(b, &data); err != nil {
		return nil, err
	}
	return &data, nil
}

func (h *Handler) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     h.Config.CookieName,
		Value:    "",
		Path:     "/",
		Domain:   h.Config.CookieDomain,
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}

func deriveProjectAudience(host string) string {
	subdomain := strings.Split(host, ".")[0]
	if subdomain == "pn" {
		return "project-pn"
	}
	return "project-" + subdomain
}

func parseSubjectFromJWT(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return "unknown-subject"
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "unknown-subject"
	}
	var claims struct {
		Sub string `json:"sub"`
	}
	_ = json.Unmarshal(payloadBytes, &claims)
	if claims.Sub != "" {
		return claims.Sub
	}
	return "unknown-subject"
}
