#!/usr/bin/env bash
# ==============================================================================
# cutover-bundle.sh
# FullStateMigrationBundle Orchestrator (Export | Verify | Restore)
# Governing Contract: specs/012-gke-platform-migration/contracts/fluentbit-recovery-and-cutover-bundle.md
#
# Enforces:
#   - Out-of-git chmod 0700 staging directory (refuses paths inside repo root)
#   - 6 mandatory artifacts + manifest.sha256 checksum verification:
#       1. openbao.snap
#       2. keycloak.dump.gz
#       3. proj_pn.dump.gz
#       4. proj_health.dump.gz
#       5. edge-tls-secrets.yaml
#       6. victoria-logs-data.tar.gz
#   - Shamir-first OpenBao Raft restore (`flux suspend kustomization vault`),
#     immediate cross-cluster Kubernetes auth re-binding (`vault-configure-k8s-auth.sh`
#     + `vault-reconcile.sh --confirm`), PostgreSQL + Keycloak session restore,
#     and Gate 4 `gcpckms` (`nacfson-pipeline` / `asia-northeast3`) seal migration.
# ==============================================================================

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

# Resolve canonical bundle path and strictly forbid staging inside the Git working tree before creating any directory
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
    if d.get("kind") != "Secret":
        raise SystemExit(f"Unexpected kind in edge-tls-secrets.yaml: {d.get(\"kind\")}")
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
  run_source_kubectl exec -n observability victorialogs-0 -- \
    tar -czf - -C /victoria-logs-data . > "${ABS_BUNDLE_DIR}/victoria-logs-data.tar.gz"

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

# ==============================================================================
# RESTORE MODE
# ==============================================================================
if [[ "${TARGET_ENV}" != "local-k3s" && "${TARGET_ENV}" != "gke" ]]; then
  echo "Error: --target must be 'local-k3s' or 'gke' (got '${TARGET_ENV}')." >&2
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

echo "=== [1/6] Pre-Seeding Platform Edge TLS & ACME Account Secrets ==="
bg_kubectl apply -f "${ABS_BUNDLE_DIR}/edge-tls-secrets.yaml"

echo "=== [2/6] Suspending Flux 'vault' Layer for Shamir-First Raft Snapshot Restore ==="
bg_kubectl patch kustomization vault -n flux-system --type=merge -p '{"spec":{"suspend":true}}'

if [[ "${TARGET_ENV}" == "gke" ]]; then
  echo "Ensuring temporary Shamir listener config on openbao-config before restoring Shamir-sealed snapshot..."
  cat <<'EOF' | bg_kubectl apply -f -
apiVersion: v1
kind: ConfigMap
metadata:
  name: openbao-config
  namespace: vault
data:
  openbao.hcl: |
    ui = false
    storage "raft" {
      path = "/var/lib/openbao/data"
      node_id = "openbao-0"
    }
    listener "tcp" {
      address = "0.0.0.0:8200"
      cluster_address = "0.0.0.0:8201"
      tls_disable = 1
    }
    api_addr = "http://openbao-service.vault.svc:8200"
    cluster_addr = "http://openbao-0.openbao-internal.vault.svc:8201"
EOF
  bg_kubectl delete pod -n vault openbao-0 --ignore-not-found
  kubectl wait --for=jsonpath='{.status.phase}'=Running pod/openbao-0 -n vault --timeout=180s
fi

echo "=== [3/6] Restoring openbao.snap & Re-Binding Kubernetes Auth to '${TARGET_ENV}' ==="
if [[ -z "${VAULT_TOKEN:-}" ]]; then
  read -r -s -p "Enter OpenBao Root Token (valid inside snapshot): " VAULT_TOKEN
  echo ""
fi
if [[ -z "${UNSEAL_KEY:-}" ]]; then
  read -r -s -p "Enter Shamir Unseal Key (for snapshot): " UNSEAL_KEY
  echo ""
fi

# Ensure openbao-0 is initialized and unsealed before invoking 'raft snapshot restore -force'
BAO_STATUS_JSON="$(kubectl exec -n vault openbao-0 -- env VAULT_ADDR="http://127.0.0.1:8200" bao status -format=json 2>/dev/null || true)"
BAO_INITIALIZED="$(python3 -c 'import json,sys; d=json.loads(sys.stdin.read() or "{}"); print("true" if d.get("initialized") else "false")' <<< "${BAO_STATUS_JSON}")"
BAO_SEALED="$(python3 -c 'import json,sys; d=json.loads(sys.stdin.read() or "{}"); print("true" if d.get("sealed", True) else "false")' <<< "${BAO_STATUS_JSON}")"

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

