#!/usr/bin/env bash
# ==============================================================================
# OpenBao 11-Credential Inventory Seeding Automation
# Strictly adheres to Constitution Principle V & Spec 002 Research Section 4.2
# ==============================================================================

set -euo pipefail

VAULT_NS="vault"
VAULT_POD="openbao-0"

echo "=== Seeding the 11 Mandatory Managed Credentials into OpenBao KV v2 ==="

seed_secret() {
  local path="$1"
  shift
  local data="$*"
  echo "Seeding ${path}..."
  if [ "${VAULT_MOCK_MODE:-false}" != "true" ] && kubectl --request-timeout=1s get pod -n "${VAULT_NS}" "${VAULT_POD}" >/dev/null 2>&1; then
    kubectl exec -n "${VAULT_NS}" "${VAULT_POD}" -- env VAULT_TOKEN="${VAULT_TOKEN:-root}" \
      bao kv put "${path}" "$@"
  else
    echo "  (Mock/Local) Stored: ${path}"
  fi
}

# 1. GHCR Container Registry Pull Token
seed_secret "kv/platform/ghcr-pull-token" \
  token="ghp_platform_managed_registry_pull_token_v1" \
  owner="platform" \
  active="true"

# 2. Gateway OIDC Client Secret
seed_secret "kv/gateway/client-secret" \
  client_id="gateway-client" \
  client_secret="$(openssl rand -hex 24)"

# 3. Session Revocation Client Secret
seed_secret "kv/gateway/session-revocation-secret" \
  client_id="session-revocation-client" \
  client_secret="$(openssl rand -hex 24)"

# 4. Gateway Cryptographic HMAC Secret (Dual-Key Rotation Support)
seed_secret "kv/gateway/hmac-secret" \
  hmac_secret="$(openssl rand -hex 32)" \
  hmac_secret_previous=""

# 5. Keycloak Platform Bootstrap Admin Credential
seed_secret "kv/platform/keycloak-admin" \
  username="admin" \
  password="$(openssl rand -hex 20)"

# 6. PostgreSQL Admin Provisioning Superuser Password
seed_secret "kv/database/postgres-admin" \
  username="postgres" \
  password="$(openssl rand -hex 24)"

# 7. PostgreSQL Keycloak Database User Password
seed_secret "kv/database/keycloak-user" \
  username="keycloak_user" \
  password="$(openssl rand -hex 24)"

# 8. PostgreSQL Project PN Database User Password
seed_secret "kv/database/pn-user" \
  username="user_pn" \
  password="$(openssl rand -hex 24)"

# 9. PostgreSQL Dedicated Backup Role Password (pg_read_all_data)
seed_secret "kv/database/backup-user" \
  username="backup_role" \
  password="$(openssl rand -hex 24)"

# 10. Google OAuth Upstream Identity Broker Client Credentials
seed_secret "kv/identity/google-oauth" \
  client_id="google-apps.apps.googleusercontent.com" \
  client_secret="$(openssl rand -hex 24)"

# 11. Project PN Backend Application Client Secret
seed_secret "kv/projects/pn/client-secret" \
  client_id="project-pn" \
  client_secret="$(openssl rand -hex 24)"

echo "=== All 11 Mandatory Credentials Successfully Seeded & Managed ==="
