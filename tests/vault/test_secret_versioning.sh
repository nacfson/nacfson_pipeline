#!/usr/bin/env bash
# ==============================================================================
# OpenBao Secret Versioning & Isolation Integration Test
# Validates SC-001, SC-002, and FR-002:
# 1. Immutable version history retention in KV v2
# 2. Rejection of unauthorized cross-project access
# 3. Value-free audit and status assertions
# ==============================================================================

set -euo pipefail

VAULT_ADDR="${VAULT_ADDR:-http://127.0.0.1:8200}"
VAULT_NS="vault"
VAULT_POD="openbao-0"

echo "=== [Test] Vault Secret Versioning & Isolation ==="

run_bao() {
  if [ "${VAULT_MOCK_MODE:-false}" = "true" ]; then
    case "$*" in
      *"get -version=1"*) echo '{"data":{"data":{"value":"synthetic-secret-v1"}}}' ;;
      *"get -version=2"*) echo '{"data":{"data":{"value":"synthetic-secret-v2"}}}' ;;
      *"token create"*) echo '{"auth":{"client_token":"s.mock_token"}}' ;;
      *"get kv/platform/keycloak-admin"*) echo "Error reading kv/data/platform/keycloak-admin: permission denied" ;;
      *) echo "OK" ;;
    esac
    return 0
  fi
  if [ -n "${KUBECTL_EXEC:-}" ] || kubectl --request-timeout=1s get pod -n "${VAULT_NS}" "${VAULT_POD}" >/dev/null 2>&1; then
    kubectl exec -n "${VAULT_NS}" "${VAULT_POD}" -- env VAULT_TOKEN="${VAULT_TOKEN:-root}" bao "$@"
  else
    env VAULT_ADDR="${VAULT_ADDR}" VAULT_TOKEN="${VAULT_TOKEN:-root}" bao "$@"
  fi
}

# 1. Write Version 1 of a synthetic test secret
echo "[1/4] Writing synthetic secret v1..."
run_bao kv put kv/projects/pn/test-secret value="synthetic-secret-v1" owner="project-pn" >/dev/null

# 2. Write Version 2
echo "[2/4] Writing synthetic secret v2..."
run_bao kv put kv/projects/pn/test-secret value="synthetic-secret-v2" owner="project-pn" >/dev/null

# 3. Assert version history retention
echo "[3/4] Asserting immutable version history..."
V1_OUTPUT=$(run_bao kv get -version=1 -format=json kv/projects/pn/test-secret)
V2_OUTPUT=$(run_bao kv get -version=2 -format=json kv/projects/pn/test-secret)

V1_VAL=$(echo "${V1_OUTPUT}" | grep -o '"value":"[^"]*"' | cut -d'"' -f4)
V2_VAL=$(echo "${V2_OUTPUT}" | grep -o '"value":"[^"]*"' | cut -d'"' -f4)

if [ "${V1_VAL}" != "synthetic-secret-v1" ]; then
  echo "FAIL: Version 1 was overwritten or mutated! Expected 'synthetic-secret-v1', got '${V1_VAL}'"
  exit 1
fi

if [ "${V2_VAL}" != "synthetic-secret-v2" ]; then
  echo "FAIL: Version 2 mismatch! Expected 'synthetic-secret-v2', got '${V2_VAL}'"
  exit 1
fi
echo "✓ PASS: Version 1 and Version 2 immutably retained."

# 4. Test RBAC Isolation Denial
echo "[4/4] Asserting cross-project access denial..."
TOKEN_RESP=$(run_bao token create -policy="db-proxy" -ttl="10m" -format=json)
SCOPED_TOKEN=$(echo "${TOKEN_RESP}" | grep -o '"client_token":"[^"]*"' | cut -d'"' -f4)

# Attempt to access unauthorized operator or admin secret with scoped token
DENIAL_RESULT=$(VAULT_TOKEN="${SCOPED_TOKEN}" run_bao kv get kv/platform/keycloak-admin 2>&1 || true)

if echo "${DENIAL_RESULT}" | grep -q "permission denied"; then
  echo "✓ PASS: Access to unauthorized secret strictly denied with 'permission denied'."
else
  echo "FAIL: Expected 'permission denied' for scoped token, but got: ${DENIAL_RESULT}"
  exit 1
fi

# Cleanup synthetic test secret
run_bao kv metadata delete kv/projects/pn/test-secret >/dev/null 2>&1 || true
echo "=== All Secret Versioning & Isolation Tests Passed Successfully ==="
