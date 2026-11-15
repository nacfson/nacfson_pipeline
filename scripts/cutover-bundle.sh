#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

REQUIRED_ARTIFACTS=(
  "openbao.snap"
  "keycloak.dump.gz"
  "proj_pn.dump.gz"
  "proj_health.dump.gz"
  "edge-tls-secrets.yaml"
  "victoria-logs-data.tar.gz"
)

SUBCOMMAND="${1:-}"
if [[ -n "${SUBCOMMAND}" ]]; then
  shift
fi

BUNDLE_DIR=""
SSH_HOST=""
TARGET_ENV=""
ASSUME_YES="false"
DRY_RUN="false"
MIGRATE_KMS="auto"

usage() {
  cat <<'EOF'
Usage:
  # 1. Extract full production state from vps-k3s into an out-of-git 0700 directory:
  ./scripts/cutover-bundle.sh export --bundle-dir "$HOME/.nacfson-cutover/20261110" [--ssh oracleCloud]

  # 2. Offline SHA-256, permission, and archive header verification (Gate 3):
  ./scripts/cutover-bundle.sh verify --bundle-dir "$HOME/.nacfson-cutover/20261110"

  # 3. Restore onto local-k3s (Gate 3 rehearsal) or gke (Gate 4 cutover):
  ./scripts/cutover-bundle.sh restore --bundle-dir "$HOME/.nacfson-cutover/20261110" --target local-k3s|gke [--migrate-kms|--no-migrate-kms] [--yes]
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --bundle-dir)
      BUNDLE_DIR="${2:-}"
      shift 2
      ;;
    --ssh)
      SSH_HOST="${2:-}"
      shift 2
      ;;
    --target)
      TARGET_ENV="${2:-}"
      shift 2
      ;;
    --migrate-kms)
      MIGRATE_KMS="true"
      shift
      ;;
    --no-migrate-kms)
      MIGRATE_KMS="false"
      shift
      ;;
    --dry-run)
      DRY_RUN="true"
      shift
      ;;
    -y|--yes)
      ASSUME_YES="true"
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "Error: Unknown argument '$1'" >&2
      usage >&2
      exit 1
      ;;
  esac
done

if [[ -z "${SUBCOMMAND}" || ( "${SUBCOMMAND}" != "export" && "${SUBCOMMAND}" != "verify" && "${SUBCOMMAND}" != "restore" ) ]]; then
  usage >&2
  exit 1
fi

if [[ -z "${BUNDLE_DIR}" ]]; then
  echo "Error: --bundle-dir <path> is required." >&2
  exit 1
fi

ABS_BUNDLE_DIR="$(python3 -c 'import os, sys; print(os.path.realpath(sys.argv[1]))' "${BUNDLE_DIR}")"
ABS_REPO_ROOT="$(cd "${REPO_ROOT}" && pwd -P)"

case "${ABS_BUNDLE_DIR}/" in
  "${ABS_REPO_ROOT}/"*)
    echo "Error: --bundle-dir '${ABS_BUNDLE_DIR}' MUST reside outside Git repository '${ABS_REPO_ROOT}' (Constitution Principle III)." >&2
    exit 1
    ;;
esac

run_source_kubectl() {
  if [[ -n "${SSH_HOST}" ]]; then
    local quoted=""
    for arg in "$@"; do
      quoted+=" $(printf '%q' "${arg}")"
    done
    # shellcheck disable=SC2029
    ssh "${SSH_HOST}" "sudo k3s kubectl${quoted}"
  else
    kubectl "$@"
  fi
}

