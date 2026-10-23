package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
		if len(h.Config.HMACSecretPrevious) > 0 {
			prevMac := hmac.New(sha256.New, h.Config.HMACSecretPrevious)
			prevMac.Write([]byte("csrf:" + sessionID))
			prevExpected := hex.EncodeToString(prevMac.Sum(nil))
			if hmac.Equal([]byte(csrfHeader), []byte(prevExpected)) {
				return true
			}
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
// Supports both Authorization: Bearer <session_handle> (4-layer BFF flow) and legacy cookie revocation.
func (h *Handler) HandleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	var session *SessionHandleData

	rawAuth := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(rawAuth), "bearer ") {
		bearerVal := strings.TrimSpace(rawAuth[7:])
		if bearerVal != "" {
			s, err := h.UnsealSessionHandle(bearerVal)
			if err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"error":"invalid_session","message":"Malformed session"}`))
				return
			}
			session = s
		}
	}

	if session == nil {
		// Legacy Cookie Precondition
		cookie, err := r.Cookie(h.Config.CookieName)
		if err != nil || cookie.Value == "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"unauthenticated","message":"No active session found"}`))
			return
		}

		s, err := h.UnsealSessionHandle(cookie.Value)
		if err != nil {
			h.clearSessionCookie(w)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":"invalid_session","message":"Malformed session"}`))
			return
		}
		session = s

		// CSRF Protection Precondition (required when browser cookies are used)
		if !h.ValidateCSRF(r, session.SessionID) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"error": "invalid_csrf_token", "message": "Cross-site request forgery validation failed."}`))
			return
		}
	}

	if session.SessionID == "" {
		writeLogoutError(w, http.StatusBadRequest, "invalid_session", "Malformed session")
		return
	}
	if h.Revoker == nil {
		writeLogoutError(w, http.StatusServiceUnavailable, "revocation_unavailable", "Session revocation client is not configured")
		return
	}
	if err := h.Revoker.RevokeSession(session.SessionID); err != nil {
		writeLogoutError(w, http.StatusBadGateway, "revocation_failed", "Keycloak session deletion failed")
		return
	}

	h.clearSessionCookie(w)

	redirectURI := r.FormValue("redirect_uri")
	if redirectURI == "" {
		redirectURI = r.URL.Query().Get("redirect_uri")
	}
	if redirectURI != "" && h.returnHostAllowed(redirectURI) {
		http.Redirect(w, r, redirectURI, http.StatusFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"revoked","message":"Platform session successfully terminated"}`))
}

func writeLogoutError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error":   code,
		"message": message,
	})
}
