#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SOCK_PATH="/run/nacfson-mcp/mcp.sock"
KCFG_PATH="/etc/nacfson-mcp/kubeconfig"
UNIT_NAME="nacfson-mcp.service"
DROPIN_DIR="/run/systemd/system/nacfson-mcp.service.d"
DROPIN_FILE="${DROPIN_DIR}/ttl.conf"

usage() {
  cat >&2 <<'EOF'
Usage:
  mcp-session.sh status
  sudo mcp-session.sh start <gke|local-k3s|ssh-host> [options]
  sudo mcp-session.sh stop

Start options:
  --ttl <duration>            Session and token lifetime (10m..24h, matching ^[1-9][0-9]*[mh]$, default: 2h)
  --service-account <name>    ServiceAccount in governance (default: operator-readonly)
  --server <https-url>        Override Kubernetes API server URL
  --admin-kubeconfig <path>   Explicit admin kubeconfig for gke target
  --context <name>            Explicit kubectl context for gke target
EOF
}

validate_ttl() {
  local raw="$1"
  case "$raw" in
    *$'\n'*|*$'\r'*)
      echo "Error: invalid --ttl value." >&2
      return 1
      ;;
  esac
  if ! [[ "$raw" =~ ^[1-9][0-9]*[mh]$ ]]; then
    echo "Error: --ttl must match ^[1-9][0-9]*[mh]$ (e.g. 30m, 2h, range 10m..24h); got '$raw'." >&2
    return 1
  fi
  local unit="${raw: -1}"
  local num="${raw%[mh]}"
  if [ "$unit" = "m" ] && [ "$num" -lt 10 ]; then
    echo "Error: --ttl must be at least 10m (Kubernetes TokenRequest minimum): '$raw'." >&2
    return 1
  fi
  if [ "$unit" = "m" ] && [ "$num" -gt 1440 ]; then
    echo "Error: --ttl exceeds maximum 24h (1440m): '$raw'." >&2
    return 1
  fi
  if [ "$unit" = "h" ] && [ "$num" -gt 24 ]; then
    echo "Error: --ttl exceeds maximum 24h: '$raw'." >&2
    return 1
  fi
  return 0
}

cmd_status() {
  local bin_state="missing"
  local sock_state="absent"
  if [ -x /usr/local/bin/nacfson-mcp ]; then
    bin_state="installed"
  fi
  if [ -S "$SOCK_PATH" ]; then
    sock_state="listening (${SOCK_PATH})"
  fi
  echo "binary: /usr/local/bin/nacfson-mcp (${bin_state})"
  echo "socket: ${sock_state}"
  if command -v systemctl >/dev/null 2>&1; then
    local props
    props="$(systemctl show -p ActiveState -p SubState -p UnitFileState -p ExecMainStartTimestamp -p RuntimeMaxUSec "$UNIT_NAME" 2>/dev/null || true)"
    if [ -n "$props" ]; then
      echo "$props"
    else
      echo "service: ${UNIT_NAME} not loaded"
    fi
  else
    echo "service: systemctl unavailable"
  fi
  echo "start: sudo ./scripts/mcp/mcp-session.sh start <gke|local-k3s|ssh-host> [--ttl 2h]"
  echo "stop:  sudo ./scripts/mcp/mcp-session.sh stop"
}

