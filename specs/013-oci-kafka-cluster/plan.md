# Implementation Plan: OCI Replicated Kafka Log Ingestion Cluster

**Branch**: `013-oci-kafka-cluster` | **Date**: 2026-11-17 | **Spec**: [spec.md](spec.md)

**Input**: Feature specification from `specs/013-oci-kafka-cluster/spec.md`

## Summary

Build the receiving side of the GKE Fluent Bit Kafka output: a Kafka 4.3.2 KRaft cluster on the four OCI Always Free VMs.
- **Roles**: brokers on A1-1, A1-2, and Micro-1; controller voters on A1-1, A1-2, and Micro-2.
- **Durability**: `logs.*` topics keep 3 copies and need 2 in sync.
- **Runtime**: Kafka runs as a pinned container under Podman and systemd.
- **Changes**: the operator creates the VMs with Terraform under `infra/oci/`. Ansible configures them, one VM at a time, run by the control center (`nacfson-control-center`). It runs automatically on a merge to `main` and daily for certificate renewal, and on the operator's request for patches and rebuilds (FR-031).

GKE reaches the brokers through WireGuard. A small `wg-slot-agent` DaemonSet claims one of four pre-made key slots per node and source-NATs pod traffic to the slot address. Every Kafka connection uses mutual TLS from one cert-manager CA, with deny-by-default ACLs. VM certificates are signed by the same CA: the runner submits each VM's signing request as a `CertificateRequest` in `oci-link` (R8, R22).

Health is covered three ways:
- **Events**: an ops agent on each VM (the same pinned Fluent Bit) writes Kafka, systemd, disk, and certificate events plus a heartbeat to `ops.platform.events`.
- **Metrics**: every 30 s each VM samples its metrics into `ops.platform.metrics`.
- **Relay**: `ops-relay` in GKE (in the existing `error-pipeline` module) turns events into one Discord message per state change, watches the slot agents, and serves a live `status` view.

Topics, identities, and ACLs are declared once in `infra/kafka/cluster.yaml`. The same file renders the Ansible variables, the local rehearsal, and Strimzi resources, so a later move into GKE stays possible (FR-025).

## Technical Context

**Language/Version**:
- Go 1.26 for `ops-relay` (the `error-pipeline` module moves from 1.22, because franz-go needs it) and Go 1.22 for `wg-slot-agent`
- Python 3.11+ for `scripts/kafka-declarations.py`, the rehearsal runner, and tests
- HCL for Terraform `>= 1.9.0, < 2.0.0`
- Ansible (`ansible-core` pinned in `bootstrap/requirements.txt`)
- Lua for the Fluent Bit event rules
- POSIX shell for the VM samplers (no comments in any of these files, per the constitution)

**Primary Dependencies**:
- `apache/kafka:4.3.2` (R1)
- `fluent/fluent-bit:4.2.8` (R13)
- Prometheus JMX exporter 1.x, newest release at least two weeks old (R14)
- `github.com/twmb/franz-go`, newest release at least two weeks old (R17)
- Podman with Quadlet, WireGuard, and nftables on Ubuntu 24.04 Minimal (R2, R3)
- `oracle/oci` Terraform provider, minor version pinned, with lock files for `linux_amd64` and `linux_arm64`
- cert-manager (existing) for the Kafka CA (R8)
- Strimzi 1.3.1 JSON schemas for render-only validation (R10)
- The control center (private repository `nacfson-control-center`, Go standard library, GCP e2-micro), extended with Kafka actions and the automation of R22. It runs `ansible-core` at the version pinned in `bootstrap/requirements.txt`.

**Record size**: Fluent Bit's Kafka output does not set `message.max.bytes`, so librdkafka's default of 1,000,000 bytes caps every record it sends, including joined multiline records. Larger records fail in the producer, not on the broker. Each topic's `max_message_bytes` is 1,048,588 (Kafka's default), which is above that cap plus the record-batch overhead.

**Storage**: Kafka data on each VM's 50 GB boot volume (`/var/lib/kafka`), within a 20 GB per-broker budget (R12). Terraform state for `infra/oci/terraform` lives in Spec 015's existing GCS bucket `nacfson-pipeline-tfstate`, prefix `oci` (R21). There are no other stores, and the relay keeps state in memory, rebuilt from the topic.

