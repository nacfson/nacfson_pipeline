# Implementation Plan: GKE Infrastructure as Code (Terraform)

**Branch**: `015-gke-terraform-infra` | **Date**: 2026-11-14 | **Spec**: [spec.md](spec.md)

**Input**: Feature specification from `specs/015-gke-terraform-infra/spec.md`

## Summary

Put every Google Cloud resource the `clusters/gke/` overlay depends on into three Terraform roots under `infra/gcp/`: `bootstrap`, `project` and `gke`. The operator applies them from Cloud Shell, and CI validates them offline without credentials. Terraform stops at an empty cluster. Flux owns everything inside it, and the control center restores the Spec 012 bundle afterwards.

Web traffic enters through a single regional **L4 pass-through load balancer** that GKE creates from `Service/platform/traefik`. It uses an address reserved by Terraform and referenced **by name**. This replaces Spec 012's zero-LoadBalancer `hostPort` edge, so Render Invariant 10, preflight Rule 2 and Spec 012's placement contract §3 are updated. A dedicated VPC with Private Google Access gives OpenBao's `gcpckms` seal the `private.googleapis.com` path that `clusters/gke/layers.yaml` already expects. A shared-values JSON file plus an offline cross-check keeps Terraform and `clusters/gke/` from drifting apart.

## Technical Context

**Language/Version**:
- HCL with Terraform `>= 1.9.0, < 2.0.0` (exact version pinned in CI)
- Python 3.11+ for `scripts/check-gcp-shared-values.py` and tests
- YAML for Kustomize and Flux manifests

**Primary Dependencies**:
- `hashicorp/google` provider `~> 8.0`, with lock files committed for `linux_amd64` and `linux_arm64`
- PyYAML, already used by `scripts/render-invariants.py`
- `hashicorp/setup-terraform` in CI
- Cloud Shell (`gcloud`, `terraform`, `kubectl`)

**Storage**: Terraform state in GCS bucket `nacfson-pipeline-tfstate` (versioned, public access prevention), prefixes `project` and `gke`. `bootstrap/` uses local state that is never committed.

**Testing**:
- `terraform fmt -check`, `init -backend=false` and `validate` for each root
- `python3 -m unittest discover tests/infra` (new)
- `python3 -m unittest discover tests/preflight` (updated `test_30` plus new load balancer cases)
- `./scripts/render-validate.sh gke` (new Invariant 10)
- `python3 scripts/check-arch-doc-sync.py --worktree`
- Live scenarios in [quickstart.md](quickstart.md), run only after Spec 012 Gates 1–3

**Target Platform**: GCP project `nacfson-pipeline`, region `asia-northeast3`, zonal GKE Standard cluster in `asia-northeast3-a` on the `REGULAR` channel, with a daily 18:00–22:00 UTC maintenance window

**Project Type**: Infrastructure as code (Terraform roots) plus GitOps manifest and preflight governance changes

**Performance Goals**:
- operator reaches "ready for Flux" in under 60 minutes (SC-001)
- "ready for restore" within 30 minutes of `flux bootstrap` (SC-008)
- CI validation in under 5 minutes (SC-004)

**Constraints**:
- no cloud resource created before Spec 012 Gates 1–3 pass (FR-016)
- CI holds no Google Cloud credentials (FR-014)
- every resource has a single owner (FR-002)
- steady-state cost at most $80/month (SC-006)
- TLS stays at Traefik (Constitution, Platform-Owned Edge TLS)
- no state, plan or credentials in Git (FR-011, FR-019)

**Scale/Scope**:
- 3 Terraform roots, about 25 Google resources
- 1 shared-values file and 1 check script with tests
- Traefik Service and Deployment changes (GKE-only layer)
- Invariant 10 and preflight Rule 2 updates
- 1 CI job, 1 runbook, Spec 012 doc updates

All unknowns are resolved in [research.md](research.md) (R1–R13). No `NEEDS CLARIFICATION` remains.

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-checked after Phase 1 design (below).*

Evaluated against `.specify/memory/constitution.md` v2.3.1.

