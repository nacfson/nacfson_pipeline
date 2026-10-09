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

// CodeExchanger exchanges an authorization code for tokens.
type CodeExchanger interface {
	ExchangeCode(code, codeVerifier, redirectURI string) (*keycloak.TokenResponse, error)
}

// Handler handles session confirmation and OIDC return.
type Handler struct {
	Config    *config.Config
	Validator keycloak.SessionValidator
	Client    *keycloak.Client
	Revoker   keycloak.SessionTerminator
	Exchanger CodeExchanger
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

// HandleForwardAuth confirms a session when a shared project asks.
// A missing cookie does not redirect. Opening the project host does not require one.
func (h *Handler) HandleForwardAuth(w http.ResponseWriter, r *http.Request) {
	// 1. Inbound Header Sanitization: Strip client-supplied identity headers
	r.Header.Del("X-User-Subject")
	r.Header.Del("X-User-Issuer")
	r.Header.Del("Authorization")

	targetHost := r.Header.Get("X-Forwarded-Host")
	if targetHost == "" {
		targetHost = r.Host
	}

	// 2. A missing or unreadable session leaves the visitor unsigned-in.
	cookie, err := r.Cookie(h.Config.CookieName)
	if err != nil || cookie.Value == "" {
		http.Error(w, `{"error":"unsigned_in","message":"No confirmed session"}`, http.StatusUnauthorized)
		return
	}

	session, err := h.decodeSessionCookie(cookie.Value)
	if err != nil {
		http.Error(w, `{"error":"unsigned_in","message":"No confirmed session"}`, http.StatusUnauthorized)
		return
	}

	if h.Config.SessionTimeout > 0 && time.Now().Unix()-session.CreatedAt >= int64(h.Config.SessionTimeout.Seconds()) {
		http.Error(w, `{"error":"unsigned_in","message":"Session expired"}`, http.StatusUnauthorized)
		return
	}
	if h.Config.KeycloakIssuerURL != "" && session.Issuer != h.Config.KeycloakIssuerURL {
		http.Error(w, `{"error":"unsigned_in","message":"Session issuer does not match this sign-in service"}`, http.StatusUnauthorized)
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

	// 4. Confirm only a token issued for this project.
	projectAudience := deriveProjectAudience(targetHost)
	projectToken := session.Tokens[projectAudience]
	if projectToken == "" {
		http.Error(w, `{"error":"unsigned_in","message":"Session is not confirmed for this project"}`, http.StatusUnauthorized)
		return
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

	redirectURI := fmt.Sprintf("https://auth%s/oauth/callback", h.Config.CookieDomain)

	loginIssuer := h.Config.KeycloakPublicURL
	if loginIssuer == "" {
		loginIssuer = h.Config.KeycloakIssuerURL
	}

	loginClientID := h.Config.ClientID
	if loginClientID == "" {
		loginClientID = deriveProjectAudience(targetHost)
	}

	loginURL := fmt.Sprintf("%s/protocol/openid-connect/auth?client_id=%s&response_type=code&scope=openid+profile+email&redirect_uri=%s&state=%s&code_challenge=%s&code_challenge_method=S256",
		loginIssuer,
		url.QueryEscape(loginClientID),
		url.QueryEscape(redirectURI),
		url.QueryEscape(signedState),
		url.QueryEscape(challenge),
	)

	http.Redirect(w, r, loginURL, http.StatusFound)
}

// HandleSessionStart begins sign-in for a shared project.
// return must already be an address on that project's door.
func (h *Handler) HandleSessionStart(w http.ResponseWriter, r *http.Request) {
	returnTo := r.URL.Query().Get("return")
	if !h.returnHostAllowed(returnTo) {
		http.Error(w, "return address is not on this project door", http.StatusBadRequest)
		return
	}
	u, err := url.Parse(returnTo)
	if err != nil {
		http.Error(w, "return address is not on this project door", http.StatusBadRequest)
		return
	}
	h.initiateLogin(w, r, returnTo, u.Hostname())
}

func (h *Handler) returnHostAllowed(targetURL string) bool {
	if h.Config == nil {
		return false
	}
	u, err := url.Parse(targetURL)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Scheme != "https" {
		return false
	}
	host := u.Hostname()
	for _, allowed := range h.Config.AllowedReturnHosts {
		if strings.EqualFold(host, strings.TrimSpace(allowed)) {
			return true
		}
	}
	return false
}

// ProjectHostSessionCookie is the session cookie scoped to one project host.
func ProjectHostSessionCookie(name, value, host string, expires time.Time) *http.Cookie {
	return &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		Domain:   host,
		Expires:  expires,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	}
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
		valid := false
		if len(h.Config.HMACSecretPrevious) > 0 {
			prevMac := hmac.New(sha256.New, h.Config.HMACSecretPrevious)
			prevMac.Write(payloadBytes)
			if hmac.Equal(sigBytes, prevMac.Sum(nil)) {
				valid = true
			}
		}
		if !valid {
			http.Error(w, "tampered state signature", http.StatusBadRequest)
			return
		}
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

	if !h.returnHostAllowed(state.TargetURL) {
		http.Error(w, "return address is not on this project door", http.StatusBadRequest)
		return
	}
	returnURL, err := url.Parse(state.TargetURL)
	if err != nil {
		http.Error(w, "return address is not on this project door", http.StatusBadRequest)
		return
	}

	// Exchange Code with Keycloak. Unreachable sign-in does not confirm the user.
	redirectURI := fmt.Sprintf("https://auth%s/oauth/callback", h.Config.CookieDomain)
	var tokenResp *keycloak.TokenResponse
	if h.Exchanger != nil {
		tokenResp, err = h.Exchanger.ExchangeCode(code, verifier, redirectURI)
	} else if h.Client != nil {
		tokenResp, err = h.Client.ExchangeCode(code, verifier, redirectURI)
	} else {
		http.Error(w, `{"error":"unsigned_in","message":"Sign-in service unreachable"}`, http.StatusServiceUnavailable)
		return
	}
	if err != nil {
		http.Error(w, `{"error":"unsigned_in","message":"Sign-in service unreachable"}`, http.StatusServiceUnavailable)
		return
	}

	sessionIssuer := h.Config.KeycloakPublicURL
	if sessionIssuer == "" {
		sessionIssuer = h.Config.KeycloakIssuerURL
	}

	// Build session data for the project that owns the return address.
	audience := deriveProjectAudience(returnURL.Hostname())
	sess := SessionCookieData{
		SessionID: tokenResp.SessionState,
		Subject:   parseSubjectFromJWT(tokenResp.AccessToken),
		Issuer:    sessionIssuer,
		Tokens: map[string]string{
			audience: tokenResp.AccessToken,
		},
		RefreshToken: tokenResp.RefreshToken,
		CreatedAt:    time.Now().Unix(),
	}

	cookieVal := h.encodeSessionCookie(sess)

	// Cookie is scoped to the project host, not the platform parent domain.
	http.SetCookie(w, ProjectHostSessionCookie(
		h.Config.CookieName,
		cookieVal,
		returnURL.Hostname(),
		time.Now().Add(h.Config.SessionTimeout),
	))

	// Clear temporary auth cookie
	http.SetCookie(w, &http.Cookie{
		Name:     "OIDC_AUTH_STATE",
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(0, 0),
		HttpOnly: true,
		Secure:   true,
	})

	http.Redirect(w, r, state.TargetURL, http.StatusFound)
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
		valid := false
		if len(h.Config.HMACSecretPrevious) > 0 {
			prevMac := hmac.New(sha256.New, h.Config.HMACSecretPrevious)
			prevMac.Write(b)
			if hmac.Equal(sig, prevMac.Sum(nil)) {
				valid = true
			}
		}
		if !valid {
			return nil, fmt.Errorf("invalid cookie signature")
		}
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
