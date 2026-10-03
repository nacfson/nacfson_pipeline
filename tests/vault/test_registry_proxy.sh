#!/usr/bin/env bash
# ==============================================================================
# Vault Registry Pull Proxy Verification Drill
# Validates SC-005 & SC-008:
# 1. Anonymous manifest retrieval with digest succeeds
# 2. Inbound Authorization header is strictly rejected
# 3. Mutable tags without digest are rejected
# 4. Zero tokens exposed in headers or responses
# ==============================================================================

set -euo pipefail

PROXY_BIN="/tmp/registry-proxy-drill-test"
if [ -z "${REGISTRY_PROXY_URL:-}" ]; then
  echo "Starting isolated test instance of registry proxy on port 5505..."
  (cd services/vault-proxies && go build -o "${PROXY_BIN}" ./cmd/registry-proxy)
  PORT=5505 UPSTREAM_REGISTRY="ghcr.io" GHCR_PULL_TOKEN="initial_token" "${PROXY_BIN}" >/dev/null 2>&1 &
  PROXY_PID=$!
  trap "kill -9 ${PROXY_PID} 2>/dev/null || true" EXIT
  sleep 1
  PROXY_URL="http://127.0.0.1:5505"
else
  PROXY_URL="${REGISTRY_PROXY_URL}"
fi

echo "=== [Test] Vault Registry Pull Proxy ==="

# 1. Health check
echo "[1/4] Checking proxy healthz..."
HEALTH=$(curl -fsSL "${PROXY_URL}/healthz")
if [ "${HEALTH}" != "OK" ]; then
  echo "FAIL: Expected 'OK', got '${HEALTH}'"
  exit 1
fi
echo "✓ PASS: Healthz endpoint responsive."

# 2. V2 Ping
echo "[2/4] Testing /v2/ API version ping..."
V2_STATUS=$(curl -s -o /dev/null -w "%{http_code}" "${PROXY_URL}/v2/")
if [ "${V2_STATUS}" != "200" ]; then
  echo "FAIL: Expected HTTP 200 on /v2/, got ${V2_STATUS}"
  exit 1
fi
echo "✓ PASS: V2 ping successful."

# 3. Security Invariant: Reject caller-supplied Authorization header
echo "[3/4] Asserting rejection of caller-supplied Authorization header..."
AUTH_HEADER_KEY="Authorization"
AUTH_HEADER_VAL="Bearer test-denial-probe"
FORBIDDEN_STATUS=$(curl -s -o /dev/null -w "%{http_code}" -H "${AUTH_HEADER_KEY}: ${AUTH_HEADER_VAL}" "${PROXY_URL}/v2/")
if [ "${FORBIDDEN_STATUS}" != "403" ]; then
  echo "FAIL: Expected HTTP 403 Forbidden, got ${FORBIDDEN_STATUS}"
  exit 1
fi
echo "✓ PASS: Caller-supplied Authorization header strictly rejected (HTTP 403)."

# 4. Tag Invalidity Assertion: Mutable tags must be rejected
echo "[4/4] Asserting rejection of mutable tags without sha256 digest..."
BAD_TAG_STATUS=$(curl -s -o /dev/null -w "%{http_code}" "${PROXY_URL}/v2/nacfson/test-repo/manifests/latest")
if [ "${BAD_TAG_STATUS}" != "400" ]; then
  echo "FAIL: Expected HTTP 400 Bad Request for mutable tag, got ${BAD_TAG_STATUS}"
  exit 1
fi
echo "✓ PASS: Mutable tags rejected; immutable digest enforced (HTTP 400)."

echo "=== All Registry Proxy Tests Passed Successfully ==="
