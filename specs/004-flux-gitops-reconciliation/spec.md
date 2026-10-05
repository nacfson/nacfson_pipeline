# Feature Specification: Flux GitOps Reconciliation

**Feature Branch**: short-lived branches from `main`, each merged through a pull request (see Assumption "Delivery during implementation"); drafted on `feature/002-internal-cluster-vault`

**Created**: 2026-10-03

**Status**: Draft

**Input**: Agreed conversation scope: activate the GitOps operating model that [SPEC.md](../../SPEC.md) and the [constitution](../../.specify/memory/constitution.md) defer to a "future phase". A Flux reconciler runs as ordinary workloads inside the existing single K3s cluster (not as a host process, separate node, or CI push). It pulls the Git repository and applies the existing platform and project declarations in dependency order. It replaces the operator-run ordered apply, baseline, and rollback scripts. The existing architecture (Ansible-bootstrapped K3s, OpenBao vault, namespaces, and workload declarations) is otherwise unchanged. Secrets stay out of the reconciler.

## Clarifications

### Session 2026-10-03

- Q: How does the reconciler get read access to the repository? → A: Option A. The repository is public, so no repository credential is stored in the cluster. The operator chose this because the repository doubles as a public career portfolio.
- Q: Is the reconciler installed manually or automatically? → A: Automatically. The existing VM bootstrap (spec 003) installs it as a final stage after cluster readiness, and skips that stage when a reconciler is already present. There is no separate manual installation action. Vault initialization, seeding, and unseal stay manual.
- Q: Are manual cluster changes only reverted, or also blocked? → A: Also blocked. The cluster rejects manual changes to reconciler-managed objects and namespaces unless the operator deliberately uses a separate break-glass identity. Workstation credentials are read-only.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Deliver Changes Through Git Only (Priority: P1)

As a platform operator, I want a reviewed change merged into the environment's deployment source to reach the cluster automatically, so that I no longer need cluster credentials, a specific workstation, or a manual script to deploy.

**Why this priority**: This is the core value of the feature. Without it, every other story still depends on the manual apply path that this feature replaces.

**Independent Test**: On a test cluster with the reconciler installed, merge a harmless declaration change (for example a label) into the tracked deployment source. Confirm that the cluster reflects it without any operator command against the cluster, and that the status shows the new revision.

**Acceptance Scenarios**:

1. **Given** an installed reconciler tracking an environment's deployment source, **When** a reviewed change is merged into that source, **Then** the cluster converges to the new declarations within the documented interval with no operator action against the cluster.
2. **Given** a converged environment, **When** the operator queries reconciliation status, **Then** each layer reports its applied source revision, its health, and the reason for any failure.
3. **Given** a merged change that cannot be applied (invalid declaration or rejected by the cluster), **When** reconciliation runs, **Then** the affected layer reports failure with the cause, the last successfully applied state stays in place, and unrelated healthy layers are not modified.

---

### User Story 2 - Bring Up an Empty Cluster in Dependency Order (Priority: P1)

As a platform operator, I want a clean VM to reach the full declared state by running the existing VM bootstrap, plus the vault's existing manual unseal and seed procedure, so that rebuilding an environment is repeatable and ordered.

**Why this priority**: The current ordered apply script skips declarations (vault, persistent volume claim, initialization job). Replacing that ordering with declared dependencies is required before production use.

**Independent Test**: Start from a clean VM. Run the existing VM bootstrap, then the vault unseal/seed procedure, with no other manual action. Observe that the reconciler is installed and that each layer is applied only after the layers it depends on report healthy.

**Acceptance Scenarios**:

