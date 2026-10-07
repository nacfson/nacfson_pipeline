package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config represents runtime settings for the Go Authentication Gateway.
type Config struct {
	Port                    int
	KeycloakIssuerURL       string
	KeycloakAdminURL        string
	KeycloakPublicURL       string
	CookieDomain            string
	CookieName              string
	HMACSecret              []byte
	HMACSecretPrevious      []byte
	ClientID                string
	ClientSecret            string
	SessionRevocationSecret string
	SessionTimeout          time.Duration
}

// LoadFromEnv loads and validates configuration from environment variables and ephemeral tmpfs mounts.
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

	publicURL := os.Getenv("KEYCLOAK_PUBLIC_URL")
	if publicURL == "" {
		if cookieDomain != "" && cookieDomain != ".example.com" {
			publicURL = fmt.Sprintf("https://auth%s/realms/platform", cookieDomain)
		} else {
			publicURL = issuerURL
		}
	}

	// 1. Load HMAC Secret (check ephemeral tmpfs first, then environment variable)
	hmacSecretStr := readSecretFileOrEnv("GATEWAY_HMAC_SECRET_FILE", "/var/run/secrets/gateway/hmac", "GATEWAY_HMAC_SECRET", "default-insecure-secret-for-dev-32b!")
	if len(hmacSecretStr) < 32 {
		return nil, fmt.Errorf("GATEWAY_HMAC_SECRET must be at least 32 bytes")
	}

	// 2. Load optional Previous HMAC Secret for zero-downtime rotation grace period
	hmacSecretPrevStr := readSecretFileOrEnv("GATEWAY_HMAC_SECRET_PREVIOUS_FILE", "/var/run/secrets/gateway/hmac_previous", "GATEWAY_HMAC_SECRET_PREVIOUS", "")
	var hmacSecretPrevious []byte
	if len(hmacSecretPrevStr) >= 32 {
		hmacSecretPrevious = []byte(hmacSecretPrevStr)
	}

	// 3. Load Gateway Client Secret (for OIDC authorization code exchange)
	clientSecret := readSecretFileOrEnv("GATEWAY_CLIENT_SECRET_FILE", "/var/run/secrets/gateway/client_secret", "GATEWAY_CLIENT_SECRET", "")

	// 4. Load Session Revocation Client Secret (for Admin API session termination)
	sessionRevocationSecret := readSecretFileOrEnv("SESSION_REVOCATION_CLIENT_SECRET_FILE", "/var/run/secrets/gateway/session_revocation_secret", "SESSION_REVOCATION_CLIENT_SECRET", clientSecret)

	return &Config{
		Port:                    port,
		KeycloakIssuerURL:       issuerURL,
		KeycloakAdminURL:        adminURL,
		KeycloakPublicURL:       publicURL,
		CookieDomain:            cookieDomain,
		CookieName:              cookieName,
		HMACSecret:              []byte(hmacSecretStr),
		HMACSecretPrevious:      hmacSecretPrevious,
		ClientID:                getEnvOrDefault("GATEWAY_CLIENT_ID", "gateway-client"),
		ClientSecret:            clientSecret,
		SessionRevocationSecret: sessionRevocationSecret,
		SessionTimeout:          8 * time.Hour,
	}, nil
}

func readSecretFileOrEnv(fileEnvVar, defaultPath, envVar, fallback string) string {
	// Check custom file path env var
	if path := os.Getenv(fileEnvVar); path != "" {
		if content, err := os.ReadFile(path); err == nil {
			return strings.TrimSpace(string(content))
		}
	}
	// Check standard ephemeral tmpfs path
	if content, err := os.ReadFile(defaultPath); err == nil {
		return strings.TrimSpace(string(content))
	}
	// Fall back to environment variable
	if val := os.Getenv(envVar); val != "" {
		return strings.TrimSpace(val)
	}
	return fallback
}

func getEnvOrDefault(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}
