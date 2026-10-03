# Contract: Database Proxy Integration

**Service**: `vault-database-proxy`  
**Endpoint**: `tcp://vault-db-proxy.vault.svc:5432`  
**Consumer**: Project Workload Pods (e.g. `pn-backend`)  
**Upstream**: Standard PostgreSQL StatefulSet (`postgres.platform.svc:5432`)  

---

## 1. Authentication Contract (Pod ➔ DB Proxy)

Pods connect using the PostgreSQL wire protocol (v3.0).

* **Handshake**:
  * Pod sends StartupMessage with:
    * `database`: Target database (e.g. `pn_production`).
    * `user`: Platform workload identity (e.g. `project:pn:workload:backend`).
  * Proxy responds with AuthenticationRequest (Password / Cleartext or SASL).
  * Pod sends its **Caller Identity Token** (signed short-lived JWT) in the password field.

---

## 2. Proxy Validation & Role Mapping (Inside Trusted Boundary)

1. **Verify Token**: Proxy validates JWT signature against Platform Identity JWKS, checks `exp` (unexpired), and confirms `aud == "vault.platform.svc"`.
2. **Access Grant Lookup**: Proxy verifies that `sub` has an active grant for target `database: pn_production`.
3. **Restricted Role Binding**: Proxy fetches the dedicated restricted database role and password for this project from OpenBao (e.g. user `pn_app_user` with password stored in OpenBao).
4. **Upstream Connection**: Proxy opens or reuses a pooled connection to PostgreSQL authenticated as `pn_app_user`.

---

## 3. Query Execution Contract

* **Permitted Operations**: Standard SQL `SELECT`, `INSERT`, `UPDATE`, `DELETE`, transactions (`BEGIN`, `COMMIT`, `ROLLBACK`), parameterized queries.
* **Prohibited Operations**:
  * Any `SET ROLE` or `RESET ROLE` attempting to escape the bound role.
  * Schema migrations / DDL commands (`DROP`, `ALTER`, `CREATE`) on normal application connections (migrations must use separate administrative bindings).
  * Queries targeting peer project tables or `keycloak_db`.
* **Revocation Invariant ([FR-020](file:///Users/hyungjuyu/Projects/Brain/nacfson_pipeline/specs/002-internal-cluster-vault/spec.md#L145))**:
  * If a pod's access grant is revoked, the proxy immediately terminates its connection pool or rejects the *next* query on the existing connection with SQLSTATE `28000` (Invalid Authorization).

---

## 4. Platform Services Binding (Non-Circular Bootstrap)

Platform infrastructure components (Keycloak, Vault internal jobs) do NOT depend on runtime workload tokens:
* **Keycloak Binding**: Keycloak connects to its database (`keycloak_db`) using static platform credentials provisioned directly from OpenBao at bootstrap.
* **Separation**: Keycloak is an upstream consumer of the database, not an identity provider for the database proxy.
