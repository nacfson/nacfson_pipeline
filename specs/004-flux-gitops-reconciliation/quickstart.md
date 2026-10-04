# Quickstart: Validate Flux GitOps Reconciliation

**Feature**: [spec.md](spec.md) | **Contracts**: [layers](contracts/reconciliation-layers.md), [preflight](contracts/platform-preflight.md), [operations](contracts/operations.md), [bootstrap stage](contracts/bootstrap-reconciler-stage.md), [manual-change policy](contracts/manual-change-policy.md)

This guide proves the success criteria end to end, first on your local VM (the test VM: K3s v1.35, environment `local-k3s`, **disposable**), where every step runs, including destructive ones. The VPS (`vps-k3s`) then gets only the non-destructive smoke steps in section E. It references procedures instead of repeating them. "Break-glass" means the prefix in [operations §12](contracts/operations.md#12-break-glass-identity), run on the node.

## Prerequisites

- [ ] Publication precondition complete ([operations §1](contracts/operations.md#1-publication-precondition-one-time)); the repository can be cloned anonymously.
- [ ] Branch protection active on `main` with the required checks.
- [ ] The tasks under test are merged to `main` through pull requests that passed the required checks.
- [ ] Local VM (test VM): clean, meets the spec 003 support profile, reachable over SSH; holds only disposable data.
- [ ] Workstation: Ansible per `bootstrap/requirements.txt`, `flux` 2.9.5, `kubectl`, `gitleaks`, Python 3.12.
- [ ] `clusters/local-k3s/capacity.yaml` measured from the test node ([operations §2](contracts/operations.md#2-capacity-measurement-per-environment-whenever-the-node-changes)).

## A. CI gate (no cluster needed): SC-006, SC-009

| Step | Action | Expected |
|---|---|---|
| A1 | PR with the restructure (no content change) | `render-validate` and `platform-preflight` pass for both environments |
| A2 | PR adding `type: NodePort` to a PN Service | `platform-preflight` fails with `REJECTED_ISOLATION`; merge is blocked |
| A3 | PR lowering `pn-backend` memory limit | `REJECTED_MEMORY_SHRINK`; blocked |
| A4 | PR raising `pn-backend` replicas past the slice | `REJECTED_OVER_BUDGET`; blocked |
| A5 | PR adding `postBuild.substitute` to any layer | `render-validate` fails (invariant 1) |
| A6 | PR deleting `capacity.yaml` | `REJECTED_NO_MEASUREMENT`; blocked |
| A7 | `tests/preflight` fixture suite | All fixtures match the [contract table](contracts/platform-preflight.md#6-required-fixtures-testspreflight) |
| A8 | gitleaks over full history | Zero findings |
| A9 | PR adding a Namespace under `deploy/` without adding it to the policy binding | `render-validate` fails (invariant 8) |
| A10 | PR changing a policy binding to `validationActions: [Warn]` | `render-validate` fails (invariant 8) |

## B. Bootstrap and ordered bring-up: SC-001, SC-008, SC-010

| Step | Action | Expected |
|---|---|---|
| B1 | Run the [bootstrap](contracts/operations.md#3-install-vm-bootstrap-no-separate-manual-step) on the clean test VM with `gitops_environment=local-k3s` | `status: ready`, `reconciler_ready: true`, `reconciler_preexisting: false`; `flux get kustomizations -A` lists 9 layers plus `flux-system` |
| B2 | Observe before unseal | `governance` Ready; `vault` Ready=False (sealed); `database`, `identity`, `ingress`, `vault-proxies` and `proj-pn` show `DependencyNotReady` naming their dependency |
| B3 | [Unseal](contracts/operations.md#4-vault-unseal-after-first-start-and-after-every-openbao-restart) | `vault` Ready within 1 retry (≤ 1 min) |
| B4 | Observe downstream | Each layer becomes Ready, or fails **only** for an out-of-scope blocker named in the spec, with the cause visible in `flux get kustomizations -A`. No manual apply is done. |
| B5 | `kubectl -n flux-system top pods` after 3 reconciliation cycles, plus the restart count | Memory below the 128Mi limits; zero OOMKilled (SC-008) |
| B6 | `flux get kustomizations -A` timed | Revision and health for every layer in under 1 min (SC-010) |
| B7 | Run the bootstrap again | `status: ready`, `reconciler_preexisting: true`; the Flux Deployments' `metadata.generation` is unchanged (Story 2.5, SC-001) |
| B8 | On a throwaway branch tracked only by the test cluster (procedure in D), change `clusters/local-k3s/flux-system/`: regenerate `gotk-components.yaml` with a newer Flux patch release if one exists, otherwise add a label to both controller Deployments. Then repeat B7. | Both controllers roll out the change with no cluster command; the B7 re-run leaves the new state unchanged (Story 2.4, Story 2.5) |

## C. Delivery, drift, prune, rollback: SC-002 to SC-005

Use a disposable marker: a label `platform.nacfson.io/probe: <n>` on objects in `governance`.

| Step | Action | Expected |
|---|---|---|
| C1 | Merge a label change, ×10 | Visible on the cluster ≤ 5 min after each merge, with zero cluster commands (SC-002) |
| C2 | Under break-glass, `kubectl label`/`edit` 5 different managed objects | Each reverted ≤ 10 min (SC-003) |
| C3 | Merge removal of a test ConfigMap | Object gone ≤ 10 min (SC-004) |
| C4 | Write test rows into PostgreSQL (disposable), then merge removal of the `database` layer entry **and** the `identity` Namespace declaration | Deployments and Services pruned; `postgres-data-postgres-0` and `postgres-backup-pvc` and the `identity` namespace remain; rows intact after restoring the layer (SC-004) |
| C5 | Same for `vault`: remove the layer entry | `data-openbao-0` and `openbao-backup-pvc` remain |
| C6 | `git revert` the C1 change | Previous state restored ≤ 10 min (SC-005) |
| C7 | Under break-glass: suspend `governance`, edit an object, wait one interval, resume | Edit persists while suspended, is reverted after resume, and other layers keep reconciling |
| C8 | Reboot the test VM, then run only the [unseal](contracts/operations.md#4-vault-unseal-after-first-start-and-after-every-openbao-restart) | Every layer returns to its pre-reboot state ≤ 10 min after unseal, with no other command (edge case "Restarts") |
| C9 | Under break-glass, suspend the root `flux-system` Kustomization and set the GitRepository URL to a non-existent repository; wait 2 intervals; restore the URL and resume | `flux get sources git` shows Ready=False with the fetch error; every layer keeps its last applied revision and its objects; after restore, all layers are Ready (edge case "Repository unreachable") |

## D. Tenant boundary: SC-007

On a throwaway branch tracked only by the test cluster: under break-glass, suspend the root `flux-system` Kustomization (otherwise it restores the branch within 10 min), then patch the GitRepository branch. Add each of these to `deploy/projects/pn/workloads/`:

| Probe object | Expected |
|---|---|
| `ResourceQuota` in proj-pn | `proj-pn` layer fails with an RBAC "forbidden" error; existing quota unchanged |
| `NetworkPolicy` in proj-pn | Same |
| `ServiceAccount` / `RoleBinding` in proj-pn | Same |
| `Namespace` change for proj-pn | Same |
| `Deployment` with `metadata.namespace: identity` | Same; nothing created in `identity` |

Expected total: **0** successful writes; other layers stay Ready. Afterwards restore the branch and resume `flux-system` under break-glass.

## F. Manual-change rejection: SC-011

| Step | Action | Expected |
|---|---|---|
| F0 | During B and C, check for system requests denied by the policy: `kubectl get events -A` (look for `FailedCreate` or denials) and the controller logs | None. Any K3s internal user that is denied is added to allow rule 4 in the [policy contract](contracts/manual-change-policy.md#3-allow-rule-identical-in-both-policies) and Git, then B–C are re-run. |
| F1 | On the node, as the K3s admin **without** break-glass: create, update and delete one object of every declared kind in the managed namespaces, plus each listed cluster-scoped kind | Every request denied with the policy message; cluster unchanged |
| F2 | Same identity: create a new ConfigMap in `identity`, a Pod in `default`, and a new Namespace | All denied |
| F3 | Workstation read-only kubeconfig ([operations §13](contracts/operations.md#13-read-only-workstation-kubeconfig)): `kubectl auth can-i --list`, then try one write | Read-only verbs only; the write is denied |
| F4 | Repeat one F1 edit under break-glass | Accepted; Flux reverts it ≤ 10 min |
| F5 | Operational actions as the K3s admin: `vault-init.sh` unseal, `kubectl delete pod` in `identity`, `logs`, `exec`, `port-forward`, `create token` for `operator-readonly` | All allowed |
| F6 | `scripts/verify-isolation.sh` in break-glass mode | Pod Security still rejects the root pod |

Expected total for F1–F3: **0** successful manual writes. F0 and F5: **0** denials of Flux, system components, or the unseal procedure.

## E. Production (`vps-k3s`)

0. Before the bootstrap, compare the VPS OCPU and memory with `clusters/local-k3s/capacity.yaml`. **Stop if the VPS is smaller.** The first VPS deployment is checked only against local-k3s (accepted gap, see the plan's Complexity Tracking).
1. Run the bootstrap on the OCI A1 VM with `gitops_environment=vps-k3s` (`status: ready`, `reconciler_ready: true`).
2. Measure and commit `clusters/vps-k3s/capacity.yaml`; the PR passes the gate. Then add `platform-preflight (vps-k3s)` to the required checks on `main`.
3. Repeat B2–B7 on the VPS. Run C1 (×3) and C2 (×2) as a smoke test, plus F1 for one object, F3, and F5. The destructive C4/C5, D, and the full F1–F2 run on the test cluster only.
4. Replace the workstation admin kubeconfig with the read-only one ([operations §13](contracts/operations.md#13-read-only-workstation-kubeconfig)).
5. Confirm the retired scripts are absent and the runbook matches [operations.md](contracts/operations.md) (Story 6).

## Exit

All of A–D and F pass on the test cluster, and E passes on the VPS. Record the results in `specs/004-flux-gitops-reconciliation/validation.md`.
