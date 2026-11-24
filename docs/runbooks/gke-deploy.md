# Deploy GKE from your PC

The short version of [`gke-terraform.md`](gke-terraform.md). Each step ends with **✅ Check**: do
not go on until it passes. Run every command from the repository folder:

```bash
cd ~/Projects/nacfson_pipeline
git switch main && git pull --ff-only
git status --short
```

✅ **Check:** `git status --short` prints nothing. Steps 4 and 6 apply the files on your disk, and Flux
reads `main` from GitHub, so both must be the reviewed, merged `main`.

> **Start only after Spec 012 Gates 2 and 3 have passed** (see
> [`specs/012-gke-platform-migration/quickstart.md`](../../specs/012-gke-platform-migration/quickstart.md)).
> Step 4 starts billing, about $65 a month.

---

## 1. Tools (once)

```bash
terraform version | head -1
gcloud --version | head -1
kubectl version --client
flux --version
gke-gcloud-auth-plugin --version
```

✅ **Check:** Terraform is `v1.17.2`, Flux is `2.9.5`, and every command prints a version.

If `gke-gcloud-auth-plugin` is missing:

```bash
sudo apt-get install -y google-cloud-cli-gke-gcloud-auth-plugin
```

(If you installed gcloud without apt: `gcloud components install gke-gcloud-auth-plugin`.)

If `kubectl` warns `Unable to read /etc/rancher/k3s/k3s.yaml`, your `kubectl` is the K3s one. It
ignores `~/.kube/config` unless told to use it, so step 5 would connect to nothing:

```bash
echo 'export KUBECONFIG="$HOME/.kube/config"' >> ~/.bashrc && source ~/.bashrc
```

## 2. Sign in (once per PC)

```bash
gcloud auth login
gcloud auth application-default login
gcloud config set project nacfson-pipeline
gcloud auth application-default set-quota-project nacfson-pipeline
```

The first login is for `gcloud`. The second one is for Terraform. Both open a browser and use your
Google account.

✅ **Check:** `gcloud config get project` prints `nacfson-pipeline`.

## 3. Quota check

```bash
gcloud compute regions describe asia-northeast3 --flatten=quotas \
  --format="table(quotas.metric,quotas.limit,quotas.usage)" | grep -E "^(CPUS|STATIC_ADDRESSES) "
gcloud compute project-info describe --flatten=quotas \
  --format="table(quotas.metric,quotas.limit,quotas.usage)" | grep -E "^CPUS_ALL_REGIONS "
```

✅ **Check:** `CPUS` and `CPUS_ALL_REGIONS` each have at least **6** free (limit − usage), and
`STATIC_ADDRESSES` has at least **1** free.
If not, request more quota in the console (IAM & Admin → Quotas) and wait.

## 4. Create everything (3 parts, in this order)

Each part: `init` downloads plugins, `plan` shows what will be created, `apply` creates it.
**Read each plan before applying.** It must say `0 to change, 0 to destroy`.

**4a. State bucket** (where Terraform keeps its records)

```bash
terraform -chdir=infra/gcp/bootstrap init
terraform -chdir=infra/gcp/bootstrap plan -out=bootstrap.tfplan
terraform -chdir=infra/gcp/bootstrap apply bootstrap.tfplan
```

**4b. APIs and the $110 budget**

Turns on the Google services the cluster needs and adds a **budget alert**. The budget is **not a
spending limit**: Google emails you at $55, $99 and $110 of spending in a month, and charges keep going
past $110. Charges go to the card on the billing account linked to the project, once a month. Expected
cost is about **$96/month** (servers ~$70, load balancer ~$18, disks and traffic ~$8), starting at 4c.
Current spending: console → Billing → Reports.

```bash
export TF_VAR_billing_account="$(gcloud billing projects describe nacfson-pipeline --format='value(billingAccountName)' | sed 's|billingAccounts/||')"
gcloud services enable cloudresourcemanager.googleapis.com --project nacfson-pipeline
terraform -chdir=infra/gcp/project init
terraform -chdir=infra/gcp/project plan -out=project.tfplan
terraform -chdir=infra/gcp/project apply project.tfplan
```

Then delete or raise the control center's old **$1 budget** in the console (Billing → Budgets &
alerts). Otherwise it alerts every day.

