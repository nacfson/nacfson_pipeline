# OpenBao Policy: Internal AI Gateway Proxy (vault-ai-proxy)
# Grants access to the upstream Gemini API key and per-project kill-switch state.

path "kv/data/ai/*" {
  capabilities = ["create", "read", "update"]
}

path "kv/metadata/ai/*" {
  capabilities = ["read", "list"]
}
