# Platform Operations Runbook

**Feature**: `004-flux-gitops-reconciliation`, `012-gke-platform-migration`  
**System**: Personal Project Platform GitOps Operations  
**Target Environments**: `local-k3s`, `gke` (primary cloud target in `nacfson-pipeline` / `asia-northeast3-a`), `vps-k3s` (transitional until Gate 4 cutover)  
**Governing Contracts**: `specs/004-flux-gitops-reconciliation/contracts/operations.md`, `contracts/manual-change-policy.md`, `contracts/reconciliation-layers.md`, `specs/012-gke-platform-migration/contracts/`

---

## 1. Overview & Operational Principles

The Personal Project Platform is managed strictly through continuous GitOps reconciliation using in-cluster Flux 2.9.5 controllers. Manual manifest deployment has been completely retired.

All cluster interactions follow two distinct tiers:

| Access | Location | Can Write? | Authorization |
| :--- | :--- | :--- | :--- |
| **Read-only** | Workstation. On this machine use [`scripts/open-observe.sh`](../scripts/open-observe.sh) (`127.0.0.1:16443`). Do not bind `6443`; local k3s owns it. A machine that does not run local k3s may use `ssh -L 6443:127.0.0.1:6443 <vm>`. | No | `ServiceAccount governance/operator-readonly` via short-lived (≤24h) token; ClusterRole `view` |
| **Break-glass** | On K3s node over SSH, or GKE cluster-admin break-glass impersonation | Yes | Deliberate impersonation of group `platform:break-glass` via admin credentials |

### Key Invariants
1. **Git as Single Source of Truth**: The `main` branch defines desired state. Changes are delivered via Pull Requests passing automated CI gates.
2. **Cluster Rejection of Manual Changes**: Kubernetes `ValidatingAdmissionPolicy` intercepts and rejects manual mutations (`CREATE`, `UPDATE`, `DELETE`) across all managed namespaces (`default`, `flux-system`, `governance`, `identity`, `platform`, `vault`, `observability`, `proj-*`, `cert-manager`) and critical cluster kinds, while allowlisting K3s and GKE control-plane principals.
3. **Residual Risk - `kube-system` (R16)**: To prevent disrupting core control plane components (CoreDNS, metrics-server, GKE/K3s node agents), `kube-system` is not gated by the manual-change policy. A root user on the host node could run workloads directly in `kube-system`. This residual risk is bounded by restricting SSH/root node access and eliminating cluster-admin credentials from workstations.
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
   - `render-validate (gke)`
   - `render-validate (vps-k3s)` *(until Gate 4 retirement)*
   - `platform-preflight (local-k3s)`
   - `platform-preflight (gke)`
   - `lint` (yamllint, shellcheck, Go tests, Ansible syntax)
   - `Scan for Leaked Secrets (Gitleaks)`
5. **Exit Criterion**:
   ```bash
   git ls-remote https://github.com/nacfson/nacfson_pipeline main
   ```
   Must succeed without any credentials.

---

## 3. Capacity Measurement (Whenever Host Node Changes)

Whenever a node or GKE node pool is provisioned, resized, or upgraded:

1. Retrieve node allocatable resources:
   ```bash
   kubectl get node <node-name> -o jsonpath='{.status.allocatable}'
   ```
   Convert CPU to millicores (`cpuMillicores`) and memory to MiB (`memoryMib`).
2. Sum live resource requests of `kube-system` pods per node pool to establish `systemReserved`.
3. Commit `clusters/<env>/capacity.yaml` via Pull Request (multi-node environments declare `nodeCount` and per-pool `pools` entries whose sums match top-level `allocatable` and `systemReserved`):
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
   **Rule**: Values MUST come from observing the node(s), never from desired figures.

---

## 4. Host Bootstrap & Reconciler Installation

For single-node K3s (`local-k3s` / `vps-k3s`), execute Ansible bootstrap targeting the host VM:

```bash
ansible-playbook -i '<vm>,' -u <ssh-user> bootstrap/bootstrap.yml \
  -e k3s_version=v1.35.9+k3s1 -e gitops_environment=<env>
```

**Workflow**:
- Installs and configures pinned K3s `v1.35.9+k3s1`.
- Validates system parameters and verifies core services.
- Stage 4 (`reconciler`): Deploys pinned Flux 2.9.5 controllers (`source-controller`, `kustomize-controller`) from `clusters/<env>/flux-system/`.
- Flux pulls `main` and begins sequential layer reconciliation according to `layers.yaml`.

For GKE (`clusters/gke/`), see **Section 16.3 (Gate 4 Single-Shot GKE Cutover)**.

