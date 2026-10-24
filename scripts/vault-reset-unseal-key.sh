#!/usr/bin/env bash
# ==============================================================================
# Zero-Data-Loss OpenBao Unseal Key Regeneration & PostgreSQL Role Password Sync
# Use when the Shamir Unseal Key and Root Token are both lost.
# Wipes ONLY OpenBao's encrypted store, generates a brand-new Unseal Key,
# re-seeds Vault credentials, and syncs the new DB role passwords into
# postgres-0 via local Unix socket (preserving 100% of PostgreSQL tables/data).
# ==============================================================================

set -euo pipefail

VAULT_NS="${VAULT_NS:-vault}"
VAULT_POD="${VAULT_POD:-openbao-0}"
IDENTITY_NS="${IDENTITY_NS:-identity}"
POSTGRES_POD="${POSTGRES_POD:-postgres-0}"
KUBE_EXEC="${KUBE_EXEC:-kubectl}"

if ! ${KUBE_EXEC} --request-timeout=2s get pod -n "${VAULT_NS}" "${VAULT_POD}" >/dev/null 2>&1; then
  # shellcheck disable=SC2029
  if ssh -o ConnectTimeout=5 oracleCloud "sudo k3s kubectl get pod -n ${VAULT_NS} ${VAULT_POD}" >/dev/null 2>&1; then
    KUBE_EXEC="ssh oracleCloud sudo k3s kubectl"
  fi
fi

KUBE_BG="${KUBE_EXEC} --as=system:admin --as-group=platform:break-glass"

echo "================================================================================"
echo "ZERO-DATA-LOSS OPENBAO UNSEAL KEY REGENERATION"
echo "  - Wipes ONLY OpenBao Raft storage (/var/lib/openbao/data/* in ${VAULT_NS}/${VAULT_POD})"
echo "  - Generates a brand-new Shamir Unseal Key for your password manager"
echo "  - Re-seeds internal passwords & syncs them into ${IDENTITY_NS}/${POSTGRES_POD}"
echo "  - Preserves 100% of PostgreSQL tables and rows (${IDENTITY_NS}/${POSTGRES_POD})"
echo "================================================================================"
read -r -p "Type 'RESET-OPENBAO' to confirm and regenerate a new Unseal Key: " CONFIRM
if [ "${CONFIRM}" != "RESET-OPENBAO" ]; then
  echo "Aborted."
  exit 1
fi

echo "=== [1/6] Wiping OpenBao storage in ${VAULT_NS}/${VAULT_POD} and restarting pod ==="
${KUBE_BG} exec -n "${VAULT_NS}" "${VAULT_POD}" -- rm -rf /var/lib/openbao/data/vault.db /var/lib/openbao/data/raft
${KUBE_BG} delete pod -n "${VAULT_NS}" "${VAULT_POD}" --grace-period=0 --force --wait=true 2>/dev/null || \
  ${KUBE_BG} delete pod -n "${VAULT_NS}" "${VAULT_POD}" --wait=true

echo "Waiting for recreated ${VAULT_NS}/${VAULT_POD} container to start..."
READY_FOR_INIT=false
for _ in $(seq 1 60); do
  STATUS_JSON=$(${KUBE_BG} exec -n "${VAULT_NS}" "${VAULT_POD}" -- env VAULT_ADDR="http://127.0.0.1:8200" bao status -format=json 2>/dev/null || true)
  if echo "${STATUS_JSON}" | grep -Eq '"initialized":[[:space:]]*false'; then
    READY_FOR_INIT=true
    break
  fi
  sleep 2
done
if [ "${READY_FOR_INIT}" != "true" ]; then
  echo "ERROR: Timed out waiting for ${VAULT_NS}/${VAULT_POD} to enter uninitialized state."
  exit 1
fi

echo "=== [2/6] Initializing fresh OpenBao & generating new Unseal Key ==="
INIT_OUTPUT=$(${KUBE_BG} exec -n "${VAULT_NS}" "${VAULT_POD}" -- env VAULT_ADDR="http://127.0.0.1:8200" bao operator init -key-shares=1 -key-threshold=1 -format=json)
INIT_COMPACT=$(printf "%s" "${INIT_OUTPUT}" | tr -d '\r\n ')
UNSEAL_KEY=$(printf "%s" "${INIT_COMPACT}" | grep -o '"unseal_keys_b64":\["[^"]*"' | cut -d'"' -f4)
ROOT_TOKEN=$(printf "%s" "${INIT_COMPACT}" | grep -o '"root_token":"[^"]*"' | cut -d'"' -f4)