**Testing**:
- Go unit tests for the relay rules (fed with captured Kafka log fixtures) and for the slot agent's claim logic (fake API server)
- `python3 -m unittest discover tests/infra` (declarations check, SC-008, SC-012)
- `tests/preflight` (new `oci-link` rule)
- `./scripts/render-validate.sh gke`
- `terraform validate`
- `ansible-playbook --syntax-check` and `ansible-lint`
- The Podman rehearsal (quickstart Scenarios 2–5)
- Go unit tests in the control-center repository for the automation triggers, pause, lock, and result events (R22)
- Live Scenarios 0 and 6–12

**Target Platform**:
- OCI region `ap-seoul-1` (R4) with 2 × `VM.Standard.A1.Flex` (1 OCPU / 6 GB, arm64) and 2 × `VM.Standard.E2.1.Micro` (amd64)
- GKE (Spec 015) for the slot agent and the relay

**Project Type**: Infrastructure as code (Terraform and Ansible), two small Go services, GitOps manifests, governance changes, and a local rehearsal harness

**Performance Goals**:
- acknowledgements resume within 30 s after a broker loss (SC-002)
- full replication within 15 min (SC-003)
- alerts within 10 min and typically under 20 s (FR-023)
- live view values no older than 60 s (SC-014)
- rebuild under 60 min (SC-004)

**Constraints**:
- $0 OCI, guarded by a $1 budget alert (FR-026)
- 200 GB total storage (R12)
- 1 GB micro VMs (R16)
- no public TCP ports (SC-005)
- no private key leaves the place it was made (FR-024)
- Fluent Bit output changes only for TLS and zstd (FR-004)
- no comments in code
- the GKE platform reservation must still pass the Interim Memory Limit Freeze (R16)
- the runner may not create, change, or delete OCI resources, and Terraform stays operator-run (Clarification 2.4.1)

**Scale/Scope**:
- 4 VMs, 4 topics, about 9 Kafka identities, 4 key slots
- 2 Go binaries, 1 declarations file with a 3-target renderer
- 1 Terraform root plus its bootstrap, 1 Ansible tree with 7 roles and 4 playbooks
- 2 GKE manifest directories, 1 constitution amendment, 1 runbook

All unknowns are resolved in [research.md](research.md) (R1–R22). Items that need a live check are marked **Verify at implementation** there and become task gates. No `NEEDS CLARIFICATION` remains.

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-checked after Phase 1 design (below).*

Evaluated against `.specify/memory/constitution.md` v2.4.1.

| Principle / Gate | Assessment | Result |
|---|---|---|
| **I. Declarative GitOps** | GKE objects (slot agent, keygen Job, relay, Certificates, ClusterIssuer, NetworkPolicies, runner RBAC) are Flux-reconciled. OCI resources are Terraform from a reviewed saved plan, run by the operator (Clarification 2.4.1). VM configuration is Ansible from Git, run by the control-center runner on a merge to `main` (R22). That runner is an automated apply path, which Clarification 2.4.1 allows only through an amendment, so Amendment 2.5.0 defines it. No human writes to the cluster: the runner identity creates `CertificateRequest`s and Events in `oci-link` at runtime, like the slot agent's Leases. Third-party images are pinned to exact tags; the relay and slot agent images by digest. | PASS with Amendment 2.5.0 |
| **II. Workload Isolation & Restricted PSS** | Project namespaces are untouched. `oci-link/wg-slot-agent` needs `hostNetwork` and `NET_ADMIN`, which is a platform workload, not a project one, and preflight admits it on that DaemonSet only (R19). | PASS with Amendment 2.5.0 |
| **III. Defense-in-Depth Identity** | No broker, controller, metrics, or SSH port is public. Operator and runner SSH both go through OCI Bastion. The runner uses its own OCI credential, which may only open sessions on that Bastion, and an SSH key created on the control center. The live view is reached only through `kubectl exec` (FR-028). Mutual TLS with ACLs on every Kafka connection. | PASS |
| **IV. Bounded Resources & 1/n** | Relay, slot agent, and keygen Job declare requests equal to limits. The GKE platform reservation grows by about 10m/64Mi plus 10m/32Mi per node, and `platform-preflight` must still pass the memory freeze (R16 gate). On the VMs, systemd limits protect Kafka (FR-021). | PASS, gated on preflight |
| **V. Zero Plaintext Secrets** | Backend credentials stay in OpenBao, and the Discord webhook stays behind `vault-notify-proxy`. New key material held as Kubernetes Secrets: WireGuard slot keys (`oci-link`), Kafka client keys (`observability`), and the Kafka CA key (`cert-manager`). Principle V does not allow these today. Like the edge TLS exception, they are generated in the cluster, never in Git, and limited to platform namespaces. | PASS with Amendment 2.5.0 |
| **VI. Portability** | Location-neutral declarations, a Strimzi render validated in CI, and an OCI-only layer that can be deleted (FR-025). | PASS |
| **VII. Lifecycle signals** | No project frontend or backend. The relay's liveness is process-local and its readiness means state is rebuilt. | N/A (pattern kept) |
| **Capacity & Budget Verification** | `platform-preflight` counts the new workloads, and `clusters/gke/capacity.yaml` reservation is updated. | PASS |
| **Multi-Architecture Images** | `ops-relay` and `wg-slot-agent` are built for amd64 and arm64 and pushed to the public registry by digest. | PASS |
| **Automated Manifest Validation** | `render-validate.sh gke` covers `oci-link`; kubeconform covers the Strimzi render with pinned schemas. | PASS |
| **Repository Secret Hygiene** | Only public keys, the CA certificate, and the cluster ID are committed. State, plans, `tfvars`, and the inventory's Bastion session details are gitignored. | PASS |
| **No Comments in Code** | Applies to the Lua, shell, Terraform, Ansible, Quadlet units, Go, and rendered files. Explanations go to the runbook and this plan. | PASS |

