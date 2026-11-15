# Tasks: GKE Infrastructure as Code (Terraform)

**Input**: Design documents from `specs/015-gke-terraform-infra/`
**Prerequisites**: [plan.md](plan.md), [spec.md](spec.md), [research.md](research.md), [data-model.md](data-model.md), [contracts/](contracts/), [quickstart.md](quickstart.md)

**Tests**: Included. The spec requires an offline cross-check with failing cases (FR-018, US3), and the plan requires updated preflight and invariant tests for the load balancer change (contracts/traefik-l4-edge.md §4).

**Cloud rule (FR-016)**: no task before Phase 9 touches Google Cloud. Phase 9 tasks are marked **GATED**. Run them only after Spec 012 Gates 1–3 have passed, by the operator, from Cloud Shell.

**Tooling note**: Terraform is not installed on the dev machine. Before T007, install the exact version from `infra/gcp/.terraform-version` into `~/.local/bin` (official HashiCorp zip, SHA256 verified), or run it through the `hashicorp/terraform:<version>` container. No cloud credentials are needed for any task before Phase 9.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: can run in parallel (different files, no dependency on an incomplete task)
- **[Story]**: US1–US5 from spec.md

---

## Phase 1: Setup

**Purpose**: repository scaffolding and Git hygiene for Terraform.

- [X] T001 Create directories `infra/gcp/bootstrap/`, `infra/gcp/project/`, `infra/gcp/gke/`, `docs/runbooks/` and `tests/infra/` (add an empty `tests/infra/__init__.py` only if `tests/preflight/` has one; match the existing convention)
- [X] T002 [P] Append Terraform ignores to `.gitignore`: `*.tfstate`, `*.tfstate.*`, `*.tfplan`, `**/.terraform/`, `.terraform.tfstate.lock.info`, `terraform.tfvars`, `*.auto.tfvars`, `crash.log`. Do NOT ignore `.terraform.lock.hcl` or `infra/gcp/shared-values.json`.
- [X] T003 [P] Create `infra/gcp/.terraform-version` containing one exact Terraform version: the latest stable 1.x release at implementation time, which must satisfy `>= 1.9.0, < 2.0.0` (research R2)

---

## Phase 2: Foundational (blocks all user stories)

**Purpose**: the shared-values source and provider pins that every root reads.

- [X] T004 Create `infra/gcp/shared-values.json` with exactly the content in `contracts/shared-values.md` § File:
  - project `nacfson-pipeline`, region `asia-northeast3`, zone `asia-northeast3-a`, cluster `nacfson-gke`, workload pool `nacfson-pipeline.svc.id.goog`
  - network `nacfson-gke` / `nacfson-gke-asia-northeast3` with `10.20.0.0/22`, `10.24.0.0/17`, `10.28.0.0/22`
  - pools `ondemand-core` (e2-standard-2, 1, spot false, no taints) and `spot-controllers` (e2-small, 1, spot true, taint `cloud.google.com/gke-spot=true:NoSchedule`)
  - kms `nacfson-vault` / `openbao-unseal` / `openbao-kms` / `vault/openbao`
  - registry `nacfson-public` with `cleanup_source` `deploy/gcp/gar-cleanup-policy.json`
  - ingress `gke-ingress-ip`
- [X] T005 [P] Create `infra/gcp/bootstrap/versions.tf`: `required_version = ">= 1.9.0, < 2.0.0"`; `hashicorp/google` `~> 8.0` (first confirm on registry.terraform.io that 8.x is the latest stable major; if not, use the latest stable major and record it in `research.md` R2); a `provider "google"` with `project` and `region` from `local.shared = jsondecode(file("${path.module}/../shared-values.json"))` in the same file
- [X] T006 [P] Create `infra/gcp/project/versions.tf` (same pins and provider as T005, plus its own `local.shared`) and `infra/gcp/project/backend.tf` (`backend "gcs" { bucket = "nacfson-pipeline-tfstate" prefix = "project" }`)
- [X] T007 [P] Create `infra/gcp/gke/versions.tf` (same pins), `infra/gcp/gke/backend.tf` (`bucket = "nacfson-pipeline-tfstate"`, `prefix = "gke"`) and `infra/gcp/gke/locals.tf` (`local.shared = jsondecode(file("${path.module}/../shared-values.json"))`, plus convenience locals for the `ondemand-core` and `spot-controllers` pool objects found by name). No shared value is written as a literal anywhere in `infra/gcp/**/*.tf` (contracts/shared-values.md check 9).

