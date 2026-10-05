#!/usr/bin/env bash
# ==============================================================================
# OpenBao Vault Kubernetes Authentication & Workload Roles Configuration
# Strictly adheres to Constitution Principle III, Principle V, and Spec 005
# ==============================================================================

set -euo pipefail

VAULT_NS="${VAULT_NS:-vault}"
VAULT_POD="${VAULT_POD:-openbao-0}"
POLICIES_DIR="${POLICIES_DIR:-deploy/platform/vault/policies}"
KUBE_EXEC="${KUBE_EXEC:-kubectl}"

# If running against remote host without local kubectl cluster access
if ! ${KUBE_EXEC} get pod -n "${VAULT_NS}" "${VAULT_POD}" >/dev/null 2>&1; then
  if ssh oracleCloud "sudo k3s kubectl get pod -n ${VAULT_NS} ${VAULT_POD}" >/dev/null 2>&1; then
    KUBE_EXEC="ssh oracleCloud sudo k3s kubectl"
  fi
fi

echo "=== [1/4] Verifying Vault Status ==="
INIT_STATUS=$(${KUBE_EXEC} exec -n "${VAULT_NS}" "${VAULT_POD}" -- env VAULT_ADDR="http://127.0.0.1:8200" bao status -format=json 2>/dev/null || true)
IS_SEALED=$(echo "${INIT_STATUS}" | grep -o '"sealed":[^,]*' | cut -d: -f2 | tr -d ' ' || echo "true")
IS_INITIALIZED=$(echo "${INIT_STATUS}" | grep -o '"initialized":[^,]*' | cut -d: -f2 | tr -d ' ' || echo "false")

if [ "${IS_INITIALIZED}" != "true" ] || [ "${IS_SEALED}" == "true" ]; then
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

  local otp_json=$(${KUBE_EXEC} exec -n "${VAULT_NS}" "${VAULT_POD}" -- env VAULT_ADDR="http://127.0.0.1:8200" bao operator generate-root -generate-otp -format=json)
  local otp=$(echo "${otp_json}" | grep -o '"otp":"[^"]*"' | cut -d'"' -f4)

  local init_json=$(${KUBE_EXEC} exec -n "${VAULT_NS}" "${VAULT_POD}" -- env VAULT_ADDR="http://127.0.0.1:8200" bao operator generate-root -init -otp="${otp}" -format=json)
  local nonce=$(echo "${init_json}" | grep -o '"nonce":"[^"]*"' | cut -d'"' -f4)

  local step_json=$(${KUBE_EXEC} exec -n "${VAULT_NS}" "${VAULT_POD}" -- env VAULT_ADDR="http://127.0.0.1:8200" bao operator generate-root -nonce="${nonce}" -otp="${otp}" -format=json "${unseal_key}")
  local encoded=$(echo "${step_json}" | grep -o '"encoded_root_token":"[^"]*"' | cut -d'"' -f4)

  if [ -z "${encoded}" ]; then
    echo "Error: Failed to encode root token. Check your unseal key." >&2
    exit 1
  fi

  local decoded_token=$(${KUBE_EXEC} exec -n "${VAULT_NS}" "${VAULT_POD}" -- env VAULT_ADDR="http://127.0.0.1:8200" bao operator generate-root -decode="${encoded}" -otp="${otp}" | tr -d '\r\n ')
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

run_bao() {
  ${KUBE_EXEC} exec -n "${VAULT_NS}" "${VAULT_POD}" -- env VAULT_ADDR="http://127.0.0.1:8200" VAULT_TOKEN="${VAULT_TOKEN}" bao "$@"
}

echo "=== [2/4] Enabling & Configuring Kubernetes Auth Method ==="
run_bao auth enable kubernetes 2>/dev/null || echo "auth/kubernetes already enabled."

echo "Provisioning Kubernetes Cluster CA and Token Reviewer JWT..."
CA_DATA=$(${KUBE_EXEC} config view --raw -o jsonpath='{.clusters[0].cluster.certificate-authority-data}' | base64 -d)
TOKEN_JWT=$(${KUBE_EXEC} create token openbao-token-reviewer -n vault --duration=87600h)

${KUBE_EXEC} exec -n "${VAULT_NS}" "${VAULT_POD}" -- /bin/sh -c "cat <<'EOF' > /tmp/ca.crt
${CA_DATA}
EOF
cat <<'EOF' > /tmp/token_reviewer.jwt
${TOKEN_JWT}
EOF"

run_bao write auth/kubernetes/config \
  kubernetes_host="https://kubernetes.default.svc:443" \
  kubernetes_ca_cert=@/tmp/ca.crt \
  token_reviewer_jwt=@/tmp/token_reviewer.jwt \
  disable_iss_validation=true

echo "=== [3/4] Syncing Vault RBAC Policies ==="
if [ -d "${POLICIES_DIR}" ]; then
  for policy_file in "${POLICIES_DIR}"/*.hcl; do
    if [ -f "${policy_file}" ]; then
      policy_name=$(basename "${policy_file}" .hcl)
      echo "Applying policy: ${policy_name}..."
      ${KUBE_EXEC} exec -n "${VAULT_NS}" -i "${VAULT_POD}" -- \
        env VAULT_ADDR="http://127.0.0.1:8200" VAULT_TOKEN="${VAULT_TOKEN}" \
        bao policy write "${policy_name}" - < "${policy_file}"
    fi
  done
fi

echo "=== [4/4] Configuring Workload ServiceAccount Roles ==="

create_k8s_role() {
  local role_name="$1"
  local sa_name="$2"
  local sa_ns="$3"
  local policy="$4"

  echo "Configuring role: ${role_name} (SA: ${sa_ns}/${sa_name}, Policy: ${policy})..."
  run_bao write "auth/kubernetes/role/${role_name}" \
    bound_service_account_names="${sa_name}" \
    bound_service_account_namespaces="${sa_ns}" \
    policies="${policy}" \
    ttl="10m"
}

# Identity & Database Workload Roles
create_k8s_role "postgres-server" "postgres" "identity" "postgres-server"
create_k8s_role "postgres-init" "postgres-init" "identity" "postgres-init"
create_k8s_role "postgres-backup" "postgres-backup" "identity" "backup-job"
create_k8s_role "keycloak" "keycloak" "identity" "keycloak-broker"
create_k8s_role "gateway" "gateway" "identity" "gateway-transit"

# Vault Proxy Roles
create_k8s_role "vault-db-proxy" "vault-db-proxy" "vault" "db-proxy"
create_k8s_role "vault-registry-proxy" "vault-registry-proxy" "vault" "registry-proxy"
create_k8s_role "openbao-backup" "openbao-backup" "vault" "operator-admin"

echo "SUCCESS: Kubernetes Auth and all workload roles successfully configured!"
