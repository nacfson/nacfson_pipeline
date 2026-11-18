# Feature Specification: OCI Replicated Kafka Log Ingestion Cluster

**Feature Branch**: `013-oci-kafka-cluster`

**Created**: 2026-11-17

**Status**: Draft (clarified)

**Input**: User description: "I want to specify a new spec to implement Kafka architecture to receive Fluent Bit's log sender requests. We will use free-spec OCI VPS and we must support a 3-replica architecture to prevent impediment issues." Expanded: Spec 013 (number reserved by Spec 012 and Spec 015) is the OCI Kafka cluster that receives Fluent Bit's log producer traffic from GKE. It runs on Oracle Cloud Always Free capacity as a 3-replica cluster, so the loss of one machine neither loses acknowledged logs nor stops ingestion. It must accept the producer settings already committed in `clusters/gke/observability/fluent-bit-kafka.conf`.

**Related**: [`012-gke-platform-migration`](../012-gke-platform-migration/spec.md) (defines the GKE producer side and, in FR-011, frees the `vps-k3s` VM's capacity for this cluster at Gate 4; this feature deletes and recreates it, see FR-027), [`015-gke-terraform-infra`](../015-gke-terraform-infra/spec.md) (GKE nodes keep external IPs for egress to this cluster), [`contracts/CONTRACT_LOG_SCHEMA.md`](../../contracts/CONTRACT_LOG_SCHEMA.md) (record shape on the topics). Spec 014 (Flink processing, `logs.proj.errors`, VictoriaLogs and the viewer, sharing this feature's A1 VMs) consumes from this cluster and is out of scope.

## Background: What Already Exists

The producer side is built. On `gke`, every node's Fluent Bit sends `proj.*` records to topic `logs.proj.valid` and `quarantine.*` records to `logs.proj.quarantine`. It uses brokers `10.200.0.11:9092`, `10.200.0.12:9092`, `10.200.0.13:9092`, waits for acknowledgement from all in-sync replicas (`acks=-1`), sends with idempotence on, retries forever, and buffers up to 256 MiB per node on disk (192 MiB valid, 64 MiB quarantine) while the cluster is unreachable. Nothing in the repository yet defines the machines, the brokers, the topics, or the network path behind those addresses. This feature defines them.

## Capacity Facts That Shape This Feature

Oracle's current Always Free page (checked 2026-11-17) gives Ampere A1 an allowance of 2 OCPUs and 12 GB of memory in total, and at most two A1 instances of 1 OCPU each. It also gives two AMD micro instances (1/8 OCPU, 1 GB memory, 50 Mbps to the internet), 200 GB of boot and block storage combined (each boot volume is at least 47–50 GB), and 10 TB per month of outbound data. Repository documents disagree about the existing `vps-k3s` VM: `docs/security-and-complexity-audit.md` and `docs/operations-runbook.md` say 4 OCPU / 24 GiB, while `TROUBLESHOOTING.md` and `SPEC.md` §10.2 say 2 OCPUs. Older tenancies may keep the earlier 4 OCPU / 24 GB allowance. Oracle may also reclaim an Always Free A1 instance whose CPU, network, and memory use all stay below 20% for 7 days. The tenancy has a billing account (pay-as-you-go): the Always Free allowance still applies, anything beyond it is charged, and A1 capacity is more reliably available than on a free-only tenancy.

## Clarifications

### Session 2026-11-17