**Checkpoint**: `terraform -chdir=infra/gcp/<root> init -backend=false` succeeds for all three roots.

---

## Phase 3: User Story 1 — Create the GKE environment from the repository (P1) 🎯 MVP

**Goal**: three roots that, applied in order, create every cloud resource in `data-model.md` §2 owned by Terraform, plus the runbook up to "ready for Flux".

**Independent test**: quickstart Scenario 1 (offline: `fmt`, `validate` and lock files pass for all roots). Live: quickstart Scenario 2 (GATED, T041).

- [X] T008 [P] [US1] Create `infra/gcp/bootstrap/main.tf` and `outputs.tf`:
  - `google_storage_bucket` `nacfson-pipeline-tfstate` in `local.shared.region`
  - `uniform_bucket_level_access = true`, `public_access_prevention = "enforced"`, `versioning { enabled = true }`
  - lifecycle rule deleting noncurrent versions beyond 10 (`num_newer_versions = 10`, `with_state = "ARCHIVED"`), `lifecycle { prevent_destroy = true }`
  - output `state_bucket`
- [X] T009 [P] [US1] Create `infra/gcp/project/apis.tf`: `google_project_service` with `for_each` over `container`, `compute`, `cloudkms`, `artifactregistry`, `cloudbuild`, `iam`, `iamcredentials`, `billingbudgets` and `cloudresourcemanager` (`.googleapis.com`), each with `disable_on_destroy = false` (FR-004)
- [X] T010 [P] [US1] Create the project budget files:
  - `infra/gcp/project/variables.tf`: `variable "billing_account"`, type string, no default. Its description says to set it with `TF_VAR_billing_account` in Cloud Shell and never commit it.
  - `infra/gcp/project/budget.tf`: `google_billing_budget` scoped to `projects/<project number>` (from the `google_project` data source), amount 80 USD, `threshold_rules` at 0.5, 0.9 and 1.0 of `CURRENT_SPEND`, depending on the billingbudgets service (FR-015, R9)
  - `infra/gcp/project/outputs.tf`: output `budget_name`
- [X] T011 [P] [US1] Create `infra/gcp/gke/network.tf` (R13):
  - `google_compute_network` `local.shared.network.vpc` with `auto_create_subnetworks = false`
  - `google_compute_subnetwork` `local.shared.network.subnet` in `region`, with `ip_cidr_range = nodes_cidr`, `private_ip_google_access = true`, and `secondary_ip_range` entries `pods` (`pods_cidr`) and `services` (`services_cidr`)
  - no `google_compute_firewall` resources
- [X] T012 [US1] Create `infra/gcp/gke/cluster.tf` (R5, depends on T011): `google_container_cluster` with
  - `name = local.shared.cluster_name`, `location = local.shared.zone`
  - `network`, `subnetwork`, and `ip_allocation_policy` using the `pods` and `services` secondary range names
  - `remove_default_node_pool = true`, `initial_node_count = 1`
  - `deletion_protection = true` and `lifecycle { prevent_destroy = true }`
  - `datapath_provider = "ADVANCED_DATAPATH"`, `logging_service = "none"`, `monitoring_service = "none"`
  - `workload_identity_config { workload_pool = local.shared.workload_pool }`
  - `release_channel { channel = "REGULAR" }`
  - `maintenance_policy { recurring_window { start_time = "2026-01-01T18:00:00Z", end_time = "2026-01-01T22:00:00Z", recurrence = "FREQ=DAILY" } }`
  - public IP control-plane endpoint, no `master_authorized_networks_config` (FR-025), with a comment pointing to research R5 for the later DNS-only switch
