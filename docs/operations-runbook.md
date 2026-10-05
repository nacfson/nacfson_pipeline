# Platform Operations Runbook

**Feature**: `004-flux-gitops-reconciliation`  
**System**: Personal Project Platform GitOps Operations  
**Target Environments**: `local-k3s`, `vps-k3s`  
**Governing Contracts**: `specs/004-flux-gitops-reconciliation/contracts/operations.md`, `contracts/manual-change-policy.md`, `contracts/reconciliation-layers.md`

---

## 1. Overview & Operational Principles

The Personal Project Platform is managed strictly through continuous GitOps reconciliation using in-cluster Flux 2.9.5 controllers. Manual manifest deployment has been completely retired.

All cluster interactions follow two distinct tiers:

| Access | Location | Can Write? | Authorization |
| :--- | :--- | :--- | :--- |
| **Read-only** | Workstation through SSH tunnel (`ssh -L 6443:127.0.0.1:6443 <vm>`) | No | `ServiceAccount governance/operator-readonly` via short-lived (≤24h) token; ClusterRole `view` |
| **Break-glass** | On the node only, over SSH | Yes | Deliberate impersonation of group `platform:break-glass` via node admin credentials |

### Key Invariants
1. **Git as Single Source of Truth**: The `main` branch defines desired state. Changes are delivered via Pull Requests passing automated CI gates.
2. **Cluster Rejection of Manual Changes**: Kubernetes `ValidatingAdmissionPolicy` intercepts and rejects manual mutations (`CREATE`, `UPDATE`, `DELETE`) across all managed namespaces (`default`, `flux-system`, `governance`, `identity`, `platform`, `vault`, `proj-pn`) and critical cluster kinds.
3. **Residual Risk - `kube-system` (R16)**: To prevent disrupting core K3s control plane components (CoreDNS, metrics-server, Traefik), `kube-system` is not gated by the manual-change policy. A root user on the host node could run workloads directly in `kube-system`. This residual risk is bounded by restricting SSH/root node access and eliminating cluster-admin credentials from workstations.
4. **Verification Scripts Under Break-Glass**: Verification scripts that execute active probe pod creation (such as `scripts/verify-isolation.sh`) must run in break-glass mode (`BREAK_GLASS=1`) to bypass the admission policy and exercise Pod Security Admission directly.

---

## 2. Publication Precondition (One-Time Setup)

The repository must be public before clusters can pull without stored credentials:

1. **Credential Rotation**: Rotate any credentials that appeared in historical commits (commit `be85073`):
   - Google OAuth client secret (Google Cloud Console).
   - Keycloak bootstrap admin credentials.
2. **Secret Scan Verification**:
   ```bash
   gitleaks git . --log-opts="--all"
   ```
   Confirm zero unresolved findings.
3. **Repository Visibility**: Change repository visibility to Public on GitHub.
4. **Branch Protection on `main`**:
   Require pull requests before merging, require status checks to pass before merging:
   - `render-validate (local-k3s)`
   - `render-validate (vps-k3s)`
   - `platform-preflight (local-k3s)`
   - `lint` (yamllint, shellcheck, Go tests, Ansible syntax)
   - `Scan for Leaked Secrets (Gitleaks)`
   *(Note: `platform-preflight (vps-k3s)` is added after the VPS capacity measurement is committed).*
5. **Exit Criterion**:
   ```bash
   git ls-remote https://github.com/nacfson/nacfson_pipeline main
   ```
   Must succeed without any credentials.

---

## 3. Capacity Measurement (Whenever Host Node Changes)

Whenever a node is provisioned, resized, or upgraded:

1. Retrieve node allocatable resources:
   ```bash
   kubectl get node <node-name> -o jsonpath='{.status.allocatable}'
   ```
   Convert CPU to millicores (`cpuMillicores`) and memory to MiB (`memoryMib`).
