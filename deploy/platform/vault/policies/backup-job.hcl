# OpenBao Policy: Database Daily Backup Job
# Least-privilege read access strictly to dedicated backup_role credentials.

path "kv/data/database/backup-user" {
  capabilities = ["read"]
}
