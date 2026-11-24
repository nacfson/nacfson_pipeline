#!/usr/bin/env bash
set -euo pipefail

if [ "$(id -u)" -ne 0 ]; then
  echo "Error: run with sudo (must write 0400 /etc/nacfson-mcp/kubeconfig for _nacfson-mcp)." >&2
  exit 1
fi

usage() {
  cat >&2 <<'EOF'
Usage: sudo refresh-kubeconfig.sh <gke|local-k3s|ssh-host> [options]

Options:
  --service-account <name>    ServiceAccount in governance (default: operator-readonly)
  --duration <duration>       Token TTL (default: 24h)
  --server <https-url>        Override Kubernetes API server URL
  --admin-kubeconfig <path>   Explicit admin kubeconfig for gke target
  --context <name>            Explicit kubectl context for gke target
EOF
}

if [ $# -lt 1 ]; then
  usage
  exit 2
fi

TARGET="$1"
shift

SERVICE_ACCOUNT="operator-readonly"
DURATION="24h"
SERVER_OVERRIDE=""
ADMIN_KUBECONFIG="${KUBECONFIG:-}"
KCTX=""

while [ $# -gt 0 ]; do
  case "$1" in
    --service-account)
      if [ $# -lt 2 ]; then
        echo "Error: --service-account requires a value." >&2
        exit 2
      fi
      SERVICE_ACCOUNT="$2"
      shift 2
      ;;
    --duration)
      if [ $# -lt 2 ]; then
        echo "Error: --duration requires a value." >&2
        exit 2
      fi
      DURATION="$2"
      shift 2
      ;;
    --server)
      if [ $# -lt 2 ]; then
        echo "Error: --server requires a value." >&2
        exit 2
      fi
      SERVER_OVERRIDE="$2"
      shift 2
      ;;
    --admin-kubeconfig)
      if [ $# -lt 2 ]; then
        echo "Error: --admin-kubeconfig requires a value." >&2
        exit 2
      fi
      ADMIN_KUBECONFIG="$2"
      shift 2
      ;;
    --context)
      if [ $# -lt 2 ]; then
        echo "Error: --context requires a value." >&2
        exit 2
      fi
      KCTX="$2"
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

NAMESPACE="governance"
OUT_FILE="/etc/nacfson-mcp/kubeconfig"
TOKEN=""
CA_DATA=""
SERVER=""

KUBECTL_BIN=""
if command -v kubectl >/dev/null 2>&1; then
  KUBECTL_BIN="$(command -v kubectl)"
elif [ -x /usr/local/bin/kubectl ]; then
  KUBECTL_BIN="/usr/local/bin/kubectl"
elif [ -x /usr/bin/kubectl ]; then
  KUBECTL_BIN="/usr/bin/kubectl"
fi

if [ "$TARGET" = "gke" ]; then
  if [ -z "$KUBECTL_BIN" ]; then
    echo "Error: kubectl not found in PATH or standard locations for gke target." >&2
    exit 1
  fi
  if [ -z "$ADMIN_KUBECONFIG" ] && [ -n "${SUDO_USER:-}" ]; then
    SUDO_HOME="$(getent passwd "$SUDO_USER" | cut -d: -f6 || true)"
    if [ -n "$SUDO_HOME" ] && [ -f "${SUDO_HOME}/.kube/config" ]; then
      ADMIN_KUBECONFIG="${SUDO_HOME}/.kube/config"
    fi
  fi
  KARGS=()
  if [ -n "$ADMIN_KUBECONFIG" ]; then
    KARGS+=(--kubeconfig "$ADMIN_KUBECONFIG")
  fi
  if [ -n "$KCTX" ]; then
    KARGS+=(--context "$KCTX")
  fi
  KUBECTL_RUN=("$KUBECTL_BIN")
  if [ -n "${SUDO_USER:-}" ] && [ "$SUDO_USER" != "root" ]; then
    if [ -z "$ADMIN_KUBECONFIG" ] || sudo -u "$SUDO_USER" test -r "$ADMIN_KUBECONFIG"; then
      KUBECTL_RUN=(sudo -H -u "$SUDO_USER" "$KUBECTL_BIN")
    fi
  fi
  TOKEN="$("${KUBECTL_RUN[@]}" "${KARGS[@]}" -n "$NAMESPACE" create token "$SERVICE_ACCOUNT" --duration="$DURATION")"
  SERVER="$("${KUBECTL_RUN[@]}" "${KARGS[@]}" config view --raw --minify -o jsonpath='{.clusters[0].cluster.server}')"
  CA_DATA="$("${KUBECTL_RUN[@]}" "${KARGS[@]}" config view --raw --minify -o jsonpath='{.clusters[0].cluster.certificate-authority-data}')"
  if [ -z "$CA_DATA" ]; then
    CA_FILE="$("${KUBECTL_RUN[@]}" "${KARGS[@]}" config view --raw --minify -o jsonpath='{.clusters[0].cluster.certificate-authority}')"
    if [ -n "$CA_FILE" ] && [ -f "$CA_FILE" ]; then
      CA_DATA="$(base64 -w0 < "$CA_FILE")"
    fi
  fi
elif [ "$TARGET" = "local-k3s" ] && command -v k3s >/dev/null 2>&1 && [ -r /etc/rancher/k3s/k3s.yaml ]; then
  TOKEN="$(k3s kubectl -n "$NAMESPACE" create token "$SERVICE_ACCOUNT" --duration="$DURATION")"
  SERVER="$(k3s kubectl config view --raw --minify -o jsonpath='{.clusters[0].cluster.server}')"
  CA_DATA="$(k3s kubectl config view --raw --minify -o jsonpath='{.clusters[0].cluster.certificate-authority-data}')"
else
  SSH_RUN=(ssh "$TARGET")
  if [ -n "${SUDO_USER:-}" ]; then
    SSH_RUN=(sudo --preserve-env=SSH_AUTH_SOCK -H -u "$SUDO_USER" ssh "$TARGET")
  fi
  TOKEN="$("${SSH_RUN[@]}" sudo k3s kubectl -n "$NAMESPACE" create token "$SERVICE_ACCOUNT" --duration="$DURATION")"
  SERVER="$("${SSH_RUN[@]}" sudo k3s kubectl config view --raw --minify -o jsonpath='{.clusters[0].cluster.server}')"
  CA_DATA="$("${SSH_RUN[@]}" sudo k3s kubectl config view --raw --minify -o jsonpath='{.clusters[0].cluster.certificate-authority-data}')"
fi

if [ -n "$SERVER_OVERRIDE" ]; then
  SERVER="$SERVER_OVERRIDE"
fi

if [ -z "$TOKEN" ] || [ -z "$CA_DATA" ] || [ -z "$SERVER" ]; then
  echo "Error: failed to resolve token, CA data, or server URL for target '$TARGET'." >&2
  exit 1
fi

case "$SERVER" in
  https://*)
    ;;
  *)
    echo "Error: API server URL must start with https:// (got: $SERVER)." >&2
    exit 1
    ;;
esac

install -d -m 0750 -o root -g _nacfson-mcp /etc/nacfson-mcp
TMP_KCFG="$(mktemp /etc/nacfson-mcp/.kubeconfig.tmp.XXXXXX)"
TMP_RBAC_ERR="$(mktemp /tmp/nmcp-rbac.XXXXXX)"
trap 'rm -f "$TMP_KCFG" "$TMP_RBAC_ERR"' EXIT

cat <<EOF > "$TMP_KCFG"
apiVersion: v1
kind: Config
clusters:
- cluster:
    certificate-authority-data: ${CA_DATA}
    server: ${SERVER}
  name: nacfson-${TARGET}
contexts:
- context:
    cluster: nacfson-${TARGET}
    namespace: ${NAMESPACE}
    user: ${SERVICE_ACCOUNT}
  name: nacfson-${TARGET}
current-context: nacfson-${TARGET}
users:
- name: ${SERVICE_ACCOUNT}
  user:
    token: ${TOKEN}
EOF

chown _nacfson-mcp:_nacfson-mcp "$TMP_KCFG"
chmod 0400 "$TMP_KCFG"

CHECK_CMD=()
if [ -n "$KUBECTL_BIN" ]; then
  CHECK_CMD=("$KUBECTL_BIN")
elif command -v k3s >/dev/null 2>&1; then
  CHECK_CMD=(k3s kubectl)
else
  echo "Error: neither kubectl nor k3s is installed locally; cannot verify Secret RBAC permissions, refusing." >&2
  exit 1
fi

for verb in get list watch; do
  CAN_VERB="$(sudo -u _nacfson-mcp "${CHECK_CMD[@]}" --kubeconfig "$TMP_KCFG" auth can-i "$verb" secrets -A 2>/dev/null || true)"
  if [ "$CAN_VERB" != "no" ]; then
    echo "Error: RBAC check 'auth can-i ${verb} secrets -A' returned '${CAN_VERB:-error}' (expected 'no'); refusing." >&2
    exit 1
  fi
done

STATIC_NS="default kube-system kube-public governance platform vault identity database cert-manager observability oci-link flux-system proj-pn proj-health proj-blog"
DISCOVERED_NS="$(sudo -u _nacfson-mcp "${CHECK_CMD[@]}" --kubeconfig "$TMP_KCFG" get namespaces -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' 2>/dev/null || true)"
CHECK_NS="$(printf '%s\n' "$STATIC_NS" "$DISCOVERED_NS" | tr ' ' '\n' | grep -E '^[a-z0-9-]+$' | sort -u)"
NS_COUNT="$(printf '%s\n' "$CHECK_NS" | wc -l | tr -d ' ')"

pids=()
for ns in $CHECK_NS; do
  for verb in get list watch; do
    (
      res="$(sudo -u _nacfson-mcp "${CHECK_CMD[@]}" --kubeconfig "$TMP_KCFG" auth can-i "$verb" secrets -n "$ns" 2>/dev/null || true)"
      if [ "$res" != "no" ]; then
        printf '%s:%s:%s\n' "$ns" "$verb" "${res:-error}" >> "$TMP_RBAC_ERR"
      fi
    ) &
    pids+=("$!")
    if [ "${#pids[@]}" -ge 8 ]; then
      wait "${pids[@]}" || true
      pids=()
    fi
  done
done
if [ "${#pids[@]}" -gt 0 ]; then
  wait "${pids[@]}" || true
fi

if [ -s "$TMP_RBAC_ERR" ]; then
  echo "Error: ServiceAccount ${NAMESPACE}/${SERVICE_ACCOUNT} failed strict Secret RBAC verification (expected 'no' across all namespaces):" >&2
  cat "$TMP_RBAC_ERR" >&2
  exit 1
fi

CAN_PODS="$(sudo -u _nacfson-mcp "${CHECK_CMD[@]}" --kubeconfig "$TMP_KCFG" auth can-i get pods -A 2>/dev/null || true)"
echo "RBAC check: secrets (get/list/watch -A + ${NS_COUNT} namespaces) = no, get pods -A = ${CAN_PODS:-unknown}"

mv -f "$TMP_KCFG" "$OUT_FILE"

if systemctl is-active --quiet nacfson-mcp.service 2>/dev/null; then
  systemctl reload nacfson-mcp.service
  echo "Reloaded nacfson-mcp.service with fresh ${DURATION} token for ${NAMESPACE}/${SERVICE_ACCOUNT}."
elif systemctl is-enabled --quiet nacfson-mcp.service 2>/dev/null; then
  systemctl start nacfson-mcp.service
  echo "Started nacfson-mcp.service with fresh ${DURATION} token for ${NAMESPACE}/${SERVICE_ACCOUNT}."
else
  echo "Wrote ${OUT_FILE} (0400 _nacfson-mcp:_nacfson-mcp) for ${NAMESPACE}/${SERVICE_ACCOUNT}."
fi