- [X] T013 [US1] Create `infra/gcp/gke/nodepools.tf` (R6, depends on T012):
  - `google_service_account` `gke-nodes`, plus `google_project_iam_member` `roles/container.defaultNodeServiceAccount`
  - `google_container_node_pool` for each pool in `local.shared.node_pools`: `node_count`, `machine_type`, `spot`
  - `disk_size_gb = 30`, `disk_type = "pd-balanced"`, `service_account = gke-nodes`, `oauth_scopes = ["https://www.googleapis.com/auth/cloud-platform"]`
  - `workload_metadata_config { mode = "GKE_METADATA" }`
  - `management { auto_repair = true, auto_upgrade = true }`, `upgrade_settings { strategy = "SURGE", max_surge = 1, max_unavailable = 0 }`
  - a `dynamic "taint"` built from each pool's `taints` strings (`key=value:NoSchedule` → `key`, `value`, `effect = "NO_SCHEDULE"`)
- [X] T014 [P] [US1] Create `infra/gcp/gke/ingress.tf`: `google_compute_address` named `local.shared.ingress.address_name`, `region = local.shared.region`, `address_type = "EXTERNAL"`, `network_tier = "PREMIUM"`, with **no** `prevent_destroy` (data-model §2: removed by US4 so it stops billing)
- [X] T015 [P] [US1] Create `infra/gcp/gke/kms.tf` (R7):
  - `google_kms_key_ring` `local.shared.kms.key_ring` and `google_kms_crypto_key` `local.shared.kms.crypto_key` (`purpose = "ENCRYPT_DECRYPT"`, no `rotation_period`), both with `prevent_destroy = true`
  - `google_service_account` `local.shared.kms.gsa_account_id`
  - `google_kms_crypto_key_iam_member` for `roles/cloudkms.cryptoKeyEncrypterDecrypter` and `roles/cloudkms.viewer`
  - `google_service_account_iam_member` `roles/iam.workloadIdentityUser` for `serviceAccount:${local.shared.workload_pool}[vault/openbao]`, built from `local.shared.kms.ksa`
- [X] T016 [P] [US1] Create `infra/gcp/gke/registry.tf` (R8):
  - `google_artifact_registry_repository` `local.shared.registry.repository_id`, format DOCKER, in `region`, `prevent_destroy = true`
  - `cleanup_policies` produced by `dynamic` blocks from `jsondecode(file("${path.module}/../../../${local.shared.registry.cleanup_source}"))`: map `name` → `id`; action `Delete`/`Keep` → `DELETE`/`KEEP`; `condition.tagState` → `tag_state`; `condition.olderThan` → `older_than`; `mostRecentVersions.keepCount` → `most_recent_versions.keep_count`
  - `google_artifact_registry_repository_iam_member` `roles/artifactregistry.reader` for `allUsers`
- [X] T017 [US1] Create `infra/gcp/gke/outputs.tf` exactly per `contracts/terraform-outputs.md`: `cluster_name`, `cluster_zone`, `cluster_endpoint`, `cluster_ca_certificate` (`sensitive = true`), `get_credentials_command` (`gcloud container clusters get-credentials <name> --zone <zone> --project <project>`), `ingress_address_name`, `ingress_ip`, `kms_key_id`, `openbao_gsa_email` (depends on T012–T016)
- [X] T018 [US1] In each root, run `terraform fmt -recursive`, `terraform init -backend=false` and `terraform validate`, then `terraform providers lock -platform=linux_amd64 -platform=linux_arm64`. Commit `infra/gcp/{bootstrap,project,gke}/.terraform.lock.hcl`, and fix any validate errors in the files above.
- [X] T019 [US1] Write `docs/runbooks/gke-terraform.md`, part 1 (FR-020, FR-021). For every step, give the exact command, what success looks like, and what to do on the most likely failure.
  - **Prerequisites:** Spec 012 Gates 1–3 passed; billing enabled; open Cloud Shell; `terraform version` must match `infra/gcp/.terraform-version`, otherwise install it to `~/bin`.
  - **Quota preflight (R12.1):** `CPUS` headroom of at least 6 in `asia-northeast3`.
  - **Steps:** bootstrap apply; project apply with `TF_VAR_billing_account`; manual removal or raise of the control center's $1 budget; gke `plan -out=gke.tfplan`, then read it (additions only), then `apply gke.tfplan`.
  - **Checkpoint "ready for Flux":** the commands from quickstart Scenario 2, plus the Spec 012 `kubectl get endpoints kubernetes -n default` check and the `/32` `openbao-ingress-policy` instruction if the address is public.
  - **Glossary:** a plain-language entry for plan, apply, state and output.

