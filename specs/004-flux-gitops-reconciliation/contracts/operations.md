# Contract: Operations in the Reconciled Model

**Feature**: [spec.md](../spec.md) FR-002, FR-007, FR-008, FR-012, FR-014, FR-015, FR-016 | **Decisions**: [research.md](../research.md) R2, R10, R14, R16

These are the only sanctioned operator procedures; the rewritten `docs/operations-runbook.md` follows them. `<env>` ∈ {`local-k3s`, `vps-k3s`}.

Two kinds of cluster access exist:

| Access | Where | Can write? |
|---|---|---|
| **Read-only** | Workstation, through the SSH tunnel (`ssh -L 6443:127.0.0.1:6443 <vm>`), kubeconfig from §13 | No |
| **Break-glass** | On the node only, over SSH (§12) | Yes, deliberately |

No kubeconfig is ever kept in Git.

## 1. Publication precondition (one time)

The repository must be public before any cluster can pull it (R2). Steps, in order:

1. Rotate the credentials that appear SOPS-encrypted in commit `be85073`: the Google OAuth client secret (Google Cloud Console) and the Keycloak bootstrap admin password.
2. `gitleaks git . --log-opts="--all"`: zero unresolved findings. False positives go into `.gitleaksignore` with a justification comment.
3. Change the GitHub repository visibility to public.
4. Configure branch protection on `main` per [platform-preflight §7](platform-preflight.md#7-branch-protection-operator-configuration-documented-in-the-runbook).

**Exit criterion**: `git ls-remote https://github.com/nacfson/nacfson_pipeline main` succeeds without credentials.

## 2. Capacity measurement (per environment, whenever the node changes)

1. `kubectl get node <name> -o jsonpath='{.status.allocatable}'` (read-only access is enough), converted to millicores and MiB.
2. Sum the live requests of the K3s bundled pods in `kube-system` to get `systemReserved`.
3. Commit `clusters/<env>/capacity.yaml` with `nodeName` and `observedAt`, through a PR.

**Rule**: the values MUST come from observing the node, never from the desired figure (DEPLOY-04).

## 3. Install (VM bootstrap; no separate manual step)

Preconditions: §1 is complete; this feature is merged to `main`.

```text
ansible-playbook -i '<vm>,' -u <ssh-user> bootstrap/bootstrap.yml \
  -e k3s_version=v1.35.9+k3s1 -e gitops_environment=<env>
```

**Result**: K3s is installed and verified, then stage 4 installs Flux ([contract](bootstrap-reconciler-stage.md)). Flux pulls `main` and applies `clusters/<env>`, including its own manifests and `layers.yaml`. Re-running the bootstrap leaves an existing Flux unchanged.

**Exit criterion**: `bootstrap_result.status: ready` with `reconciler_ready: true`; `flux get kustomizations -A` lists all 9 layers plus `flux-system`.

## 4. Vault unseal (after first start and after every OpenBao restart)

Run `scripts/vault-init.sh`, unchanged. It only uses `kubectl exec`, which the manual-change policy never matches. While OpenBao is sealed, `vault` is `Ready=False`, and `database`, `identity`, `ingress`, `vault-proxies` and `proj-pn` report `DependencyNotReady`. They converge without further action after unseal.

## 5. Status (read-only)

| Need | Command |
|---|---|
| All layers: revision, Ready, reason | `flux get kustomizations -A` |
| Source revision | `flux get sources git -A` |
| Objects of a layer | `flux tree kustomization <layer>` |
| Why a layer failed | `flux events --for Kustomization/<layer>` |

## 6. Deploy

Open a PR → required checks pass → review → merge to `main`. No cluster command is needed. Flux picks up the commit within 1 minute. Forcing an earlier sync is a write and needs break-glass; normally just wait.

## 7. Roll back

`git revert <sha>` → PR → merge. Flux converges to the previous declared state.

## 8. Emergency change (break-glass)

All commands use the break-glass prefix from §12.

1. Suspend the layer: `kubectl patch kustomization <layer> -n flux-system --type=merge -p '{"spec":{"suspend":true}}'`. Other layers keep reconciling.
2. Apply the temporary fix.
3. Commit the same fix to Git through a PR (constitution Principle I).
4. Resume: the same patch with `"suspend":false`. Any difference left between the cluster and Git is reverted.

## 9. Deleting protected data (break-glass)

Namespaces and PVCs marked `prune: disabled` are never deleted by Flux. To decommission, first remove their declarations from Git and merge, then delete the objects with `kubectl delete` under break-glass. Record why in the PR.

## 10. Upgrade Flux

Regenerate `clusters/<env>/flux-system/gotk-components.yaml` with the new pinned CLI (`flux install --version=<v> --components=source-controller,kustomize-controller --export`), update the CI pin to the same version, and merge the PR. Flux upgrades itself.

## 11. Retired procedures

`scripts/apply-release.sh`, `scripts/rollback-release.sh`, `scripts/preflight-budget.sh`, `.deploy-baseline`, and the manual `kubectl apply -f deploy/...` sequence in the runbook are removed. Any instruction that applies `deploy/` manually is invalid.

## 12. Break-glass identity

On the node, over SSH:

```text
sudo k3s kubectl --as=break-glass:<operator> --as-group=platform:break-glass <command>
```

- Without the `--as-group` flag, the same command is denied in managed namespaces, even as the K3s admin ([policy](manual-change-policy.md)).
- Every break-glass change MUST be reconciled back to Git through a PR.
- Reinstalling Flux on a governed cluster ([bootstrap §5](bootstrap-reconciler-stage.md#5-known-limit)) applies `clusters/<env>/flux-system` with this prefix.
- Policy recovery (last resort): [manual-change-policy §6](manual-change-policy.md#6-recovery).

## 13. Read-only workstation kubeconfig

1. Delete any admin kubeconfig previously copied from the node (for example `~/.kube/config` pointing at the VM). If it may have been shared, rotate the K3s client certificates.
2. Run `scripts/operator-kubeconfig.sh <vm>`. It requests a 24-hour token for `governance/operator-readonly` on the node over SSH, and writes `~/.kube/nacfson-<env>.yaml` pointing at `https://127.0.0.1:6443` (through the tunnel).
3. Re-run it when the token expires.

**Exit criterion**: `kubectl auth can-i create deployments -n identity` → `no`; `flux get kustomizations -A` works.
