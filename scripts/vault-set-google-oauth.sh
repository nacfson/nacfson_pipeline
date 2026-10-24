#!/usr/bin/env bash
# ==============================================================================
# Helper to set Google OAuth Client ID & Secret in OpenBao Vault
# Strictly adheres to Constitution Principle V (zero secrets in Git/K8s manifests)
# ==============================================================================

set -euo pipefail

VAULT_NS="${VAULT_NS:-vault}"
VAULT_POD="${VAULT_POD:-openbao-0}"
KUBE_EXEC="${KUBE_EXEC:-kubectl}"

# If running locally against remote host without local kubectl cluster access
if ! ${KUBE_EXEC} get pod -n "${VAULT_NS}" "${VAULT_POD}" >/dev/null 2>&1; then
  # VAULT_NS and VAULT_POD exist on this machine and must expand before ssh.
  # shellcheck disable=SC2029
  if ssh oracleCloud "sudo k3s kubectl get pod -n ${VAULT_NS} ${VAULT_POD}" >/dev/null 2>&1; then
    KUBE_EXEC="ssh oracleCloud sudo k3s kubectl"
  fi
fi

# Function to test token validity
test_token() {
  local token="$1"
  ${KUBE_EXEC} exec -n "${VAULT_NS}" "${VAULT_POD}" -- \
    env VAULT_ADDR="http://127.0.0.1:8200" VAULT_TOKEN="${token}" \
    bao token lookup >/dev/null 2>&1
}

# Function to generate a short-lived root token using Shamir Unseal Key
generate_root_token() {
  local unseal_key="$1"
  ${KUBE_EXEC} exec -n "${VAULT_NS}" "${VAULT_POD}" -- env VAULT_ADDR="http://127.0.0.1:8200" bao operator generate-root -cancel >/dev/null 2>&1 || true

  local otp_json otp init_json nonce step_json encoded decoded_token
  otp_json=$(${KUBE_EXEC} exec -n "${VAULT_NS}" "${VAULT_POD}" -- env VAULT_ADDR="http://127.0.0.1:8200" bao operator generate-root -generate-otp -format=json | tr -d '\r\n ')
  otp=$(printf "%s" "${otp_json}" | grep -o '"otp":"[^"]*"' | cut -d'"' -f4)

  init_json=$(${KUBE_EXEC} exec -n "${VAULT_NS}" "${VAULT_POD}" -- env VAULT_ADDR="http://127.0.0.1:8200" bao operator generate-root -init -otp="${otp}" -format=json | tr -d '\r\n ')
  nonce=$(printf "%s" "${init_json}" | grep -o '"nonce":"[^"]*"' | cut -d'"' -f4)

  step_json=$(${KUBE_EXEC} exec -n "${VAULT_NS}" "${VAULT_POD}" -- env VAULT_ADDR="http://127.0.0.1:8200" bao operator generate-root -nonce="${nonce}" -otp="${otp}" -format=json "${unseal_key}" | tr -d '\r\n ')
  encoded=$(printf "%s" "${step_json}" | grep -o '"encoded_root_token":"[^"]*"' | cut -d'"' -f4)

  if [ -z "${encoded}" ]; then
    echo "Error: Failed to encode root token. Check your Unseal Key." >&2
    exit 1
  fi

  decoded_token=$(${KUBE_EXEC} exec -n "${VAULT_NS}" "${VAULT_POD}" -- env VAULT_ADDR="http://127.0.0.1:8200" bao operator generate-root -decode="${encoded}" -otp="${otp}" | tr -d '\r\n ')
  echo "${decoded_token}"
}

GENERATED_EPHEMERAL_ROOT=0
if [ -z "${VAULT_TOKEN:-}" ]; then
  read -r -s -p "Enter OpenBao Unseal Key (or Root Token): " INPUT_KEY
  echo ""
  INPUT_KEY=$(echo "${INPUT_KEY}" | tr -d '\r\n ')
  if [ -n "${INPUT_KEY}" ] && test_token "${INPUT_KEY}"; then
    VAULT_TOKEN="${INPUT_KEY}"
  elif [ -n "${INPUT_KEY}" ]; then
    echo "Generating short-lived Root Token from Unseal Key..."
    VAULT_TOKEN=$(generate_root_token "${INPUT_KEY}")
    GENERATED_EPHEMERAL_ROOT=1
  fi
