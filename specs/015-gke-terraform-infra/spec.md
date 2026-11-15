# Feature Specification: GKE Infrastructure as Code (Terraform)

**Feature Branch**: `015-gke-terraform-infra`

**Created**: 2026-11-14

**Status**: Draft (clarified, ready for planning)

**Input**: User description: "Let's specify to deploy its architecture via Terraform. What should I Decide? I don't have any background to deploy it now."

**Related**: [`012-gke-platform-migration`](../012-gke-platform-migration/spec.md) (Gate 4 step 2 provisions the GKE cluster this feature defines). Spec numbers 013 and 014 are reserved for the Kafka and Flink log pipeline already referenced by Spec 012.

## Clarifications

### Session 2026-11-14

- Q: Which resources outside the GKE environment, if any, are also managed by these definitions: the control-center VM (private repo), Cloudflare DNS records, or neither? → A: Neither. Scope is the GKE environment only. The control-center VM keeps its existing setup in its private repository, and Cloudflare DNS records stay manual and are switched once during the Spec 012 cutover. Either can get its own spec later.
- Q: When GKE replaces the node that serves the websites, how should the site's fixed public IP address get onto the new node? → A: A Google Cloud load balancer holds the fixed public address and forwards traffic to the `ondemand-core` node. It is a pass-through (TCP, layer 4) load balancer, so HTTPS is still decrypted by Traefik and visitors' real IP addresses reach the platform. This is the user's initial choice (about $18/month fixed plus about $0.008/GiB processed). It reverses Spec 012's "zero GCP LoadBalancer" design for GKE. The user may later switch to kubeip ($0) to save the fixed fee. Downtime during a node replacement is about the same either way, because every pod on the single on-demand node restarts.
- Q: Who should run `terraform apply`, the step that actually creates or changes resources in Google Cloud? → A: The operator, from Cloud Shell, signed in as themselves. CI only formats and validates the definitions and holds no Google Cloud credentials. Automated previews or applies from CI are out of scope.
- Q: When moving to GKE, which machine should restore the exported bundle (databases, OpenBao secrets, TLS certificates) onto the new cluster? → A: The control center, using its existing Restore action with passkey confirmation. The bundle stays in the control-center VM's RAM, and the operator pastes in a temporary GKE admin kubeconfig built from this feature's outputs. Spec 015 does not restore anything itself. It guarantees that GKE is ready to receive the restore. The control center's own GKE access stays in its private repository (Q1).
- Q: How should the GKE control API (the management endpoint that `kubectl`, Terraform's checks and the control center use) be protected? → A: Public endpoint, protected by Google sign-in and permissions (IAM) and then Kubernetes RBAC, with no IP allow list. This is the user's initial choice. The considered alternatives were an IP allow list (blocks stolen credentials used from elsewhere, but Cloud Shell's address changes every session), a private endpoint with a bastion, and the DNS-only endpoint behind Google's sign-in check (Google's hardening recommendation: strangers never reach the API). The user may move to the DNS-only endpoint later.

## Overview

Spec 012 describes the GKE cluster the platform will move to, but it creates that cluster with a list of one-off console and command-line steps (`quickstart.md` Gate 4 step 2). Those steps are easy to get subtly wrong, leave no record of what exists in the cloud project, and cannot be reviewed before they run.

This feature puts every Google Cloud resource the GKE environment needs into version-controlled infrastructure definitions in this repository (`infra/gcp/`), written with Terraform. An operator with no prior cloud deployment experience can create, inspect, change and remove the environment by following a written runbook. Terraform stops at an empty, correctly shaped cluster. FluxCD continues to own everything inside the cluster (Constitution Principle I), and the Spec 012 cutover bundle is restored afterwards from the control center.

The end-to-end order is:

1. Terraform creates the cloud resources (this feature).
2. `flux bootstrap` points the cluster at `clusters/gke/`.
3. Flux deploys every platform and project workload. Services that need OpenBao wait until step 4.
4. On cutover day, writes on `vps-k3s` stop, a fresh bundle is exported and restored from the control center, and GKE is tested by address.
5. The Cloudflare DNS records are switched (Spec 012).

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Create the GKE environment from the repository (Priority: P1)

The operator, who has not deployed cloud infrastructure before, follows the runbook from a browser-based cloud terminal. A small number of commands create every cloud resource the `clusters/gke/` overlay expects: the cluster, its two node pools, the public address and its load balancer, the web firewall opening, the OpenBao unseal key and its keyless identity, and the public image registry. At the end, the operator can connect to the cluster and run the Flux bootstrap step of Spec 012 Gate 4.

**Why this priority**: Without this, Spec 012 Gate 4 cannot start. Everything else in this feature refines or protects this path.

**Independent Test**: In the `nacfson-pipeline` project, run the runbook from step 1 to the "ready for Flux" checkpoint. The checkpoint commands report two nodes with the pool names, labels and taints recorded in `clusters/gke/capacity.yaml`. They also report a reserved public IPv4 address held by the load balancer, and an unseal key whose name matches `clusters/gke/layers.yaml`.

**Acceptance Scenarios**:

1. **Given** an empty `nacfson-pipeline` project with billing enabled and Spec 012 Gates 1–3 passed, **When** the operator runs the runbook's create steps, **Then** a zonal cluster in `asia-northeast3-a` exists with the settings in Spec 012 Gate 4 step 2: logging off, monitoring off, the advanced dataplane, and workload pool `nacfson-pipeline.svc.id.goog`.
2. **Given** the created cluster, **When** the operator lists nodes, **Then** there is exactly one `ondemand-core` node (e2-medium) without taints and one `spot-controllers` node (e2-small, Spot) carrying `cloud.google.com/gke-spot=true:NoSchedule`, matching `clusters/gke/capacity.yaml`.
3. **Given** the created resources, **When** OpenBao later starts on the cluster with the `gcpckms` seal in `clusters/gke/layers.yaml`, **Then** it can use the key `openbao-unseal` in key ring `nacfson-vault` through the Kubernetes service account `vault/openbao`, with no key file or Kubernetes Secret.
4. **Given** the created registry, **When** an anonymous client pulls an image by digest from `asia-northeast3-docker.pkg.dev/nacfson-pipeline/nacfson-public`, **Then** the pull succeeds without credentials, and the registry applies the same cleanup rules as `deploy/gcp/gar-cleanup-policy.json`.
5. **Given** the operator reaches the runbook's final checkpoint, **When** they read its output, **Then** they have the public IPv4 address to use for the DNS switch, and the exact command to connect to the cluster.

---

### User Story 2 - Review every infrastructure change before it happens (Priority: P1)

Any change to the cloud environment starts as an edit to the definitions in `infra/gcp/`, goes through a pull request, and is previewed before it is applied. The preview says in plain terms what will be added, changed or destroyed. When nothing has changed, running the definitions again reports that there is nothing to do. A difference that someone made by hand in the console shows up as drift in the preview.

**Why this priority**: The operator is new to cloud deployment. Seeing exactly what will happen before it happens is the main protection against costly or destructive mistakes, and matches how this repository already handles cluster changes.

**Independent Test**: After a successful create, run the preview with no edits and confirm zero changes. Change one node pool setting in a branch and confirm the preview lists only that change. Then change a label by hand in the console and confirm the next preview reports it.

**Acceptance Scenarios**:

1. **Given** a freshly created environment, **When** the operator runs the preview without editing anything, **Then** it reports zero resources to add, change or destroy.
2. **Given** a preview that would destroy or replace the cluster, the unseal key or the registry, **When** the operator reads it, **Then** the runbook tells them to stop, and protected resources refuse destruction unless their protection is first removed in a reviewed change.
3. **Given** a pull request that edits `infra/gcp/`, **When** CI runs, **Then** the definitions are checked for formatting and validity without any cloud credentials, and the result appears on the pull request.

---

### User Story 3 - Prepare and validate before any cloud resource exists (Priority: P2)

Spec 012's Gate 1 forbids creating cloud clusters until all offline and local checks pass. This feature's definitions can be written, reviewed, formatted, validated, and cross-checked against the `clusters/gke/` manifests entirely offline. The work can then merge before Gate 4 without touching the cloud project.

**Why this priority**: It lets this feature finish now without breaking the Gate 1 rule, and it catches name mismatches before they cost money or cause an outage.

**Independent Test**: On a machine with no cloud credentials, run the repository's offline checks. The infrastructure definitions validate. A deliberate mismatch, such as renaming the unseal key in the definitions only, fails the cross-check.

**Acceptance Scenarios**:

1. **Given** no cloud credentials, **When** the offline checks run, **Then** they complete and report pass or fail for the infrastructure definitions.
2. **Given** a value that both the definitions and `clusters/gke/` depend on (pool names, Spot taint, key ring, key name, service account, workload pool, registry name, region, zone), **When** the two disagree, **Then** the cross-check fails and names the mismatched value.

---

### User Story 4 - Remove the environment safely (Priority: P3)

If the cutover is abandoned before DNS moves to GKE, or the environment has to be rebuilt, the operator can remove the billable resources with a documented procedure. The OpenBao unseal key and the record of what was deployed stay intact. The current `vps-k3s` cluster and DNS are never affected.

**Why this priority**: It matters only when something has gone wrong, but without it a beginner would be left deleting resources by hand in the console.

**Independent Test**: In a rehearsal, create the environment and then follow the removal procedure. Confirm that no cluster, nodes, load balancer, reserved address or firewall rule remain, and that the unseal key and stored state still exist.

**Acceptance Scenarios**:

1. **Given** a created environment before the DNS switch, **When** the operator follows the removal procedure, **Then** the cluster, node pools, load balancer, reserved address and firewall rule are gone, and `vps-k3s` keeps serving all hostnames.
2. **Given** the removal procedure, **When** it completes, **Then** the unseal key still exists and the stored deployment state is retained, so the environment can be recreated with the same names.

---

### User Story 5 - GKE is ready to receive the cutover bundle (Priority: P2)

After the cluster exists and Flux has deployed the platform, the operator can run the Spec 012 restore from the control center without doing any further cloud setup. Everything the restore depends on outside the cluster already exists, and the runbook names the checkpoint at which the restore may start.

**Why this priority**: The cluster on its own serves nobody. Moving the data is the reason it exists, and a missing prerequisite discovered mid-cutover means extra downtime.

**Independent Test**: After the "ready for Flux" checkpoint and `flux bootstrap`, the runbook's "ready for restore" checks pass:
- the unseal key and its trust binding exist
- `openbao-0`, `postgres-0` and `keycloak` are running on `ondemand-core`
- OpenBao reports uninitialized rather than failing to start
- the control-center VM can reach the cluster API

**Acceptance Scenarios**:

1. **Given** the environment created by this feature, **When** Flux deploys OpenBao with the `gcpckms` seal from `clusters/gke/layers.yaml`, **Then** OpenBao starts and reports uninitialized instead of failing on a missing key.
2. **Given** the "ready for restore" checkpoint passes, **When** the control center runs `cutover.restore --target gke` with a fresh bundle, **Then** no cloud resource needs to be created or changed by hand for the restore, including its switch to the KMS seal.
3. **Given** the cluster outputs, **When** the operator prepares the control center's temporary GKE access, **Then** the outputs provide the cluster name, zone and endpoint needed to build it.

---

### Edge Cases

- **Spot node reclaimed**: the cloud provider replaces the `spot-controllers` node. The replacement keeps the pool's label and taint because they are defined on the pool, not on the node.
- **On-demand node replaced** (upgrade or repair): the load balancer keeps the public address and starts sending traffic to the new `ondemand-core` node once it passes the health check. The sites are still down for a few minutes, because every pod on the single on-demand node restarts on the new one. A maintenance window keeps planned upgrades to a time the operator chooses.
- **Spot node**: `spot-controllers` is never a load balancer backend. Traefik runs only on standard nodes (`deploy/platform/traefik/deployment.yaml`), so reclaiming the spot node does not affect web traffic.
- **Traefik not running yet**: right after the cluster is created, the load balancer reports no healthy backend until Flux deploys Traefik. This is harmless, because DNS still points at `vps-k3s` until cutover.
- **Quota**: the regional vCPU quota (6 in `asia-northeast3`) has to cover two nodes plus one temporary upgrade node. The runbook's preflight step checks the quota before any create.
- **Partial create**: a create that fails halfway, for example on quota or a missing API, can be re-run, and it completes only what is missing.
- **Lost local terminal**: the cloud terminal session ends mid-run. Stored state lives in the cloud project, not on the terminal, so the next session continues from it.
- **API server address is public**: Spec 012 requires a checkpoint before Flux bootstrap. The runbook includes `kubectl get endpoints kubernetes -n default` and points to the `openbao-ingress-policy` change if the address is public.
- **Names that cannot be reused**: key rings and keys can never be deleted in Google Cloud. A typo in their names is permanent, so they are cross-checked offline before the first create.
- **Budget**: the project's existing $1 budget alert (from the control-center setup) would fire on day one, because the planned nodes cost about $39 per month.
- **Stale or lost bundle**: the control center keeps bundle secrets in RAM, so they are lost if its VM stops or reboots, and a bundle exported before cutover day misses later writes on `vps-k3s`. The runbook's "ready for restore" checkpoint requires a fresh export taken in the same sitting as the restore.
- **Restore starts before the platform is up**: if the restore starts while `postgres-0` or `keycloak` are still starting, it fails partway. The checkpoint requires them running first, and the restore can be re-run because DNS has not moved.

## Requirements *(mandatory)*

### Functional Requirements

**Scope and ownership**

- **FR-001**: The repository MUST contain infrastructure definitions under `infra/gcp/` for every Google Cloud resource the `clusters/gke/` overlay depends on, and nothing else. The control-center VM and its firewall, IAM and service account, and the Cloudflare DNS records, are out of scope.
- **FR-002**: Each cloud resource MUST be owned by exactly one set of definitions. No resource may be managed both by these definitions and by another tool, repository or manual procedure.
- **FR-003**: The definitions MUST stop at an empty cluster. Workloads, namespaces, secrets and Flux itself remain owned by FluxCD and the Spec 012 cutover procedure.

**What gets created** (values fixed by Spec 012 and `clusters/gke/`)

- **FR-004**: The definitions MUST turn on the cloud services the environment needs, and MUST NOT turn them off when resources are removed.
- **FR-005**: The definitions MUST create a zonal cluster in project `nacfson-pipeline`, zone `asia-northeast3-a`. It MUST have built-in logging and monitoring off, the advanced dataplane that enforces standard NetworkPolicy, and workload identity pool `nacfson-pipeline.svc.id.goog`.
- **FR-006**: The definitions MUST create the two node pools in `clusters/gke/capacity.yaml`:
  - `ondemand-core`: 1 × e2-medium, no taints
  - `spot-controllers`: 1 × e2-small Spot, taint `cloud.google.com/gke-spot=true:NoSchedule`

  Nodes MUST run under a dedicated least-privilege node identity, not the project's default compute identity.
- **FR-007**: The definitions MUST reserve one static public IPv4 address in `asia-northeast3`, held by a regional external pass-through (TCP, layer 4) load balancer for ports 80 and 443 (Clarifications, session 2026-11-14). The load balancer:
  - MUST send traffic only to `ondemand-core` nodes, and follow node replacement automatically without any manual step
  - MUST NOT decrypt HTTPS. TLS stays at Traefik (Constitution, Platform-Owned Edge TLS), and visitors' source IP addresses MUST reach the platform unchanged.
  - MUST mark a node healthy only while Traefik answers on it

  Web traffic MUST reach only nodes running a ready Traefik pod, which means `ondemand-core` only. No node may serve ports 80 or 443 on its own node address. The web firewall opening (TCP 80/443 from the internet to the reserved address, node VMs only) is owned by these definitions, with GKE's automatic load balancer firewall rule turned off; only the health-check firewall rule for Google's probe ranges stays with GKE (code review, 2026-11-14).
- **FR-007a**: Exactly one owner MUST manage the load balancer (FR-002): either these definitions, or the GKE controller through a Traefik Service of type `LoadBalancer` that uses the reserved address. `/speckit-plan` chooses one and records why. Any change to `clusters/gke/` or `deploy/platform/traefik/` that this requires is part of this feature. It MUST keep `platform-preflight` passing, and it MUST NOT allow `LoadBalancer` or `NodePort` Services in project namespaces.
- **FR-008**: The definitions MUST create key ring `nacfson-vault` and key `openbao-unseal` in `asia-northeast3`. They MUST also create the identity `openbao-kms`, allowed to encrypt and decrypt with that key and to read its metadata, and trusted only by Kubernetes service account `vault/openbao`.
- **FR-009**: The definitions MUST create the publicly readable Docker registry `nacfson-public` in `asia-northeast3`, with cleanup rules equivalent to `deploy/gcp/gar-cleanup-policy.json`.
- **FR-010**: The definitions MUST expose as outputs:
  - the reserved public IPv4 address
  - the cluster name and zone
  - the command to connect to the cluster

**Safety**

- **FR-011**: Deployment state MUST be stored in a private, versioned cloud storage bucket in the same project. It MUST NOT be committed to Git, because the repository is public.
- **FR-012**: The cluster, the unseal key and the registry MUST refuse destruction unless their protection is first removed through a reviewed change.
- **FR-013**: Every apply MUST be preceded by a saved preview, and the apply MUST execute exactly that preview.
- **FR-014**: Applying MUST be done by the operator from Cloud Shell, signed in as themselves. CI MUST NOT hold Google Cloud credentials for this feature (Clarifications, session 2026-11-14).
- **FR-015**: A budget alert MUST exist for the project at a level that does not fire under the planned steady-state cost. The default is $80 per month; it notifies at 50%, 90% and 100%. These definitions own it. The runbook tells the operator to remove or raise the control center's $1 alert by hand, so that only one alert covers the project (FR-002).
- **FR-016**: No step in this feature may create or change cloud resources until Spec 012 Gates 1–3 have passed. Writing, reviewing and validating the definitions offline is allowed at any time.

**Validation**

- **FR-017**: CI MUST check formatting and validity of `infra/gcp/` on every pull request that touches it, without cloud credentials.
- **FR-018**: An offline cross-check MUST fail when a value shared between `infra/gcp/` and `clusters/gke/` differs. The shared values are:
  - pool names and the Spot taint
  - key ring, key and unseal service account names
  - the Kubernetes service account
  - the workload pool, region, zone and registry name
  - the reserved ingress address name
- **FR-019**: The repository's existing secret scanning MUST cover `infra/gcp/`. No credential, key file or state file may be committed.

**Documentation**

- **FR-020**: A runbook MUST take an operator with no prior cloud deployment experience from an empty project to the "ready for Flux bootstrap" checkpoint, without needing outside material. For each step it MUST say:
  - the exact command
  - what success looks like
  - what to do on the most likely failure
- **FR-021**: The runbook MUST include the removal procedure from User Story 4, and a quota check before the first create.
- **FR-022**: Spec 012's `quickstart.md` Gate 4 step 2 MUST point to this runbook instead of listing one-off creation commands. Spec 012's "zero GCP LoadBalancer" constraint, its DNS step ("Node 1's static IPv4"), and the related notes in `docs/platform-architecture.html` and `deploy/platform/traefik/service.yaml` MUST be updated to describe the load balancer address.

**Hand-off to the cutover** (Clarifications, session 2026-11-14)

- **FR-023**: The unseal key, its identity and the trust binding (FR-008) MUST exist before `flux bootstrap`. The runbook MUST order the steps so that this holds.
- **FR-024**: The runbook MUST define a "ready for restore" checkpoint:
  - Flux is bootstrapped
  - `openbao-0` reports uninitialized
  - `postgres-0` and `keycloak` are running on `ondemand-core`
  - the cluster API is reachable from the control-center VM

  It MUST then hand over to the Spec 012 restore procedure without repeating it.
- **FR-025**: The cluster API endpoint MUST be public, with no IP allow list. Access MUST require a valid Google identity with permission on the project (IAM), followed by Kubernetes RBAC. Cloud Shell and the control-center VM MUST reach it without extra network setup. Moving later to the DNS-only endpoint, with IP endpoints disabled, MUST be possible as a reviewed change to these definitions, without recreating the cluster.
- **FR-026**: No cutover bundle file may be written to the repository, to deployment state, or to any resource these definitions create.

### Key Entities

- **Infrastructure definitions**: the reviewed files in `infra/gcp/` that describe the cloud resources. Grouped as one-time state storage, project-wide services, and the GKE environment.
- **Deployment state**: the stored record of which cloud resources these definitions currently manage. It is used to compute previews and detect drift. It may contain sensitive values, so it lives only in the private state bucket.
- **Preview (plan)**: the saved list of additions, changes and removals that an apply will perform.
- **Shared values**: names and settings that both `infra/gcp/` and `clusters/gke/` must agree on. They are the subject of the offline cross-check.
- **Runbook**: the step-by-step operator guide with checkpoints ("ready for Flux", "ready for restore").
- **Cutover bundle** (owned by Spec 012 and the control center, referenced only): the exported OpenBao snapshot, Keycloak and project database dumps, and edge TLS secrets, restored onto GKE after this feature's checkpoints pass.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: A first-time operator using only the runbook goes from an empty project to the "ready for Flux bootstrap" checkpoint in under 60 minutes. Console clicks are limited to the ones the runbook lists explicitly.
- **SC-002**: 100% of the shared values listed in FR-018 match between `infra/gcp/` and `clusters/gke/`, as reported by the offline cross-check.
- **SC-003**: Immediately after a successful create, a second preview reports zero changes.
- **SC-004**: Every pull request touching `infra/gcp/` receives an automated validity result within 5 minutes, without cloud credentials.
- **SC-005**: Zero credentials, key files or state files appear in Git history, as verified by the existing secret scan.
- **SC-006**: The steady-state monthly cost of the created resources stays at or below $80. This covers about $39 for the nodes, about $18 for the load balancer, plus disks, the address and data processing. The budget alert notifies before that amount is exceeded.
- **SC-007**: In a rehearsal, the removal procedure leaves zero billable compute, load balancer, address or firewall resources within 20 minutes. The unseal key and the stored state remain, and `vps-k3s` serves every hostname throughout.
- **SC-008**: In a rehearsal, the "ready for restore" checkpoint passes within 30 minutes of `flux bootstrap`, with zero manual cloud changes.

## Assumptions

- **Tooling**: the user chose Terraform. Region, zone, node sizes and the names fixed by `clusters/gke/` come from Spec 012 and are not reopened here. Spec 012's "no load balancer" design is reopened for GKE by the 2026-11-14 clarification (FR-007, FR-022).
- **Starting point**: the cloud project `nacfson-pipeline` already exists with billing enabled. Billing setup is a manual prerequisite in the runbook.
- **New names**: four names have no existing value in the repository and are chosen here, renameable before the first create:
  - cluster: `nacfson-gke`
  - state bucket: `nacfson-pipeline-tfstate`
  - node identity: `gke-nodes`
  - reserved address: `gke-ingress-ip`
- **Where commands run**: the browser-based cloud terminal (Cloud Shell), which comes with the cloud command line, Terraform and kubectl. No local workstation setup is required.
- **Build trigger deferred**: the build trigger that runs `deploy/gcp/cloudbuild.yaml` on each push needs a one-time GitHub connection made in the console. It is out of scope; builds keep running on demand.
- **DNS**: Cloudflare records are DNS-only (`docs/tls-certificates.md`), stay manual, and are changed during the Spec 012 cutover (Clarification Q1).
- **Shared project**: the control-center VM lives in the same cloud project and keeps its existing setup. The budget alert in FR-015 replaces the $1 alert for the whole project.
- **Operator account**: because the API endpoint is public (FR-025), the operator's Google account is the effective key to the cluster. It is assumed to use 2-step verification, preferably a passkey or security key. Only the operator holds admin permission. The control center's `cc-viewer` identity stays read-only.
- **Migration downtime**: the operator accepts downtime of the `vps-k3s` sites during the move to GKE. This feature sets no availability target for the cutover window. It only requires that creating, changing or removing the GKE environment never changes `vps-k3s` or DNS.
- **Container images**: every first-party image referenced by `clusters/gke/` and `deploy/` is on GHCR, publicly pullable without credentials, and built for both `linux/amd64` and `linux/arm64` (checked 2026-11-14). GKE's amd64 nodes need no pull secret. The `nacfson-public` registry is created for Cloud Build, but moving images to it is not required for this feature. `vault-ai-proxy` is referenced by `:latest` rather than a digest. That is a Spec 012 / Constitution conformance issue, not part of this feature.
- **Cutover tooling**: the restore procedure, bundle contents and their known issues are owned by Spec 012 and the control center. The known issues are the KMS-step data path and service address, the undocumented "stop writes" step, and the smoke-test usage in `quickstart.md`. The user also intends to drop the VictoriaLogs archive from the bundle. These are fixed there, not here.
- **Constitution**: Principle I governs what runs inside clusters. Cloud resources around the cluster are not covered by it. This feature applies the same review-before-change discipline to them through pull requests and saved previews (FR-013).
