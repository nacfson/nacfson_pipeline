# Spec Delta

## Purpose

Defines the trusted path from a registered repository's protected default-branch commit to an immutable production image, GitOps reconciliation, and a bounded automatic rollback without giving CI direct cluster or deployment-repository ownership.

## ADDED Requirements

### Requirement: Trusted build admission and queueing

A commit to a registered protected default branch SHALL enqueue one trusted build under the platform concurrency limit. Duplicate delivery events for the same repository and commit SHALL be idempotent. Builds for unregistered repositories or ineligible refs MUST NOT enter the production delivery queue.

#### Scenario: Default-branch commit is queued

- **WHEN** the approved Forgejo workflow reports a new commit on a registered protected default branch
- **THEN** one build is queued with the project identifier, canonical repository identity, commit SHA, contract version, and target platform

#### Scenario: Duplicate event is delivered

- **WHEN** the same repository and commit event is delivered more than once
- **THEN** the system reuses the existing build result or queue entry and does not publish or promote twice

#### Scenario: Concurrency limit is reached

- **WHEN** all trusted runner capacity is occupied
- **THEN** additional eligible builds remain queued and the platform does not evict workloads or exceed the configured concurrency limit

### Requirement: Isolated verification and image build

The trusted pipeline SHALL complete repository verification and declared tests before publishing an image. It SHALL build with rootless BuildKit and MUST NOT receive a Kubernetes API token, host container-runtime socket, platform-config Git credential, or credential for another project's registry namespace. Pull-request and fork verification SHALL run without production publication or promotion credentials.

#### Scenario: Tests fail

- **WHEN** repository verification or declared tests fail
- **THEN** the build becomes `build-failed`, no production artifact is published, and no deployment commit is created

#### Scenario: Trusted build succeeds

- **WHEN** tests pass and rootless BuildKit produces an image for every registered target platform
- **THEN** only the project-scoped publication step receives authority to push that image to the assigned registry namespace

#### Scenario: Build requests prohibited access

- **WHEN** build code attempts to use a host runtime socket, Kubernetes credential, platform-config credential, or another project's registry credential
- **THEN** the capability is unavailable and the build cannot mutate the corresponding system

### Requirement: Immutable artifact identity

A successful production candidate SHALL be identified by the registry-confirmed digest, never by a mutable tag. The build result SHALL bind the canonical repository identity, source commit SHA, build-run identifier, build-contract version, target platform, registry namespace, and digest. ProcessManager SHALL verify the digest and platform metadata through the registry before promotion.

#### Scenario: Artifact is published successfully

- **WHEN** the registry accepts the built image
- **THEN** the build result records the registry-confirmed immutable digest and all required source and build metadata

#### Scenario: Mutable image reference is reported

- **WHEN** a build result reports `latest`, another mutable tag, or a digest that the registry cannot confirm in the assigned namespace
- **THEN** promotion is rejected and production desired state is unchanged

#### Scenario: Required platform image is absent

- **WHEN** the manifest does not contain an image for the project's registered production architecture
- **THEN** promotion is rejected with an architecture mismatch

### Requirement: Delivery consumes the shared ProcessManager GitOps contracts

Project delivery SHALL use the platform's version-compatible `ProjectDesiredStateStore` for every registration, promotion, and rollback mutation, and SHALL use its `ProjectReconciliationObserver` for every Flux/Kubernetes deployment observation. Delivery MAY add release orchestration and state correlation above those contracts, but MUST NOT instantiate another deployment-repository writer, mutable project catalog, Kubernetes writer, or reconciliation observer. Delivery MUST remain disabled when either required contract is missing or incompatible.

#### Scenario: Compatible shared contracts are available

- **WHEN** ProcessManager starts with the required store and observer contract versions
- **THEN** delivery orchestration uses those instances for all desired-state mutations and deployment observations

#### Scenario: Shared contract is missing or incompatible

- **WHEN** either required GitOps contract version is unavailable
- **THEN** production delivery remains disabled, the last committed desired state is preserved, and no fallback writer or observer is created

#### Scenario: Delivery observes a deployment

- **WHEN** delivery needs the Flux or workload state for a deployment revision
- **THEN** it queries `ProjectReconciliationObserver` and does not create a feature-specific Kubernetes or Flux client

### Requirement: Current-head automatic promotion

ProcessManager SHALL automatically promote a successful artifact only after verifying that the repository is registered, the build used the approved workflow and contract, the source commit is still the protected default branch head, the artifact belongs to the assigned namespace, and the registry confirms its digest and target platform. It SHALL submit the resulting project-scoped mutation through `ProjectDesiredStateStore`. A successful build whose commit is no longer the default branch head SHALL become `superseded` and MUST NOT change production.

#### Scenario: Current default-branch build is promoted

- **WHEN** every promotion check succeeds and the built commit remains the protected default branch head
- **THEN** ProcessManager submits the immutable digest and source metadata with the expected project revision to `ProjectDesiredStateStore`, which validates, signs, and pushes the non-force deployment-repository commit

#### Scenario: Older queued build finishes after main advances

- **WHEN** a successful build's commit is not the current protected default branch head
- **THEN** the build becomes `superseded`, its image remains available for audit, and no deployment commit is created

#### Scenario: Promotion result is replayed

- **WHEN** ProcessManager receives the same authenticated build result again
- **THEN** it returns the existing promotion state without creating a duplicate Git commit

### Requirement: Promotion changes only release identity

Automatic promotion SHALL modify only the project's image digest and auditable source metadata within the project's allowed deployment path. It MUST NOT change runtime policy, another project, shared infrastructure, chart definitions, bootstrap configuration, or encrypted Secrets.

