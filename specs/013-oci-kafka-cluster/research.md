# Research: OCI Replicated Kafka Log Ingestion Cluster

**Feature**: [spec.md](spec.md) | **Plan**: [plan.md](plan.md) | **Date**: 2026-11-17

Each item records a decision, why it was made, and what was rejected. Items marked **Verify at implementation** name the exact check a task must run before relying on the decision. No `NEEDS CLARIFICATION` remains.

## R1: Kafka version and how it is shipped

- **Decision**: Apache Kafka **4.3.2**, run from the official `apache/kafka:4.3.2` container image (a JVM image with its own Java runtime), pinned by exact tag with the multi-arch digest recorded beside it.
- **Rationale**:
  - Kafka 4.x is KRaft-only, which FR-002 requires.
  - 4.3.2 was released on 2026-10-20, more than two weeks ago, and is the newest patch of the 4.3 line.
  - Strimzi 1.2.0, the newest Strimzi release, supports Kafka 4.3.x. FR-025 renders the same declarations as Strimzi resources, so the OCI cluster and a future GKE cluster stay on a version both can run.
  - One image for both ARM64 (A1) and x86_64 (micro) satisfies FR-011 without managing two Java installs.
- **Alternatives considered**:
  - Kafka 4.4.0 (released 2026-10-13): no Strimzi release supports it yet, which would break the FR-025 portability render.
  - Apache tarball plus a distribution JDK: two architectures, two JDK package pins, and no single digest to record.
  - `apache/kafka-native` (GraalVM): smaller, but upstream marks it as not for production.