**Checkpoint**: Scenario 1 passes. The MVP is complete offline: everything needed to create the environment exists and validates.

---

## Phase 4: User Story 2 — Review every change before it happens (P1)

**Goal**: CI validates every PR without credentials; protected resources refuse destruction; the runbook teaches the plan-then-apply habit and drift detection.

**Independent test**: open a PR that changes a `.tf` file and confirm the `terraform-validate` job reports a result within 5 minutes (SC-004). Run `python3 -m unittest tests/infra/test_terraform_protections.py`.

- [X] T020 [P] [US2] Write `tests/infra/test_terraform_protections.py` (unittest, stdlib only, text and regex over `.tf` files) asserting:
  - the cluster has `deletion_protection = true` and `prevent_destroy = true`
  - `prevent_destroy = true` on the state bucket, key ring, crypto key and registry
  - **no** `prevent_destroy` on `google_compute_address`
  - `disable_on_destroy = false` on project services
  - no `google_compute_firewall` resource in `infra/gcp/gke/`
  - no `master_authorized_networks_config` (FR-025)
- [X] T021 [US2] Add job `terraform-validate` to `.github/workflows/lint.yaml` (R10):
  - steps: checkout; `hashicorp/setup-terraform` pinned to a release tag, with `terraform_version` read from `infra/gcp/.terraform-version` and `terraform_wrapper: false`; then, for each of `bootstrap`, `project` and `gke`, `fmt -check -recursive`, `init -backend=false -lockfile=readonly` and `validate`
  - **no** `google-github-actions/auth` step and no secrets referenced
- [X] T022 [US2] Add `python3 -m unittest discover tests/infra` next to the existing `python3 -m unittest discover tests/preflight` step in `.github/workflows/gitops.yaml`
- [X] T023 [US2] Write `docs/runbooks/gke-terraform.md`, part 2, "Changing infrastructure":
  - **Flow:** edit `.tf` → PR (CI validates) → merge → Cloud Shell `git pull` → `plan -out` → read → `apply <saved plan>` (FR-013)
  - **Stop rule:** stop if the plan destroys or replaces the cluster, key ring, key or registry (US2 scenario 2)
  - **Drift check:** `plan -detailed-exitcode` (0 = no changes, 2 = drift)
  - **Recovering a broken state:** from bucket object versions

**Checkpoint**: US1 and US2 together satisfy SC-003 and SC-004 offline.

---

## Phase 5: User Story 5 — GKE is ready to receive the cutover bundle (P2)

**Goal**: traffic enters through the single L4 pass-through load balancer (FR-007/007a); render and preflight governance updated; runbook checkpoint "ready for restore" (FR-023/024/025).

**Independent test**: `./scripts/render-validate.sh gke` passes with the new Invariant 10, and `python3 -m unittest discover tests/preflight` passes, including the new load balancer cases. Live: quickstart Scenario 4 (GATED, T043).

*Ordered before US3 because the shared-values check 8 reads the Traefik Service annotation added here.*

- [X] T024 [P] [US5] Update `tests/preflight/test_platform_preflight.py`:
  - rewrite `test_30_ondemand_backup_headroom_and_loadbalancer_rejected_on_gke` so a plain project `LoadBalancer` is still rejected
  - add cases from `contracts/traefik-l4-edge.md` §4:
    1. the exact allowed `platform/traefik` shape passes
    2. a second `LoadBalancer` in `platform` is rejected
    3. Traefik with `loadBalancerClass` missing or different is rejected
    4. Traefik with `externalTrafficPolicy: Cluster` is rejected
    5. Traefik without the `networking.gke.io/load-balancer-ip-addresses` annotation is rejected

  These tests are written first and must fail before T028.
- [X] T025 [P] [US5] Create `tests/preflight/test_render_invariants_gke_edge.py`. Load `scripts/render-invariants.py` with `importlib` the same way `test_platform_preflight.py` loads `platform-preflight.py`, and feed synthetic rendered docs that cover each rule in `contracts/traefik-l4-edge.md` §3:
  - exactly one `LoadBalancer`, which is `platform/traefik`, with the class, policy and annotation
  - Traefik has no `hostPort`
  - no `NodePort`

  These tests must fail before T027.