**Post-design re-check (after Phase 1)**: PASS. Phase 1 added three things. The `ops.platform.metrics` topic and its exporter bind only to localhost, so there is no new exposure. The Lease annotations need no new identity. The S1 `hostNetwork` fallback is included in Amendment 2.5.0 before it is needed. No new violations.

## Cross-Spec Changes (not constitutional violations)

| Change | Why | Where recorded |
|---|---|---|
| Spec 012 FR-011 "shrunk or recreated" becomes "recreated" | FR-027 (clarify Q5) | Spec 012 `spec.md` FR-011, `quickstart.md` Gate 4 |
| "OCI VM 4" removed; Spec 014 shares the A1 VMs | Clarify Q1 | `ARCHITECTURE.md`, Spec 012 text, `docs/platform-architecture.html` |
| Fluent Bit Kafka output gains TLS and zstd | FR-004, FR-024 | `clusters/gke/observability/fluent-bit-kafka.conf`, `contracts/CONTRACT_LOG_SCHEMA.md` |
| Spec 014 inherits about 14 GB disk and the memory reservations per A1 VM, consumer identities, and the Kafka client | R12, R16, R17 | handoff section of `docs/runbooks/oci-kafka.md` |
| The control center gains Kafka actions, the merge watcher and daily schedule, a pause switch, the `cc-kafka-runner` GCP identity, and stored runner keys | FR-031, R22 | `nacfson-control-center` repository; this repository records the contract in [contracts/ansible-runner.md](contracts/ansible-runner.md) |

## Project Structure

### Documentation (this feature)

```text
specs/013-oci-kafka-cluster/
├── spec.md
├── plan.md                          # this file
├── research.md                      # R1–R22
├── data-model.md
├── quickstart.md                    # Scenarios 0–11
├── contracts/
│   ├── ansible-runner.md            # control-center actions, triggers, permissions, result events
│   ├── kafka-declarations.md
│   ├── ops-events-and-metrics.md    # copied to contracts/CONTRACT_OPS_EVENTS.md during implementation
│   ├── ops-relay.md
│   └── wg-slot-agent.md
├── checklists/requirements.md
└── tasks.md                         # created by /speckit-tasks
```

### Source Code (repository root)

