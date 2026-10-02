package keycloak

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// SessionTerminator defines the interface for revoking a user session in Keycloak.
type SessionTerminator interface {
	RevokeSession(sessionID string) error
}

// SessionClient handles administrative session revocation in Keycloak via Admin API.
type SessionClient struct {
	AdminURL     string
	ClientID     string
	ClientSecret string
	HTTPClient   *http.Client
}

// NewSessionClient creates a new SessionClient using dedicated session-revocation credentials.
func NewSessionClient(adminURL, clientID, clientSecret string) *SessionClient {
	return &SessionClient{
		AdminURL:     strings.TrimSuffix(adminURL, "/"),
		ClientID:     clientID,
		ClientSecret: clientSecret,
		HTTPClient: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

// getAdminToken fetches a service account access token using client credentials grant.
func (c *SessionClient) getAdminToken() (string, error) {
	tokenURL := fmt.Sprintf("%s/realms/platform/protocol/openid-connect/token", c.AdminURL)
	data := url.Values{}
	data.Set("grant_type", "client_credentials")
	data.Set("client_id", c.ClientID)
	if c.ClientSecret != "" {
		data.Set("client_secret", c.ClientSecret)
	}

	req, err := http.NewRequest(http.MethodPost, tokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to obtain session revocation token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("revocation token request returned status %d", resp.StatusCode)
	}

	var tr struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return "", fmt.Errorf("failed to parse token response: %w", err)
	}
	return tr.AccessToken, nil
}

// RevokeSession terminates a user session synchronously via Keycloak Admin API.
// DELETE /admin/realms/platform/sessions/{sessionId}
func (c *SessionClient) RevokeSession(sessionID string) error {
	if sessionID == "" {
		return fmt.Errorf("sessionID cannot be empty")
	}

	token, err := c.getAdminToken()
	if err != nil {
		return err
	}

	revokeURL := fmt.Sprintf("%s/admin/realms/platform/sessions/%s", c.AdminURL, url.PathEscape(sessionID))
	req, err := http.NewRequest(http.MethodDelete, revokeURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create delete session request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("session revocation request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("keycloak session deletion returned status %d", resp.StatusCode)
	}
	return nil
}

// RevokeSession on Client delegates or performs session revocation using admin credentials.
func (c *Client) RevokeSession(sessionID string) error {
	if sessionID == "" {
		return fmt.Errorf("sessionID cannot be empty")
	}
	// Direct call if Client is configured with revocation credentials
	sessionClient := NewSessionClient(c.AdminURL, c.ClientID, c.ClientSecret)
	return sessionClient.RevokeSession(sessionID)
}
