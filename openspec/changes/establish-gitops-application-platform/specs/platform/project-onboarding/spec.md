# Spec Delta

## Purpose

Defines how an existing trusted source repository becomes a managed project without allowing source-controlled build settings to take ownership of production policy, credentials, or another project's artifact namespace.

## ADDED Requirements

### Requirement: Existing repository registration

The system SHALL allow an authorized administrator to register an existing repository from the approved Forgejo instance. Registration SHALL include a unique DNS-safe project identifier, canonical repository identity, protected default branch, build-contract path and version, assigned registry namespace, deployment path, and runtime configuration. The initial release MUST NOT create, template, fork, or delete source repositories.

#### Scenario: Trusted repository is registered

- **WHEN** an administrator submits an accessible approved repository with a protected default branch and valid build contract
- **THEN** the system records one registration with the platform-assigned project identifier, registry namespace, and deployment path

#### Scenario: Untrusted or inaccessible repository is rejected

- **WHEN** a repository is outside the approved Forgejo instance, cannot be read with the registration credential, or lacks the required protected default branch
- **THEN** registration fails before any build, registry, or deployment credential is issued

#### Scenario: Duplicate identity is rejected

- **WHEN** a new registration reuses an existing project identifier, canonical repository identity, deployment path, or registry namespace for a different project
- **THEN** registration fails and neither registration is modified

### Requirement: Declarative registration authority

The deployment repository SHALL be the authoritative registration store. ProcessManager SHALL read and mutate registrations only through the platform's version-compatible `ProjectDesiredStateStore`; it MUST NOT add a registration-specific Git writer or separate mutable catalog. ProcessManager SHALL reconstruct registrations after restart from the committed project registration and release files rather than relying on process memory. Repeating an identical registration request SHALL be idempotent; a request that conflicts with committed state SHALL fail without overwriting it.

#### Scenario: ProcessManager reconstructs registrations

- **WHEN** ProcessManager starts with no in-memory project state
- **THEN** it reconstructs every registration, registry assignment, build-contract version, and deployment path from the deployment repository

#### Scenario: Identical registration is replayed

- **WHEN** the same authorized registration request is submitted more than once
- **THEN** the existing registration is returned without producing a second commit or a second project identity

#### Scenario: Conflicting replay is rejected

- **WHEN** a repeated request changes an immutable repository identity, project identifier, or assigned registry namespace
- **THEN** the system rejects it as a conflict and preserves the committed registration

#### Scenario: Shared desired-state contract is unavailable

- **WHEN** the required `ProjectDesiredStateStore` version is absent or incompatible
- **THEN** registration and deregistration remain disabled and no deployment-repository mutation is attempted

### Requirement: Versioned source build contract

A registered repository SHALL contain a versioned build contract at the configured repository-relative path on its default branch. The initial contract SHALL support a repository-local Dockerfile builder, repository-relative build context and Dockerfile paths, and target platforms allowed by the registration. Contract paths MUST NOT escape the checkout. The contract MUST NOT declare a registry namespace, production image digest, production access policy, production Secrets, or other platform-owned runtime values.

#### Scenario: Supported build contract is accepted

- **WHEN** the default branch contains a supported contract version with a Dockerfile and build context inside the repository and allowed target platforms
- **THEN** registration records the validated contract version and permits trusted builds

#### Scenario: Unsupported or unsafe build contract is rejected

- **WHEN** the contract version or builder is unsupported, a path escapes the checkout, or a target platform is outside the registration
- **THEN** registration and builds are blocked with the specific contract violation

#### Scenario: Source attempts to claim platform-owned fields

- **WHEN** the source build contract declares a registry namespace, production digest, runtime access policy, production Secret, or other platform-owned value
- **THEN** validation rejects the contract and does not apply the attempted value

### Requirement: Protected branch and approved workflow

Automatic production delivery SHALL be enabled only for a registered protected default branch and an approved version of the trusted build workflow. Unregistered repositories, unprotected branches, tags, pull requests, forks, and other branches MUST NOT receive production registry push or promotion credentials.

#### Scenario: Protected default branch qualifies for delivery

- **WHEN** registration verifies the protected default branch and approved workflow version
- **THEN** commits to that branch may enter the trusted delivery queue

#### Scenario: Untrusted ref requests production credentials

- **WHEN** a build originates from a pull request, fork, tag, non-default branch, or unregistered repository
- **THEN** it receives no production registry push or promotion credential

### Requirement: Platform-assigned artifact namespace

The platform SHALL assign exactly one OCI registry namespace to each registered project. Build and promotion credentials SHALL be limited to that namespace, and the source repository MUST NOT override it or publish a production candidate for another project.

#### Scenario: Project publishes inside its namespace

- **WHEN** a trusted build for a registered project publishes an artifact
- **THEN** the registry accepts the artifact only under the namespace assigned by the registration

#### Scenario: Cross-project publication is attempted

- **WHEN** a project attempts to push or report an artifact under another project's namespace
- **THEN** the registry or promotion validation rejects it without modifying either project's release

### Requirement: Runtime policy remains platform-owned

Source repository changes SHALL NOT directly change the production hostname, access policy, resources, replicas, health check, Secret references, persistence, or other runtime configuration. Those fields SHALL change only through an authorized ProcessManager operation committed to the project's allowed deployment path.

#### Scenario: Build contract changes runtime policy

- **WHEN** a source commit attempts to modify a platform-owned runtime field through its build contract or build result
- **THEN** validation ignores no value silently; it rejects the request and preserves the committed runtime policy

#### Scenario: Authorized runtime update succeeds independently

- **WHEN** an authorized administrator changes runtime policy through ProcessManager
- **THEN** ProcessManager validates and commits that policy separately from any image promotion

### Requirement: Safe deregistration

Deregistering a project SHALL stop future trusted builds and automatic promotions before removing its managed release. Deregistration MUST NOT delete the source repository, registry history, Secrets, persistent data, or backups unless each destructive action is separately authorized under its retention policy.

#### Scenario: Registered project is deregistered

- **WHEN** an administrator confirms deregistration
- **THEN** new builds and promotions are rejected and the managed release is removed through a validated Git commit

#### Scenario: Durable assets remain without explicit deletion

- **WHEN** deregistration completes without separate retention actions
- **THEN** the source repository, immutable image history, Secrets, persistent data, and backups remain intact
