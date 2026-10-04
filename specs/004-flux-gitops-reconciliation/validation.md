# Validation Record: Flux GitOps Reconciliation & Cluster Governance

**Date**: 2026-10-04  
**Feature**: `specs/004-flux-gitops-reconciliation`  
**Target Cluster**: Native K3s (`v1.36.5+k3s1`), single-node control-plane on Linux (`local-k3s`)  
**Tools Verified**: Flux CLI `v2.9.5`, Kubeconform `v0.6.7`, Python 3.12, Kubectl  
**Execution Status**: **ACCEPTED & VERIFIED END-TO-END**

---

## 1. Static & Gating Verification (CI Gate)

| Test Case | Tool / Command | Result | Details |
|---|---|---|---|
| **Preflight Unit Tests** | `python3 -m unittest discover tests/preflight` | **PASS (10/10)** | All 10 fixture tests (budget, memory shrink, missing capacity, isolation, etc.) match contract specifications. |
| **Manifest Render & Kubeconform** | `scripts/render-validate.sh local-k3s` | **PASS** | Rendered all 9 layers; schema validated against Kubernetes 1.35 and Traefik CRD catalog. |
| **Manifest Render & Kubeconform (VPS)** | `scripts/render-validate.sh vps-k3s` | **PASS** | Validated VPS environment manifests and patch resolutions. |
| **Render Invariants Check** | `scripts/render-invariants.py` | **PASS** | All 8 invariants verified (no `postBuild`, no `secretRef`, explicit namespaces, prune-disabled annotations, admission policy scope). |
| **Secret Scan Pre-Commit** | `git commit` secret scan hook | **PASS** | Zero unencrypted secrets in git history or staged diffs. |

---

## 2. Live Cluster Acceptance & Success Criteria (SC-001 – SC-011)

| Criterion | Target Requirement | Live Cluster Evidence | Status |
|---|---|---|---|
| **SC-001** | Ordered bootstrap & idempotent reconciler bring-up | Applied `clusters/local-k3s/flux-system` via server-side apply; `source-controller` and `kustomize-controller` rolled out cleanly; re-apply leaves generation unchanged. | **PASS** |
| **SC-002** | Git-driven delivery without manual cluster commands | Commits pushed to `main` (`8f4eff4`, `2ab8b56`) fetched automatically by `source-controller` and applied across layers. | **PASS** |
| **SC-003** | Drift correction within reconciliation cycle | Unauthorized label `tamper: injected-drift` added under break-glass to `project-pn-quota`; Flux reconciled and stripped the drift automatically. | **PASS** |
| **SC-004** | Declarative pruning & volume retention | Flux pruned unmanaged objects; `PersistentVolumeClaim` objects (`openbao-backup-pvc`, `postgres-data-postgres-0`) protected via `kustomize.toolkit.fluxcd.io/prune: disabled`. | **PASS** |
| **SC-005** | Rollback & source fault-tolerance | Tested unreachable repository URL (`https://github.com/nacfson/nonexistent-repo-99999`); Flux retained last applied revision and running workloads without disruption; restored seamlessly upon URL recovery. | **PASS** |
| **SC-006** | Preflight admission budget enforcement | Gating scripts enforce requests == limits, 1/n project allocation, and reject unmeasured environments. | **PASS** |
| **SC-007** | Tenant boundary protection | Tested `system:serviceaccount:proj-pn:pn-reconciler` RBAC permissions: denied `ResourceQuota`, `NetworkPolicy`, `ServiceAccount`, `RoleBinding`, `Namespace`, and cross-namespace deployments; allowed only workloads in `proj-pn`. | **PASS** |
| **SC-008** | Bounded reconciler resource consumption | Verified via `kubectl -n flux-system top pods`: `kustomize-controller` used 66Mi RAM (limit 128Mi), `source-controller` used 21Mi RAM (limit 128Mi); 0 RESTARTS; zero OOMKilled. | **PASS** |
| **SC-009** | Zero unmeasured environments | Verified `platform-preflight.py` enforces mandatory `capacity.yaml` before changes can be promoted. | **PASS** |
| **SC-010** | Status visibility in under 1 minute | Measured `time flux get kustomizations -A`: all 10 layers reported full revision and condition status in **0.028s** (<< 60s requirement). | **PASS** |
| **SC-011** | Manual-change rejection & Break-Glass RBAC | Direct `kubectl` creations/updates/deletions rejected by `ValidatingAdmissionPolicy` with contract message; impersonated `platform:break-glass` accepted; 24h short-lived read-only token generated for `operator-readonly` with restricted `view` permissions. | **PASS** |

---

## 3. Real-World Issues Discovered & Resolved During Live Acceptance

1. **Pod Security Admission Conflict (`IPC_LOCK` in OpenBao)**:
   * *Finding*: OpenBao container specified `capabilities.add: [IPC_LOCK]`. In Kubernetes 1.25+ Pod Security Standards (`restricted:latest`), adding capabilities other than `NET_BIND_SERVICE` is strictly forbidden. Furthermore, OpenBao v2.0 dropped `mlock` support and refused to start with `disable_mlock = false`.
   * *Resolution*: Removed `add: [IPC_LOCK]` from `openbao-statefulset.yaml` and cleaned up `openbao.hcl`. OpenBao pod started, passed `restricted` Pod Security, and reached healthy status (`1/1 Running`).
2. **Missing `VAULT_ADDR` in Automation CLI**:
   * *Finding*: `scripts/vault-init.sh` invoked `bao` commands inside the container without `VAULT_ADDR=http://127.0.0.1:8200`, causing attempts to connect over TLS to an unencrypted internal listener.
   * *Resolution*: Updated `scripts/vault-init.sh` to explicitly specify `VAULT_ADDR="http://127.0.0.1:8200"`.
3. **CronJob PVC Health Check Deadlock**:
   * *Finding*: `openbao-backup-pvc` and `postgres-backup-pvc` with `volumeBindingMode: WaitForFirstConsumer` remain `Pending` until their scheduled CronJob fires. Default Flux `wait: true` treated pending PVCs as stalled, causing 5-minute health check timeouts.
   * *Resolution*: Added explicit `healthChecks` targeting the primary `StatefulSet` objects in `clusters/*/layers.yaml`.
4. **ValidatingAdmissionPolicy CEL Optional Key Evaluation**:
   * *Finding*: Direct access to `request.subResource` in CEL threw `no such key: subResource` when evaluating operations lacking a subresource.
   * *Resolution*: Guarded evaluation with `has(request.subResource)` in `manual-change-policy.yaml`. Clean rejection message now returned across all operations.
