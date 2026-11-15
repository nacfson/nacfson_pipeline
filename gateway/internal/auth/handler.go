package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"nacfson_pipeline/gateway/internal/config"
	"nacfson_pipeline/gateway/internal/keycloak"
)

type StatePayload struct {
	TargetURL      string `json:"target_url"`
	Nonce          string `json:"nonce"`
	Timestamp      int64  `json:"ts"`
	VerifierDigest string `json:"vd,omitempty"`
}

type SessionCookieData struct {
	SessionID    string            `json:"sid"`
	Subject      string            `json:"sub"`
	Issuer       string            `json:"iss"`
	Audience     string            `json:"aud,omitempty"`
	Tokens       map[string]string `json:"tokens"`
	RefreshToken string            `json:"rt"`
	CreatedAt    int64             `json:"iat"`
}

type SessionHandleData = SessionCookieData

type CodeExchanger interface {
	ExchangeCode(code, codeVerifier, redirectURI string) (*keycloak.TokenResponse, error)
}

type TokenRefreshValidator interface {
	ValidateAndRefresh(sessionID, accessToken, refreshToken, targetAudience string) (bool, *keycloak.TokenResponse, error)
}

type Handler struct {
	Config         *config.Config
	Validator      keycloak.SessionValidator
	Client         *keycloak.Client
	Revoker        keycloak.SessionTerminator
	Exchanger      CodeExchanger
	ticketInitOnce sync.Once
	tickets        *TicketStore
}

func NewHandler(cfg *config.Config, validator keycloak.SessionValidator, client *keycloak.Client) *Handler {
	return &Handler{
		Config:    cfg,
		Validator: validator,
		Client:    client,
		tickets:   newTicketStore(),
	}
}

func (h *Handler) SetRevoker(revoker keycloak.SessionTerminator) {
	h.Revoker = revoker
}

func (h *Handler) SealSessionHandle(data SessionHandleData) (string, error) {
	return sealJSON(data, h.Config.HMACSecret)
}

func (h *Handler) UnsealSessionHandle(raw string) (*SessionHandleData, error) {
	if strings.HasPrefix(raw, sealedPrefix) {
		var data SessionHandleData
		if err := unsealJSON(raw, h.Config.HMACSecret, h.Config.HMACSecretPrevious, &data); err != nil {
			return nil, err
		}
		return &data, nil
	}
	return h.decodeSessionCookie(raw)
}