- **Verify at implementation**: Confirm on Docker Hub that `apache/kafka:4.3.2` publishes `linux/amd64` and `linux/arm64`, and record the index digest. Confirm the newest Strimzi release's Kafka support at kafka.apache.org/downloads and the Strimzi release notes.
- Sources: [endoflife.date: Apache Kafka](https://endoflife.date/apache-kafka), [Strimzi releases](https://github.com/strimzi/strimzi-kafka-operator/releases).

## R2: Runtime on the VMs

- **Decision**: Podman with systemd Quadlet units. Each VM runs `kafka.container` and `ops-agent.container`. Memory and CPU limits are set in systemd (`MemoryMin`, `MemoryMax`, `CPUWeight`, `OOMScoreAdjust`).
- **Rationale**:
  - Podman has no daemon, so the 1 GB micro VMs pay only for the containers themselves.
  - Quadlet makes each container an ordinary systemd unit, so failures appear in the journal, where the event agent reads them (R13), and systemd restarts them.
  - Images are pinned the same way as in the cluster (Constitution Principle I).
- **Alternatives considered**: Docker (a resident daemon of about 50–100 MB, too much on 1 GB VMs); k3s with Strimzi (rejected in clarify Q3).

## R3: Operating system image

- **Decision**: Canonical Ubuntu 24.04 Minimal platform images (aarch64 for A1, amd64 for micro), selected in Terraform by exact display name, with the resolved image OCID recorded as an output. The micro VMs get a 1 GB swap file with `vm.swappiness=1` as an out-of-memory cushion.
- **Rationale**: The smallest supported footprint for a 1 GB VM. Podman and WireGuard are in the standard repositories, and WireGuard is in the kernel.
- **Alternatives considered**: Oracle Linux 9 (heavier base and agents); Oracle Linux 9 Slim (fewer packaged tools).

## R4: Region, fault domains, and VM placement

- **Decision**: The home region is assumed to be `ap-seoul-1` (one availability domain, three fault domains). Placement:

  | VM | Shape | Fault domain | Kafka roles | Private IP |
  |---|---|---|---|---|
  | A1-1 | VM.Standard.A1.Flex 1 OCPU / 6 GB | FD-1 | broker 1 + controller 1 | 10.200.0.11 |
  | A1-2 | VM.Standard.A1.Flex 1 OCPU / 6 GB | FD-2 | broker 2 + controller 2 | 10.200.0.12 |
  | Micro-1 | VM.Standard.E2.1.Micro | FD-3 | broker 3 | 10.200.0.13 |
  | Micro-2 | VM.Standard.E2.1.Micro | FD-3 | controller 4 | 10.200.0.14 |

- **Rationale**: The three voters (1, 2, 4) and the three brokers (1, 2, 3) each sit in three different fault domains. Losing FD-3 removes Micro-1 and Micro-2 together, leaving 2 brokers and 2 of 3 voters, so writes continue. Putting Micro-2 in FD-1 instead would make FD-1 hold two voters, and losing it would stop the quorum.
- **Verify at implementation**: The operator confirms the tenancy's home region in the OCI Console. If it is not `ap-seoul-1`, only the Terraform region variable changes; the fault-domain layout stays.
- **Idle reclamation (accepted risk, 2026-11-18)**: Oracle may reclaim an Always Free VM whose CPU, network, and memory (memory counts on A1 only) all stay under 20% for 7 days. Micro-2, and possibly Micro-1, could qualify. A 2022 Oracle notice and user reports say pay-as-you-go tenancies like this one are not reclaimed, but the current documentation does not say so. The maintainer accepted the risk without safeguards for now. If it happens, it is handled as a VM loss: the heartbeat alert fires, and the VM is rebuilt per Story 3. Safeguards (a fixed Kafka heap, an early CPU warning) are revisited only if it occurs in production.

## R5: OCI network and operator access

- **Decision**:
  - One VCN `10.200.0.0/16` with one public subnet `10.200.0.0/24`. Each VM has a fixed private IP (R4) and a public IP.
  - A1-1, A1-2, and Micro-1 have **reserved** public IPs, so the WireGuard endpoints survive a rebuild. Micro-2 has an ephemeral public IP used only for outbound image and package downloads.
  - The security list allows inbound UDP 51820 (WireGuard) to the three brokers from anywhere, and TCP 9092–9093 only from inside `10.200.0.0/24`. Nothing else is inbound. **No TCP 22.**
  - SSH for the operator and for the control-center runner (R22) uses the **OCI Bastion service** with time-limited managed SSH sessions. Ansible connects through it with a `ProxyCommand`. Managed sessions need the Oracle Cloud Agent's Bastion plugin turned on in each instance.
  - Each VM's host firewall (nftables) allows only 9092 on `wg0` and drops everything else arriving on it.
- **Rationale**:
  - GKE nodes have ephemeral external IPs (Spec 015), so the WireGuard port cannot be limited by source address. WireGuard ignores packets without a valid key, so leaving it open reveals nothing.
  - Bastion removes the only other public port, which makes SC-005 stronger, and it fits Principle III's definition of private, authenticated access: IAM sign-in, then a key held only by the operator.
  - A public subnet avoids a NAT gateway.
- **Alternatives considered**: SSH open to the internet (allowed by Clarification 2.3.1, but needless exposure); a private subnet with a NAT gateway (more resources, and not needed).
- **Verify at implementation**: That the Bastion service and reserved public IPs are free on this pay-as-you-go tenancy. The FR-026 budget alert also catches this in the first month.

## R6: WireGuard topology, key slots, and the Dataplane V2 question

- **Decision**:
  - **Topology**: Every GKE node peers with each of the three broker VMs, so it has three peers. Routes on the node: `10.200.0.11/32` through A1-1's peer, `.12` through A1-2's, `.13` through Micro-1's. Losing one broker VM leaves the other two routes working, so there is no single point of failure (FR-008). Micro-2 runs no WireGuard.
  - **Slot addresses**: `10.201.0.0/28`, room for slots 0–13, with slots 0–3 in the pool at start (ConfigMap `wg-slot-pool`, declared in Git). GKE runs 2 nodes plus at most 1 being replaced, and one slot is spare. This range does not overlap Spec 015's ranges (`10.20.0.0/22`, `10.24.0.0/17`, `10.28.0.0/22`) or `10.200.0.0/16`.
  - **Source address**: On each GKE node, the slot agent adds a source-NAT rule so traffic leaving `wg0` carries the slot address. That is the only address the OCI side knows for that peer. Replies come back through WireGuard, and connection tracking reverses the translation.
  - **Claiming a slot**: one Lease `wg-slot-<n>` per pooled slot in namespace `oci-link`. Duration 90 s, renewed every 30 s, holder = node name. A lease is taken over when it has expired, or when its holder node no longer exists in the Node API. That releases a dead node's slot within 5 minutes (SC-009).
- **Rationale**: The fixed slots match clarify Q1. Source NAT means OCI never needs to know GKE's changing pod or node addresses.
- **Open risk (spike S1, must pass before the slot agent is built)**: GKE Dataplane V2 (`ADVANCED_DATAPATH` in Spec 015) handles pod traffic in eBPF. GKE documents that IP masquerading still uses iptables rules through `ip-masq-agent` on Dataplane V2, which suggests a POSTROUTING rule on `wg0` will apply, but this exact path is not documented. Spike S1 (quickstart Scenario 0) proves it on the real cluster before restore.
- **Fallback if S1 fails**: Run Fluent Bit and the alert relay with `hostNetwork: true`, and add a route `10.200.0.0/24 dev wg0 src <slot address>`. Traffic generated by the host takes the route's source address with no NAT. This is a smaller manifest change, already covered by the FR-009 amendment, and the plan keeps both manifests ready.
- **Alternatives considered**: Tailscale and Headscale (rejected in clarify); one WireGuard gateway pod (a single point of failure).
- Sources: [GKE IP masquerade agent](https://docs.cloud.google.com/kubernetes-engine/docs/concepts/ip-masquerade-agent).

## R7: Slot agent implementation and key generation

- **Decision**:
  - `wg-slot-agent` is a Go program using only the standard library, matching `services/vault-proxies`. It runs as a DaemonSet in `oci-link` with `hostNetwork: true`, the capability `NET_ADMIN`, and read-only `hostPath: /var/lib/fluent-bit-state` (to measure buffer fill).
  - It calls the Kubernetes API (Leases, Nodes, Events) with its ServiceAccount token, and drives `wg` and `nft` from `wireguard-tools` and `nftables` in its image.
  - **Key generation**: A Flux-managed Job `wg-slot-keygen` reads the slot list in `wg-slot-pool`. It creates a private key in Secret `wg-slot-keys` for each listed slot that has none, never overwrites an existing key, and removes the keys of slots no longer listed once they are free. It writes the public keys and addresses to ConfigMap `wg-slot-public`. Flux re-runs the Job whenever the slot list changes. This makes resizing and key rotation a merged change (FR-022). The operator copies the public keys into the Ansible variables through a pull request (`infra/oci/ansible/group_vars/brokers/wg_slots.yml`).
- **Rationale**: Private keys are created in the cluster and only public keys leave it (FR-009). No third-party Go modules are needed for the Kubernetes calls.
- **Alternatives considered**: client-go (heavy dependency for four API calls); generating keys on the operator's machine (the private keys would leave their place of use).

## R8: Mutual TLS certificate authority and certificates

- **Decision**:
  - cert-manager creates a self-signed root, used as `ClusterIssuer/kafka-mtls-ca`. Its key Secret lives in `cert-manager`, next to the existing ACME account keys.
  - **In-cluster clients**: Flux-managed `Certificate` objects for `fluent-bit-gke` and `ops-relay` in `observability`, valid 90 days and renewed after 60 (`renewBefore: 720h`). Both are mounted read-only.
  - **VM identities** (`broker-N`, `controller-N`, `ops-agent-<vm>`, `ops-cli`): the Ansible `certs` role creates the key and a CSR on the VM. The runner (R22) then submits a `CertificateRequest` in `oci-link` as the `cc-kafka-runner` identity, waits for cert-manager to sign it, and installs the certificate. `renew-certs.yml` runs daily and repeats this only for certificates past day 60 (30 days or fewer left). The agent warns at 14 days (FR-023).
  - **Identity mapping**: `ssl.principal.mapping.rules` maps the certificate CN to the Kafka user (`User:<CN>`).
- **Rationale**: Every private key is made where it is used (FR-024), and one CA serves both clouds, so the certificates do not depend on where the brokers run (FR-025). The runner submits requests itself, so renewal runs unattended.
- **Governance consequence**: `manual-change-policy` blocks writes to namespaced objects by anyone it does not allow. A narrow allowance is needed: the `cc-kafka-runner` identity may `CREATE` `certificaterequests.cert-manager.io` and `events` in `oci-link` only. No human writes to the cluster. The allowance goes into Amendment 2.5.0 (R19).
- **Who can get a certificate**: cert-manager's default approver signs every request that names `kafka-mtls-ca`. Only the runner (in `oci-link`) and Flux-managed `Certificate`s in `observability` may create requests. Preflight already rejects certificate resources in project namespaces.
- **Alternatives considered**:
  - Flux-managed `Certificate` objects for broker certificates (the key would be made in the cluster and copied to the VM, which breaks FR-024)
  - CSRs committed through a pull request for Flux to apply (no exception needed, but renewal would wait for a merge every 60 days)
  - a separate CA on the operator's machine (a second root, and FR-025 point 4 would break after a move)

## R9: ACLs and authorization

- **Decision**: KRaft `StandardAuthorizer`, `allow.everyone.if.no.acl.found=false`, `super.users` = the broker and controller identities only. ACLs:

  | Identity | Allowed | Resource |
  |---|---|---|
  | `fluent-bit-gke` | Write, Describe | topics `logs.proj.valid`, `logs.proj.quarantine` |
  | `fluent-bit-gke` | IdempotentWrite | cluster |
  | `ops-agent-a1-1` … `ops-agent-micro-2` | Write, Describe | topics `ops.platform.events`, `ops.platform.metrics` |
  | `ops-agent-*` | IdempotentWrite | cluster |
  | `ops-relay` | Read, Describe | topics `ops.platform.events`, `ops.platform.metrics` |
  | `ops-relay` | Read | group `ops-relay` |
  | `ops-relay` | Describe, DescribeConfigs | cluster, all topics |
  | `ops-cli` | Describe, DescribeConfigs | cluster, all topics, all groups |

- **Rationale**: Deny by default, with exactly the grants FR-024, FR-028, and FR-029 describe.

## R10: Declarations in Git and the portability render

- **Decision**: One location-neutral file, `infra/kafka/cluster.yaml`, holds the cluster ID, node IDs and roles, topics, identities, ACLs, broker settings, and per-environment bootstrap lists. `scripts/kafka-declarations.py` renders it three ways:
  - `ansible`: variables for the OCI VMs
  - `compose`: the local rehearsal (R15)
  - `strimzi`: `Kafka`, `KafkaNodePool`, `KafkaTopic`, and `KafkaUser` YAML. This is never applied; kubeconform validates it against pinned Strimzi 1.2.0 JSON schemas.

  The `check` subcommand implements FR-015 and SC-012. It fails on:
  - a Fluent Bit broker list or topic that differs from the declarations
  - replication factor below 3, or minimum in-sync below 2, on `logs.*` topics
  - missing Fluent Bit TLS settings
  - any producer ACL beyond writing its two topics
  - a Strimzi render that fails its schema
- **Rationale**: One source of truth, a CI-enforced move path, and the existing kubeconform tooling.
- **Notes**: The cluster ID is generated once with `kafka-storage random-uuid` and committed. It is an identifier, not a secret.

## R11: Controller quorum and rebuilds

- **Decision**: Use the **dynamic** controller quorum (KIP-853), bootstrapped with `kafka-storage format --initial-controllers` on the three voters and `controller.quorum.bootstrap.servers` set (not `controller.quorum.voters`).
  - **Rebuilding a broker-only VM**: format the empty disk with the committed cluster ID and the same `node.id`, then start it. Kafka copies its replicas back.
  - **Rebuilding a controller VM**: stop the lost voter if it still runs, `kafka-metadata-quorum remove-controller --controller-id N --controller-directory-id <old>`, start the rebuilt node so it joins as an observer, then `add-controller`. One controller at a time.
  - The `replace-controller.yml` playbook scripts these steps. The Story 3 drill exercises both cases.
- **Rationale**: A static voter list cannot safely replace a controller whose disk was lost, and Story 3 requires that repair.
- **Verify at implementation**: Run the full controller replacement on the local rehearsal (quickstart Scenario 4) and capture the real command output as test fixtures.
- Sources: [KIP-853](https://cwiki.apache.org/confluence/display/KAFKA/KIP-853:+KRaft+Controller+Membership+Changes), [Kafka 4.3 KRaft operations](https://kafka.apache.org/43/operations/kraft/).

## R12: Storage, retention, and how many days fit

- **Decision**: All four boot volumes are 50 GB (exactly 200 GB). There are no separate block volumes, and Kafka data lives on the boot volume under `/var/lib/kafka`. Per-broker Kafka budget is **20 GB**:

  | Topic | Partitions | `retention.ms` | `retention.bytes` per partition | Per broker |
  |---|---|---|---|---|
  | `logs.proj.valid` | 3 | 7 days | 5.5 GB | 16.5 GB |
  | `logs.proj.quarantine` | 3 | 7 days | 1 GB | 3 GB |
  | `ops.platform.events` | 1 | 30 days | 256 MB | 0.25 GB |
  | `ops.platform.metrics` | 1 | 7 days | 256 MB | 0.25 GB |

  `segment.bytes` is 128 MB, so the active segment overshoots its limit by at most 128 MB per partition. Fluent Bit compresses with zstd (FR-004), and brokers keep `compression.type=producer`, so they never re-compress, which saves the micro VMs' CPU.
- **Arithmetic**: A 50 GB volume minus about 6 GB for the OS and images, minus 20 GB for Kafka, leaves about 24 GB. The 20% free rule (10 GB) leaves **about 14 GB on each A1 VM for Spec 014** and nothing else needed on the micros. Spec 014 must fit VictoriaLogs and Flink state in that space, or use OCI Object Storage. This is recorded as a handoff.
- **How many days fit**: `days = 16.5 GB ÷ (raw bytes per day ÷ zstd ratio)`. With JSON logs at a zstd ratio of 5, 16.5 GB holds 7 days for up to about 11.8 GB of raw logs per day.
- **Measurement (operator, read-only, before tasks are cut)**: With `scripts/open-observe.sh` running, query the current store's last 30 days:

  ```bash
  curl -s 'http://127.0.0.1:9428/select/logsql/query' \
    --data-urlencode 'query=_time:30d | stats count() lines, sum_len(_msg) msg_bytes'
  ssh oracleCloud "sudo k3s kubectl -n observability exec victorialogs-0 -- du -sh /victoria-logs-data"
  ```

  Raw bytes per day ≈ `msg_bytes ÷ 30 × 2` (JSON field overhead). Record the numbers in `docs/runbooks/oci-kafka.md`.

## R13: Event agent pipeline on the VMs

- **Decision**: `ops-agent.container` runs Fluent Bit **4.2.8**, the same pinned version as GKE (FR-023). Inputs:
  - `systemd` input for `kafka.service`, `wg-quick@wg0.service`, and `ops-agent.service`, both unit state changes and container log lines (Podman writes them to the journal)
  - `tail` on `/var/lib/ops-agent/samples/*.jsonl`, written by two systemd timers (`ops-sample.timer` every 30 s, `ops-certs.timer` daily). Their small shell scripts read `df`, `/proc/meminfo`, the localhost metrics exporter (R14), and the certificate expiry dates
  - `dummy` input with `interval_sec 60` for the heartbeat

  A Lua filter maps Kafka log lines and journal records to the event schema. Rules live in `infra/oci/ansible/roles/ops-agent/files/ops_events.lua`, and threshold-crossing state lives in Lua. Two `kafka` outputs with mutual TLS send to `ops.platform.events` and `ops.platform.metrics`, buffered on disk (64 MB cap).
- **Rationale**:
  - The official Fluent Bit image is distroless, so the `exec` input has no shell to run. Timers outside the container write samples that the agent tails, which keeps the image unchanged.
  - Using the same Fluent Bit version as GKE means one set of known behaviours.
- **Test fixtures**: Kafka log lines (broker fenced and unfenced, ISR shrink and expand, controller changes) are captured from `apache/kafka:4.3.2` during the local rehearsal (Scenario 2), never typed by hand. This follows the repository's lesson from the Spec 012 cutover: guessed stubs let three bugs through.
- **Alternatives considered**:
  - Fluent Bit metrics inputs with OTLP output to Kafka: the 5.0 documentation lists OTLP metrics support for the Kafka output, but 4.2.8's support is unconfirmed, and it would split the record format in two.
  - A custom Go agent: more code to own on 1 GB VMs.

## R14: Continuous metrics (FR-030)

- **Decision**: The Prometheus JMX exporter runs as a Java agent inside the Kafka container, bound to `127.0.0.1:7071`, with a short allowlist of metrics:
  - active and fenced broker counts
  - under-replicated, under-min-ISR, and offline partitions
  - produce request p99 latency
  - bytes in and out
  - JVM heap used and GC time
  - active controller count

  `ops-sample.sh` reads it every 30 s with `curl`, adds OS figures, and writes one JSON line per metric in the contract's format. GKE-side metrics (Fluent Bit buffer bytes and WireGuard handshake age) are written by `wg-slot-agent` as annotations on its Lease every 30 s. The relay already watches the Leases, so no new Kafka identity is needed in GKE.
- **Rationale**: The exporter is the standard way to read Kafka's JMX metrics without exposing JMX. Bound to localhost, nothing new listens publicly (SC-005).
- **Cost**: About 20–40 MB inside each Kafka JVM, counted in R16.
- **Verify at implementation**: Pin the newest JMX exporter 1.x release that is at least two weeks old, and confirm it supports Java 21.

## R15: Local rehearsal (FR-016)

- **Decision**: `scripts/kafka-rehearsal.sh up|test|down` runs Podman Compose with four Kafka containers in the exact R4 roles (node IDs 1–4), one Fluent Bit with the GKE output configuration (rendered for the local CA), one ops agent, and the relay binary. The CA and certificates are generated per run in a temporary directory, never committed. `tests/kafka/` (Python `unittest`, standard library) drives Stories 1–3:
  - numbered records
  - stopping and starting brokers
  - wiping a broker's volume
  - the controller replacement
  - the relay's messages, captured by a local fake notify endpoint
- **Rationale**: Problems surface before the Gate 4 window, and real log fixtures get captured.

## R16: Memory and CPU budgets (FR-012, FR-020, FR-021)

| VM | OS | Kafka (heap / `MemoryMax`) | Exporter | Ops agent | Spec 014 (reserved) | Headroom |
|---|---|---|---|---|---|---|
| A1-1 6 GB | 0.5 GB | 1 GB / 1.6 GB | in Kafka | 64 MB | Flink 2.5 GB | ≈ 1.3 GB |
| A1-2 6 GB | 0.5 GB | 1 GB / 1.6 GB | in Kafka | 64 MB | Flink + VictoriaLogs 3 GB | ≈ 0.8 GB |
| Micro-1 1 GB (+1 GB swap) | 0.2 GB | 384 MB / 680 MB | in Kafka | 48 MB | none | ≈ 70 MB |
| Micro-2 1 GB (+1 GB swap) | 0.2 GB | 256 MB / 480 MB | in Kafka | 48 MB | none | ≈ 270 MB |

- `kafka.service` sets `MemoryMin` equal to its `MemoryMax`, `OOMScoreAdjust=-900`, and `CPUWeight=1000`. Spec 014 units must use `OOMScoreAdjust>=500` and `CPUWeight=100` (FR-021).
- **GKE**: `ops-relay` requests and limits 10m / 64Mi (one replica). `wg-slot-agent` 10m / 32Mi per node. The `wg-slot-keygen` Job uses 10m / 32Mi and runs only when the slot list changes. All are added to the platform reservation in `clusters/gke/capacity.yaml`.
- **Risk**: Micro-1 has the least headroom. If the measured resident memory leaves less than 50 MB, the ops agent on Micro-1 is replaced by a `kcat`-based shipper with the same contract (a task gate).
- **Verify at implementation**: `platform-preflight` must still pass the Interim Memory Limit Freeze with the larger GKE reservation. If it does not, the overage is resolved before the merge.

## R17: Alert relay (FR-023, FR-028)

- **Decision**: The relay is `services/error-pipeline/cmd/ops-relay`, in the same Go module as `error-detector`, reusing its `notify` package for `vault-notify-proxy`. Spec 014 moves `error-detector` to Kafka, so both share one Kafka client.
  - **Kafka client**: `github.com/twmb/franz-go`, pinned to its newest release that is at least two weeks old. Go's standard library has no Kafka protocol.
  - **Behaviour**: a state per subject, incident grouping, timers (3-minute heartbeat silence, 60-second connection loss, 3-minute start-up wait), rebuilding state from 24 hours of `ops.platform.events` and 6 hours of `ops.platform.metrics`, a Kubernetes watch on `oci-link` Events and Leases, and trend rules on metrics.
  - **Live view**: `ops-relay status` talks to a Unix socket inside the pod (FR-028).
  - **Probes**: liveness is the process only. Readiness means state has been rebuilt.
- **Rationale**: Reuses the existing notify path and an error-pipeline module that Spec 014 extends anyway.
- **Alternatives considered**: A new module with its own copy of `notify`; the Confluent Go client (cgo and librdkafka).

## R18: Budget guard (FR-026)

- **Decision**: An `oci_budget_budget` for the tenancy at $1 per month with an actual-spend alert rule at 100%. The recipient email comes from `TF_VAR_budget_alert_email`, never committed.
- **Rationale**: On a pay-as-you-go tenancy, a resource outside the free allowance costs money without any other signal.

## R19: Governance and constitution changes

- **Constitution Amendment 2.5.0** (MINOR, new platform exceptions):
  1. `oci-link/wg-slot-agent` may use `hostNetwork`, `NET_ADMIN`, and read-only `hostPath: /var/lib/fluent-bit-state`.
  2. WireGuard slot private keys may be held as Secret `oci-link/wg-slot-keys`, created in the cluster, never in Git.
  3. Kafka client keys for `fluent-bit-gke` and `ops-relay` may be held as cert-manager Secrets in `observability`, and the Kafka CA key in `cert-manager`.
  4. If spike S1 fails, `fluent-bit` and `ops-relay` may use `hostNetwork`.
  5. **Automated Ansible path** (Clarification 2.4.1). The control center's runner applies OCI VM configuration:
     - **Identity**: `cc-kafka-runner` in GKE and `kafka-runner` in OCI
     - **Scope**: allowlisted playbooks against the four Kafka VMs
     - **Approval**: a merge to `main` whose required checks passed
     - **Triggers**: merges and a daily certificate renewal
     - **Limits**: the runner cannot create, change, or delete OCI resources, and Terraform stays operator-run
  6. The runner identity may create `CertificateRequest`s and Events in `oci-link`. Its private keys are created on the control center and kept there, never in Git.

  Project namespaces are unchanged.
- **`manual-change-policy`**:
  - allow `system:serviceaccount:oci-link:wg-slot-agent` to update Leases and create Events in `oci-link` only
  - allow `system:serviceaccount:oci-link:wg-slot-keygen` to create Secret `wg-slot-keys` and ConfigMap `wg-slot-public` only
  - allow the `cc-kafka-runner` Google service account to `CREATE` `certificaterequests` and `events` in `oci-link` only
- **`platform-preflight`**: add `oci-link` to `PLATFORM_NAMESPACES`, add a rule that admits those host rights only on that DaemonSet, and extend the observability rule for the S1 fallback.

## R20: Cross-spec updates

- Spec 012 FR-011: "shrunk or recreated" becomes "recreated", per FR-027.
- `ARCHITECTURE.md` and Spec 012: the separate "OCI VM 4" goes away, and Spec 014 shares the A1 VMs.
- Spec 014 handoff: about 14 GB of disk per A1 VM (R12), the memory reservations (R16), consumer identities added with the declaration format (R10), and `error-detector` moving to the Kafka client (R17).
- `nacfson-control-center`: Kafka actions, automation, and the runner identity (R22). The contract between the two repositories is [contracts/ansible-runner.md](contracts/ansible-runner.md).

## R21: Terraform state for the OCI root

- **Decision**: Store state in Spec 015's existing GCS bucket `nacfson-pipeline-tfstate` (versioned, public access prevention) under prefix `oci`. The operator runs Terraform from their own machine with the OCI API key in `~/.oci/config` (never in Git) and their Google sign-in for the state.
- **Rationale**: One state home with versioning already exists. It needs no second bootstrap root and no OCI Object Storage access keys. Keeping state outside the cloud it describes is common practice.
- **Alternatives considered**:
  - OCI Object Storage through Terraform's S3-compatible backend: needs Customer Secret Keys, one more credential.
  - Local state: not versioned, and lost with the laptop.

## R22: Automated Ansible runs from the control center

- **Decision**: The control center (`nacfson-control-center`, an always-on GCP e2-micro outside both clouds) runs the Ansible playbooks. Contract: [contracts/ansible-runner.md](contracts/ansible-runner.md).
  - **Automatic**: `site.yml` runs when a commit on `main` passed its required checks and changes `infra/kafka/` or `infra/oci/ansible/`. `renew-certs.yml` runs daily.
  - **Operator-started**: `rolling-change.yml`, `rebuild-node.yml`, and `replace-controller.yml`, each confirmed with the control center's passkey step-up.
  - **Merge watcher**: polls the public GitHub API for `main` every 5 minutes, unauthenticated, about 24 requests per hour against a limit of 60. It compares the changed paths with the commit last applied on each VM (`/etc/nacfson/applied-commit`).
  - **Safety**:
    - one run at a time, with a lock shared with operator-started actions
    - a pre-flight play refuses a degraded cluster and reports `DeployBlocked`
    - `serial: 1` with the full-replication wait between VMs
    - `any_errors_fatal`, so the run stops at the first failure
    - a pause switch behind the passkey step-up
  - **Reporting**: Kubernetes Events `DeployOK`, `DeployFailed`, and `DeployBlocked` in `oci-link`, with the commit, playbook, VM, and failed step. `ops-relay` already watches Events in `oci-link`, so a failure alerts even when OCI is unreachable. The control center's UI keeps the run log.
  - **GKE identity**: Google service account `cc-kafka-runner`, impersonated by the control-center VM's service account. It has IAM `roles/container.clusterViewer` to sign in, plus a Kubernetes Role in `oci-link` (`clusters/gke/oci-link/runner-rbac.yaml`) to create, get, and list `certificaterequests` and create `events`. The service account and its IAM live in the control-center repository, like `cc-viewer`.
  - **OCI identity**: user and group `kafka-runner` from `infra/oci/terraform/iam.tf`, with the smallest policy the OCI Bastion documentation lists for creating managed SSH sessions on this one Bastion. The API key and the SSH key are created on the control center, and only their public halves go to Terraform. They are rotated every 90 days (runbook).
  - **Stays manual**: Terraform, the one-time slot key exchange, and the `wg-peers` pull request after a broker rebuild.
- **Rationale**:
  - A missed renewal takes a VM out of the cluster after 90 days, so renewal must not depend on someone remembering.
  - The control center already runs privileged actions with an audit trail and opens OCI Bastion tunnels.
  - Being outside both clouds, it keeps working when GKE or OCI is degraded.
  - Approval stays with the merge, and the replication wait keeps one-VM-at-a-time safety without a person watching.
- **Alternatives considered**:
  - Operator-run Ansible: renewal depends on memory.
  - GitHub Actions: OCI credentials in a public repository's CI, and pull requests from forks are a risk.
  - A Job in GKE: OCI SSH keys inside the cluster, and the runner would depend on the cluster it reports about.
  - `ansible-pull` on the VMs: forbidden by FR-013.
  - Fully event-driven runs from Flux notifications: they add a second trigger path without removing the poll.
- **Verify at implementation**: the exact OCI policy statements for managed SSH sessions, from the current Bastion documentation, and that the control center's e2-micro runs `ansible-core` against four hosts within its memory (expected 100–200 MB).
