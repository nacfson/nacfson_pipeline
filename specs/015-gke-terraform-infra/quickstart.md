# Quickstart: validating GKE Infrastructure as Code

**Feature**: [spec.md](spec.md) | **Contracts**: [terraform-outputs](contracts/terraform-outputs.md), [shared-values](contracts/shared-values.md), [traefik-l4-edge](contracts/traefik-l4-edge.md)

This guide proves the feature works. The full beginner runbook (FR-020) is delivered by the implementation as `docs/runbooks/gke-terraform.md`. This file lists the scenarios and expected results that runbook must satisfy.

---

## Scenario 1: Offline (no cloud, allowed now: User Story 3, FR-016/017/018)

On any machine with no Google Cloud credentials:

```bash
python3 scripts/check-gcp-shared-values.py
python3 -m unittest discover tests/infra
python3 -m unittest discover tests/preflight
./scripts/render-validate.sh gke
for d in infra/gcp/bootstrap infra/gcp/project infra/gcp/gke; do
  terraform -chdir=$d fmt -check -recursive &&
  terraform -chdir=$d init -backend=false -lockfile=readonly &&
  terraform -chdir=$d validate
done
```

**Expected:**
- every command exits 0
- `render-validate.sh gke` reports the new Invariant 10 (single L4 load balancer) as passing

**Negative check (SC-002):** change `crypto_key` in `infra/gcp/shared-values.json` to `x`. `check-gcp-shared-values.py` must then exit 1 with `CHECK 3 FAIL kms.crypto_key`. Revert the change afterwards.

## Scenario 2: Create (Cloud Shell, only after Spec 012 Gates 1–3: User Story 1)

1. **Preflight.** Run the quota check from the runbook. `CPUS` headroom in `asia-northeast3` must be at least 6.
2. **Bootstrap:** `terraform -chdir=infra/gcp/bootstrap init && terraform -chdir=infra/gcp/bootstrap apply`. The bucket `nacfson-pipeline-tfstate` exists.
3. **Project:** `export TF_VAR_billing_account=…` (not committed), then `init` and `apply` in `infra/gcp/project`. The APIs are on and the $80 budget exists. Remove or raise the control center's $1 budget by hand.
4. **GKE:** `init`, then `plan -out=gke.tfplan`, then `apply gke.tfplan` in `infra/gcp/gke`.
   - **Expected:** the plan shows only additions. Apply finishes in about 10–15 minutes.

**Checkpoint "ready for Flux":**

```bash
$(terraform -chdir=infra/gcp/gke output -raw get_credentials_command)
kubectl get nodes -L cloud.google.com/gke-nodepool,cloud.google.com/gke-spot,cloud.google.com/gke-provisioning
kubectl get nodes -o custom-columns=NAME:.metadata.name,TAINTS:.spec.taints
kubectl get endpoints kubernetes -n default
terraform -chdir=infra/gcp/gke output ingress_ip
```

**Expected:**
- one `ondemand-core` node (standard, no taint)
- one `spot-controllers` node (spot, `gke-spot=true:NoSchedule`)
- an IP printed for `ingress_ip`

If the `kubernetes` endpoint address is public, add it as a `/32` to `openbao-ingress-policy` in `clusters/gke/layers.yaml` (Spec 012 open item) before Scenario 4.

**SC-001:** steps 1–4 plus the checkpoint take under 60 minutes for a first-time operator.

## Scenario 3: Nothing to do (User Story 2, SC-003)

```bash
terraform -chdir=infra/gcp/gke plan -detailed-exitcode   # expected exit code 0 = no changes
```

Then change a label on a node pool by hand in the console and run `plan` again. **Expected:** exit code 2 and the drift listed. Revert the change by applying.

## Scenario 4: Ready for restore (User Story 5, FR-023/024)

After installing Flux from `clusters/gke/flux-system/` with `kubectl apply --server-side -k` (runbook Part 3.1; no GitHub token, no `flux bootstrap`):

```bash
flux get kustomizations
kubectl -n vault exec openbao-0 -- env VAULT_ADDR=http://127.0.0.1:8200 bao status -format=json | grep -E '"initialized"|"type"'
kubectl get pods -A -o wide | grep -E 'openbao-0|postgres-0|keycloak'
kubectl get svc -n platform traefik
```

**Expected:**
- all Flux layers Ready except vault and the layers that depend on it (database, vault-proxies, identity, ingress, tls) wait until the restore: OpenBao is empty, so its readiness check fails (HTTP 501) until the restore fills and unseals it
- OpenBao running with seal type `gcpckms` and `initialized: false`; it must not be crash-looping on a missing key
- the database and identity layers are not applied yet: they depend on vault, so `postgres-0` and
  `keycloak` start only after the restore unseals OpenBao
- the `traefik` `EXTERNAL-IP` equals `ingress_ip`
- `curl -sk --resolve auth.nacfson.cloud:443:<ingress_ip> https://auth.nacfson.cloud/ -o /dev/null -w '%{http_code}'` returns an HTTP status (any code), which proves load balancer → Traefik works before DNS moves (Traefik serves its default certificate until the restore brings up the tls layer)

**SC-008:** reached within 30 minutes of installing Flux, with no manual cloud changes. The control-center restore (Spec 012) may start from here.

## Scenario 5: Node replacement keeps the address (FR-007)

Rehearsal only, before cutover:

```bash
gcloud container clusters upgrade nacfson-gke --zone asia-northeast3-a --node-pool ondemand-core --cluster-version <current-master-version>   # re-creates the pool's nodes at the same version
```

**Expected:** during the operation, the old node drains and the new node becomes healthy. Afterwards, `kubectl get svc -n platform traefik` still shows the same `EXTERNAL-IP`, and the `curl --resolve` check from Scenario 4 succeeds again. The spot node never appears as a healthy load balancer backend.

## Scenario 6: Removal (User Story 4, SC-007)

Only before the DNS switch:

1. Set `deletion_protection = false` on the cluster in a reviewed change, and remove its `prevent_destroy`.
2. `kubectl delete svc -n platform traefik`, or suspend Flux and delete it, so GKE removes the load balancer parts.
3. `terraform -chdir=infra/gcp/gke plan -destroy -out=remove.tfplan -target=…` for exactly the addresses listed in `docs/runbooks/gke-terraform.md` Part 4, then `apply remove.tfplan`. Do not run a full `destroy`: Terraform refuses the whole plan when it includes a `prevent_destroy` resource.
4. Delete leftover `pvc-…` disks (GKE does not delete them with the cluster).

**Expected:**
- `gcloud compute forwarding-rules list`, `gcloud compute addresses list` and `gcloud container clusters list` show nothing for this feature
- the key ring and key, the registry and the state bucket remain
- vps-k3s still serves all hostnames

## Scenario 7: No secrets in Git (FR-019, SC-005)

`git status` after Scenarios 2–6 shows no `*.tfstate*`, `*.tfplan`, `.terraform/` or `terraform.tfvars` files. The Gitleaks job passes on the pull request.