```text
infra/kafka/
└── cluster.yaml                         # location-neutral declarations (contracts/kafka-declarations.md)

infra/oci/
├── terraform/
│   ├── versions.tf  backend.tf  locals.tf   # backend gcs, prefix "oci"; locals read infra/kafka/cluster.yaml
│   ├── network.tf                       # VCN 10.200.0.0/16, subnet /24, security list (UDP 51820 only inbound)
│   ├── compute.tf                       # 4 instances, fixed private IPs, fault domains, 50 GB boot volumes
│   ├── public_ips.tf                    # reserved public IPs for the 3 brokers
│   ├── bastion.tf                       # OCI Bastion for operator and runner SSH
│   ├── iam.tf                           # kafka-runner user, group, Bastion-session-only policy (R22)
│   ├── budget.tf                        # $1 budget + alert rule (FR-026)
│   ├── outputs.tf
│   └── .terraform.lock.hcl
└── ansible/
    ├── site.yml                         # full build and every merged change, serial: 1, refuses a degraded cluster
    ├── rolling-change.yml               # one VM at a time, waits for under-replicated = 0
    ├── rebuild-node.yml                 # broker rebuild (Story 3)
    ├── replace-controller.yml           # remove-controller / add-controller (R11)
    ├── renew-certs.yml                  # daily: CSR → CertificateRequest → install, only past day 60 (R8)
    ├── spike-s1.yml                     # Scenario 0 only
    ├── inventory/oci.yml                # Bastion ProxyCommand, no secrets
    ├── group_vars/all/kafka.generated.yml
    ├── group_vars/brokers/wireguard_slots.yml  # slot public keys and addresses
    └── roles/
        ├── base/                        # packages, swap on micros, sysctl, nftables, podman
        ├── wireguard/
        ├── certs/
        ├── kafka/                       # Quadlet unit, properties, format, JMX exporter
        ├── topics_acls/
        ├── ops_agent/                   # Quadlet unit, fluent-bit.conf, ops_events.lua
        └── samplers/                    # ops-sample / ops-certs timers and scripts

services/error-pipeline/
├── cmd/ops-relay/                       # main, status subcommand
├── internal/opsrelay/                   # state machine, grouping, timers, trend rules, rebuild
├── internal/kafkaclient/                # franz-go wrapper shared with Spec 014
└── Dockerfile.ops-relay

services/wg-slot-agent/                  # Go standard library only
├── go.mod
├── cmd/wg-slot-agent/
├── internal/{slots,kube,wg,metrics}/
└── Dockerfile

clusters/gke/
├── oci-link/                            # namespace, keygen Job, DaemonSet, RBAC, runner RBAC, wg-peers ConfigMap, NetworkPolicy
├── observability/
│   ├── fluent-bit-kafka.conf            # + TLS, + zstd
│   ├── fluent-bit-kafka-tls.yaml        # Certificate + volume patch
│   ├── ops-relay.yaml                   # Deployment, SA, Role, Certificate, NetworkPolicy
│   └── kustomization.yaml
├── cert-manager/kafka-mtls-ca.yaml      # self-signed root + ClusterIssuer/kafka-mtls-ca
└── layers.yaml                          # + Flux Kustomizations kafka-ca and oci-link

deploy/platform/governance/manual-change-policy.yaml   # three narrow allowances (R19)
../nacfson-control-center/                             # separate repository: Kafka actions, automation, runner identity (R22)
deploy/platform/vault/…                                 # vault-notify-proxy NetworkPolicy: allow ops-relay
scripts/
├── kafka-declarations.py                # render | check
├── kafka-rehearsal.sh                   # up | test <suite> | capture-logs | down
└── platform-preflight.py                # oci-link namespace + DaemonSet rule
tests/
├── infra/test_kafka_declarations.py
├── kafka/                               # rehearsal suites: story1, story2, story3, alerts, security
├── fixtures/kafka-logs/4.3.2/           # captured, never hand-written
└── preflight/test_platform_preflight.py # oci-link cases
contracts/CONTRACT_OPS_EVENTS.md
docs/runbooks/oci-kafka.md               # build, cutover, rebuild, rolling change, health, certs, move-to-GKE outline
.specify/memory/constitution.md          # Amendment 2.5.0
.github/workflows/lint.yaml              # terraform validate (oci), ansible-lint, declarations check, go tests
ARCHITECTURE.md, docs/platform-architecture.html, specs/012-gke-platform-migration/{spec,quickstart}.md
```

**Structure Decision**: OCI infrastructure goes under `infra/oci/`, next to `infra/gcp/` (Spec 015). The location-neutral Kafka declarations get their own `infra/kafka/`, so nothing OCI-specific sits in the portable core (FR-025). The relay joins `services/error-pipeline` because it shares `notify` and, after Spec 014, the Kafka client with `error-detector`. The slot agent is its own standard-library module, like `vault-proxies`, and is deleted with `clusters/gke/oci-link/` after a move.

