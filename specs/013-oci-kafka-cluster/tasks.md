---

description: "Task list for Spec 013: OCI Replicated Kafka Log Ingestion Cluster"
---

# Tasks: OCI Replicated Kafka Log Ingestion Cluster

**Input**: Design documents from `specs/013-oci-kafka-cluster/`

**Prerequisites**: [plan.md](plan.md), [spec.md](spec.md), [research.md](research.md), [data-model.md](data-model.md), [contracts/](contracts/), [quickstart.md](quickstart.md)

**Tests**: Included. The spec requires them: FR-015 and SC-008/SC-012 (declaration check with mutation tests), FR-016 (rehearsal suites runnable with one command), and FR-023 point 4 (relay rules tested on captured Kafka log lines). Where tests are listed, write them first and confirm they fail.

**Organization**: Tasks are grouped by user story so each story can be built and checked on its own.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependency on an unfinished task)
- **[Story]**: US1–US5, from spec.md
- **Operator**: The task needs the operator's credentials or a live system. Agents prepare the commands and never log in to the VMs or the cluster themselves.
- Repository rules apply to every task: no comments in any code or generated file, third-party images pinned to exact tags (the relay and slot agent images by digest), no private key in Git, and `python3 scripts/check-arch-doc-sync.py --worktree` whenever `specs/` changes.

## Path Conventions

Paths follow the plan's Source Code tree: `infra/kafka/`, `infra/oci/{terraform,ansible}/`, `services/error-pipeline/`, `services/wg-slot-agent/`, `clusters/gke/{oci-link,observability,cert-manager}/`, `scripts/`, `tests/{infra,kafka,preflight,fixtures}/`, `docs/runbooks/oci-kafka.md`.

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Inputs from the operator, pinned versions, and empty structure

