path "kv/data/identity/google-oauth" {
  capabilities = ["read"]
}

path "kv/data/database/keycloak-user" {
  capabilities = ["read"]
}

path "kv/data/gateway/*" {
  capabilities = ["read"]
}

path "kv/data/projects/pn/*" {
  capabilities = ["read"]
}

path "kv/data/projects/health/*" {
  capabilities = ["read"]
}