| Principle / Gate | Assessment | Result |
|---|---|---|
| **I. Declarative GitOps** | Cloud resources around the cluster are reviewed through PRs and applied from a saved plan (FR-013). Everything in the cluster, including the Traefik `LoadBalancer` Service, stays Flux-reconciled. `flux bootstrap` is the one-time step Spec 012 already defines. Terraform never runs `kubectl apply`. | PASS |
| **II. Workload Isolation & Restricted PSS** | The only `LoadBalancer` Service is `platform/traefik`. The preflight rule rejecting `NodePort` and `LoadBalancer` in project namespaces is unchanged, and new tests pin that. Removing Traefik's `hostPort` reduces host-level exposure. | PASS |
| **III. Defense-in-Depth Identity** | The load balancer forwards only TCP 80/443 to Traefik, whose routing allowlist (no `/admin/`, no management ports) is unchanged. The cluster API is public but needs Google IAM and then RBAC (FR-025, a user decision). This is "private, authenticated cluster access" as defined by Constitution v2.3.1 (Clarification 2.3.1, finding D1): never through public ingress, one strongly authenticated operator, then RBAC. The operator's Google account uses 2-step verification (spec Assumptions). | PASS |
| **IV. Bounded Resources & 1/n** | No new pods: the load balancer is a cloud resource. Traefik's requests and limits and the `Recreate` strategy are unchanged, so the platform reservation and 1/n slice in `clusters/gke/capacity.yaml` are unchanged. The node surge fits the 6-vCPU quota (R6). | PASS |
| **V. Zero Plaintext Secrets / Cloud KMS exception** | KMS is accessed only through Workload Identity (`openbao-kms` ↔ `vault/openbao`), with no key file. State, plans and `terraform.tfvars` are gitignored. The billing account ID is passed through an environment variable. The CA certificate output is marked sensitive. | PASS |
| **VI. Portability** | The Traefik layer is GKE-only, and project manifests are untouched. Cloud specifics live in `infra/gcp/` and `clusters/gke/`, which are environment configuration. | PASS |
| **VII. Lifecycle signals** | Not affected | N/A |
| **Capacity & Budget Verification** | `platform-preflight` is still the required check. The only change is the narrowed load balancer allowance. | PASS |
| **Automated Manifest Validation (pinned tools)** | The Terraform version and provider lock files are pinned, and `render-validate.sh gke` keeps running. | PASS |
| **Repository Secret Hygiene** | Gitleaks covers `infra/gcp/`, `.gitignore` excludes state, plan and tfvars, and CI has no credentials. | PASS |

**Post-design re-check (after Phase 1):** PASS. Phase 1 added one design element: the dedicated VPC (R13). It changes no principle. It narrows exposure (no default SSH, RDP or ICMP rules), and its CIDRs sit inside `10.0.0.0/8`, which the existing NetworkPolicies already treat as private.

## Cross-Spec Changes (not constitutional violations)

| Change | Why | Where recorded |
|---|---|---|
| Spec 012's "Zero GCP LoadBalancer" edge replaced by a single L4 pass-through load balancer | User clarification on 2026-11-14 (FR-007) | [contracts/traefik-l4-edge.md](contracts/traefik-l4-edge.md); Spec 012 `plan.md` Constraints, `quickstart.md` Gate 4 steps 2 and 4, `contracts/gke-capacity-and-placement.md` §3; `docs/platform-architecture.html` (FR-022) |
| Spec 012 Gate 4 step 2 one-off `gcloud` creation replaced by this runbook | FR-022 | Spec 012 `quickstart.md` |

## Project Structure

### Documentation (this feature)

```text
specs/015-gke-terraform-infra/
├── spec.md
├── plan.md              # this file
├── research.md          # R1–R13
├── data-model.md        # resource ownership, shared values, lifecycle states
├── quickstart.md        # validation scenarios 1–7
├── contracts/
│   ├── terraform-outputs.md
│   ├── shared-values.md
│   └── traefik-l4-edge.md
├── checklists/requirements.md
└── tasks.md             # created by /speckit-tasks
```

