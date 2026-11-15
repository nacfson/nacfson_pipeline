# Runbook: GKE infrastructure with Terraform (Spec 015)

This runbook creates, changes, hands off and removes the Google Cloud resources that the
`clusters/gke/` overlay runs on. It is written for someone who has never used Terraform.

- **Spec**: [`specs/015-gke-terraform-infra/`](../../specs/015-gke-terraform-infra/spec.md)
- **Code**: [`infra/gcp/`](../../infra/gcp/) (three folders, applied in order: `bootstrap` → `project` → `gke`)
- **Where to run it**: Google Cloud Shell, signed in as the operator account. Nothing here runs from CI.
- **Short version**: [`gke-deploy.md`](gke-deploy.md) runs the same steps from your own PC, compactly.

> **Do not start Part 1 until Spec 012 Gates 1–3 have passed.** Until then, no cloud resource
> may be created (FR-016). Everything in `infra/gcp/` can be checked offline without credentials;
> see "Offline check" at the end.

## Glossary (plain language)

| Word | Meaning |
|---|---|
| **plan** | Terraform compares the code with what exists in Google Cloud and prints what it *would* add, change or delete. Nothing changes. `plan -out=<file>` saves that exact list. |
| **apply** | Terraform carries out a plan. `apply <saved file>` does exactly what you read, nothing more. |
| **state** | Terraform's notebook of what it created. It lives in the bucket `nacfson-pipeline-tfstate` (except for `bootstrap`, which keeps a local file). Never edit it by hand, never commit it. |
| **output** | A value Terraform prints after apply, such as the web IP. Read one with `terraform output <name>`. |
| **drift** | Someone changed a resource by hand, so the cloud no longer matches the code. `plan` shows it. |

Reading a plan: `+` means create, `~` means change in place, `-` means delete, and
`-/+` means **delete and recreate** (replace). The last line summarises: `Plan: 3 to add, 0 to change, 0 to destroy.`

---

## Part 1 — Create the environment (up to "ready for Flux")

### 1.0 Prerequisites

1. Spec 012 Gates 1–3 have passed.
2. Billing is enabled on project `nacfson-pipeline`.
3. Open Cloud Shell and get the repository:

   ```bash
   git clone https://github.com/nacfson/nacfson_pipeline.git && cd nacfson_pipeline   # or: cd nacfson_pipeline && git pull
   gcloud config set project nacfson-pipeline
   ```

4. Check the Terraform version:

   ```bash
   terraform version            # first line, e.g. "Terraform v1.17.2"
   cat infra/gcp/.terraform-version
   ```

   **Success:** the two versions are the same.
   **If they differ:** install the pinned version into `~/bin` (Cloud Shell keeps your home folder):

   ```bash
   V=$(cat infra/gcp/.terraform-version)
   curl -sSfLO "https://releases.hashicorp.com/terraform/${V}/terraform_${V}_linux_amd64.zip"
   curl -sSfLO "https://releases.hashicorp.com/terraform/${V}/terraform_${V}_SHA256SUMS"
   grep "linux_amd64.zip" "terraform_${V}_SHA256SUMS" | sha256sum -c -     # must print "OK"
   mkdir -p ~/bin && unzip -o "terraform_${V}_linux_amd64.zip" terraform -d ~/bin && rm terraform_${V}_*
   export PATH="$HOME/bin:$PATH" && terraform version
   ```

### 1.1 Quota preflight

```bash
gcloud compute regions describe asia-northeast3 --flatten=quotas \
  --format="table(quotas.metric,quotas.limit,quotas.usage)" | grep -E "^(CPUS|STATIC_ADDRESSES|IN_USE_ADDRESSES) "
```

**Success:** for `CPUS`, limit minus usage is **at least 6** (two nodes plus one extra during an
upgrade: 2 + 2 + 2 vCPUs), and `STATIC_ADDRESSES` has at least 1 free.
**If not:** stop. Request a quota increase in the console (IAM & Admin → Quotas) and wait for approval.

### 1.2 Bootstrap: the state bucket

