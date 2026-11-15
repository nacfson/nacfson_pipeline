package auth

import (
	"fmt"
)

type SessionValidator interface {
	ValidateSession(sessionID string) (bool, error)
}

func (h *Handler) CheckSessionStatus(sessionID string) (bool, error) {
	if sessionID == "" {
		return false, fmt.Errorf("session ID is empty")
	}
	if h.Validator == nil {
		return true, nil
	}
	return h.Validator.ValidateSession(sessionID)
}
