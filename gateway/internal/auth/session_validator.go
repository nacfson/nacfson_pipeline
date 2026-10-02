package auth

import (
	"fmt"
)

// SessionValidator defines interface for checking session status against Keycloak.
type SessionValidator interface {
	ValidateSession(sessionID string) (bool, error)
}

// CheckSessionStatus synchronously checks active session state in Keycloak.
// Returns active=true if the session is valid, active=false if revoked or expired.
// Returns an error if Keycloak is unreachable or encounters an operational failure,
// allowing caller to enforce fail-closed behavior (HTTP 503).
func (h *Handler) CheckSessionStatus(sessionID string) (bool, error) {
	if sessionID == "" {
		return false, fmt.Errorf("session ID is empty")
	}
	if h.Validator == nil {
		return true, nil
	}
	return h.Validator.ValidateSession(sessionID)
}