```bash
terraform -chdir=infra/gcp/bootstrap init
terraform -chdir=infra/gcp/bootstrap plan -out=bootstrap.tfplan
terraform -chdir=infra/gcp/bootstrap apply bootstrap.tfplan
```

**Success:** `Apply complete! Resources: 1 added` and output `state_bucket = "nacfson-pipeline-tfstate"`.
**If it fails with "bucket name already exists / 409":** the bucket was created earlier. Bring it under
Terraform instead of creating it:
`terraform -chdir=infra/gcp/bootstrap import google_storage_bucket.tfstate nacfson-pipeline-tfstate`, then plan again.

`infra/gcp/bootstrap/terraform.tfstate` is a local file. It is gitignored. It only describes the
bucket, so losing it is harmless (re-import with the command above).

### 1.3 Project: APIs and the $80 budget

```bash
export TF_VAR_billing_account="$(gcloud billing projects describe nacfson-pipeline --format='value(billingAccountName)' | sed 's|billingAccounts/||')"
echo "$TF_VAR_billing_account"      # looks like 0X0X0X-0X0X0X-0X0X0X; never commit it
gcloud services enable cloudresourcemanager.googleapis.com --project nacfson-pipeline
terraform -chdir=infra/gcp/project init
terraform -chdir=infra/gcp/project plan -out=project.tfplan
terraform -chdir=infra/gcp/project apply project.tfplan
```

**Success:** 9 APIs enabled and one budget created; output `budget_name`.

The `gcloud services enable` line is needed once on a new project: Terraform reads the project number
through the Cloud Resource Manager API while it plans, before it can turn any API on itself.
**If it fails with "billing account permission denied":** your account needs *Billing Account
Costs Manager* (or *Billing Account Administrator*) on the billing account. Ask in the console under Billing → Account management.
**If it fails with "API has not been used / is disabled":** wait two minutes after the API step and run `plan`/`apply` again.

**Manual step:** the control center created a $1 budget for its own VM. With GKE running it will
alert every day. In the console (Billing → Budgets & alerts), delete it or raise it. Terraform does not
own that budget.

### 1.4 GKE: network, cluster, node pools, address, KMS, registry

```bash
terraform -chdir=infra/gcp/gke init
terraform -chdir=infra/gcp/gke plan -out=gke.tfplan
```

**Read the plan.** On a first run it must show only `+` lines and end with `0 to change, 0 to destroy`.
If anything shows `-` or `-/+`, stop and ask for a review.

```bash
terraform -chdir=infra/gcp/gke apply gke.tfplan       # about 10–15 minutes
```

**Success:** `Apply complete!` followed by the outputs (`cluster_name`, `ingress_ip`, ...).

**Most likely failures:**

- **"already exists" (409) for the key ring, key or registry.** They were created by hand earlier
  (KMS key rings can never be deleted, so this is common). Import them, then plan again:

  ```bash
  terraform -chdir=infra/gcp/gke import google_kms_key_ring.vault projects/nacfson-pipeline/locations/asia-northeast3/keyRings/nacfson-vault
  terraform -chdir=infra/gcp/gke import google_kms_crypto_key.openbao_unseal projects/nacfson-pipeline/locations/asia-northeast3/keyRings/nacfson-vault/cryptoKeys/openbao-unseal
  terraform -chdir=infra/gcp/gke import google_artifact_registry_repository.public projects/nacfson-pipeline/locations/asia-northeast3/repositories/nacfson-public
  ```

- **"Quota exceeded" for CPUS.** Go back to 1.1.
- **"Identity Pool does not exist"** on the Workload Identity binding. The cluster was still being
  created. Run `plan -out=gke.tfplan` and `apply gke.tfplan` again.
- **Spot capacity unavailable** for `spot-controllers`. GKE keeps retrying; run `plan`/`apply` again later.

### 1.5 Checkpoint "ready for Flux"