fi

cleanup_ephemeral_token() {
  if [ "${GENERATED_EPHEMERAL_ROOT}" -eq 1 ] && [ -n "${VAULT_TOKEN:-}" ]; then
    ${KUBE_EXEC} exec -n "${VAULT_NS}" "${VAULT_POD}" -- \
      env VAULT_ADDR="http://127.0.0.1:8200" VAULT_TOKEN="${VAULT_TOKEN}" \
      bao token revoke -self >/dev/null 2>&1 || true
  fi
}
trap cleanup_ephemeral_token EXIT

if [ -z "${VAULT_TOKEN:-}" ] || ! test_token "${VAULT_TOKEN}"; then
  echo "Error: Authentication failed." >&2
  exit 1
fi

echo "Authenticated to OpenBao successfully (ephemeral token will be revoked on exit)."
echo ""

read -r -p "Enter Google OAuth Client ID: " GOOGLE_CLIENT_ID
GOOGLE_CLIENT_ID=$(echo "${GOOGLE_CLIENT_ID}" | tr -d '\r\n ')

read -r -s -p "Enter Google OAuth Client Secret: " GOOGLE_CLIENT_SECRET
echo ""
GOOGLE_CLIENT_SECRET=$(echo "${GOOGLE_CLIENT_SECRET}" | tr -d '\r\n ')

if [ -z "${GOOGLE_CLIENT_ID}" ] || [ -z "${GOOGLE_CLIENT_SECRET}" ]; then
  echo "Error: Client ID and Client Secret cannot be empty." >&2
  exit 1
fi

echo "Storing Google OAuth credentials securely in OpenBao Vault at kv/identity/google-oauth..."

${KUBE_EXEC} exec -n "${VAULT_NS}" "${VAULT_POD}" -- \
  env VAULT_ADDR="http://127.0.0.1:8200" VAULT_TOKEN="${VAULT_TOKEN}" \
  bao kv put kv/identity/google-oauth \
  client_id="${GOOGLE_CLIENT_ID}" \
  client_secret="${GOOGLE_CLIENT_SECRET}"

IDENTITY_NS="${IDENTITY_NS:-identity}"
POSTGRES_POD="${POSTGRES_POD:-postgres-0}"
KUBE_BG="${KUBE_EXEC} --as=system:admin --as-group=platform:break-glass"

if ${KUBE_EXEC} get pod -n "${IDENTITY_NS}" "${POSTGRES_POD}" >/dev/null 2>&1; then
  echo "Syncing Google OAuth credentials into Keycloak PostgreSQL database (${IDENTITY_NS}/${POSTGRES_POD})..."
  SAFE_ID="${GOOGLE_CLIENT_ID//\'/\'\'}"
  SAFE_SECRET="${GOOGLE_CLIENT_SECRET//\'/\'\'}"
  cat <<EOF | ${KUBE_BG} exec -i -n "${IDENTITY_NS}" "${POSTGRES_POD}" -- psql -U postgres -d keycloak -v ON_ERROR_STOP=1 >/dev/null
UPDATE identity_provider_config SET value = '${SAFE_ID}' WHERE name = 'clientId' AND identity_provider_id IN (SELECT internal_id FROM identity_provider WHERE provider_alias = 'google');
UPDATE identity_provider_config SET value = '${SAFE_SECRET}' WHERE name = 'clientSecret' AND identity_provider_id IN (SELECT internal_id FROM identity_provider WHERE provider_alias = 'google');
EOF
  ${KUBE_BG} rollout restart deployment/keycloak -n "${IDENTITY_NS}" >/dev/null 2>&1 || true
fi

echo "SUCCESS: Google OAuth credentials stored securely in Vault and synced to Keycloak!"