1. **Given** a clean VM, **When** the operator runs the existing VM bootstrap, **Then** the cluster becomes ready, the reconciler is installed and starts, it manages its own declarations from Git, and it applies layers in the declared order (governance → vault → database → identity → ingress; vault → vault proxies; governance → project boundary; project workloads after project boundary, ingress, and vault proxies).
2. **Given** a layer whose dependency is not yet healthy, **When** reconciliation runs, **Then** that layer waits and reports which dependency it is waiting for, and is not applied out of order.
3. **Given** a sealed vault (first start or after a restart), **When** dependent layers reconcile, **Then** they report not-ready with a cause that points to the vault. Once the operator completes the existing unseal procedure, they converge without any further manual apply.
4. **Given** the reconciler is already installed, **When** a newer reconciler version is merged into Git, **Then** the reconciler upgrades itself through the same Git-driven path.
5. **Given** a reconciler that is already installed (including one upgraded from Git), **When** the VM bootstrap is run again, **Then** the reconciler installation and version are left unchanged and the bootstrap still reports success.

---

### User Story 3 - Correct Drift, Prune Safely, and Roll Back by Revert (Priority: P1)

As a platform operator, I want manual cluster changes undone, declarations removed from Git deleted from the cluster, and rollback done by reverting Git, while persistent data is never deleted by automation.

**Why this priority**: Drift and orphaned objects are the main risks of the manual path. Data loss through automated deletion would be worse than either.

**Independent Test**: On a test cluster with disposable data: modify managed objects manually, remove a declaration from Git, revert a merged change, and remove the declaration of a layer that owns persistent volumes. Observe convergence and confirm the data survives.

**Acceptance Scenarios**:

1. **Given** a managed object changed manually in the cluster, **When** the next reconciliation runs, **Then** the object returns to its declared state.
2. **Given** a declaration removed from the deployment source, **When** reconciliation runs, **Then** the corresponding object is removed from the cluster.
3. **Given** a declaration that owns persistent data (volume claims, vault storage, database storage), **When** that declaration or its whole layer is removed from Git, **Then** the persistent data and its volumes remain in the cluster.
4. **Given** a merged change that caused a regression, **When** the operator reverts that change in Git, **Then** the cluster converges back to the previous declared state without any script.
5. **Given** an emergency that requires a temporary manual change, **When** the operator suspends reconciliation for one layer, **Then** that layer is not reverted until reconciliation is resumed, and the other layers continue reconciling.

---

### User Story 4 - Gate Revisions Before They Reach the Cluster (Priority: P2)

As a platform operator, I want each candidate revision checked for rendering, schema validity, resource budget, and secret leakage before it can reach an environment's deployment source, so that automation never applies a revision the manual process would have rejected.

**Why this priority**: The constitution's capacity and isolation gates are manual today. Automatic reconciliation would otherwise apply over-budget or non-compliant revisions without anyone checking.

**Independent Test**: Open candidate revisions that individually fail rendering, schema validation, the per-environment resource budget, and the secret scan. Confirm that none can reach the tracked deployment source and that a compliant revision can.

**Acceptance Scenarios**:

1. **Given** a candidate revision that fails any required check for an environment, **When** promotion is attempted, **Then** the environment's deployment source is unchanged.
2. **Given** a candidate revision that passes all required checks, **When** it is promoted, **Then** it becomes eligible for reconciliation in that environment.
3. **Given** a zero-project revision, **When** checks run, **Then** they pass without a division-by-zero or empty-input failure.

---

### User Story 5 - Reconcile Project Workloads With Project-Scoped Authority (Priority: P2)

As a platform operator, I want each project's workload declarations reconciled under an identity limited to that project's workload objects, so that a project change cannot weaken the platform-owned boundary around it.

**Why this priority**: [SPEC.md](../../SPEC.md) ISOLATE requirements say that only the platform identity reconciles namespaces, quotas, network policy, service accounts, and RBAC. Automated reconciliation must keep that separation.

**Independent Test**: Put project workload declarations that attempt to modify the project namespace, quota, network policy, service account, or role bindings into the project's declaration path. Confirm that the reconciler rejects each one and reports the failure for that project's layer only.

**Acceptance Scenarios**:

