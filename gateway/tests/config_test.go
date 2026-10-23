package tests

import (
	"os"
	"path/filepath"
	"testing"

	"nacfson_pipeline/gateway/internal/config"
)

func TestConfigRevocationSecretIsNotGatewaySecret(t *testing.T) {
	path := filepath.Join(t.TempDir(), "revocation_secret")
	if err := os.WriteFile(path, []byte("rev-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SESSION_REVOCATION_CLIENT_SECRET_FILE", path)
	t.Setenv("GATEWAY_CLIENT_SECRET", "gw-secret")

	cfg, err := config.LoadFromEnv()
	if err != nil {
		t.Fatalf("LoadFromEnv: %v", err)
	}
	if cfg.SessionRevocationSecret != "rev-secret" {
		t.Errorf("SessionRevocationSecret = %q, want rev-secret", cfg.SessionRevocationSecret)
	}
	if cfg.ClientSecret != "gw-secret" {
		t.Errorf("ClientSecret = %q, want gw-secret", cfg.ClientSecret)
	}
}

func TestConfigDoesNotFallBackToGatewaySecret(t *testing.T) {
	if _, err := os.Stat("/var/run/secrets/gateway/session_revocation_secret"); err == nil {
		t.Skip("/var/run/secrets/gateway/session_revocation_secret exists")
	}
	t.Setenv("GATEWAY_CLIENT_SECRET", "gw-secret")
	t.Setenv("SESSION_REVOCATION_CLIENT_SECRET", "")
	t.Setenv("SESSION_REVOCATION_CLIENT_SECRET_FILE", filepath.Join(t.TempDir(), "missing"))

	cfg, err := config.LoadFromEnv()
	if err != nil {
		t.Fatalf("LoadFromEnv: %v", err)
	}
	if cfg.SessionRevocationSecret != "" {
		t.Errorf("SessionRevocationSecret = %q, want empty", cfg.SessionRevocationSecret)
	}
	if cfg.ClientSecret != "gw-secret" {
		t.Errorf("ClientSecret = %q, want gw-secret", cfg.ClientSecret)
	}
}
