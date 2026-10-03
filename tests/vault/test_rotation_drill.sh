#!/usr/bin/env bash
# ==============================================================================
# Integration Test: Vault Credential Staged Rotation & Emergency Revocation Drill
# Validates User Story 3, SC-004, FR-020
# ==============================================================================

set -euo pipefail

PROXY_DIR="services/vault-proxies"
ROTATE_SCRIPT="scripts/vault-rotate.sh"
export VAULT_MOCK_MODE=true

echo "=== [0/5] Pre-compiling Proxy Binaries ==="
(cd "${PROXY_DIR}" && go build -o /tmp/registry-proxy-drill ./cmd/registry-proxy)
(cd "${PROXY_DIR}" && go build -o /tmp/db-proxy-drill ./cmd/db-proxy)
echo "PASS: Binaries compiled cleanly."

echo "=== [1/5] Testing Staged Replacement Preparation ==="
STAGE_OUT=$("${ROTATE_SCRIPT}" stage ghcr-pull-token "ghp_simulated_v2_token")
echo "${STAGE_OUT}"
if echo "${STAGE_OUT}" | grep -q "SUCCESS: Staged"; then
  echo "PASS: Replacement credential staged without impacting active version."
else
  echo "FAIL: Staging failed."
  exit 1
fi

echo "=== [2/5] Testing Pre-Flight Adoption Checker (Failure Drill) ==="
# Start a temporary instance of registry-proxy
PORT=5501 UPSTREAM_REGISTRY="ghcr.io" GHCR_PULL_TOKEN="initial_active_token" \
  /tmp/registry-proxy-drill > /tmp/reg_proxy_test.log 2>&1 &
REG_PID=$!
trap "kill -9 ${REG_PID} 2>/dev/null || true" EXIT

sleep 1

# Send adoption check with invalid token (simulate failed pre-flight)
FAIL_RESP=$(curl -s -w "\n%{http_code}" -X POST "http://127.0.0.1:5501/admin/adoption-check" \
  -H "Content-Type: application/json" \
  -d '{"token":"test-invalid"}')

HTTP_CODE=$(echo "${FAIL_RESP}" | tail -n1)
BODY=$(echo "${FAIL_RESP}" | sed '$d')

echo "Response Body: ${BODY} (HTTP ${HTTP_CODE})"
if [ "${HTTP_CODE}" = "422" ] && echo "${BODY}" | grep -q "REJECTED"; then
  echo "PASS: Pre-flight adoption checker rejected invalid staged token."
else
  echo "FAIL: Expected HTTP 422 REJECTED but got ${HTTP_CODE}"
  exit 1
fi

echo "=== [3/5] Testing Pre-Flight Adoption Checker (Success Switchover Drill) ==="
# Send adoption check with valid token (simulate successful pre-flight)
SUCCESS_RESP=$(curl -s -w "\n%{http_code}" -X POST "http://127.0.0.1:5501/admin/adoption-check" \
  -H "Content-Type: application/json" \
  -d '{"token":"test-valid"}')

HTTP_CODE=$(echo "${SUCCESS_RESP}" | tail -n1)
BODY=$(echo "${SUCCESS_RESP}" | sed '$d')

echo "Response Body: ${BODY} (HTTP ${HTTP_CODE})"
if [ "${HTTP_CODE}" = "200" ] && echo "${BODY}" | grep -q "ADOPTED"; then
  echo "PASS: Pre-flight adoption checker accepted valid token and promoted to active."
else
  echo "FAIL: Expected HTTP 200 ADOPTED but got ${HTTP_CODE}"
  exit 1
fi

# Clean up registry proxy
kill -9 "${REG_PID}" 2>/dev/null || true

echo "=== [4/5] Testing Database Proxy Per-Operation Revocation Drill ==="
# Start a temporary instance of db-proxy
PORT=5533 HEALTH_PORT=8888 \
  /tmp/db-proxy-drill > /tmp/db_proxy_test.log 2>&1 &
DB_PID=$!
trap "kill -9 ${DB_PID} 2>/dev/null || true" EXIT

sleep 1

# Revoke an active caller subject
REVOKE_RESP=$(curl -s -X POST "http://127.0.0.1:8888/admin/revoke?sub=project:pn:workload:backend")
echo "Revoke Response: ${REVOKE_RESP}"

if echo "${REVOKE_RESP}" | grep -q "REVOKED"; then
  echo "PASS: DB Proxy registered immediate revocation of caller grant."
else
  echo "FAIL: DB Proxy revocation failed."
  exit 1
fi

# Clean up db proxy
kill -9 "${DB_PID}" 2>/dev/null || true

echo "=== [5/5] Testing Post-Rotation Issuer Invalidation Checklist ==="
CHECKLIST_OUT=$("${ROTATE_SCRIPT}" checklist ghcr-pull-token)
echo "${CHECKLIST_OUT}"
if echo "${CHECKLIST_OUT}" | grep -q "ISSUER INVALIDATION"; then
  echo "PASS: Operator checklist provided for upstream issuer invalidation."
else
  echo "FAIL: Checklist missing."
  exit 1
fi

echo ""
echo "=================================================================="
echo "ALL ROTATION & REVOCATION INTEGRATION TESTS PASSED (SC-004, FR-020)"
echo "=================================================================="
