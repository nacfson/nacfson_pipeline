package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config represents runtime settings for the Go Authentication Gateway.
type Config struct {
	Port              int
	KeycloakIssuerURL string
	KeycloakAdminURL  string
	CookieDomain      string
	CookieName        string
	HMACSecret        []byte
	ClientID          string
	ClientSecret      string
	SessionTimeout    time.Duration
}

// LoadFromEnv loads and validates configuration from environment variables.
func LoadFromEnv() (*Config, error) {
	portStr := getEnvOrDefault("GATEWAY_PORT", "8080")
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, fmt.Errorf("invalid GATEWAY_PORT: %w", err)
	}

	issuerURL := os.Getenv("KEYCLOAK_ISSUER_URL")
	if issuerURL == "" {
		issuerURL = "http://keycloak.identity.svc:8080/realms/platform"
	}

	adminURL := os.Getenv("KEYCLOAK_ADMIN_URL")
	if adminURL == "" {
		adminURL = "http://keycloak.identity.svc:8080"
	}

	cookieDomain := getEnvOrDefault("COOKIE_DOMAIN", ".example.com")
	cookieName := getEnvOrDefault("COOKIE_NAME", "PLATFORM_SESSION")

	hmacSecretStr := getEnvOrDefault("GATEWAY_HMAC_SECRET", "default-insecure-secret-for-dev-32b!")
	if len(hmacSecretStr) < 32 {
		return nil, fmt.Errorf("GATEWAY_HMAC_SECRET must be at least 32 bytes")
	}

	return &Config{
		Port:              port,
		KeycloakIssuerURL: issuerURL,
		KeycloakAdminURL:  adminURL,
		CookieDomain:      cookieDomain,
		CookieName:        cookieName,
		HMACSecret:        []byte(hmacSecretStr),
		ClientID:          getEnvOrDefault("GATEWAY_CLIENT_ID", "gateway-client"),
		ClientSecret:      os.Getenv("GATEWAY_CLIENT_SECRET"),
		SessionTimeout:    8 * time.Hour,
	}, nil
}

func getEnvOrDefault(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}