**Exit Criterion**:
`bootstrap_result.status: ready` with `reconciler_ready: true`.

---

## 5. Vault Initialization & Unseal

- **On `local-k3s` (and legacy `vps-k3s`)**: OpenBao uses Shamir unseal. After initial cluster bootstrap or any `openbao-0` container restart, run `./scripts/vault-init.sh` (uses `kubectl exec`, a `CONNECT` request permitted by `ValidatingAdmissionPolicy`).
- **On `gke` (post-Gate 4)**: `openbao-0` runs under `ServiceAccount/openbao` (`iam.gke.io/gcp-service-account: openbao-kms@nacfson-pipeline.iam.gserviceaccount.com`) with `seal "gcpckms"` (`project=nacfson-pipeline`, `region=asia-northeast3`, `key_ring=openbao`, `crypto_key=unseal`) per Constitution Amendment `2.3.0`. After the one-time Gate 4 `bao operator unseal -migrate` ceremony (Section 16.3), `openbao-0` auto-unseals unattended across GKE node maintenance and pod restarts, while Shamir recovery keys remain stored offline in the operator's password manager.
- Downstream platform layers (`database`, `identity`, `ingress`, `vault-proxies`, `proj-*`) report `DependencyNotReady` while Vault is sealed and converge automatically once unsealed.

---

## 6. Read-Only Workstation Inspection

Operators inspect cluster state from their local workstation using read-only credentials.

On this workstation, do not bind `6443`. Local k3s already owns it. One script opens the VPS API on `127.0.0.1:16443` and both private UIs:

```bash
./scripts/open-observe.sh
```

A machine that does not run local k3s opens the API first:

```bash
ssh -L 6443:127.0.0.1:6443 <vm>
```

Then, on either machine:

1. Generate 24-hour read-only kubeconfig (Section 13). `open-observe.sh` already does this for the VPS.
   ```bash
   ./scripts/operator-kubeconfig.sh <vm>
   export KUBECONFIG=~/.kube/nacfson-<env>.yaml
   ```
2. Inspect cluster status and GitOps reconciliation:
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

Desktop Headlamp loads the kubeconfig written by `./scripts/operator-kubeconfig.sh` and shows live container logs. `kubectl logs --previous` shows the previous container instance after a restart. Historical `proj-*` search is section 15, not Headlamp.

---

## 7. Standard Deployment Workflow

All infrastructure and application changes are made declaratively:

1. Create a short-lived branch from `main`.
2. Commit manifest modifications or patches.
3. Open a Pull Request targeting `main`.
4. Ensure all required GitHub Actions checks pass:
   - `render-validate`: validates schemas, layers, portable `NetworkPolicy`, and zero-`LoadBalancer` invariants.
   - `platform-preflight`: validates QoS (`requests == limits`), memory freeze, multi-node pool placement, and $1/n$ capacity budgeting.
5. Review and merge the Pull Request.
6. Flux detects the merge within 1 minute and reconciles changes without human intervention.
7. **Keycloak realm JSON** (`deploy/platform/identity/keycloak-realm-config.yaml`) is imported only on a fresh database. `kc.sh start --import-realm` skips a realm that already exists (`Realm 'platform' already exists. Import skipped`), and the `-Dkeycloak.migration.strategy=OVERWRITE_EXISTING` flag on the Deployment has no effect on that import. Bumping `platform.nacfson.io/realm-scope-sync` restarts Keycloak but does not re-import the realm. A realm setting changed in Git must also be applied to the live realm (see **Applying realm session lifetimes** below). First-time Google SSO creates the Keycloak user via `google jit first broker login` (AUTH-01); see [`contracts/CONTRACT_AUTH_DOOR.md`](../contracts/CONTRACT_AUTH_DOOR.md) §4. Do not create users in the Keycloak admin console as the source of truth.

### Applying realm session lifetimes

The contracted values live in [`contracts/CONTRACT_AUTH_DOOR.md`](../contracts/CONTRACT_AUTH_DOOR.md) §5. After the realm JSON change is merged, an operator applies them to the live realm:

1. Read the current values (read-only):
   ```bash
   kubectl exec -n identity postgres-0 -- psql -U postgres -d keycloak -c "
   select access_token_lifespan, sso_idle_timeout, sso_max_lifespan, revoke_refresh_token
   from realm where name='platform';
   select 'realm' as level, name, value from realm_attribute
    where realm_id=(select id from realm where name='platform') and name like 'clientSession%'
   union all
   select c.client_id, a.name, a.value from client_attributes a join client c on c.id=a.client_id
    where c.realm_id=(select id from realm where name='platform') and a.name like 'client.session%';"
   ```
