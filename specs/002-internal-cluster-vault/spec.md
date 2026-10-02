# Feature Specification: Internal Cluster Vault

**Feature Branch**: `main` (existing branch; no branch-creation hook configured)

**Created**: 2026-10-02

**Status**: Ready for planning

**Input**: User description: "I want to build our own internal cluster vault system into our kubernetes system."

## Clarifications

### Session 2026-10-03

- Q: Does “our own” mean operating an established vault product or developing a custom vault service? A: Self-host an established vault. The organization owns deployment, data, access policies, and cluster integration; product selection belongs to planning. Developing a custom vault security engine is out of scope.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Manage Secrets Privately (Priority: P1)

As a platform operator, I want a centrally managed internal vault for platform and project secrets so that sensitive values are not maintained in source-controlled deployment files or shared through messages.

**Why this priority**: Private storage and controlled access are the core value of the vault.

**Independent Test**: An authorized operator creates, reads, and updates a synthetic secret through private access; an unauthorized identity and an unauthenticated public client cannot retrieve it.

**Acceptance Scenarios**:

1. **Given** an authenticated operator with management permission for a project, **When** they create a named secret and update its value, **Then** the vault retains both versions, identifies the current version, and returns values only to identities with explicit read permission.
2. **Given** a project-scoped operator, **When** they attempt to read, list, modify, or grant access to another project's secrets or platform credentials, **Then** access is denied without revealing secret values or names outside their scope.
3. **Given** a client without private cluster access, **When** they attempt to reach vault management, health, or secret-access functions, **Then** none is publicly accessible.
4. **Given** stored secrets, **When** vault processes are replaced, **Then** acknowledged versions and access rules survive and remain protected.

---

### User Story 2 - Deliver Secrets Without Weakening Workload Isolation (Priority: P1)

As a platform operator, I want to deliver only approved secrets to each workload so that applications can use the vault-managed credentials without gaining broader platform access.

**Why this priority**: A vault must serve the existing platform while preserving its isolation and application configuration contracts.

**Independent Test**: Register synthetic credentials for two projects, deliver each to its authorized consumer, and verify that neither consumer receives the other's credentials or vault-administration privileges.

**Acceptance Scenarios**:

1. **Given** an approved secret version and a declared consumer, **When** an authorized operator provisions it, **Then** the consumer receives the intended value using its existing secret reference without application source changes or additional control-plane privileges.
2. **Given** an unknown secret, missing version, denied access, or unavailable vault, **When** delivery is requested, **Then** delivery reports failure, does not substitute an empty or default value, and does not overwrite an existing delivered value.
3. **Given** an existing platform credential, **When** it is migrated into the vault, **Then** the operator verifies its consumer before retiring the previous management procedure, and exactly one authoritative management procedure remains after cutover.
4. **Given** a clean cluster with no functioning application identity service, **When** the operator follows bootstrap instructions, **Then** they can initialize the vault and deliver the credentials needed to start the identity service without depending on that service already running.

---

### User Story 3 - Rotate and Revoke Secrets Deliberately (Priority: P2)

As a platform operator, I want explicit secret-version, delivery, and revocation state so that credential changes do not silently leave consumers using the wrong value.

**Why this priority**: Rotation must distinguish changing a stored value from updating its consumers and invalidating a credential at its issuer.

**Independent Test**: Rotate a synthetic credential for a representative consumer, observe a failed delivery without losing the previous value, complete delivery, and verify revocation at the credential's issuing service.

**Acceptance Scenarios**:

1. **Given** a current credential and registered consumers, **When** an operator stages a replacement, **Then** the previous version remains identifiable and each consumer's delivery and adoption status is visible without displaying values.
2. **Given** a consumer that requires a restart to adopt a new value, **When** a replacement is delivered but not yet adopted, **Then** the rotation remains incomplete until adoption is verified.
3. **Given** a revoked vault access grant, **When** the identity makes its next vault request, **Then** it is denied; revocation does not claim to erase values already obtained.
4. **Given** a credential declared compromised, **When** an operator completes the revocation procedure, **Then** new vault delivery of that version is blocked and the issuing service rejects the old credential; incomplete issuer revocation is reported as incomplete.
5. **Given** concurrent edits based on the same version, **When** one succeeds, **Then** the other reports a conflict rather than silently overwriting the successful update.

---

### User Story 4 - Audit and Recover the Vault (Priority: P2)

As a platform operator, I want attributable secret operations and recoverable protected backups so that I can investigate access and recover after cluster storage loss.

**Why this priority**: Centralizing secrets also centralizes the consequences of loss or unauthorized access.

**Independent Test**: Use synthetic secrets to exercise allowed and denied operations, inspect audit records for attribution and absence of values, then restore a protected backup into a clean environment using separately held recovery material.

**Acceptance Scenarios**:

1. **Given** successful and denied secret reads, writes, deliveries, access-rule changes, and recovery actions, **When** an authorized reviewer examines audit records, **Then** each event identifies its actor, time, action, permitted target identifier, and outcome without containing secret values or authentication credentials.
2. **Given** the vault cannot durably record an operation's audit event, **When** a read, change, or delivery is attempted, **Then** it fails closed rather than completing without an audit record.
3. **Given** loss of the vault's local storage, **When** an authorized operator restores the latest backup with separately held recovery material, **Then** secret versions and access rules match the backup, previously revoked versions are not delivered until revocation state has been reconciled, and recovery does not require secrets available only inside the lost vault.
4. **Given** a backup but no authorized recovery material, **When** an unauthorized party examines it, **Then** secret values cannot be recovered in plaintext.
5. **Given** a candidate vault deployment, **When** its resource needs exceed available platform capacity or reduce protected existing allocations, **Then** deployment is rejected before it affects running projects.

### Edge Cases

- A locked, uninitialized, or unreachable vault denies new reads and deliveries. Previously delivered values may continue to function; their presence is not represented as current vault authorization.
- Replacing vault processes must not reset stored data, access rules, or initialization state.
- A secret with active consumers cannot be permanently deleted until an authorized operator explicitly confirms its affected consumers; the deletion and result are audited.
- Restoring an older backup can reintroduce obsolete access rules or credentials. Consumer delivery stays disabled until access and revocation state have been reconciled.
- Vault access revocation cannot recall a value already copied by a consumer; issuer-side credential invalidation is a separate required operational step.
- Recovery must remain possible when the identity service, private image registry credentials, or the original cluster are unavailable. Bootstrap dependencies must not form a cycle through the vault itself.
- Recovery material lost together with the vault makes recovery impossible; the operator must verify separate custody before declaring recovery readiness.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The platform MUST provide a self-hosted, established vault product for platform and project secrets, operated within the organization's cluster and under its control of data and access policies. Developing a custom vault security engine is out of scope. Acceptance: an operator completes Story 1 using the internally operated product, without relying on an externally hosted secret-management service.
- **FR-002**: The vault MUST support named secrets, explicit project or platform ownership, immutable value versions, and an identifiable current version. Concurrent updates MUST reject stale changes rather than silently lose updates. Acceptance: Stories 1.1 and 3.5.
- **FR-003**: Vault access MUST require an authenticated identity and explicit action-and-scope grants, deny access by default, and keep secret-value read permissions distinct from permission to manage access rules. Project-scoped grants MUST NOT permit access to platform or peer-project secrets. Acceptance: Stories 1.1–1.2.
- **FR-004**: Vault secret access, administration, health, and operational functions MUST be private and MUST NOT be exposed through public ingress. Acceptance: Story 1.3.
- **FR-005**: Secret values MUST be encrypted in persistent storage, backups, and transit, and MUST NOT appear in source control, deployment configuration values, build or operational logs, audit records, or shared messages. Authorized entry, retrieval, and workload delivery are the only permitted value-disclosure paths. Acceptance: Stories 1.1, 4.1, and 4.4, plus synthetic-value inspection of release and operational artifacts.
- **FR-006**: Acknowledged secret versions, ownership, and access rules MUST survive ordinary vault process replacement without reinitialization. Acceptance: Story 1.4.
- **FR-007**: Operators MUST explicitly approve secret delivery to named consumers within an authorized scope. Delivery MUST preserve existing application secret references and MUST NOT grant project workloads additional platform privileges. Acceptance: Stories 2.1–2.2.
- **FR-008**: New secret retrieval or delivery MUST fail closed on unavailable vault state, denied authorization, or missing values. Failed delivery MUST leave existing consumer values unchanged and report a non-sensitive reason. Acceptance: Story 2.2 and the unavailable-vault edge case.
- **FR-009**: Operators MUST be able to stage a new version, identify affected consumers, and distinguish stored, delivered, adopted, and revoked states. Rotation MUST NOT be reported complete until all selected consumers have verified adoption and any required issuer-side invalidation is verified. Acceptance: Stories 3.1–3.4.
- **FR-010**: Access-grant revocation MUST deny the next vault request by that identity. Credential revocation MUST block further delivery of the revoked version and explicitly track issuer-side invalidation separately from vault revocation. Acceptance: Stories 3.3–3.4.
- **FR-011**: Permanent deletion MUST require explicit authorized confirmation of affected consumers and MUST produce an audit record. Active consumers MUST NOT be silently orphaned. Acceptance: the active-consumer deletion edge case.
- **FR-012**: All successful and denied secret operations, deliveries, access-rule changes, and bootstrap or recovery actions MUST have durable audit records containing actor, timestamp, action, authorized target metadata, and outcome, without values or authentication credentials. Reads, changes, and deliveries MUST fail closed if their audit record cannot be persisted. Acceptance: Stories 4.1–4.2.
- **FR-013**: The vault MUST support protected backups and a documented clean-environment restoration procedure for secret versions and access rules. Recovery material MUST be held independently of the vault and its storage. A successful restore exercise MUST precede declaring recovery readiness. Acceptance: Stories 4.3–4.4.
- **FR-014**: Restoration MUST keep consumer delivery disabled until the operator has reconciled access grants and revoked versions against the recovery record. Recovery MUST NOT silently reactivate obsolete credentials. Acceptance: Story 4.3 and the older-backup edge case.
- **FR-015**: The operator MUST be able to bootstrap and recover the vault without a working application identity service or credentials obtainable only from the uninitialized or lost vault. Routine operation MUST NOT retain unrestricted bootstrap access. Acceptance: Story 2.4 and a post-bootstrap attempt to reuse initial unrestricted access, which must be denied.
- **FR-016**: Adoption MUST inventory and migrate existing platform and project credentials, verify each affected consumer, and retire superseded manual value-management procedures after successful cutover. Non-secret deployment declarations remain version-controlled. Acceptance: Story 2.3.
- **FR-017**: Vault operations MUST fit within measured platform capacity without violating existing project isolation, resource reservations, or the memory-limit freeze. Environment changes MUST NOT require application source changes. Acceptance: Stories 2.1 and 4.5, evaluated against the supported environment profiles.