cleanup_root_token() {
  if [ -n "${ROOT_TOKEN:-}" ]; then
    ${KUBE_BG} exec -n "${VAULT_NS}" "${VAULT_POD}" -- \
      env VAULT_ADDR="http://127.0.0.1:8200" VAULT_TOKEN="${ROOT_TOKEN}" \
      bao token revoke -self >/dev/null 2>&1 || true
    ROOT_TOKEN=""
  fi
}
trap cleanup_root_token EXIT

echo ""
echo "********************************************************************************"
echo "CRITICAL: SAVE THIS NEW SHAMIR UNSEAL KEY IN YOUR PASSWORD MANAGER IMMEDIATELY!"
echo "New Unseal Key: ${UNSEAL_KEY}"
echo "********************************************************************************"
echo ""

${KUBE_BG} exec -n "${VAULT_NS}" "${VAULT_POD}" -- env VAULT_ADDR="http://127.0.0.1:8200" bao operator unseal "${UNSEAL_KEY}" >/dev/null
${KUBE_BG} wait --for=condition=Ready pod/"${VAULT_POD}" -n "${VAULT_NS}" --timeout=60s
echo "PASS: OpenBao unsealed and Ready with new Unseal Key."

echo "=== [3/6] Enabling KV v2 & Transit Engines and Configuring Least-Privilege K8s Auth ==="
${KUBE_BG} exec -n "${VAULT_NS}" "${VAULT_POD}" -- env VAULT_ADDR="http://127.0.0.1:8200" VAULT_TOKEN="${ROOT_TOKEN}" \
  bao secrets enable -version=2 kv 2>/dev/null || true
${KUBE_BG} exec -n "${VAULT_NS}" "${VAULT_POD}" -- env VAULT_ADDR="http://127.0.0.1:8200" VAULT_TOKEN="${ROOT_TOKEN}" \
  bao secrets enable transit 2>/dev/null || true

VAULT_TOKEN="${ROOT_TOKEN}" KUBE_EXEC="${KUBE_BG}" ./scripts/vault-configure-k8s-auth.sh

echo "=== [4/6] Seeding Fresh Internal Credentials into OpenBao ==="
VAULT_TOKEN="${ROOT_TOKEN}" KUBE_EXEC="${KUBE_BG}" ./scripts/vault-seed-inventory.sh --force

echo "=== [5/6] Syncing New Database Role Passwords & Keycloak Client Secrets into ${IDENTITY_NS}/${POSTGRES_POD} ==="
NEW_PG_ADMIN=$(${KUBE_BG} exec -n "${VAULT_NS}" "${VAULT_POD}" -- \
  env VAULT_ADDR="http://127.0.0.1:8200" VAULT_TOKEN="${ROOT_TOKEN}" \
  bao kv get -field=password kv/database/postgres-admin | tr -d '\r\n')

if [[ ! "${NEW_PG_ADMIN}" =~ ^[0-9a-fA-F]+$ ]]; then
  echo "ERROR: Unexpected format for generated postgres-admin password." >&2
  exit 1
fi

printf "ALTER ROLE postgres WITH PASSWORD '%s';\n" "${NEW_PG_ADMIN}" | \
  ${KUBE_BG} exec -i -n "${IDENTITY_NS}" "${POSTGRES_POD}" -- \
  psql -U postgres -d postgres -v ON_ERROR_STOP=1 >/dev/null
echo "PASS: Synced postgres administrator password via local Unix socket."

${KUBE_BG} delete job -n "${IDENTITY_NS}" postgres-init-job --ignore-not-found
kubectl kustomize deploy/platform/database | ${KUBE_BG} apply -f -
${KUBE_BG} wait --for=condition=complete job/postgres-init-job -n "${IDENTITY_NS}" --timeout=120s
echo "PASS: postgres-init-job synced keycloak_user, user_pn, and backup_role passwords."