- [X] T026 [US5] Edit `deploy/platform/traefik/service.yaml` to the exact spec in `contracts/traefik-l4-edge.md` §1:
  - annotation `networking.gke.io/load-balancer-ip-addresses: gke-ingress-ip`, `type: LoadBalancer`, `loadBalancerClass: networking.gke.io/l4-regional-external`, `externalTrafficPolicy: Local`
  - ports unchanged
  - replace the "ClusterIP only … zero GCP LoadBalancer" comment with one pointing to the Spec 015 contract

  In the same change, edit `deploy/platform/traefik/deployment.yaml`: remove `hostPort: 80` and `hostPort: 443`, keep `strategy: Recreate`, and update its comment to "Recreate keeps rollouts within the 1/n reservation (no surge)". Keep the `nodeSelector`, probes and resources unchanged.
- [X] T027 [US5] Rewrite Invariant 10 in `scripts/render-invariants.py` (currently around lines 266–300) to `contracts/traefik-l4-edge.md` §3 and update its comment reference. Make T025 pass.
- [X] T028 [US5] Update `scripts/platform-preflight.py` Rule 2 for gke (currently around lines 1513–1527). Reject every `LoadBalancer` Service except one that is exactly `platform/traefik` with the §1 class, policy and non-empty annotation, and update the `detail` text. Leave the project-namespace `NodePort`/`LoadBalancer` rejection (around line 1579) unchanged. Make T024 pass.
- [X] T029 [US5] Run `./scripts/render-validate.sh gke`, `./scripts/render-validate.sh local-k3s` and `./scripts/render-validate.sh vps-k3s`. Confirm gke passes and the other two are unchanged (the Traefik layer is GKE-only). Also confirm with `grep` that no `NetworkPolicy` in `deploy/` or `clusters/gke/` selects `app.kubernetes.io/name: traefik` in `platform` and would drop traffic from internet source IPs.
- [X] T030 [US5] Rewrite `specs/012-gke-platform-migration/contracts/gke-capacity-and-placement.md` §3, "Zero-LoadBalancer Edge Ingress Contract". Replace it with a short "Single L4 pass-through LoadBalancer" section that states the change and links to `specs/015-gke-terraform-infra/contracts/traefik-l4-edge.md` (FR-022).
- [X] T031 [US5] Write `docs/runbooks/gke-terraform.md`, part 3, "Hand-off to Flux and the restore" (FR-023, FR-024, FR-025):
  - **Ordering:** Terraform apply (KMS key and WI binding exist) **before** Flux is installed with `kubectl apply --server-side -k clusters/gke/flux-system` (no GitHub token; `flux bootstrap github` would store a repository key in the cluster, which Principle I forbids).
  - **Checkpoint "ready for restore":** the commands and expected results from quickstart Scenario 4, including Traefik `EXTERNAL-IP` = `terraform output ingress_ip` and the `curl --resolve` check.
  - **Control-center kubeconfigs:** how to build the read-only `gke.yaml` (server and CA from outputs, with a `user: {}` placeholder) and the temporary inline admin kubeconfig (endpoint + CA + `gcloud auth print-access-token`).
  - **Hand-off:** a pointer to the Spec 012 restore, run from the control center with a fresh export.

**Checkpoint**: the edge change is governed and documented. US5 is complete offline.

---

## Phase 6: User Story 3 — Prepare and validate before any cloud resource exists (P2)

**Goal**: the offline cross-check (FR-018) and its tests, wired into CI and render-validate.

**Independent test**: quickstart Scenario 1, including its negative check (changing `crypto_key` must give `CHECK 3 FAIL kms.crypto_key`, exit 1).

- [X] T032 [P] [US3] Write `tests/infra/test_check_gcp_shared_values.py` (unittest), per `contracts/shared-values.md` § Tests:
  1. the real repository passes (exit 0)
  2. for each check id 1–11, copy the relevant sources into a temp dir, change one value, run with `--repo-root <tmp>` and assert exit 1 with `CHECK <id> FAIL` in the output
  3. a missing source file gives exit 2

  These tests must fail before T033.
