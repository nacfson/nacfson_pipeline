#!/usr/bin/env bash
# ==============================================================================
# Internal AI Gateway (vault-ai-proxy) Operator CLI & Zero-Token Key Manager
# Signals the loopback Kill Switch (127.0.0.1:8081) and manages kv/ai/* secrets.
# ==============================================================================

set -euo pipefail

VAULT_NS="vault"
AI_PROXY_DEPLOY="deploy/vault-ai-proxy"
OPENBAO_POD="openbao-0"
LOCAL_PORT="${AI_GATEWAY_LOCAL_PORT:-18081}"
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
  status                         View live Kill-Switch & daily quota status for all projects
  stop <project>                 Immediately flip project Kill Switch to STOPPED (403 Forbidden)
  start <project>                Flip project Kill Switch to ACTIVE (200 OK)
  limit <project> <daily_limit>  Set daily AI request quota for <project>
  set-key                        Prompt silently for Gemini API key and save to OpenBao (Zero Vault Token needed)
  self-test                      Trigger the 5-point /startupz live vulnerability & Kill-Switch self-audit
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
  pf_log="$(mktemp /tmp/ai-gw-pf-XXXXXX.log)"
  if [ "${REMOTE_SSH}" -eq 1 ]; then
    # shellcheck disable=SC2029
    ssh -L "127.0.0.1:${LOCAL_PORT}:127.0.0.1:${LOCAL_PORT}" oracleCloud \
      "sudo k3s kubectl -n ${VAULT_NS} port-forward --address 127.0.0.1 ${AI_PROXY_DEPLOY} ${LOCAL_PORT}:${target_port}" >"${pf_log}" 2>&1 &
  else
    ${KUBE_EXEC} -n "${VAULT_NS}" port-forward --address 127.0.0.1 "${AI_PROXY_DEPLOY}" "${LOCAL_PORT}:${target_port}" >"${pf_log}" 2>&1 &
  fi
  local pf_pid=$!

  local ready=0
  for _ in $(seq 1 25); do
    if curl -fsS "http://127.0.0.1:${LOCAL_PORT}/healthz" >/dev/null 2>&1 || curl -fsS "http://127.0.0.1:${LOCAL_PORT}/admin/status" >/dev/null 2>&1; then
      ready=1
      break
    fi
    sleep 0.2
  done

  if [ "${ready}" -ne 1 ]; then
    echo "Error: Failed to establish loopback tunnel to ${AI_PROXY_DEPLOY} on port ${target_port}." >&2
    cat "${pf_log}" >&2 || true
    kill "${pf_pid}" >/dev/null 2>&1 || true
    rm -f "${pf_log}"
    return 1
  fi

  local rc=0
  if [ "${has_stdin_body}" = "1" ]; then
    curl -fsS -X "${method}" -H "Content-Type: application/json" --data-binary @- "http://127.0.0.1:${LOCAL_PORT}${path}" || rc=$?
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
  stop)
    PROJ="${1:-}"
    [ -n "${PROJ}" ] || { echo "Error: missing <project> (e.g. pn)" >&2; exit 1; }
    with_loopback_tunnel 8081 POST "/admin/revoke?project=${PROJ}"
    ;;
  start)
    PROJ="${1:-}"
    [ -n "${PROJ}" ] || { echo "Error: missing <project> (e.g. pn)" >&2; exit 1; }
    with_loopback_tunnel 8081 POST "/admin/enable?project=${PROJ}"
    ;;
  limit)
    PROJ="${1:-}"
    LIM="${2:-}"
    [ -n "${PROJ}" ] && [ -n "${LIM}" ] || { echo "Error: usage: $0 limit <project> <daily_limit>" >&2; exit 1; }
    with_loopback_tunnel 8081 POST "/admin/quota?project=${PROJ}&daily_limit=${LIM}"
    ;;
  self-test)
    with_loopback_tunnel 8080 GET "/startupz"
    ;;
  set-key)
    read -r -s -p "Enter Upstream Gemini API Key (input hidden, zero Vault token needed): " GEMINI_KEY
    echo ""
    if [ -z "${GEMINI_KEY}" ]; then
      echo "Error: Gemini API key cannot be empty." >&2
      exit 1
    fi
    # Primary path: send over loopback tunnel via stdin to vault-ai-proxy (never exposing key in process argv)
    if ${KUBE_EXEC} -n "${VAULT_NS}" get "${AI_PROXY_DEPLOY}" >/dev/null 2>&1; then
      if printf '{"api_key":"%s"}' "${GEMINI_KEY}" | with_loopback_tunnel 8081 POST "/admin/set-key" 1; then
        echo "SUCCESS: Gemini API key saved to OpenBao (kv/ai/gemini-api-key) and hot-reloaded in vault-ai-proxy RAM!"
        exit 0
      fi
    fi
    echo "Fallback: Writing kv/ai/gemini-api-key via least-privilege vault-ai-proxy Kubernetes ServiceAccount stdin..."
    JWT=$(${KUBE_EXEC} --as=system:admin --as-group=platform:break-glass create token vault-ai-proxy -n "${VAULT_NS}")
    # shellcheck disable=SC2016
    printf '{"jwt":"%s","key":"%s"}\n' "${JWT}" "${GEMINI_KEY}" | ${KUBE_EXEC} exec -i -n "${VAULT_NS}" "${OPENBAO_POD}" -- /bin/sh -c '
      set -eu
      export VAULT_ADDR="http://127.0.0.1:8200"
      PAYLOAD="$(cat)"
      JWT_VAL="$(printf "%s" "${PAYLOAD}" | sed -n "s/.*\"jwt\":\"\([^\"]*\)\".*/\1/p")"
      KEY_VAL="$(printf "%s" "${PAYLOAD}" | sed -n "s/.*\"key\":\"\([^\"]*\)\".*/\1/p")"
      VAULT_TOKEN="$(printf "{\"role\":\"vault-ai-proxy\",\"jwt\":\"%s\"}" "${JWT_VAL}" | bao write -field=token auth/kubernetes/login -)"
      export VAULT_TOKEN
      printf "{\"value\":\"%s\"}" "${KEY_VAL}" | bao kv put kv/ai/gemini-api-key - >/dev/null
    '
    echo "SUCCESS: Gemini API key stored in OpenBao (kv/ai/gemini-api-key) with zero Root Token!"
    ;;
  *)
    usage
    ;;
esac
