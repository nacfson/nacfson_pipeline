# Proposal

## Why

The platform needs one implementable contract from replaceable infrastructure through authenticated application delivery. Keeping the GitOps foundation, central identity, and multi-repository delivery as separately applied changes creates avoidable ordering and ownership ambiguity around the project model, deployment-repository writer, Flux observation, and reusable chart. A single change makes those dependencies internal phases of one architecture: every resource has one writer, ProcessManager has one GitOps kernel, and a protected source commit reaches production only through an immutable, observable, recoverable path.

## What Changes

- Establish `platform-config` as the authoritative production desired-state repository with deterministic cluster, infrastructure, application, and reusable-chart boundaries.
- Assign external provider resources to OpenTofu, host configuration and k3s/Flux bootstrap to Ansible, Git-managed Kubernetes resources to Flux, and placement to Kubernetes capability matching; prohibit overlapping writers and hard-coded provider or node identity in project configuration.
- Add offline validation for Kustomize roots, Helm values and rendering, cross-references, namespace containment, ownership, determinism, and plaintext-secret rejection; represent secret-bearing manifests only with SOPS encryption.
- Deploy a recoverable, non-HA Keycloak and CloudNativePG identity stack, broker ordinary users through Google OpenID Connect, require Keycloak-controlled TOTP for administrators, and retain one restricted local recovery administrator.
- Define `public`, `oidc-protected`, and `oidc-native` project access policies with isolated clients, Secrets, groups, callbacks, proxy routing, and network policy; remove the custom Go password/session service from production.
- Replace ProcessManager's imperative Kubernetes mutations with one versioned `ProjectDesiredStateStore` for validated, signed, project-scoped Git transactions and one read-only `ProjectReconciliationObserver` for exact-revision Flux and workload status. Identity and delivery use the same instances.
- Register existing trusted Forgejo repositories declaratively without creating or templating source repositories. Each project receives a protected default branch, versioned build contract, assigned OCI namespace, deployment path, runtime policy, and delivery policy.
- Run approved protected-branch builds through Forgejo Actions and rootless BuildKit with bounded concurrency and project-scoped credentials; publish registry-confirmed immutable digests while denying production authority to pull requests, forks, and unregistered repositories.
- Automatically promote only the current protected-branch head through `ProjectDesiredStateStore`; stale builds become `superseded`, automatic diffs change only release identity, and CI never receives deployment-repository or Kubernetes credentials.
- Correlate build, artifact, Git, Flux, workload, and rollback states without reporting build or commit success as readiness. Restore the previous `ready` digest through one project-scoped rollback only for a confirmed current-release failure and rollback-compatible state.
- **BREAKING** Managed production no longer permits the legacy custom authentication path, mutable image tags as desired state, direct CI-to-Kubernetes deployment, or direct ProcessManager application mutation.

## Capabilities

### New Capabilities

- `platform/gitops-config`: Authoritative deployment layout, Flux root, chart boundary, ownership model, encrypted-secret representation, and validation gate.
- `identity/central-identity`: Central Keycloak authority, dedicated CNPG persistence, account isolation, administration, capacity targets, and recovery behavior.
- `identity/project-access`: Tagged project access policies, trusted identity boundaries, reusable chart behavior, and the shared ProcessManager GitOps kernel.
- `platform/project-onboarding`: Existing-repository registration, source build-contract validation, trust and branch requirements, namespace assignment, runtime ownership, and deregistration.
- `platform/project-delivery`: Trusted builds, immutable artifacts, current-head promotion, end-to-end release state, and bounded automatic rollback.

### Modified Capabilities

None. The project has no existing main capability specifications.

## Impact

- Replaces the three unimplemented planning changes `establish-gitops-platform-config`, `add-central-project-identity`, and `add-multi-repository-project-delivery` with one dependency-ordered change.
- Affects this repository's future `platform-config`, charts, validation, Flux, SOPS, Keycloak, CNPG, Forgejo runner, registry, backup, and recovery configuration.
- Affects the sibling `/Users/hyungjuyu/Projects/Brain/ProcessManager` project model, handlers, authentication boundary, GitOps kernel, Forgejo/OCI adapters, release orchestration, status presentation, and legacy-auth removal.
- Requires version-pinned Flux, Kustomize, Helm, SOPS, Keycloak, CloudNativePG, oauth2-proxy, Forgejo Actions, rootless BuildKit, OCI registry, ingress, certificate, and backup components.
- Preserves one implementation owner per boundary: one chart, one project model, one deployment-repository store, one reconciliation observer, one identity authority, and Flux as the sole routine Kubernetes writer.
- Does not create source repositories, support untrusted source-control providers, add preview or progressive environments, provide high availability, automatically reverse incompatible data migrations, or allow application delivery to provision external infrastructure.