archive_victorialogs() {
  local archive_file="${ABS_BUNDLE_DIR}/victoria-logs-data.tar.gz"
  if [[ -n "${SSH_HOST}" ]]; then
    local pv node_path quoted_path
    pv="$(run_source_kubectl get pvc -n observability data-victorialogs-0 -o 'jsonpath={.spec.volumeName}')"
    if [[ -z "${pv}" ]]; then
      echo "Error: failed to resolve PV for PVC observability/data-victorialogs-0." >&2
      exit 1
    fi

    node_path="$(run_source_kubectl get pv "${pv}" -o 'jsonpath={.spec.hostPath.path}{.spec.local.path}')"
    if [[ -z "${node_path}" || "${node_path}" != /* ]]; then
      echo "Error: PV '${pv}' does not expose an absolute hostPath/local node path (got '${node_path}')." >&2
      exit 1
    fi

    quoted_path="$(printf '%q' "${node_path}")"
    # shellcheck disable=SC2029
    if ! ssh "${SSH_HOST}" "sudo test -d ${quoted_path} && sudo du -sh ${quoted_path}"; then
      echo "Error: VictoriaLogs PV directory '${node_path}' on '${SSH_HOST}' is missing or inaccessible." >&2
      exit 1
    fi

    # shellcheck disable=SC2029
    if ! ssh "${SSH_HOST}" "sudo tar --warning=no-file-changed -czf - -C ${quoted_path} . ; rc=\$?; [ \"\$rc\" -le 1 ]" > "${archive_file}"; then
      echo "Error: failed to stream VictoriaLogs archive from '${SSH_HOST}:${node_path}'." >&2
      exit 1
    fi
  else
    if ! run_source_kubectl exec -n observability victorialogs-0 -- \
      tar -czf - -C /victoria-logs-data . > "${archive_file}"; then
      echo "Error: in-pod tar failed (the victoria-logs image ships no tar). Re-run export with --ssh <k3s-host> to archive the PV from the node." >&2
      exit 1
    fi
  fi

  local archive_size
  archive_size="$(du -Lh "${archive_file}" | cut -f1)"
  echo "✓ VictoriaLogs archive: ${archive_file} (${archive_size})"
}

restore_victorialogs_local_k3s() {
  local archive_file="${ABS_BUNDLE_DIR}/victoria-logs-data.tar.gz"
  if [[ -n "${SSH_HOST}" ]]; then
    local pv node_path quoted_path
    pv="$(kubectl get pvc -n observability data-victorialogs-0 -o 'jsonpath={.spec.volumeName}')"
    if [[ -z "${pv}" ]]; then
      echo "Error: failed to resolve PV for PVC observability/data-victorialogs-0." >&2
      exit 1
    fi

    node_path="$(kubectl get pv "${pv}" -o 'jsonpath={.spec.hostPath.path}{.spec.local.path}')"
    if [[ -z "${node_path}" || "${node_path}" != /* ]]; then
      echo "Error: PV '${pv}' does not expose an absolute hostPath/local node path (got '${node_path}')." >&2
      exit 1
    fi

    quoted_path="$(printf '%q' "${node_path}")"
    bg_kubectl scale statefulset/victorialogs -n observability --replicas=0
    kubectl wait --for=delete pod/victorialogs-0 -n observability --timeout=120s || true

    # shellcheck disable=SC2029
    if ! ssh "${SSH_HOST}" "sudo tar -xzf - -C ${quoted_path} && sudo chown -R 1000:1000 ${quoted_path}" < "${archive_file}"; then
      bg_kubectl scale statefulset/victorialogs -n observability --replicas=1 || true
      echo "Error: failed to restore VictoriaLogs archive onto '${SSH_HOST}:${node_path}'." >&2
      exit 1
    fi

    bg_kubectl scale statefulset/victorialogs -n observability --replicas=1
  else
    if ! kubectl exec -i -n observability victorialogs-0 -- \
      tar -xzf - -C /victoria-logs-data < "${archive_file}"; then
      echo "Error: in-pod tar failed (the victoria-logs image ships no tar). Re-run restore with --ssh <k3s-host> to extract onto the node PV." >&2
      exit 1
    fi
    bg_kubectl delete pod -n observability victorialogs-0 --ignore-not-found
  fi
}

verify_bundle() {
  echo "=== Verifying FullStateMigrationBundle in ${ABS_BUNDLE_DIR} ==="
  if [[ ! -d "${ABS_BUNDLE_DIR}" ]]; then
    echo "Error: Bundle directory '${ABS_BUNDLE_DIR}' does not exist." >&2
    exit 1
  fi
  local perms
  perms="$(stat -c '%a' "${ABS_BUNDLE_DIR}")"
  if [[ "${perms}" != "700" ]]; then
    echo "Error: Bundle directory permissions must be 0700, found 0${perms}." >&2
    exit 1
  fi

  if [[ ! -s "${ABS_BUNDLE_DIR}/manifest.sha256" ]]; then
    echo "Error: Missing or empty ${ABS_BUNDLE_DIR}/manifest.sha256." >&2
    exit 1
  fi

  for item in "${REQUIRED_ARTIFACTS[@]}"; do
    if [[ ! -s "${ABS_BUNDLE_DIR}/${item}" ]]; then
      echo "Error: Missing or empty required artifact '${item}'." >&2
      exit 1
    fi
  done

  (
    cd "${ABS_BUNDLE_DIR}"
    sha256sum -c manifest.sha256
  )

  for gz in keycloak.dump.gz proj_pn.dump.gz proj_health.dump.gz; do
    gzip -t "${ABS_BUNDLE_DIR}/${gz}"
  done
  tar -tzf "${ABS_BUNDLE_DIR}/victoria-logs-data.tar.gz" >/dev/null

  python3 -c '
import sys, yaml
with open(sys.argv[1], "r", encoding="utf-8") as fh:
    docs = [d for d in yaml.safe_load_all(fh) if d]
if not docs:
    raise SystemExit("edge-tls-secrets.yaml contains zero Secret documents")
for d in docs:
    kind = d.get("kind")
    if kind != "Secret":
        raise SystemExit(f"Unexpected kind in edge-tls-secrets.yaml: {kind}")
' "${ABS_BUNDLE_DIR}/edge-tls-secrets.yaml"

  echo "PASS: All 6 migration artifacts, 0700 permissions, and SHA-256 digests verified."
}

if [[ "${SUBCOMMAND}" == "export" ]]; then
  mkdir -p "${ABS_BUNDLE_DIR}"
  chmod 0700 "${ABS_BUNDLE_DIR}"
  umask 0077
  echo "=== [1/5] Exporting OpenBao Raft Snapshot (openbao.snap) ==="
  if [[ "${DRY_RUN}" == "true" ]]; then
    echo "[Dry-Run] Would export openbao.snap, 3 PostgreSQL dumps, edge-tls-secrets.yaml, and victoria-logs-data.tar.gz into ${ABS_BUNDLE_DIR}"
    exit 0
  fi

  if [[ -z "${VAULT_TOKEN:-}" ]]; then
    read -r -s -p "Enter Vault Root/Snapshot Token for source cluster export: " VAULT_TOKEN
    echo ""
  fi
  run_source_kubectl exec -n vault openbao-0 -- \
    env VAULT_ADDR="http://127.0.0.1:8200" VAULT_TOKEN="${VAULT_TOKEN}" \
    bao operator raft snapshot save /tmp/openbao.snap
  run_source_kubectl exec -n vault openbao-0 -- cat /tmp/openbao.snap > "${ABS_BUNDLE_DIR}/openbao.snap"
  run_source_kubectl exec -n vault openbao-0 -- rm -f /tmp/openbao.snap

  echo "=== [2/5] Exporting PostgreSQL Databases (keycloak, proj_pn, proj_health) ==="
  DB_SSH_ARGS=()
  if [[ -n "${SSH_HOST}" ]]; then
    DB_SSH_ARGS=(--ssh "${SSH_HOST}")
  fi
  "${SCRIPT_DIR}/db-migrate-dump.sh" --keycloak --export "${ABS_BUNDLE_DIR}/keycloak.dump.gz" "${DB_SSH_ARGS[@]}"
  "${SCRIPT_DIR}/db-migrate-dump.sh" --project pn --export "${ABS_BUNDLE_DIR}/proj_pn.dump.gz" "${DB_SSH_ARGS[@]}"
  "${SCRIPT_DIR}/db-migrate-dump.sh" --project health --export "${ABS_BUNDLE_DIR}/proj_health.dump.gz" "${DB_SSH_ARGS[@]}"

  echo "=== [3/5] Exporting Platform Edge TLS & ACME Account Secrets ==="
  {
    for tls_sec in auth-tls pn-tls health-tls blog-tls; do
      if run_source_kubectl get secret -n platform "${tls_sec}" >/dev/null 2>&1; then
        run_source_kubectl get secret -n platform "${tls_sec}" -o yaml
        echo "---"
      fi
    done
    for acme_sec in letsencrypt-prod-account letsencrypt-staging-account; do
      if run_source_kubectl get secret -n cert-manager "${acme_sec}" >/dev/null 2>&1; then
        run_source_kubectl get secret -n cert-manager "${acme_sec}" -o yaml
        echo "---"
      fi
    done
  } | python3 -c '
import sys, yaml
cleaned = []
for doc in yaml.safe_load_all(sys.stdin):
    if not isinstance(doc, dict) or doc.get("kind") != "Secret":
        continue
    meta = doc.get("metadata") or {}
    for k in ("uid", "resourceVersion", "creationTimestamp", "managedFields", "ownerReferences", "selfLink"):
        meta.pop(k, None)
    ann = meta.get("annotations") or {}
    ann.pop("kubectl.kubernetes.io/last-applied-configuration", None)
    cleaned.append(doc)
yaml.safe_dump_all(cleaned, sys.stdout, sort_keys=False)
' > "${ABS_BUNDLE_DIR}/edge-tls-secrets.yaml"

  echo "=== [4/5] Archiving VictoriaLogs History (victoria-logs-data.tar.gz) ==="
  archive_victorialogs

  echo "=== [5/5] Generating manifest.sha256 & Verifying Bundle ==="
  (
    cd "${ABS_BUNDLE_DIR}"
    sha256sum "${REQUIRED_ARTIFACTS[@]}" > manifest.sha256
  )
  verify_bundle
  exit 0
fi

if [[ "${SUBCOMMAND}" == "verify" ]]; then
  verify_bundle
  exit 0
fi

if [[ "${TARGET_ENV}" != "local-k3s" && "${TARGET_ENV}" != "gke" ]]; then
  echo "Error: --target must be 'local-k3s' or 'gke' (got '${TARGET_ENV}')." >&2
  exit 1
fi

if [[ "${MIGRATE_KMS}" == "auto" ]]; then
  if [[ "${TARGET_ENV}" == "gke" ]]; then
    MIGRATE_KMS="true"
  else
    MIGRATE_KMS="false"
  fi
fi
if [[ "${TARGET_ENV}" == "gke" && "${MIGRATE_KMS}" != "true" ]]; then
  echo "Error: --no-migrate-kms is not supported for --target gke: Flux applies the gcpckms seal config from Git, so OpenBao must finish on the Cloud KMS seal." >&2
  exit 1
fi
if [[ "${TARGET_ENV}" == "local-k3s" && "${MIGRATE_KMS}" == "true" ]]; then
  echo "Error: --migrate-kms applies only to --target gke." >&2
  exit 1
fi

verify_bundle

if [[ "${DRY_RUN}" == "true" ]]; then
  echo "=== [Dry-Run] Verified bundle for target '${TARGET_ENV}'. Zero cluster mutations executed. ==="
  exit 0
fi

if [[ "${ASSUME_YES}" != "true" ]]; then
  read -r -p "Restore verified cutover bundle from '${ABS_BUNDLE_DIR}' onto target '${TARGET_ENV}'? [y/N]: " CONFIRM
  if [[ ! "${CONFIRM}" =~ ^[Yy]$ ]]; then
    echo "Aborted by operator."
    exit 1
  fi
fi

bg_kubectl() {
  kubectl --as=break-glass:cutover --as-group=platform:break-glass "$@" 2>/dev/null || kubectl "$@"
}

POLL_SECONDS="${CUTOVER_POLL_SECONDS:-5}"
LAYER_TIMEOUT="${CUTOVER_LAYER_TIMEOUT:-15m}"

set_layer_suspend() {
  bg_kubectl patch kustomization "$1" -n flux-system --type=merge -p "{\"spec\":{\"suspend\":$2}}"
}

request_layer_reconcile() {
  bg_kubectl annotate kustomization "$1" -n flux-system --overwrite "reconcile.fluxcd.io/requestedAt=$(date -u +%Y%m%dT%H%M%SZ)"
}

wait_layer_ready() {
  echo "Waiting for Flux layer '$1' to become Ready (timeout ${LAYER_TIMEOUT})..."
  kubectl wait "kustomization/$1" -n flux-system --for=condition=Ready "--timeout=${LAYER_TIMEOUT}"
}

set_openbao_config() {
  local payload
  payload="$(python3 -c 'import json,sys; print(json.dumps({"data": {"openbao.hcl": sys.stdin.read()}}))' <<< "$1")"
  bg_kubectl patch configmap openbao-config -n vault --type=merge -p "${payload}"
}

restart_openbao() {
  bg_kubectl delete pod -n vault openbao-0 --ignore-not-found --wait=true
  local i
  for i in $(seq 1 60); do
    if kubectl get pod openbao-0 -n vault >/dev/null 2>&1; then
      break
    fi
    sleep "${POLL_SECONDS}"
  done
  kubectl wait --for=jsonpath='{.status.phase}'=Running pod/openbao-0 -n vault --timeout=180s
}

bao_status_field() {
  local json
  json="$(kubectl exec -n vault openbao-0 -- env VAULT_ADDR="http://127.0.0.1:8200" bao status -format=json 2>/dev/null || true)"
  python3 -c 'import json,sys; d=json.loads(sys.stdin.read() or "{}"); v=d.get(sys.argv[1]); print(str(v).lower() if isinstance(v,bool) else (v or ""))' "$1" <<< "${json}"
}

if [[ -z "${VAULT_TOKEN:-}" ]]; then
  read -r -s -p "Enter OpenBao Root Token (valid inside snapshot): " VAULT_TOKEN
  echo ""
fi
if [[ -z "${UNSEAL_KEY:-}" ]]; then
  read -r -s -p "Enter Shamir Unseal Key (for snapshot): " UNSEAL_KEY
  echo ""
fi

KMS_HCL=""
if [[ "${TARGET_ENV}" == "gke" ]]; then
  KMS_HCL="$(kubectl get configmap openbao-config -n vault -o jsonpath='{.data.openbao\.hcl}')"
  if ! grep -q 'seal "gcpckms"' <<< "${KMS_HCL}"; then
    echo "Error: live vault/openbao-config has no seal \"gcpckms\" stanza; refusing to restore onto gke." >&2
    exit 1
  fi
  SHAMIR_HCL="$(python3 -c 'import re,sys; print(re.sub(r"\n?seal \"[^\"]+\" \{.*?\n\}\n?", "\n", sys.stdin.read(), flags=re.S).strip())' <<< "${KMS_HCL}")"
fi

echo "=== [1/7] Pre-Seeding Platform Edge TLS & ACME Account Secrets ==="
bg_kubectl apply -f "${ABS_BUNDLE_DIR}/edge-tls-secrets.yaml"

echo "=== [2/7] Suspending Flux 'vault' and 'identity' Layers ==="
set_layer_suspend vault true
set_layer_suspend identity true

if [[ "${TARGET_ENV}" == "gke" ]]; then
  echo "Switching openbao-config to the live config without its seal stanza (Shamir) for the Shamir-sealed snapshot..."
  set_openbao_config "${SHAMIR_HCL}"
  restart_openbao
fi

echo "=== [3/7] Restoring openbao.snap ==="
BAO_INITIALIZED="$(bao_status_field initialized)"
BAO_SEALED="$(bao_status_field sealed)"

RESTORE_AUTH_TOKEN="${VAULT_TOKEN}"
if [[ "${BAO_INITIALIZED}" != "true" ]]; then
  echo "Target openbao-0 is uninitialized; performing ephemeral 1-share bootstrap init/unseal for raft snapshot restore..."
  INIT_JSON="$(kubectl exec -n vault openbao-0 -- env VAULT_ADDR="http://127.0.0.1:8200" bao operator init -key-shares=1 -key-threshold=1 -format=json)"
  TEMP_UNSEAL_KEY="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["unseal_keys_b64"][0])' <<< "${INIT_JSON}")"
  RESTORE_AUTH_TOKEN="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["root_token"])' <<< "${INIT_JSON}")"
  kubectl exec -n vault openbao-0 -- env VAULT_ADDR="http://127.0.0.1:8200" bao operator unseal "${TEMP_UNSEAL_KEY}" >/dev/null
  unset INIT_JSON TEMP_UNSEAL_KEY
elif [[ "${BAO_SEALED}" == "true" ]]; then
  kubectl exec -n vault openbao-0 -- env VAULT_ADDR="http://127.0.0.1:8200" bao operator unseal "${UNSEAL_KEY}" >/dev/null
fi

kubectl exec -i -n vault openbao-0 -- tee /tmp/openbao.snap >/dev/null < "${ABS_BUNDLE_DIR}/openbao.snap"
kubectl exec -n vault openbao-0 -- \
  env VAULT_ADDR="http://127.0.0.1:8200" VAULT_TOKEN="${RESTORE_AUTH_TOKEN}" \
  bao operator raft snapshot restore -force /tmp/openbao.snap
kubectl exec -n vault openbao-0 -- rm -f /tmp/openbao.snap
unset RESTORE_AUTH_TOKEN

kubectl exec -n vault openbao-0 -- \
  env VAULT_ADDR="http://127.0.0.1:8200" \
  bao operator unseal "${UNSEAL_KEY}" >/dev/null

if [[ "${TARGET_ENV}" == "gke" ]]; then
  echo "=== [4/7] Migrating openbao-0 from Shamir to Cloud KMS (gcpckms) ==="
  set_openbao_config "${KMS_HCL}"
  restart_openbao
  kubectl exec -n vault openbao-0 -- \
    env VAULT_ADDR="http://127.0.0.1:8200" \
    bao operator unseal -migrate "${UNSEAL_KEY}"
  MIGRATED="false"
  for i in $(seq 1 24); do
    if [[ "$(bao_status_field type)" == "gcpckms" && "$(bao_status_field sealed)" == "false" ]]; then
      MIGRATED="true"
      break
    fi
    sleep "${POLL_SECONDS}"
  done
  if [[ "${MIGRATED}" != "true" ]]; then
    echo "Error: openbao-0 is not unsealed on gcpckms after migration; Flux 'vault' and 'identity' stay suspended for break-glass recovery." >&2
    exit 1
  fi
  echo "PASS: openbao-0 runs on unattended Cloud KMS auto-unseal (nacfson-pipeline / asia-northeast3)."
else
  echo "=== [4/7] Keeping Shamir seal on local-k3s ==="
fi

echo "Re-binding OpenBao Kubernetes auth to '${TARGET_ENV}'..."
ALLOW_SSH_FALLBACK=false VAULT_TOKEN="${VAULT_TOKEN}" "${SCRIPT_DIR}/vault-configure-k8s-auth.sh"
VAULT_TOKEN="${VAULT_TOKEN}" "${SCRIPT_DIR}/vault-reconcile.sh" --confirm

echo "=== [5/7] Resuming Flux 'vault' and Waiting for PostgreSQL ==="
set_layer_suspend vault false
request_layer_reconcile vault
wait_layer_ready vault
request_layer_reconcile database
wait_layer_ready database
kubectl rollout status statefulset/postgres -n identity --timeout=600s

echo "=== [6/7] Restoring PostgreSQL Databases (keycloak, proj_pn, proj_health) ==="
"${SCRIPT_DIR}/db-migrate-dump.sh" --keycloak --file "${ABS_BUNDLE_DIR}/keycloak.dump.gz" --yes
"${SCRIPT_DIR}/db-migrate-dump.sh" --project pn --file "${ABS_BUNDLE_DIR}/proj_pn.dump.gz" --yes
"${SCRIPT_DIR}/db-migrate-dump.sh" --project health --file "${ABS_BUNDLE_DIR}/proj_health.dump.gz" --yes

echo "=== [7/7] Resuming Flux 'identity' (Keycloak starts on the restored database) ==="
set_layer_suspend identity false
request_layer_reconcile identity
if kubectl get deployment keycloak -n identity >/dev/null 2>&1; then
  bg_kubectl rollout restart deployment/keycloak -n identity
fi
wait_layer_ready identity
kubectl rollout status deployment/keycloak -n identity --timeout=300s

if [[ "${TARGET_ENV}" == "local-k3s" ]]; then
  echo "Restoring VictoriaLogs history onto local-k3s..."
  restore_victorialogs_local_k3s
else
  echo "Skipping in-cluster VictoriaLogs restore on GKE (logs stream to OCI Kafka; archive preserved for Spec 014)."
fi

unset VAULT_TOKEN UNSEAL_KEY
echo "SUCCESS: FullStateMigrationBundle restored onto '${TARGET_ENV}'."
