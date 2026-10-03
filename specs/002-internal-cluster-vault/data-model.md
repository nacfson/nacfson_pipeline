# Data Model: Internal Cluster Vault

**Feature**: `specs/002-internal-cluster-vault`  
**Date**: 2026-10-03  
**Status**: Phase 1 Design

---

## 1. Entity Definitions & Schemas

### 1.1 Credential Version
Represents an immutable revision of a backend credential stored inside the Vault engine.

```json
{
  "name": "string (e.g. 'ghcr-pull-token', 'postgres-pn-user')",
  "owner": "string (e.g. 'platform', 'project-pn')",
  "version": "integer (positive, monotonically increasing)",
  "state": "enum ('staged', 'verified_active', 'superseded', 'revoked')",
  "created_at": "ISO-8601 timestamp",
  "created_by": "string (operator identity)",
  "verified_at": "ISO-8601 timestamp | null",
  "revoked_at": "ISO-8601 timestamp | null",
  "revocation_reason": "string | null",
  "encrypted_payload": "bytes (ciphertext managed by OpenBao KV v2 engine)"
}
```

* **Invariants**:
  * Versions are immutable. Once written, a version's payload cannot be overwritten.
  * Exactly one version per credential may hold `state: "verified_active"` at any time.
  * A `revoked` version can never return to `verified_active`.

---

### 1.2 Caller Identity Token
Represents a short-lived proof of identity presented by a workload or node to request an authorized operation.

```json
{
  "iss": "https://auth.internal.platform.svc",
  "sub": "string (e.g. 'project:pn:workload:backend', 'node:vps-node-01')",
  "aud": "vault.platform.svc",
  "iat": "Unix timestamp",
  "exp": "Unix timestamp (<= iat + 900, max 15 minutes)",
  "jti": "UUIDv4 (unique token identifier)"
}
```

* **Invariants**:
  * Must be signed by the trusted platform identity key (verified via public JWKS).
  * Expiration is strictly enforced on every request.
  * Possessing a token grants zero permissions by default; access requires an explicit **Access Grant**.

---

### 1.3 Access Grant
Represents a revocable authorization mapping a verified caller identity to permitted operations and targets.

```json
{
  "grant_id": "string (e.g. 'grant-pn-backend-db')",
  "caller_pattern": "string (e.g. 'project:pn:workload:backend')",
  "allowed_operations": ["enum ('db.query', 'ghcr.pull', 'transit.sign')"],
  "target_scope": {
    "database": "string (e.g. 'pn_production')",
    "enforced_db_role": "string (e.g. 'pn_app_user')",
    "allowed_repositories": ["string (e.g. 'nacfson/pn-backend')"]
  },
  "status": "enum ('active', 'revoked')",
  "granted_by": "string (operator identity)",
  "granted_at": "ISO-8601 timestamp"
}
```

* **Invariants**:
  * Deny by default: If no active grant matches caller and target, the request fails closed.
  * Role escalation is impossible: The caller-supplied target cannot widen `enforced_db_role`.

---

### 1.4 Operation Binding
Connects an operation to the internal backend credential reference used to execute it upstream.

```json
{
  "binding_id": "string (e.g. 'bind-ghcr-pull')",
  "operation": "ghcr.pull",
  "target_endpoint": "https://ghcr.io",
  "internal_credential_ref": "ghcr-pull-token",
  "active_version": "integer (references Credential Version)",
  "input_constraints": {
    "require_immutable_digest": true,
    "allowed_methods": ["GET", "HEAD"]
  },
  "result_contract": {
    "strip_auth_headers": true,
    "payload_type": "application/vnd.oci.image.manifest.v1+json"
  }
}
```

---

### 1.5 Audit Event ([FR-012](file:///Users/hyungjuyu/Projects/Brain/nacfson_pipeline/specs/002-internal-cluster-vault/spec.md#L136))
Attributable, value-free record of an attempted management or operation execution.

```json
{
  "timestamp": "ISO-8601 timestamp",
  "request_id": "UUIDv4",
  "actor": "string (verified caller or operator identity)",
  "operation": "string (e.g. 'ghcr.pull', 'db.query', 'secret.rotate')",
  "target": "string (non-sensitive target metadata)",
  "credential_version_used": "integer | null",
  "outcome": "enum ('ALLOWED', 'DENIED', 'ERROR')",
  "error_reason": "string (non-sensitive classification) | null"
}
```

* **Invariants**:
  * **Zero Secret Values**: Must never contain passwords, tokens, private keys, or sensitive query result payloads.
  * **Fail-Closed Storage**: If the audit event cannot be durably flushed, the operation is rejected before external execution.

---

### 1.6 Recovery Set ([FR-013](file:///Users/hyungjuyu/Projects/Brain/nacfson_pipeline/specs/002-internal-cluster-vault/spec.md#L137))
The complete bundle required to restore the service after total storage loss.

* **Encrypted Snapshot**: Compressed Raft database snapshot (`vault-YYYY-MM-DD.snap`).
* **Recovery Material**: Master Unseal Key held offline in operator custody.
* **Reconciliation Record**: A log of any credentials rotated or revoked since the snapshot timestamp.
