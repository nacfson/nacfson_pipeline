# OpenBao Policy: PostgreSQL StatefulSet Service
# Least-privilege read access strictly to PostgreSQL administrator initialization password.

path "kv/data/database/postgres-admin" {
  capabilities = ["read"]
}
