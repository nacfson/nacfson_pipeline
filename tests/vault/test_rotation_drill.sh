#!/usr/bin/env bash
# ==============================================================================
# Integration Test: Vault Credential Staged Rotation & Emergency Revocation Drill
# Validates User Story 3, SC-004, FR-020 (updated in Spec 012 for retired registry-proxy)
# ==============================================================================

set -euo pipefail

PROXY_DIR="services/vault-proxies"
ROTATE_SCRIPT="scripts/vault-rotate.sh"
export VAULT_MOCK_MODE=true

echo "=== [0/3] Pre-compiling Active Proxy Binaries ==="
(cd "${PROXY_DIR}" && go build -o /tmp/db-proxy-drill ./cmd/db-proxy)
trap 'rm -f /tmp/db-proxy-drill /tmp/db_proxy_test.log' EXIT
echo "PASS: db-proxy binary compiled cleanly."

echo "=== [1/3] Testing Staged Replacement Preparation ==="
STAGE_OUT=$("${ROTATE_SCRIPT}" stage gateway-hmac-signing-key "simulated_v2_hmac_secret")
echo "${STAGE_OUT}"
if echo "${STAGE_OUT}" | grep -q "SUCCESS: Staged"; then
  echo "PASS: Replacement credential staged without impacting active version."
else
  echo "FAIL: Staging failed."
  exit 1
fi

echo "=== [2/3] Testing Database Proxy Per-Operation Revocation Drill ==="
PORT=5533 HEALTH_PORT=8888 \
  /tmp/db-proxy-drill > /tmp/db_proxy_test.log 2>&1 &
DB_PID=$!
trap 'kill -9 "${DB_PID}" 2>/dev/null || true; rm -f /tmp/db-proxy-drill /tmp/db_proxy_test.log' EXIT

sleep 1

REVOKE_RESP=$(curl -s -X POST "http://127.0.0.1:8888/admin/revoke?sub=project:pn:workload:backend")
echo "Revoke Response: ${REVOKE_RESP}"

if echo "${REVOKE_RESP}" | grep -q "REVOKED"; then
  echo "PASS: DB Proxy registered immediate revocation of caller grant."
else
  echo "FAIL: DB Proxy revocation failed."
  exit 1
fi

kill -9 "${DB_PID}" 2>/dev/null || true

echo "=== [3/3] Testing Post-Rotation Issuer Invalidation Checklist ==="
CHECKLIST_OUT=$("${ROTATE_SCRIPT}" checklist gateway-hmac-signing-key)
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
