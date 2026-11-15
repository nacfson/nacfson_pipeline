# Research: GKE Infrastructure as Code (Terraform)

**Feature**: [spec.md](spec.md) | **Plan**: [plan.md](plan.md) | **Date**: 2026-11-14

Each item lists the decision, why it was made, and the alternatives considered. All Technical Context unknowns are resolved here.

---

## R1. Who owns the load balancer (FR-007a)

**Decision**: GKE owns the load balancer. `Service/traefik` in `platform` becomes `type: LoadBalancer` with:

- `spec.loadBalancerClass: networking.gke.io/l4-regional-external`, which gives a backend-service-based regional external pass-through Network Load Balancer
- `spec.externalTrafficPolicy: Local`
- the annotation `networking.gke.io/load-balancer-ip-addresses: gke-ingress-ip`, which references the reserved address by **name**, so no IP literal is committed

Terraform owns the reserved regional address `gke-ingress-ip` and the web firewall rule `nacfson-gke-allow-web` (TCP 80/443 to that address, targeted at the `gke-nodes` service account). The cluster sets `disable_l4_lb_firewall_reconciliation = true` (GKE 1.31.3-gke.105600+), so GKE does not create its own load balancer firewall rule. GKE does not reserve addresses referenced this way, so the address has to exist before the Service. GKE creates and removes the forwarding rule, backend service, health check and the health-check firewall rule for the Service. *Changed after code review (2026-11-14): the operator asked to move as much as possible into Terraform.*

**Rationale**:

- **Supported path.** This is GKE's own way to put an L4 load balancer in front of pods. Dataplane V2 delivers packets addressed to the load balancer IP to the Traefik pod. A Terraform-built load balancer pointing at GKE's managed instance groups and Traefik's `hostPort` would depend on node-level handling of foreign destination IPs. That handling isn't documented for GKE, and it would mean Terraform editing GKE-managed instance groups, which GKE does not support.
- **One owner per resource (FR-002).** Terraform: the address. GKE controller: the load balancer parts. Flux: the Service.
- **`externalTrafficPolicy: Local`.** It keeps visitors' source IPs (spec FR-007). The load balancer's health check also marks only nodes with a ready Traefik pod as healthy. Traefik is pinned to `cloud.google.com/gke-provisioning: standard`, so `spot-controllers` never receives web traffic.
- **Pass-through.** TLS keeps terminating at Traefik, as the Platform-Owned Edge TLS rule requires.

