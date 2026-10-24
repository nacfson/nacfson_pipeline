#!/usr/bin/env bash
# Open Hubble UI and VictoriaLogs through one workstation session.
#
# SSH forwards only the VPS Kubernetes API. kubectl then port-forwards the
# ClusterIP UIs. Do not SSH-forward 12000 or 9428: nothing listens for those
# services on the VPS loopback, and those binds collide with kubectl.
#
# Usage:
#   ./scripts/open-observe.sh

set -euo pipefail

SSH_HOST="oracleCloud"
ENV_NAME="vps-k3s"
API_PORT="16443"
HUBBLE_PORT="12000"
LOGS_PORT="9428"
SCRIPT_PATH="$(readlink -f "${BASH_SOURCE[0]}")"
ROOT="$(cd "$(dirname "$SCRIPT_PATH")/.." && pwd)"
KUBECONFIG_FILE="${HOME}/.kube/nacfson-${ENV_NAME}.yaml"

SSH_PID=""
HUBBLE_PID=""
LOGS_PID=""

usage() {
  cat <<EOF
Usage: $0

Opens ssh ${SSH_HOST} to 127.0.0.1:${API_PORT} -> VPS 127.0.0.1:6443, writes a
24-hour operator-readonly kubeconfig, then port-forwards:

  Hubble UI      http://127.0.0.1:${HUBBLE_PORT}
  VictoriaLogs   http://127.0.0.1:${LOGS_PORT}/select/vmui/

Ctrl+C closes the SSH forward and both UI forwards.
EOF
}

cleanup() {
  local pid
  for pid in "$LOGS_PID" "$HUBBLE_PID" "$SSH_PID"; do
    if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
      kill "$pid" 2>/dev/null || true
      wait "$pid" 2>/dev/null || true
    fi
  done
}
trap cleanup EXIT INT TERM

port_busy() {
  local port="$1"
  ss -ltn | awk -v p=":${port}$" '$4 ~ p { found=1 } END { exit found ? 0 : 1 }'
}

require_free() {
  local port="$1"
  local name="$2"
  if port_busy "$port"; then
    echo "Error: ${name} port ${port} is already in use." >&2
    exit 1
  fi
}

wait_for_listen() {
  local port="$1"
  local pid="$2"
  local label="$3"
  local _
  for _ in $(seq 1 30); do
    if ! kill -0 "$pid" 2>/dev/null; then
      echo "Error: ${label} exited before port ${port} was listening." >&2
      exit 1
    fi
    if port_busy "$port"; then
      return 0
    fi
    sleep 0.2
  done
  echo "Error: ${label} did not bind port ${port}." >&2
  exit 1
}

http_ok() {
  local url="$1"
  local code
  code="$(curl -sS -o /dev/null -w '%{http_code}' --max-time 3 "$url" || true)"
  case "$code" in
    2??|3??) return 0 ;;
    *) return 1 ;;
  esac
}

wait_for_http() {
  local url="$1"
  local _
  for _ in $(seq 1 25); do
    if http_ok "$url"; then
      return 0
    fi
    sleep 0.2
  done
  echo "Error: ${url} did not respond." >&2
  exit 1
}

point_kubeconfig_at_tunnel() {
  local tmp
  tmp="$(mktemp)"
  if ! awk -v port="$API_PORT" '
    $1 == "server:" && $2 == "https://127.0.0.1:6443" {
      print "    server: https://127.0.0.1:" port
      found = 1
      next
    }
    { print }
    END { if (!found) exit 2 }
  ' "$KUBECONFIG_FILE" >"$tmp"; then
    rm -f "$tmp"
    echo "Error: kubeconfig server line was not https://127.0.0.1:6443." >&2
    exit 1
  fi
  mv "$tmp" "$KUBECONFIG_FILE"
  chmod 600 "$KUBECONFIG_FILE"
}

if [ "${1:-}" = "-h" ] || [ "${1:-}" = "--help" ]; then
  usage
  exit 0
fi
if [ $# -gt 0 ]; then
  echo "Error: unknown argument '$1'." >&2
  usage >&2
  exit 2
fi

if ! command -v kubectl >/dev/null 2>&1; then
  echo "Error: kubectl is required but not found in PATH." >&2
  exit 1
fi
if ! command -v ssh >/dev/null 2>&1; then
  echo "Error: ssh is required but not found in PATH." >&2
  exit 1
fi
if ! command -v curl >/dev/null 2>&1; then
  echo "Error: curl is required but not found in PATH." >&2
  exit 1
fi

require_free "$API_PORT" "API tunnel"
require_free "$HUBBLE_PORT" "Hubble"
require_free "$LOGS_PORT" "VictoriaLogs"

echo "Opening ssh ${SSH_HOST} API forward 127.0.0.1:${API_PORT} -> 127.0.0.1:6443"
ssh -o BatchMode=yes -o ExitOnForwardFailure=yes -o ConnectTimeout=15 -N \
  -L "127.0.0.1:${API_PORT}:127.0.0.1:6443" "$SSH_HOST" &
SSH_PID=$!
wait_for_listen "$API_PORT" "$SSH_PID" "ssh ${SSH_HOST}"

"$ROOT/scripts/operator-kubeconfig.sh" "$SSH_HOST" "$ENV_NAME" >/dev/null
point_kubeconfig_at_tunnel
export KUBECONFIG="$KUBECONFIG_FILE"

echo "Checking Hubble UI and VictoriaLogs"
if ! kubectl -n kube-system rollout status deployment/hubble-ui --timeout=60s >/dev/null; then
  echo "Error: deployment/hubble-ui is not ready." >&2
  exit 1
fi
if ! kubectl -n observability rollout status statefulset/victorialogs --timeout=60s >/dev/null; then
  echo "Error: statefulset/victorialogs is not ready." >&2
  exit 1
fi

kubectl -n kube-system port-forward --address 127.0.0.1 svc/hubble-ui "${HUBBLE_PORT}:80" >/dev/null &
HUBBLE_PID=$!
kubectl -n observability port-forward --address 127.0.0.1 svc/victorialogs "${LOGS_PORT}:9428" >/dev/null &
LOGS_PID=$!
wait_for_listen "$HUBBLE_PORT" "$HUBBLE_PID" "Hubble port-forward"
wait_for_listen "$LOGS_PORT" "$LOGS_PID" "VictoriaLogs port-forward"
wait_for_http "http://127.0.0.1:${HUBBLE_PORT}/"
wait_for_http "http://127.0.0.1:${LOGS_PORT}/select/vmui/"

echo "=================================================================="
echo "Hubble UI:     http://127.0.0.1:${HUBBLE_PORT}"
echo "VictoriaLogs:  http://127.0.0.1:${LOGS_PORT}/select/vmui/"
echo "LogsQL:        {project=\"proj-pn\"}"
echo "Press Ctrl+C to close ssh ${SSH_HOST} and both UI forwards."
echo "=================================================================="

while true; do
  if ! kill -0 "$SSH_PID" 2>/dev/null \
    || ! kill -0 "$HUBBLE_PID" 2>/dev/null \
    || ! kill -0 "$LOGS_PID" 2>/dev/null; then
    echo "Error: a tunnel exited." >&2
    exit 1
  fi
  wait -n
  echo "Error: a tunnel exited." >&2
  exit 1
done