2. Sum live resource requests of bundled K3s pods in `kube-system` to establish `systemReserved`.
3. Commit `clusters/<env>/capacity.yaml` via Pull Request:
   ```yaml
   environment: <env>
   nodeName: <node-name>
   observedAt: "<RFC3339-timestamp>"
   allocatable:
     cpuMillicores: <observed-cpu>
     memoryMib: <observed-mem>
   systemReserved:
     cpuMillicores: <system-cpu>
     memoryMib: <system-mem>
   ```
   **Rule**: Values MUST come from observing the node, never from desired figures.

---

## 4. Host Bootstrap & Reconciler Installation

Execute Ansible bootstrap targeting the host VM:

```bash
ansible-playbook -i '<vm>,' -u <ssh-user> bootstrap/bootstrap.yml \
  -e k3s_version=v1.35.9+k3s1 -e gitops_environment=<env>
```

**Workflow**:
- Installs and configures pinned K3s `v1.35.9+k3s1`.
- Validates system parameters and verifies core services.
- Stage 4 (`reconciler`): Deploys pinned Flux 2.9.5 controllers (`source-controller`, `kustomize-controller`) from `clusters/<env>/flux-system/`.
- Flux pulls `main` and begins sequential layer reconciliation according to `layers.yaml`.

**Exit Criterion**:
`bootstrap_result.status: ready` with `reconciler_ready: true`.

---

## 5. Vault Initialization & Unseal

After initial cluster bootstrap or any OpenBao container restart:

1. Open an SSH session to the node or tunnel to the cluster.
2. Run the initialization script:
   ```bash
   ./scripts/vault-init.sh
   ```
   *(Uses `kubectl exec`, which is a CONNECT request not restricted by ValidatingAdmissionPolicy).*
3. Downstream platform layers (`database`, `identity`, `ingress`, `vault-proxies`, `proj-pn`) will report `DependencyNotReady` while Vault is sealed, and will converge automatically once unsealed.

---

## 6. Read-Only Workstation Inspection

Operators inspect cluster state from their local workstation using read-only credentials:

1. Open SSH port forward:
   ```bash
   ssh -L 6443:127.0.0.1:6443 <vm>
   ```
2. Generate 24-hour read-only kubeconfig (Section 13):
   ```bash
   ./scripts/operator-kubeconfig.sh <vm>
   export KUBECONFIG=~/.kube/nacfson-<env>.yaml
   ```
3. Inspect cluster status and GitOps reconciliation:
   ```bash
   # Inspect cluster nodes and metrics (granted via operator-diagnostics)
   kubectl get nodes
   kubectl top nodes
   kubectl top pods -A

   # Open temporary port-forward tunnel to internal services for diagnosis (e.g. Vault)
   kubectl port-forward -n vault openbao-0 8200:8200 &

   # Check all reconciliation layers
   flux get kustomizations -A

   # Inspect Git repository sync status
   flux get sources git -A

   # Tree representation of reconciled resources
   flux tree kustomization <layer-name>

   # Inspect events and reconciliation failures
   flux events --for Kustomization/<layer-name>
   ```

---

## 7. Standard Deployment Workflow

All infrastructure and application changes are made declaratively:

1. Create a short-lived branch from `main`.
2. Commit manifest modifications or patches.
3. Open a Pull Request targeting `main`.
4. Ensure all required GitHub Actions checks pass:
   - `render-validate`: validates schemas, layers, and security invariants.
   - `platform-preflight`: validates QoS (`requests == limits`), memory freeze, and 1/n capacity budgeting.
5. Review and merge the Pull Request.
6. Flux detects the merge within 1 minute and reconciles changes without human intervention.

---

## 8. Rollback Procedure

To roll back a faulty deployment:

1. Revert the commit in Git:
   ```bash
   git revert <commit-sha>
   ```
2. Open a PR, verify CI passes, and merge to `main`.
3. Flux detects the reverted commit and restores previous cluster state automatically.

---

## 9. Emergency Break-Glass Procedure