cmd_start() {
  if [ $# -lt 1 ]; then
    usage
    exit 2
  fi
  local target="$1"
  shift

  local ttl="2h"
  local service_account="operator-readonly"
  local server_override=""
  local admin_kubeconfig=""
  local kctx=""

  while [ $# -gt 0 ]; do
    case "$1" in
      --ttl)
        if [ $# -lt 2 ]; then
          echo "Error: --ttl requires a value." >&2
          exit 2
        fi
        ttl="$2"
        shift 2
        ;;
      --service-account)
        if [ $# -lt 2 ]; then
          echo "Error: --service-account requires a value." >&2
          exit 2
        fi
        service_account="$2"
        shift 2
        ;;
      --server)
        if [ $# -lt 2 ]; then
          echo "Error: --server requires a value." >&2
          exit 2
        fi
        server_override="$2"
        shift 2
        ;;
      --admin-kubeconfig)
        if [ $# -lt 2 ]; then
          echo "Error: --admin-kubeconfig requires a value." >&2
          exit 2
        fi
        admin_kubeconfig="$2"
        shift 2
        ;;
      --context)
        if [ $# -lt 2 ]; then
          echo "Error: --context requires a value." >&2
          exit 2
        fi
        kctx="$2"
        shift 2
        ;;
      -h|--help)
        usage
        exit 0
        ;;
      *)
        echo "Unknown argument: $1" >&2
        usage
        exit 2
        ;;
    esac
  done

  if ! validate_ttl "$ttl"; then
    exit 2
  fi

  if [ "$(id -u)" -ne 0 ]; then
    echo "Error: 'start' requires sudo to write 0400 ${KCFG_PATH} and start ${UNIT_NAME}." >&2
    exit 1
  fi

  if [ ! -x /usr/local/bin/nacfson-mcp ] || [ ! -f "/etc/systemd/system/${UNIT_NAME}" ]; then
    echo "Error: daemon not installed yet. Run once: sudo ./scripts/mcp/install-daemon.sh" >&2
    exit 1
  fi

  if systemctl is-active --quiet "$UNIT_NAME" 2>/dev/null; then
    systemctl stop "$UNIT_NAME"
  fi

  install -d -m 0755 -o root -g root "$DROPIN_DIR"
  printf '[Service]\nRuntimeMaxSec=%s\n' "$ttl" > "$DROPIN_FILE"
  chmod 0644 "$DROPIN_FILE"
  systemctl daemon-reload

  local refresh_args=("$target" --duration "$ttl" --service-account "$service_account")
  if [ -n "$server_override" ]; then
    refresh_args+=(--server "$server_override")
  fi
  if [ -n "$admin_kubeconfig" ]; then
    refresh_args+=(--admin-kubeconfig "$admin_kubeconfig")
  fi
  if [ -n "$kctx" ]; then
    refresh_args+=(--context "$kctx")
  fi

  "${SCRIPT_DIR}/refresh-kubeconfig.sh" "${refresh_args[@]}"
  systemctl start "$UNIT_NAME"

  local i=0
  while [ "$i" -lt 50 ]; do
    if [ -S "$SOCK_PATH" ]; then
      echo "Temporary MCP session active (ttl=${ttl}, service_account=governance/${service_account})."
      echo "Connect with: /usr/local/bin/nacfson-mcp --connect ${SOCK_PATH}"
      echo "Stop early:   sudo ./scripts/mcp/mcp-session.sh stop"
      return 0
    fi
    sleep 0.1
    i=$((i + 1))
  done

  echo "Error: ${SOCK_PATH} did not appear within 5s. Check: journalctl -u ${UNIT_NAME} -n 20" >&2
  exit 1
}

cmd_stop() {
  if [ "$(id -u)" -ne 0 ]; then
    echo "Error: 'stop' requires sudo to stop ${UNIT_NAME} and remove ${KCFG_PATH}." >&2
    exit 1
  fi
  if command -v systemctl >/dev/null 2>&1; then
    systemctl stop "$UNIT_NAME" 2>/dev/null || true
  fi
  rm -f "$KCFG_PATH"
  rm -f "$DROPIN_FILE"
  rmdir "$DROPIN_DIR" 2>/dev/null || true
  if command -v systemctl >/dev/null 2>&1; then
    systemctl daemon-reload
  fi
  echo "Stopped ${UNIT_NAME} and removed temporary ${KCFG_PATH}."
}

if [ $# -lt 1 ]; then
  usage
  exit 2
fi

SUBCMD="$1"
shift

case "$SUBCMD" in
  status)
    cmd_status "$@"
    ;;
  start)
    cmd_start "$@"
    ;;
  stop)
    cmd_stop "$@"
    ;;
  -h|--help)
    usage
    exit 0
    ;;
  *)
    echo "Unknown subcommand: $SUBCMD" >&2
    usage
    exit 2
    ;;
esac