```bash
$(terraform -chdir=infra/gcp/gke output -raw get_credentials_command)
kubectl get nodes -L cloud.google.com/gke-nodepool,cloud.google.com/gke-spot,cloud.google.com/gke-provisioning
kubectl get nodes -o custom-columns=NAME:.metadata.name,TAINTS:.spec.taints
kubectl get endpoints kubernetes -n default
terraform -chdir=infra/gcp/gke output ingress_ip
```

**Success:**

- two nodes `Ready`: one `ondemand-core` (provisioning `standard`, no taint) and one `spot-controllers`
  (spot `true`, taint `cloud.google.com/gke-spot=true:NoSchedule`)
- an IP printed for `ingress_ip` (nothing uses it yet)

**Spec 012 API endpoint check:** look at the address in `kubectl get endpoints kubernetes -n default`.
If it is **not** a private address (`10.0.0.0/8`, `172.16.0.0/12` or `192.168.0.0/16`), add it as a `/32` egress `ipBlock` (port 443) to the
`openbao-ingress-policy` patch in `clusters/gke/layers.yaml` through a PR, and merge it **before** Part 3.
OpenBao needs to reach the API to check Kubernetes service-account tokens.

---

## Part 2 — Changing infrastructure

Every change goes through Git first. Nobody edits resources in the console.

The operator applies by hand after each merge. Automating apply (a CI pipeline or Flux's Tofu
Controller) is an open decision: see [Spec 015 research R14](../../specs/015-gke-terraform-infra/research.md)
and constitution Clarification 2.4.1. Neither may be added without a constitution amendment.

1. Edit a `.tf` file (or `infra/gcp/shared-values.json`) on a branch.
2. Open a PR. CI runs `terraform-validate` (format, init without a backend, validate) and
   `check-gcp-shared-values.py`. CI has no Google credentials and never applies anything.
3. Merge.
4. In Cloud Shell: `git pull`, then

   ```bash
   terraform -chdir=infra/gcp/<root> plan -out=change.tfplan
   ```

5. **Read the plan.** Check that it changes only what the PR meant to change.
6. `terraform -chdir=infra/gcp/<root> apply change.tfplan`

**Stop rule.** Do not apply if the plan would delete or replace (`-` or `-/+`) the cluster, the
key ring, the key, or the registry. These carry `prevent_destroy`, so Terraform refuses anyway with
`Instance cannot be destroyed`. That error means "this change is dangerous", not "try harder".
Find out which setting forces a replacement (the plan marks it `# forces replacement`) and change the PR.

**Drift check** (run any time, changes nothing):

```bash
terraform -chdir=infra/gcp/gke plan -detailed-exitcode; echo "exit=$?"
```