**4c. The cluster** (about 10–15 minutes)

```bash
terraform -chdir=infra/gcp/gke init
terraform -chdir=infra/gcp/gke plan -out=gke.tfplan
terraform -chdir=infra/gcp/gke apply gke.tfplan
```

✅ **Check:** each part ends with `Apply complete!`.

## 5. Connect kubectl

```bash
$(terraform -chdir=infra/gcp/gke output -raw get_credentials_command)
kubectl get nodes -L cloud.google.com/gke-nodepool
terraform -chdir=infra/gcp/gke output ingress_ip
kubectl get endpoints kubernetes -n default
```

✅ **Check:** two nodes are `Ready` (`ondemand-core` and `spot-controllers`), and `ingress_ip`
prints an IP.

`kubectl` now talks to GKE. To switch: `kubectl config use-context default` (local-k3s) or
`kubectl config use-context gke_nacfson-pipeline_asia-northeast3-a_nacfson-gke` (GKE).
`kubectl config current-context` shows which one is active.

**Look at the last command's address.** If it does **not** start with `10.`, `172.16–31.` or
`192.168.`, add it as a `/32` on TCP 443 to `openbao-ingress-policy` in `clusters/gke/layers.yaml`,
and merge that PR **before step 6**. Otherwise every OpenBao login fails.

## 6. Start Flux

No GitHub token is needed. The repository is public, and Flux reads it over HTTPS with no
credential (constitution Principle I). Install Flux from the files already in Git:

```bash
kubectl config use-context gke_nacfson-pipeline_asia-northeast3-a_nacfson-gke
kubectl apply --server-side -f clusters/gke/flux-system/gotk-components.yaml
kubectl wait --for=condition=Established crd --all --timeout=120s
kubectl apply --server-side -k clusters/gke/flux-system
kubectl -n flux-system rollout status deployment/source-controller --timeout=300s
kubectl -n flux-system rollout status deployment/kustomize-controller --timeout=300s
```

The first line makes sure you install into GKE, not local-k3s. The second line creates Flux's
resource types; the fourth adds the repository settings, after which Flux deploys everything else.

Do **not** use `flux bootstrap github`. It would store a GitHub key inside the cluster, rewrite
`gotk-sync.yaml` and push a commit to `main`, which the constitution and render Invariant 2 forbid.

## 7. Ready check

```bash
flux get kustomizations
kubectl -n vault exec openbao-0 -- env VAULT_ADDR=http://127.0.0.1:8200 bao status -format=json | grep -E '"initialized"|"type"'
kubectl get svc -n platform traefik
kubectl get pods -A | grep -E 'ImagePull|ErrImage'
IP=$(terraform -chdir=infra/gcp/gke output -raw ingress_ip)
curl -sk --resolve auth.nacfson.cloud:443:$IP https://auth.nacfson.cloud/ -o /dev/null -w '%{http_code}\n'
```

✅ **Check** (may take up to 30 minutes):

- `Ready True` for governance, cert-manager, traefik, project-boundaries*, projects and observability.
  vault and the layers that depend on it (database, vault-proxies, identity, ingress, tls) wait until the restore: OpenBao is empty, so its readiness check fails (HTTP 501) until the restore fills and unseals it.
- OpenBao shows `"type": "gcpckms"` and `"initialized": false`. It is empty and waiting for the restore.
- the Traefik `EXTERNAL-IP` equals `$IP`
- the `ImagePull` line prints nothing (every image was downloaded)
- `curl` prints any HTTP code

**GKE is now deployed but serves no real data, and DNS still points at OCI.**

**Images.** GKE downloads the same images your other clusters use, from GitHub's registry
(`ghcr.io/nacfson/...`). They are public and built for both amd64 (GKE) and arm64 (OCI), so no login
or copy is needed. Terraform also creates an empty Google registry (`nacfson-public`); moving images
there is a later, separate change (see "Later: move images to Google's registry").

## 8. Next: move the data and switch DNS

Follow **Gate 4** in [`specs/012-gke-platform-migration/quickstart.md`](../../specs/012-gke-platform-migration/quickstart.md):
fresh export from vps-k3s → restore onto GKE → test → point DNS at `ingress_ip`.

---

## After deploying: sign Terraform out

