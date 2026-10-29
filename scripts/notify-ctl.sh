#!/usr/bin/env bash
# ==============================================================================
# Notify Gateway (vault-notify-proxy) Operator CLI
#
# Seeds the Discord webhook URLs the notify proxy posts with, and reports which
# channels are configured. The proxy's admin listener binds to the pod's own
# loopback, so this reaches it through a port-forward and never exposes it to
# the cluster. The URL is read with `read -rs`, so it never enters argv, the
# shell history, or the process table.
# ==============================================================================

set -euo pipefail

VAULT_NS="vault"
NOTIFY_DEPLOY="deploy/vault-notify-proxy"
LOCAL_PORT="${NOTIFY_GATEWAY_LOCAL_PORT:-18082}"
KUBE_EXEC="${KUBE_EXEC:-kubectl}"
REMOTE_SSH=0

if ! ${KUBE_EXEC} --request-timeout=2s get ns "${VAULT_NS}" >/dev/null 2>&1; then
  # shellcheck disable=SC2029
  if ssh -o ConnectTimeout=5 oracleCloud "sudo k3s kubectl get ns ${VAULT_NS}" >/dev/null 2>&1; then
    KUBE_EXEC="ssh oracleCloud sudo k3s kubectl"
    REMOTE_SSH=1
  fi
fi

usage() {
  cat <<EOF
Usage: $0 <command> [args]

Commands:
  status                            Show which channels have a webhook configured, and send counters
  set-webhook <channel>             Prompt silently for a Discord webhook URL and store it in OpenBao
  self-test                         Trigger the proxy's /startupz vulnerability self-audit
EOF
  exit 1
}

if [ $# -lt 1 ]; then
  usage
fi

CMD="$1"
shift

with_loopback_tunnel() {
  local target_port="$1"
  local method="$2"
  local path="$3"
  local has_stdin_body="${4:-0}"

  local pf_log
  pf_log="$(mktemp /tmp/notify-gw-pf-XXXXXX.log)"
  if [ "${REMOTE_SSH}" -eq 1 ]; then
    # shellcheck disable=SC2029
    ssh -n -L "127.0.0.1:${LOCAL_PORT}:127.0.0.1:${LOCAL_PORT}" oracleCloud \
      "sudo k3s kubectl -n ${VAULT_NS} port-forward --address 127.0.0.1 ${NOTIFY_DEPLOY} ${LOCAL_PORT}:${target_port}" </dev/null >"${pf_log}" 2>&1 &
  else
    ${KUBE_EXEC} -n "${VAULT_NS}" port-forward --address 127.0.0.1 "${NOTIFY_DEPLOY}" "${LOCAL_PORT}:${target_port}" </dev/null >"${pf_log}" 2>&1 &
  fi
  local pf_pid=$!

  local ready=0
  for _ in $(seq 1 25); do
    if curl -fsS "http://127.0.0.1:${LOCAL_PORT}/healthz" >/dev/null 2>&1 \
      || curl -fsS "http://127.0.0.1:${LOCAL_PORT}/admin/status" >/dev/null 2>&1; then
      ready=1
      break
    fi
    sleep 0.2
  done

  if [ "${ready}" -ne 1 ]; then
    echo "Error: failed to establish loopback tunnel to ${NOTIFY_DEPLOY} on port ${target_port}." >&2
    cat "${pf_log}" >&2 || true
    kill "${pf_pid}" >/dev/null 2>&1 || true
    rm -f "${pf_log}"
    return 1
  fi

  local rc=0
  if [ "${has_stdin_body}" = "1" ]; then
    curl -fsS -X "${method}" -H "Content-Type: application/json" --data-binary @- \
      "http://127.0.0.1:${LOCAL_PORT}${path}" || rc=$?
  else
    curl -fsS -X "${method}" "http://127.0.0.1:${LOCAL_PORT}${path}" || rc=$?
  fi
  echo ""
  kill "${pf_pid}" >/dev/null 2>&1 || true
  rm -f "${pf_log}"
  return "${rc}"
}

case "${CMD}" in
  status)
    with_loopback_tunnel 8081 GET "/admin/status"
    ;;
  set-webhook)
    CHANNEL="${1:-}"
    [ -n "${CHANNEL}" ] || { echo "Error: missing <channel> (e.g. errors, ai-debug)" >&2; exit 1; }
    read -r -s -p "Enter Discord webhook URL for '${CHANNEL}' (input hidden): " WEBHOOK_URL
    echo ""
    if [ -z "${WEBHOOK_URL}" ]; then
      echo "Error: webhook URL cannot be empty." >&2
      exit 1
    fi
    case "${WEBHOOK_URL}" in
      https://discord.com/api/webhooks/*) ;;
      *)
        echo "Error: the URL must start with https://discord.com/api/webhooks/" >&2
        unset WEBHOOK_URL
        exit 1
        ;;
    esac
    # The URL travels over the loopback tunnel on stdin, never in argv, so it
    # is absent from ps, the shell history, and the port-forward log.
    printf '{"url":"%s"}' "${WEBHOOK_URL}" | with_loopback_tunnel 8081 POST "/admin/set-webhook?channel=${CHANNEL}" 1
    rc=$?
    unset WEBHOOK_URL
    if [ "${rc}" -eq 0 ]; then
      echo "SUCCESS: webhook for '${CHANNEL}' stored in OpenBao (kv/notify/${CHANNEL})."
    fi
    exit "${rc}"
    ;;
  self-test)
    with_loopback_tunnel 8080 GET "/startupz"
    ;;
  *)
    usage
    ;;
esac
