# Contract: Bootstrap Reconciler Stage

**Feature**: [spec.md](../spec.md) Story 2.1, Story 2.5, FR-002, SC-001 | **Decisions**: [research.md](../research.md) R10, R17

Stage 4 of `bootstrap/bootstrap.yml` (spec 003). It follows the existing stage pattern: set `current_stage` and `stage_failure_remediation`, record outcomes in `bootstrap_result`, and fail through the existing `rescue` block.

## 1. Inputs

| Input | Rule |
|---|---|
| `gitops_environment` | NEW, required. MUST be in the allowlist [`local-k3s`, `vps-k3s`]. Validated in the controller play next to `k3s_version`. |
| `clusters/<gitops_environment>/flux-system/` | MUST exist in the controller's checkout (`playbook_dir/../clusters/...`). Checked in preflight, so `--check` fails early when it is missing. |

## 2. Steps (`bootstrap/tasks/reconciler.yml`)

| # | Step | Command (on the node, as root) | Rule |
|---|---|---|---|
| 1 | Set stage | `current_stage: reconciler`; remediation: "Check `k3s kubectl -n flux-system get pods,events`, outbound HTTPS to ghcr.io, then re-run bootstrap." | — |
| 2 | Detect | `k3s kubectl -n flux-system get deployment source-controller kustomize-controller` | rc 0 → `reconciler_preexisting: true` and skip steps 3–4 |
| 3 | Copy | Copy `clusters/<env>/flux-system/` to `/var/lib/nacfson-bootstrap/flux-system/` (owner root, mode 0700) | Only when not preexisting |
| 4 | Apply | `k3s kubectl apply --server-side -k /var/lib/nacfson-bootstrap/flux-system/`; on failure, `k3s kubectl wait --for=condition=Established crd --all --timeout=60s` and apply again (max 3 attempts) | Only when not preexisting |
| 5 | Verify | `k3s kubectl -n flux-system rollout status deployment/source-controller --timeout=300s`, then the same for `kustomize-controller` | Always |
| 6 | Record | `readiness_outcomes.reconciler_ready: true`; `completed_stages += ['reconciler']`; `reconciler_preexisting` in the result | Always |

## 3. Result changes

| Field | Before | After |
|---|---|---|
| `readiness_outcomes` | service, API, node, CoreDNS | + `reconciler_ready` |
| Final `next_action` | "Base cluster bootstrap ready. Proceed to separate platform deployment." | "Cluster and reconciler ready. Run `scripts/vault-init.sh` to initialize or unseal the vault; Flux applies all other layers." |

## 4. MUST NOT

- Update, delete, or downgrade an existing Flux installation (Story 2.5).
- Apply any `deploy/` layer or vault manifest.
- Copy a kubeconfig off the node or open the API.
- Run under `--check` (the existing check-mode short-circuit ends the play before stage 2).

## 5. Known limit

If Flux was installed, the `governance` layer has applied the [manual-change policy](manual-change-policy.md), and the Flux Deployments were later deleted, step 4 is denied, because the bootstrap uses the K3s admin credential without the break-glass group. The stage then fails with the remediation "Reinstall under break-glass (operations §12)". This is intended: a re-install over a live, governed cluster is a break-glass action.