func (h *Handler) HandleForwardAuth(w http.ResponseWriter, r *http.Request) {

	rawAuthHeader := strings.TrimSpace(r.Header.Get("Authorization"))
	var bearerCandidate string
	if strings.HasPrefix(strings.ToLower(rawAuthHeader), "bearer ") {
		bearerCandidate = strings.TrimSpace(rawAuthHeader[7:])
	}

	r.Header.Del("X-User-Subject")
	r.Header.Del("X-User-Issuer")
	r.Header.Del("Authorization")

	targetHost := r.Header.Get("X-Forwarded-Host")
	if targetHost == "" {
		targetHost = r.Host
	}

	var session *SessionHandleData
	if bearerCandidate != "" {
		if s, err := h.UnsealSessionHandle(bearerCandidate); err == nil {
			session = s
		}
	}
	if session == nil && h.Config != nil && h.Config.CookieName != "" {
		if cookie, err := r.Cookie(h.Config.CookieName); err == nil && cookie.Value != "" {
			if s, err := h.UnsealSessionHandle(cookie.Value); err == nil {
				session = s
			}
		}
	}

	if session == nil {
		http.Error(w, `{"error":"unsigned_in","message":"No confirmed session"}`, http.StatusUnauthorized)
		return
	}

	if h.Config.SessionTimeout > 0 && time.Now().Unix()-session.CreatedAt >= int64(h.Config.SessionTimeout.Seconds()) {
		http.Error(w, `{"error":"unsigned_in","message":"Session expired"}`, http.StatusUnauthorized)
		return
	}
	if !h.Config.IsAllowedIssuer(session.Issuer) {
		http.Error(w, `{"error":"unsigned_in","message":"Session issuer does not match this sign-in service"}`, http.StatusUnauthorized)
		return
	}

	projectAudience := deriveProjectAudience(targetHost)
	if session.Audience != "" && session.Audience != projectAudience {
		http.Error(w, `{"error":"unsigned_in","message":"Session is not confirmed for this project"}`, http.StatusUnauthorized)
		return
	}
	projectToken := ""
	if session.Tokens != nil {
		projectToken = session.Tokens[projectAudience]
	}

	var refreshedSessionHandle string
	if h.Validator != nil {
		var active bool
		var err error
		if trv, ok := h.Validator.(TokenRefreshValidator); ok {
			var refreshed *keycloak.TokenResponse
			active, refreshed, err = trv.ValidateAndRefresh(session.SessionID, projectToken, session.RefreshToken, projectAudience)
			if err == nil && active && refreshed != nil && refreshed.AccessToken != "" {
				if session.Tokens == nil {
					session.Tokens = make(map[string]string)
				}
				session.Tokens[projectAudience] = refreshed.AccessToken
				projectToken = refreshed.AccessToken
				if refreshed.RefreshToken != "" {
					session.RefreshToken = refreshed.RefreshToken
				}
				if sealed, sealErr := h.SealSessionHandle(*session); sealErr == nil {
					refreshedSessionHandle = sealed
				}
			}
		} else {
			active, err = h.Validator.ValidateSession(session.SessionID)
			if err == nil && !active && session.RefreshToken != "" {
				if refreshed, refreshErr := h.Validator.ExchangeTokenForProject(session.RefreshToken, projectAudience); refreshErr == nil && refreshed != nil && refreshed.AccessToken != "" {
					active = true
					if session.Tokens == nil {
						session.Tokens = make(map[string]string)
					}
					session.Tokens[projectAudience] = refreshed.AccessToken
					projectToken = refreshed.AccessToken
					if refreshed.RefreshToken != "" {
						session.RefreshToken = refreshed.RefreshToken
					}
					if sealed, sealErr := h.SealSessionHandle(*session); sealErr == nil {
						refreshedSessionHandle = sealed
					}
				}
			}
		}

		if err != nil {
			http.Error(w, `{"error":"service_unavailable","message":"Authentication dependency unavailable"}`, http.StatusServiceUnavailable)
			return
		}
		if !active {
			h.clearSessionCookie(w)
			http.Error(w, `{"error":"session_revoked","message":"Session revoked or expired"}`, http.StatusUnauthorized)
			return
		}
	}

	if projectToken == "" {
		http.Error(w, `{"error":"unsigned_in","message":"Session is not confirmed for this project"}`, http.StatusUnauthorized)
		return
	}

	w.Header().Set("Authorization", "Bearer "+projectToken)
	w.Header().Set("X-User-Subject", session.Subject)
	w.Header().Set("X-User-Issuer", session.Issuer)
	w.Header().Set("X-Project-Audience", projectAudience)
	if email := parseEmailFromJWT(projectToken); email != "" {
		w.Header().Set("X-User-Email", email)
	}
	if refreshedSessionHandle != "" {
		w.Header().Set("X-Refreshed-Session-Handle", refreshedSessionHandle)
	}
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) initiateLogin(w http.ResponseWriter, r *http.Request, targetURL, targetHost string) {
	verifier, challenge, err := keycloak.GeneratePKCE()
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	now := time.Now().Unix()
	nonce, err := generateRandomID()
	if err != nil {
		nonce = strconv.FormatInt(now, 10)
	}
	vHash := sha256.Sum256([]byte(verifier))
	statePayload := StatePayload{
		TargetURL:      targetURL,
		Nonce:          nonce,
		Timestamp:      now,
		VerifierDigest: base64.RawURLEncoding.EncodeToString(vHash[:]),
	}

	signedState, err := sealJSON(statePayload, h.Config.HMACSecret)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "OIDC_AUTH_STATE",
		Value:    verifier,
		Path:     "/oauth/callback",
		MaxAge:   maxStateAgeSecs,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})

	redirectURI := fmt.Sprintf("https://auth%s/oauth/callback", h.Config.CookieDomain)
	loginIssuer := h.Config.CanonicalIssuer()
	loginClientID := h.Config.ClientID
	if loginClientID == "" {
		loginClientID = deriveProjectAudience(targetHost)
	}

	loginURL := fmt.Sprintf("%s/protocol/openid-connect/auth?client_id=%s&response_type=code&scope=openid+profile+email&redirect_uri=%s&state=%s&code_challenge=%s&code_challenge_method=S256&kc_idp_hint=google",
		loginIssuer,
		url.QueryEscape(loginClientID),
		url.QueryEscape(redirectURI),
		url.QueryEscape(signedState),
		url.QueryEscape(challenge),
	)

	http.Redirect(w, r, loginURL, http.StatusFound)
}

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

