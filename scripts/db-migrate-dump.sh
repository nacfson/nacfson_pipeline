#!/usr/bin/env bash
# ==============================================================================
# db-migrate-dump.sh
# Standardized, Zero-Plaintext Pipeline Database Dump Import / Export Tool
# Governing Contract: contracts/CONTRACT_DATABASE.md (§6)
#
# Features:
#   - Project-agnostic: maps --project <slug> -> db: proj_<slug>, role: user_<slug>
#   - Zero plaintext passwords on workstation: streams over `kubectl exec -i`
#     (auto-detects local kubectl vs `ssh <host> sudo k3s kubectl`)
#   - Automatic pre-restore safety snapshot on postgres-0 (/tmp or /backups)
#   - Supports .dump (custom), .dump.gz, .sql, .sql.gz, and .tar.gz archives
#   - Enforces ownership normalization (REASSIGN OWNED / GRANT ALL to user_<slug>)
#   - Supports --export to pull a verified .dump.gz + .sha256 from the cluster
# ==============================================================================

set -euo pipefail

PROJECT_SLUG="pn"
DUMP_PATH=""
EXPORT_PATH=""
MODE="full" # full | data
SSH_HOST="${SSH_HOST:-}"
PG_NAMESPACE="${PG_NAMESPACE:-identity}"
PG_POD="${PG_POD:-postgres-0}"
SKIP_SNAPSHOT="false"
ASSUME_YES="false"
DRY_RUN="false"

usage() {
  cat <<'EOF'
Usage:
  # Import a local dump (.dump, .dump.gz, .sql, .sql.gz, .tar.gz) into proj_<slug>:
  ./scripts/db-migrate-dump.sh --project <slug> --file <path> [--mode full|data] [--ssh <host>] [--yes]

  # Export proj_<slug> from the cluster to a local custom dump (.dump.gz + .sha256):
  ./scripts/db-migrate-dump.sh --project <slug> --export <output.dump.gz> [--ssh <host>]

Options:
  -p, --project <slug>     Project slug (default: pn -> db: proj_pn, role: user_pn)
  -f, --file <path>        Local dump file to import (.dump, .dump.gz, .sql, .sql.gz, .tar.gz)
  -e, --export <path>      Export cluster database proj_<slug> to local file (.dump.gz)
  -m, --mode <full|data>   Import mode: 'full' (schema+data, --clean) or 'data' (--data-only) (default: full)
      --ssh <host>         Target remote K3s SSH host (e.g., oracleCloud); if omitted, tries local kubectl first then oracleCloud
      --skip-snapshot      Skip pre-migration safety backup inside postgres-0
      --dry-run            Validate archive and print execution plan without mutating the database
  -y, --yes                Skip interactive confirmation prompt
  -h, --help               Show this help message
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    -p|--project)
      PROJECT_SLUG="${2:-}"
      shift 2
      ;;
    -f|--file)
      DUMP_PATH="${2:-}"
      shift 2
      ;;
    -e|--export)
      EXPORT_PATH="${2:-}"
      shift 2
      ;;
    -m|--mode)
      MODE="${2:-}"
      shift 2
      ;;
    --ssh)
      SSH_HOST="${2:-}"
      shift 2
      ;;
    --skip-snapshot)
      SKIP_SNAPSHOT="true"
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

# Normalize and validate project slug
PROJECT_SLUG="$(echo "${PROJECT_SLUG}" | tr '[:upper:]' '[:lower:]' | sed -E 's/^proj[-_]//; s/-/_/g')"
if [[ ! "${PROJECT_SLUG}" =~ ^[a-z0-9_]+$ ]]; then
  echo "Error: Invalid project slug '${PROJECT_SLUG}' (must be alphanumeric/underscore)." >&2
  exit 1
fi

TARGET_DB="proj_${PROJECT_SLUG}"
TARGET_ROLE="user_${PROJECT_SLUG}"

if [[ "${MODE}" != "full" && "${MODE}" != "data" ]]; then
  echo "Error: --mode must be 'full' or 'data' (got '${MODE}')." >&2
  exit 1
fi

