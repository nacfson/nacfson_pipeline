# Contract: `infra/gcp/gke` outputs

**Consumers**: the runbook (Cloud Shell), the operator building the control center's read-only `gke.yaml` and temporary admin kubeconfig, and the Cloudflare DNS switch in Spec 012.

`terraform output` in `infra/gcp/gke/` MUST provide these outputs. Names are stable. Renaming one is a breaking change to this contract.

| Output | Type | Sensitive | Example | Used for |
|---|---|---|---|---|
| `cluster_name` | string | no | `nacfson-gke` | `get-credentials`, control-center config |
| `cluster_zone` | string | no | `asia-northeast3-a` | `get-credentials` |
| `cluster_endpoint` | string | no | `34.64.x.y` | `server:` in kubeconfigs |
| `cluster_ca_certificate` | string (base64) | **yes** | — | `certificate-authority-data:` in kubeconfigs |
| `get_credentials_command` | string | no | `gcloud container clusters get-credentials nacfson-gke --zone asia-northeast3-a --project nacfson-pipeline` | first `kubectl` setup (FR-010) |
| `ingress_address_name` | string | no | `gke-ingress-ip` | must equal the Traefik Service annotation |
| `ingress_ip` | string | no | `34.64.a.b` | Cloudflare A records at cutover, `curl --resolve` smoke tests |
| `kms_key_id` | string | no | `projects/nacfson-pipeline/locations/asia-northeast3/keyRings/nacfson-vault/cryptoKeys/openbao-unseal` | "ready for restore" check |
| `openbao_gsa_email` | string | no | `openbao-kms@nacfson-pipeline.iam.gserviceaccount.com` | must equal the annotation in `clusters/gke/layers.yaml` |

## Rules

- **Nothing secret in outputs.** No output contains a credential or token. `cluster_ca_certificate` is public-key material, but it is marked sensitive so it isn't printed by default. Read it with `terraform output -raw cluster_ca_certificate`.
- **`project/` outputs only `budget_name`.**
- **`bootstrap/` outputs only `state_bucket`.**
