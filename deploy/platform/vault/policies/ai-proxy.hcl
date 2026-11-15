path "kv/data/ai/*" {
  capabilities = ["create", "read", "update"]
}

path "kv/metadata/ai/*" {
  capabilities = ["read", "list"]
}