if [[ -z "${DUMP_PATH}" && -z "${EXPORT_PATH}" ]] || [[ -n "${DUMP_PATH}" && -n "${EXPORT_PATH}" ]]; then
  echo "Error: Specify exactly one of --file <path> (import) or --export <path> (export)." >&2
  usage >&2
  exit 1
fi

# Helper to run kubectl locally or over SSH on the K3s host
KUBE_MODE="local"
resolve_kubectl() {
  if [[ "${DRY_RUN}" == "true" ]]; then
    KUBE_MODE="dry-run"
    return 0
  fi
  if [[ -n "${SSH_HOST}" ]]; then
    if ssh -o BatchMode=yes -o ConnectTimeout=5 "${SSH_HOST}" "sudo k3s kubectl get pod -n ${PG_NAMESPACE} ${PG_POD}" >/dev/null 2>&1; then
      KUBE_MODE="ssh"
      return 0
    fi
    echo "Error: Cannot reach pod ${PG_NAMESPACE}/${PG_POD} via ssh '${SSH_HOST}'." >&2
    exit 1
  fi
  if command -v kubectl >/dev/null 2>&1 && kubectl exec -n "${PG_NAMESPACE}" "${PG_POD}" -- true >/dev/null 2>&1; then
    KUBE_MODE="local"
  elif ssh -o BatchMode=yes -o ConnectTimeout=5 "oracleCloud" "sudo k3s kubectl get pod -n ${PG_NAMESPACE} ${PG_POD}" >/dev/null 2>&1; then
    SSH_HOST="oracleCloud"
    KUBE_MODE="ssh"
  else
    echo "Error: Cannot reach pod ${PG_NAMESPACE}/${PG_POD} via local kubectl or ssh 'oracleCloud'." >&2
    exit 1
  fi
}

kube_exec() {
  if [[ "${KUBE_MODE}" == "local" ]]; then
    kubectl exec -n "${PG_NAMESPACE}" "${PG_POD}" -- "$@"
  else
    local quoted_args=""
    for arg in "$@"; do
      quoted_args+=" $(printf '%q' "${arg}")"
    done
    # shellcheck disable=SC2029
    ssh "${SSH_HOST}" "sudo k3s kubectl exec -n ${PG_NAMESPACE} ${PG_POD} --${quoted_args}"
  fi
}

kube_exec_stdin() {
  if [[ "${KUBE_MODE}" == "local" ]]; then
    kubectl exec -i -n "${PG_NAMESPACE}" "${PG_POD}" -- "$@"
  else
    local quoted_args=""
    for arg in "$@"; do
      quoted_args+=" $(printf '%q' "${arg}")"
    done
    # shellcheck disable=SC2029
    ssh "${SSH_HOST}" "sudo k3s kubectl exec -i -n ${PG_NAMESPACE} ${PG_POD} --${quoted_args}"
  fi
}

# ==============================================================================
# EXPORT MODE
# ==============================================================================
if [[ -n "${EXPORT_PATH}" ]]; then
  resolve_kubectl
  echo "=== [Pipeline DB Export] Exporting ${TARGET_DB} (role: ${TARGET_ROLE}) -> ${EXPORT_PATH} ==="
  if [[ "${DRY_RUN}" == "true" ]]; then
    echo "[Dry-Run] Would stream pg_dump -Fc --no-owner --no-acl -U ${TARGET_ROLE} -d ${TARGET_DB} | gzip > ${EXPORT_PATH}"
    exit 0
  fi

  mkdir -p "$(dirname "${EXPORT_PATH}")"
  kube_exec env PGUSER="${TARGET_ROLE}" PGDATABASE="${TARGET_DB}" \
    pg_dump --format=custom --no-owner --no-acl | gzip -c > "${EXPORT_PATH}"
  sha256sum "${EXPORT_PATH}" > "${EXPORT_PATH}.sha256"
  echo "✓ Export complete: ${EXPORT_PATH} ($(du -h "${EXPORT_PATH}" | awk '{print $1}'))"
  echo "✓ Checksum written: ${EXPORT_PATH}.sha256"
  exit 0
fi

