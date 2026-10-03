# OpenBao Policy: Registry Pull Proxy
# Least-privilege read access strictly to GHCR pull token.

path "kv/data/platform/ghcr-pull-token" {
  capabilities = ["read"]
}

path "kv/metadata/platform/ghcr-pull-token" {
  capabilities = ["read", "list"]
}