# Unseal openbao-0 using the restored snapshot's Shamir key
kubectl exec -n vault openbao-0 -- \
  env VAULT_ADDR="http://127.0.0.1:8200" \
  bao operator unseal "${UNSEAL_KEY}" >/dev/null

ALLOW_SSH_FALLBACK=false VAULT_TOKEN="${VAULT_TOKEN}" "${SCRIPT_DIR}/vault-configure-k8s-auth.sh"
VAULT_TOKEN="${VAULT_TOKEN}" "${SCRIPT_DIR}/vault-reconcile.sh" --confirm

echo "=== [4/6] Restoring PostgreSQL Databases (keycloak, proj_pn, proj_health) ==="
"${SCRIPT_DIR}/db-migrate-dump.sh" --keycloak --file "${ABS_BUNDLE_DIR}/keycloak.dump.gz" --yes
"${SCRIPT_DIR}/db-migrate-dump.sh" --project pn --file "${ABS_BUNDLE_DIR}/proj_pn.dump.gz" --yes
"${SCRIPT_DIR}/db-migrate-dump.sh" --project health --file "${ABS_BUNDLE_DIR}/proj_health.dump.gz" --yes

bg_kubectl rollout restart deployment/keycloak -n identity
kubectl rollout status deployment/keycloak -n identity --timeout=300s

if [[ "${TARGET_ENV}" == "local-k3s" ]]; then
  echo "=== [5/6] Restoring VictoriaLogs History onto local-k3s ==="
  kubectl exec -i -n observability victorialogs-0 -- tar -xzf - -C /victoria-logs-data < "${ABS_BUNDLE_DIR}/victoria-logs-data.tar.gz"
  bg_kubectl delete pod -n observability victorialogs-0 --ignore-not-found
else
  echo "=== [5/6] Skipping in-cluster VictoriaLogs restore on GKE (logs stream to OCI Kafka; archive preserved for Spec 014) ==="
fi

echo "=== [6/6] Resuming Flux 'vault' Reconciliation ==="
bg_kubectl patch kustomization vault -n flux-system --type=merge -p '{"spec":{"suspend":false}}'

if [[ "${MIGRATE_KMS}" == "auto" ]]; then
  if [[ "${TARGET_ENV}" == "gke" ]]; then
    MIGRATE_KMS="true"
  else
    MIGRATE_KMS="false"
  fi
fi

if [[ "${TARGET_ENV}" == "gke" && "${MIGRATE_KMS}" == "true" ]]; then
  echo "=== Executing Shamir -> Cloud KMS (gcpckms) Seal Migration ==="
  bg_kubectl apply -f - <<'EOF'
apiVersion: v1
kind: ConfigMap
metadata:
  name: openbao-config
  namespace: vault
data:
  openbao.hcl: |
    ui = false
    disable_mlock = true

    storage "raft" {
      path    = "/openbao/data"
      node_id = "openbao-0"
    }

    seal "gcpckms" {
      project     = "nacfson-pipeline"
      region      = "asia-northeast3"
      key_ring    = "nacfson-vault"
      crypto_key  = "openbao-unseal"
    }

    listener "tcp" {
      address     = "0.0.0.0:8200"
      tls_disable = 1
    }

    api_addr     = "http://openbao.vault.svc.cluster.local:8200"
    cluster_addr = "http://openbao-0.openbao-internal.vault.svc.cluster.local:8201"
EOF
  flux reconcile kustomization vault --as=system:admin --as-group=platform:break-glass --with-source || true
  bg_kubectl delete pod -n vault openbao-0 --ignore-not-found
  kubectl wait --for=jsonpath='{.status.phase}'=Running pod/openbao-0 -n vault --timeout=180s
  kubectl exec -n vault openbao-0 -- \
    env VAULT_ADDR="http://127.0.0.1:8200" \
    bao operator unseal -migrate "${UNSEAL_KEY}"
  echo "PASS: openbao-0 migrated to unattended Cloud KMS auto-unseal (nacfson-pipeline / asia-northeast3)."
fi

unset VAULT_TOKEN UNSEAL_KEY
echo "SUCCESS: FullStateMigrationBundle restored onto '${TARGET_ENV}'."
