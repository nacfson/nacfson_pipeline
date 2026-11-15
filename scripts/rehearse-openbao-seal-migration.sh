#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CLASSIFIER="${SCRIPT_DIR}/lib/openbao_status.py"
COMPARE="${SCRIPT_DIR}/lib/openbao_fixture_compare.py"

BAO_VERSION="2.0.0"
BAO_TARBALL="bao_${BAO_VERSION}_Linux_x86_64.tar.gz"
BAO_URL="https://github.com/openbao/openbao/releases/download/v${BAO_VERSION}/${BAO_TARBALL}"
BAO_SHA256="0c49fd54d133cf95bd4d916a4a6b8f1583d3293f31880371f7a4e1412234fec6"

PROVIDER_TOKEN="rehearsal-provider-root"
TRANSIT_KEY="autounseal"
KV_MOUNT="rehearsal"
KV_VALUE="survives-the-seal-migration"
POLL_ATTEMPTS="${REHEARSAL_POLL_ATTEMPTS:-150}"

MODE="run"
FIXTURE_DIR=""

usage() {
  cat <<'EOF'
Usage:
  ./scripts/rehearse-openbao-seal-migration.sh
  ./scripts/rehearse-openbao-seal-migration.sh --write-fixtures DIR
  ./scripts/rehearse-openbao-seal-migration.sh --check-fixtures DIR

Runs a real OpenBao 2.0.0 binary on 127.0.0.1 through Shamir init, unseal, a
Shamir to transit auto-unseal migration and an unattended restart, and asserts
that scripts/lib/openbao_status.py classifies every captured status correctly.

Set BAO_BIN to use an existing binary instead of downloading the pinned release.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --write-fixtures | --check-fixtures)
      [[ $# -ge 2 ]] || { echo "Error: $1 needs a directory." >&2; exit 2; }
      MODE="${1#--}"
      FIXTURE_DIR="$2"
      shift 2
      ;;
    -h | --help)
      usage
      exit 0
      ;;
    *)
      usage >&2
      exit 2
      ;;
  esac
done

while IFS= read -r name; do
  unset "${name}"
done < <(compgen -e | grep -E '^(VAULT|BAO)_' | grep -vx 'BAO_BIN' || true)

WORK="$(mktemp -d)"
chmod 700 "${WORK}"
PROVIDER_PID=""
TARGET_PID=""

stop_pid() {
  local pid="$1"
  if [[ -n "${pid}" ]] && kill -0 "${pid}" 2>/dev/null; then
    kill "${pid}" 2>/dev/null || true
    wait "${pid}" 2>/dev/null || true
  fi
}

cleanup() {
  stop_pid "${TARGET_PID}"
  stop_pid "${PROVIDER_PID}"
  rm -rf "${WORK:?}"
}
trap cleanup EXIT

fail() {
  echo "FAIL: $*" >&2
  if [[ -f "${WORK}/target.log" ]]; then
    echo "--- last target log lines ---" >&2
    tail -n 15 "${WORK}/target.log" >&2
  fi
  exit 1
}

free_port() {
  python3 -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])'
}

json_field() {
  python3 -c 'import json,sys; v = json.load(sys.stdin)[sys.argv[1]]; print(v[0] if isinstance(v, list) else v)' "$1"
}

fetch_bao() {
  [[ "$(uname -sm)" == "Linux x86_64" ]] || fail "no pinned release for $(uname -sm); set BAO_BIN to an OpenBao ${BAO_VERSION} binary"
  mkdir -p "${WORK}/download"
  curl -fsSL --retry 3 -o "${WORK}/download/${BAO_TARBALL}" "${BAO_URL}"
  echo "${BAO_SHA256}  ${WORK}/download/${BAO_TARBALL}" | sha256sum --check --status \
    || fail "${BAO_TARBALL} does not match the pinned sha256 ${BAO_SHA256}"
  tar -xzf "${WORK}/download/${BAO_TARBALL}" -C "${WORK}/download" bao
  BAO_BIN="${WORK}/download/bao"
}