1. **Given** a project workload layer, **When** it contains only permitted workload objects for its own namespace, **Then** it reconciles successfully.
2. **Given** a project workload layer containing a platform-owned object type, or an object in another namespace, **When** reconciliation runs, **Then** that object is rejected, the platform-owned objects are unchanged, and other layers are unaffected.
3. **Given** platform-owned project boundary objects (namespace, quota, network policy, service account, reconciler role), **When** they are reconciled, **Then** they are applied by the platform layer before the project's workload layer.

---

### User Story 6 - Retire the Manual Release Path (Priority: P3)

As a platform operator, I want the manual ordered-apply, baseline, and rollback scripts removed and the runbook rewritten around reconciliation, so that there is exactly one documented way to change an environment.

**Why this priority**: Two deployment paths would cause drift and confusion. This story only completes after Stories 1–3 are proven.

**Independent Test**: Review the repository and runbook. No instruction tells the operator to apply workload declarations or the reconciler manually, except documented break-glass steps.

**Acceptance Scenarios**:

1. **Given** the feature is complete, **When** the repository is inspected, **Then** the manual ordered-apply, baseline-recording, and rollback scripts and their baseline file are gone.
2. **Given** the updated runbook, **When** an operator follows it to deploy, roll back, check status, or handle an emergency, **Then** every procedure goes through Git or a documented reconciler status or suspend action.

---

### User Story 7 - Reject Manual Changes at the Cluster (Priority: P2)

As a platform operator, I want the cluster itself to reject manual changes to reconciler-managed objects, so that Git is the only way to change an environment, and an emergency change requires a deliberate break-glass step.

**Why this priority**: Drift correction only reverts objects the reconciler already manages. A manually created object that is not in Git would keep running, and a manual edit would stay live until the next reconciliation. The constitution prohibits manual cluster changes, but today nothing enforces that.

**Independent Test**: On a test cluster with the reconciler running, use ordinary operator credentials to create, edit, and delete objects in managed namespaces, and to create a new undeclared object there. Confirm that each request is rejected. Then confirm that a merged Git change still converges, that the vault unseal procedure still works, and that the same change succeeds only when the break-glass identity is used deliberately.

**Acceptance Scenarios**:

1. **Given** ordinary operator credentials, **When** the operator tries to create, update, or delete an object in a reconciler-managed namespace, or a cluster-scoped object of a kind declared in Git, **Then** the request is rejected with a message that points to Git, and the cluster is unchanged.
2. **Given** an object that is not declared in Git, **When** an operator tries to create it in a reconciler-managed namespace, **Then** the request is rejected.
3. **Given** a merged revision, **When** the reconciler (or the project reconciliation identity, within its scope) applies it, **Then** the request is accepted. The cluster's own system components (for example, controllers that create pods for a workload) are unaffected.
4. **Given** an emergency, **When** the operator deliberately uses the break-glass identity (including to suspend or resume reconciliation), **Then** the manual change is accepted. The reconciler reverts changes to managed objects once that layer reconciles again, and the runbook requires every break-glass change to be reconciled back to Git.
5. **Given** operational actions that do not change declared state (status queries, cluster node inspection, metrics reading, logs, executing into the vault pod for unseal, port-forwarding, and deleting a pod so that its controller recreates it), **When** the operator performs them, **Then** they are allowed.
6. **Given** the credentials kept on the operator workstation, **When** they are used, **Then** they can read status (including cluster nodes and metrics) and open port-forward tunnels, but cannot change any object, read secrets, or execute interactive container shells.

### Edge Cases