`gcloud auth application-default login` left a long-lived Google sign-in on this PC that can change the
whole project. Remove it when you are done, and run step 2's second login again next time:

```bash
gcloud auth application-default revoke
```

## Later: change the cluster's size

**Who owns what.** Terraform owns the machines: how many nodes, which size. Flux owns what runs on
them: pods, copies, CPU and memory. Terraform does not re-apply itself the way Flux does; you run
`plan` and `apply` after each merge.

**Open decision:** whether these Terraform changes should later be applied automatically (a GitHub
Actions pipeline or Flux's Tofu Controller) is deferred. Until then you apply by hand. See
[Spec 015 research R14](../../specs/015-gke-terraform-infra/research.md) and constitution
Clarification 2.4.1.

**More or bigger nodes** (Terraform)

1. Edit the pool in **two** files so they agree:
   - `infra/gcp/shared-values.json` → `node_pools[]` (`node_count` or `machine_type`)
   - `clusters/gke/capacity.yaml` → the same pool's `nodeCount` / `machineType`, plus `allocatable`
     and the top-level totals. The 1/n per-project budget is calculated from these.
2. Check, then open a PR:

   ```bash
   python3 scripts/ci/check-gcp-shared-values.py
   ./scripts/ci/render-validate.sh gke
   ```

3. After merge: `terraform -chdir=infra/gcp/gke plan -out=change.tfplan`. The plan should show `~`
   (change) on the node pool, never `-/+` on the cluster. Then `apply change.tfplan`.
   Nodes are replaced one at a time, so the site stays up.
4. Check step 3's quota first (each extra `e2-standard-2` needs 2 CPUs) and expect roughly
   +$60/month per extra `e2-standard-2`.

**More copies or more memory for a service** (Flux, no Terraform)

Edit `replicas` or `resources` in the service's YAML under `deploy/` (or its patch in
`clusters/gke/layers.yaml`), run `./scripts/ci/render-validate.sh gke`, and merge. Flux applies it
within about 5 minutes. If preflight fails, the service does not fit: add nodes first.

## Later: move images to Google's registry

Optional. It makes downloads faster (same region) and removes the dependency on GitHub.

1. Build and push into `nacfson-public` with Cloud Build (`deploy/gcp/cloudbuild.yaml`, contract:
   [Spec 012 GAR contract](../../specs/012-gke-platform-migration/contracts/portable-network-policy-and-gar.md)).
2. In a PR, change each `image:` under `deploy/` to
   `asia-northeast3-docker.pkg.dev/nacfson-pipeline/nacfson-public/<name>@sha256:<digest>`.
3. Merge. Flux rolls the pods onto the new images.

---

## If something fails

| Error | Fix |
|---|---|
| `409 already exists` (bucket, key ring, key, registry) | It was made earlier. Use the `import` command from [`gke-terraform.md`](gke-terraform.md) §1.2 / §1.4, then `plan` again. |
| `Quota exceeded` for CPUS | Go back to step 3. |
| `Identity Pool does not exist` | The cluster was still starting. Run `plan` and `apply` in 4c again. |
| `Cloud Resource Manager API has not been used` (4b) | Run the `gcloud services enable cloudresourcemanager…` line from 4b, wait 2 minutes, run `plan` again. |
| `API has not been used` (other APIs) | Wait 2 minutes after 4b and run again. |
| `billing account permission denied` (4b) | Your account needs *Billing Account Costs Manager* on the billing account. |
| `gke-gcloud-auth-plugin not found` (step 5) | Step 1. |
| A pod shows `ImagePullBackOff` | `kubectl describe pod -n <namespace> <pod>` and read Events. Usually a wrong image name or digest in `deploy/`. |
| Traefik `EXTERNAL-IP` stays `<pending>` | `kubectl describe svc -n platform traefik` and read Events. |
| OpenBao crash-loops with a KMS error | The OpenBao ServiceAccount annotation in `clusters/gke/layers.yaml` must equal `terraform -chdir=infra/gcp/gke output openbao_gsa_email`. |

**Never commit** `*.tfstate`, `*.tfplan`, `.terraform/` or your billing account ID. They are already in `.gitignore`.
**To remove everything** (only before the DNS switch): [`gke-terraform.md`](gke-terraform.md) Part 4.