- [X] T033 [US3] Implement `scripts/check-gcp-shared-values.py` (Python 3 plus PyYAML, executable, with a `#!/usr/bin/env python3` shebang) exactly per `contracts/shared-values.md`:
  - CLI `[--repo-root PATH] [--json]`, exit codes 0/1/2
  - output lines of the form `CHECK <id> FAIL <field>: infra=<a> repo=<b> (<source file>)`
  - checks 1–11 against `clusters/gke/capacity.yaml`, `clusters/gke/layers.yaml` (every `seal "gcpckms"` block and the `iam.gke.io/gcp-service-account` annotation), `deploy/platform/vault/service-accounts.yaml`, `deploy/gcp/cloudbuild.yaml`, `deploy/gcp/gar-cleanup-policy.json`, `deploy/platform/traefik/service.yaml` and `infra/gcp/**/*.tf` (literal scan, check 9)

  Make T032 pass.
- [X] T034 [US3] Call `python3 "${REPO_ROOT}/scripts/check-gcp-shared-values.py"` from `scripts/render-validate.sh` when the environment is `gke`, failing the render on a non-zero exit. Also add a `python3 scripts/check-gcp-shared-values.py` step to the `terraform-validate` job in `.github/workflows/lint.yaml`, after `validate`.
  *Changed after code review (finding 9): the check runs once per PR, in the `terraform-validate` CI job and in `tests/infra`. It was removed from `render-validate.sh`, because it reads repository files, not the rendered output.*

**Checkpoint**: SC-002 is enforced on every PR and every gke render.

---

## Phase 7: User Story 4 — Remove the environment safely (P3)

**Goal**: a documented, rehearsable removal that leaves no billable compute, load balancer, address or firewall resources, and keeps the key, registry and state (SC-007).

**Independent test**: read-through against quickstart Scenario 6. Live rehearsal: GATED, T044.

- [X] T035 [US4] Write `docs/runbooks/gke-terraform.md`, part 4, "Removing the environment (before the DNS switch only)":
  1. Check that DNS still points at vps-k3s, and stop if it doesn't.
  2. In a reviewed PR, set `deletion_protection = false` and remove the cluster's `prevent_destroy`.
  3. `flux suspend` the traefik layer and `kubectl delete svc -n platform traefik`, so GKE deletes its load balancer parts. Confirm with `gcloud compute forwarding-rules list`.
  4. Run `terraform -chdir=infra/gcp/gke destroy` with `-target` for the node pools, cluster, address, network/subnet and `gke-nodes`. List the exact resource addresses from T011–T014.
  5. Verify with `gcloud container clusters list`, `gcloud compute addresses list`, `gcloud compute forwarding-rules list` and `gcloud compute firewall-rules list --filter=network:nacfson-gke`.
  6. Explain that the key ring, key, registry and state stay by design, and how to recreate the environment with the same names.
- [X] T036 [US4] Extend `tests/infra/test_terraform_protections.py` (from T020) with an assertion that every resource listed for `-target` in T035 exists in `infra/gcp/gke/*.tf` under that exact address, so the runbook and the code cannot drift apart

---

## Phase 8: Polish & cross-cutting (offline)

- [X] T037 [P] Update Spec 012 docs (FR-022):
  - `specs/012-gke-platform-migration/plan.md`: the Summary bullet "Zero-LB Edge Ingress" and the Constraints line "Zero GCP LoadBalancer" now describe the single L4 pass-through load balancer and link to Spec 015
  - `specs/012-gke-platform-migration/quickstart.md`: Gate 4 step 2 now says "Provision with `docs/runbooks/gke-terraform.md` (Spec 015)", replacing the `gcloud` flags; step 4 now says to flip the DNS A records to `terraform output ingress_ip` (the load balancer address), instead of "Node 1's static IPv4"
