#!/usr/bin/env bash
# Private loopback tunnel and live verification for VictoriaLogs.
#
# Usage:
#   ./scripts/open-victorialogs-ui.sh [--port <local-port>]
#   ./scripts/open-victorialogs-ui.sh --verify

set -euo pipefail

PORT="9428"
MODE="tunnel"
NS="observability"

usage() {
  cat <<EOF
Usage: $0 [--port <local-port>] [--verify]

Options:
  --port <port>   Local loopback port to bind on 127.0.0.1 (default: 9428)
  --verify        Run non-interactive live VictoriaLogs verification
  -h, --help      Show this help message
EOF
}

# Fluent Bit 4.2.8 prefixes every line with [YYYY/MM/DD HH:MM:SS.<ns>].
# Drop that prefix so the same [error] text counts as a repeat. One unique
# error still passes. Unit tests source this file with OPEN_VICTORIALOGS_LIB=1.
repeated_fluentbit_errors() {
  grep -F '[error]' \
    | sed -E 's/^\[[0-9]{4}\/[0-9]{2}\/[0-9]{2} [0-9]{2}:[0-9]{2}:[0-9]{2}\.[0-9]+\] //' \
    | sort \
    | uniq -d || true
}

if [ "${OPEN_VICTORIALOGS_LIB:-0}" = "1" ]; then
  return 0
fi

while [ $# -gt 0 ]; do
  case "$1" in
    --port)
      if [ $# -lt 2 ]; then
        echo "Error: --port requires a port number" >&2
        exit 2
      fi
      PORT="$2"
      shift 2
      ;;
    --verify)
      MODE="verify"
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "Error: Unknown argument '$1'" >&2
      usage >&2
      exit 2
      ;;
  esac
done

if ! command -v kubectl >/dev/null 2>&1; then
  echo "Error: kubectl is required but not found in PATH." >&2
  exit 1
fi

run_verify() {
  echo "=== [1/5] Verifying Fluent Bit and VictoriaLogs rollout ==="
  if ! kubectl -n "${NS}" rollout status daemonset/fluent-bit --timeout=120s; then
    echo "FAIL: daemonset/fluent-bit is not ready" >&2
    exit 1
  fi
  if ! kubectl -n "${NS}" rollout status statefulset/victorialogs --timeout=120s; then
    echo "FAIL: statefulset/victorialogs is not ready" >&2
    exit 1
  fi
  echo "  ✓ daemonset/fluent-bit and statefulset/victorialogs are ready"

  echo "=== [2/5] Verifying ClusterIP services and zero public IngressRoutes ==="
  SVC_TYPES="$(kubectl -n "${NS}" get svc -o jsonpath='{range .items[*]}{.metadata.name}={.spec.type}{"\n"}{end}')"
  if [ -z "${SVC_TYPES}" ]; then
    echo "FAIL: no Services found in ${NS}" >&2
    exit 1
  fi
  while IFS= read -r row; do
    [ -z "${row}" ] && continue
    svc_type="${row#*=}"
    if [ "${svc_type}" != "ClusterIP" ]; then
      echo "FAIL: expected ClusterIP services in ${NS}, got ${row}" >&2
      exit 1
    fi
  done <<EOF
${SVC_TYPES}
EOF
  INGRESS_ROUTES="$(kubectl get ingressroute -A -o jsonpath='{range .items[*]}{.metadata.namespace}/{.metadata.name}:{.spec.routes[*].services[*].name}{"\n"}{end}' 2>/dev/null || true)"
  if printf '%s\n' "${INGRESS_ROUTES}" | grep -Eq 'victorialogs|fluent-bit'; then
    echo "FAIL: Found IngressRoute referencing victorialogs or fluent-bit:" >&2
    printf '%s\n' "${INGRESS_ROUTES}" | grep -E 'victorialogs|fluent-bit' >&2
    exit 1
  fi
  echo "  ✓ observability Services are ClusterIP and no IngressRoute references victorialogs or fluent-bit"

  echo "=== [3/5] Verifying HTTP 200 at /select/vmui/ over 127.0.0.1:${PORT} ==="
  PF_LOG="$(mktemp /tmp/victorialogs-ui-pf-XXXXXX.log)"
  kubectl -n "${NS}" port-forward --address 127.0.0.1 svc/victorialogs "${PORT}:9428" >"${PF_LOG}" 2>&1 &
  PF_PID=$!
  # Invoked only by the EXIT trap below. ShellCheck does not treat trap
  # callbacks as calls (https://www.shellcheck.net/wiki/SC2329).
  # shellcheck disable=SC2329
  cleanup_pf() {
    kill "${PF_PID}" >/dev/null 2>&1 || true
    rm -f "${PF_LOG}"
  }
  trap cleanup_pf EXIT

  HTTP_OK=0
  for _ in $(seq 1 15); do
    if curl -fsS --max-time 3 "http://127.0.0.1:${PORT}/select/vmui/" >/dev/null 2>&1; then
      HTTP_OK=1
      break
    fi
    sleep 1
  done
  if [ "${HTTP_OK}" -ne 1 ]; then
    echo "FAIL: VictoriaLogs did not respond with HTTP 200 on http://127.0.0.1:${PORT}/select/vmui/" >&2
    cat "${PF_LOG}" >&2 || true
    exit 1
  fi
  echo "  ✓ tunnel served HTTP 200 at http://127.0.0.1:${PORT}/select/vmui/"

  echo "=== [4/5] Verifying LogsQL query returns only proj-* lines (empty body passes) ==="
  if ! QUERY_BODY="$(curl -fsS --max-time 15 -X POST \
    --data-urlencode 'query=*' \
    --data-urlencode 'limit=50' \
    "http://127.0.0.1:${PORT}/select/logsql/query")"; then
    echo "FAIL: POST /select/logsql/query failed" >&2
    exit 1
  fi
  if [ -n "${QUERY_BODY}" ]; then
    while IFS= read -r line || [ -n "${line}" ]; do
      [ -z "${line}" ] && continue
      case "${line}" in
        *'"project":"proj-'*) ;;
        *)
          echo "FAIL: query line missing \"project\":\"proj- : ${line}" >&2
          exit 1
          ;;
      esac
    done <<EOF
