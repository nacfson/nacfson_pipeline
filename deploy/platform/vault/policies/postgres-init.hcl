# OpenBao Policy: Database Provisioning Job
# Read-only access to database role passwords and admin password for provisioning.

path "kv/data/database/*" {
  capabilities = ["read"]
}