- [X] T038 [P] Update `docs/operations-runbook.md`: line ~173 ("zero-`LoadBalancer` invariants" → "single L4 load balancer invariant"), and lines ~398–405 (manual GAR creation, static IP assignment and firewall rule → point to `docs/runbooks/gke-terraform.md`)
- [X] T039 Update `docs/platform-architecture.html` wherever it states zero GCP LoadBalancer or Traefik hostPort on GKE. Today that is around lines 68, 289–290, 856, 1134, 1275–1284, 1769, 1802, 2185–2190, 2330 and 2878–2880. Each should describe the L4 pass-through load balancer → Traefik on `ondemand-core` (no hostPort, TLS at Traefik, about $18/month). Mark the Spec 015 entry (around line 1141) as "Planned". Run `python3 scripts/check-arch-doc-sync.py --worktree`.
- [X] T040 Run the full CLAUDE.md verification suite and record the results in the PR description:
  - `(cd gateway && go test ./... && go vet ./...)`
  - `(cd services/vault-proxies && go test ./... && go vet ./...)`
  - `python3 -m unittest discover tests/preflight`
  - `python3 -m unittest discover tests/infra`
  - `./scripts/render-validate.sh local-k3s`, `vps-k3s` and `gke`
  - `python3 scripts/check-arch-doc-sync.py --worktree`
  - quickstart Scenario 1
  - `git status` shows no state, plan or tfvars files (Scenario 7)

---

## Phase 9: Live validation (GATED: only after Spec 012 Gates 1–3, by the operator in Cloud Shell)

- [ ] T041 GATED. Run `specs/015-gke-terraform-infra/quickstart.md` Scenario 2 (preflight, bootstrap, project, gke) and the "ready for Flux" checkpoint. Record the elapsed time against SC-001 (under 60 minutes).
- [ ] T042 GATED. Run `specs/015-gke-terraform-infra/quickstart.md` Scenario 3 (zero-change plan, then manual drift detected, then reverted). Record against SC-003.
- [ ] T043 GATED. Install Flux from `clusters/gke/flux-system/` (runbook Part 3.1) and run `specs/015-gke-terraform-infra/quickstart.md` Scenario 4 ("ready for restore"). Record against SC-008 (under 30 minutes, no manual cloud changes).
- [ ] T044 GATED. Rehearse `specs/015-gke-terraform-infra/quickstart.md` Scenario 5 (node replacement keeps the address) and Scenario 6 (removal), then recreate. Record against SC-007.
- [ ] T045 GATED. After a month of running, record in `docs/runbooks/gke-terraform.md` (Cost section) that billing shows about $65 or less and that the $80 budget alert exists (SC-006).

---

## Dependencies & execution order

```text
Phase 1 Setup ─▶ Phase 2 Foundational ─┬─▶ Phase 3 US1 (MVP) ─▶ Phase 4 US2
                                       ├─▶ Phase 5 US5 ──────▶ Phase 6 US3 (check 8 needs T026)
                                       └─▶ Phase 7 US4 (needs T011–T014 resource names, T020)
Phases 3–7 ─▶ Phase 8 Polish ─▶ merge ─▶ (Spec 012 Gates 1–3) ─▶ Phase 9 GATED
```

- **US1** depends only on Phase 2.
- **US2** needs US1's files to validate (T021 runs on them), and T020 inspects them.
- **US5** depends only on Phase 2. It touches manifests and governance, not Terraform.
- **US3** depends on Phase 2, US1 (check 9 scans the `.tf` files) and US5 T026 (check 8).
- **US4** depends on US1 resource addresses and T020.
- Inside each story, tests come first (T020, T024, T025, T032), then implementation.

## Parallel opportunities

- **Phase 1:** T002 and T003.
- **Phase 2:** T005, T006 and T007 after T004.
- **US1:** T008, T009, T010, T011, T014, T015 and T016 in parallel (different files). Then T012 → T013 → T017 → T018 → T019.
- **US5 alongside US1:** a second worker can do T024 and T025 (in parallel), then T026 → T027 → T028 → T029 → T030 → T031, while US1 proceeds.
- **Polish:** T037 and T038 in parallel.

## Implementation strategy

1. **MVP = Phases 1–3 (US1)**: Terraform that validates offline, and a runbook up to "ready for Flux". Reviewable and mergeable without touching the cloud.
2. **Add US2** (CI and protections), **then US5** (the load balancer edge with governance), **then US3** (cross-check), **then US4** (removal docs).
3. **Phase 8** polish and the full verification suite, then one PR (or one PR per phase if review size matters).
4. **Phase 9 only after Spec 012 Gates 1–3.** Until then, nothing in this feature creates or changes a cloud resource.
