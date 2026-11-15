# Contract: shared values and the offline cross-check

**Covers**: FR-018, SC-002, User Story 3.

## File: `infra/gcp/shared-values.json`

```json
{
  "project_id": "nacfson-pipeline",
  "region": "asia-northeast3",
  "zone": "asia-northeast3-a",
  "cluster_name": "nacfson-gke",
  "workload_pool": "nacfson-pipeline.svc.id.goog",
  "network": {
    "vpc": "nacfson-gke",
    "subnet": "nacfson-gke-asia-northeast3",
    "nodes_cidr": "10.20.0.0/22",
    "pods_cidr": "10.24.0.0/17",
    "services_cidr": "10.28.0.0/22"
  },
  "node_pools": [
    {"name": "ondemand-core", "machine_type": "e2-medium", "node_count": 1, "spot": false, "taints": []},
    {"name": "spot-controllers", "machine_type": "e2-small", "node_count": 1, "spot": true,
     "taints": ["cloud.google.com/gke-spot=true:NoSchedule"]}
  ],
  "kms": {"key_ring": "nacfson-vault", "crypto_key": "openbao-unseal", "gsa_account_id": "openbao-kms", "ksa": "vault/openbao"},
  "registry": {"repository_id": "nacfson-public", "cleanup_source": "deploy/gcp/gar-cleanup-policy.json"},
  "ingress": {"address_name": "gke-ingress-ip"}
}
```

Terraform reads it with `local.shared = jsondecode(file("${path.module}/../shared-values.json"))`. No shared value may also be written as a literal in `.tf` files. The check fails if one is (rule 9 below).

## CLI: `scripts/check-gcp-shared-values.py`

```text
usage: check-gcp-shared-values.py [--repo-root PATH] [--json]
exit 0  all checks pass
exit 1  one or more mismatches (each printed as: CHECK <id> FAIL <field>: infra=<a> repo=<b> (<source file>))
exit 2  a source file is missing or unparseable
```

It runs offline, needs no cloud credentials, and uses only Python 3 and PyYAML (already used by `scripts/render-invariants.py`).

## Checks

| Id | Rule | Repo source |
|---|---|---|
| 1 | Pool set, names, `machineType`, `nodeCount` equal | `clusters/gke/capacity.yaml` `pools[]` |
| 2 | `spot` ⇔ `provisioning: spot`; taints equal (order-insensitive) | same |
| 3 | KMS `project`, `region`, `key_ring`, `crypto_key` equal | `clusters/gke/layers.yaml` (every `seal "gcpckms"` block) |
| 4 | `<gsa_account_id>@<project_id>.iam.gserviceaccount.com` equals the annotation on the `kms.ksa` ServiceAccount only (other Workload Identity users are ignored) | `clusters/gke/layers.yaml` (`iam.gke.io/gcp-service-account` patch on `vault/openbao`) |
| 5 | `ksa` namespace and name exist as a ServiceAccount | `deploy/platform/vault/service-accounts.yaml` |
| 6 | `region` = `_GAR_REGION`, `repository_id` = `_GAR_REPO` | `deploy/gcp/cloudbuild.yaml` |
| 7 | Registry cleanup rules in `registry.tf` are derived from `cleanup_source` (ids, actions, `olderThan`, `keepCount`) | `deploy/gcp/gar-cleanup-policy.json` |
| 8 | `ingress.address_name` equals the `networking.gke.io/load-balancer-ip-addresses` annotation, and `loadBalancerClass` is `networking.gke.io/l4-regional-external` | `deploy/platform/traefik/service.yaml` |
| 9 | No shared value appears as a whole string literal in `infra/gcp/**/*.tf` (exact match of the full literal; comments ignored; `backend.tf` exempt because backend blocks accept only literals) | `infra/gcp/` |
| 10 | Network CIDRs are inside `10.0.0.0/8` and do not overlap | `shared-values.json` |
| 11 | `zone` starts with `region`; `workload_pool` = `<project_id>.svc.id.goog` | `shared-values.json` |

## Tests

`tests/infra/test_check_gcp_shared_values.py` (unittest) covers:

- the real repository passes
- one fixture copy per check id with one value changed, which must fail with that id
- a missing source file, which must exit 2