- **Repository unreachable or no longer public:** the cluster keeps the last applied state, and status reports the source as failing or stale instead of reporting success.
- **Invalid revision bypasses the gates:** the failing layer keeps its last good state, and later layers that depend on it do not proceed on a broken dependency.
- **Placeholder clash:** declarations that contain literal vault reference placeholders (for example `${VAULT:...}`) must reach the cluster unchanged, including when environment-specific values are applied.
- **Restarts:** after a VM reboot or reconciler restart, reconciliation resumes without operator action except the existing vault unseal.
- **Immutable fields:** a declaration that cannot be updated in place (for example a completed initialization job) must not leave its layer permanently failed after a legitimate change.
- **Environment divergence:** values that differ by environment (hostnames, cookie domain) must not require copying whole declarations.
- **Local edits on the node:** a manual edit on the node or a forced manual apply during suspension must be visible as drift once reconciliation resumes.
- **Container image references:** the reconciler's own container images must not be routed through the project's private-registry pull path.
- **Reconciler installation fails during bootstrap:** the bootstrap reports failure at the reconciler installation stage with a remediation, keeps the completed cluster stages and existing cluster state, and a re-run retries only the missing installation.
- **Manual-change rule misconfigured:** if the rule would reject the reconciler or a system component, the operator can still recover with the break-glass identity. The rule never applies to the cluster's system namespaces.
- **Manual-change rule availability:** enforcement must not depend on an additional in-cluster service whose outage would either block every change or silently allow manual changes.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The reconciler MUST run as ordinary workloads inside the target cluster, managed by the cluster like any other workload. It MUST NOT require a separate host process, an additional node, or inbound network access to the cluster's management interface. Acceptance: Story 1.1 and Story 2.1.
- **FR-002**: Installing the reconciler MUST need no manual action beyond running the existing VM bootstrap. The bootstrap MUST install the reconciler as its final stage, after cluster readiness is verified, from the same declarations the reconciler later manages from Git. It MUST skip installation when a reconciler is already present, so a re-run never overwrites or downgrades a Git-managed reconciler, and it MUST verify that the reconciler is running before reporting success. After installation, the reconciler's own declarations, including version upgrades, MUST be managed from Git. Acceptance: Story 2.1, Story 2.4, Story 2.5, and Edge Cases.
- **FR-003**: Each environment (initially local K3s and the production VPS K3s) MUST have its own entrypoint in Git, declaring which layers that environment reconciles and with which non-secret environment values. Environment differences MUST NOT require copying shared declarations. Acceptance: Story 1.1 and Edge Cases.
- **FR-004**: Existing platform and project declarations MUST be organized into reconciliation layers with explicit dependencies: governance, vault, vault proxies, database, identity, ingress, project boundary, and project workloads. A layer MUST NOT be applied before its dependencies report healthy. Acceptance: Story 2.1–2.3.
- **FR-005**: Reconciliation MUST correct drift on managed objects and remove objects whose declarations were removed from the deployment source. Both MUST happen within a documented interval. Acceptance: Story 3.1–3.2.
- **FR-006**: Persistent data declarations (volume claims and the storage backing the database and the vault) MUST be protected from deletion by pruning, including when their whole layer is removed. Acceptance: Story 3.3.
- **FR-007**: Rollback MUST be done by reverting the deployment source. Operators MUST be able to suspend and resume reconciliation per layer for emergencies, without stopping other layers. Acceptance: Story 3.4–3.5.
- **FR-008**: Reconciliation status MUST expose, per layer, the applied revision, health, dependency waits, and failure causes, without exposing secret values. Acceptance: Story 1.2–1.3 and Story 2.2–2.3.
- **FR-009**: The reconciler MUST NOT store, decrypt, generate, or deliver secret values. Secret material stays governed by Constitution Principle V and the existing vault feature. Literal vault reference placeholders in declarations MUST be applied unchanged. Acceptance: Edge Cases and SC-009.
- **FR-010**: Each environment MUST track a protected deployment source. Only revisions that passed that environment's required checks (rendering with pinned tool versions, schema validation, the `platform-preflight` capacity and isolation check defined in [SPEC.md](../../SPEC.md) DEPLOY-04, and secret scanning) MAY reach it. Acceptance: Story 4.1–4.3.
- **FR-011**: Project workload layers MUST be reconciled under an identity limited to workload objects in that project's namespace. Platform-owned boundary objects (namespace, quota, network policy, service account, RBAC) MUST be reconciled only by the platform layer. Acceptance: Story 5.1–5.3.
- **FR-012**: The repository MUST be public, and the reconciler MUST read it anonymously, with no repository credential stored in the cluster. The reconciler MUST have no write access to the repository. Before the repository becomes public, its full history MUST pass a secret scan, and any real credential that ever appeared in history, even encrypted, MUST be rotated. Acceptance: Story 1.1, Edge Cases, and SC-009.
- **FR-013**: The reconciler's workloads MUST declare bounded resources consistent with Constitution Principle IV, and their footprint MUST be counted in the platform reservation used by the capacity check. Acceptance: SC-008.
- **FR-014**: The manual ordered-apply, baseline, and rollback scripts and their baseline file MUST be removed. The operations runbook MUST describe installation, status, deployment, rollback, suspend/resume, and vault unseal in the reconciled model. Acceptance: Story 6.1–6.2.
- **FR-015**: The cluster MUST reject create, update, and delete requests for objects in reconciler-managed namespaces and the `default` namespace, and for cluster-scoped objects of kinds declared in Git, unless the requester is the reconciler, the project reconciliation identity within its scope, a cluster system component, or the break-glass identity. The rejection MUST apply to every operator credential, including full cluster administrators. Operational actions that do not change declared state (reads, logs, node status, metrics, port-forward, and deleting a pod so that its controller recreates it) MUST remain allowed. The rule MUST be declared in Git and applied by the reconciler, and MUST NOT depend on an additional in-cluster service. Acceptance: Story 7.1–7.3, Story 7.5, and Edge Cases.
- **FR-016**: Manual changes, including suspending and resuming reconciliation, MUST require the operator to deliberately use a separate break-glass identity, reachable only through the existing SSH path to the node. Credentials kept on the operator workstation MUST be read-only and diagnostic (including node and metrics inspection, but excluding secrets and interactive exec). The runbook MUST describe the break-glass procedure, including reconciling every break-glass change back to Git. Acceptance: Story 7.4 and Story 7.6.

