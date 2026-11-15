# Data Model: GKE Infrastructure as Code (Terraform)

**Feature**: [spec.md](spec.md) | **Research**: [research.md](research.md)

This feature has no application data. Its "data" is the set of cloud resources, who owns each one, the values shared with `clusters/gke/`, and the states the environment moves through.

## 1. Terraform roots

| Root | State | Purpose | Applied |
|---|---|---|---|
| `infra/gcp/bootstrap/` | local `terraform.tfstate` (gitignored) | State bucket | Once ever |
| `infra/gcp/project/` | `gs://nacfson-pipeline-tfstate/project` | APIs and budget | Rarely |
| `infra/gcp/gke/` | `gs://nacfson-pipeline-tfstate/gke` | Network, cluster, pools, identities, KMS, address, registry | Create, then on reviewed changes |

The roots depend on each other in order: `bootstrap` → `project` → `gke`. `gke` does not read `project`'s state. It only needs the APIs to be on.

## 2. Resource inventory and ownership

Every cloud resource has exactly one owner (FR-002).

| Resource | Name / value | Owner | Root | Protection |
|---|---|---|---|---|
| GCS bucket | `nacfson-pipeline-tfstate` | Terraform | bootstrap | versioning, `prevent_destroy` |
| Project services | container, compute, cloudkms, artifactregistry, cloudbuild, iam, iamcredentials, billingbudgets | Terraform | project | `disable_on_destroy = false` |
| Billing budget | 80 USD/month, 50/90/100% | Terraform | project | — |
| Billing budget ($1, control center) | existing | **Manual removal** (runbook) | — | — |
| VPC | `nacfson-gke` | Terraform | gke | — |
| Subnet | `nacfson-gke-asia-northeast3`, Private Google Access on | Terraform | gke | — |
| GKE cluster | `nacfson-gke`, zonal `asia-northeast3-a` | Terraform | gke | `deletion_protection`, `prevent_destroy` |
| Node pool | `ondemand-core` | Terraform | gke | — |
| Node pool | `spot-controllers` | Terraform | gke | — |
| Service account | `gke-nodes` + `roles/container.defaultNodeServiceAccount` | Terraform | gke | — |
| KMS key ring | `nacfson-vault` | Terraform | gke | `prevent_destroy` (GCP never deletes rings) |
| KMS key | `openbao-unseal` | Terraform | gke | `prevent_destroy` |
| Service account | `openbao-kms` + key roles + WI binding `vault/openbao` | Terraform | gke | — |
| Static address | `gke-ingress-ip`, regional external IPv4, Premium tier | Terraform | gke | none: removed by US4 so it stops billing (SC-007). After cutover, plan review is the guard, because a destroy would change the IP that DNS points at. |
| Web firewall rule | `nacfson-gke-allow-web`: ingress TCP 80/443 from `0.0.0.0/0` to the reserved address, targets `gke-nodes` | Terraform | gke | none: removed by US4 with the address |
| L4 load balancer: forwarding rule, backend service, health check, health-check firewall rule | generated `k8s2-…` names | **GKE controller**, from `Service/platform/traefik` (`disable_l4_lb_firewall_reconciliation = true` stops GKE's own web rule) | — | follows the Service's lifecycle |
| Artifact Registry | `nacfson-public` + `allUsers` reader + cleanup rules | Terraform | gke | `prevent_destroy` |
| Workloads, Flux, Secrets | `clusters/gke/` | Flux / Spec 012 | — | — |
| DNS A records | Cloudflare | Manual (Spec 012) | — | — |
| Control-center VM, `cc-viewer` IAM | — | Private control-center repository | — | — |

## 3. Shared values (`infra/gcp/shared-values.json`)

This is the single source of the values that Terraform and `clusters/gke/` must agree on. Terraform reads it into `locals`, and the offline check compares it with the YAML sources ([contracts/shared-values.md](contracts/shared-values.md)).

| Field | Value | Validation rule |
|---|---|---|
| `project_id` | `nacfson-pipeline` | equals KMS `project` in `layers.yaml` and the GSA email domain |
| `region` | `asia-northeast3` | equals KMS `region`, `_GAR_REGION` |
| `zone` | `asia-northeast3-a` | starts with `region` |
| `cluster_name` | `nacfson-gke` | lowercase, at most 40 characters |
| `workload_pool` | `nacfson-pipeline.svc.id.goog` | equals `<project_id>.svc.id.goog` |
| `node_pools[]` | `name`, `machine_type`, `node_count`, `spot`, `taints[]` | must match `capacity.yaml` `pools[]`: name, machineType, nodeCount, provisioning (`spot` ⇔ `provisioning: spot`), taints |
| `kms.key_ring` / `kms.crypto_key` | `nacfson-vault` / `openbao-unseal` | equal the `seal "gcpckms"` block in `layers.yaml` |
| `kms.gsa_account_id` | `openbao-kms` | `<id>@<project_id>.iam.gserviceaccount.com` equals the `iam.gke.io/gcp-service-account` annotation |
| `kms.ksa` | `vault/openbao` | the ServiceAccount exists in `deploy/platform/vault/service-accounts.yaml` |
| `registry.repository_id` | `nacfson-public` | equals `_GAR_REPO` |
| `registry.cleanup` | from `gar-cleanup-policy.json` | ids, actions, `olderThan` and `keepCount` equal |
| `ingress.address_name` | `gke-ingress-ip` | equals the `networking.gke.io/load-balancer-ip-addresses` annotation on `Service/platform/traefik` |
| `network.*` | VPC, subnet, CIDRs | CIDRs are inside `10.0.0.0/8` and do not overlap |

## 4. Outputs

These form the operator-facing interface ([contracts/terraform-outputs.md](contracts/terraform-outputs.md)):

- `cluster_name`, `cluster_zone`, `cluster_endpoint`, `cluster_ca_certificate` (sensitive)
- `ingress_ip`, `ingress_address_name`
- `get_credentials_command`
- `kms_key_id`, `openbao_gsa_email`

## 5. Environment lifecycle (states)

```text
absent
  │ bootstrap apply
  ▼
state-ready ──────── project apply ───────▶ project-ready
                                              │ gke apply (US1)
                                              ▼
                                       ready-for-flux  ◀── checkpoint 1 (FR-020)
                                              │ Flux install (Spec 012)
                                              ▼
                                       ready-for-restore ◀── checkpoint 2 (FR-024)
                                              │ control-center restore + DNS switch (Spec 012)
                                              ▼
                                           serving
  removal (US4), allowed from ready-for-flux or ready-for-restore, never from serving:
    ready-for-* ──▶ removed (cluster, pools, load balancer, address gone; key ring, key,
                    registry and state kept because of prevent_destroy)
```

Transition rules:

- **Order.** `gke apply` is only allowed after Spec 012 Gates 1–3 have passed (FR-016).
- **Leaving `ready-for-flux`.** It requires the KMS key and Workload Identity binding to exist (FR-023).
- **Removal.** Removal from `serving` is out of scope. DNS must first point back away from GKE.
- **Second apply.** A second `gke apply` in any state reports zero changes when nothing was edited (SC-003).
