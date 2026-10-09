#!/usr/bin/env bash
# ==============================================================================
# OpenBao 11-Credential Inventory Seeding Automation
# Strictly adheres to Constitution Principle V & Spec 002 Research Section 4.2
# ==============================================================================

set -euo pipefail

VAULT_NS="${VAULT_NS:-vault}"
VAULT_POD="${VAULT_POD:-openbao-0}"
KUBE_EXEC="${KUBE_EXEC:-kubectl}"
FORCE_SEED="${FORCE_SEED:-false}"

if [ "${1:-}" = "--force" ]; then
  FORCE_SEED="true"
fi

# If running against remote host without local kubectl cluster access
if ! ${KUBE_EXEC} get pod -n "${VAULT_NS}" "${VAULT_POD}" >/dev/null 2>&1; then
  # VAULT_NS and VAULT_POD exist on this machine and must expand before ssh.
  # shellcheck disable=SC2029
  if ssh oracleCloud "sudo k3s kubectl get pod -n ${VAULT_NS} ${VAULT_POD}" >/dev/null 2>&1; then
    KUBE_EXEC="ssh oracleCloud sudo k3s kubectl"
  fi
fi

echo "=== [1/3] Verifying Vault Status ==="
INIT_STATUS=$(${KUBE_EXEC} exec -n "${VAULT_NS}" "${VAULT_POD}" -- env VAULT_ADDR="http://127.0.0.1:8200" bao status -format=json 2>/dev/null || true)
IS_SEALED=$(echo "${INIT_STATUS}" | grep -o '"sealed":[^,]*' | cut -d: -f2 | tr -d ' ' || echo "true")
IS_INITIALIZED=$(echo "${INIT_STATUS}" | grep -o '"initialized":[^,]*' | cut -d: -f2 | tr -d ' ' || echo "false")

if [ "${IS_INITIALIZED}" != "true" ] || [ "${IS_SEALED}" = "true" ]; then
  echo "Error: Vault is not initialized or is sealed. Please unseal Vault first." >&2
  exit 1
fi
echo "PASS: Vault is initialized and unsealed."

# Function to test token validity
test_token() {
  local token="$1"
  ${KUBE_EXEC} exec -n "${VAULT_NS}" "${VAULT_POD}" -- \
    env VAULT_ADDR="http://127.0.0.1:8200" VAULT_TOKEN="${token}" \
    bao token lookup >/dev/null 2>&1
}

# Function to generate a new root token using Shamir Unseal Key
generate_root_token() {
  local unseal_key="$1"
  echo "Initiating root token generation ceremony..."
  ${KUBE_EXEC} exec -n "${VAULT_NS}" "${VAULT_POD}" -- env VAULT_ADDR="http://127.0.0.1:8200" bao operator generate-root -cancel >/dev/null 2>&1 || true

  local otp_json
  otp_json=$(${KUBE_EXEC} exec -n "${VAULT_NS}" "${VAULT_POD}" -- env VAULT_ADDR="http://127.0.0.1:8200" bao operator generate-root -generate-otp -format=json)
  local otp
  otp=$(echo "${otp_json}" | grep -o '"otp":"[^"]*"' | cut -d'"' -f4)

  local init_json
  init_json=$(${KUBE_EXEC} exec -n "${VAULT_NS}" "${VAULT_POD}" -- env VAULT_ADDR="http://127.0.0.1:8200" bao operator generate-root -init -otp="${otp}" -format=json)
  local nonce
  nonce=$(echo "${init_json}" | grep -o '"nonce":"[^"]*"' | cut -d'"' -f4)

  local step_json
  step_json=$(${KUBE_EXEC} exec -n "${VAULT_NS}" "${VAULT_POD}" -- env VAULT_ADDR="http://127.0.0.1:8200" bao operator generate-root -nonce="${nonce}" -otp="${otp}" -format=json "${unseal_key}")
  local encoded
  encoded=$(echo "${step_json}" | grep -o '"encoded_root_token":"[^"]*"' | cut -d'"' -f4)

  if [ -z "${encoded}" ]; then
    echo "Error: Failed to encode root token. Check your unseal key." >&2
    exit 1
  fi

  local decoded_token
  decoded_token=$(${KUBE_EXEC} exec -n "${VAULT_NS}" "${VAULT_POD}" -- env VAULT_ADDR="http://127.0.0.1:8200" bao operator generate-root -decode="${encoded}" -otp="${otp}" | tr -d '\r\n ')
  echo "${decoded_token}"
}

# Resolve Vault Token (Prompt or environment)
if [ -z "${VAULT_TOKEN:-}" ]; then
  read -r -s -p "Enter Vault Root/Admin Token (or press Enter if you only have the Unseal Key): " VAULT_TOKEN
  echo ""
fi
VAULT_TOKEN=$(echo "${VAULT_TOKEN}" | tr -d '\r\n ')

if [ -n "${VAULT_TOKEN}" ] && test_token "${VAULT_TOKEN}"; then
  echo "PASS: Vault Token authenticated successfully."
else
  if [ -n "${VAULT_TOKEN}" ]; then
    echo "Notice: The provided token was rejected by Vault (Code 403: permission denied)."
    echo "Remember: The Root Token is different from the Unseal Key."
  fi
  read -r -p "Would you like to generate a new Root Token using your Unseal Key? [Y/n]: " DO_GEN
  if [[ "${DO_GEN}" =~ ^[Nn] ]]; then
    echo "Aborted." >&2
    exit 1
  fi

  read -r -s -p "Enter Unseal Key: " UNSEAL_KEY
  echo ""
  UNSEAL_KEY=$(echo "${UNSEAL_KEY}" | tr -d '\r\n ')

  VAULT_TOKEN=$(generate_root_token "${UNSEAL_KEY}")
  echo "================================================================================"
  echo "SUCCESS: Generated new Root Token: ${VAULT_TOKEN}"
  echo "Please store this Root Token in your password manager!"
  echo "================================================================================"
fi

echo "=== [2/3] Ensuring KV v2 Engine is Enabled ==="
${KUBE_EXEC} exec -n "${VAULT_NS}" "${VAULT_POD}" -- \
  env VAULT_ADDR="http://127.0.0.1:8200" VAULT_TOKEN="${VAULT_TOKEN}" \
  bao secrets enable -version=2 kv 2>/dev/null || echo "KV v2 engine already enabled."

echo "=== [3/3] Seeding the 11 Mandatory Managed Credentials into OpenBao KV v2 ==="

seed_secret() {
  local path="$1"
  shift
  if [ "${FORCE_SEED}" != "true" ] && ${KUBE_EXEC} exec -n "${VAULT_NS}" "${VAULT_POD}" -- \
      env VAULT_ADDR="http://127.0.0.1:8200" VAULT_TOKEN="${VAULT_TOKEN}" \
      bao kv get "${path}" >/dev/null 2>&1; then
    echo "Already exists: ${path} (skipping, use --force to overwrite)"
    return 0
  fi

  echo "Seeding ${path}..."
  ${KUBE_EXEC} exec -n "${VAULT_NS}" "${VAULT_POD}" -- \
    env VAULT_ADDR="http://127.0.0.1:8200" VAULT_TOKEN="${VAULT_TOKEN}" \
    bao kv put "${path}" "$@"
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
