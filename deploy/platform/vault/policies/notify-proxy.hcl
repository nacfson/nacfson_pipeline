# OpenBao Policy: Notify Proxy (vault-notify-proxy)
# Grants access to the Discord webhook URLs the proxy posts with. The proxy is
# the only component that reads them, and it never writes them into its own
# logs or /admin/status output.

path "kv/data/notify/*" {
  capabilities = ["create", "read", "update"]
}

path "kv/metadata/notify/*" {
  capabilities = ["read", "list"]
}