`exit=0` means the cloud matches Git. `exit=2` means drift or an unapplied change: the plan lists it.
Fix drift by applying the plan (which puts Git's values back) or, if the manual change was right,
by putting it in a PR first.

**Recovering a broken state file.** The state bucket keeps old versions. List them and copy an older
one back over the current one only if a state file was corrupted:

```bash
gcloud storage ls --all-versions gs://nacfson-pipeline-tfstate/gke/
gcloud storage cp "gs://nacfson-pipeline-tfstate/gke/default.tfstate#<generation>" gs://nacfson-pipeline-tfstate/gke/default.tfstate
```

Then run `plan` and check that it shows no unexpected changes.

---

## Part 3 — Hand-off to Flux and the restore

### 3.1 Order

Terraform apply comes **first**: the KMS key and the Workload Identity binding must exist before
OpenBao starts, or its `gcpckms` seal fails. Then install Flux from the manifests in Git (Spec 012). No GitHub token is needed: the repository is
public and Flux reads it over HTTPS with no credential. Do not use `flux bootstrap github`; it stores a
GitHub key in the cluster and pushes to `main`, which Principle I and render Invariant 2 forbid:

```bash
kubectl config use-context gke_nacfson-pipeline_asia-northeast3-a_nacfson-gke
kubectl apply --server-side -f clusters/gke/flux-system/gotk-components.yaml
kubectl wait --for=condition=Established crd --all --timeout=120s
kubectl apply --server-side -k clusters/gke/flux-system
kubectl -n flux-system rollout status deployment/source-controller --timeout=300s
kubectl -n flux-system rollout status deployment/kustomize-controller --timeout=300s
```

From here Flux creates everything inside the cluster, including `Service/platform/traefik` of type
`LoadBalancer`. GKE sees that Service and builds the web load balancer on the reserved address
`gke-ingress-ip` (contract: `specs/015-gke-terraform-infra/contracts/traefik-l4-edge.md`).

### 3.2 Checkpoint "ready for restore"

```bash
flux get kustomizations
kubectl -n vault exec openbao-0 -- env VAULT_ADDR=http://127.0.0.1:8200 bao status -format=json | grep -E '"initialized"|"type"'
kubectl get pods -A -o wide | grep -E 'openbao-0|postgres-0|keycloak'
kubectl get svc -n platform traefik
IP=$(terraform -chdir=infra/gcp/gke output -raw ingress_ip)
curl -sk --resolve auth.nacfson.cloud:443:$IP https://auth.nacfson.cloud/ -o /dev/null -w '%{http_code}\n'
```

**Success:**

- Flux kustomizations `Ready True` except vault and the layers that depend on it (database, vault-proxies, identity, ingress, tls) wait until the restore: OpenBao is empty, so its readiness check fails (HTTP 501) until the restore fills and unseals it
- OpenBao seal type `gcpckms` and `"initialized": false` (empty, waiting for the restore), not crash-looping
- the database and identity layers are not applied yet: they depend on vault, so `postgres-0` and
  `keycloak` start only after the restore unseals OpenBao
- `traefik` `EXTERNAL-IP` equals `ingress_ip`
- `curl` prints an HTTP status code (any code). That proves internet → load balancer → Traefik before
  DNS moves. Traefik answers with its default certificate until the restore brings up the tls layer.

The restore (`scripts/cutover-bundle.sh restore --target gke`, run from the control center) restores and
migrates OpenBao first, then lets Flux start Postgres, loads the databases, and only then starts Keycloak
([Spec 012 contract §2.3](../../specs/012-gke-platform-migration/contracts/fluentbit-recovery-and-cutover-bundle.md)).

**If `EXTERNAL-IP` is set but `curl` times out:** the web firewall rule is Terraform's, not GKE's. Check it with `gcloud compute firewall-rules describe nacfson-gke-allow-web` (TCP 80/443 to `ingress_ip`) and run `terraform -chdir=infra/gcp/gke plan` for drift.
**If `EXTERNAL-IP` stays `<pending>`:** `kubectl describe svc -n platform traefik` shows the reason in
Events. The usual one is an address name mismatch: the annotation must equal `ingress_address_name`.
**If OpenBao crash-loops with a KMS permission error:** check that the ServiceAccount annotation in
`clusters/gke/layers.yaml` equals `terraform output openbao_gsa_email`.

### 3.3 Control-center kubeconfigs

The control center reads GKE with its own read-only identity `cc-viewer` (IAM `roles/container.viewer`).
Its file `/etc/nacfson-cc-pki/readonly/gke.yaml` holds only the address and CA; the control center fills
in a token from its VM's metadata server. Print the file in Cloud Shell and copy it to the VM:

```bash
cat <<EOF
apiVersion: v1
kind: Config
clusters:
- name: gke
  cluster:
    server: https://$(terraform -chdir=infra/gcp/gke output -raw cluster_endpoint)
    certificate-authority-data: $(terraform -chdir=infra/gcp/gke output -raw cluster_ca_certificate)
contexts:
- name: gke
  context: {cluster: gke, user: cc-viewer}
current-context: gke
users:
- name: cc-viewer
  user: {}
EOF
```

For the restore, the control center needs **temporary** admin access. Print the same file with your
own short-lived token (valid about one hour) and paste it into the control center's Break-Glass
kubeconfig slot for `gke`. Do not save it to a file:

```bash
cat <<EOF
apiVersion: v1
kind: Config
clusters:
- name: gke
  cluster:
    server: https://$(terraform -chdir=infra/gcp/gke output -raw cluster_endpoint)
    certificate-authority-data: $(terraform -chdir=infra/gcp/gke output -raw cluster_ca_certificate)
contexts:
- name: gke
  context: {cluster: gke, user: operator}
current-context: gke
users:
- name: operator
  user:
    token: $(gcloud auth print-access-token)
EOF
```

**Check:** in the control center, a read-only view of the `gke` environment lists the nodes.

### 3.4 Hand-off

Run the Spec 012 restore from the control center with a **fresh** export from vps-k3s
(`specs/012-gke-platform-migration/quickstart.md`, Gate 4). This runbook ends here.

---

## Part 4 — Removing the environment (before the DNS switch only)

Use this to tear down a rehearsal. After DNS points at GKE, removal is an outage: do not use it.

1. **Check DNS still points at vps-k3s.**

   ```bash
   dig +short auth.nacfson.cloud
   terraform -chdir=infra/gcp/gke output -raw ingress_ip
   ```

   **Stop** if the two match.

2. **Allow deletion of the cluster** in a reviewed PR: in `infra/gcp/gke/cluster.tf` set
   `deletion_protection = false` and delete the `lifecycle { prevent_destroy = true }` block.
   Merge, `git pull`, then `plan -out=unprotect.tfplan` (expect `1 to change`) and `apply unprotect.tfplan`.

3. **Remove the web load balancer first,** so GKE deletes its forwarding rule, health check and health-check firewall rule:

   ```bash
   flux suspend kustomization --all
   kubectl delete svc -n platform traefik
   gcloud compute forwarding-rules list --filter="region:asia-northeast3"     # must become empty
   ```

4. **Destroy the compute and network resources** (only these addresses):

   ```bash
   terraform -chdir=infra/gcp/gke plan -destroy -out=remove.tfplan \
     -target='google_container_node_pool.pools["ondemand-core"]' \
     -target='google_container_node_pool.pools["spot-controllers"]' \
     -target='google_container_cluster.this' \
     -target='google_compute_address.ingress' \
     -target='google_compute_firewall.web_ingress' \
     -target='google_compute_subnetwork.gke' \
     -target='google_compute_network.gke' \
     -target='google_project_iam_member.gke_nodes_default' \
     -target='google_service_account.gke_nodes'
   terraform -chdir=infra/gcp/gke apply remove.tfplan
   ```

   The plan also lists `google_service_account_iam_member.openbao_workload_identity`, because it
   depends on the cluster. That is expected; it comes back on the next create.

5. **Verify nothing billable is left:**

   ```bash
   gcloud container clusters list
   gcloud compute addresses list
   gcloud compute forwarding-rules list
   gcloud compute firewall-rules list --filter="network:nacfson-gke"
   gcloud compute disks list --filter="zone:asia-northeast3-a"
   ```

   All must be empty for this feature. Disks that GKE created for volume claims (`pvc-...`) are **not**
   deleted with the cluster and keep billing. Delete them:
   `gcloud compute disks delete <name> --zone asia-northeast3-a`. Their data is only a rehearsal copy.

6. **What stays on purpose:** the KMS key ring and key (Google never deletes key rings), the registry,
   the state bucket, the APIs and the budget. They cost cents per month. To create the environment
   again, revert the PR from step 2 and run Part 1 step 1.4 again: the same names are reused.

---

## Cost

Expected steady state: about $65/month (two nodes about $39, load balancer about $18, disks, address and
traffic about $8). The budget alerts at $40, $72 and $80.
*To be recorded after one month of running (Spec 015 T045).*

## Offline check (no credentials, any machine)

```bash
python3 scripts/check-gcp-shared-values.py
for d in infra/gcp/bootstrap infra/gcp/project infra/gcp/gke; do
  terraform -chdir=$d fmt -check -recursive &&
  terraform -chdir=$d init -backend=false -lockfile=readonly &&
  terraform -chdir=$d validate
done
```