## Implementation Order (input for `/speckit-tasks`)

1. **Measure first**: the operator runs the R12 log-rate commands, and the values set `retention_bytes` and `max_message_bytes`.
2. **Offline foundation**: `infra/kafka/cluster.yaml`, `kafka-declarations.py` (render and check) with tests, the contract copied to `contracts/`, CI job. Mergeable on its own.
3. **Rehearsal and fixtures**: `kafka-rehearsal.sh` and the compose render. Pass Scenario 2 and capture the real log fixtures.
4. **Services**: `ops-relay` (rules tested on fixtures) and `wg-slot-agent` (claim logic tested on a fake API). Pass Scenarios 3–5 on the rehearsal.
5. **Governance**: Amendment 2.5.0, `manual-change-policy` allowances, preflight `oci-link` rule and tests, capacity reservation, `render-validate.sh gke`.
6. **GKE manifests**: `oci-link/`, the observability additions, the Kafka CA. Merged but harmless until brokers exist (Fluent Bit keeps buffering).
7. **OCI code**: Terraform roots and the Ansible tree, validated offline (`validate`, `--syntax-check`, `ansible-lint`).
8. **Control center**: the Kafka actions (operator-started first), the runner identity, then the merge watcher, daily renewal, pause switch, and result events (R22).
9. **Docs**: runbook covering:
   - build, and the cutover checklist with the `vps-k3s` deletion
   - rebuild and controller replacement
   - rolling change and cert renewal
   - automated runs and the runner's key rotation
   - health
   - the move outline

   Also: Spec 012 and `ARCHITECTURE.md` updates, and the handbook.
10. **Live**: Scenario 0 at Gate 4 step 2, then Scenarios 6–10 and 12 after the restore. Scenario 11 after the first month.

## Complexity Tracking

| Exception | Why Needed | Simpler Alternative Rejected Because |
|---|---|---|
| Amendment 2.5.0: `wg-slot-agent` with `hostNetwork` and `NET_ADMIN` in `oci-link` | A WireGuard interface and routes must exist in each GKE node's network namespace for pod traffic to reach OCI | Tailscale or Headscale (third-party SaaS, or a coordination single point of failure, rejected in clarify); managed VPN (cost) |
| Amendment 2.5.0: WireGuard slot keys and Kafka client keys as Kubernetes Secrets; Kafka CA key in `cert-manager` | The kernel and librdkafka need the key material in the pod; same reasoning as the edge TLS exception (Amendment 2.2.0) | OpenBao delivery still ends in a Secret or a file in the pod, and makes log shipping depend on Vault unseal |
| Amendment 2.5.0 and `manual-change-policy`: the runner identity `cc-kafka-runner` may create `CertificateRequest`s and Events in `oci-link` | VM keys must be generated on the VM (FR-024); only the CSR travels to the cluster's CA, and renewal must run unattended (FR-031) | Flux-managed `Certificate` for VMs (the key would be made in the cluster and copied out); CSRs committed through a pull request (renewal every 60 days would wait for a merge); a second CA outside the cluster (breaks FR-025 point 4) |
| Amendment 2.5.0: automated Ansible path from the control center (Clarification 2.4.1) | Certificates expire every 90 days, and merged changes should reach the VMs without a hand-run step | Operator-run Ansible (renewal depends on memory); GitHub Actions (OCI credentials in a public repository's CI); a Job in GKE (OCI SSH keys inside the cluster); `ansible-pull` on the VMs (forbidden by FR-013) |
| Stored runner keys on the control center | Unattended runs need an OCI credential and SSH key at any time, where today the control center keeps keys in RAM for 15 minutes | Keys pasted per run (no automation); a broader OCI credential (the stored one may only open sessions on one Bastion) |
| `manual-change-policy`: slot agent Leases and Events, keygen Secret and ConfigMap in `oci-link` | Slot claiming and key generation are runtime writes by platform ServiceAccounts | Static per-node keys in Git (private keys in Git, forbidden) |
| Third-party Go module (`franz-go`) in `error-pipeline` | Go's standard library has no Kafka protocol | Writing a Kafka client (unjustified); the Confluent client (cgo and librdkafka in the image) |