**Consequences** (all in this feature's scope through FR-007a and FR-022):

- **Traefik `hostPort` removed.** `deploy/platform/traefik/deployment.yaml` drops `hostPort: 80/443`. Traffic enters only through the load balancer, and no node serves 80/443 on its own address. This layer is deployed only by `clusters/gke/`, so local-k3s and vps-k3s are unaffected. The `Recreate` strategy stays, so a rollout never surges past the 1/n reservation.
- **Render invariant rewritten.** Invariant 10 in `scripts/render-invariants.py` changes from "zero LoadBalancer, Traefik hostPort {80, 443}" to:
  - exactly one `LoadBalancer` Service on gke, `platform/traefik`
  - with the class, the policy and the address-name annotation above
  - Traefik has no `hostPort`
- **Preflight rule updated.** `scripts/platform-preflight.py` Rule 2 (GKE) allows that single Service and still rejects every other `LoadBalancer`. The project-namespace rule rejecting `NodePort` and `LoadBalancer` (`platform-preflight.py:1579`) is unchanged. `tests/preflight/test_platform_preflight.py::test_30` is updated, and new cases cover the allowed shape and near-misses: wrong class, missing annotation, `Cluster` policy, another namespace.
- **Spec 012 contract updated.** `specs/012-gke-platform-migration/contracts/gke-capacity-and-placement.md` §3 is rewritten from "Zero-LoadBalancer" to "Single L4 pass-through LoadBalancer".

**Alternatives considered**:

- **Terraform-owned L4 load balancer targeting GKE's node instance groups, keeping `hostPort`**: rejected for the delivery-path and GKE-managed-resource reasons above.
- **Application (L7) load balancer or GKE Ingress**: rejected. It decrypts TLS at Google's edge, which conflicts with the Constitution's Platform-Owned Edge TLS rule.
- **kubeip with no load balancer**: offered and declined by the user for now. It stays a later option (about $18/month cheaper). The address resource and Spec 012's `hostPort` contract would come back in that case.
- **`spec.loadBalancerIP`**: rejected. It is deprecated upstream and needs the IP literal in Git before Terraform has created it. The name annotation needs GKE 1.29 or later, which is satisfied.

---

## R2. Terraform and provider versions

**Decision**:

- `required_version = ">= 1.9.0, < 2.0.0"`.
- The `hashicorp/google` provider is pinned to the latest stable major at implementation time. As of 2026-11, the release series shows 8.x, so the constraint is `~> 8.0`, confirmed against the registry when the code is written.
- `.terraform.lock.hcl` is committed for each root, with hashes for `linux_amd64` and `linux_arm64` (`terraform providers lock -platform=...`).
- CI uses `hashicorp/setup-terraform` with an exact Terraform version. The runbook tells the operator to check `terraform version` in Cloud Shell. If the version is outside the range, it installs the pinned version into `~/bin` first.

**Rationale**: these are the Constitution's "pinned tool versions" applied to infrastructure. The lock file makes Cloud Shell and CI resolve identical providers.

**Alternatives considered**: floating versions (non-reproducible); a module registry wrapper such as `terraform-google-modules/kubernetes-engine` (hides the dozens of settings this spec fixes explicitly, and adds a dependency the operator cannot read easily).

---

## R3. Folder layout and state

**Decision**: three roots under `infra/gcp/`: `bootstrap/` (local state, run once), `project/` and `gke/` (GCS backend, prefixes `project` and `gke`). State bucket `nacfson-pipeline-tfstate`:

- regional, in `asia-northeast3`
- uniform access, public access prevention enforced
- object versioning on, with a lifecycle rule keeping 10 noncurrent versions

`*.tfstate*`, `.terraform/` and `*.tfplan` are added to `.gitignore`. Any `terraform.tfvars` with operator values stays uncommitted.

**Rationale**: a separate bootstrap means the bucket cannot be destroyed by a mistake in `gke/` (spec US2 and FR-011). Versioning lets a corrupted state be recovered. The GCS backend has built-in state locking.

**Alternatives considered**: one root (larger blast radius); Terraform Cloud (an external service holding state, and another account).

---

## R4. Values shared with `clusters/gke/` (FR-018)

**Decision**: one committed JSON file, `infra/gcp/shared-values.json`, holds the values both sides depend on. Terraform reads it with `jsondecode(file(...))` into `locals`, so no value is typed twice in HCL. `scripts/check-gcp-shared-values.py` (Python 3, PyYAML, which the repo's render scripts already use) compares it with:

| Shared value | Source in the repo |
|---|---|
| Pool names, machine types, counts, Spot label and taint | `clusters/gke/capacity.yaml` |
| KMS project, region, key ring, key | `clusters/gke/layers.yaml` (OpenBao `seal "gcpckms"`) |
| `openbao-kms` GSA email | `clusters/gke/layers.yaml` (`iam.gke.io/gcp-service-account`) |
| `vault/openbao` service account | `deploy/platform/vault/service-accounts.yaml` |
| Region and registry name | `deploy/gcp/cloudbuild.yaml` (`_GAR_REGION`, `_GAR_REPO`) |
| Registry cleanup rules | `deploy/gcp/gar-cleanup-policy.json` |
| Ingress address name | `deploy/platform/traefik/service.yaml` annotation |

It exits non-zero and names each mismatch. It runs in CI, and from `scripts/render-validate.sh gke`.

**Rationale**: comparing JSON with YAML needs no HCL parser. Terraform and the check read the same file, so drift can come only from the YAML side, which is exactly what the check covers.

**Alternatives considered**: parsing HCL in Python (no stdlib parser, fragile); generating `clusters/gke/` values from Terraform outputs (would make Flux manifests depend on a Terraform run, breaking the offline Gate 1).

---

## R5. Cluster settings

**Decision** (`google_container_cluster`):

- `location = "asia-northeast3-a"` (zonal; the free tier covers one zonal cluster's management fee)
- `remove_default_node_pool = true`, `initial_node_count = 1`
- `deletion_protection = true`, plus `lifecycle.prevent_destroy`
- `datapath_provider = "ADVANCED_DATAPATH"`
- `logging_service = "none"`, `monitoring_service = "none"`
- `workload_identity_config.workload_pool = "nacfson-pipeline.svc.id.goog"`
- `release_channel = "REGULAR"`
- Maintenance policy: a daily recurring window from 18:00 to 22:00 UTC (03:00–07:00 KST). GKE requires at least 48 hours of availability in any 32-day period, and a weekly 4-hour window does not satisfy that.
- A dedicated VPC and subnet owned by these definitions, VPC-native with secondary ranges (see R13)
- Public control-plane IP endpoint with no authorized networks (FR-025). The DNS endpoint is left disabled for now. Switching to DNS-only later is an in-place update (`control_plane_endpoints_config`) on a cluster created after Private Service Connect became the default, so no recreation is needed.

**Rationale**: these mirror Spec 012 Gate 4 step 2 and the 2026-11-14 clarifications. The deferred maintenance-window choice is settled here, so planned node replacements, and the few minutes of downtime each one causes, fall in the early-morning hours in Korea.

**Alternatives considered**: the `RAPID` channel (more frequent replacements); no channel with auto-upgrade off (GKE still force-upgrades at end of support, and security patches arrive late); a regional cluster (management fee not covered by the free tier).

---

## R6. Node pools

**Decision**:

- **`ondemand-core`**: e2-medium, `node_count = 1`
  - 30 GB `pd-balanced` boot disk
  - service account `gke-nodes` with `roles/container.defaultNodeServiceAccount` only, scope `cloud-platform`
  - `workload_metadata_config.mode = "GKE_METADATA"`
  - auto-repair and auto-upgrade on
  - `upgrade_settings`: surge, `max_surge = 1`, `max_unavailable = 0`
- **`spot-controllers`**: e2-small, `spot = true`, same disk and identity
  - taint `cloud.google.com/gke-spot=true:NO_SCHEDULE`, set explicitly (GKE adds the label but not the taint)

**Rationale**: matches `clusters/gke/capacity.yaml`. Surge with one extra node keeps the old node until the new one is ready. That is 2 + 2 + 2 = 6 vCPUs during an e2-medium surge, within the control center's `asia-northeast3` CPU quota of 6. A 30 GB disk instead of the 100 GB default saves cost; the platform's PVCs are separate disks.

**Alternatives considered**: `max_surge = 0, max_unavailable = 1` (no quota risk, but the only on-demand node is drained before a replacement exists, so the outage is longer); the default compute service account (Editor-level access, which spec FR-006 forbids).

---

## R7. KMS and Workload Identity

**Decision**: key ring `nacfson-vault` and key `openbao-unseal` in `asia-northeast3`, symmetric `ENCRYPT_DECRYPT`, rotation off, `prevent_destroy`. GSA `openbao-kms` gets two roles on the key: `roles/cloudkms.cryptoKeyEncrypterDecrypter` and `roles/cloudkms.viewer`, for `cloudkms.cryptoKeys.get`, which the `gcpckms` seal calls at startup. `roles/iam.workloadIdentityUser` on the GSA goes to `serviceAccount:nacfson-pipeline.svc.id.goog[vault/openbao]`.

**Rationale**: these are least-privilege grants scoped to one key. OpenBao reads the key's metadata at startup, which `cryptoKeyEncrypterDecrypter` alone does not allow. Automatic rotation is left off for now. The seal handles key versions, but rotating adds a moving part that the cutover rehearsal has not covered.

**Alternatives considered**: project-level KMS roles (too broad); a key file in a Kubernetes Secret (forbidden by Constitution Principle V and the Cloud KMS exception).

---

## R8. Registry

**Decision**: `google_artifact_registry_repository.nacfson-public` (DOCKER, `asia-northeast3`), with cleanup policies matching `deploy/gcp/gar-cleanup-policy.json` and `allUsers: roles/artifactregistry.reader`. `lifecycle.prevent_destroy` is set. Images stay on public GHCR for now (spec Assumptions). The registry exists so Cloud Build can publish.

**Alternatives considered**: deferring the registry (Spec 012 already depends on it for Cloud Build); a private registry (would need pull credentials on local-k3s, against the Spec 012 clarification).

---

## R9. Budget

**Decision**: `google_billing_budget` in `project/`, scoped to project `nacfson-pipeline`:

- amount 80 USD per month
- thresholds 0.5, 0.9 and 1.0 of actual spend
- default email recipients (billing admins)

The billing account ID is passed as a variable through `TF_VAR_billing_account` in Cloud Shell, never committed. The `billingbudgets.googleapis.com` API is enabled in `project/`. The runbook includes a manual step to delete or raise the control center's $1 budget.

**Rationale**: the steady state is about $39 (nodes) + $18 (load balancer) + about $8 (disks, address, data processing), roughly $65, so $80 has headroom (SC-006).

**Alternatives considered**: Terraform importing the $1 budget (it belongs to the control center's setup, so taking it over would cross repositories, against Q1).

---

## R10. CI without cloud credentials (FR-017, FR-019)

**Decision**: a new job `terraform-validate` in `.github/workflows/lint.yaml`, triggered when `infra/gcp/**`, `scripts/check-gcp-shared-values.py` or the shared YAML sources change. For each root it runs `terraform fmt -check -recursive`, `terraform init -backend=false -lockfile=readonly` and `terraform validate`. It then runs `python3 scripts/check-gcp-shared-values.py`, with no `google-github-actions/auth` step. Gitleaks already scans all paths, and `.gitignore` covers state and plan files.

**Rationale**: validation needs only provider schemas, which download anonymously. With no credentials in CI, a compromised workflow on this public repository cannot touch the project.

**Alternatives considered**: `terraform plan` in CI (needs read credentials, rejected in the clarification); tflint or checkov (useful, but not required by the spec, so left for later).

---

## R11. Operator access: Cloud Shell and the control center

**Decision**: the runbook uses Cloud Shell for every apply. `outputs.tf` exposes:

- `cluster_name`, `cluster_zone` and `cluster_endpoint`
- `cluster_ca_certificate`, marked sensitive
- `ingress_ip` and `get_credentials_command`

The control center's read-only `gke.yaml` (server plus CA, with a `user: {}` placeholder) and the temporary admin kubeconfig for the restore are built from these outputs by the operator, following the control center's own guide. The IAM for `cc-viewer` stays in the private repository (Q1).

**Rationale**: covers spec FR-010, FR-024 and FR-025 without Terraform reaching into the control center's configuration.

---

## R12. Checks the runbook performs

**Decision**:

1. **Quota preflight.** `gcloud compute regions describe asia-northeast3 --format=...` checks that `CPUS` limit minus usage is at least 6, and the address quota. It stops if not.
2. **Spec 012 API endpoint check.** `kubectl get endpoints kubernetes -n default`. The endpoint is public (FR-025), so the address the pods see may be public. If it is, the `/32` is added to `openbao-ingress-policy` in `clusters/gke/layers.yaml` before Flux installation.
3. **"ready for Flux".** Two nodes with the expected labels and taint, plus the address reserved and outputs present.
4. **"ready for restore".** Flux Ready, `openbao-0` running and `bao status` reporting uninitialized, the database and identity layers waiting for vault, the `traefik` Service showing the reserved IP as its external IP, and the load balancer health check healthy.

**Rationale**: every checkpoint the spec asks for is a concrete command with an expected result (FR-020, FR-024).

---

## R13. Network: dedicated VPC with Private Google Access

**Decision**: `gke/network.tf` creates:

- VPC `nacfson-gke` (custom mode, no auto subnets)
- one subnet `nacfson-gke-asia-northeast3` with:
  - nodes `10.20.0.0/22`, secondary `pods` `10.24.0.0/17` and `services` `10.28.0.0/22`
  - **`private_ip_google_access = true`**

The VPC's only internet opening is the Terraform-owned web rule (TCP 80/443 to the reserved address). GKE adds rules for the control plane and the load balancer's health checks. There is no SSH, RDP or ICMP open to the internet, and node addresses and node ports stay closed. Nodes keep external IPs for egress (GHCR pulls, and the OCI Kafka link from Spec 013/014), so no Cloud NAT is needed.

**Rationale**:

- **KMS needs it.** `clusters/gke/layers.yaml:173-176` pins `cloudkms.googleapis.com` to `199.36.153.8` (`private.googleapis.com`), and `openbao-ingress-policy` allows only `199.36.153.8/30:443` for KMS. That range is served through Private Google Access, which is off by default on the `default` network's subnets. Enabling it there would mean Terraform taking over a shared default subnet, or a manual change outside any owner (FR-002).
- **The default network is exposed.** It comes with `default-allow-ssh`, `default-allow-rdp` and `default-allow-icmp` open to `0.0.0.0/0`. A dedicated VPC starts closed.
- **The ranges fit existing policies.** They sit inside `10.0.0.0/8`, which the platform's NetworkPolicies already treat as private (RFC1918 rules and the OpenBao API-server allowance).

**Alternatives considered**: the default network with Private Google Access switched on by hand (no single owner, and keeps the open SSH rule); Cloud NAT with private nodes (about $1/month plus per-GB charges, and more parts for no requirement in the spec).

---

## R14. Applying later Terraform changes (DEFERRED)

**Status**: deferred by the repository maintainer on 2026-11-15. Decide after GKE is running and the
Spec 012 cutover is done. Until then the operator runs `plan` and `apply` by hand after each merge
(R10, R11, [`docs/runbooks/gke-deploy.md`](../../docs/runbooks/gke-deploy.md)).

**Question**: after the first, manual creation, how should Terraform changes such as node count or
machine size reach Google Cloud?

| Option | How it works | Main cost |
|---|---|---|
| **A. GitHub Actions pipeline** (leading candidate) | PR runs fmt, validate, tflint, a security scan, `plan` and a cost estimate, posted as a PR comment. Merge to `main` runs `apply` after the operator approves a protected GitHub Environment. A daily `plan -detailed-exitcode` opens an issue on drift. | CI gets a Google identity through keyless Workload Identity Federation, restricted to this repository, `main` and that environment. Fork PRs must never receive it. Push-based. |
| **B. Tofu Controller (Flux)** | Flux reconciles `Terraform` objects in-cluster; plans wait for `approvePlan` in Git; drift detected on each interval. Scope limited to node pools. | The controller would change the cluster it runs on (production setups use a separate management cluster); the cluster holds an identity able to rebuild itself; a runner pod needs about 300–500 MiB on small nodes; project is v0.x and community-maintained. |
| **C. Keep manual apply** | Current state. | Depends on one workstation's credentials; no approval record beyond the merged PR. |

**What the decision needs**:

- a new spec (CI apply contradicts R10 and FR-017 of this feature)
- a constitution amendment: today only the named operator changes cloud infrastructure
  (Clarification 2.4.1)
- in either automated option, the state bucket and the CI or controller identity stay a one-time
  manual bootstrap; Terraform state carries over unchanged, so nothing created by hand is lost
- node autoscaling is a separate decision: preflight's 1/n budget assumes a fixed node count