2. Write the contracted values:
   ```bash
   kubectl exec -n identity postgres-0 -- psql -U postgres -d keycloak -v ON_ERROR_STOP=1 -c "
   update realm set access_token_lifespan=300, sso_idle_timeout=1800, sso_max_lifespan=36000, revoke_refresh_token=false
   where name='platform';"
   ```
   If step 1 listed a non-zero client-level `clientSession*` or `client.session.*` value, remove it too. The contract forbids those overrides.
3. Restart Keycloak so it reloads the realm from the database: `kubectl --as=break-glass:<operator-id> --as-group=platform:break-glass rollout restart deployment/keycloak -n identity`. With Spec 012's `--features=persistent-user-sessions` flag enabled on `Deployment/keycloak`, active user and client sessions are persisted in `postgres-0` (`keycloak` DB) and survive Keycloak pod restarts and GKE weekly node upgrades without forcing interactive re-login.
4. Rerun step 1 and confirm `300 | 1800 | 36000 | f` and no client-level rows.

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
Execute commands with explicit `platform:break-glass` impersonation:
```bash
kubectl --as=break-glass:<operator-id> --as-group=platform:break-glass <command>
```
Without `--as-group=platform:break-glass`, requests are rejected by the admission policy.

### Hotfix Sequence
1. **Suspend Reconciliation**:
   ```bash
   kubectl --as=break-glass:<operator-id> --as-group=platform:break-glass \
     patch kustomization <layer> -n flux-system --type=merge -p '{"spec":{"suspend":true}}'
   ```
2. **Apply Emergency Fix**:
   Execute necessary changes under the break-glass identity.
3. **Reconcile Back to Git**:
   Immediately commit the fix to Git through a standard Pull Request and merge to `main`.
4. **Resume Reconciliation**:
   ```bash
   kubectl --as=break-glass:<operator-id> --as-group=platform:break-glass \
     patch kustomization <layer> -n flux-system --type=merge -p '{"spec":{"suspend":false}}'
   ```

---

## 10. Decommissioning Protected Resources

Namespaces and PersistentVolumeClaims marked with annotation `kustomize.toolkit.fluxcd.io/prune: disabled` are protected against automatic deletion by Flux:

1. Remove the resource declaration from Git via Pull Request and merge.
2. Once the layer reconciles without the declaration, delete the object under break-glass:
   ```bash
   kubectl --as=break-glass:<operator-id> --as-group=platform:break-glass \
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
   *(On `clusters/gke/`, preserve the `spot-controllers` `nodeSelector`, toleration, and `64Mi`/`128Mi` resource patches on `source-controller` and `kustomize-controller`).*
3. Update `FLUX_VERSION` in `.github/workflows/gitops.yaml`.
4. Open a Pull Request, review diffs, and merge to `main`.
5. Flux reconciles and upgrades its own controllers in-cluster.

---

## 12. Policy Emergency Recovery (Last Resort)

If a misconfigured `ValidatingAdmissionPolicy` locks out even break-glass operations on K3s:

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

All legacy manual deployment, rollback, and budget scripts, baseline state files, `vault-registry-proxy`, and manual `kubectl apply -k deploy/...` or `kubectl apply -f deploy/...` procedures from prior versions have been permanently retired and deleted.

Any manual deployment outside the authorized break-glass workflow is strictly blocked by the admission controller.

---

## 15. Project Log History (`local-k3s` VictoriaLogs & `gke` Kafka Sink)

Stored history covers `proj-*` stdout and stderr only; platform namespaces remain live-only (`kubectl logs` or Headlamp).

### Collector Recovery State (Spec 012 Solution B)

Across both `local-k3s` and `gke`, `DaemonSet/fluent-bit` replaces the retired single-node `fluent-bit-state` RWO PVC with per-node `hostPath` `/var/lib/fluent-bit-state` (`type: DirectoryOrCreate`, mounted `readOnly: false` at `/state` per Constitution Amendment `2.3.0`):
- `/state/tail.db` (SQLite WAL) checkpoints byte offsets per `/var/log/pods` file on the same node disk.
- `/state/chunks` persists in-flight multiline, `rewrite_tag`, and output chunks to disk, strictly capped at `256 MiB` per node (`256M` single HTTP sink on `local-k3s`; `192M` `logs.proj.valid` + `64M` `logs.proj.quarantine` on `gke` `[OUTPUT] kafka` to OCI WireGuard brokers `10.200.0.11..13:9092`).
- On `gke`, in-cluster `StatefulSet/victorialogs` is deleted (VictoriaLogs moves to OCI VM 4 in Spec 014) and `Deployment/error-detector` is paused at `replicas: 0` (retaining its pre-reserved `25m/64Mi` capacity slot until Spec 014 switches it to consume `logs.proj.errors`).

### Open the Local / Legacy VPS VictoriaLogs UI

1. On `local-k3s` (when its API is on `127.0.0.1:6443`): `./scripts/open-hubble-ui.sh` and `./scripts/open-victorialogs-ui.sh`.
2. Example LogsQL (Contract v3):
   - `{k8s.namespace.name="proj-pn"}`
   - `{k8s.namespace.name="proj-pn"} level:=error`
   - `trace_id:=4bf92f3577b34da6a3ce929d0e0e4736`
   - `level:=error | stats by (service.version, exception.type) count()`
   - `_msg:"[REDACTED:"`
3. Quarantine Tenant (`AccountID 0, ProjectID 1`):
   ```bash
   curl -fsS -H 'ProjectID: 1' -X POST \
     --data-urlencode 'query=*' \
     --data-urlencode 'limit=50' \
     http://127.0.0.1:9428/select/logsql/query
   ```

---

## 16. GKE Platform Cutover & Recovery Drills (Spec 012 Gates 2–4)

Cloud provisioning and production cutover follow strict sequential gates after Gate 1 offline verification passes (`specs/012-gke-platform-migration/quickstart.md`).

### 16.1 Gate 2 — Local Fluent Bit Outage & Crash Recovery Drill (`local-k3s`)

Verify `CollectorRecoveryState` (`/var/lib/fluent-bit-state`) before touching production state:

1. Reconcile `clusters/local-k3s` and verify `DaemonSet/fluent-bit` mounts `/var/lib/fluent-bit-state` at `/state` with zero `PVC/fluent-bit-state`.
2. Suspend `observability` reconciliation and scale `StatefulSet/victorialogs` to `0` under break-glass:
   ```bash
   kubectl --as=break-glass:drill --as-group=platform:break-glass \
     patch kustomization observability -n flux-system --type=merge -p '{"spec":{"suspend":true}}'
   kubectl --as=break-glass:drill --as-group=platform:break-glass \
     scale statefulset/victorialogs -n observability --replicas=0
   ```
3. Emit `200` numbered JSON log lines from a `proj-*` pod (`seq 1 200`), delete the `fluent-bit` pod mid-outage so it restarts against `/state/tail.db` and `/state/chunks`, then scale `StatefulSet/victorialogs` back to `1` and resume `observability`.
4. Query VictoriaLogs (`http://127.0.0.1:9428/select/logsql/query`) and confirm all `200/200` sequence numbers arrive with **zero missing and zero duplicate records** (`SC-002`).

### 16.2 Gate 3 — Full-State Bundle Extraction & Local Restore Rehearsal (`local-k3s`)

Rehearse the complete production backup and identity-preserving restore locally before provisioning GKE:

1. **Extract read-only production bundle from `vps-k3s`** into a `0700` directory outside Git:
   ```bash
   export BUNDLE_DIR="$HOME/.nacfson-cutover/$(date -u +%Y%m%dT%H%M%SZ)"
   ./scripts/cutover-bundle.sh export --bundle-dir "$BUNDLE_DIR" --ssh oracleCloud
   ```
   Produces 6 verified artifacts (`openbao.snap`, `keycloak.dump.gz`, `proj_pn.dump.gz`, `proj_health.dump.gz`, `edge-tls-secrets.yaml`, `victoria-logs-data.tar.gz`) plus `manifest.sha256`.
2. **Verify offline checksums and archive headers**:
   ```bash
   ./scripts/cutover-bundle.sh verify --bundle-dir "$BUNDLE_DIR"
   ```
3. **Restore onto `local-k3s` and verify identity continuity**:
   ```bash
   ./scripts/cutover-bundle.sh restore --bundle-dir "$BUNDLE_DIR" --target local-k3s --yes
   ./scripts/smoke-auth-chain.sh --env local-k3s
   ```
   Confirm `openbao-0` unseals, `vault-configure-k8s-auth.sh` + `vault-reconcile.sh --confirm` re-bind the local cluster's `kube-root-ca.crt` and token reviewer JWT, `keycloak` + `proj_pn` + `proj_health` databases restore cleanly, and existing user `(issuer, subject)` rows remain intact (`SC-003`).

### 16.3 Gate 4 — Single-Shot GKE Cutover & `vps-k3s` Repurposing (`nacfson-pipeline` / `asia-northeast3-a`)

Execute only after Gate 2 and Gate 3 pass:

1. **Provision GCP prerequisites (`nacfson-pipeline`, `asia-northeast3-a`)**:
   - Create Artifact Registry `nacfson-public` in `asia-northeast3`, apply `deploy/gcp/gar-cleanup-policy.json`, and run `deploy/gcp/cloudbuild.yaml` to publish pinned `@sha256` multi-arch images.
   - Create Cloud KMS key `projects/nacfson-pipeline/locations/asia-northeast3/keyRings/nacfson-vault/cryptoKeys/openbao-unseal` and IAM service account `openbao-kms@nacfson-pipeline.iam.gserviceaccount.com` with:
     - `roles/cloudkms.cryptoKeyEncrypterDecrypter` on the `openbao-unseal` key, and
     - `roles/iam.workloadIdentityUser` bound to `serviceAccount:nacfson-pipeline.svc.id.goog[vault/openbao]`.
   - Provision Zonal GKE Standard cluster (`--zone asia-northeast3-a`, `--workload-pool=nacfson-pipeline.svc.id.goog`, `--enable-network-policy`, `--logging=NONE`, `--monitoring=NONE`) with Design 1 pools:
     - `ondemand-core`: `1 × e2-medium` (`pd-balanced` `20 GB`, standard provisioning).
     - `spot-controllers`: `1 × e2-small` (`pd-standard` `20 GB`, `--spot`, taint `cloud.google.com/gke-spot=true:NoSchedule`).
   - Assign a reserved static external IPv4 address to `ondemand-core` Node 1 and open a VPC firewall rule permitting inbound TCP `80` and `443` to `ondemand-core` (`Traefik` `hostPort: 80/443`, zero GCP LoadBalancer).
2. **Measure live GKE node pools & bootstrap Flux**:
   - Verify `kubectl get nodes -o jsonpath` allocatable values match `clusters/gke/capacity.yaml` (`1880m / 4408Mi`).
   - Apply `clusters/gke/flux-system/` so Flux reconciles `clusters/gke/layers.yaml`.
3. **Export fresh final cutover bundle & restore onto GKE with KMS migration**:
   ```bash
   export BUNDLE_DIR="$HOME/.nacfson-cutover/gate4-$(date -u +%Y%m%dT%H%M%SZ)"
   ./scripts/cutover-bundle.sh export --bundle-dir "$BUNDLE_DIR" --ssh oracleCloud
   ./scripts/cutover-bundle.sh restore --bundle-dir "$BUNDLE_DIR" --target gke --migrate-kms --yes
   ```
   `restore --target gke --migrate-kms` automatically:
   - Verifies `manifest.sha256` across all 6 artifacts.
   - Pre-seeds `edge-tls-secrets.yaml` (`auth-tls`, `pn-tls`, `health-tls`, `blog-tls`, and Let's Encrypt ACME account key) so GKE serves valid TLS immediately without Let's Encrypt rate-limit risk.
   - Suspends `Kustomization/vault`, boots `openbao-0` with Shamir config, restores `openbao.snap`, unseals, and runs `scripts/vault-configure-k8s-auth.sh` + `scripts/vault-reconcile.sh --confirm` to replace K3s's CA/JWT with GKE's `ConfigMap/kube-root-ca.crt`.
   - Restores `keycloak.dump.gz`, `proj_pn.dump.gz`, and `proj_health.dump.gz` into `postgres-0` and restarts `Deployment/keycloak`.
   - Resumes `Kustomization/vault` to reconcile the `seal "gcpckms"` config overlay and runs `bao operator unseal -migrate` so subsequent GKE node upgrades auto-unseal `openbao-0` via Workload Identity.
4. **Pre-DNS `--resolve` verification & Cloudflare/DNS cutover**:
   - Run `./scripts/smoke-auth-chain.sh` using `curl --resolve <host>:443:<NODE1_STATIC_IP>` across `auth.nacfson.cloud`, `pn.nacfson.cloud`, `health.nacfson.cloud`, and `blog.nacfson.cloud`; verify zero TLS warnings and intact OIDC sessions (`SC-004`).
   - Update DNS `A` records to `<NODE1_STATIC_IP>` and re-run `./scripts/smoke-auth-chain.sh` against live public DNS.
5. **Immediate Same-Window `vps-k3s` Repurposing (`FR-011`)**:
   - Once public DNS serves traffic cleanly on GKE, archive `victoria-logs-data.tar.gz` for Spec 014 import into OCI VM 4, shrink/recreate the `4 OCPU / 24 GiB` `vps-k3s` ARM64 instance into `1 OCPU / 6 GiB` OCI Kafka Broker VM 1 (`10.200.0.11`) for Spec 013, and remove `clusters/vps-k3s/` via PR.