When manual cluster interventions are required to resolve an active outage:

### Break-Glass Identity
Execute commands directly on the host node over SSH:
```bash
sudo k3s kubectl --as=break-glass:<operator-id> --as-group=platform:break-glass <command>
```
Without `--as-group=platform:break-glass`, requests are rejected by the admission policy.

### Hotfix Sequence
1. **Suspend Reconciliation**:
   ```bash
   sudo k3s kubectl --as=break-glass:<operator-id> --as-group=platform:break-glass \
     patch kustomization <layer> -n flux-system --type=merge -p '{"spec":{"suspend":true}}'
   ```
2. **Apply Emergency Fix**:
   Execute necessary changes under the break-glass identity.
3. **Reconcile Back to Git**:
   Immediately commit the fix to Git through a standard Pull Request and merge to `main`.
4. **Resume Reconciliation**:
   ```bash
   sudo k3s kubectl --as=break-glass:<operator-id> --as-group=platform:break-glass \
     patch kustomization <layer> -n flux-system --type=merge -p '{"spec":{"suspend":false}}'
   ```

---

## 10. Decommissioning Protected Resources

Namespaces and PersistentVolumeClaims marked with annotation `kustomize.toolkit.fluxcd.io/prune: disabled` are protected against automatic deletion by Flux:

1. Remove the resource declaration from Git via Pull Request and merge.
2. Once the layer reconciles without the declaration, delete the object under break-glass:
   ```bash
   sudo k3s kubectl --as=break-glass:<operator-id> --as-group=platform:break-glass \
     delete pvc <pvc-name> -n <namespace>
   ```

---

## 11. Upgrading Flux GitOps Controller

To upgrade Flux components:

1. Update the pinned Flux CLI locally.
2. Regenerate the component manifest:
   ```bash
   flux install --version=<new-version> --components=source-controller,kustomize-controller --export > clusters/<env>/flux-system/gotk-components.yaml
   ```
3. Update `FLUX_VERSION` in `.github/workflows/gitops.yaml`.
4. Open a Pull Request, review diffs, and merge to `main`.
5. Flux reconciles and upgrades its own controllers in-cluster.

---

## 12. Policy Emergency Recovery (Last Resort)

If a misconfigured `ValidatingAdmissionPolicy` locks out even break-glass operations:

1. Log into the node as root.
2. Temporarily restart K3s with admission validation disabled:
   Add `--kube-apiserver-arg=disable-admission-plugins=ValidatingAdmissionPolicy` to `/etc/rancher/k3s/config.yaml` and restart `k3s`:
   ```bash
   sudo systemctl restart k3s
   ```
3. Delete the problematic `ValidatingAdmissionPolicyBinding`.
4. Fix the policy in Git and merge to `main`.
5. Remove the temporary disable flag from `/etc/rancher/k3s/config.yaml` and restart `k3s`.
6. Flux will reapply the corrected policy from Git.

---

## 13. Read-Only Workstation Kubeconfig Setup

1. Remove any previous cluster-admin kubeconfigs from operator workstations:
   ```bash
   rm -f ~/.kube/nacfson-*.yaml
   ```
2. Run the helper script to issue a 24-hour token:
   ```bash
   ./scripts/operator-kubeconfig.sh <vm> [<env>]
   ```
3. Test read-only permissions:
   ```bash
   KUBECONFIG=~/.kube/nacfson-<env>.yaml kubectl auth can-i create deployments -n identity
   # Output MUST be: no

   KUBECONFIG=~/.kube/nacfson-<env>.yaml flux get kustomizations -A
   # Successfully displays status of all layers
   ```

---

## 14. Retired Procedures (Forbidden)

All legacy manual deployment, rollback, and budget scripts, baseline state files, and manual `kubectl apply -k deploy/...` or `kubectl apply -f deploy/...` procedures from prior versions have been permanently retired and deleted.

Any manual deployment outside the authorized break-glass workflow is strictly blocked by the admission controller.