# ==============================================================================
# IMPORT MODE
# ==============================================================================
if [[ ! -f "${DUMP_PATH}" ]]; then
  echo "Error: Dump file not found at '${DUMP_PATH}'." >&2
  exit 1
fi

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT

echo "=== [1/5] Pre-Flight Archive Validation ==="
if [[ -f "${DUMP_PATH}.sha256" ]]; then
  echo "Verifying SHA-256 sidecar checksum (${DUMP_PATH}.sha256)..."
  EXPECTED_SHA="$(awk '{print $1}' "${DUMP_PATH}.sha256" | head -n 1 | tr -d '\r\n ')"
  ACTUAL_SHA="$(sha256sum "${DUMP_PATH}" | awk '{print $1}')"
  if [[ -z "${EXPECTED_SHA}" || "${EXPECTED_SHA}" != "${ACTUAL_SHA}" ]]; then
    echo "Error: SHA-256 mismatch! Expected ${EXPECTED_SHA}, got ${ACTUAL_SHA}." >&2
    exit 1
  fi
  echo "  ✓ SHA-256 verified: ${ACTUAL_SHA}"
fi

WORK_DIR="${TMP_DIR}/extracted"
mkdir -p "${WORK_DIR}"

case "${DUMP_PATH}" in
  *.tar.gz|*.tgz)
    tar -xzf "${DUMP_PATH}" -C "${WORK_DIR}"
    PAYLOAD_FILE="$(find "${WORK_DIR}" -type f \( -name '*.dump' -o -name '*.backup' -o -name '*.custom' -o -name '*.sql' \) | head -n 1 || true)"
    ;;
  *.dump.gz|*.sql.gz|*.gz)
    BASE_NAME="$(basename "${DUMP_PATH%.gz}")"
    PAYLOAD_FILE="${WORK_DIR}/${BASE_NAME}"
    gzip -dc "${DUMP_PATH}" > "${PAYLOAD_FILE}"
    ;;
  *)
    PAYLOAD_FILE="${DUMP_PATH}"
    ;;
esac

if [[ -z "${PAYLOAD_FILE}" || ! -f "${PAYLOAD_FILE}" ]]; then
  echo "Error: Could not locate a valid .dump or .sql payload inside '${DUMP_PATH}'." >&2
  exit 1
fi

# Detect whether payload is PostgreSQL custom format ("PGDMP" magic header) or plain SQL
HEADER_MAGIC="$(head -c 5 "${PAYLOAD_FILE}" 2>/dev/null || true)"
if [[ "${HEADER_MAGIC}" == "PGDMP" ]]; then
  ARCHIVE_FORMAT="custom"
else
  ARCHIVE_FORMAT="sql"
fi

echo "  • Project Slug   : ${PROJECT_SLUG}"
echo "  • Target DB      : ${TARGET_DB}"
echo "  • Target Role    : ${TARGET_ROLE}"
echo "  • Payload Format : ${ARCHIVE_FORMAT} ($(du -h "${PAYLOAD_FILE}" | awk '{print $1}'))"
echo "  • Import Mode    : ${MODE}"

if [[ "${DRY_RUN}" == "true" ]]; then
  echo "=== [Dry-Run] Validation succeeded. No cluster changes executed. ==="
  exit 0
fi

resolve_kubectl

if [[ "${ASSUME_YES}" != "true" ]]; then
  read -r -p "Proceed with importing '${DUMP_PATH}' (${MODE}) into '${TARGET_DB}' on ${PG_NAMESPACE}/${PG_POD}? [y/N]: " CONFIRM
  if [[ ! "${CONFIRM}" =~ ^[Yy]$ ]]; then
    echo "Aborted by operator."
    exit 1
  fi
fi

echo "=== [2/5] Creating Pre-Migration Safety Snapshot ==="
TS="$(date -u +%Y%m%d_%H%M%S)"
SNAPSHOT_PATH="/tmp/pre_migrate_${TARGET_DB}_${TS}.dump.gz"
if [[ "${SKIP_SNAPSHOT}" == "true" ]]; then
  echo "  • Skipping pre-migration snapshot (--skip-snapshot)."
