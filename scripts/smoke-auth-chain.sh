#!/usr/bin/env bash
# Public auth-chain smoke test (spec 007 amendment 2026-10-13).
# Covers what kubelet probes cannot see: Traefik routes on the auth host, gateway reachability and
# return-host config, the public /api route and the 401 shape the SPA depends on.
# Note: Traefik sends /api straight to pn-backend, so the FE nginx /api/ proxy is covered by the FE
# startupProbe (/api/auth/language-options), not by this script.
# Usage: scripts/smoke-auth-chain.sh <pn-url> <auth-url>
#   e.g. scripts/smoke-auth-chain.sh https://pn.nacfson.cloud https://auth.nacfson.cloud
set -uo pipefail

PN="${1:-}"
AUTH="${2:-}"
if [ -z "$PN" ] || [ -z "$AUTH" ]; then
  echo "Usage: $0 <pn-url> <auth-url>" >&2
  exit 2
fi
PN="${PN%/}"
AUTH="${AUTH%/}"

FAILS=0
TMP="$(mktemp)"
trap 'rm -f "$TMP"' EXIT

# check <label> <expected-status> <url> [body-or-header-substring]
check() {
  local label="$1" want="$2" url="$3" needle="${4:-}"
  local code
  code="$(curl -sS -m 10 -o "$TMP.body" -D "$TMP" -w '%{http_code}' "$url" 2>/dev/null)" || true
  [ -n "$code" ] || code=000
  local ok=1
  [ "$code" = "$want" ] || ok=0
  if [ "$ok" = 1 ] && [ -n "$needle" ]; then
    if ! grep -qiF -- "$needle" "$TMP" "$TMP.body" 2>/dev/null; then ok=0; fi
  fi
  if [ "$ok" = 1 ]; then
    echo "PASS  $label (HTTP $code)"
  else
    local hint=""
    [ -z "$needle" ] || hint=" containing '$needle'"
    echo "FAIL  $label: want HTTP ${want}${hint}, got HTTP $code  [$url]"
    FAILS=$((FAILS + 1))
  fi
  rm -f "$TMP.body"
}

check "pn frontend serves the SPA"                         200 "$PN/"
check "pn /api Traefik route reaches backend (public route)" 200 "$PN/api/auth/language-options"
check "pn /api/auth/me answers 401 JSON (signed-out)"      401 "$PN/api/auth/me" '"error"'
check "auth /oauth/start redirects to Keycloak login"      302 "$AUTH/oauth/start?return=$PN/" '/protocol/openid-connect/auth'
check "auth /oauth/start without return is rejected by gateway" 400 "$AUTH/oauth/start"
check "auth /auth/logout reaches gateway (GET not allowed)" 405 "$AUTH/auth/logout"

if [ "$FAILS" -gt 0 ]; then
  echo "$FAILS check(s) failed" >&2
  exit 1
fi
echo "All auth-chain checks passed"
