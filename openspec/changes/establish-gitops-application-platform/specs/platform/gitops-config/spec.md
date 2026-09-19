# Spec Delta

## Purpose

Defines the authoritative deployment configuration that carries production desired state: its reconciliation root, layout and ownership boundaries, project configuration contract, encrypted secret representation, and the validation gate that keeps invalid configuration out of the production branch.

## ADDED Requirements

### Requirement: Production reconciliation root

The repository SHALL provide exactly one production cluster root that the GitOps controller reconciles, and every Git-managed infrastructure and application resource SHALL be reachable from that root through declared references. Reconciliation units SHALL declare their dependencies so shared infrastructure converges before the identity services and applications that require it.

#### Scenario: Root renders from a clean checkout

- **WHEN** the production root and each of its reconciliation units are rendered offline from a clean checkout
- **THEN** rendering succeeds and reports no unresolved resource, chart, value, or secret references

#### Scenario: Declared order gates dependent units

- **WHEN** the reconciliation units are inspected without a live cluster
- **THEN** each unit declares the units it depends on, and infrastructure units precede the identity and application units that consume them

### Requirement: Layout and ownership boundaries

The repository SHALL separate the production cluster root, shared infrastructure, per-project applications, and the reusable application chart into distinct areas. Each project SHALL have its own directory with its own reconciliation entry point, and every rendered resource SHALL target the owning project's namespace only. Automated project management SHALL modify only the per-project application paths it owns; bootstrap files, shared infrastructure, the shared chart, and other projects MUST remain outside its write boundary.

#### Scenario: Layout separates platform and project areas

- **WHEN** the deployment tree is inspected
- **THEN** the cluster root, infrastructure, applications, and chart areas exist as distinct areas, and each existing project directory contains its own reconciliation entry point

#### Scenario: Project resources stay in the owning namespace

- **WHEN** a project's configuration renders
- **THEN** every namespaced resource targets that project's own namespace and no resource targets another project's or a platform namespace

#### Scenario: Out-of-boundary modification is rejected

- **WHEN** a proposed change modifies a path outside the requester's ownership boundary, such as bootstrap files, shared infrastructure, the shared chart, or another project's directory
- **THEN** validation rejects the change before it can reach the production branch

### Requirement: Project configuration contract

Every application configuration SHALL satisfy one versioned contract before it reaches the production branch. The contract SHALL include a DNS-safe project identifier, an immutable image digest instead of a mutable tag, container port, replica count, CPU and memory requests and limits, health-check path, supported architecture, exactly one ingress access policy, the policy-specific identity fields, references to pre-existing Secrets, and persistence classification where required.

#### Scenario: Complete configuration is accepted

- **WHEN** a project configuration supplies every required contract field with an immutable image digest
- **THEN** validation accepts it and the rendered resources carry labels identifying the project, the owning release, the component, and the Git revision when available

#### Scenario: Mutable or incomplete configuration is rejected

- **WHEN** a project configuration omits a required field, selects an unsupported access policy, or references an image by mutable tag
- **THEN** validation rejects it with the failing field before the change reaches the production branch

### Requirement: Deterministic rendering and revert safety

Rendering SHALL depend only on committed repository content. Identical revisions SHALL produce identical output, and no rendered value may depend on build time, ambient environment, or mutable external references. Reverting a commit SHALL restore the previously rendered desired state.

#### Scenario: Repeated renders are identical

- **WHEN** the same revision is rendered twice from clean checkouts
- **THEN** the two outputs are byte-identical

#### Scenario: Revert restores the prior state

- **WHEN** the commit that changed a project's configuration is reverted on the production branch
- **THEN** the rendered desired state matches the state before that commit

### Requirement: Pre-merge validation gate

Every change that reaches the production branch SHALL pass offline validation of the production root and every affected reconciliation unit: rendering succeeds, values validate against the published schema, cross-references resolve, project namespace containment holds, and no plaintext secret material is present. A change that fails any check MUST NOT reach the production branch, and the failure SHALL identify the specific violation.

#### Scenario: Valid change passes the gate

- **WHEN** a change renders successfully, satisfies the configuration contract, and passes every check
- **THEN** validation exits successfully and the change may be merged

#### Scenario: Invalid change is blocked

- **WHEN** a change fails any validation check
- **THEN** the gate reports the specific violation and the change does not reach the production branch

### Requirement: Encrypted secret representation

Secret-bearing Kubernetes manifests SHALL exist in the repository only in encrypted form, decryptable by the platform's decryption key, and other configurations SHALL reference them by namespace and name only. Plaintext secret values MUST NOT appear in repository files, commit messages, or validation output, and decryption key material MUST NOT be stored in the repository.

#### Scenario: Plaintext secret material is rejected

- **WHEN** a change introduces an unencrypted secret value or embeds one in a non-secret manifest
- **THEN** validation fails and reports the location as plaintext secret material without echoing the value

#### Scenario: Secrets are referenced, not duplicated

- **WHEN** a project configuration requires a credential
- **THEN** it references an existing encrypted manifest by namespace and name, and decrypted values exist only inside the cluster

### Requirement: Single-writer reconciliation

External provider resources SHALL be owned by OpenTofu, host configuration and k3s/Flux bootstrap SHALL be owned by Ansible, and Git-managed Kubernetes resources SHALL be owned by Flux. No resource may have more than one active writer. Workloads SHALL express placement through stable capabilities such as CPU architecture, resource capacity, labels, taints, and storage class rather than provider resource identifiers or Kubernetes node names. After Flux takes ownership, bootstrap automation MUST NOT apply or mutate platform or application manifests.

#### Scenario: Control-plane ownership remains disjoint

- **WHEN** the OpenTofu configuration, Ansible configuration, and deployment repository are inspected together
- **THEN** provider resources appear only under OpenTofu ownership, host and bootstrap configuration appears only under Ansible ownership, and Git-managed Kubernetes resources appear only under Flux ownership

#### Scenario: Replacement node preserves the workload contract

- **WHEN** a failed node is replaced by one that provides the required architecture, capacity, labels, taints, and storage classes
- **THEN** Flux and Kubernetes restore the workloads without changing project configuration to reference the replacement node's provider identifier or node name

#### Scenario: Bootstrap stops writing after handover

- **WHEN** machine bootstrap runs after Flux has taken ownership
- **THEN** it applies no application or infrastructure manifests

#### Scenario: Removal from Git is reconciled

- **WHEN** a resource is removed from the production root and the revision is reconciled
- **THEN** Flux removes the corresponding cluster resource and reports the reconciliation result
