path "kv/data/notify/*" {
  capabilities = ["create", "read", "update"]
}

path "kv/metadata/notify/*" {
  capabilities = ["read", "list"]
}
