#!/usr/bin/env bash
# ==============================================================================
# OpenBao Vault Initialization & Unseal Automation
# Strictly adheres to specs/002-internal-cluster-vault/spec.md (FR-015)
# ==============================================================================

set -euo pipefail

VAULT_NS="vault"
VAULT_POD="openbao-0"
POLICIES_DIR="deploy/platform/vault/policies"

echo "=== [1/5] Checking OpenBao Pod Status in namespace '${VAULT_NS}' ==="
if ! kubectl get pod -n "${VAULT_NS}" "${VAULT_POD}" >/dev/null 2>&1; then
  echo "Error: Pod ${VAULT_POD} not found in namespace ${VAULT_NS}."
  echo "Wait for the Flux layer \`vault\` to apply (\`flux get kustomizations -A\`)"
  exit 1
fi

echo "=== [2/5] Checking Initialization Status ==="
INIT_STATUS=$(kubectl exec -n "${VAULT_NS}" "${VAULT_POD}" -- env VAULT_ADDR="http://127.0.0.1:8200" bao status -format=json 2>/dev/null || true)

IS_INITIALIZED=$(echo "${INIT_STATUS}" | grep -o '"initialized":[^,]*' | cut -d: -f2 | tr -d ' ' || echo "false")
IS_SEALED=$(echo "${INIT_STATUS}" | grep -o '"sealed":[^,]*' | cut -d: -f2 | tr -d ' ' || echo "true")

if [ "${IS_INITIALIZED}" != "true" ]; then
  echo "OpenBao is uninitialized. Running operator init (key-shares=1, key-threshold=1)..."
  INIT_OUTPUT=$(kubectl exec -n "${VAULT_NS}" "${VAULT_POD}" -- env VAULT_ADDR="http://127.0.0.1:8200" bao operator init -key-shares=1 -key-threshold=1 -format=json)
  
  UNSEAL_KEY=$(echo "${INIT_OUTPUT}" | grep -o '"unseal_keys_b64":\["[^"]*"' | cut -d'"' -f4)
  ROOT_TOKEN=$(echo "${INIT_OUTPUT}" | grep -o '"root_token":"[^"]*"' | cut -d'"' -f4)
  
  echo ""
  echo "****************************************************************"
  echo "CRITICAL: Store these credentials in your secure password manager!"
  echo "Unseal Key: ${UNSEAL_KEY}"
  echo "Root Token: ${ROOT_TOKEN}"
  echo "****************************************************************"
  echo ""
  
  echo "=== [3/5] Unsealing OpenBao ==="
  kubectl exec -n "${VAULT_NS}" "${VAULT_POD}" -- env VAULT_ADDR="http://127.0.0.1:8200" bao operator unseal "${UNSEAL_KEY}"
else
  echo "OpenBao is already initialized."
  if [ "${IS_SEALED}" == "true" ]; then
    echo "OpenBao is sealed. Prompting for unseal key..."
    read -r -s -p "Enter Unseal Key: " UNSEAL_KEY
    echo ""
    kubectl exec -n "${VAULT_NS}" "${VAULT_POD}" -- env VAULT_ADDR="http://127.0.0.1:8200" bao operator unseal "${UNSEAL_KEY}"
  else
    echo "OpenBao is already unsealed."
  fi
fi

echo "=== [4/5] Enabling Core Secrets Engines & Policies ==="
if [ -n "${ROOT_TOKEN:-}" ]; then
  export VAULT_TOKEN="${ROOT_TOKEN}"
fi

if [ -z "${VAULT_TOKEN:-}" ]; then
  read -r -s -p "Enter Vault Admin/Root Token to configure policies: " VAULT_TOKEN
  echo ""
fi

# Enable KV v2 engine if not already enabled
kubectl exec -n "${VAULT_NS}" "${VAULT_POD}" -- env VAULT_ADDR="http://127.0.0.1:8200" VAULT_TOKEN="${VAULT_TOKEN}" \
  bao secrets enable -version=2 kv 2>/dev/null || echo "KV v2 engine already enabled."

# Enable Transit engine if not already enabled
kubectl exec -n "${VAULT_NS}" "${VAULT_POD}" -- env VAULT_ADDR="http://127.0.0.1:8200" VAULT_TOKEN="${VAULT_TOKEN}" \
  bao secrets enable transit 2>/dev/null || echo "Transit engine already enabled."

# Enable file audit device (FR-011, T028)
kubectl exec -n "${VAULT_NS}" "${VAULT_POD}" -- env VAULT_ADDR="http://127.0.0.1:8200" VAULT_TOKEN="${VAULT_TOKEN}" \
  bao audit enable file file_path=/var/log/openbao/audit.log log_raw=false 2>/dev/null || echo "Audit device already enabled."

# Apply RBAC policies from deploy/platform/vault/policies/
if [ -d "${POLICIES_DIR}" ]; then
  for policy_file in "${POLICIES_DIR}"/*.hcl; do
    if [ -f "${policy_file}" ]; then
      policy_name=$(basename "${policy_file}" .hcl)
      echo "Applying policy: ${policy_name}..."
      kubectl exec -n "${VAULT_NS}" -i "${VAULT_POD}" -- env VAULT_ADDR="http://127.0.0.1:8200" VAULT_TOKEN="${VAULT_TOKEN}" \
        bao policy write "${policy_name}" - < "${policy_file}"
    fi
  done
fi

echo "=== [5/5] Setup Completed Successfully ==="
echo "Note: Per Spec FR-015, revoke the initial root token after provisioning operator AppRoles:"
echo "kubectl exec -n ${VAULT_NS} ${VAULT_POD} -- env VAULT_TOKEN=<token> bao token revoke -self"
