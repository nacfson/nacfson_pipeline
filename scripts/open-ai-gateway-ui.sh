#!/usr/bin/env bash
# ==============================================================================
# Private Loopback Tunnel & Verification for Internal AI Gateway UI (127.0.0.1:18081)
# Matches scripts/open-hubble-ui.sh & scripts/open-victorialogs-ui.sh
# ==============================================================================

set -euo pipefail

PORT="18081"
MODE="tunnel"
OPEN_BROWSER=1

usage() {
  cat <<EOF
Usage: $0 [--port <local-port>] [--verify] [--no-browser]

Options:
  --port <port>   Local loopback port on 127.0.0.1 (default: 18081)
  --verify        Verify loopback dashboard (:8081) and 5-point /startupz self-audit (:8080)
  --no-browser    Start tunnel without launching browser
  -h, --help      Show this help message
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --port)
      PORT="${2:-}"
      shift 2
      ;;
    --verify)
      MODE="verify"
      shift
      ;;
    --no-browser)
      OPEN_BROWSER=0
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

KUBE_EXEC="${KUBE_EXEC:-kubectl}"
REMOTE_SSH=0
if ! ${KUBE_EXEC} --request-timeout=2s get ns vault >/dev/null 2>&1; then
  # shellcheck disable=SC2029
  if ssh -o ConnectTimeout=5 oracleCloud "sudo k3s kubectl get ns vault" >/dev/null 2>&1; then
    KUBE_EXEC="ssh oracleCloud sudo k3s kubectl"
    REMOTE_SSH=1
  fi
fi

if [ "${MODE}" = "verify" ]; then
  echo "=== [1/2] Verifying vault-ai-proxy 5-point /startupz vulnerability self-audit ==="
  ./scripts/ai-gateway-ctl.sh self-test
  echo "=== [2/2] Verifying vault-ai-proxy loopback /admin/status ==="
  ./scripts/ai-gateway-ctl.sh status
  echo "  ✓ vault-ai-proxy loopback Kill Switch & /startupz self-audit verified"
  exit 0
fi

URL="http://127.0.0.1:${PORT}/"
echo "Starting loopback tunnel ${URL} -> vault/deploy/vault-ai-proxy:8081 ..."
if [ "${OPEN_BROWSER}" -eq 1 ] && command -v xdg-open >/dev/null 2>&1; then
  (sleep 1.2 && xdg-open "${URL}" >/dev/null 2>&1 || true) &
fi
if [ "${REMOTE_SSH}" -eq 1 ]; then
  # shellcheck disable=SC2029
  exec ssh -L "127.0.0.1:${PORT}:127.0.0.1:${PORT}" oracleCloud \
    "sudo k3s kubectl -n vault port-forward --address 127.0.0.1 deploy/vault-ai-proxy ${PORT}:8081"
fi
exec ${KUBE_EXEC} -n vault port-forward --address 127.0.0.1 deploy/vault-ai-proxy "${PORT}:8081"
