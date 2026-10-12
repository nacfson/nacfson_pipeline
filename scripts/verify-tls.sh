#!/usr/bin/env bash
# ==============================================================================
# verify-tls.sh
# Verification Suite for Platform-Owned TLS Termination (Spec 008, US3).
# Inspects edge certificates in namespace 'platform' and tests external TLS.
# ==============================================================================

set -euo pipefail

usage() {
  cat << 'EOF'
Usage: verify-tls.sh [--external] [--help]

Options:
  --external    Test public HTTPS TLS handshakes for all platform hostnames.
  -h, --help    Show this help message and exit.

Environment:
  KUBECTL       Kubectl command to use (default: "kubectl", or e.g. "sudo k3s kubectl").
EOF
}

CHECK_EXTERNAL=false

while [[ $# -gt 0 ]]; do
  case "$1" in
    --external)
      CHECK_EXTERNAL=true
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "Unknown option: $1" >&2
      usage
      exit 2
      ;;
  esac
done

# Read KUBECTL into an array to handle multi-word values safely
read -r -a KUBECTL_CMD <<< "${KUBECTL:-kubectl}"

echo "================================================================================"
echo "Checking Edge TLS Certificates in namespace 'platform'..."
echo "================================================================================"

CERT_JSON=$("${KUBECTL_CMD[@]}" get certificate -n platform -o json)

# Process certificates with python3 for robust date math and JSON parsing
PYTHON_SCRIPT='
import sys
import json
from datetime import datetime, timezone, timedelta

raw = sys.stdin.read()
if not raw.strip():
    print("No certificates found in namespace platform.", file=sys.stderr)
    sys.exit(1)

try:
    data = json.loads(raw)
except Exception as e:
    print(f"Error parsing JSON: {e}", file=sys.stderr)
    sys.exit(1)

items = data.get("items", [])
if not items:
    print("No certificates found in namespace platform.", file=sys.stderr)
    sys.exit(1)

now = datetime.now(timezone.utc)
failures = []
hostnames = set()

print(f"{len(items)} certificate(s) found in namespace platform:\n")

for cert in items:
    meta = cert.get("metadata", {})
    name = meta.get("name", "<unnamed>")
    spec = cert.get("spec", {})
    status = cert.get("status", {})

    dns_names = spec.get("dnsNames", [])
    for d in dns_names:
        hostnames.add(d)

    conditions = status.get("conditions", [])
    ready_cond = next((c for c in conditions if c.get("type") == "Ready"), {})
    ready_status = ready_cond.get("status", "Unknown")
    ready_reason = ready_cond.get("reason", "")
    ready_msg = ready_cond.get("message", "")

    not_after_str = status.get("notAfter")
    renewal_time_str = status.get("renewalTime")
    last_failure_str = status.get("lastFailureTime")

    print(f"Certificate: {name}")
    print(f"  dnsNames:        {dns_names}")
    print(f"  Ready:           {ready_status} (reason: {ready_reason}, message: {ready_msg})")
    print(f"  notAfter:        {not_after_str}")
    print(f"  renewalTime:     {renewal_time_str}")
    print(f"  lastFailureTime: {last_failure_str}")

    # Check 1: Ready condition must be True
    if ready_status != "True":
        failures.append(f"{name}: Ready condition is {ready_status} ({ready_reason}: {ready_msg})")

    # Check 2: renewalTime not more than 1 hour in the past
    if renewal_time_str:
        try:
            clean_str = renewal_time_str.replace("Z", "+00:00")
            ren_dt = datetime.fromisoformat(clean_str)
            if now - ren_dt > timedelta(hours=1):
                failures.append(f"{name}: renewalTime ({renewal_time_str}) is overdue by more than 1 hour")
        except Exception as e:
            failures.append(f"{name}: cannot parse renewalTime {renewal_time_str}: {e}")

    # Check 3: notAfter must be at least 14 days in the future
    if not_after_str:
        try:
            clean_str = not_after_str.replace("Z", "+00:00")
            exp_dt = datetime.fromisoformat(clean_str)
            if exp_dt - now < timedelta(days=14):
                days_left = (exp_dt - now).total_seconds() / 86400.0
                failures.append(f"{name}: fewer than 14 days remaining ({days_left:.1f} days left, expires {not_after_str})")
        except Exception as e:
            failures.append(f"{name}: cannot parse notAfter {not_after_str}: {e}")
    else:
        failures.append(f"{name}: status.notAfter is not set")

    print("")

if failures:
    print("================================================================================", file=sys.stderr)
    print("TLS Verification FAILED:", file=sys.stderr)
    for f in failures:
        print(f"  - {f}", file=sys.stderr)
    print("================================================================================", file=sys.stderr)
    sys.exit(1)

print("All platform certificates are Ready and within validity bounds.")
'

# Run the python validation
if ! printf '%s\n' "$CERT_JSON" | python3 -c "$PYTHON_SCRIPT"; then
  exit 1
fi

if [[ "$CHECK_EXTERNAL" == "true" ]]; then
  echo ""
  echo "================================================================================"
  echo "Running External TLS Handshake Checks..."
  echo "================================================================================"

  HOSTNAMES=$(printf '%s\n' "$CERT_JSON" | python3 -c '
import sys, json
data = json.loads(sys.stdin.read())
hosts = []
for c in data.get("items", []):
    for d in (c.get("spec") or {}).get("dnsNames") or []:
        if d not in hosts:
            hosts.append(d)
print("\n".join(hosts))
')

  if [[ -z "$HOSTNAMES" ]]; then
    echo "Warning: No hostnames found in platform certificates to verify externally." >&2
  fi

  EXT_FAILURES=0
  while IFS= read -r host; do
    [[ -z "$host" ]] && continue
    echo "Checking external TLS for https://${host}:443..."
    # Connect with openssl s_client and verify return error
    OPENSSL_OUTPUT=""
    if ! OPENSSL_OUTPUT=$(echo "" | openssl s_client -connect "${host}:443" -servername "${host}" -verify_return_error 2>&1); then
      echo "  FAIL: openssl s_client failed for ${host}:" >&2
      while IFS= read -r line; do
        echo "    ${line}" >&2
      done <<< "${OPENSSL_OUTPUT}"
      EXT_FAILURES=$((EXT_FAILURES + 1))
      continue
    fi

    # Fail if issuer contains TRAEFIK DEFAULT CERT
    if echo "${OPENSSL_OUTPUT}" | grep -qi "TRAEFIK DEFAULT CERT"; then
      echo "  FAIL: Certificate issuer for ${host} is TRAEFIK DEFAULT CERT (fallback certificate)" >&2
      EXT_FAILURES=$((EXT_FAILURES + 1))
      continue
    fi

    ISSUER=$(echo "${OPENSSL_OUTPUT}" | grep -i "issuer=" | head -n 1 || true)
    echo "  PASS: ${host} verified successfully (${ISSUER})"
  done <<< "$HOSTNAMES"

  if [[ "$EXT_FAILURES" -gt 0 ]]; then
    echo "================================================================================" >&2
    echo "External TLS Verification FAILED ($EXT_FAILURES host(s) failed)" >&2
    echo "================================================================================" >&2
    exit 1
  fi
  echo "All external TLS endpoints passed verification."
fi

echo "================================================================================"
echo "TLS Verification PASSED"
echo "================================================================================"
