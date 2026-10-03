# Feature Specification: Minimal VM Bootstrap

**Feature Branch**: `main` (existing branch; no branch-creation hook configured)

**Created**: 2026-10-03

**Status**: Ready for planning

**Input**: Agreed conversation scope: use Ansible only to bootstrap an existing VM for K3s, with mandatory configuration only: VM address, SSH user using existing authentication, and a pinned K3s version; validate prerequisites, install/start the cluster, and verify readiness. Keep application deployment and Vault operations separate. See [bootstrap scope](../../docs/bootstrap-scope.md).

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Prepare an Existing VM (Priority: P1)

As a platform operator, I want to prepare an existing VM with one bootstrap invocation so that I can subsequently deploy my applications through the existing cluster configuration.

**Why this priority**: A usable cluster host is the complete initial deliverable; provisioning infrastructure and deploying applications are separate responsibilities.

**Independent Test**: Use a fresh VM matching the declared support profile, supply the three required settings and existing operator access, and verify that bootstrap produces a ready cluster without deploying project or shared platform workloads.

**Acceptance Scenarios**:

1. **Given** a supported VM with satisfied prerequisites, **When** the operator starts bootstrap, **Then** the selected cluster version is installed, its service is enabled to start on boot, and readiness is verified before success is reported.
2. **Given** successful bootstrap, **When** the operator checks the result, **Then** the cluster service is running, the management interface responds to an authorized check, the node reports ready, and the built-in name-resolution service is ready.
3. **Given** a fresh VM with no running vault or application identity service, **When** bootstrap runs, **Then** it completes using independently accessible installation artifacts without requesting or installing backend credentials.
4. **Given** completed bootstrap, **When** installed resources are inspected, **Then** only the base cluster and its default bundled components have been installed; project workloads, shared application services, and vault components have not been deployed.

---

### User Story 2 - Receive an Actionable Failure (Priority: P1)

As a platform operator, I want missing prerequisites and unsuccessful setup steps to be reported accurately so that I can correct the environment without mistaking an incomplete installation for a working cluster.

**Why this priority**: False success would make later deployment fail for reasons unrelated to its application configuration.

**Independent Test**: Exercise missing access, unsupported host, insufficient resources, unavailable downloads, and failed readiness checks separately; inspect the result and host state.

**Acceptance Scenarios**:

1. **Given** missing required input or failed prerequisite validation, **When** bootstrap runs, **Then** it stops before installation changes and names the failed condition and the operator action needed.
2. **Given** an installation or readiness failure after validation, **When** bootstrap ends, **Then** it reports failure, identifies the failed stage and any completed stages, and does not claim that prior changes were rolled back.
3. **Given** an unreachable dependency or a readiness condition that never succeeds, **When** the documented bounded wait expires, **Then** bootstrap terminates with failure instead of waiting indefinitely or reporting success.

---

### User Story 3 - Rerun Without Losing State (Priority: P1)

As a platform operator, I want to rerun bootstrap safely so that verification or a retry does not replace my cluster or damage existing data.

**Why this priority**: A repeatable bootstrap is useful only if repeating it preserves the host it has already prepared.

**Independent Test**: Run bootstrap twice on a matching installation containing disposable verification data; compare cluster identity and data before and after. Separately exercise conflicting and partially installed states.

**Acceptance Scenarios**:

1. **Given** a healthy matching installation, **When** bootstrap is rerun with the same inputs, **Then** it verifies readiness without reinstalling, unnecessarily restarting, or changing cluster identity, configuration, or existing data.
2. **Given** a different installed version or conflicting managed configuration, **When** bootstrap runs, **Then** it stops and reports the conflict without upgrading, downgrading, resetting, or overwriting the installation.
3. **Given** a previous incomplete installation, **When** bootstrap is rerun, **Then** it either resumes steps whose existing state is verified compatible or stops with an explicit recovery instruction; it never formats disks or silently creates a replacement cluster.

### Edge Cases

