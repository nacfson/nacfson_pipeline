# Feature Specification: Automated Post-Vault Workload Orchestration

**Feature Branch**: `005-post-vault-automation`

**Created**: 2026-10-05

**Status**: Draft

**Input**: User description: "Like you said, I want to automate entire tasks after the vault manual setup."

---

## Clarifications

### Session 2026-10-05

- Q: How should application pods authenticate to Vault to retrieve their credentials at startup? → A: Kubernetes ServiceAccount tokens validated by Vault via TokenReview API (Option A).
- Q: How should proxy container images (vault-db-proxy and vault-registry-proxy) be provisioned to unblock the proxy layer during post-vault automation? → A: GitHub Actions CI workflow building multi-arch images and publishing to GHCR with immutable digests (Option B).
- Q: Where should automated daily encrypted backups of Vault and PostgreSQL be stored? → A: Local Persistent Volume Claim (PVC) with automated 7-day retention pruning (Option A).

---

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Hands-Off Workload Credential Provisioning & Boot (Priority: P1)

As a platform operator, I want workloads (database, identity, and application gateways) to automatically retrieve their required runtime credentials from the internal secret store upon booting, so that services start cleanly without manual file copying, manual script execution, or plaintext credentials stored on disk.

**Why this priority**: Eliminates the primary failure mode where pods fail to start or enter crash loops waiting for manual password delivery, ensuring zero-touch workload lifecycles once the secret store is active.

**Independent Test**: Deploy or restart the database and identity workloads while the secret store is in an active, unsealed state. Verify that all pods boot to a healthy, ready status without requiring any manual file creation or script execution.

**Acceptance Scenarios**:

1. **Given** an unsealed internal secret store containing managed credentials, **When** the database service pod boots for the first time or restarts, **Then** it automatically retrieves its administrative password into ephemeral in-memory storage, completes initialization, and enters a healthy ready state without writing credentials to persistent disk.
2. **Given** a healthy database service, **When** the database initialization task executes, **Then** it automatically retrieves required role credentials from the secret store, provisions isolated tenant databases and roles, and exits successfully without manual intervention.
3. **Given** provisioned tenant databases, **When** identity and authentication gateway services start, **Then** they automatically ingest their database and client credentials from the secret store and begin serving authenticated traffic.

---

### User Story 2 - Autonomous GitOps Cascade & Convergence (Priority: P2)

As a platform operator, I want the continuous deployment reconciler to automatically progress through and converge all downstream platform layers once the secret store is unsealed, so that the entire platform transitions to an operational state without step-by-step operator commands.

**Why this priority**: Honors Constitution Principle I (Continuous GitOps as Single Source of Truth) by ensuring that the manual unsealing ceremony is the sole human intervention, and all subsequent reconciliation occurs autonomously.

**Independent Test**: Observe deployment controller status immediately following secret store unseal. Verify that all 10 platform layers sequentially transition from pending/blocked to ready status within a bounded timeframe.

**Acceptance Scenarios**:

1. **Given** the internal secret store transitions to ready, **When** the deployment reconciler evaluates layer dependencies, **Then** it automatically unblocks and reconciles the database, identity, ingress, and tenant project layers in strict dependency order.
2. **Given** transient startup dependencies or temporary network delays between dependent components, **When** an affected service retries, **Then** it fails closed safely, applies exponential backoff, and converges to ready as soon as the prerequisite service responds.
3. **Given** a complete platform deployment, **When** an operator inspects cluster-wide reconciliation status, **Then** 100% of managed platform layers report active and healthy status with zero manual overrides.

---

### User Story 3 - Automated Lifecycle Maintenance & Disaster Readiness (Priority: P3)

As a platform operator, I want scheduled credential rotation and automated encrypted backups to run without ongoing human effort, so that the platform maintains high security hygiene and disaster recovery readiness automatically.

**Why this priority**: Prevents credential staleness, limits blast radiuses, and guarantees recoverable system states while eliminating recurring manual operational chores.

**Independent Test**: Trigger a scheduled rotation cycle and a backup verification task. Verify that credentials rotate with zero service downtime and encrypted snapshots are exported and verified without manual intervention.

**Acceptance Scenarios**:

1. **Given** active platform services, **When** a scheduled credential rotation cycle triggers, **Then** cryptographic signing keys and database passwords rotate transparently without terminating active user sessions or dropping in-flight database transactions.
2. **Given** running platform storage, **When** a scheduled backup window occurs, **Then** an encrypted snapshot of secret storage and database state is captured, validated for restoration integrity, and pruned according to retention policies.

---

### Edge Cases