# Sync rotated OIDC client secrets into Keycloak's existing 'platform' realm in PostgreSQL
# (kc.sh start --import-realm uses IGNORE_EXISTING and skips updating existing realms).
GW_SECRET=$(${KUBE_BG} exec -n "${VAULT_NS}" "${VAULT_POD}" -- env VAULT_ADDR="http://127.0.0.1:8200" VAULT_TOKEN="${ROOT_TOKEN}" bao kv get -field=client_secret kv/gateway/client-secret | tr -d '\r\n')
REV_SECRET=$(${KUBE_BG} exec -n "${VAULT_NS}" "${VAULT_POD}" -- env VAULT_ADDR="http://127.0.0.1:8200" VAULT_TOKEN="${ROOT_TOKEN}" bao kv get -field=client_secret kv/gateway/session-revocation-secret | tr -d '\r\n')
PN_SECRET=$(${KUBE_BG} exec -n "${VAULT_NS}" "${VAULT_POD}" -- env VAULT_ADDR="http://127.0.0.1:8200" VAULT_TOKEN="${ROOT_TOKEN}" bao kv get -field=client_secret kv/projects/pn/client-secret | tr -d '\r\n')
HL_SECRET=$(${KUBE_BG} exec -n "${VAULT_NS}" "${VAULT_POD}" -- env VAULT_ADDR="http://127.0.0.1:8200" VAULT_TOKEN="${ROOT_TOKEN}" bao kv get -field=client_secret kv/projects/health/client-secret | tr -d '\r\n')

cat <<EOF | ${KUBE_BG} exec -i -n "${IDENTITY_NS}" "${POSTGRES_POD}" -- psql -U postgres -d keycloak -v ON_ERROR_STOP=1 >/dev/null
UPDATE client SET secret = '${GW_SECRET}' WHERE realm_id = (SELECT id FROM realm WHERE name = 'platform') AND client_id = 'gateway-client';
UPDATE client SET secret = '${REV_SECRET}' WHERE realm_id = (SELECT id FROM realm WHERE name = 'platform') AND client_id = 'session-revocation-client';
UPDATE client SET secret = '${PN_SECRET}' WHERE realm_id = (SELECT id FROM realm WHERE name = 'platform') AND client_id = 'project-pn';
UPDATE client SET secret = '${HL_SECRET}' WHERE realm_id = (SELECT id FROM realm WHERE name = 'platform') AND client_id = 'project-health';
EOF
echo "PASS: Synced rotated OIDC client secrets into Keycloak PostgreSQL database."

# Preserve existing Google OAuth credentials from Keycloak PostgreSQL into OpenBao if present
EXISTING_GOOGLE_ID=$(${KUBE_BG} exec -n "${IDENTITY_NS}" "${POSTGRES_POD}" -- psql -U postgres -d keycloak -Atc "SELECT value FROM identity_provider_config WHERE name = 'clientId' AND identity_provider_id IN (SELECT internal_id FROM identity_provider WHERE provider_alias = 'google');" 2>/dev/null | tr -d '\r\n ' || true)
EXISTING_GOOGLE_SECRET=$(${KUBE_BG} exec -n "${IDENTITY_NS}" "${POSTGRES_POD}" -- psql -U postgres -d keycloak -Atc "SELECT value FROM identity_provider_config WHERE name = 'clientSecret' AND identity_provider_id IN (SELECT internal_id FROM identity_provider WHERE provider_alias = 'google');" 2>/dev/null | tr -d '\r\n ' || true)
if [ -n "${EXISTING_GOOGLE_ID}" ] && [ "${EXISTING_GOOGLE_ID}" != "google-apps.apps.googleusercontent.com" ] && [ -n "${EXISTING_GOOGLE_SECRET}" ]; then
  ${KUBE_BG} exec -n "${VAULT_NS}" "${VAULT_POD}" -- \
    env VAULT_ADDR="http://127.0.0.1:8200" VAULT_TOKEN="${ROOT_TOKEN}" \
    bao kv put kv/identity/google-oauth client_id="${EXISTING_GOOGLE_ID}" client_secret="${EXISTING_GOOGLE_SECRET}" >/dev/null
  echo "PASS: Preserved existing Google OAuth credentials in OpenBao kv/identity/google-oauth."
fi

echo "=== [6/6] Revoking Initial Root Token & Rolling Dependent Proxies ==="
cleanup_root_token

${KUBE_BG} rollout restart deployment/vault-db-proxy deployment/vault-registry-proxy deployment/vault-ai-proxy -n "${VAULT_NS}" 2>/dev/null || true
${KUBE_BG} rollout restart deployment/keycloak deployment/gateway -n "${IDENTITY_NS}" 2>/dev/null || true

echo "================================================================================"
echo "SUCCESS: OpenBao reset complete with zero PostgreSQL data loss!"
echo "********************************************************************************"
echo "SAVE THIS NEW SHAMIR UNSEAL KEY IN YOUR PASSWORD MANAGER:"
echo "New Unseal Key: ${UNSEAL_KEY}"
echo "********************************************************************************"
echo "Next steps for external API keys:"
echo "  1. ./scripts/ai-gateway-ctl.sh set-key"
echo "  2. ./scripts/vault-set-google-oauth.sh"
echo "================================================================================"
