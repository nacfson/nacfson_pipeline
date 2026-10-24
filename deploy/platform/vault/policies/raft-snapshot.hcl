# OpenBao Policy: Encrypted Raft Snapshot Backup (openbao-backup)
# Grants read-only access to the AES-256-GCM encrypted Raft snapshot stream.
# Zero access to kv/* plaintext secrets, transit/* keys, or sys/policies/*.

path "sys/storage/raft/snapshot" {
  capabilities = ["read"]
}
