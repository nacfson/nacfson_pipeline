# Research: Flux GitOps Reconciliation

**Feature**: [spec.md](spec.md) | **Plan**: [plan.md](plan.md) | **Date**: 2026-10-03

Evidence was gathered locally with the Flux CLI 2.9.5 that is already installed (`flux install --export` and `flux build kustomization --dry-run` against the current `deploy/` tree), plus inspection of the repository. No Technical Context item remains NEEDS CLARIFICATION.

---

## R1. Reconciler placement and components

- **Decision**: Run Flux **2.9.5** inside each cluster as ordinary Deployments in `flux-system`, with only `source-controller` (v1.9.5) and `kustomize-controller` (v1.9.5).
- **Rationale**: This meets the spec (FR-001) and constitution Principle I. These two controllers are the minimum for pulling Git and applying Kustomize. `flux install --export` works offline from the pinned CLI and yields reviewable manifests (about 3,600 lines, including Flux's own NetworkPolicies `allow-egress`, `allow-scraping`, `allow-webhooks`).
- **Alternatives considered**:
  - `helm-controller`, `notification-controller`, and image automation: not needed and cost RAM; image automation also needs Git write access.
  - Argo CD: heavier, and the user had already chosen Flux.
  - Host systemd timer, CI push, or a management cluster: rejected in conversation. They lack ordering, prune, and health, or they expose the API or an admin kubeconfig.

## R2. Repository access and source definition

- **Decision**: Use a `GitRepository` named `flux-system` with URL `https://github.com/nacfson/nacfson_pipeline`, branch `main`, interval `1m`, **no `secretRef`**. Its `ignore` rules include only `/clusters/` and `/deploy/` in the artifact.
- **Rationale**: Spec clarification Option A and constitution Principle I (no repository credential in the cluster). Anonymous HTTPS needs only outbound 443, which Flux's bundled `allow-egress` policy permits. The `ignore` rules keep the artifact small. They do not skip reconciliation: a docs-only commit still produces a new revision, and the layers re-apply unchanged content as a no-op.
- **Alternatives considered**: SSH deploy key or GitHub App (needs a credential in the cluster, which would be a Principle V exception); an OCI artifact pushed by CI (needs a GHCR credential and a push pipeline).
- **Precondition**: the repository is currently not public. It must be published first, after the full-history gitleaks scan and after rotating the Google OAuth client secret and Keycloak bootstrap admin password that are SOPS-encrypted in commit `be85073`. See [operations.md §1](contracts/operations.md#1-publication-precondition-one-time).

## R3. Environment differences without variable substitution

- **Decision**: **Never use `spec.postBuild` substitution.** Express environment differences as inline `spec.patches` on the Flux Kustomizations in `clusters/<env>/layers.yaml` and `clusters/<env>/projects/`. CI fails if any Flux Kustomization declares `postBuild`.
- **Evidence** (local `flux build kustomization --dry-run` with a `postBuild.substitute` block):
  - `identity`: `KC_DB_PASSWORD: "${VAULT:keycloak_db_password}"` renders as `value: null`.
  - `database`: `--header="X-Vault-Token: ${VAULT_TOKEN}" ${VAULT_ADDR}/…` renders as `--header="X-Vault-Token: " /v1/…`.
  - 23 `${…}` occurrences across 5 files would be affected: `postgres-init-job` 11, database `backup-cronjob` 6, vault `backup-cronjob` 3, `keycloak-realm-config` 2, `keycloak-deployment` 1.
  - Without `postBuild`, all occurrences render unchanged (database: 17 in source, 17 rendered; Keycloak placeholder intact).
- **Rationale**: Substitution silently corrupts both shell scripts and Keycloak vault references. Escaping each one (`$${…}`, which was verified to work) or adding per-object opt-out annotations is fragile, because any future `${` would break silently. The environment-specific values are few (hostnames in two IngressRoutes, `COOKIE_DOMAIN`, `CENTRAL_AUTH_URL`) and are easy to patch.
- **Alternatives considered**: substitution plus escaping (fragile); substitution plus the `kustomize.toolkit.fluxcd.io/substitute: disabled` annotation per object (opt-out by default is unsafe); per-environment overlay directories (more files than inline patches for the same result).
- **Known limitation (handed off)**: `keycloak-realm-config` holds hostnames (`https://auth.example.com/oauth/callback` and `https://pn.example.com/*`) inside a 95-line JSON string, next to `${VAULT:google_client_*}` placeholders. Inline patches can only replace that key as a whole, which would mean copying the realm, including its vault placeholders, once per environment. Both environments currently share the `example.com` placeholders, so this feature only delivers and proves the patch mechanism on the simple values: the 5 IngressRoute `match` hosts, `COOKIE_DOMAIN`, and `CENTRAL_AUTH_URL`. How the realm gets real per-environment hostnames (for example Keycloak import-time environment placeholders) belongs to the hostname/TLS work listed as out of scope in the spec.

## R4. Layer graph, dependencies, and health

- **Decision**: 9 Flux Kustomizations per environment. The first 8 are in `clusters/<env>/layers.yaml` (namespace `flux-system`); `proj-pn` is in `clusters/<env>/projects/proj-pn.yaml` and is applied by the `projects` layer ([R18](#r18-first-install-ordering-for-project-layers)):

  | Layer | Path | dependsOn |
  |---|---|---|
  | `governance` | `deploy/platform/governance` | none |
  | `vault` | `deploy/platform/vault` | governance |
  | `vault-proxies` | `deploy/platform/vault-proxies` | vault |
  | `database` | `deploy/platform/database` | vault |
  | `identity` | `deploy/platform/identity` | database |
  | `ingress` | `deploy/platform/ingress` | identity |
  | `project-boundaries` | `deploy/projects/pn/boundary` | governance |
  | `projects` | `clusters/<env>/projects` | project-boundaries |
  | `proj-pn` (in ns `proj-pn`) | `deploy/projects/pn/workloads` | project-boundaries, ingress, vault-proxies |

  All layers use `wait: true`, `prune: true`, `interval: 5m`, `retryInterval: 1m`, `timeout: 5m`, except `projects`, which uses `wait: false` (R18).
- **Rationale**:
  - `wait: true` uses kstatus readiness for every object (Deployments available, StatefulSets ready, Jobs complete), so a dependent starts only on real health (FR-004).
  - `vault-proxies` is split from `vault` because the proxy images (`:latest`) are currently unpublished. In one layer they would hold the vault layer not-ready forever and block database and identity.
  - `proj-pn` depends on `vault-proxies` because the PN backend connects through `vault-db-proxy.vault.svc`.
- **Alternatives considered**: one big Kustomization (no ordering or per-layer status); `healthChecks` naming only selected objects (hides failing objects from layer status).

## R5. Sealed vault must read as not-ready without being killed

- **Decision**:
  - OpenBao **readiness** probe becomes `/v1/sys/health?standbyok=true` (sealed returns 503, uninitialized returns 501: not ready).
  - **liveness** becomes `/v1/sys/health?standbyok=true&sealedcode=204&uninitcode=204` (the process is alive even while sealed).
- **Rationale**:
  - Today readiness uses `sealedcode=204&uninitcode=204`, so a sealed vault is "Ready" and Flux would mark the vault layer healthy (this breaks Story 2.3).
  - Liveness uses strict health, so a sealed pod fails liveness after about 30 s + 3×15 s and restarts during manual unseal.
  - With the split, Flux reports `dependency 'flux-system/vault' is not ready` for downstream layers until unseal, which is the operator signal the spec requires.
  - `scripts/vault-init.sh` uses `kubectl exec` into the pod, which works on non-ready pods, so the unseal procedure is unaffected.
- **Alternatives considered**: a custom health check Job (extra object and code); leaving the probes as they are (wrong status plus restart loop).

## R6. Prune protection for persistent data

- **Decision**: Annotate `kustomize.toolkit.fluxcd.io/prune: disabled` on:
  - the PVCs declared in Git: `postgres-data-postgres-0`, the database backup PVC, the vault backup PVC;
  - every **Namespace** that holds persistent data or project state: `identity`, `vault`, `proj-pn`; for consistency also `platform` and `governance`.
- **Rationale**:
  - Pruning a Namespace cascades to every PVC inside it. Removing the `governance` or `vault` layer, or the whole `layers.yaml` entry, would otherwise delete PostgreSQL and vault data (FR-006, "including when their whole layer is removed").
  - `data-openbao-0` comes from a StatefulSet `volumeClaimTemplate`. Flux never tracks it in its inventory, and the StatefulSet's default PVC retention (`Retain`) keeps it when the StatefulSet is deleted.
- **Consequence**: deleting a namespace or PVC needs a deliberate manual action, documented as break-glass.
- **Alternatives considered**: `prune: false` on whole layers (then removed Deployments and Services would also linger, which breaks FR-005).

## R7. Immutable Job updates

- **Decision**: Annotate `postgres-init-job` with `kustomize.toolkit.fluxcd.io/force: enabled`.
- **Rationale**: A Job's pod template is immutable. With `force`, Flux recreates the Job when its spec changes instead of failing the layer permanently. The Job has no `ttlSecondsAfterFinished`, so an unchanged completed Job is not recreated on every interval.
- **Alternatives considered**: a TTL plus re-creation (would rerun init every 5 min); converting to a CronJob (changes semantics).

## R8. Project-scoped reconciliation

- **Decision**: The `proj-pn` Flux Kustomization lives in namespace `proj-pn` with `serviceAccountName: pn-reconciler` and a cross-namespace `sourceRef` to `flux-system/flux-system`. It has **no** `targetNamespace`. It is declared in `clusters/<env>/projects/proj-pn.yaml` and applied by the `projects` layer, not by the root ([R18](#r18-first-install-ordering-for-project-layers)). `deploy/projects/pn/boundary/reconciler-rbac.yaml` (applied by the platform layer `project-boundaries`) defines:
  - ServiceAccount `pn-reconciler` (token automount disabled; Flux impersonates it, so no mounted token is needed);
  - a Role in `proj-pn` allowing get, list, watch, create, update, patch and delete on `deployments`, `services`, `configmaps` and `traefik.io/ingressroutes`;
  - a RoleBinding in `proj-pn`.
- **Rationale**:
  - SPEC.md ISOLATE requirements say the project identity must not touch the Namespace, ResourceQuota, NetworkPolicy, ServiceAccount or RBAC.
  - Without `targetNamespace`, an object declared in another namespace is **rejected** by RBAC rather than silently rewritten into `proj-pn`, as Story 5.2 requires.
  - Cross-namespace source references are allowed by default in kustomize-controller.
- **Alternatives considered**: `targetNamespace: proj-pn` (rewrites rather than rejects); a separate GitRepository per tenant (duplicate source, no benefit with one repository).

## R9. Gate: protected source and required checks

- **Decision**: Every environment tracks the protected `main` branch. A new workflow, `.github/workflows/gitops.yaml`, runs a matrix over `local-k3s` and `vps-k3s`:
  1. **render-validate**: for each Flux Kustomization in `clusters/<env>/layers.yaml` and `clusters/<env>/projects/*.yaml`, run `flux build kustomization <name> --path <path> --kustomization-file <file that declares it> --dry-run` with the pinned CLI 2.9.5. Pipe the result to kubeconform (K8s 1.35, strict, Flux 2.9.5 `crd-schemas`, Traefik CRD schemas). Also fail on any `postBuild` and on any rendered namespaced object without a namespace.
  2. **platform-preflight**: run `scripts/platform-preflight.py` on the rendered output and `clusters/<env>/capacity.yaml`, plus the merge-base render for the memory freeze ([contract](contracts/platform-preflight.md)).

  Branch protection on `main` requires both matrix jobs plus the existing gitleaks and lint checks, with PR review. The secret-scan workflow trigger is widened to every PR into `main`.
- **Rationale**:
  - `flux build --dry-run` uses the controller's own Kustomize engine and Flux patch handling, which meets DEPLOY-04's "same versions Flux will apply" (verified offline).
  - A protected `main` meets the constitution's "protected deployment source per environment" with checks run separately per environment. Per-environment branches add promotion machinery with no benefit while only one production environment exists.
  - Branch protection is free on public repositories.
- **Alternatives considered**: a standalone kustomize binary (could differ from the controller's engine); per-environment promotion branches (deferred until a second production environment exists).

## R10. Installation and self-management

- **Decision**:
  - `clusters/<env>/flux-system/` holds:
    - `gotk-components.yaml`, generated by `flux install --version=v2.9.5 --components=source-controller,kustomize-controller --export`;
    - `gotk-sync.yaml`: the GitRepository from R2, plus the root Kustomization `flux-system` with path `./clusters/<env>`, `prune: true`, interval 10m;
    - a `kustomization.yaml` with the resource patches from R11.
  - `clusters/<env>/kustomization.yaml` lists only `flux-system` and `layers.yaml`, so `capacity.yaml` is never applied.
  - **Installation is stage 4 of the spec 003 bootstrap** (`bootstrap/tasks/reconciler.yml`), run after the existing verify stage ([contract](contracts/bootstrap-reconciler-stage.md)):
    1. A new required input, `gitops_environment` ∈ {`local-k3s`, `vps-k3s`}, is validated in the controller play. Preflight also asserts that `clusters/<env>/flux-system/` exists in the controller's checkout.
    2. If both `source-controller` and `kustomize-controller` Deployments already exist in `flux-system`, the apply is skipped (Story 2.5).
    3. Otherwise the directory is copied to the node and applied with `k3s kubectl apply --server-side -k`, as root on the node. If the CRDs are not yet established, the stage waits for them and re-applies (idempotent).
    4. In both cases the stage verifies that both Deployments are Available, then records `reconciler_ready` in the bootstrap result.
  - After installation, Flux manages its own manifests from `main`. Upgrades are a PR that regenerates `gotk-components.yaml`. If the checkout used for bootstrap differs from `main`, the root Kustomization converges Flux to `main` within one interval.
- **Rationale**:
  - FR-002 (no manual install action) and Story 2.5 (re-run never overwrites or downgrades).
  - Ansible already reaches the node over SSH and runs `k3s kubectl` locally (see `bootstrap/tasks/verify.yml`), so the API stays closed and no kubeconfig leaves the node.
  - Applying the same `clusters/<env>/flux-system` files that the root Kustomization later manages keeps a single source of truth.
- **Alternatives considered**:
  - A manual `kubectl apply` from the workstation (the previous decision): one more manual step; rejected by the spec clarification.
  - The K3s auto-deploy directory (`/var/lib/rancher/k3s/server/manifests`): K3s would keep re-applying the file on restart and fight Flux's self-upgrade from Git.
  - `kubectl apply -k` against a remote GitHub URL from the node: needs Git on the node and depends on GitHub during bootstrap.
  - `flux bootstrap github`: needs a PAT with admin rights and pushes directly to `main`, skipping the PR gate.

## R11. Reconciler resource footprint

- **Decision**: Patch the controllers with:

  | Controller | CPU request / limit | Memory request / limit |
  |---|---|---|
  | `source-controller` | 50m / 500m | 64Mi / 128Mi |
  | `kustomize-controller` | 100m / 500m | 64Mi / 128Mi |

  The platform reservation counts the **memory limits** (256 MiB total) and the CPU requests (150m).
- **Rationale**: The defaults are 50m/64Mi and 100m/64Mi requests with **1000m/1Gi limits each**, which allows up to 2 GiB of memory overcommit on a 12 GB node shared with Keycloak (1.5 GiB). 128 MiB is enough for a repository this size (about 50 objects). SC-008 (≤ 256 MiB) holds when the reservation is counted at memory limits.
- **Risk and fallback**: if acceptance shows OOMKilled restarts, raise to 256Mi each, update SC-008 with measured evidence, and re-run the preflight.

## R12. Namespace-less governance objects

- **Decision**: Give `default-deny-all` (base-network-policy) and `project-sa-template` an explicit `namespace: governance`.
- **Rationale**: Today `kubectl apply -f` drops them into the operator's current namespace (normally `default`), so they protect nothing. Reconciliation needs explicit namespaces, and CI will enforce this. `governance` has no workloads, so the change does not alter behaviour. Applying default-deny to `identity` or `platform` would break PostgreSQL and Keycloak traffic that has no allow policies, which is out of scope here.
- **Alternatives considered**: applying them to `identity`/`platform` (breaking change); dropping them (removes declared intent).

## R13. Retirement and documentation sync

- **Decision**:
  - Remove `scripts/apply-release.sh`, `scripts/rollback-release.sh`, `scripts/preflight-budget.sh`, `deploy/kustomization.yaml` and `deploy/environments/`. `.deploy-baseline` is never committed; remove any reference to it.
  - Rewrite `docs/operations-runbook.md` around [operations.md](contracts/operations.md).
  - Sync SPEC.md:
    - §2 table row "Deployment reconciliation" and the paragraph after it;
    - DEPLOY-01 (in-cluster Flux is now the initial model);
    - DEPLOY-04 (protected `main` with per-environment required checks);
    - the availability line "MUST NOT require … FluxCD";
    - the §2 "Secrets" row, which conflicts with Principle V (pre-existing drift).
- **Rationale**: FR-014, and the constitution follow-up TODOs from v1.4.0.

## R14. Break-glass, status, and the read-only workstation credential

- **Decision**:
  - **Status (workstation, read-only)**: the operator keeps the SSH tunnel to `127.0.0.1:6443` and the `flux` CLI 2.9.5, with a kubeconfig for ServiceAccount `governance/operator-readonly`, bound to the built-in `view` ClusterRole. Flux 2.9.5 aggregates read access to its own resources into `view` (`flux-view-flux-system`, verified in the exported manifests), and `view` excludes Secrets. `scripts/operator-kubeconfig.sh` builds the kubeconfig from a short-lived token (`kubectl create token --duration=24h`, run on the node over SSH) and the cluster CA. No long-lived token Secret exists.
  - Status commands: `flux get kustomizations -A`, `flux get sources git -A`, `flux tree`, `flux events`.
  - **Break-glass (node only)**: on the node, `sudo k3s kubectl --as=break-glass:<operator> --as-group=platform:break-glass …`. Only the node's K3s admin credential can impersonate. ClusterRoleBinding `platform-break-glass` grants the group `cluster-admin`. Suspend and resume are `kubectl patch kustomization <layer> -n flux-system --type=merge -p '{"spec":{"suspend":true|false}}'` under that identity, so the node does not need the Flux CLI.
  - The admin kubeconfig copied to the workstation earlier is deleted. K3s client certificates are rotated if that copy may have been shared.
- **Rationale**: FR-007, FR-008, FR-016, and SC-010. Writes need deliberate impersonation, which leaves the group name in every request the manual-change rule sees ([R16](#r16-cluster-side-rejection-of-manual-changes)). There is no dashboard or notification controller (spec exclusion).
- **Alternatives considered**: a long-lived ServiceAccount token Secret (a standing credential in the cluster and on disk); a K3s client certificate for a read-only user (needs CSR handling and certificate rotation); the Flux CLI on the node (more node software for two patch commands).

## R15. Interval selection

- **Decision**: GitRepository 1m; layer Kustomizations interval 5m, retryInterval 1m, timeout 5m; root `flux-system` Kustomization 10m.
- **Rationale**:
  - SC-002 (≤ 5 min from merge): the source detects the commit within 1 min, and the dependent Kustomizations reconcile as soon as the new artifact appears.
  - SC-003 and SC-004 (≤ 10 min): drift and prune correction happen at the latest on the 5-min interval.
  - The 1-min retry speeds up dependency chains after an unseal.

## R16. Cluster-side rejection of manual changes

- **Decision**: Use the built-in Kubernetes **ValidatingAdmissionPolicy** (`admissionregistration.k8s.io/v1`, GA since 1.30), declared in `deploy/platform/governance/manual-change-policy.yaml` and applied by the `governance` layer ([contract](contracts/manual-change-policy.md)):
  - Policy `gitops-managed-namespaces` matches CREATE, UPDATE and DELETE on all namespaced resources. Its binding selects namespaces by the API-server-set label `kubernetes.io/metadata.name` ∈ {`default`, `flux-system`, `governance`, `identity`, `platform`, `vault`, `proj-pn`}. `default` is included so that it cannot be used to run undeclared workloads.
  - Policy `gitops-managed-cluster-kinds` matches CREATE, UPDATE and DELETE on the cluster-scoped kinds declared in Git, plus the admission configuration kinds that could disable the rule: `namespaces`, `customresourcedefinitions`, `clusterroles`, `clusterrolebindings`, `validatingadmissionpolicies`, `validatingadmissionpolicybindings`, `validatingwebhookconfigurations`, `mutatingwebhookconfigurations`.
  - Both use the same allow rule: Flux ServiceAccounts in `flux-system`, `proj-pn:pn-reconciler`, Kubernetes and K3s system identities (`system:serviceaccount:kube-system:*`, `system:node:*`, `system:kube-controller-manager`, `system:kube-scheduler`, `system:apiserver`, and K3s internal users confirmed on the test cluster), the group `platform:break-glass`, and the operational exceptions: Pod DELETE, `pods/eviction`, and `serviceaccounts/token`. Everything else is denied with a message pointing to Git. `failurePolicy: Fail`, `validationActions: [Deny]`.
  - The binding does not use workload labels, so no existing declaration changes.
- **Rationale**:
  - FR-015: the rule runs inside the API server, so there is no extra service whose outage could block or open the cluster (spec edge case). Admission runs after RBAC and also applies to `system:masters`, so full administrators are blocked too.
  - Read, `exec`, `port-forward` and `logs` are GET or CONNECT requests and are never matched, so `vault-init.sh` (which only uses `kubectl exec`) works unchanged. Pod deletion stays allowed for restarts (`verify-persistence.sh`).
  - Flux server-side dry-runs run under the same Flux identity and are allowed.
- **Residual risks**:
  - `kube-system` stays open, because blocking it could break K3s. A K3s admin could still run a workload there. This is accepted with the root-on-node limit in the spec, and recorded in the runbook.
  - An incorrect allow rule could block a system controller. Mitigation: validate on the disposable test cluster first ([quickstart F](quickstart.md#f-manual-change-rejection-sc-011)); recovery is the break-glass group, and as a last resort root on the node restarts K3s with `--kube-apiserver-arg=disable-admission-plugins=ValidatingAdmissionPolicy` and removes the binding.
  - `verify-isolation.sh` creates a test pod in `proj-pn`. It gains a break-glass mode so it keeps testing Pod Security rather than this rule.
- **Alternatives considered**: Kyverno or OPA Gatekeeper (extra components, memory, and a webhook whose outage would fail open or closed); RBAC alone (cannot restrict `system:masters`); relying on Flux drift correction only (does not catch new undeclared objects and leaves edits live for up to 5 min).

## R17. Bootstrap scope change (spec 003)

- **Decision**: Amend `docs/bootstrap-scope.md` so the bootstrap installs the reconciler as its last stage and nothing else. Vault installation and every other workload stay outside it. The playbook header changes from three to four inputs, and the final `next_action` becomes "Run `scripts/vault-init.sh`; Flux applies all other layers."
- **Rationale**: Spec Assumption "Bootstrap scope change" and FR-002. This keeps the spec 003 boundary sharp: the bootstrap ends at "cluster plus reconciler running".
- **Alternatives considered**: a separate playbook for Flux (two commands again); folding vault installation into the bootstrap (conflicts with GitOps and spec 002).

## R18. First-install ordering for project layers

- **Decision**: Project layer objects (today only `proj-pn`) are **not** in `layers.yaml`. They live in `clusters/<env>/projects/` (`kustomization.yaml` plus one file per project, with that project's environment patches). A platform layer, `projects`, applies that directory:
  - namespace `flux-system`, `path: ./clusters/<env>/projects`, `dependsOn: project-boundaries`, controller identity (no `serviceAccountName`);
  - the common spec, except `wait: false`.
  - `clusters/<env>/kustomization.yaml` does not list `projects/`.
- **Rationale**:
  - The `proj-pn` object must live in namespace `proj-pn`, because Flux impersonates a ServiceAccount from the object's own namespace (R8). That namespace is created by `project-boundaries`, not by the root.
  - Flux dry-runs every object of a batch on the server before applying the batch. If `proj-pn` were in `layers.yaml`, its dry-run on a clean cluster would fail with "namespace not found". The root would then apply none of the layers, so nothing would ever create the namespace: a deadlock that blocks SC-001.
  - With `projects` depending on `project-boundaries`, the namespace and `pn-reconciler` exist before the `proj-pn` object is created. This order is correct whether or not Flux would recover by retrying, so the design does not depend on that behaviour.
  - `wait: false`: the only objects in `projects` are Flux Kustomizations that report their own health. Waiting on them would copy each project's failure onto `projects`.
  - Environment patches for projects stay under `clusters/<env>/`, so `deploy/` stays environment-neutral (Principle VI).
- **Alternatives considered**:
  - Keep `proj-pn` in `layers.yaml` and rely on retries (the earlier decision): likely deadlocks on a clean VM.
  - Put the `proj-pn` object in `deploy/projects/pn/boundary/`, so `project-boundaries` applies it together with its namespace: mixes environment patches into the shared base.
  - Move `Namespace proj-pn` into the root: one object with two owners.
- **Origin**: `/speckit-analyze` finding I1, accepted by the operator on 2026-10-03.