- [ ] T001 Operator: run the R12 log-rate measurement (research.md R12, `scripts/open-observe.sh` running, plus the `ssh oracleCloud ... du -sh /victoria-logs-data` command, run by the operator) and paste the output. Record `lines`, `msg_bytes`, the on-disk size, raw bytes per day (`msg_bytes ÷ 30 × 2`), and days that fit (`15 GB ÷ (raw bytes per day ÷ 5)`) in a "Measured log rate" section of docs/runbooks/oci-kafka.md. If 7 days do not fit, update the R12 table in specs/013-oci-kafka-cluster/research.md before T010.
- [ ] T002 Operator: confirm the OCI home region with `oci iam region-subscription list --query 'data[?"is-home-region"]'` and record it in docs/runbooks/oci-kafka.md. It must not be `ap-chuncheon-1`. If it is not `ap-seoul-1`, update research.md R4 and use the confirmed region as the default of `var.region` in T038. In the same step, confirm from current Oracle pricing pages that the Bastion service and three reserved public IPs cost nothing on this tenancy, and record the links in research.md R5. If either is charged, stop and revise R5 before T040 and T041.
- [X] T003 Operator: approve Constitution Amendment 2.5.0 in principle (the six items in research.md R19, including the automated Ansible path and the runner's certificate requests). T018 must not be committed without this approval.
- [X] T004 [P] Create the directory skeleton from plan.md: infra/kafka/, infra/oci/terraform/, infra/oci/ansible/{inventory,group_vars/all,group_vars/brokers,library,roles/{base,wireguard,certs,kafka,topics_acls,ops_agent,samplers}}, services/wg-slot-agent/{cmd/wg-slot-agent,internal/{slots,kube,wg,metrics}}, services/error-pipeline/{cmd/ops-relay,internal/opsrelay,internal/kafkaclient}, clusters/gke/oci-link/, clusters/gke/cert-manager/, tests/kafka/rehearsal/, tests/fixtures/kafka-logs/4.3.2/, tests/fixtures/kafka-quorum/4.3.2/
- [X] T005 [P] Add ignore rules to .gitignore: `infra/oci/terraform/.terraform/`, `infra/oci/terraform/*.tfstate*`, `infra/oci/terraform/tfplan`, `infra/oci/terraform/*.tfvars`, `infra/oci/ansible/inventory/bastion.local.yml`, `build/strimzi/`, `tests/kafka/rehearsal/.run/` (per-run CA and keys)
- [X] T006 [P] Verify and record pins in specs/013-oci-kafka-cluster/research.md (R1, R13, R14, R17): the `apache/kafka:4.3.2` multi-arch index digest (must list `linux/amd64` and `linux/arm64`), the `fluent/fluent-bit:4.2.8` index digest, the newest Prometheus JMX exporter 1.x release at least two weeks old with Java 21 support (version and jar SHA-256), the newest `github.com/twmb/franz-go` release at least two weeks old, and the oldest Strimzi release that lists Kafka 4.3.2
- [X] T007 [P] Initialize services/wg-slot-agent/go.mod (`module nacfson_pipeline/services/wg-slot-agent`, `go 1.22`, no third-party requirements)
- [X] T008 [P] Add `github.com/twmb/franz-go` at the version from T006 to services/error-pipeline/go.mod and go.sum
- [X] T009 [P] Vendor the Strimzi 1.3.1 JSON schemas for `Kafka`, `KafkaNodePool`, `KafkaTopic`, and `KafkaUser`, converted from the release's CRDs, into tests/fixtures/strimzi-schemas/1.3.1/, and write how to regenerate them in tests/fixtures/strimzi-schemas/README.md

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Declarations, governance, the Kafka CA, and the rehearsal harness. Every story depends on these.

**⚠️ CRITICAL**: No user story work starts until this phase is complete.

### Declarations (contracts/kafka-declarations.md)

- [X] T010 Generate the cluster ID with `podman run --rm apache/kafka:4.3.2 /opt/kafka/bin/kafka-storage.sh random-uuid` and write infra/kafka/cluster.yaml in the contract's shape. It contains:
  - **nodes**: `a1-1` (node 1, broker+controller, 10.200.0.11), `a1-2` (node 2, broker+controller, 10.200.0.12), `micro-1` (node 3, broker, 10.200.0.13), `micro-2` (node 4, controller, 10.200.0.14)
  - **listeners**: client 9092 and controller 9093, both mutual TLS
  - **broker_settings**: the five from the contract
  - **topics**: the four topics from the R12 table (as amended by T001). `logs.*` have 3 partitions, RF 3, min ISR 2, `CreateTime`. `ops.*` have 1 partition, RF 3, min ISR 1, `LogAppendTime`. `segment_bytes` is 134217728 on all four.
  - **max_message_bytes**: at least Fluent Bit's largest record after multiline joining, read from deploy/platform/observability/fluent-bit/. Record both numbers in plan.md Technical Context.
  - **identities**: every identity and grant in research.md R9. `broker-1..3` and `controller-1`, `controller-2`, `controller-4` have `super_user: true` and keys at `vm:<node>`. `fluent-bit-gke` and `ops-relay` have keys at `cluster:observability/<secret>`. `ops-agent-a1-1`, `ops-agent-a1-2`, `ops-agent-micro-1`, and `ops-agent-micro-2` have keys at `vm:<node>`. `ops-cli` has `vm:all` (a separate key on every VM).
  - **bootstrap**: `gke` and `rehearsal` lists
  - The file must not mention OCI, shapes, Terraform, or Ansible.
- [X] T011 [P] Write tests/infra/test_kafka_declarations.py (stdlib `unittest`). One test passes on the committed files. Eight tests each mutate a temporary copy for one `check` rule (contract rules 1–8) and expect a non-zero exit naming the field. A last test renders each target twice and expects byte-identical output. Confirm they fail before T012.
- [X] T012 Implement `check` in scripts/kafka-declarations.py (Python 3.11, PyYAML from scripts/requirements-preflight.txt, `--root` option for tests). Rules 1–7 come from contracts/kafka-declarations.md: Fluent Bit `Brokers`/`Topics` vs `bootstrap.gke` and the topics, the RF/min-ISR floors, the TLS and zstd lines, the producer grants and super users, node roles, IDs and addresses inside `10.200.0.0/24`, and the per-broker 20 GB budget (`retention_bytes × partitions` plus one segment per partition).
- [X] T013 Implement `render --target ansible` in scripts/kafka-declarations.py, writing infra/oci/ansible/group_vars/all/kafka.generated.yml (no comments, stable key order) and commit the output
- [X] T014 Implement `render --target compose` in scripts/kafka-declarations.py, writing tests/kafka/rehearsal/compose.generated.yaml. It defines four `apache/kafka:4.3.2` services `node-1`…`node-4` with the R4 roles and node IDs, network aliases `broker-1`…`broker-3`, and per-node server.properties under tests/kafka/rehearsal/nodes/. Commit the output.
- [X] T015 Implement `render --target strimzi` in scripts/kafka-declarations.py, writing build/strimzi/*.yaml: `Kafka`, two `KafkaNodePool`s (broker+controller and broker-only/controller-only as declared), four `KafkaTopic`s, and one `KafkaUser` per non-super identity with `tls` auth and `simple` ACLs. Add check rule 8, which runs kubeconform on them against tests/fixtures/strimzi-schemas/1.3.1/.
- [X] T016 [P] Copy specs/013-oci-kafka-cluster/contracts/ops-events-and-metrics.md to contracts/CONTRACT_OPS_EVENTS.md (fix relative links) and add it to contracts/README.md
- [X] T017 Add a `kafka-declarations` job to .github/workflows/lint.yaml. It runs `python3 scripts/kafka-declarations.py check`, then `python3 -m unittest discover tests/infra`, then re-renders the ansible and compose targets and fails on `git diff --exit-code`.

### Governance (needs T003)

- [X] T018 Write Constitution Amendment 2.5.0 in .specify/memory/constitution.md: the six items of research.md R19 (item 5 names the runner's identity, scope, approval by merge to `main`, and targets, as Clarification 2.4.1 requires), MINOR bump 2.4.1 → 2.5.0, sync impact report, and project namespaces unchanged. Update the version reference in CLAUDE.md, AGENTS.md, and the `Constitution v2.4.1` badge in docs/platform-architecture.html.
- [X] T019 [P] Add `oci-link` cases to tests/preflight/test_platform_preflight.py:
  - **Admitted**: `DaemonSet/wg-slot-agent` in `oci-link` with `hostNetwork: true`, capability `NET_ADMIN` only, and read-only `hostPath: /var/lib/fluent-bit-state`
  - **Rejected**: the same spec under another name or namespace, any added capability, a writable or different hostPath, `privileged: true`
  - **S1 fallback**: `hostNetwork` admitted only on `DaemonSet/fluent-bit` and `Deployment/ops-relay` in `observability`
  - **Memory freeze**: the freeze still passes with the T022 reservation
- [X] T020 Implement the rule in scripts/platform-preflight.py: add `oci-link` to `PLATFORM_NAMESPACES` and add the admission rule and the observability S1 exception from T019. `python3 -m unittest discover tests/preflight` passes.
- [X] T021 [P] Add the three allowances from research.md R19 to deploy/platform/governance/manual-change-policy.yaml:
  - `system:serviceaccount:oci-link:wg-slot-agent`: create and update Leases and create Events in `oci-link`
  - `system:serviceaccount:oci-link:wg-slot-keygen`: create Secret `wg-slot-keys` and ConfigMap `wg-slot-public` only
  - Google service account `cc-kafka-runner`: CREATE `certificaterequests.cert-manager.io` and `events` in `oci-link` only
- [X] T022 [P] Add the platform reservation to clusters/gke/capacity.yaml: `ops-relay` 10m/64Mi (1 replica), `wg-slot-agent` 10m/32Mi per node, `wg-slot-keygen` 10m/32Mi. Run `python3 scripts/platform-preflight.py` for `gke`. If the Interim Memory Limit Freeze fails, stop and resolve the overage with the operator before merging (research.md R16).

### Kafka certificate authority (FR-024, R8)

- [X] T023 Create clusters/gke/cert-manager/kafka-mtls-ca.yaml with three objects: a self-signed `ClusterIssuer/kafka-mtls-selfsigned`, a `Certificate/kafka-mtls-ca` in `cert-manager` (`isCA: true`, ECDSA P-256, 10-year duration, Secret `kafka-mtls-ca`), and `ClusterIssuer/kafka-mtls-ca` backed by that Secret. Add clusters/gke/cert-manager/kustomization.yaml and a Flux `Kustomization` named `kafka-ca` (path `./clusters/gke/cert-manager`, `dependsOn: cert-manager`) in clusters/gke/layers.yaml. `./scripts/render-validate.sh gke` passes.

### Rehearsal harness (R15)

- [X] T024 Implement `up` and `down` in scripts/kafka-rehearsal.sh (POSIX shell, `podman compose`):
  - **Certificates**: make a per-run CA with openssl in tests/kafka/rehearsal/.run/ and issue one certificate per identity in infra/kafka/cluster.yaml
  - **Format**: format storage with `--initial-controllers` on nodes 1, 2, 4 and without initial controllers on node 3. Confirm the exact 4.3.2 flags with `kafka-storage.sh format --help` and record them in research.md R11.
  - **Start**: start the four nodes and wait for 3 registered brokers and 3 voters
  - **Topics and ACLs**: create the topics and ACLs from the declarations with the `broker-1` identity. `down` removes containers, volumes, and `.run/`.
- [X] T025 Write tests/kafka/harness.py (stdlib only):
  - **Records**: produce numbered records and consume from the beginning using the containers' Kafka CLI tools with an identity's certificate
  - **Nodes**: stop, start, and wipe a node
  - **Waits**: wait for under-replicated = 0 with a timeout
  - **Targets**: a `--target rehearsal|live` switch. `live` takes bootstrap and certificate paths from environment variables and never logs in to VMs.
  - Wire `scripts/kafka-rehearsal.sh test <suite>` to `python3 -m unittest tests.kafka.test_<suite>`.

**Checkpoint**: `kafka-declarations.py check`, `tests/infra`, `tests/preflight`, `render-validate.sh gke`, and `kafka-rehearsal.sh up` all pass. User stories can start.

---

## Phase 3: User Story 1 - Logs from GKE land durably in OCI (Priority: P1) 🎯 MVP

**Goal**: Fluent Bit's records from GKE are stored on three OCI VMs before acknowledgement, over WireGuard and mutual TLS.

**Independent Test**: quickstart Scenario 2 on the rehearsal, then Scenarios 6–7 live. 100,000 numbered lines read back exactly once, in order per pod within each partition, with every `logs.*` partition at 3 replicas and ISR 3.

### Tests for User Story 1 ⚠️

- [X] T026 [P] [US1] Write tests/kafka/test_story1.py. The rehearsal runs `fluent/fluent-bit:4.2.8` with clusters/gke/observability/fluent-bit-kafka.conf, changing only broker addresses and certificate paths. It covers:
  - **Exactly once**: 100,000 numbered lines from a fake `proj-*` source appear exactly once and in order per pod within each partition, with replicas 3 and ISR 3 on every `logs.*` partition
  - **Fields**: fields match the input (no added, removed, or renamed fields)
  - **Retries**: pausing a leader mid-batch forces retries without duplicates
  - **Refusals**: an undeclared topic is refused, not created, and an oversized record above `max_message_bytes` is refused
- [X] T027 [P] [US1] Write services/wg-slot-agent/internal/slots/claim_test.go against an `httptest` fake API server. It covers:
  - claims the lowest free slot
  - takes over an expired Lease, and a Lease whose holder Node is gone
  - on a `resourceVersion` conflict, re-lists and takes the next slot
  - with no free slot, emits Warning Event `NoFreeSlot`, starts no tunnel, and retries every 30 s
  - renewal every 30 s with a 90 s duration
  - release on SIGTERM clears the holder
- [X] T028 [P] [US1] Write services/wg-slot-agent/internal/wg/config_test.go. It checks the exact command plan for slot n:
  - `wg0` with address `10.201.0.<n+1>/32`
  - one peer per `wg-peers` entry, with `AllowedIPs` set to that broker's `/32` and `PersistentKeepalive 25`
  - routes `10.200.0.11/32`, `.12/32`, `.13/32` dev `wg0`
  - nft table `oci_link` with a single rule, `oifname "wg0" snat to 10.201.0.<n+1>`
  - re-running yields no changes, and teardown touches only `wg0` and `oci_link`
  - `hostroute` mode yields `ip route replace 10.200.0.0/24 dev wg0 src 10.201.0.<n+1>` and no nft table

### Implementation for User Story 1: GKE side

- [X] T029 [P] [US1] Implement services/wg-slot-agent/internal/kube/ with the standard library only: in-cluster token and CA, Leases list/get/update with `resourceVersion`, Nodes get, Events create
- [X] T030 [US1] Implement services/wg-slot-agent/internal/slots/ (claim, renew, release, takeover rules from contracts/wg-slot-agent.md) so T027 passes
- [X] T031 [US1] Implement services/wg-slot-agent/internal/wg/. It builds and applies the command plan through `wg`, `ip`, and `nft`, with `WG_MODE=snat|hostroute`, so T028 passes.
- [X] T032 [US1] Implement services/wg-slot-agent/cmd/wg-slot-agent/main.go with two subcommands:
  - **`run`**: claim, bring up, a 30 s renew loop, Events `TunnelStale` (every peer handshake older than 180 s, once) and `TunnelRecovered`, and release on SIGTERM
  - **`keygen`**: X25519 keys with `crypto/ecdh` for the slots listed in the mounted `wg-slot-pool` (each within 0–13). It creates Secret `wg-slot-keys` and ConfigMap `wg-slot-public` if absent, adds `slot-<n>.key`, `slot-<n>.pub`, and `slot-<n>.address` for each listed slot that has no key, never overwrites an existing key, and removes the entries of an unlisted slot only when its Lease has no live holder (otherwise it exits non-zero so the Job retries). Tests in services/wg-slot-agent/cmd/wg-slot-agent/keygen_test.go against a fake API server cover: first run, adding a slot, an existing key left unchanged, removing a free slot, and refusing to remove a held slot.
  - **`run`** claims only slots that are listed in `wg-slot-pool` and have a key, and releases its slot and claims another when its slot leaves the pool.
- [X] T033 [P] [US1] Write services/wg-slot-agent/Dockerfile (multi-stage, static Go build, runtime base pinned by exact tag with `wireguard-tools`, `iproute2`, `nftables`, non-root except the `NET_ADMIN` capability). Add a `wg-slot-agent` entry to the matrix in .github/workflows/build-images.yaml for amd64 and arm64.
- [ ] T034 [US1] Create clusters/gke/oci-link/ with these files:
  - **namespace.yaml**: Namespace `oci-link`
  - **rbac.yaml**:
    - ServiceAccounts `wg-slot-agent` and `wg-slot-keygen`
    - Role: leases get/list/watch/create/update and events create in `oci-link`
    - keygen Role: secrets and configmaps create/get/update, leases get/list
    - ClusterRole: nodes get
    - the matching bindings
  - **slot-pool**: a kustomize `configMapGenerator` entry for ConfigMap `wg-slot-pool` with `slots=0,1,2,3`, so its name changes with its content
  - **keygen-job.yaml**: requests = limits 10m/32Mi, mounts `wg-slot-pool`, and carries `kustomize.toolkit.fluxcd.io/force: enabled` so Flux re-runs it on every pool change
  - **daemonset.yaml**: `hostNetwork: true`, only `NET_ADMIN`, read-only `hostPath /var/lib/fluent-bit-state`, requests = limits 10m/32Mi, a Spot toleration so it runs on both pools, image by digest
  - **wg-peers.yaml**: ConfigMap with one entry per broker (name, public key placeholder filled in T059, reserved public IP from T041 outputs, port 51820, VCN `/32`)
  - **network-policy.yaml**: egress to the Kubernetes API only
  - **kustomization.yaml**
  - **Flux layer**: a Flux `Kustomization` named `oci-link` in clusters/gke/layers.yaml
  - **Checks**: `render-validate.sh gke` and preflight pass
- [X] T035 [P] [US1] Create clusters/gke/oci-link/runner-rbac.yaml (contracts/ansible-runner.md, Permissions):
  - Role `kafka-runner` in `oci-link`: create, get, list `certificaterequests.cert-manager.io`; create `events`
  - RoleBinding to User `cc-kafka-runner@<gcp-project>.iam.gserviceaccount.com`
  - Add it to clusters/gke/oci-link/kustomization.yaml
- [X] T036 [US1] Add TLS and zstd to both `[OUTPUT]` blocks in clusters/gke/observability/fluent-bit-kafka.conf, and change nothing else (FR-004):
  - `rdkafka.security.protocol ssl`
  - `rdkafka.ssl.ca.location`, `rdkafka.ssl.certificate.location`, `rdkafka.ssl.key.location` under `/etc/kafka-tls/`
  - `rdkafka.compression.codec zstd`
  - Create clusters/gke/observability/fluent-bit-kafka-tls.yaml:
    - `Certificate/fluent-bit-gke`: issuer `ClusterIssuer/kafka-mtls-ca`, CN `fluent-bit-gke`, duration 2160h, renewBefore 720h, `privateKey.rotationPolicy: Always`
    - a strategic-merge patch mounting Secret `fluent-bit-kafka-client` read-only at `/etc/kafka-tls`
  - Add the new file to clusters/gke/observability/kustomization.yaml. `kafka-declarations.py check` rule 4 passes.
- [X] T037 [P] [US1] Update contracts/CONTRACT_LOG_SCHEMA.md with the transport change: mutual TLS and zstd on the Kafka output, record format unchanged

### Implementation for User Story 1: OCI side

- [X] T038 [P] [US1] Create infra/oci/terraform/versions.tf, backend.tf, locals.tf, and variables.tf:
  - **versions.tf**: Terraform `>= 1.9.0, < 2.0.0`, `oracle/oci` with the minor version pinned
  - **backend.tf**: `gcs` backend, bucket `nacfson-pipeline-tfstate`, prefix `oci`
  - **locals.tf**: `yamldecode(file("${path.module}/../../kafka/cluster.yaml"))` for node names, addresses, and roles
  - **variables.tf**: `region` defaulting to the T002 value, `tenancy_ocid`, `compartment_ocid`, `budget_alert_email` (sensitive, no default)
- [X] T039 [US1] Create infra/oci/terraform/network.tf:
  - VCN `10.200.0.0/16`, internet gateway, route table, public subnet `10.200.0.0/24`
  - security list ingress: UDP 51820 from `0.0.0.0/0`, and TCP 9092–9093 only from `10.200.0.0/24`
  - no TCP 22 and nothing else inbound
- [X] T040 [US1] Create infra/oci/terraform/compute.tf:
  - four instances with the R4 shapes (`VM.Standard.A1.Flex` 1 OCPU/6 GB, `VM.Standard.E2.1.Micro`) and fault domains (A1-1 FD-1, A1-2 FD-2, Micro-1 FD-3, Micro-2 FD-3)
  - fixed private IPs from locals and 50 GB boot volumes
  - Ubuntu 24.04 Minimal images selected by exact display name per architecture
  - Micro-2 with an ephemeral public IP
  - launch-time `metadata.ssh_authorized_keys` injected from `var.ssh_authorized_keys` with `lifecycle { ignore_changes = [metadata["ssh_authorized_keys"]] }` (no Oracle Cloud Agent Bastion plugin; day-2 keys managed by Ansible `roles/base`)
- [X] T041 [P] [US1] Create infra/oci/terraform/public_ips.tf, bastion.tf, budget.tf, and outputs.tf:
  - **public_ips.tf**: reserved public IPs attached to the three broker VNICs
  - **bastion.tf**: an OCI Bastion on the subnet, with the client CIDR allowlist as a variable
  - **budget.tf**: `oci_budget_budget` at $1/month with an ACTUAL alert rule at 100% to `var.budget_alert_email`
  - **outputs.tf**: reserved IPs, private IPs, resolved image OCIDs, Bastion ID
- [X] T042 [P] [US1] Create infra/oci/terraform/iam.tf (research.md R22):
  - user and group `kafka-runner`
  - an API key from `var.kafka_runner_api_public_key` (the public half only; the key is created on the control center)
  - a policy with only the statements needed for SSH port forwarding sessions scoped by `target.bastion.ocid` on this Bastion and reading its target instances/network metadata (no `instance-agent-plugins` read grant, no `target.bastion-session.username` condition; Constitution Amendment `3.1.0`)
  - record the exact statements in research.md R22
- [X] T043 [US1] Generate infra/oci/terraform/.terraform.lock.hcl with `terraform providers lock -platform=linux_amd64 -platform=linux_arm64`. Extend tests/infra/test_terraform_protections.py with OCI-root cases matching its GCP checks. Add `infra/oci/terraform` to the `terraform-validate` job in .github/workflows/lint.yaml (fmt, init `-backend=false`, validate).
- [X] T044 [P] [US1] Create the Ansible scaffolding:
  - **infra/oci/ansible/ansible.cfg**
  - **infra/oci/ansible/requirements.yml**: collections pinned to exact versions
  - **infra/oci/ansible/inventory/oci.yml**: hosts a1-1, a1-2, micro-1, micro-2; groups `brokers`, `controllers`, `a1`, `micros`; an OCI Bastion `ProxyCommand` that reads the session ID from the gitignored `inventory/bastion.local.yml`; no secrets
- [X] T045 [P] [US1] Implement infra/oci/ansible/roles/base:
  - pinned packages: podman, wireguard-tools, nftables, chrony, openssl, curl
  - turn off automatic package updates (remove `unattended-upgrades` and disable `apt-daily.timer` and `apt-daily-upgrade.timer`), so packages change only through `kafka.rolling-change` with tag `patch` (FR-013)
  - on `micros`: a 1 GB swap file and `vm.swappiness=1`
  - nftables input on `wg0`: accept only TCP 9092 to the VM's VCN address, drop everything else
  - user `kafka` and directories `/var/lib/kafka` and `/etc/kafka/tls` (mode 0700)
- [X] T046 [P] [US1] Implement infra/oci/ansible/roles/wireguard (brokers only):
  - generate the VM key on the VM if absent; the private key never leaves it
  - print the public key for the `wg-peers` pull request
  - render `/etc/wireguard/wg0.conf` with `ListenPort 51820` and one `[Peer]` per slot from infra/oci/ansible/group_vars/brokers/wireguard_slots.yml (`AllowedIPs = 10.201.0.<n+1>/32`)
  - enable `wg-quick@wg0`
  - create infra/oci/ansible/group_vars/brokers/wireguard_slots.yml with placeholders, filled in T059
- [X] T047 [P] [US1] Implement infra/oci/ansible/roles/certs:
  - for each identity whose `key_location` is `vm:<this node>` or `vm:all`, create a P-256 key (mode 0600) and CSR in `/etc/kafka/tls`
  - from the runner (`delegate_to: localhost`, on the control center, with the `cc-kafka-runner` kubeconfig the control center provides), create a `CertificateRequest` in `oci-link` with `issuerRef` `ClusterIssuer/kafka-mtls-ca` and duration 2160h, then wait for Ready
  - install the certificate and CA, and build the keystore and truststore
  - skip when the current certificate has more than 30 days left unless `force_renew=true`
- [X] T048 [US1] Implement infra/oci/ansible/roles/kafka:
  - **Quadlet unit**: `kafka.container` with the image by digest from infra/kafka/cluster.yaml, host network, and volumes `/var/lib/kafka` and `/etc/kafka`
  - **server.properties**, rendered from kafka.generated.yml:
    - `process.roles`, `node.id`, `controller.quorum.bootstrap.servers` (not `controller.quorum.voters`)
    - SSL client listener on `<vcn-ip>:9092`, CONTROLLER on `<vcn-ip>:9093`, `ssl.client.auth=required`
    - CN-to-user `ssl.principal.mapping.rules`
    - `authorizer.class.name=org.apache.kafka.metadata.authorizer.StandardAuthorizer`, `super.users` from the declarations
    - `auto.create.topics.enable=false`, `compression.type=producer`
  - **Memory** (R16): heap 1g on A1, 384m on micro-1, 256m on micro-2. A systemd drop-in sets `MemoryMin=MemoryMax` (1.6G / 680M / 480M), `OOMScoreAdjust=-900`, and `CPUWeight=1000`.
  - **Format** with the T024 flags, only when `meta.properties` is absent. Assert that an existing `meta.properties` carries the committed cluster ID.
- [X] T049 [US1] Implement infra/oci/ansible/roles/topics_acls (`run_once`, on a1-1, with the `broker-1` identity):
  - create declared topics if missing
  - converge their configs with `kafka-configs --alter`
  - add declared ACLs and remove undeclared ones
  - a second run reports no changes
- [X] T050 [US1] Write infra/oci/ansible/site.yml with `serial: 1` and controllers first:
  - roles in order: base, wireguard, certs, kafka, then topics_acls once
  - between hosts, wait (15-min timeout) until every broker is registered and `kafka-topics --describe --under-replicated-partitions` is empty, run on the VM with the `ops-cli` identity
- [X] T051 [P] [US1] Write infra/oci/ansible/renew-certs.yml, which runs daily:
  - it runs the certs role, which renews only certificates with 30 days or fewer left (`force_renew=true` renews all, operator only)
  - it restarts Kafka and the ops agent only on VMs whose certificates changed, one VM at a time, with the T050 wait between VMs
- [X] T052 [P] [US1] Write infra/oci/ansible/spike-s1.yml for micro-1 only. It brings up `wg0` with slot 0's peer, opens UDP 51820 in nftables, and runs a plain TCP listener on `10.200.0.13:9092` through `systemd-run`, for Scenario 0.
- [X] T053 [US1] Extend .github/workflows/lint.yaml:
  - `ansible-playbook --syntax-check` for each playbook in infra/oci/ansible/
  - `ansible-lint infra/oci/ansible`
  - `go test ./... && go vet ./...` for services/wg-slot-agent and services/error-pipeline
- [X] T054 [US1] Write the build sections of docs/runbooks/oci-kafka.md:
  - prerequisites: the control center's Kafka actions, its runner keys, and the public halves passed to Terraform
  - the Gate 4 cutover checklist: confirm GKE serves all public traffic and the bundle restore succeeded, then delete `vps-k3s` (FR-027)
  - `terraform plan -out tfplan` / `apply tfplan`, and retrying A1 in other fault domains
  - the `wg-peers` and `wireguard_slots.yml` pull-request steps
  - starting `kafka.site` from the control center
  - break-glass: running `site.yml` from the operator's machine through a Bastion session when the control center is down
  - measuring log loss across the cutover gap (FR-017), with the expected gap at the T001 rate

### Implementation for User Story 1: control center (separate repository)

- [ ] T055 [US1] Add operator-started Kafka actions to ../nacfson-control-center/internal/ops/runner.go:
  - **Actions**: `kafka.site`, `kafka.renew-certs`, `kafka.rolling-change`, `kafka.rebuild-node`, `kafka.replace-controller`, with the arguments and host checks in contracts/ansible-runner.md
  - **Approval**: passkey step-up, as for the existing actions
  - **Run**: `ansible-playbook` from the read-only clone at the exact `main` commit, through an OCI Bastion SSH port forwarding session to each VM's private IP port `22` (internal/ops/bastion.go)
  - **Keys**: OCI API key and SSH key read from `/var/lib/nacfson-cc/credentials/kafka-runner/`
  - **GKE access**: a `cc-kafka-runner` kubeconfig minted by impersonation from the VM's service account
  - **Run lock**: one run at a time
  - **Tests**: in ../nacfson-control-center/internal/ops/ops_test.go
- [ ] T056 [P] [US1] Install `ansible-core` (the version pinned in bootstrap/requirements.txt) and the collections from infra/oci/ansible/requirements.yml in the control-center host bootstrap under ../nacfson-control-center/scripts/, and confirm a four-host run fits the e2-micro's memory (research.md R22)
- [ ] T057 [US1] Operator: set up the runner's identities:
  - create the Google service account `cc-kafka-runner` with `roles/container.clusterViewer`, impersonable by the control-center VM's service account, recorded beside `cc-viewer` in ../nacfson-control-center/README.md
  - create the OCI API key and SSH key on the control center (mode 0600)
  - pass only the public API key to Terraform (`TF_VAR_kafka_runner_api_public_key`)

### Live validation for User Story 1 (Operator)

- [ ] T058 [US1] Operator: run quickstart Scenario 0 (spike S1) during Spec 012 Gate 4 step 2 and record the path taken in docs/runbooks/oci-kafka.md. If it fails, take the fallback:
  - set `WG_MODE=hostroute` in clusters/gke/oci-link/daemonset.yaml
  - add `hostNetwork: true` patches for `DaemonSet/fluent-bit` and `Deployment/ops-relay` in clusters/gke/observability/kustomization.yaml
  - rerun step 3 from a hostNetwork pod
- [ ] T059 [US1] Operator: run quickstart Scenario 6 (live build). Copy each broker's WireGuard public key into clusters/gke/oci-link/wg-peers.yaml and each slot public key from `oci-link/wg-slot-public` into infra/oci/ansible/group_vars/brokers/wireguard_slots.yml through one pull request. Start `kafka.site` from the control center again, then confirm Fluent Bit buffers drain to 0.
- [ ] T060 [US1] Operator: run the Story 1 half of quickstart Scenario 7: `kafka-rehearsal.sh test story1 --target live` with a test `proj-*` namespace, and a 1-hour run at the measured production rate (SC-001)
- [ ] T061 [US1] Operator: run quickstart Scenario 9. Delete the GKE Spot node and confirm the new node holds a slot and delivers within 5 minutes, and the dead node's slot is free within 5 minutes (SC-009).

**Checkpoint**: Logs from GKE reach three OCI VMs over WireGuard and mutual TLS. This is the MVP.

---

## Phase 4: User Story 2 - Ingestion keeps running when one machine is down (Priority: P1)

**Goal**: Any one broker can stop, reboot, or be patched without stopping writes or losing acknowledged records.

**Independent Test**: quickstart Scenario 3 (failover part) on the rehearsal, then Scenario 7 live. Each broker is stopped in turn for 10 minutes: acknowledgements resume within 30 s, nothing acknowledged is lost, and full replication returns within 15 min.

### Tests for User Story 2 ⚠️

- [X] T062 [P] [US2] Write tests/kafka/test_story2.py. The outage length comes from `KAFKA_TEST_OUTAGE_SECONDS`, default 600. It covers:
  - **One broker down** (nodes 1, 2, 3 in turn) under a steady numbered stream: acknowledgement gap ≤ 30 s, zero acknowledged records missing, and under-replicated back to 0 within 15 min of restart (SC-002, SC-003)
  - **Two brokers down**: writes fail with `NOT_ENOUGH_REPLICAS`, the rehearsal Fluent Bit buffers, and there is no loss after one returns
  - **Rolling restart**: restarting one broker at a time never stops ingestion

### Implementation for User Story 2

- [X] T063 [US2] Write infra/oci/ansible/rolling-change.yml (FR-014), which the operator starts with `kafka.rolling-change`. It runs with `serial: 1`, controllers before brokers, and does this per host:
  - pre-check that under-replicated is 0
  - optional `apt` upgrade (tag `patch`)
  - restart (or reboot with tag `reboot`)
  - wait until the node is registered, under-replicated and under-min-ISR are 0, and for controllers `kafka-metadata-quorum describe --status` shows lag 0, before the next host
- [X] T064 [P] [US2] Add the rolling patching section to docs/runbooks/oci-kafka.md:
  - starting `kafka.rolling-change` from the control center
  - what two brokers down looks like
  - how long a full outage the 256 MiB per-node buffer covers at the T001 rate
  - resizing the slot pool and rotating WireGuard slot keys without stopping ingestion (FR-017), as in contracts/wg-slot-agent.md "Changing the pool": add slots to `wg-slot-pool` and `wireguard_slots.yml` and merge, then remove the old slots in a second merge
- [ ] T065 [US2] Operator: run the failover half of quickstart Scenario 7 live (`systemctl stop kafka` through the Bastion, one broker at a time, `kafka-rehearsal.sh test story2 --target live`), and record the results in the runbook

**Checkpoint**: US1 and US2 both pass live. The cluster survives any single broker.

---

## Phase 5: User Story 3 - Losing a VM is a rehearsed repair (Priority: P2)

**Goal**: Rebuilding a lost broker-only VM or controller-only VM is a scripted, timed, practised procedure.

**Independent Test**: quickstart Scenario 4 on the rehearsal, then Scenario 10 live. Micro-1 rejoins as broker 3 with nothing lost, and Micro-2 is swapped in the quorum while writes continue, each in under 60 minutes.

### Tests for User Story 3 ⚠️

- [X] T066 [P] [US3] Write tests/kafka/test_story3.py. It covers:
  - **Broker wipe**: wipe node 3's volume, format it with the committed cluster ID, `node.id` 3, and no initial controllers, then start it. It rejoins the same cluster, partitions return to 3/3, and nothing is lost.
  - **Controller wipe**: wipe node 4's volume, then `remove-controller` with the old directory ID, start node 4 as an observer, and `add-controller`. Three voters return and writes continue throughout.
  - **Empty recreate**: recreate the whole cluster empty. All topics exist with declared settings before Fluent Bit connects, and implicit creation is refused.
  - Save the raw `kafka-metadata-quorum` command outputs to tests/fixtures/kafka-quorum/4.3.2/.
- [X] T067 [P] [US3] Write tests/infra/test_kafka_quorum_module.py. It feeds the captured outputs in tests/fixtures/kafka-quorum/4.3.2/ to the Ansible module from T068 and checks the parsed voters, observers, directory IDs, and lag.

### Implementation for User Story 3

- [X] T068 [US3] Implement the custom Ansible module infra/oci/ansible/library/kafka_quorum.py. It parses `kafka-metadata-quorum describe --status` and `--replication` output into voters, observers, directory IDs, and lag, so T067 passes.
- [X] T069 [US3] Write infra/oci/ansible/rebuild-node.yml, limited to one broker host:
  - roles base, wireguard, certs, kafka
  - format with the committed cluster ID and the same `node.id`, never with `--initial-controllers`
  - refuse to continue if `meta.properties` holds a different cluster ID
  - print the new WireGuard public key and pause for the `wg-peers` pull request before opening 9092
  - wait until under-replicated is 0
- [X] T070 [US3] Write infra/oci/ansible/replace-controller.yml for one controller host. Using `kafka_quorum`, it:
  - stops the old voter if it still runs
  - records its directory ID
  - runs `remove-controller --controller-id N --controller-directory-id <old>`
  - runs base, certs, kafka on the new VM and starts it as an observer
  - runs `add-controller`
  - asserts 3 voters with lag 0
- [X] T071 [P] [US3] Add the rebuild sections to docs/runbooks/oci-kafka.md:
  - pausing automation before a rebuild and resuming it afterwards
  - broker rebuild (Terraform `-target` for one instance, then `kafka.rebuild-node`)
  - controller replacement (`kafka.replace-controller`)
  - what to watch while running on 2 of 3
  - retrying A1 when out of host capacity
  - a timing table for drill results
- [ ] T072 [US3] Operator: run quickstart Scenario 10. Terminate Micro-1 and rebuild it with `kafka.rebuild-node`, then do the same for Micro-2 with `kafka.replace-controller`. Record each duration (SC-004: under 60 min) and confirm no records were lost.

**Checkpoint**: Both rebuild paths are proven on the rehearsal and live.

---

## Phase 6: User Story 4 - Operator sees cluster health continuously (Priority: P2)

**Goal**: Event-based alerts (one message per state change), 30 s metrics, trend warnings, and a live `ops-relay status` view, with nothing exposed publicly.

**Independent Test**: quickstart Scenario 3 (alerts and live-view parts) on the rehearsal, then Scenario 7 alerts and Scenario 8 port scan live.

### Fixtures and tests for User Story 4 ⚠️

- [X] T073 [US4] Implement `capture-logs` in scripts/kafka-rehearsal.sh. It triggers broker fence and unfence, ISR shrink and expand, a controller leader change, and a `kafka.service` stop on the rehearsal, then copies the raw Kafka 4.3.2 log lines unchanged into tests/fixtures/kafka-logs/4.3.2/ (one file per kind). Write in tests/fixtures/kafka-logs/README.md that these files are captured, never edited.
- [X] T074 [P] [US4] Write tests/kafka/test_ops_events_lua.py. It runs `fluent/fluent-bit:4.2.8` with infra/oci/ansible/roles/ops_agent/files/ops_events.lua over each fixture file and compares stdout events with tests/fixtures/ops-events/expected/*.json (schema 1, `kind`, `state`, `subject`, `vm`, `observed_by`, `source`).
- [X] T075 [P] [US4] Write services/error-pipeline/internal/opsrelay/state_test.go with a fake clock, fed with the expected events from T074. It covers:
  - **Messages per change**: one message per transition `OK→DOWN→OK`, and repeats in the same state send nothing
  - **Grouping**: events from a VM with an open incident are grouped into it
  - **Silence**: heartbeat silence of 180 s raises DOWN `vm/<vm>`, labelled "broker down" when `kafka.brokers.active` dropped and "agent silent, broker healthy" otherwise
  - **Start-up wait**: no silence verdict within 180 s of start
  - **Unreachable**: `cluster/unreachable` goes DOWN after 60 s without a connection, holds back silent-VM and stale-tunnel transitions, and its message is sent directly
  - **Tunnel**: a `TunnelStale` Event raises one DOWN `gke/<node>/tunnel` and `TunnelRecovered` clears it; a high `wireguard.handshake_age` metric raises nothing
  - **Rebuild**: rebuilding from 24 h of events and 6 h of metrics re-sends nothing already resolved and prefixes late messages `(delayed, event at HH:MM UTC)`
- [X] T076 [P] [US4] Write services/error-pipeline/internal/opsrelay/trend_test.go with synthetic series. It covers:
  - `os.disk.free_ratio` slope over 6 h projecting a crossing of 0.20 within 72 h raises exactly one WARN `disk/<vm>`
  - `fluentbit.buffer.bytes` above 50% of 256 MiB raises WARN `gke/<node>/buffer`
  - the disk trend stays silent for a VM with less than 6 h of samples
- [X] T077 [P] [US4] Write services/error-pipeline/internal/opsrelay/status_test.go, which checks the text and `--json` output against the example in contracts/ops-relay.md
- [X] T078 [P] [US4] Write services/wg-slot-agent/internal/metrics/metrics_test.go. It covers summing a temp `chunks` tree, parsing captured `wg show wg0 latest-handshakes` output into the max age, and writing both Lease annotations.

### Implementation for User Story 4: VM side

- [X] T079 [P] [US4] Add the JMX exporter to infra/oci/ansible/roles/kafka:
  - the jar pinned with SHA-256 from T006, mounted as a `-javaagent`, bound to `127.0.0.1:7071`
  - an allowlist in roles/kafka/files/jmx-exporter.yaml limited to the metrics in contracts/CONTRACT_OPS_EVENTS.md
  - confirm that `MemoryMax` from T048 still covers the exporter (R16)
- [X] T080 [P] [US4] Implement infra/oci/ansible/roles/samplers:
  - **files/ops-sample.sh**: reads `127.0.0.1:7071`, `/proc/meminfo`, `df /var/lib/kafka`, and `/proc/stat` steal, and appends one metric JSON line per metric to `/var/lib/ops-agent/samples/metrics.jsonl`. It also writes `disk.low`/`disk.ok` events only on a 20% crossing, keeping crossing state in `/var/lib/ops-agent/state`.
  - **files/ops-certs.sh**: writes `cert.expiring` when fewer than 14 days are left and `cert.renewed` after renewal
  - **Timers**: `ops-sample.timer` (30 s) and `ops-certs.timer` (daily)
  - **Size cap**: a cap on the samples files
- [X] T081 [US4] Implement infra/oci/ansible/roles/ops_agent:
  - **Quadlet unit**: `ops-agent.container` running `fluent/fluent-bit:4.2.8` by digest, `MemoryMax` 64M on A1 and 48M on micros
  - **files/fluent-bit.conf** inputs:
    - `systemd` for `kafka.service`, `wg-quick@wg0.service`, and `ops-agent.service`
    - `tail` on `/var/lib/ops-agent/samples/*.jsonl`
    - `dummy` with a 60 s heartbeat
  - **files/fluent-bit.conf** outputs: two `kafka` outputs with mutual TLS as `ops-agent-<vm>`, to `ops.platform.events` (key `subject`) and `ops.platform.metrics` (key `source`), with a 64 MB filesystem buffer
  - **files/ops_events.lua**: implements the mapping so T074 passes
  - Add the role to site.yml after `topics_acls`.

### Implementation for User Story 4: relay and slot agent

- [X] T082 [US4] Implement services/error-pipeline/internal/kafkaclient (franz-go):
  - mutual TLS from mounted files
  - a consumer group reader
  - a read-from-timestamp reader for the rebuild (24 h of events, 6 h of metrics)
  - an admin describe-cluster call every 30 s
- [X] T083 [US4] Implement services/error-pipeline/internal/opsrelay so T075–T077 pass:
  - **state.go**: per-subject state, incidents, timers
  - **trend.go**
  - **rebuild.go**
  - **kube.go**: a standard-library watch of `oci-link` Events (`NoFreeSlot`, `TunnelStale`, `TunnelRecovered`) and Leases (annotations become `gke/<node>` metrics)
  - **status.go**
- [X] T084 [US4] Implement services/error-pipeline/cmd/ops-relay/main.go:
  - `serve` sends through the existing services/error-pipeline/internal/notify to `vault-notify-proxy` in the format of contracts/ops-relay.md
  - the Unix socket `/run/ops-relay/status.sock`
  - `status [--json]`
  - a readiness file written after the rebuild, for an exec probe
  - liveness is the process only
- [X] T085 [P] [US4] Write services/error-pipeline/Dockerfile.ops-relay (static, non-root) and add an `ops-relay` entry to the matrix in .github/workflows/build-images.yaml for amd64 and arm64
- [X] T086 [US4] Implement services/wg-slot-agent/internal/metrics and call it from the renew loop in cmd/wg-slot-agent/main.go, writing `oci-link.nacfson.io/fluent-bit-buffer-bytes` and `oci-link.nacfson.io/handshake-age-seconds` every 30 s, so T078 passes
- [ ] T087 [US4] Create clusters/gke/observability/ops-relay.yaml:
  - **Deployment**: 1 replica, image by digest, requests = limits 10m/64Mi, `emptyDir` at `/run/ops-relay`, exec readiness and liveness probes
  - **ServiceAccount, Role, RoleBinding**: a Role and RoleBinding in `oci-link` for events and leases get/list/watch
  - **Certificate**: `Certificate/ops-relay` (issuer `kafka-mtls-ca`, CN `ops-relay`, 2160h/720h)
  - **NetworkPolicy**: egress to `10.200.0.11/32`, `.12/32`, `.13/32` TCP 9092, `vault-notify-proxy` 8080, the Kubernetes API, and DNS, with no ingress
  - Add the file to clusters/gke/observability/kustomization.yaml. Allow ingress from `observability/ops-relay` to `vault-notify-proxy` in deploy/platform/vault/network-policy.yaml. `render-validate.sh gke` and preflight pass.
- [X] T088 [US4] Write tests/kafka/test_alerts.py, running the relay binary against the rehearsal with a fake notify endpoint. It covers each case in quickstart Scenario 3:
  - one DOWN and one OK per stopped broker
  - a broker restarted within 2 minutes gives exactly one DOWN and one OK
  - an agent stopped for 4 minutes gives one "agent silent, broker healthy"
  - cutting the relay's network gives one `cluster/unreachable` and no silent-VM messages
  - `ops-relay status` shows the stopped broker within 60 s
  - a synthetic disk-fill series gives one WARN
  - SC-011, SC-013, SC-014
- [X] T089 [P] [US4] Add the health section to docs/runbooks/oci-kafka.md:
  - `kubectl -n observability exec deploy/ops-relay -- ops-relay status`
  - the meaning of each alert
  - VM-side fallback commands run with the `ops-cli` identity (FR-029)
  - reading the last hour of `ops.platform.metrics`

### Live validation for User Story 4 (Operator)

- [ ] T090 [US4] Operator: on Micro-1 after the live build, measure the resident memory of Kafka and the ops agent. If headroom is below 50 MB (research.md R16 gate), replace the ops agent on micro-1 with a `kcat`-based shipper that sends the same contract records, as a host variant of infra/oci/ansible/roles/ops_agent.
- [ ] T091 [US4] Operator: run quickstart Scenario 8, `nmap -Pn -p 1-65535` and `nmap -sU -p 51820` against each reserved IP from outside OCI and GCP (SC-005). Confirm the alert and live-view expectations during the T065 live failover runs (SC-011, SC-014).

**Checkpoint**: Health is visible continuously, and each incident sends exactly one message.

---

## Phase 7: User Story 6 - Merged changes and renewals apply themselves (Priority: P2)

**Goal**: Merges to `main` and daily certificate renewals reach the VMs with no operator action, one VM at a time, with one alert for a failed or blocked run.

**Independent Test**: quickstart Scenario 12. A merged harmless change is applied within 60 minutes. Nothing runs while paused. A merge while a broker is down produces one `DeployBlocked` alert and no run.

### Tests for User Story 6 ⚠️

- [ ] T092 [P] [US6] Write ../nacfson-control-center/internal/ops/kafka_auto_test.go with a fake GitHub API, a fake clock, and a fake Kubernetes API. It covers:
  - **Merge trigger**: a run starts only for a `main` commit whose required checks all succeeded and whose changed paths include `infra/kafka/` or `infra/oci/ansible/`
  - **Applied commit**: no run when every VM's applied commit already matches
  - **Daily**: one renewal run per day
  - **Pause**: no automatic run while paused; operator actions still run
  - **Lock**: a second run waits for the first
  - **Results**: a failed `preflight.yml` creates `DeployBlocked` and runs no playbook; a failed task creates `DeployFailed` naming the VM and the step; success creates `DeployOK`
- [X] T093 [P] [US6] Add a `deploy/<playbook>` case to services/error-pipeline/internal/opsrelay/state_test.go: `DeployFailed` and `DeployBlocked` are DOWN, `DeployOK` is OK, and there is one message per state change

### Implementation for User Story 6

- [X] T094 [US6] Add the run rules from contracts/ansible-runner.md to infra/oci/ansible/site.yml and infra/oci/ansible/renew-certs.yml:
  - **Pre-flight**: `preflight.yml`, imported first by every playbook, fails when a broker is missing or any partition is under-replicated
  - **Plays**: `any_errors_fatal: true`
  - **Last task per VM**: writes the commit to `/etc/nacfson/applied-commit` (mode 0644)
- [ ] T095 [US6] Implement ../nacfson-control-center/internal/ops/kafka_auto.go so T092 passes:
  - **Merge watcher**: polls the public GitHub API for `main` every 5 minutes, unauthenticated
  - **Required checks**: confirms they succeeded
  - **Changed paths**: compared since each VM's applied commit
  - **Runs**: `kafka.site` on a matching merge; `kafka.renew-certs` daily
  - **Pause switch**: in the UI, behind the passkey step-up
  - **Result events**: `DeployOK`, `DeployFailed`, `DeployBlocked` in `oci-link` with the contract's message format
- [X] T096 [US6] Handle `DeployOK`, `DeployFailed`, and `DeployBlocked` Events as subject `deploy/<playbook>` in services/error-pipeline/internal/opsrelay/kube.go, so T093 passes
- [X] T097 [P] [US6] Add an "Automated runs" section to docs/runbooks/oci-kafka.md:
  - which change triggers which playbook
  - pausing and resuming
  - reading a failed or blocked run
  - what stays manual
  - rotating the runner's OCI and SSH keys every 90 days
  - revoking them if leaked
- [ ] T098 [US6] Operator: run quickstart Scenario 12, record the run times against SC-015, and record the first unattended certificate renewal in docs/runbooks/oci-kafka.md

**Checkpoint**: Merged changes and certificate renewals reach the VMs on their own, and a failed or blocked run alerts once.

---

## Phase 8: User Story 5 - Rehearse the cluster locally before it touches OCI (Priority: P3)

**Goal**: One command runs the Story 1 and Story 2 tests on a local three-broker rehearsal built from the same declarations as production.

**Independent Test**: `./scripts/kafka-rehearsal.sh up && ./scripts/kafka-rehearsal.sh test all` passes with the same topic settings as production.

- [X] T099 [P] [US5] Write tests/infra/test_rehearsal_parity.py. It checks that the compose render and the ansible render carry identical topic, broker, and ACL settings (they differ only in addresses and certificate paths), and that the rehearsal Fluent Bit configuration equals clusters/gke/observability/fluent-bit-kafka.conf except broker addresses and certificate paths.
- [X] T100 [US5] Add `test all` to scripts/kafka-rehearsal.sh, which runs story1, story2, story3, alerts, and security in order with one command (FR-016), and add a `--target live` passthrough and usage text
- [X] T101 [US5] Write tests/kafka/test_security.py (quickstart Scenario 5, SC-010). A client without a CA-signed certificate is refused, `fluent-bit-gke` cannot read any topic or write `ops.platform.events` or an undeclared topic, and all three refusals appear in broker logs.
- [X] T102 [US5] Run quickstart Scenarios 1–5 locally, then record the results and the rehearsal instructions in a "Rehearsal" section of docs/runbooks/oci-kafka.md

**Checkpoint**: Every story's test passes on the rehearsal before anything is created in OCI.

---

## Phase 9: Polish & Cross-Cutting Concerns

- [X] T103 [P] Update specs/012-gke-platform-migration/spec.md FR-011 ("shrunk or recreated" → "recreated") and specs/012-gke-platform-migration/quickstart.md Gate 4 (Scenario 0 at step 2, and deleting `vps-k3s` through the docs/runbooks/oci-kafka.md checklist)
- [X] T104 [P] Update ARCHITECTURE.md: remove "OCI VM 4", show Spec 014 sharing the A1 VMs, and add the Kafka node layout and the WireGuard path
- [X] T105 [P] Update the Spec 013 card in docs/platform-architecture.html from plan to built status, and run `python3 scripts/check-arch-doc-sync.py --worktree`
- [X] T106 [P] Add to docs/runbooks/oci-kafka.md:
  - **Spec 014 handoff**: about 14 GB disk per A1, the R16 memory reservations, `OOMScoreAdjust>=500` and `CPUWeight=100` for Spec 014 units, the consumer identity format in infra/kafka/cluster.yaml, and `error-detector` moving to internal/kafkaclient
  - **Move-to-GKE outline** (FR-025 point 6)
  - **First-month cost check** (quickstart Scenario 11)
- [X] T107 Run the no-comments review over every new code, unit, template, and generated file, and remove any comment found
- [X] T108 Run all offline gates: quickstart Scenario 1 and the five gates in CLAUDE.md, with `./scripts/render-validate.sh gke` in place of the retired `vps-k3s` render where that environment no longer exists
- [ ] T109 Operator: after the first full billing month, run quickstart Scenario 11. Check the OCI cost report shows $0, the $1 budget alert never fired, and Google Cloud egress to OCI is under $1 (SC-006).

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: T001 blocks T010, because the measured rate sets `retention_bytes`. T002 blocks T038. T003 blocks T018. The other setup tasks have no dependencies.
- **Foundational (Phase 2)**: depends on Setup, and blocks every story.
- **US1 (Phase 3)**: depends on Foundational. Its live tasks (T058–T061) also depend on Spec 012 Gate 4.
- **US2 (Phase 4)**: depends on US1. It tests the cluster US1 builds. T062 can be written while US1 is in progress.
- **US3 (Phase 5)**: depends on the US1 roles (T045–T049). Rehearsal parts can run in parallel with US2.
- **US4 (Phase 6)**: depends on Foundational and the US1 slot agent (T032) and roles (T048). T073 needs the rehearsal from T024. The live tasks need T059.
- **US6 (Phase 7)**: depends on the US1 control-center actions (T055) and runner identities (T057, T035, T042), and on the relay from US4 (T083). Its live task needs T059.
- **US5 (Phase 8)**: depends on Foundational. T100 and T102 depend on the story suites existing (T026, T062, T066, T088, T101).
- **Polish (Phase 9)**: after the stories you deliver. T109 is a month after T059.

### Within each story

- Tests first (they must fail), then the code they cover, then manifests and playbooks, then runbook text, then live operator runs
- Live tasks in this order: Scenario 0 (T058), then build (T059), then ingestion (T060), node replacement (T061), failover (T065), alerts and scan (T091), automated runs (T098), and finally the rebuild drill (T072) with automation paused

### Parallel Opportunities

- Setup: T004–T009 together, once the operator inputs T001–T003 are in hand
- Foundational: T011, T016, T019, T021, and T022 alongside the `kafka-declarations.py` work. T012–T015 run in sequence because they edit the same file.
- US1:
  - tests T026–T028 together
  - T029 and T033 together
  - Terraform T038 and T041 together with the Ansible roles T045–T047
  - the GKE side (T029–T037) and the OCI side (T038–T054) are independent until the live build
  - the control-center work (T055, T056) runs in its own repository alongside both
- US6: T092 and T093 together, then T094–T096
- US4: T074–T078 together, then T079 and T080 alongside the relay work (T082–T084)
- US3 and US4 rehearsal work can run alongside US2's live run

---

## Parallel Example: User Story 1

```bash
Task: "Write tests/kafka/test_story1.py"
Task: "Write services/wg-slot-agent/internal/slots/claim_test.go"
Task: "Write services/wg-slot-agent/internal/wg/config_test.go"

Task: "Create infra/oci/terraform/versions.tf, backend.tf, locals.tf, variables.tf"
Task: "Implement infra/oci/ansible/roles/base"
Task: "Implement infra/oci/ansible/roles/wireguard"
Task: "Implement infra/oci/ansible/roles/certs"
```

## Parallel Example: User Story 4

```bash
Task: "Write tests/kafka/test_ops_events_lua.py"
Task: "Write services/error-pipeline/internal/opsrelay/state_test.go"
Task: "Write services/error-pipeline/internal/opsrelay/trend_test.go"
Task: "Write services/error-pipeline/internal/opsrelay/status_test.go"
Task: "Write services/wg-slot-agent/internal/metrics/metrics_test.go"
```

---

## Implementation Strategy

### MVP first (User Story 1)

1. Phase 1, including the three operator inputs
2. Phase 2: it can be merged on its own, harmless with no cluster
3. Phase 3 offline work, proven on the rehearsal (Scenario 2)
4. Merge the GKE manifests. Fluent Bit keeps buffering until the brokers exist.
5. At Spec 012 Gate 4: Scenario 0, then the live build (T058–T061)
6. **Stop and validate**: SC-001 live

### Incremental delivery

1. US1: durable ingestion (MVP)
2. US2: failover proven live, with the rolling procedure
3. US4: alerts and live view. Run it before the US3 drill, so the drill's alerts are checked too.
4. US6: automated runs and renewals. Needs the US4 relay for its alerts, and must be live before the first certificates reach day 60.
5. US3: rebuild drill
6. US5: one-command rehearsal parity. Most of the rehearsal already exists from Phase 2.

### Gates that can change the design

- **T001**: the log rate can shrink retention below 7 days, or the disk left for Spec 014
- **T022**: if the preflight memory freeze fails, the overage is resolved before merging
- **T058**: if spike S1 fails, the hostNetwork fallback is used
- **T090**: low Micro-1 memory replaces its ops agent with the `kcat` shipper

## Notes

- `[P]` = different files and no dependency on unfinished work
- Commit after each task or logical group, and ask the operator about the review gate before each commit (REVIEW.md)
- AI agents never run commands on the VMs or the live cluster. The operator runs every task marked Operator, and the control center runs Ansible.
- Paths starting with `../nacfson-control-center/` are in the separate private repository. Those tasks are committed and reviewed there.