if [[ -z "${BAO_BIN:-}" ]]; then
  fetch_bao
fi
[[ -x "${BAO_BIN}" ]] || fail "BAO_BIN=${BAO_BIN} is not executable"
"${BAO_BIN}" version | grep -q "OpenBao v${BAO_VERSION} " || fail "expected OpenBao ${BAO_VERSION}, got $("${BAO_BIN}" version)"

PROVIDER_PORT="$(free_port)"
TARGET_PORT="$(free_port)"
TARGET_CLUSTER_PORT="$(free_port)"
TARGET_TOKEN=""
CAPTURE_DIR="${WORK}/captures"
mkdir -p "${CAPTURE_DIR}" "${WORK}/raft"

provider_bao() {
  env VAULT_ADDR="http://127.0.0.1:${PROVIDER_PORT}" VAULT_TOKEN="${PROVIDER_TOKEN}" "${BAO_BIN}" "$@"
}

target_bao() {
  env VAULT_ADDR="http://127.0.0.1:${TARGET_PORT}" VAULT_TOKEN="${TARGET_TOKEN}" "${BAO_BIN}" "$@"
}

target_status() {
  target_bao status -format=json 2>/dev/null || true
}

write_target_config() {
  local seal_stanza="$1"
  cat > "${WORK}/target.hcl" <<EOF
disable_mlock = true
ui = false

storage "raft" {
  path    = "${WORK}/raft"
  node_id = "rehearsal-0"
}

listener "tcp" {
  address         = "127.0.0.1:${TARGET_PORT}"
  cluster_address = "127.0.0.1:${TARGET_CLUSTER_PORT}"
  tls_disable     = 1
}

api_addr     = "http://127.0.0.1:${TARGET_PORT}"
cluster_addr = "http://127.0.0.1:${TARGET_CLUSTER_PORT}"
${seal_stanza}
EOF
}

transit_seal_stanza() {
  cat <<EOF

seal "transit" {
  address         = "http://127.0.0.1:${PROVIDER_PORT}"
  token           = "${PROVIDER_TOKEN}"
  mount_path      = "transit/"
  key_name        = "${TRANSIT_KEY}"
  disable_renewal = "true"
}
EOF
}

start_target() {
  "${BAO_BIN}" server -config="${WORK}/target.hcl" >> "${WORK}/target.log" 2>&1 &
  TARGET_PID=$!
}

stop_target() {
  stop_pid "${TARGET_PID}"
  TARGET_PID=""
}

wait_for_state() {
  local want="$1" raw="" got="unreachable" settled="false"
  for _ in $(seq 1 "${POLL_ATTEMPTS}"); do
    raw="$(target_status)"
    got="$(python3 "${CLASSIFIER}" <<< "${raw}")"
    if [[ "${got}" == "${want}" ]]; then
      case "${want}" in
        unsealed-*) grep -q '"is_self"' <<< "${raw}" && settled="true" ;;
        *) settled="true" ;;
      esac
    fi
    [[ "${settled}" == "true" ]] && break
    sleep 0.2
  done
  [[ "${settled}" == "true" ]] || fail "expected a settled openbao state ${want}, classifier returned ${got} for: ${raw}"
  STATUS_RAW="${raw}"
}

assert_state() {
  wait_for_state "$1"
  echo "PASS: ${1}"
}

capture_state() {
  wait_for_state "$1"
  printf '%s\n' "${STATUS_RAW}" > "${CAPTURE_DIR}/${1}.json"
  echo "PASS: ${1} (captured)"
}

echo "=== [1/6] Starting the transit provider (bao server -dev) on 127.0.0.1:${PROVIDER_PORT} ==="
"${BAO_BIN}" server -dev -dev-root-token-id="${PROVIDER_TOKEN}" -dev-listen-address="127.0.0.1:${PROVIDER_PORT}" > "${WORK}/provider.log" 2>&1 &
PROVIDER_PID=$!
for _ in $(seq 1 "${POLL_ATTEMPTS}"); do
  provider_bao status >/dev/null 2>&1 && break
  sleep 0.2
