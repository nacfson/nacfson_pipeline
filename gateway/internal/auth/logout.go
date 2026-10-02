package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"strings"
)

// GenerateCSRFToken generates a deterministic HMAC CSRF token bound to the session ID.
func (h *Handler) GenerateCSRFToken(sessionID string) string {
	mac := hmac.New(sha256.New, h.Config.HMACSecret)
	mac.Write([]byte("csrf:" + sessionID))
	return hex.EncodeToString(mac.Sum(nil))
}

// ValidateCSRF verifies that the request originates from an authorized project domain
// or carries a valid cryptographic CSRF token.
func (h *Handler) ValidateCSRF(r *http.Request, sessionID string) bool {
	// Check 1: X-CSRF-Token header
	csrfHeader := r.Header.Get("X-CSRF-Token")
	if csrfHeader != "" {
		expected := h.GenerateCSRFToken(sessionID)
		if hmac.Equal([]byte(csrfHeader), []byte(expected)) {
			return true
		}
	}

	// Check 2: Origin or Referer domain validation
	origin := r.Header.Get("Origin")
	if origin == "" {
		origin = r.Header.Get("Referer")
	}
	if origin != "" {
		u, err := url.Parse(origin)
		if err == nil && u.Hostname() != "" {
			hostname := u.Hostname()
			domain := strings.TrimPrefix(h.Config.CookieDomain, ".")
			if hostname == domain || strings.HasSuffix(hostname, "."+domain) || hostname == "localhost" || hostname == "127.0.0.1" {
				return true
			}
		}
	}

	return false
}

// HandleLogout processes user-initiated session termination on POST /auth/logout.
// Strictly adheres to specs/001-project-platform/contracts/session-revocation.md
func (h *Handler) HandleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	// 1. Authenticated Request Precondition
	cookie, err := r.Cookie(h.Config.CookieName)
	if err != nil || cookie.Value == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"unauthenticated","message":"No active session found"}`))
		return
	}

	session, err := h.decodeSessionCookie(cookie.Value)
	if err != nil {
		h.clearSessionCookie(w)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"invalid_session","message":"Malformed session"}`))
		return
	}

	// 2. CSRF Protection Precondition
	if !h.ValidateCSRF(r, session.SessionID) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error": "invalid_csrf_token", "message": "Cross-site request forgery validation failed."}`))
		return
	}

	// 3. Synchronous Backend Keycloak Session Invalidation
	if h.Revoker != nil && session.SessionID != "" {
		_ = h.Revoker.RevokeSession(session.SessionID)
	} else if h.Client != nil && session.SessionID != "" {
		_ = h.Client.RevokeSession(session.SessionID)
	}

	// 4. Synchronously clear the platform session cookie
	h.clearSessionCookie(w)

	// 5. Response Interface: Redirect (HTTP 302) or JSON confirmation (HTTP 200)
	redirectURI := r.FormValue("redirect_uri")
	if redirectURI == "" {
		redirectURI = r.URL.Query().Get("redirect_uri")
	}

	if redirectURI != "" {
		http.Redirect(w, r, redirectURI, http.StatusFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"revoked","message":"Platform session successfully terminated"}`))
}