- An invalid version, unsupported OS/architecture, missing privilege, insufficient capacity, network-range overlap, or missing prerequisite must be reported before installation changes where detectable.
- Connectivity can fail after validation; a partial run must remain distinguishable from success and from an unchanged host.
- An existing installation cannot be assumed compatible solely because its version matches.
- A running service without node and name-resolution readiness is not a successful bootstrap.
- Authentication failure or an unexpected host identity must not trigger relaxed connection verification.
- Reboot readiness requires previously configured networking and storage to return; bootstrap must not erase storage to repair a missing mount.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: Bootstrap MUST target one existing VM per invocation and require only its address, SSH user, and explicit cluster version as feature-specific settings. Existing operator authentication MUST be reused without a new secret-distribution mechanism. Acceptance: Story 1.1 and Story 2.1.
- **FR-002**: Bootstrap MUST document and validate one initial supported host profile, including OS release, architecture, administrative access, required host facilities, sufficient cluster capacity, network compatibility, and installation-artifact access. Numerical minima and validation methods belong to planning. Detectable unmet prerequisites MUST stop installation changes. Acceptance: Story 2.1.
- **FR-003**: Bootstrap MUST install the explicitly selected version, enable its service, and use the supported cluster defaults. It MUST NOT silently select a newer version or require optional tuning inputs for the supported profile. Acceptance: Story 1.1.
- **FR-004**: Bootstrap MUST verify the running service, authorized management access, node readiness, and built-in name-resolution readiness before reporting success. Each remote operation and readiness wait MUST have a documented finite limit. Acceptance: Story 1.2 and Story 2.3.
- **FR-005**: Bootstrap MUST distinguish prerequisite, installation, and readiness failures and identify completed stages without exposing credentials or falsely reporting rollback or success. Acceptance: Story 2.1–2.3.
- **FR-006**: A matching healthy rerun MUST preserve cluster identity, existing configuration, and data and MUST NOT reinstall or unnecessarily restart the service. Conflicting or unverified existing state MUST stop automatic mutation rather than trigger replacement. Acceptance: Story 3.1–3.3.
- **FR-007**: Bootstrap MUST NOT retrieve, provision, or log application/backend credentials, weaken connection verification, or place operator private authentication material in source-controlled configuration. Base installation MUST work without the application identity service, vault, or their private-image access path. Acceptance: Story 1.3 and inspection of failure output in Story 2.
- **FR-008**: Bootstrap MUST NOT create cloud infrastructure, change cloud or host firewall policy, provision or format disks, deploy application/shared platform/vault workloads, schedule backups, configure monitoring, or perform upgrades or data restoration. These exclusions do not remove default bundled cluster components. Acceptance: Story 1.4 and Story 3.2–3.3.
- **FR-009**: The result MUST identify the target, selected/observed version, and readiness or failure outcome, and state that application deployment remains a separate step. It MUST NOT claim application readiness, vault compliance, or restored data. Acceptance: Story 1.2–1.4 and Story 2.2.

### Key Entities

- **Bootstrap Target**: The existing VM, its operator access, and observed compatibility with the supported host profile.
- **Bootstrap Request**: Target address, SSH user, and the explicitly selected cluster version; authentication remains in the operator's existing access mechanism.
- **Bootstrap Result**: Target and version identification, completed stages, readiness outcome, and actionable failure information without secret values.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: An operator prepares each of two fresh supported test hosts with one invocation per host, three feature-specific settings, and zero manual installation steps after prerequisites are satisfied.
- **SC-002**: Every successful acceptance run passes all four readiness checks; every deliberately failed readiness check prevents a success result.
- **SC-003**: Two consecutive reruns on a matching healthy test host cause zero reinstalls, unnecessary service restarts, cluster-identity changes, or changes to existing verification data.
- **SC-004**: Every enumerated invalid-input, prerequisite, conflict, and interrupted-run acceptance case produces an actionable failure or verified safe continuation; none silently upgrades, resets, or erases data.
- **SC-005**: Inspection of successful and failed acceptance runs finds zero backend-credential retrieval/distribution operations and zero application deployments; the operator can identify whether separate deployment may proceed from the final result alone.

## Assumptions

- Ansible and K3s are user-selected scope constraints, not new tool choices introduced by this specification. Their exact commands, packages, file layout, and version selection belong to planning.
- The initial target is an existing Oracle Cloud Linux VM. Cloud creation, network rules, SSH access, required host facilities, and usable storage are operator-provided prerequisites; no cloud account credentials are required by this bootstrap.
- Planning selects one initial OS/architecture profile and documents it. Bootstrap support for that profile does not remove the broader platform's portability obligations or claim managed-cloud compatibility has been tested.
- Default network ranges and default node-backed storage are acceptable only where their prerequisites hold. Custom disks, networking, private-image routing, and other profiles require separately scoped follow-up work rather than additional mandatory configuration here.
- Ordinary cluster-generated infrastructure credentials remain part of the existing K3s baseline. This feature does not resolve or approve caller-authentication choices in the separate vault feature and does not claim a node has no cryptographic secret material.
- The vault feature's placement, operation integrations, and governance reconciliation remain independent. No backend secret delivery is introduced or migrated by this feature, and existing application manifests are unchanged.
- Bootstrap completion is host readiness only. Workload budgets, application isolation, backups, upgrades, and recovery retain their existing owners and requirements.
- [Minimal bootstrap scope](../../docs/bootstrap-scope.md), [platform constitution](../../.specify/memory/constitution.md), and the separate [vault specification](../002-internal-cluster-vault/spec.md) provide context; this feature does not amend them.
