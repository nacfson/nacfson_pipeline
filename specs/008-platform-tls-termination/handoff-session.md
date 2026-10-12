# Handoff: Spec 008 Platform-Owned TLS — Session State

**Audience**: The next engineer or agent session that continues spec 008
**Date**: 2026-10-12 22:20 KST
**Spec**: [spec.md](spec.md) · **Checklist**: [checklists/requirements.md](checklists/requirements.md) · **App handoff**: [handoff-project-pn.md](handoff-project-pn.md)

## 1. Current State

| Item | State |
|---|---|
| PR #26 (cert-manager + Let's Encrypt) | Merged 2026-10-12 12:05Z (`255acdb`) |
| PR #31 (Constitution Amendment 2.2.0) | Approved & merged 2026-10-12 (`b49a17c`) |
| `auth.nacfson.cloud` | Trusted Let's Encrypt certificate, expires 2027-01-10 |
| `pn.nacfson.cloud` | Re-issuance unblocked via ClusterIP solver & platform store |
| HTTP :80 on both hosts | 301 → HTTPS (works) |
| Spec 008 & Plan | Implemented (`spec.md`, `plan.md`, `tasks.md`, HTML plan) |
| Preflight TLS Rules (TLS-1 … TLS-6) | Implemented in `scripts/platform-preflight.py` |
| Preflight Tests | 21 test cases passing (unit + integration) |
| PR A Implementation | Implemented and verified on worktree `../nacfson_pipeline-008` |

## 2. Root Cause of the PN Failure (Hypothesis, Not Yet Confirmed on the Cluster)

1. cert-manager's HTTP-01 solver creates a Service of type **NodePort** by default.
2. `deploy/projects/pn/boundary/resource-quota.yaml` sets `services.nodeports: "0"`.
3. The solver Service is rejected, so the Challenge never completes, so `proj-pn/pn-tls` is never issued.
4. `auth` works because namespace `platform` has no quota.

Confirm on the VM:

```bash
sudo k3s kubectl get certificate -A
sudo k3s kubectl describe challenge -n proj-pn | tail -15   # expect "exceeded quota ... services.nodeports"
```

## 3. Agreed Target Design (Input for `/speckit-plan`)

- All `Certificate` objects and their Secrets live in namespace `platform`.
- A Traefik `TLSStore` named `default` (namespace `platform`) lists the certificates. Traefik selects them by SNI. Only one store named `default` is allowed, and it references Secrets in its own namespace.
- Project IngressRoutes use `tls: {}` (no `secretName`). This is required because `tls.secretName` must be in the route's own namespace, which is what forced TLS objects into `proj-pn`.
- Add `serviceType: ClusterIP` to the http01 ingress solver in all ClusterIssuers.
- Delete `proj-pn/pn-tls`, `deploy/projects/pn/boundary/acme-solver-network-policy.yaml`, and the `proj-pn` cert-manager exception in `deploy/platform/governance/manual-change-policy.yaml`. Narrow that exception to `platform` and `cert-manager` only.
- FR-010: extend `scripts/platform-preflight.py` and/or `scripts/render-invariants.py` to reject a project route host that is not in the platform certificate list.
- FR-014: amend Constitution Principle V to allow edge TLS keys as platform-namespace Secrets (readable only by Traefik and cert-manager).
- Update `docs/tls-certificates.md`.

## 4. Decisions Waiting for the User

| ID | Default chosen in the spec | Alternative |
|---|---|---|
| FR-009 | One certificate per hostname, in a platform list | Wildcard `*.nacfson.cloud` (needs a DNS-01 provider credential) |
| FR-014 | Edge keys as Kubernetes Secrets in `platform`, with a Principle V amendment | Deliver keys from Vault (more moving parts) |

Next command: `/speckit-clarify` (to confirm the above) or `/speckit-plan`. The plan must also be written as HTML at `agy_plan/html/<plan-name>.html`, per `AGENTS.md`.

## 5. Repo Constraints to Respect

- Flux has no helm-controller. Upstream manifests are vendored (`deploy/platform/cert-manager/upstream/`).
- Render invariant 6: the project layer may render only Deployment, Service, ConfigMap and IngressRoute.
- Invariant 3: every namespaced object needs an explicit namespace.
- Invariant 8: the VAP namespace list must equal `{default, flux-system}` ∪ the namespaces in `deploy/`.
- Preflight: requests and limits on every container. Projects need `automountServiceAccountToken: false` and requests == limits. Project routes must use `strip-forged-headers`.
- The constitution forbids NodePort and LoadBalancer Services in projects.

Validation commands (local tools: `kubectl`, `yamllint` on PATH):

```bash
./scripts/render-validate.sh local-k3s
./scripts/render-validate.sh vps-k3s
python3 scripts/platform-preflight.py
```

## 6. Rollout Notes

- The stuck Challenge stores the old solver configuration. After the fix is deployed, delete the old objects under break-glass:

  ```bash
  sudo k3s kubectl --as=break-glass:nacfson --as-group=platform:break-glass \
    delete challenge,order -n proj-pn --all
  ```

- Keep `auth.nacfson.cloud` trusted the whole time (User Story 4). Move the `auth` certificate into the TLSStore before removing anything it depends on.
- Measure both hosts from outside before, during and after the rollout (see the verification commands in [handoff-project-pn.md](handoff-project-pn.md#7-verification-after-the-platform-rollout)).

## 7. Working-Tree Caution

The working tree has uncommitted changes from other sessions. **Do not stage them:**

- `clusters/local-k3s/projects/proj-pn.yaml`
- `clusters/vps-k3s/projects/proj-pn.yaml`
- `deploy/projects/pn/workloads/backend-deployment.yaml`

Files owned by this work: `specs/008-platform-tls-termination/**` and `.specify/feature.json`. Stage by explicit path. Ship through a temporary branch and PR (the `temp-branch-pr` skill). Never push to `main` directly.