### Source Code (repository root)

```text
infra/gcp/
├── shared-values.json            # single source for values shared with clusters/gke/
├── bootstrap/
│   ├── versions.tf               # terraform + google provider pins
│   ├── main.tf                   # state bucket (versioned, PAP enforced, prevent_destroy)
│   ├── outputs.tf                # state_bucket
│   └── .terraform.lock.hcl
├── project/
│   ├── versions.tf  backend.tf   # gcs prefix "project"
│   ├── apis.tf                   # project services, disable_on_destroy = false
│   ├── budget.tf                 # $80 budget (billing account via TF_VAR_billing_account)
│   ├── variables.tf  outputs.tf
│   └── .terraform.lock.hcl
└── gke/
    ├── versions.tf  backend.tf   # gcs prefix "gke"
    ├── locals.tf                 # jsondecode(shared-values.json)
    ├── network.tf                # VPC nacfson-gke + subnet (Private Google Access, secondary ranges)
    ├── cluster.tf                # zonal cluster, DPv2, WI, REGULAR, maintenance window, protections
    ├── nodepools.tf              # gke-nodes SA, ondemand-core, spot-controllers (+ taint)
    ├── ingress.tf                # reserved address gke-ingress-ip + web firewall rule (TCP 80/443 to it)
    ├── kms.tf                    # key ring, key, openbao-kms SA, key IAM, WI binding
    ├── registry.tf               # nacfson-public, cleanup from gar-cleanup-policy.json, allUsers reader
    ├── outputs.tf                # per contracts/terraform-outputs.md
    └── .terraform.lock.hcl

deploy/platform/traefik/
├── service.yaml                  # → type LoadBalancer, l4-regional-external, ETP Local, address-name annotation
└── deployment.yaml               # → hostPort removed

scripts/
├── check-gcp-shared-values.py    # new (contracts/shared-values.md)
├── render-invariants.py          # Invariant 10 rewritten (contracts/traefik-l4-edge.md §3)
├── platform-preflight.py         # Rule 2: allow exactly the Traefik L4 Service on gke
└── gke_edge.py                   # new: shared edge rules for preflight, invariants and the check

tests/
├── infra/test_check_gcp_shared_values.py   # new
└── preflight/test_platform_preflight.py    # test_30 updated + new LB cases

.github/workflows/lint.yaml       # new job terraform-validate (no credentials)
.gitignore                        # *.tfstate*, *.tfplan, .terraform/, terraform.tfvars
docs/runbooks/gke-terraform.md    # beginner runbook (FR-020/021/024), checkpoints "ready for Flux" / "ready for restore"
docs/platform-architecture.html   # Spec 015 entry + Spec 012 zero-LB notes updated
specs/012-gke-platform-migration/{plan.md,quickstart.md,contracts/gke-capacity-and-placement.md}  # FR-022
```

**Structure Decision**: Terraform lives in a new top-level `infra/gcp/`, split into three roots with separate state for blast-radius isolation (R3). Manifest and governance changes stay in their existing locations. The runbook goes under `docs/runbooks/` next to the existing operations docs.

## Implementation Order (input for `/speckit-tasks`)

1. **Offline foundation (US3):** `.gitignore`, `shared-values.json`, the check script and its tests, the CI job. Mergeable now.
2. **Terraform roots (US1, US2):** `bootstrap` → `project` → `gke`, each with `fmt` and `validate` passing and lock files committed.
3. **Edge change (FR-007/007a):** Traefik Service and Deployment, Invariant 10, preflight Rule 2 and tests, Spec 012 contract §3. `render-validate.sh gke` and the preflight tests must pass.
4. **Docs (FR-020–024):** runbook with both checkpoints and the removal procedure; Spec 012 `plan.md` and `quickstart.md` updates; architecture handbook.
5. **Live validation (after Gates 1–3 only):** quickstart Scenarios 2–7.

## Complexity Tracking

No Constitution violations. See Cross-Spec Changes above for the Spec 012 contract reversal.
