#!/usr/bin/env bash
set -euo pipefail

if [ "$(id -u)" -ne 0 ]; then
  echo "Error: run with sudo (must create system user _nacfson-mcp and install systemd unit)." >&2
  exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
TARGET_USER="${SUDO_USER:-}"
ENABLE_ON_BOOT=0

while [ $# -gt 0 ]; do
  case "$1" in
    --user)
      if [ $# -lt 2 ]; then
        echo "Error: --user requires a value." >&2
        exit 2
      fi
      TARGET_USER="$2"
      shift 2
      ;;
    --enable)
      ENABLE_ON_BOOT=1
      shift
      ;;
    *)
      echo "Usage: sudo $0 [--user <workstation-user>] [--enable]" >&2
      exit 2
      ;;
  esac
done

GO_BIN=""
if command -v go >/dev/null 2>&1; then
  GO_BIN="$(command -v go)"
elif [ -x /usr/local/go/bin/go ]; then
  GO_BIN="/usr/local/go/bin/go"
fi

if [ -z "$GO_BIN" ]; then
  echo "Error: Go toolchain not found in PATH or /usr/local/go/bin/go." >&2
  exit 1
fi

if ! getent group nacfson-mcp >/dev/null 2>&1; then
  groupadd --system nacfson-mcp
fi

if ! getent group _nacfson-mcp >/dev/null 2>&1; then
  groupadd --system _nacfson-mcp
fi

if ! id -u _nacfson-mcp >/dev/null 2>&1; then
  useradd --system --gid _nacfson-mcp --no-create-home --shell /usr/sbin/nologin _nacfson-mcp
fi

if [ -n "$TARGET_USER" ] && [ "$TARGET_USER" != "root" ] && [ "$TARGET_USER" != "_nacfson-mcp" ]; then
  if id -u "$TARGET_USER" >/dev/null 2>&1; then
    usermod -aG nacfson-mcp "$TARGET_USER"
  else
    echo "Warning: user '$TARGET_USER' does not exist; skipped group membership." >&2
  fi
fi

install -d -m 0750 -o root -g _nacfson-mcp /etc/nacfson-mcp

TMP_BIN="$(mktemp)"
trap 'rm -f "$TMP_BIN"' EXIT
(
  cd "${REPO_ROOT}/services/debug-mcp"
  CGO_ENABLED=0 "$GO_BIN" build -buildvcs=false -trimpath -o "$TMP_BIN" ./cmd/nacfson-mcp
)
install -m 0755 -o root -g root "$TMP_BIN" /usr/local/bin/nacfson-mcp

install -m 0644 -o root -g root "${REPO_ROOT}/infra/workstation/nacfson-mcp.service" /etc/systemd/system/nacfson-mcp.service
systemctl daemon-reload

if [ "$ENABLE_ON_BOOT" -eq 1 ]; then
  systemctl enable nacfson-mcp.service
  if [ -f /etc/nacfson-mcp/kubeconfig ]; then
    systemctl restart nacfson-mcp.service
    echo "Installed /usr/local/bin/nacfson-mcp and restarted enabled nacfson-mcp.service."
  else
    echo "Installed /usr/local/bin/nacfson-mcp and enabled nacfson-mcp.service."
    echo "Seed /etc/nacfson-mcp/kubeconfig with: sudo ./scripts/mcp/refresh-kubeconfig.sh <local-k3s|gke|user@host>"
  fi
else
  systemctl disable nacfson-mcp.service >/dev/null 2>&1 || true
  if systemctl is-active --quiet nacfson-mcp.service 2>/dev/null; then
    systemctl stop nacfson-mcp.service
  fi
  echo "Installed /usr/local/bin/nacfson-mcp and nacfson-mcp.service (disabled at boot by default)."
  echo "Start a temporary debug session with:"
  echo "  sudo ./scripts/mcp/mcp-session.sh start <gke|local-k3s|user@host> --ttl 2h"
fi

if [ -n "$TARGET_USER" ] && id -u "$TARGET_USER" >/dev/null 2>&1; then
  if sudo -l -U "$TARGET_USER" 2>/dev/null | grep -Eq 'NOPASSWD:.*ALL'; then
    echo "[WARN] User $TARGET_USER has NOPASSWD: ALL in sudoers; agent shells could escalate to read /etc/nacfson-mcp/kubeconfig." >&2
  fi
  TARGET_HOME="$(getent passwd "$TARGET_USER" | cut -d: -f6 || true)"
  if [ -n "$TARGET_HOME" ] && [ -e "${TARGET_HOME}/.kube/config" ]; then
    echo "[WARN] ${TARGET_HOME}/.kube/config exists; remove or move admin kubeconfigs out of default ambient paths." >&2
  fi
fi