${QUERY_BODY}
EOF
  fi
  echo "  ✓ POST /select/logsql/query passed (empty or every line has \"project\":\"proj-)"

  echo "=== [5/5] Verifying Fluent Bit has no repeated [error] lines in the last 5 minutes ==="
  if ! FB_LOGS="$(kubectl -n "${NS}" logs daemonset/fluent-bit --since=5m 2>&1)"; then
    echo "FAIL: could not read Fluent Bit logs from the last 5 minutes" >&2
    printf '%s\n' "${FB_LOGS}" >&2
    exit 1
  fi
  REPEATED="$(printf '%s\n' "${FB_LOGS}" | repeated_fluentbit_errors)"
  if [ -n "${REPEATED}" ]; then
    echo "FAIL: Fluent Bit repeated [error] lines in the last 5 minutes:" >&2
    printf '%s\n' "${REPEATED}" >&2
    exit 1
  fi
  echo "  ✓ Fluent Bit logs have no repeated [error] lines in the last 5 minutes"
  echo "ALL CHECKS PASSED."
}

if [ "${MODE}" = "verify" ]; then
  run_verify
  exit 0
fi

echo "Verifying Fluent Bit and VictoriaLogs readiness in ${NS}..."
kubectl -n "${NS}" rollout status daemonset/fluent-bit --timeout=60s >/dev/null
kubectl -n "${NS}" rollout status statefulset/victorialogs --timeout=60s >/dev/null

echo "=================================================================="
echo "VictoriaLogs Private Loopback Tunnel"
echo "  Local URL:  http://127.0.0.1:${PORT}/select/vmui/"
echo "  Remote SSH: ssh -L ${PORT}:127.0.0.1:${PORT} <vps-host>"
echo "Press Ctrl+C to close the tunnel."
echo "=================================================================="

exec kubectl -n "${NS}" port-forward --address 127.0.0.1 svc/victorialogs "${PORT}:9428"