func (h *Handler) unsealState(stateParam string) (*StatePayload, error) {
	if strings.HasPrefix(stateParam, sealedPrefix) {
		var st StatePayload
		if err := unsealJSON(stateParam, h.Config.HMACSecret, h.Config.HMACSecretPrevious, &st); err != nil {
			return nil, err
		}
		return &st, nil
	}

	parts := strings.Split(stateParam, ".")
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid state format")
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("invalid state payload")
	}
	sigBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("invalid state signature")
	}
	if !verifyLegacyHMAC(payloadBytes, sigBytes, h.Config.HMACSecret, h.Config.HMACSecretPrevious) {
		return nil, fmt.Errorf("tampered state signature")
	}
	var st StatePayload
	if err := json.Unmarshal(payloadBytes, &st); err != nil {
		return nil, fmt.Errorf("corrupt state payload")
	}
	return &st, nil
}

func (h *Handler) HandleCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	stateParam := r.URL.Query().Get("state")
	if code == "" || stateParam == "" {
		http.Error(w, "missing code or state", http.StatusBadRequest)
		return
	}

	state, err := h.unsealState(stateParam)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
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

	if time.Now().Unix()-state.Timestamp > maxStateAgeSecs {
		http.Redirect(w, r, "/oauth/start?return="+url.QueryEscape(state.TargetURL), http.StatusFound)
		return
	}

	stateCookie, cookieErr := r.Cookie("OIDC_AUTH_STATE")
	if cookieErr != nil || stateCookie.Value == "" {
		http.Error(w, "missing or invalid auth state cookie", http.StatusBadRequest)
		return
	}
	verifier := stateCookie.Value

	if state.VerifierDigest == "" {
		http.Error(w, "missing verifier digest", http.StatusBadRequest)
		return
	}
	vHash := sha256.Sum256([]byte(verifier))
	actualDigest := base64.RawURLEncoding.EncodeToString(vHash[:])
	if subtle.ConstantTimeCompare([]byte(actualDigest), []byte(state.VerifierDigest)) != 1 {
		http.Error(w, "code_verifier mismatch", http.StatusBadRequest)
		return
	}

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

	http.SetCookie(w, &http.Cookie{
		Name:     "OIDC_AUTH_STATE",
		Value:    "",
		Path:     "/oauth/callback",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})

	sessionIssuer := h.Config.CanonicalIssuer()
	audience := deriveProjectAudience(returnURL.Hostname())
	sess := SessionHandleData{
		SessionID: tokenResp.SessionState,
		Subject:   parseSubjectFromJWT(tokenResp.AccessToken),
		Issuer:    sessionIssuer,
		Audience:  audience,
		Tokens: map[string]string{
			audience: tokenResp.AccessToken,
		},
		RefreshToken: tokenResp.RefreshToken,
		CreatedAt:    time.Now().Unix(),
	}

	sessionHandle, err := h.SealSessionHandle(sess)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	ticket, err := h.IssueTicket(returnURL.Hostname(), sessionHandle, sess.Subject, sess.Issuer, audience)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	q := returnURL.Query()
	q.Set("ticket", ticket)
	returnURL.RawQuery = q.Encode()
	http.Redirect(w, r, returnURL.String(), http.StatusFound)
}

func (h *Handler) HandleTicketExchange(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method_not_allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	var reqBody struct {
		Ticket string `json:"ticket"`
	}
	if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil || strings.TrimSpace(reqBody.Ticket) == "" {
		http.Error(w, `{"error":"bad_request","message":"missing or malformed ticket"}`, http.StatusBadRequest)
		return
	}

	callerHost := r.Header.Get("X-Forwarded-Host")
	if callerHost == "" {
		callerHost = r.Host
	}

	redeemed, err := h.RedeemTicket(strings.TrimSpace(reqBody.Ticket), callerHost)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"unauthorized","message":%q}`, err.Error()), http.StatusUnauthorized)
		return
	}

	expiresIn := int(h.Config.SessionTimeout.Seconds())
	if expiresIn <= 0 {
		expiresIn = 28800
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"session_handle": redeemed.SessionHandle,
		"subject":        redeemed.Subject,
		"issuer":         redeemed.Issuer,
		"audience":       redeemed.Audience,
		"expires_in":     expiresIn,
	})
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

	if !verifyLegacyHMAC(b, sig, h.Config.HMACSecret, h.Config.HMACSecretPrevious) {
		return nil, fmt.Errorf("invalid cookie signature")
	}

	var data SessionCookieData
	if err := json.Unmarshal(b, &data); err != nil {
		return nil, err
	}
	return &data, nil
}

func (h *Handler) clearSessionCookie(w http.ResponseWriter) {
	if h.Config == nil || h.Config.CookieName == "" {
		return
	}
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
	cleanHost := strings.Split(strings.TrimSpace(host), ":")[0]
	subdomain := strings.Split(cleanHost, ".")[0]
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

func parseEmailFromJWT(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return ""
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Email string `json:"email"`
	}
	_ = json.Unmarshal(payloadBytes, &claims)
	return strings.TrimSpace(claims.Email)
}
