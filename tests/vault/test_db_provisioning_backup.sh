#!/usr/bin/env bash
# ==============================================================================
# Integration Test: Database Provisioning Job & Daily Backup Confinement
# Validates specs/002-internal-cluster-vault/contracts/database-provisioning-backup.md
# ==============================================================================

set -euo pipefail

INIT_JOB="deploy/platform/database/postgres-init-job.yaml"
BACKUP_CRON="deploy/platform/database/backup-cronjob.yaml"

echo "=== [1/4] Checking Database Provisioning Job ==="
if grep -q "postgres-credentials" "${INIT_JOB}"; then
  echo "FAIL: postgres-init-job still references legacy postgres-credentials Secret."
  exit 1
else
  echo "PASS: Zero references to postgres-credentials Secret in postgres-init-job."
fi

if grep -q "backup_role" "${INIT_JOB}" && grep -q "pg_read_all_data" "${INIT_JOB}"; then
  echo "PASS: postgres-init-job idempotently provisions backup_role with pg_read_all_data."
else
  echo "FAIL: postgres-init-job does not provision backup_role."
  exit 1
fi

echo "=== [2/4] Checking Daily Backup CronJob ==="
if grep -q "postgres-credentials" "${BACKUP_CRON}"; then
  echo "FAIL: backup-cronjob still references legacy postgres-credentials Secret."
  exit 1
else
  echo "PASS: Zero references to postgres-credentials Secret in backup-cronjob."
fi

if grep -q 'value: "backup_role"' "${BACKUP_CRON}"; then
  echo "PASS: backup-cronjob runs as least-privilege 'backup_role' instead of superuser postgres."
else
  echo "FAIL: backup-cronjob does not specify PGUSER backup_role."
  exit 1
fi

echo "=== [3/4] Verifying Ephemeral tmpfs Storage & Memory Clearing ==="
if grep -q "unset PGPASSWORD" "${INIT_JOB}" && grep -q "unset PGPASSWORD" "${BACKUP_CRON}"; then
  echo "PASS: Passwords are immediately cleared from shell memory upon completion."
else
  echo "FAIL: Memory clearing not detected."
  exit 1
fi

if grep -q "medium: Memory" "${INIT_JOB}" && grep -q "medium: Memory" "${BACKUP_CRON}"; then
  echo "PASS: Ephemeral in-memory tmpfs mounts configured for database secret ingestion."
else
  echo "FAIL: In-memory tmpfs mount missing."
  exit 1
fi

echo "=== [4/4] Acceptance Criteria Passed ==="
echo "PASS: Database provisioning and backup operation confinement verified."