### Key Entities

- **Secret**: A named sensitive value owned by one project or a platform operational role, with version history and a lifecycle state.
- **Secret Version**: An immutable value revision with creation time, creator, and eligibility for delivery.
- **Access Grant**: Permission for an authenticated identity to perform explicit actions on a specified secret scope; revocable independently of the secret.
- **Consumer Binding**: An approved association between a secret and its intended workload or platform duty, recording selected, delivered, and verified-adopted versions without exposing values.
- **Audit Event**: A durable, attributable record of an attempted operation and its outcome, excluding sensitive values.
- **Recovery Set**: A protected backup, separately held recovery material, and the access/revocation record needed to restore safely.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: An authorized operator can store a synthetic secret and make it usable by one preconfigured consumer within 10 minutes using the documented procedure, without editing application source or placing the value in source control.
- **SC-002**: All unauthorized cases in an acceptance matrix covering unauthenticated, wrong-project, wrong-action, and revoked identities deny access; no unauthorized values or secret names are disclosed.
- **SC-003**: For every credential in the migration inventory, the intended consumer passes its functional check after cutover, and exactly one authoritative secret-management procedure remains.
- **SC-004**: A rotation exercise accurately identifies adoption for every selected consumer, preserves existing values on failed delivery, and demonstrates rejection of the old credential after issuer-side invalidation.
- **SC-005**: Inspection of release artifacts, logs, and audit records finds zero occurrences of the synthetic secret values; every exercised successful or denied secret operation has an attributable audit record.
- **SC-006**: Ordinary vault process replacement preserves 100% of acknowledged synthetic secret versions and grants. A clean-environment restore reproduces 100% of versions and grants in the selected backup, with obsolete access reconciled before delivery resumes.
- **SC-007**: A platform operator completes the documented bootstrap, rotation, and recovery exercises without undocumented assistance and can correctly identify whether a consumer is using the current approved version.

## Assumptions

- "Vault" means management of application and platform secrets, not general file storage or a personal password manager. Intended users are platform operators; application users receive no vault privileges through ordinary application login.
- The initial deployment boundary is one existing cluster, including its platform services and hosted projects. Multi-cluster replication and multi-node high availability are not assumed requirements; single-node host loss is addressed through recovery rather than a promise of uninterrupted availability.
- Initial managed values are operator-supplied credentials and keys already used by the platform, including database, identity-provider, gateway, and image-registry credentials. Automatic issuance of short-lived credentials, certificate-authority services, and general-purpose encryption services are not assumed requirements.
- Existing constitution v1.2.0 remains authoritative: consumer delivery stays operator-provisioned through named Kubernetes Secrets; project pods gain neither service-account tokens nor Kubernetes API access. Unattended delivery or direct workload authentication that violates those rules requires a separately approved governance change, not an implicit exception in this feature.
- Secret delivery preserves the existing consumer contract; this feature does not assume live reload. An operator-controlled restart and verification may be required for adoption.
- Deployment remains portable across the repository's local k3s, single-VPS k3s, EKS, and GKE profiles. No particular vault product, storage engine, language, or delivery component is selected by this specification.
- Operators have private cluster access, authority to provision consumer secrets, access to credential issuers for invalidation, and an independent protected location for recovery material and backups.
- No new regulatory retention obligation is inferred. Backup scheduling and audit retention must follow an explicit operator-selected policy; the existing optional backup baseline is daily at 00:00 UTC with seven-day retention. Recovery readiness requires a tested backup regardless of scheduling choice.
- Threat boundaries include unauthorized users, project workloads, and access to stored data or backups without recovery keys. Protection against an attacker already controlling the cluster host, control plane, or an authorized consumer's memory is not claimed.