### Key Entities

- **Deployment Source**: The protected Git location an environment tracks. It identifies the repository, the branch or revision, and the environment entrypoint.
- **Environment Entrypoint**: The per-environment declaration of which layers are reconciled, in what order, and with which non-secret environment values.
- **Reconciliation Layer**: A named group of declarations with its dependencies, prune policy, health checks, reconciling identity, and per-layer status.
- **Reconciliation Status**: Per-layer applied revision, readiness, dependency waits, suspension state, and failure cause.
- **Project Reconciliation Identity**: A project-scoped identity, limited to workload objects in one project namespace, under which that project's workload layer is applied.
- **Break-glass Identity**: A separate identity that an operator must deliberately use, through the SSH path to the node, to make any manual change to reconciler-managed objects, including suspend and resume.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: From a clean VM, an operator reaches the declared state of all reconciled layers by running the existing VM bootstrap plus the existing vault unseal/seed procedure, with zero other manual applies. Re-running the bootstrap afterwards changes nothing in the reconciler installation.
- **SC-002**: In 10 consecutive trials, a merged change appears in the cluster within 5 minutes of merge, with zero operator commands against the cluster.
- **SC-003**: Manual modifications to 5 different managed objects are each reverted within 10 minutes.
- **SC-004**: Removing a declaration removes its object within 10 minutes. Across all prune tests, including removing a whole data-owning layer, zero persistent volumes or data records are deleted.
- **SC-005**: Reverting a merged change restores the previous declared state within 10 minutes, with no script.
- **SC-006**: 100% of candidate revisions that fail any required check are kept out of the environment's deployment source. A compliant zero-project revision passes.
- **SC-007**: Attempts by a project workload layer to create or change each platform-owned object type, or objects in another namespace, succeed 0 times.
- **SC-008**: The reconciler adds no more than 256 MiB of memory, counted by its declared memory limits, to the platform reservation, and the capacity check includes it.
- **SC-009**: Secret scanning of the repository and inspection of reconciler-managed declarations find zero secret values. The reconciler delivers zero secrets.
- **SC-010**: An operator determines the applied revision and health of every layer from a single status query in under 1 minute.
- **SC-011**: Without the break-glass identity, manual create, update, and delete attempts succeed 0 times, across a test set that covers every declared object kind, one undeclared new object, and the workstation credential. During the SC-001 and SC-002 runs, the rule rejects the reconciler, system components, and the vault unseal procedure 0 times.

