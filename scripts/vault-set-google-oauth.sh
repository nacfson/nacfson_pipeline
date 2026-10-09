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

# Resolve Vault Token
if [ -z "${VAULT_TOKEN:-}" ]; then
  read -r -s -p "Enter Vault Root/Admin Token: " VAULT_TOKEN
  echo ""
fi
VAULT_TOKEN=$(echo "${VAULT_TOKEN}" | tr -d '\r\n ')

if ! test_token "${VAULT_TOKEN}"; then
  echo "Error: Vault Token rejected by Vault (Code 403: permission denied)." >&2
  exit 1
fi

echo "Vault Token validated successfully."
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

echo "SUCCESS: Google OAuth credentials stored securely in Vault!"