- Q: Which machine layout gives the three replicas? → A: The whole current Always Free compute allowance: two Ampere A1 VMs (1 OCPU / 6 GB each, ARM64) and two AMD micro VMs (1/8 OCPU / 1 GB each, x86_64). Cost is zero. The two A1 VMs are shared with Spec 014's Flink processing, so the separate "OCI VM 4" in Spec 012 and `ARCHITECTURE.md` no longer exists.
- Q: How do GKE pods reach the brokers? → A: A WireGuard tunnel from GKE to the OCI VMs. Fluent Bit keeps its committed broker addresses `10.200.0.11`–`10.200.0.13:9092`.
- Q: How is Kafka installed and changed on the OCI VMs? → A: Terraform creates the four VMs once from a reviewed saved plan. An operator-run Ansible playbook in Git (the repository's existing `bootstrap/` tool) installs and changes Kafka and WireGuard in place, one VM at a time, waiting for full replication between VMs. Rebuilding VMs for routine changes is rejected because free A1 capacity is often unavailable. k3s with a Kafka operator is rejected because it does not fit a 1 GB micro VM.
- Q: Should the A1 VMs each run Kafka and Flink, or should one A1 run only Kafka and the other only Flink, with the two micro VMs as brokers? → A (confirmed by the maintainer 2026-11-17): Kafka brokers on both A1 VMs and one micro VM, a controller-only voter on the second micro VM, and Spec 014 workloads sharing the A1 VMs under limits that protect Kafka (FR-021). Putting two of three replicas on micro VMs would make every acknowledged write depend on a 1/8-OCPU, 1 GB machine.
- Q: How does each GKE node get its own WireGuard identity (key pair and tunnel address) when GKE adds or replaces nodes? → A: Self-built fixed key slots, with no third-party coordination service. A fixed number of WireGuard identities is created ahead of time and registered once on every OCI VM. Each GKE node claims a free slot when it starts and gives it back when it leaves. Tailscale was rejected to avoid depending on a third-party SaaS, and a managed VPN was rejected for cost.
- Q: Inside the WireGuard tunnel, should Kafka also check who each client is and what it may do? → A: Yes. Mutual TLS for every Kafka connection (clients, brokers, controllers) with certificates from a private certificate authority, plus Kafka permission rules (ACLs) that deny by default: GKE producers may only write the two `logs.proj.*` topics, and Spec 014 consumers may only read the topics they are granted. The tunnel and the certificates are two independent layers.
- Q: How long should Kafka keep project logs before deleting them? → A: 7 days or the size cap, whichever comes first. Kafka is a short buffer, not the log archive; the size cap guarantees no broker disk fills. Logs older than 7 days that Spec 014 has not read by then are gone, and that gap is accepted.
- Q: At cutover, is the `vps-k3s` VM reused in place as A1-1, or deleted and recreated? → A: Deleted and recreated. After GKE has taken over production (Spec 012 Gate 4), `vps-k3s` and its k3s setup are retired entirely; nothing on it is kept (its data already moved to GKE through the Spec 012 cutover bundle). The tenancy is a pay-as-you-go account, which makes Oracle's "out of host capacity" refusals for A1 rare, so Terraform creates A1-1 fresh like the other three VMs. Reusing the VM in place was rejected because it would keep the old network attachment and need a hand-built VM imported into Terraform.
- Q: Is alerting on events enough to observe health? → A: No. Health must also be tracked continuously as metrics, with a live view and trends, because slow problems build up before any event fires (FR-028, FR-030). Story 3 was also restated: the value is a rehearsed rebuild drill, since building from Git alone does not prove a rebuilt VM rejoins the cluster.
- Q: Must this design allow moving Kafka into GKE later? → A: Yes. Moving is not part of this feature, but nothing here may block it: the cluster's settings, identities, and alerting must not depend on Kafka running on OCI VMs, except in the layer that creates and configures those VMs (FR-025).
- Q: When the Kafka cluster is degraded, how is the operator alerted? → A: Layered event alerts. An event agent on each OCI VM writes what it observes (Kafka log events, process failures, disk and certificate threshold crossings, and a heartbeat every 60 seconds) to a small `ops.platform.events` topic. An always-on alert relay in GKE reads it, sends one Discord message per state change through `vault-notify-proxy`, treats 3 minutes without a heartbeat as a silent VM, and alerts directly when its own connection to Kafka is lost for 60 seconds. Alerts must arrive within 10 minutes and every incident must be reported, even one that recovers quickly. A scheduled check (misses short outages), direct pushes from the VMs (no history, still blind to a dead VM, deleted after a move to GKE), and a full monitoring stack were rejected.

### Session 2026-11-18

- Q: Who runs the Ansible playbooks after the first build? → A: The control center (`nacfson-control-center`, the operator's always-on VM in GCP, outside both clouds) runs them automatically with its own runner identity:
  - **On merge**: a commit on `main` that passed its required checks and changes the Kafka declarations or the Ansible tree starts a run.
  - **Daily**: a certificate renewal run.
  - **Operator-started**: rolling OS patches, broker rebuilds, and controller replacement stay operator-started from the control center.
  - **Terraform**: stays operator-run.

  This replaces the "operator-run" part of the 2026-11-17 runtime answer. Running Ansible from GitHub Actions (OCI credentials in a public repository's CI), from a Job inside GKE (OCI SSH keys in the cluster), and from the VMs themselves (FR-013) were rejected.
- Q: How does each VM's certificate get signed? → A: Ansible creates the private key and a signing request on the VM, the runner identity submits the request as a `CertificateRequest` in GKE namespace `oci-link`, cert-manager signs it, and Ansible installs the certificate. Committing each signing request through a pull request was rejected because renewal every 60 days could then not run unattended.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Logs from GKE land durably in OCI (Priority: P1)

As the platform operator, I want every log record that Fluent Bit sends from GKE to be stored on three separate OCI machines before Fluent Bit considers it delivered, so that a project's logs are not lost when one machine fails.

**Why this priority**: Without a receiving cluster, the GKE Fluent Bit buffers fill (256 MiB per node) and then drop the oldest logs. Nothing else in the log pipeline works without this.

**Independent Test**: With the GKE Fluent Bit output changed only to add its TLS client settings, write a known sequence of numbered log lines from a test `proj-*` container, then read both topics from the beginning and confirm every line appears exactly once, in order per pod, with each partition stored on three machines.

**Acceptance Scenarios**:

1. **Given** the three brokers are healthy, **When** Fluent Bit on any GKE node sends a record to `logs.proj.valid` or `logs.proj.quarantine`, **Then** the cluster acknowledges it only after at least two machines have stored it, and it later appears on all three.
2. **Given** a record follows the log contract, **When** it is read from `logs.proj.valid`, **Then** its fields match what Fluent Bit sent, with no added, removed, or renamed fields.
3. **Given** Fluent Bit retries a batch after a network timeout, **When** the retried batch arrives, **Then** no record is stored twice.

---

### User Story 2 - Ingestion keeps running when one machine is down (Priority: P1)

As the platform operator, I want ingestion to continue without intervention while any one broker machine is stopped, rebooted for patches, or lost, so that routine maintenance and single failures never interrupt log delivery.

**Why this priority**: This is the reason for three replicas. A two-machine or one-machine design would stop writes or lose data on a single failure.

**Independent Test**: While a load generator sends a steady numbered stream, stop each broker in turn for 10 minutes, start it again, and confirm zero acknowledged records are missing and the producers never stop being acknowledged for more than 30 seconds.

**Acceptance Scenarios**:

1. **Given** steady ingestion, **When** any one broker machine stops without warning, **Then** producers are acknowledged again within 30 seconds and no acknowledged record is lost.
2. **Given** one broker is down, **When** it returns, **Then** it catches up automatically and the cluster reports every partition fully replicated again without operator commands.
3. **Given** two of the three brokers are down, **When** Fluent Bit sends, **Then** the cluster refuses the writes rather than accepting them with a single copy, Fluent Bit keeps the records in its on-disk buffer, and delivery resumes with no loss once a second broker returns, provided the outage does not exceed the per-node buffer.
4. **Given** the operator starts an operating system patch from the control center, **When** the rolling procedure restarts brokers one at a time, **Then** ingestion never stops.

---

### User Story 3 - Losing a VM is a rehearsed repair (Priority: P2)

As the platform operator, I want rebuilding a lost VM to be a procedure I have already practised, so that a real loss is a timed, known repair while the cluster runs on two copies, not an experiment.

**Why this priority**: Building the VMs from Git (FR-010, FR-013) does not prove that a rebuilt broker rejoins the existing cluster. A rebuild can fail in ways the first build never exercises: a freshly generated cluster ID, a reused node identity, a controller that has to be swapped in the controller quorum, or a VM that starts without a signed certificate or tunnel peers. Until the rebuild finishes, one more failure stops writes.

**Independent Test**: As a drill, destroy one broker-only VM (Micro-1) and, separately, one controller-only VM (Micro-2). Rebuild each by following the runbook, and record the time taken and any lost records.

**Acceptance Scenarios**:

1. **Given** Micro-1 and its disk are gone, **When** the operator follows the runbook, **Then** the new VM rejoins as broker 3 of the same cluster, the other two brokers copy its partitions back, Fluent Bit needs no change, and no acknowledged record is lost.
2. **Given** Micro-2 and its disk are gone, **When** the operator follows the runbook, **Then** the lost voter is removed from the controller quorum and the rebuilt controller is added back, and writes continue throughout.
3. **Given** a rebuilt VM, **When** it starts, **Then** it has a newly signed certificate and its WireGuard peers before it accepts traffic, and it never forms a separate cluster.
4. **Given** the whole cluster is recreated with empty disks, **When** it starts, **Then** all topics exist with the declared settings before any producer connects, and no topic is created implicitly by a producer.

---

### User Story 4 - Operator sees cluster health continuously (Priority: P2)

As the platform operator, I want the cluster's health tracked all the time, both as a live view and as trends over time, not only as alerts when something breaks, and without exposing any management interface publicly.

**Why this priority**: Replication only protects data if a degraded state is noticed before a second failure. Many problems (a filling disk, growing consumer lag, rising memory, slower writes, a filling Fluent Bit buffer) build up slowly before any event fires.

**Independent Test**: Watch the live health view during normal ingestion, stop one broker, and confirm that the view shows the missing broker and the partitions with fewer copies within 60 seconds, that the last hour of metric history can be read, and that the view returns to healthy after the broker returns.

**Acceptance Scenarios**:

1. **Given** normal operation, **When** the operator opens the live health view, **Then** it shows, each value no older than 60 seconds: brokers and controllers present, partitions with fewer than three copies, write latency and throughput, disk use per broker, Kafka memory use, Fluent Bit buffer fill per GKE node, tunnel handshake age per key slot, certificate days left, and consumer lag once Spec 014 consumers exist.
2. **Given** a metric trending toward a limit, **When** at its current rate it would cross the limit within 3 days, **Then** one warning message is sent under the FR-023 rules.
3. **Given** someone on the public internet, **When** they scan the broker machines, **Then** no management, metrics, or controller port answers.
4. **Given** the cluster was healthy, **When** one broker stops, **Then** one Discord message naming the missing broker arrives within 10 minutes, no further message is sent while it stays down, and one "recovered" message arrives within 10 minutes of it returning to full replication.

---

### User Story 5 - Rehearse the cluster locally before it touches OCI (Priority: P3)

As the platform operator, I want a local three-broker rehearsal that uses the same broker and topic declarations, so that failover and Fluent Bit compatibility are proven on my machine before the Gate 4 cutover window.

**Why this priority**: The A1 VMs can only be created after `vps-k3s` is deleted at cutover (FR-027), so problems found there cost a log gap. A rehearsal moves that risk earlier.

**Independent Test**: Start the local rehearsal, point a local Fluent Bit with the GKE output settings at it, run User Story 2's failover test, and get the same results.

**Acceptance Scenarios**:

1. **Given** the local rehearsal is running, **When** the Story 1 and Story 2 tests run against it, **Then** they pass with the same topic settings as production.

---

### User Story 6 - Merged changes and renewals apply themselves (Priority: P2)

As the platform operator, I want merged changes to the cluster's settings, and every certificate renewal, to reach the VMs without me running anything, so that the VMs stay in step with Git and no certificate expires because I forgot.

**Why this priority**: Certificates expire every 90 days, and an expired one takes a VM out of the cluster. Hand-run changes also let the VMs drift from Git.

**Independent Test**: Merge a harmless setting change and confirm the runner applies it to each VM in turn while ingestion continues. Pause automation and confirm a merge does nothing. Stop one broker, merge again, and confirm one "blocked" alert and no run.

**Acceptance Scenarios**:

1. **Given** a merged change to the Kafka declarations or the Ansible tree with passing checks, **When** the runner sees it, **Then** it applies the change one VM at a time, waits for full replication between VMs, and records the applied commit on each VM.
2. **Given** a VM certificate past day 60 of 90, **When** the daily run starts, **Then** the certificate is renewed, and only that VM's Kafka and ops agent restart.
3. **Given** the cluster is degraded (a broker missing or a partition under-replicated), **When** a change is merged, **Then** no run starts and one "blocked" alert is sent.
4. **Given** a run fails on a VM, **When** it stops, **Then** it does not continue to the next VM, and one "failed" alert names the VM and the step.
5. **Given** the operator paused automation, **When** a change is merged, **Then** nothing runs until automation is resumed, and the change is applied then.

### Edge Cases

- **Cutover gap**: Before Gate 4, the existing `vps-k3s` VM uses the A1 allowance, so the brokers cannot all exist before the GKE Fluent Bit starts sending. Records sent before the cluster accepts writes wait in the per-node 256 MiB buffer. If the cluster is not accepting writes before a node's buffer fills, that node drops its oldest buffered records. The cutover runbook must state the expected gap and how to measure loss.
- **Disk full on a broker**: Retention must delete the oldest data before any broker disk fills. If a disk fills anyway, that broker stops accepting writes, and the other two continue as in Story 2.
- **Spec 014 not running yet**: Nothing reads the topics until Spec 014 ships. Records live only until retention deletes them (7 days, or less if the size cap is reached first). Logs older than that are gone, which is accepted and not data loss under this feature. The Spec 012 cutover runbook should say so, so that Spec 014 is scheduled close to cutover.
- **Size cap reached before 7 days**: If the log rate grows, the size cap deletes the oldest records earlier than 7 days. The FR-023 disk alert does not fire in that case because the disk stays within its limit, so the plan MUST show the expected number of days at the measured rate.
- **Clock skew**: Record time comes from the producer (`_time`). Brokers do not rewrite it. Retention uses broker time.
- **Idle reclamation**: A broker with little traffic could fall below Oracle's 20% idle thresholds. The plan must show that each broker stays above at least one threshold, or record the risk and its repair path (Story 3).
- **Cross-cloud link loss**: When the GKE-to-OCI path is down, the cluster stays healthy and Fluent Bit buffers. When the path returns, delivery resumes without operator action.
- **Oversized record**: A record above the broker's maximum message size is rejected. The maximum must be at least what Fluent Bit can send after its multiline filter, and the plan must state both numbers.
- **Unknown topic**: A producer that names any topic other than the declared ones is refused, not given a new topic.
- **Slot pool exhausted**: GKE briefly runs more nodes than slots (for example during an upgrade surge). The extra node has no tunnel, buffers its logs locally, reports the shortage, and claims a slot as soon as one is released.
- **Node dies holding a slot**: A Spot reclaim removes a node without releasing its slot. The slot is released automatically within 5 minutes, and until then the pool has one fewer free slot.
- **Two nodes race for one slot**: Only one wins the lock. The other picks a different free slot. Two tunnels never use the same key at once.
- **Certificate expiry**: If a client or broker certificate expires, its connections are refused. For Fluent Bit this behaves like a link outage: records buffer locally. Renewal starts when a certificate has used two thirds of its life, and FR-023 alerts on any certificate with less than 14 days left.
- **Alert relay down**: Events wait in `ops.platform.events` and are sent, marked as delayed, after Kubernetes restarts the relay. If the relay is down while the cluster is also unreachable, nothing is sent until one of them returns. That gap is accepted.
- **Micro-2 failure**: Kafka records no event when a controller-only voter disappears, so the heartbeat is the main signal for Micro-2 and its alert arrives after about 3 minutes.
- **Unauthorized action**: A client with a valid certificate that tries to read, or to write a topic it was not granted, is refused and the refusal is logged on the broker.
- **Micro VM CPU throttling**: An AMD micro VM has 1/8 OCPU with bursts. After an outage, a replica on a micro VM may take much longer to catch up than one on an A1 VM. Writes continue while the two other replicas are in sync. SC-003 is measured with the slowest VM.
- **Free capacity shortage on rebuild**: Oracle can report "out of host capacity" for A1 instances, less often for a pay-as-you-go tenancy but not never. A destroyed A1 VM may not be re-creatable at once. The cluster then runs on two of three replicas, and the runbook states how to keep retrying and what to watch meanwhile.
- **Automated run fails midway**: The runner stops at the failing VM, so at most one VM has a partial change. The other VMs keep the previous configuration, and the cluster keeps running on them. The failure is alerted, and the next merge or an operator-started run converges the VM.
- **Change merged while degraded**: The runner does not touch a degraded cluster. It reports the run as blocked and starts it once the cluster is fully replicated again.
- **Control center unavailable**: Automated runs and renewals pause. The cluster keeps running. Certificates have 30 days left when renewal starts, and FR-023 warns at 14 days, which leaves time to repair the control center or run the playbook from the operator's machine as break-glass.
- **Runner credential leaked**: The runner's OCI credential can only open sessions on the one Bastion, and its GKE identity can only request certificates and write events in `oci-link`. The runbook covers revoking both and rotating the keys.
- **Cutover order**: Until Gate 4, `vps-k3s` uses the A1 allowance. The micro VMs can be created before Gate 4 if the storage allowance has room. The A1 VMs are created by Terraform only after GKE has passed the Spec 012 smoke test and `vps-k3s` has been deleted. If Oracle still refuses A1 capacity, the runbook retries in other fault domains and availability domains, and GKE's Fluent Bit buffers meanwhile.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The cluster MUST run on four OCI Always Free VMs and nothing else: two A1 VMs (1 OCPU / 6 GB, ARM64) and two AMD micro VMs (1/8 OCPU / 1 GB, x86_64), placed in different fault domains where the region allows. Monthly compute cost MUST be zero.
- **FR-002**: The cluster MUST use Kafka's built-in KRaft metadata quorum with exactly three controller voters on three different VMs, and every partition MUST have three replicas on three different VMs. It MUST NOT depend on ZooKeeper. Roles: A1-1 (`10.200.0.11`) and A1-2 (`10.200.0.12`) each run a broker and a controller voter, Micro-1 (`10.200.0.13`) runs a broker only, and Micro-2 runs a controller voter only. Losing both micro VMs still leaves two brokers and two of three voters, so writes continue.
- **FR-019**: The four VMs' boot and block volumes together MUST fit the 200 GB Always Free storage allowance. The plan MUST divide that space between Kafka data, the operating systems, and the Spec 014 workloads on the A1 VMs, and set Kafka retention size from the smallest broker disk.
- **FR-020**: The A1 VMs MUST reserve memory and CPU headroom for Spec 014 (Flink, VictoriaLogs, viewer). The plan MUST give a per-VM memory table showing Kafka, Spec 014, and the operating system fitting in 6 GB and 1 GB respectively.
- **FR-003**: Topics `logs.proj.valid` and `logs.proj.quarantine` MUST be declared in Git with replication factor 3 and minimum in-sync replicas 2, and created from that declaration. Automatic topic creation MUST be off. Changing a topic setting MUST happen only through a merged change to the declaration.
- **FR-004**: The brokers MUST accept the producer settings already in `clusters/gke/observability/fluent-bit-kafka.conf` (`acks=-1`, idempotent producer, unlimited retries, JSON records with `_time` in ISO 8601), so that a retried batch is stored once. The only changes this feature makes to that file are adding the TLS client settings and client certificate required by FR-024 and turning on zstd compression, so records cross the cloud link and sit on broker disks compressed; broker addresses, topics, acknowledgement mode, idempotence, retries, buffer limits, and record format MUST stay as they are.
- **FR-005**: Each broker MUST be reachable from GKE at a stable private address in `10.200.0.11`–`10.200.0.13` on port 9092, which accepts only mutual-TLS connections (no plaintext listener exists), and those addresses MUST survive a VM rebuild. The controller-only VM is not a producer address and is not reachable from GKE.
- **FR-006**: Retention MUST be set per topic by both time and size, and the size limit MUST leave every broker disk with at least 20% free when every partition is full. `logs.proj.valid` and `logs.proj.quarantine` MUST keep records for 7 days or until their size limit, whichever comes first; the size limit is the hard backstop and MUST be set from Micro-1's disk, the smallest broker. Brokers SHOULD store the topics compressed so 7 days fit inside the size limit at the measured log rate. The plan MUST measure the current log rate from `vps-k3s` VictoriaLogs data and show the arithmetic against each machine's real disk, including how many days actually fit.
- **FR-007**: The cluster MUST tolerate the loss of any one machine without losing an acknowledged record and without stopping writes, and MUST refuse writes rather than accept them with a single copy when two machines are lost.
- **FR-008**: Only the GKE producers and the Spec 014 consumers MUST be able to reach the brokers, and broker traffic between clouds MUST be encrypted. No broker, controller, or metrics port may be open to the public internet. Traffic between GKE and OCI MUST travel inside a WireGuard tunnel. The only internet-facing port on the OCI VMs is the WireGuard UDP port, which ignores unauthenticated packets. The tunnel MUST have no single point of failure: losing one OCI VM or one GKE node leaves every other GKE node able to reach the two remaining brokers. The GKE end MUST keep working when GKE replaces or adds a node (including Spot reclaims) without the operator editing OCI configuration by hand.
- **FR-022**: GKE nodes MUST get their WireGuard identity from a fixed pool of key slots. Each slot is one key pair and one tunnel address. The pool MUST hold at least the GKE maximum node count plus one node being replaced, and every slot MUST be registered on every broker VM by the Ansible playbook. A GKE node MUST claim exactly one free slot through an exclusive cluster lock before it brings up its tunnel, and no two nodes may hold the same slot at the same time. A slot held by a node that no longer exists MUST be released automatically within 5 minutes. When no slot is free, the node MUST keep its logs in its local buffer, and FR-023 MUST alert on the shortage. Changing the pool size or rotating slot keys MUST be a merged change followed by the automated Ansible run (FR-031), with no ingestion stop.
- **FR-009**: The GKE WireGuard slot agent needs network-administration rights on each node, the slot private keys, and permission to take and release slot locks. Fluent Bit and the FR-023 alert relay need their Kafka client keys and certificates (FR-024), and the cluster's certificate authority key must live in a platform namespace. None of these is covered by the current constitution (Principle II, Principle V, and the `hostPath`/privileged rejection rules apply to project namespaces, but no platform exception covers this). Slot private keys MUST be created inside the cluster, MUST NEVER be committed to Git, and only their public keys may leave the cluster (for the Ansible registration). This feature MUST include a constitution amendment that limits the exception to the slot agent, the Fluent Bit collector, and the alert relay in platform namespaces, states where slot keys, the Kafka client keys, and the certificate authority key are created and stored, and keeps project namespaces unchanged, as Amendment 2.2.0 did for edge TLS keys and 2.3.0 did for Cloud KMS. `platform-preflight` MUST reject the same rights anywhere else. The same amendment MUST define the automated Ansible path of FR-031 (its identity, scope, approval by merge to `main`, and targets), as Clarification 2.4.1 requires for any automated apply path, and the runner identity's permission to create certificate requests and events in `oci-link`.
- **FR-010**: The OCI machines, network, firewall rules, and disks MUST be declared in Git as infrastructure code and changed only through a merged pull request and an operator-run reviewed saved plan, as required by Constitution Clarification 2.4.1. The repository MUST NOT gain an automated apply path.
- **FR-011**: Third-party software on the brokers MUST be pinned to exact versions (Kafka, Java runtime, operating system image), consistent with Principle I's ban on floating tags, and MUST run on both ARM64 (A1) and x86_64 (micro) from the same pinned versions.
- **FR-012**: Each broker MUST declare explicit memory limits for the Kafka process (heap and total) that fit its machine with headroom for the operating system, and a CPU priority (a weight, not a hard cap, so Kafka can use idle CPU). The plan MUST record the numbers.
- **FR-013**: Broker configuration MUST come from Git, be reproducible on a fresh machine, and be changed only through a merged pull request; manual edits on a machine are break-glass only and MUST be reconciled back to Git. The VMs MUST be created by Terraform and configured by Ansible playbooks from Git, run by the FR-031 runner, which changes one VM at a time and does not move to the next until every partition is fully replicated. No agent on the VMs may apply changes on its own schedule: the operating system's automatic package updates MUST be turned off, and packages change only through the operator-started rolling patch (FR-014).
- **FR-023**: Degradation MUST be alerted from events, not periodic checks, within 10 minutes, through the existing `vault-notify-proxy` to Discord, with exactly one message when something breaks and one when it recovers, and no notification credential on the OCI VMs:
  1. **Event agents**: Each OCI VM MUST run an event agent (the same pinned Fluent Bit version as GKE) with its own Kafka identity that may only write `ops.platform.events`. It MUST produce events for: broker fenced and unfenced, and partition copies lost and regained (from the local Kafka log); Kafka and WireGuard process failure and recovery (from systemd); free disk crossing 20% in either direction (local reading once a minute, sent only on a crossing); a local certificate within 14 days of expiry (daily); and a heartbeat every 60 seconds. When it cannot write, it MUST keep events on local disk and send them later.
  2. **Topic**: `ops.platform.events` MUST have 1 partition, 3 copies, a minimum of 1 in-sync copy (so alerts flow while the cluster is at its weakest), 30-day retention, the event subject as message key, and broker-assigned timestamps. Its event format MUST be a versioned contract in `contracts/`.
  3. **Alert relay**: One always-on relay in a GKE platform namespace MUST read `ops.platform.events` and the FR-030 `ops.platform.metrics` with an identity that may only read those two topics and describe the cluster. It MUST keep a state per subject (OK, DOWN, WARN) and message only on a change; group all events from a VM with an open incident into that incident; treat 3 minutes without a heartbeat as a silent VM, and use cluster membership to say whether the VM's broker is down or only its agent is silent; alert directly through `vault-notify-proxy`, not through Kafka, when its own connection to Kafka has failed for 60 seconds, and then report one unreachable incident instead of silent VMs or stale tunnels; wait one 3-minute window after starting before declaring any VM silent; rebuild its state on start by re-reading the last 24 hours of events and the last 6 hours of metrics; and alert on Kubernetes Events from the slot agents (no free slot, stale tunnel handshake), which are the only source of tunnel alerts. Its resources MUST be counted by `platform-preflight`, and its network policy MUST allow only the tunnel range on 9092 and `vault-notify-proxy`.
  4. **Tests**: The relay's rules MUST be tested with Kafka log lines captured from the pinned Kafka version, not hand-written samples.
- **FR-028**: The alert relay MUST provide a live health view as a `status` command reachable only through `kubectl exec` into the relay pod. It shows the latest FR-030 values, every open incident, and the time of the last event and metric received. No Service, port, or route exposes it outside the pod.
- **FR-029**: An operator Kafka identity, `ops-cli`, MUST exist that may only describe the cluster, topics, and consumer groups and may not read or write any topic. A separate key is generated on each OCI VM and never leaves it. The runbook's VM-side health commands use it, as the fallback when GKE or the relay is unavailable.
- **FR-030**: Health MUST be tracked continuously as metrics, not only as events. Every 30 seconds, each OCI VM MUST sample Kafka's own metrics (through an exporter bound to localhost only) and its operating system figures (memory, disk, CPU), and write them to `ops.platform.metrics` (1 partition, 3 copies, minimum 1 in-sync copy, 7-day retention). The GKE slot agents MUST report Fluent Bit buffer fill per node and WireGuard handshake age per slot at the same interval. The relay MUST keep the latest value of every metric for FR-028 and evaluate trend rules (for example, days until a disk crosses its limit) that raise FR-023 warnings. The metric record format MUST be part of the same `contracts/` document as the ops events. The exporter's memory MUST be counted in FR-012.
- **FR-026**: Because the tenancy can be charged, Terraform MUST declare an OCI budget for this tenancy with an alert at $1 per month of actual spend that notifies the operator, so any resource outside the Always Free allowance (a larger disk, an extra VM, a paid shape) is noticed within a day. Every VM, disk, and network resource MUST be one Oracle lists as Always Free.
- **FR-027**: `vps-k3s` MUST be deleted, not reused, once Spec 012's cutover has passed, and the runbook MUST confirm before deletion that GKE serves all public traffic and that the cutover bundle restored successfully. Spec 012 FR-011 ("shrunk or recreated") is narrowed to "recreated" by this feature.
- **FR-025**: The design MUST keep a later move of Kafka into GKE possible without changing producers, consumers, or client identities beyond one address setting:
  1. Topics, ACLs, client identities, and broker settings MUST be declared once in Git in a form that does not mention OCI or VMs. The Ansible playbook renders its configuration from those declarations.
  2. The offline check (FR-015) MUST also render the same declarations as Kubernetes Kafka-operator resources (Strimzi assumed) and validate them against their schema, without deploying them, so drift that would block a move fails the build.
  3. Every producer and consumer MUST find the cluster through one bootstrap address list per environment. Moving the cluster changes only that list.
  4. The private certificate authority and client certificates MUST NOT depend on where brokers run, so existing clients keep working after a move.
  5. Everything OCI-specific (Terraform, Ansible, the WireGuard slots, the event agents on the VMs) MUST stay in its own layer and be removable after a move without changing the rest.
  6. The runbook MUST outline, without building, how a move would keep every acknowledged record (for example, add GKE brokers to the same cluster and move partitions to them, then retire the OCI brokers).
- **FR-024**: Every Kafka connection (producer, consumer, broker-to-broker, and controller) MUST use mutual TLS with certificates from one private certificate authority dedicated to this cluster. Kafka MUST deny any action not granted by an ACL declared in Git. The GKE producer identity MUST be allowed only to write `logs.proj.valid` and `logs.proj.quarantine` (including the idempotent-write permission) and MUST NOT read any topic. Consumer identities are added by Spec 014 with read-only grants. Each private key MUST be created where it is used and never copied: broker and controller keys on their VM, the producer key inside the cluster. The certificate authority's key MUST stay in one platform namespace. Certificates MUST be valid for at most 90 days and renewed automatically: in the cluster by the certificate manager, and on the VMs by the FR-031 runner, which submits each VM's signing request to the cluster's certificate authority. Private keys MUST NOT be committed to Git, and only public certificates may leave the place their key was made. ACL rules themselves are declared in Git.
- **FR-021**: On a VM that also runs Spec 014 workloads, Kafka MUST have reserved memory and CPU priority, so that a co-located workload cannot push Kafka out of memory or starve it of CPU. When memory runs short, the co-located workload MUST be the one stopped.
- **FR-014**: The operator MUST be able to patch or restart brokers one at a time by starting a documented rolling procedure from the control center, which waits for full replication before moving to the next broker.
- **FR-031**: Ansible runs MUST be automated by a runner on the control center (`nacfson-control-center`), outside both clouds, with its own identity:
  1. **Triggers**: A commit on `main` whose required checks passed and that changes `infra/kafka/` or `infra/oci/ansible/` starts `site.yml`. A daily schedule starts `renew-certs.yml`, which renews only certificates past day 60. Rolling OS patches, broker rebuilds, and controller replacement run only when the operator starts them from the control center.
  2. **Safety**: One run at a time. No run starts while the cluster is degraded (a broker missing or any partition under-replicated). One VM at a time, with the full-replication wait. Stop at the first failure. Only allowlisted playbooks, only from `main`, only against the four VMs. The operator can pause and resume automation.
  3. **Reporting**: Each run records the applied commit on every VM it finished, and creates a Kubernetes Event in `oci-link` for its result (`DeployOK`, `DeployFailed`, `DeployBlocked`). The FR-023 relay turns these into one message per state change, which works even when OCI is unreachable.
  4. **Permissions**: SSH to the four VMs only through the OCI Bastion, with an OCI credential that may only open sessions on that Bastion. In GKE, only creating and reading certificate requests and creating events in `oci-link`. It MUST NOT create, change, or delete OCI resources. Its private keys are created on the control center and never leave it.
  5. **Governance**: This automated path MUST be defined in the FR-009 constitution amendment.
- **FR-015**: The repository MUST include an offline check, run with the existing validation gates, that fails when the Fluent Bit output's broker addresses or topic names disagree with the cluster's declared brokers and topics, or when a declared topic's replication factor or minimum in-sync replicas drop below 3 and 2, or when the Fluent Bit output lacks its TLS client settings, or when an ACL grants the producer identity anything beyond writing the two declared topics.
- **FR-016**: The repository MUST include a local three-broker rehearsal built from the same topic and broker settings, and the Story 1 and Story 2 tests MUST be runnable against it with one command.
- **FR-017**: The runbook MUST cover:
  - first build inside the Spec 012 Gate 4 window
  - single-machine rebuild
  - rolling patching
  - automated runs, including pausing them and reading a failed run
  - rotating the runner's keys
  - rotating WireGuard slot keys without stopping ingestion
  - health checks
  - how to measure log loss across the cutover gap
- **FR-018**: Logs on the topics MUST NOT be exposed through any public route. Reading them is limited to the Spec 014 consumers and the operator's private access path.

### Key Entities

- **Kafka Node VM**: One of the four OCI VMs. Attributes: name (A1-1, A1-2, Micro-1, Micro-2), Kafka roles (broker, controller voter, or both), node identity, private address, fault domain, shape (OCPU, memory, CPU architecture), disk size, co-located Spec 014 workloads.
- **Topic Declaration**: The Git record of each topic. Attributes: name, partition count, replication factor, minimum in-sync replicas, retention time, retention size, maximum message size.
- **Producer Contract**: What the GKE Fluent Bit output expects from the cluster: broker addresses, topic names, acknowledgement mode, idempotence, record format. Kept in step with the Topic Declaration by FR-015.
- **WireGuard Key Slot**: One pre-made tunnel identity. Attributes: slot number, public key, tunnel address, current holder (GKE node name or free), lock expiry. The private key exists only inside the cluster.
- **Ops Event**: One record on `ops.platform.events`. Attributes: schema version, time (assigned by the broker), kind, state (down, up, warn, ok), subject (the thing that is healthy or not; also the message key), VM, observed by, source, detail.
- **Health Metric Sample**: One record on `ops.platform.metrics`. Attributes: schema version, time (assigned by the broker), source (VM or GKE node), metric name, value, unit.
- **Alert Incident**: The relay's open problem for one subject or VM. Attributes: subject, state, opened at, events grouped into it, messages sent.
- **Kafka Client Identity**: A certificate subject that ACLs refer to. Attributes: name (for example the GKE producer, or a Spec 014 consumer), where its key lives, certificate expiry, granted topics and actions.
- **Cluster Health Report**: The FR-028 live view: latest metric values, open incidents, and the age of the newest event and metric.
- **Ansible Runner**: The FR-031 automation on the control center. Attributes: identity (GKE and OCI), allowlisted playbooks, triggers, paused or active, last applied commit per VM.
- **Ansible Run**: One execution of a playbook. Attributes: commit, playbook, trigger (merge, daily, operator), state (running, ok, failed, blocked), VMs finished, failed VM and step.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: In a 1-hour run at the expected production log rate, 100% of records Fluent Bit reports as delivered can be read back from the cluster exactly once.
- **SC-002**: Stopping any single broker without warning causes zero lost acknowledged records and no more than 30 seconds without acknowledgements, repeated for each of the three brokers.
- **SC-003**: After a broker returns from a 10-minute outage, the cluster reports full replication within 15 minutes without operator commands.
- **SC-004**: The operator can rebuild one destroyed broker machine from Git and the runbook in under 60 minutes, ending with full replication and no lost records.
- **SC-005**: A port scan from the public internet of every broker machine finds no Kafka, controller, or metrics port answering.
- **SC-006**: The OCI bill for this feature is zero in its first full month, and the $1 budget alert has not fired. Google Cloud egress to OCI stays under $1 per month at the assumed log rate.
- **SC-007**: With retention full on both topics, every broker disk keeps at least 20% free space.
- **SC-008**: The FR-015 check fails on a deliberately mismatched topic name, broker address, replication factor, and minimum in-sync replicas, and passes on the committed configuration.
- **SC-009**: When GKE replaces a node, the new node holds a slot and is delivering logs within 5 minutes with no operator action, and a slot left by a node that disappeared without cleanup is free again within 5 minutes.
- **SC-010**: From inside the tunnel, a connection without a certificate from the cluster's authority is refused, the producer identity cannot read any topic or write any undeclared topic, and all three refusals are visible in broker logs.
- **SC-011**: In every SC-002 test, the operator receives exactly one degraded message within 10 minutes of the stop and exactly one recovered message within 10 minutes of full replication returning.
- **SC-012**: The offline check renders the topic, ACL, and client identity declarations both as the OCI VM configuration and as Kubernetes Kafka-operator resources that pass schema validation, and fails when the two would disagree.
- **SC-013**: A broker process that is stopped and comes back within 2 minutes produces exactly one degraded and one recovered message. Stopping only the event agent on a VM for 4 minutes produces one "agent silent, broker healthy" message and no broker-down message. Cutting the tunnel produces one unreachable message and no silent-VM or tunnel messages.
- **SC-014**: During every SC-002 test, the live health view shows the stopped broker and its partitions with fewer copies within 60 seconds; the last hour of metrics for every VM can be read from `ops.platform.metrics`; and a simulated disk fill rate that would reach the limit within 3 days produces exactly one warning.
- **SC-015**: A merged broker or topic setting change reaches all four VMs within 60 minutes of the merge, with no operator action and no gap in acknowledgements over 30 seconds. Every VM certificate is renewed before 30 days remain, with no operator action. A failed or blocked run produces exactly one alert within 10 minutes.

## Assumptions

- The OCI home region allows A1 instances (Oracle excludes South Korea North, Chuncheon) and has more than one fault domain available to the tenancy.
- The expected log rate is small (single-digit megabytes per minute across all projects), so 1 OCPU per A1 broker and a micro VM's burstable CPU are sufficient. The plan measures the current rate from `vps-k3s` before sizing.
- Kafka is a durable buffer, not the system of record. Durability comes from three replicas. Volume backups of broker disks are not taken. Long-term storage belongs to Spec 014.
- Partition count defaults to 3 per topic (one leader per broker). Records keep per-pod order within a partition. The plan confirms which key Fluent Bit uses.
- Topics for Spec 014 (`logs.proj.errors`, `logs.enriched`) are added by Spec 014 using this feature's topic declaration format.
- The OCI network (`10.200.0.0/24`) and the WireGuard slot address range do not overlap any GKE node, pod, or service range from Spec 015. The plan verifies this.
- Google Cloud charges for internet egress from GKE to OCI. At the assumed log rate that costs well under $1 per month and is accepted.
- The operator and the FR-031 runner access OCI machines only through the OCI Bastion, the private access path that Constitution Principle III and Clarification 2.3.1 define. AI agents do not log in to them.
- The control center (private repository `nacfson-control-center`) is already running, can open OCI Bastion sessions, and reads GKE with its `cc-viewer` identity. This feature adds its Kafka actions, its runner identity, and its automation. Those changes live in that repository.
