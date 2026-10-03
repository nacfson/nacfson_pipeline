# OpenBao Policy: Go Authentication Gateway
# Least-privilege read access to gateway secrets & transit signing.

path "kv/data/gateway/*" {
  capabilities = ["read"]
}

path "transit/sign/gateway-session-key" {
  capabilities = ["update"]
}

path "transit/verify/gateway-session-key" {
  capabilities = ["update"]
}

path "transit/hmac/gateway-session-hmac" {
  capabilities = ["update"]
}