#### Scenario: Promotion produces a minimal commit

- **WHEN** ProcessManager promotes an eligible artifact
- **THEN** the resulting diff changes only the registered project's digest and source/build metadata

#### Scenario: Build result attempts an unrelated mutation

- **WHEN** a promotion request includes a runtime-policy, shared-infrastructure, Secret, chart, bootstrap, or cross-project change
- **THEN** ProcessManager rejects the entire request and creates no commit

### Requirement: Release status is correlated end to end

The system SHALL preserve distinct build, artifact, promotion, Git, reconciliation, workload, and rollback states correlated by project, source commit, build run, image digest, and deployment commit. Build or Git success MUST NOT be reported as deployment success; `ready` requires Flux to observe the deployment commit and the required workload health checks to pass.

#### Scenario: Deployment progresses successfully

- **WHEN** a promoted commit is pushed and Flux observes and reconciles that exact revision to healthy workloads
- **THEN** the release progresses through `committed`, `reconciling`, and `ready` with the correlated identifiers retained

#### Scenario: Build succeeds but Flux is pending

- **WHEN** an artifact is promoted but Flux has not reported the deployment revision ready
- **THEN** the release remains `committed` or `reconciling` and is not shown as `ready`

#### Scenario: Observation systems are unavailable

- **WHEN** Flux or the Kubernetes API cannot be observed
- **THEN** the release reports observation unavailable without treating the outage alone as a release failure or initiating rollback

### Requirement: Automatic rollback compatibility

A release eligible for automatic promotion SHALL keep persistent schemas and externally visible durable state backward-compatible with the previous `ready` image. A build that declares a destructive, irreversible, or compatibility-unknown migration MUST be blocked from automatic promotion and require a separately authorized migration plan.

#### Scenario: Backward-compatible release is eligible

- **WHEN** a candidate declares only backward-compatible state changes and passes the delivery checks
- **THEN** it remains eligible for automatic promotion and bounded automatic rollback

#### Scenario: Incompatible migration is declared

- **WHEN** a candidate declares a destructive, irreversible, or compatibility-unknown migration
- **THEN** automatic promotion is blocked before the deployment commit and the current release remains unchanged

### Requirement: Bounded automatic rollback

A confirmed reconciliation failure or bounded readiness timeout for the promoted revision SHALL trigger automatic rollback only when a last `ready` release exists and the project's current desired release identity still equals the failed release. ProcessManager SHALL submit one project-scoped mutation through `ProjectDesiredStateStore` that restores the last `ready` digest and source metadata while preserving unrelated commits, then wait for Flux and workload readiness through `ProjectReconciliationObserver`. A release MUST NOT attempt more than one automatic rollback.

#### Scenario: Failed current release is rolled back

- **WHEN** the current deployment revision fails reconciliation or exceeds its readiness timeout while observation remains available, the project's desired release still identifies the failed candidate, and a last `ready` release exists
- **THEN** ProcessManager commits one revert to that `ready` digest and marks the release `rolling-back`

#### Scenario: Newer release supersedes a failure

- **WHEN** the same project's desired state no longer references the failed release
- **THEN** ProcessManager records the failure but does not revert or modify the newer release

#### Scenario: No known-good release exists

- **WHEN** the first release fails and no prior `ready` digest exists
- **THEN** the release remains `failed`, an administrator is alerted, and no synthetic rollback target is created

#### Scenario: Rollback succeeds

- **WHEN** Flux reconciles the rollback commit and the restored workload becomes healthy
- **THEN** the release becomes `rollback-ready` and the failed digest remains recorded for audit

#### Scenario: Rollback fails

- **WHEN** the rollback commit fails reconciliation or readiness
- **THEN** the release becomes `rollback-failed`, an administrator is alerted, and no further automatic rollback or promotion is initiated for that failure

### Requirement: Concurrent Git updates preserve newer state

ProcessManager delivery SHALL pass the expected project release identity with every promotion or rollback to `ProjectDesiredStateStore`. The store SHALL compare that identity and the remote deployment branch head, use non-force commits, and, after a non-fast-forward result, fetch current state and permit regeneration only when the target project's revision is unchanged. Delivery SHALL revalidate repository head, artifact eligibility, project ownership, and rollback preconditions before a regenerated mutation; otherwise it SHALL mark the operation superseded.

#### Scenario: Unrelated project advances the deployment branch

- **WHEN** another project's commit advances the deployment branch during promotion or rollback
- **THEN** ProcessManager fetches, regenerates the same project-scoped change against the new head, reruns validation, and pushes without force

#### Scenario: Same project advances before rollback

- **WHEN** the target project's newer release advances the branch before an older failure can roll back
- **THEN** the older rollback becomes superseded and does not overwrite the newer project state

### Requirement: Delivery failures remain retry-safe

Temporary Forgejo, registry, ProcessManager, or Git unavailability SHALL preserve the last committed desired state. Retried authenticated events SHALL be idempotent, and no component other than the shared `ProjectDesiredStateStore` may compensate by mutating the deployment repository; no delivery component may mutate Kubernetes or force-push deployment history.

#### Scenario: Registry is unavailable

- **WHEN** the image or digest cannot be confirmed in the registry
- **THEN** promotion remains blocked and the current deployment is unchanged

#### Scenario: ProcessManager is unavailable after publication

- **WHEN** CI publishes an artifact but cannot submit or confirm the build result
- **THEN** it may retry the same authenticated result, and ProcessManager handles the replay without duplicate promotion

#### Scenario: Deployment repository is unavailable

- **WHEN** ProcessManager cannot read or push the deployment repository
- **THEN** the release remains pending, no direct cluster mutation occurs, and retry resumes from the last committed state
