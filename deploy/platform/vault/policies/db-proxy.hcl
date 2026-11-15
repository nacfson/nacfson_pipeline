path "kv/data/database/pn-user" {
  capabilities = ["read"]
}

path "kv/data/database/health-user" {
  capabilities = ["read"]
}

path "kv/data/database/keycloak-user" {
  capabilities = ["read"]
}

path "kv/metadata/database/*" {
  capabilities = ["read", "list"]
}
