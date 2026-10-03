# OpenBao Policy: Database Query Proxy
# Least-privilege read access strictly to database user credentials.

path "kv/data/database/pn-user" {
  capabilities = ["read"]
}

path "kv/data/database/keycloak-user" {
  capabilities = ["read"]
}

path "kv/metadata/database/*" {
  capabilities = ["read", "list"]
}
