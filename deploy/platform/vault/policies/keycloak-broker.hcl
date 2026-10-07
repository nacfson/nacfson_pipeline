# OpenBao Policy: Keycloak Identity Provider & Datasource
# Least-privilege read access to Google OAuth broker secrets and Keycloak DB password.

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
