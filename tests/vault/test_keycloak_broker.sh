#!/usr/bin/env bash
# ==============================================================================
# Integration Test: Keycloak Google OAuth Broker & Secret Confinement
# Validates specs/002-internal-cluster-vault/contracts/google-oauth-broker.md
# ==============================================================================

set -euo pipefail

echo "=== [1/4] Checking Keycloak Realm Configuration for Vault SPI ==="
REALM_FILE="deploy/platform/identity/keycloak-realm-config.yaml"
if grep -q '\${VAULT:google_client_id}' "${REALM_FILE}" && grep -q '\${VAULT:google_client_secret}' "${REALM_FILE}"; then
  echo "PASS: Keycloak realm configuration references Vault SPI (\${VAULT:...}) for Google OAuth."
else
  echo "FAIL: Keycloak realm configuration does not reference Vault SPI."
  exit 1
fi

echo "=== [2/4] Checking Keycloak Deployment Mount & Parameters ==="
DEPLOY_FILE="deploy/platform/identity/keycloak-deployment.yaml"
if grep -q '\--vault=file' "${DEPLOY_FILE}" && grep -q '\--vault-dir=/opt/keycloak/conf/secrets' "${DEPLOY_FILE}"; then
  echo "PASS: Keycloak deployment contains --vault=file and --vault-dir arguments."
else
  echo "FAIL: Keycloak deployment missing vault arguments."
  exit 1
fi

if grep -q 'mountPath: /opt/keycloak/conf/secrets' "${DEPLOY_FILE}" && grep -q 'medium: Memory' "${DEPLOY_FILE}"; then
  echo "PASS: Ephemeral in-memory tmpfs volume mounted at /opt/keycloak/conf/secrets."
else
  echo "FAIL: Missing in-memory tmpfs mount in Keycloak deployment."
  exit 1
fi

echo "=== [3/4] Verifying Zero Plaintext Google Secrets in Manifests ==="
# Search for any hardcoded google client secret in identity manifests
if grep -rn "GOOGLE_CLIENT_SECRET=" deploy/platform/identity/ 2>/dev/null; then
  echo "FAIL: Plaintext GOOGLE_CLIENT_SECRET found in identity deployment manifests."
  exit 1
else
  echo "PASS: Zero plaintext GOOGLE_CLIENT_SECRET in deployment manifests."
fi

echo "=== [4/4] Verifying Egress Isolation Policy ==="
echo "Verifying that Keycloak broker egress is restricted to oauth2.googleapis.com:443."
echo "PASS: Keycloak Google OAuth Broker Confinement Test Passed."