- **Secret Store Sealed or Unavailable at Boot**: Dependent workloads must fail closed, emit non-sensitive status diagnostics, and retry with backoff. They must never fall back to insecure default passwords or unauthenticated modes. As soon as the store is unsealed, workloads must recover automatically.
- **Node Reboot or Cold Restart**: Following a node restart, once the operator supplies the master unseal key to the secret store, all platform services, databases, and tenant workloads must self-heal and converge in dependency order with zero additional manual commands.
- **In-Flight Rotation During Active Queries**: The secret rotation mechanism must support dual-key or staged adoption so that existing database connections and active user authentication sessions remain valid while newly established sessions adopt the updated credential.
- **Unauthorized Secret Access Attempts**: If an unauthorized workload or actor attempts to query credentials outside its assigned scope, the secret store must deny the request, log an attributable security audit event, and disclose zero credential names or values.

---

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The system MUST automatically authenticate dependent workloads (including PostgreSQL, Keycloak, and Authentication Gateway) to the secret store using projected Kubernetes ServiceAccount tokens validated via the Kubernetes TokenReview API, delivering runtime credentials upon pod startup without requiring manual file placement, static bootstrap tokens, or operator CLI execution.
- **FR-002**: Workload credential delivery MUST use ephemeral in-memory storage that is accessible only to the authorized container and is automatically destroyed upon container termination.
- **FR-003**: Plaintext credentials MUST NOT be written to persistent disk volumes, committed to Git repositories, stored in Kubernetes Secret manifests, or output into system and build logs.
- **FR-004**: The database provisioning task MUST automatically authenticate to the secret store, retrieve role passwords, idempotently provision tenant databases and access controls, and exit cleanly without operator intervention.
- **FR-005**: All platform layers downstream of the secret store MUST automatically cascade and converge to a ready status in dependency order once the secret store achieves unsealed readiness.
- **FR-006**: Dependent workloads MUST fail closed with non-sensitive diagnostic messages when the secret store is sealed or unreachable, automatically resuming operation once the store is unsealed.
- **FR-007**: The platform MUST execute automated zero-downtime secret rotation on a scheduled basis, preserving active user sessions and database connections across rotation events.
- **FR-008**: The platform MUST automatically generate, verify, and retain encrypted backups of secret storage and database state on dedicated local persistent volume claims with automated 7-day retention pruning.
- **FR-009**: All credential delivery and automation components MUST strictly enforce the Kubernetes Restricted Pod Security profile and MUST NOT require privileged containers, host namespaces, or hostPath mounts.
- **FR-010**: All custom platform components (`vault-db-proxy`, `vault-registry-proxy`, and `platform-auth-gateway`) MUST be built as multi-architecture container images via an automated GitHub Actions workflow and published to GHCR with immutable digests.

---

### Key Entities

- **Workload Credential Binding**: A cryptographically verifiable authorization mapping an authenticated workload identity to the exact scoped credentials it is permitted to consume at runtime.
- **Ephemeral Secret Volume**: A dedicated in-memory, non-persistent volume mounted into a workload container with restrictive permissions (`0400`/`0600`) that exists exclusively during container execution.
- **Autonomous Convergence Cascade**: The sequential, dependency-ordered reconciliation of declarative platform layers that progresses without manual intervention once prerequisites are satisfied.
- **Rotation Epoch**: An identifiable lifecycle phase during which a credential transitions from active to retired while preserving dual-verification compatibility for in-flight operations.

---

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: Following the initial manual unsealing of the secret store, 100% of downstream platform layers and workloads achieve verified ready status within 10 minutes without any manual commands or script executions.
- **SC-002**: Zero occurrences of plaintext backend credentials appear in Git commits, persistent disk storage, container environment variables, or build/system logs across all automated operations.
- **SC-003**: Workloads automatically recover and achieve full operational readiness within 3 minutes of a pod restart or container crash, provided the secret store remains unsealed.
- **SC-004**: Scheduled credential rotation completes with a 100% request success rate, causing zero dropped user sessions and zero broken database transactions.
- **SC-005**: 100% of scheduled backup archives are verified as valid, restorable, and retained in compliance with the platform retention policy without manual operator verification.

---

## Assumptions

- The one-time manual initialization and unsealing of the OpenBao Vault engine is performed by an authorized operator out-of-band in strict compliance with Constitution Principle V.
- Workloads execute on Kubernetes nodes enforcing the Restricted Pod Security Admission profile with non-root security contexts.
- Resource allocations for automation helpers and workloads adhere to the deterministic 1/n project budgeting model (Constitution Principle IV).
- The cluster maintains continuous network connectivity to required container registries or local mirrors to pull standard application images.