## Assumptions

- **Tool choice:** Flux, and running it in-cluster as ordinary workloads, are user-selected scope constraints. Exact components, versions, file layout, intervals, and annotations belong to planning.
- **Governance prerequisite (planning gate):** Constitution Principle I and the related deployment gate currently defer automated reconciliation to a future phase, as do [SPEC.md](../../SPEC.md) §2, §4 (DEPLOY-01, DEPLOY-04) and the deployment requirement that the initial deployment not require an in-cluster GitOps controller. They must be amended before planning passes the constitution check. This specification does not amend them itself.
- **Unchanged components:** the vault and its proxies (spec 002) and the namespaces stay as they are. The existing VM bootstrap (spec 003) stays as it is apart from one added final stage that installs the reconciler. The contents of workload declarations stay as they are apart from: reconciliation annotations (prune protection and forced re-creation), explicit namespaces on objects that lack one, the vault readiness/liveness probe split required by Story 2.3, resource bounds on the one container that has none (required by FR-013), and exact version tags on the two third-party images that use floating tags (database and identity provider; required by constitution Principle I, because the reconciler would otherwise apply upstream changes that no one reviewed).
- **Bootstrap scope change:** the spec 003 scope document currently places all deployment outside the VM bootstrap. It is amended so that the bootstrap installs the reconciler only. Vault installation and all other workloads stay outside the bootstrap and are applied by the reconciler.
- **Out of scope here:** known blockers owned by other work (secret delivery into workloads, publishing custom images, private-registry routing, TLS and hostnames, ingress cross-namespace routing, off-node backups). Layers that depend on them may report not-ready until those fixes land, and that state is correct, visible behaviour rather than a failure of this feature. Until custom images are published, the three project-built images keep their floating `latest` references; this is a recorded exception to constitution Principle I owned by the image-publishing work.
- **Verification order:** first on the operator's local VM, which is the disposable test cluster for the `local-k3s` environment: every test runs there, including the destructive ones, and it holds no data that must be kept. Then on the production VPS after the spec 003 live bootstrap, with non-destructive smoke tests only.
- **Delivery during implementation:** the work reaches `main` in small pull requests, each passing the required checks once they exist, so the local VM's reconciler applies each finished piece. The VPS starts tracking `main` only after its bootstrap at the end, so unfinished work never reaches production. The retired manual path may stop working on `main` before it is removed (Story 6); no environment uses it during this period.
- **Repository and break-glass access:** the repository is hosted on GitHub. The operator keeps break-glass cluster access through the existing SSH path for vault unseal, suspend/resume, and emergencies. Status queries use the read-only workstation credential.
- **Enforcement limit:** anyone with root on the node can bypass any cluster-level rule. That is accepted: root access is controlled by the existing SSH path, and the goal of Story 7 is that a manual change is never accidental and always deliberate. Existing verification scripts that create or delete objects in managed namespaces run under the break-glass identity or are adjusted.
- **Public repository:** making the repository public exposes hostnames, policies, and architecture. That is acceptable, because confidentiality rests on the vault and the secret scanning, not on hiding the repository. Git history contains SOPS-encrypted real credentials from the earlier attempt (Google identity-broker client and Keycloak bootstrap admin, commit `be85073`). Encrypted values are not plaintext leaks, but they are treated as exposed: those credentials are rotated before publication rather than rewriting history.
- **Excluded:** automated image-update controllers, notification and alerting integrations, multi-cluster or management-cluster topologies, and Helm-based packaging changes. CI-driven image digest updates are separate work.
