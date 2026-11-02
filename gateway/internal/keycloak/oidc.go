package keycloak

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// TokenResponse represents OAuth2/OIDC token response from Keycloak.
type TokenResponse struct {
	AccessToken      string `json:"access_token"`
	ExpiresIn        int    `json:"expires_in"`
	RefreshExpiresIn int    `json:"refresh_expires_in"`
	RefreshToken     string `json:"refresh_token"`
	TokenType        string `json:"token_type"`
	IDToken          string `json:"id_token"`
	SessionState     string `json:"session_state"`
	Scope            string `json:"scope"`
}

// SessionValidator defines interface for checking session status against Keycloak.
type SessionValidator interface {
	ValidateSession(sessionID string) (bool, error)
	ExchangeTokenForProject(refreshToken, targetAudience string) (*TokenResponse, error)
}

// Client implements Keycloak OIDC interactions using standard library only.
type Client struct {
	IssuerURL    string
	AdminURL     string
	ClientID     string
	ClientSecret string
	HTTPClient   *http.Client
}

// NewClient creates a new Keycloak client.
func NewClient(issuerURL, adminURL, clientID, clientSecret string) *Client {
	return &Client{
		IssuerURL:    strings.TrimSuffix(issuerURL, "/"),
		AdminURL:     strings.TrimSuffix(adminURL, "/"),
		ClientID:     clientID,
		ClientSecret: clientSecret,
		HTTPClient: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

// GeneratePKCE creates a cryptographic code_verifier and code_challenge.
func GeneratePKCE() (verifier string, challenge string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	verifier = base64.RawURLEncoding.EncodeToString(b)
	h := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(h[:])
	return verifier, challenge, nil
}

// ExchangeCode exchanges an authorization code with Keycloak for tokens using PKCE verifier.
func (c *Client) ExchangeCode(code, codeVerifier, redirectURI string) (*TokenResponse, error) {
	tokenURL := fmt.Sprintf("%s/protocol/openid-connect/token", c.IssuerURL)

	data := url.Values{}
	data.Set("grant_type", "authorization_code")
	data.Set("client_id", c.ClientID)
	if c.ClientSecret != "" {
		data.Set("client_secret", c.ClientSecret)
	}
	data.Set("code", code)
	data.Set("redirect_uri", redirectURI)
	data.Set("code_verifier", codeVerifier)

	req, err := http.NewRequest("POST", tokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token exchange failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token endpoint returned status %d", resp.StatusCode)
	}

	var tr TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return nil, err
	}
	return &tr, nil
}

// ValidateSession verifies whether an OAuth token or user session remains active in Keycloak.
func (c *Client) ValidateSession(tokenOrSessionID string) (bool, error) {
	if tokenOrSessionID == "" {
		return false, nil
	}
	// Query Keycloak realm OIDC token introspection endpoint
	checkURL := fmt.Sprintf("%s/realms/platform/protocol/openid-connect/token/introspect", c.AdminURL)
	data := url.Values{}
	data.Set("client_id", c.ClientID)
	if c.ClientSecret != "" {
		data.Set("client_secret", c.ClientSecret)
	}
	data.Set("token", tokenOrSessionID)

	req, err := http.NewRequest("POST", checkURL, strings.NewReader(data.Encode()))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("session validation network error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, nil
	}

	var res struct {
		Active bool `json:"active"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return false, err
	}
	return res.Active, nil
}

// AccessTokenRefreshLeeway is how close to its exp claim an access token may be before the gateway
// refreshes it instead of handing it to a project.
//
// Keycloak introspection reports a token active while now <= exp. A project that checks now >= exp,
// or that reads the token a moment after the gateway confirmed it, rejects a token the gateway just
// confirmed. That race signed visitors out mid-use once per access-token lifetime.
const AccessTokenRefreshLeeway = 30 * time.Second

// ValidateAndRefresh verifies the OAuth access token against Keycloak introspection and, if expired,
// attempts to transparently refresh it using the session's refresh token while the Keycloak session remains active.
// A token within AccessTokenRefreshLeeway of expiry is refreshed first, so a refreshed token has at least
// that much life left when the project receives it. If that early refresh fails (for example Keycloak
// is briefly unreachable), the current token is introspected once and, if still active, confirmed as-is;
// that fallback keeps the pre-leeway behavior rather than signing the visitor out on a transient error.
func (c *Client) ValidateAndRefresh(sessionID, accessToken, refreshToken, targetAudience string) (bool, *TokenResponse, error) {
	triedRefresh := false
	if refreshToken != "" && accessTokenExpiresWithin(accessToken, AccessTokenRefreshLeeway, time.Now()) {
		triedRefresh = true
		refreshed, refreshErr := c.ExchangeTokenForProject(refreshToken, targetAudience)
		if refreshErr == nil && refreshed != nil && refreshed.AccessToken != "" {
			return true, refreshed, nil
		}
		// Fall through: a still-valid token stays usable when the early refresh fails.
	}

	tokenToCheck := accessToken
	if tokenToCheck == "" {
		tokenToCheck = sessionID
	}
	active, err := c.ValidateSession(tokenToCheck)
	if err != nil {
		return false, nil, err
	}
	if active {
		return true, nil, nil
	}
	if refreshToken == "" || triedRefresh {
		return false, nil, nil
	}
	refreshed, refreshErr := c.ExchangeTokenForProject(refreshToken, targetAudience)
	if refreshErr != nil || refreshed == nil || refreshed.AccessToken == "" {
		return false, nil, nil
	}
	return true, refreshed, nil
}

// accessTokenExpiresWithin reports whether the JWT's exp claim falls within leeway of now.
// The token comes from the gateway's own sealed session handle, so its payload is read without
// verifying the signature. A token without a readable exp claim returns false and keeps the
// introspection-first path.
func accessTokenExpiresWithin(token string, leeway time.Duration, now time.Time) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return false
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp <= 0 {
		return false
	}
	return time.Unix(claims.Exp, 0).Sub(now) < leeway
}

// ExchangeTokenForProject requests an audience-scoped project access token using refresh flow.
func (c *Client) ExchangeTokenForProject(refreshToken, targetAudience string) (*TokenResponse, error) {
	tokenURL := fmt.Sprintf("%s/protocol/openid-connect/token", c.IssuerURL)

	data := url.Values{}
	data.Set("grant_type", "refresh_token")
	data.Set("client_id", c.ClientID)
	if c.ClientSecret != "" {
		data.Set("client_secret", c.ClientSecret)
	}
	data.Set("refresh_token", refreshToken)
	data.Set("audience", targetAudience)

	req, err := http.NewRequest("POST", tokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("project token exchange failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token endpoint returned status %d", resp.StatusCode)
	}

	var tr TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return nil, err
	}
	return &tr, nil
}
