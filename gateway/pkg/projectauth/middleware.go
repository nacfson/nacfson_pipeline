// Package projectauth provides the reusable, zero-dependency Go standard-library
// Backend-For-Frontend (BFF) authentication handlers and request verification middleware
// for hosted projects integrating with the nacfson_pipeline internal authentication gateway.
package projectauth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type contextKey int

const (
	identityContextKey contextKey = iota + 1
	pickupCodeTTL                 = 30 * time.Second
)

// Identity represents the verified platform identity (issuer, subject, audience) injected into request context.
type Identity struct {
	Issuer        string `json:"issuer"`
	Subject       string `json:"subject"`
	Audience      string `json:"audience"`
	SessionHandle string `json:"session_handle,omitempty"`
}

// FromContext retrieves the verified Identity from the request context.
func FromContext(ctx context.Context) (*Identity, bool) {
	id, ok := ctx.Value(identityContextKey).(*Identity)
	return id, ok
}

// UserResolver is an optional project hook invoked during /api/auth/callback so the project backend
// can provision or load the user's project account and requirements before returning to the frontend.
type UserResolver func(ctx context.Context, issuer, subject string) (any, error)

// PickupEntry stores the prepared authentication session and project user data for 30 seconds
// while the browser redirects from /api/auth/callback back to the Project Frontend UI.
type PickupEntry struct {
	Code          string    `json:"code"`
	SessionHandle string    `json:"session_handle"`
	Issuer        string    `json:"issuer"`
	Subject       string    `json:"subject"`
	Audience      string    `json:"audience"`
	User          any       `json:"user,omitempty"`
	ExpiresAt     time.Time `json:"expires_at"`
}

// Config configures the shared project authentication verifier for a specific project backend.
type Config struct {
	// PublicAppURL is the project's public origin (e.g., "https://pn.nacfson.cloud").
	PublicAppURL string
	// PublicAuthURL is the public OAuth redirect gateway origin (e.g., "https://auth.nacfson.cloud").
	PublicAuthURL string
	// InternalGatewayURL is the internal cluster gateway address (e.g., "http://gateway.identity.svc:8080").
	InternalGatewayURL string
	// ExpectedAudience is the required project audience (e.g., "project-pn").
	ExpectedAudience string
	// HTTPClient is an optional HTTP client (defaults to 5s timeout).
	HTTPClient *http.Client
	// UserResolver is an optional callback to load/provision project user data on login callback.
	UserResolver UserResolver
}

// Verifier implements the 4-layer cookie-free BFF endpoints and protected route middleware.
type Verifier struct {
	cfg        Config
	appHost    string
	httpClient *http.Client
	mu         sync.Mutex
	pickups    map[string]PickupEntry
}

// NewVerifier creates a new shared project authentication Verifier.
func NewVerifier(cfg Config) (*Verifier, error) {
	u, err := url.Parse(strings.TrimSuffix(strings.TrimSpace(cfg.PublicAppURL), "/"))
	if err != nil || u.Hostname() == "" {
		return nil, fmt.Errorf("invalid PublicAppURL: %q", cfg.PublicAppURL)
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	expectedAud := strings.TrimSpace(cfg.ExpectedAudience)
	if expectedAud == "" {
		sub := strings.Split(u.Hostname(), ".")[0]
		expectedAud = "project-" + sub
	}
	cfg.ExpectedAudience = expectedAud
	cfg.PublicAppURL = strings.TrimSuffix(strings.TrimSpace(cfg.PublicAppURL), "/")
	cfg.PublicAuthURL = strings.TrimSuffix(strings.TrimSpace(cfg.PublicAuthURL), "/")
	cfg.InternalGatewayURL = strings.TrimSuffix(strings.TrimSpace(cfg.InternalGatewayURL), "/")

	return &Verifier{
		cfg:        cfg,
		appHost:    u.Hostname(),
		httpClient: client,
		pickups:    make(map[string]PickupEntry),
	}, nil
}

// RegisterRoutes mounts /api/auth/login, /api/auth/callback, /api/auth/session, and /api/auth/logout onto mux.
func (v *Verifier) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/auth/login", v.HandleLogin)
	mux.HandleFunc("/api/auth/callback", v.HandleCallback)
	mux.HandleFunc("/api/auth/session", v.HandleSession)
	mux.HandleFunc("/api/auth/logout", v.HandleLogout)
}