else
  kube_exec sh -c "PGUSER=${TARGET_ROLE} PGDATABASE=${TARGET_DB} pg_dump --format=custom --no-owner --no-acl | gzip -c > ${SNAPSHOT_PATH}"
  echo "  ✓ Safety snapshot saved in ${PG_POD}:${SNAPSHOT_PATH}"
fi

echo "=== [3/5] Quiescing Active Connections & Streaming Restore ==="
if [[ "${MODE}" == "full" ]]; then
  kube_exec env PGUSER=postgres PGDATABASE=postgres psql -v ON_ERROR_STOP=1 -U postgres -d postgres -c \
    "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '${TARGET_DB}' AND pid <> pg_backend_pid();" >/dev/null
  kube_exec env PGUSER=postgres PGDATABASE="${TARGET_DB}" psql -v ON_ERROR_STOP=1 -U postgres -d "${TARGET_DB}" -c \
    "DROP SCHEMA IF EXISTS public CASCADE; CREATE SCHEMA public AUTHORIZATION ${TARGET_ROLE}; GRANT ALL ON SCHEMA public TO ${TARGET_ROLE};" >/dev/null
fi

if [[ "${ARCHIVE_FORMAT}" == "custom" ]]; then
  if [[ "${MODE}" == "full" ]]; then
    kube_exec_stdin env PGUSER=postgres PGDATABASE="${TARGET_DB}" \
      pg_restore -U postgres -d "${TARGET_DB}" --no-owner --no-privileges --role="${TARGET_ROLE}" --clean --if-exists \
      < "${PAYLOAD_FILE}"
  else
    kube_exec_stdin env PGUSER="${TARGET_ROLE}" PGDATABASE="${TARGET_DB}" \
      pg_restore -U "${TARGET_ROLE}" -d "${TARGET_DB}" --no-owner --no-privileges --role="${TARGET_ROLE}" --data-only --disable-triggers --exit-on-error \
      < "${PAYLOAD_FILE}"
  fi
else
  sed -E '/^(ALTER .* OWNER TO|GRANT |REVOKE )/d' "${PAYLOAD_FILE}" \
    | kube_exec_stdin env PGUSER="${TARGET_ROLE}" PGDATABASE="${TARGET_DB}" \
      psql -v ON_ERROR_STOP=1 -U "${TARGET_ROLE}" -d "${TARGET_DB}"
fi

echo "=== [4/5] Enforcing Role Ownership & Privileges (${TARGET_ROLE}) ==="
kube_exec_stdin env PGUSER=postgres PGDATABASE="${TARGET_DB}" psql -v ON_ERROR_STOP=1 -U postgres -d "${TARGET_DB}" <<EOSQL
DO \$\$
DECLARE
  r RECORD;
BEGIN
  FOR r IN SELECT tablename FROM pg_tables WHERE schemaname = 'public' LOOP
    EXECUTE format('ALTER TABLE public.%I OWNER TO %I', r.tablename, '${TARGET_ROLE}');
  END LOOP;
  FOR r IN SELECT sequencename FROM pg_sequences WHERE schemaname = 'public' LOOP
    EXECUTE format('ALTER SEQUENCE public.%I OWNER TO %I', r.sequencename, '${TARGET_ROLE}');
  END LOOP;
  FOR r IN SELECT viewname FROM pg_views WHERE schemaname = 'public' LOOP
    EXECUTE format('ALTER VIEW public.%I OWNER TO %I', r.viewname, '${TARGET_ROLE}');
  END LOOP;
END
\$\$;
GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA public TO ${TARGET_ROLE};
GRANT ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public TO ${TARGET_ROLE};
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON TABLES TO ${TARGET_ROLE};
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON SEQUENCES TO ${TARGET_ROLE};
EOSQL

echo "=== [5/5] Verifying Restored Tables in ${TARGET_DB} ==="
kube_exec env PGUSER="${TARGET_ROLE}" PGDATABASE="${TARGET_DB}" psql -U "${TARGET_ROLE}" -d "${TARGET_DB}" -c '\dt'

echo "=== Migration Completed Successfully for ${TARGET_DB} (role: ${TARGET_ROLE}) ==="