done
provider_bao status >/dev/null 2>&1 || fail "transit provider did not start: $(grep -Ev 'Unseal Key|Root Token' "${WORK}/provider.log" | tail -n 15)"
provider_bao secrets enable transit >/dev/null
provider_bao write -f "transit/keys/${TRANSIT_KEY}" >/dev/null

echo "=== [2/6] Starting the target with raft storage and no seal stanza (Shamir) ==="
write_target_config ""
start_target
capture_state uninitialized

echo "=== [3/6] Initializing and unsealing with 1 Shamir share ==="
INIT_JSON="$(target_bao operator init -key-shares=1 -key-threshold=1 -format=json)"
UNSEAL_KEY="$(json_field unseal_keys_b64 <<< "${INIT_JSON}")"
TARGET_TOKEN="$(json_field root_token <<< "${INIT_JSON}")"
capture_state sealed-shamir
target_bao operator unseal "${UNSEAL_KEY}" >/dev/null
capture_state unsealed-shamir

echo "=== [4/6] Writing a KV secret ==="
target_bao secrets enable -path="${KV_MOUNT}" kv-v2 >/dev/null
target_bao kv put "${KV_MOUNT}/probe" value="${KV_VALUE}" >/dev/null
[[ "$(target_bao kv get -field=value "${KV_MOUNT}/probe")" == "${KV_VALUE}" ]] || fail "KV secret not readable before the migration"
echo "PASS: KV secret written"

echo "=== [5/6] Migrating Shamir to transit auto-unseal ==="
stop_target
write_target_config "$(transit_seal_stanza)"
start_target
capture_state sealed-migration
target_bao operator unseal -migrate "${UNSEAL_KEY}" >/dev/null
capture_state unsealed-auto

echo "=== [6/6] Restarting the target; it must unseal on its own ==="
provider_bao write -f "transit/keys/${TRANSIT_KEY}/rotate" >/dev/null
provider_bao write "transit/keys/${TRANSIT_KEY}/config" min_decryption_version=2 >/dev/null
stop_target
start_target
capture_state sealed-auto
provider_bao write "transit/keys/${TRANSIT_KEY}/config" min_decryption_version=1 >/dev/null
stop_target
start_target
assert_state unsealed-auto
[[ "$(target_bao kv get -field=value "${KV_MOUNT}/probe")" == "${KV_VALUE}" ]] || fail "KV secret not readable after the unattended unseal"
echo "PASS: KV secret survived the migration and the unattended unseal"

case "${MODE}" in
  write-fixtures)
    mkdir -p "${FIXTURE_DIR}"
    for file in "${CAPTURE_DIR}"/*.json; do
      cp "${file}" "${FIXTURE_DIR}/$(basename "${file}")"
      echo "wrote ${FIXTURE_DIR}/$(basename "${file}")"
    done
    ;;
  check-fixtures)
    MISMATCHES=0
    for file in "${CAPTURE_DIR}"/*.json; do
      fixture="${FIXTURE_DIR}/$(basename "${file}")"
      if [[ ! -f "${fixture}" ]]; then
        echo "FAIL: no committed fixture ${fixture}" >&2
        MISMATCHES=$((MISMATCHES + 1))
      elif diff_output="$(python3 "${COMPARE}" "${file}" "${fixture}")"; then
        echo "MATCH: $(basename "${file}")"
      else
        echo "FAIL: $(basename "${file}") differs from ${fixture}" >&2
        echo "${diff_output}" >&2
        MISMATCHES=$((MISMATCHES + 1))
      fi
    done
    if [[ "${MISMATCHES}" -ne 0 ]]; then
      echo "FAIL: ${MISMATCHES} fixture(s) drifted from the real OpenBao ${BAO_VERSION} output; refresh with --write-fixtures" >&2
      exit 1
    fi
    ;;
esac

echo "SUCCESS: seal migration rehearsal passed on OpenBao ${BAO_VERSION}."