// HandleLogin redirects the browser from the Project Backend (/api/auth/login) to the pipeline's headless /oauth/start endpoint.
func (v *Verifier) HandleLogin(w http.ResponseWriter, r *http.Request) {
	nonce, err := randomHex128()
	if err != nil {
		http.Error(w, `{"error":"internal_error"}`, http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "__Host-pa_nonce",
		Value:    nonce,
		Path:     "/",
		MaxAge:   600,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
	nh := sha256.Sum256([]byte(nonce))
	bindDigest := base64.RawURLEncoding.EncodeToString(nh[:])

	nextPath := sanitizeRelativePath(r.URL.Query().Get("next"))
	q := url.Values{}
	q.Set("pa_bind", bindDigest)
	if nextPath != "/" {
		q.Set("next", nextPath)
	}
	callbackURL := v.cfg.PublicAppURL + "/api/auth/callback?" + q.Encode()
	startURL := v.cfg.PublicAuthURL + "/oauth/start?return=" + url.QueryEscape(callbackURL)
	http.Redirect(w, r, startURL, http.StatusFound)
}

// HandleCallback exchanges the one-time handoff ticket with internal gateway.identity.svc:8080/auth/exchange,
// resolves the project's user data, and redirects back to the Project Frontend UI with a 30-second pickup code.
func (v *Verifier) HandleCallback(w http.ResponseWriter, r *http.Request) {
	ticket := strings.TrimSpace(r.URL.Query().Get("ticket"))
	nextPath := sanitizeRelativePath(r.URL.Query().Get("next"))
	if ticket == "" {
		http.Redirect(w, r, v.cfg.PublicAppURL+nextPath, http.StatusFound)
		return
	}

	// Verify browser login-binding nonce before redeeming the ticket (prevents Login CSRF)
	expectedBind := strings.TrimSpace(r.URL.Query().Get("pa_bind"))
	nonceCookie, cookieErr := r.Cookie("__Host-pa_nonce")
	if cookieErr != nil || nonceCookie.Value == "" || expectedBind == "" {
		http.Error(w, `{"error":"bad_request","message":"missing login binding"}`, http.StatusBadRequest)
		return
	}
	nh := sha256.Sum256([]byte(nonceCookie.Value))
	actualBind := base64.RawURLEncoding.EncodeToString(nh[:])
	if subtle.ConstantTimeCompare([]byte(actualBind), []byte(expectedBind)) != 1 {
		http.Error(w, `{"error":"bad_request","message":"login binding mismatch"}`, http.StatusBadRequest)
		return
	}

	reqBody, _ := json.Marshal(map[string]string{"ticket": ticket})
	exchReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, v.cfg.InternalGatewayURL+"/auth/exchange", bytes.NewReader(reqBody))
	if err != nil {
		http.Redirect(w, r, v.cfg.PublicAppURL+nextPath, http.StatusFound)
		return
	}
	exchReq.Header.Set("Content-Type", "application/json")
	exchReq.Header.Set("X-Forwarded-Host", v.appHost)

	resp, err := v.httpClient.Do(exchReq)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			resp.Body.Close()
		}
		// Graceful fallback to project login UI if ticket was already consumed or expired (FR-004)
		http.Redirect(w, r, v.cfg.PublicAppURL+nextPath, http.StatusFound)
		return
	}
	defer resp.Body.Close()

	// Clear login-binding nonce cookie now that ticket exchange succeeded
	http.SetCookie(w, &http.Cookie{
		Name:     "__Host-pa_nonce",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})

	var exch struct {
		SessionHandle string `json:"session_handle"`
		Subject       string `json:"subject"`
		Issuer        string `json:"issuer"`
		Audience      string `json:"audience"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&exch); err != nil || exch.SessionHandle == "" {
		http.Redirect(w, r, v.cfg.PublicAppURL+nextPath, http.StatusFound)
		return
	}

	if exch.Audience != v.cfg.ExpectedAudience {
		http.Error(w, `{"error":"forbidden_audience","message":"credential audience does not match project"}`, http.StatusForbidden)
		return
	}

	var userPayload any
	if v.cfg.UserResolver != nil {
		resolved, resolveErr := v.cfg.UserResolver(r.Context(), exch.Issuer, exch.Subject)
		if resolveErr != nil {
			http.Error(w, `{"error":"user_resolution_failed"}`, http.StatusInternalServerError)
			return
		}
		userPayload = resolved
	}

	code, err := randomHex128()
	if err != nil {
		http.Error(w, `{"error":"internal_error"}`, http.StatusInternalServerError)
		return
	}

	now := time.Now()
	v.mu.Lock()
	v.evictExpiredPickupsLocked(now)
	v.pickups[code] = PickupEntry{
		Code:          code,
		SessionHandle: exch.SessionHandle,
		Issuer:        exch.Issuer,
		Subject:       exch.Subject,
		Audience:      exch.Audience,
		User:          userPayload,
		ExpiresAt:     now.Add(pickupCodeTTL),
	}
	v.mu.Unlock()

	sep := "?"
	if strings.Contains(nextPath, "?") {
		sep = "&"
	}
	http.Redirect(w, r, v.cfg.PublicAppURL+nextPath+sep+"code="+url.QueryEscape(code), http.StatusFound)
}

// HandleSession exchanges a 30-second one-time pickup code from the Project Frontend for { session_handle, subject, issuer, user } JSON.
func (v *Verifier) HandleSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method_not_allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var body struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, `{"error":"bad_request","message":"missing or invalid pickup code"}`, http.StatusBadRequest)
		return
	}
	code := strings.TrimSpace(body.Code)
	if len(code) != 32 {
		http.Error(w, `{"error":"bad_request","message":"missing or invalid pickup code"}`, http.StatusBadRequest)
		return
	}
	now := time.Now()

	v.mu.Lock()
	v.evictExpiredPickupsLocked(now)
	entry, ok := v.pickups[code]
	if ok {
		// Delete immediately on first use
		delete(v.pickups, code)
	}
	v.mu.Unlock()

	if !ok || entry.ExpiresAt.Before(now) {
		http.Error(w, `{"error":"unauthorized","message":"invalid or expired pickup code"}`, http.StatusUnauthorized)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"session_handle": entry.SessionHandle,
		"subject":        entry.Subject,
		"issuer":         entry.Issuer,
		"audience":       entry.Audience,
		"user":           entry.User,
	})
}

// HandleLogout forwards session revocation to internal gateway.identity.svc:8080/auth/logout.
func (v *Verifier) HandleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method_not_allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	authHdr := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(strings.ToLower(authHdr), "bearer ") {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, v.cfg.InternalGatewayURL+"/auth/logout", nil)
	if err != nil {
		http.Error(w, `{"error":"internal_error"}`, http.StatusInternalServerError)
		return
	}
	req.Header.Set("Authorization", authHdr)

	resp, err := v.httpClient.Do(req)
	if err != nil {
		http.Error(w, `{"error":"service_unavailable"}`, http.StatusServiceUnavailable)
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write([]byte(`{"status":"revoked"}`))
}

// RequireAuth strips spoofed identity headers, verifies Authorization: Bearer <session_handle>
// against internal gateway.identity.svc:8080/auth, enforces project audience isolation, and injects Identity into context.
func (v *Verifier) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 1. Strip any spoofed client headers
		r.Header.Del("X-User-Subject")
		r.Header.Del("X-User-Issuer")

		authHdr := strings.TrimSpace(r.Header.Get("Authorization"))
		if !strings.HasPrefix(strings.ToLower(authHdr), "bearer ") {
			http.Error(w, `{"error":"unauthorized","message":"missing bearer session_handle"}`, http.StatusUnauthorized)
			return
		}
		handle := strings.TrimSpace(authHdr[7:])
		if handle == "" {
			http.Error(w, `{"error":"unauthorized","message":"empty bearer session_handle"}`, http.StatusUnauthorized)
			return
		}

		verifyReq, err := http.NewRequestWithContext(r.Context(), http.MethodGet, v.cfg.InternalGatewayURL+"/auth", nil)
		if err != nil {
			http.Error(w, `{"error":"internal_error"}`, http.StatusInternalServerError)
			return
		}
		verifyReq.Header.Set("Authorization", "Bearer "+handle)
		verifyReq.Header.Set("X-Forwarded-Host", v.appHost)

		resp, err := v.httpClient.Do(verifyReq)
		if err != nil {
			http.Error(w, `{"error":"service_unavailable","message":"authentication gateway unreachable"}`, http.StatusServiceUnavailable)
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			http.Error(w, `{"error":"unauthorized","message":"session rejected by gateway"}`, resp.StatusCode)
			return
		}

		sub := strings.TrimSpace(resp.Header.Get("X-User-Subject"))
		iss := strings.TrimSpace(resp.Header.Get("X-User-Issuer"))
		aud := strings.TrimSpace(resp.Header.Get("X-Project-Audience"))
		if sub == "" || iss == "" {
			http.Error(w, `{"error":"unauthorized","message":"incomplete identity headers"}`, http.StatusUnauthorized)
			return
		}
		if aud != "" && aud != v.cfg.ExpectedAudience {
			http.Error(w, `{"error":"forbidden_audience","message":"session audience mismatch"}`, http.StatusForbidden)
			return
		}

		if refreshed := strings.TrimSpace(resp.Header.Get("X-Refreshed-Session-Handle")); refreshed != "" {
			w.Header().Set("X-Refreshed-Session-Handle", refreshed)
			handle = refreshed
		}

		r.Header.Set("X-User-Subject", sub)
		r.Header.Set("X-User-Issuer", iss)
		ctx := context.WithValue(r.Context(), identityContextKey, &Identity{
			Issuer:        iss,
			Subject:       sub,
			Audience:      v.cfg.ExpectedAudience,
			SessionHandle: handle,
		})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (v *Verifier) evictExpiredPickupsLocked(now time.Time) {
	for k, entry := range v.pickups {
		if entry.ExpiresAt.Before(now) {
			delete(v.pickups, k)
		}
	}
}

func randomHex128() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func sanitizeRelativePath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" || !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") {
		return "/"
	}
	return p
}
