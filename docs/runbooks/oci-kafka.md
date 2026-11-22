# Runbook: OCI Kafka Cluster (Spec 013)

The cluster receives GKE Fluent Bit records on three brokers (`a1-1`, `a1-2`, `micro-1`) with three controller voters (`a1-1`, `a1-2`, `micro-2`). Design: [specs/013-oci-kafka-cluster](../../specs/013-oci-kafka-cluster/). Declarations: [infra/kafka/cluster.yaml](../../infra/kafka/cluster.yaml).

Agents never log in to the VMs or the cluster. Every command below is run by the operator, or by the control center where it says so.

## Measured log rate

Not measured yet (task T001). Run the two commands in [research.md R12](../../specs/013-oci-kafka-cluster/research.md#r12-storage-retention-and-how-many-days-fit) and record here:

| Value | Result |
|---|---|
| `lines` (30 days) | |
| `msg_bytes` (30 days) | |
| On-disk size of `/victoria-logs-data` | |
| Raw bytes per day (`msg_bytes ÷ 30 × 2`) | |
| Days that fit (`15 GB ÷ (raw bytes per day ÷ 5)`) | |

If fewer than 7 days fit, lower `retention_ms` or raise `retention_bytes` within the 20 GB budget, and `scripts/kafka-declarations.py check` confirms the budget.

## Home region

Confirmed `ap-chuncheon-1` (`region-key: YNY`, `is-home-region: true`, `status: READY`) on 2026-11-22 via `oci iam region-subscription list`. [infra/oci/terraform/variables.tf](../../infra/oci/terraform/variables.tf) defaults `region` to `ap-chuncheon-1`.

## Prerequisites

- The OCI CLI signed in with an API key in `~/.oci/config`, and Google sign-in for the Terraform state bucket.
- Optional for the first build: the control center's Kafka actions and runner identity ([contracts/ansible-runner.md](../../specs/013-oci-kafka-cluster/contracts/ansible-runner.md)). The runner's OCI API key and SSH key are made on the control center. Only the public API key reaches Terraform. Without them, you run Ansible yourself (break-glass) until the control center is ready.
- The Kafka CA in GKE: Flux layer `kafka-ca` reconciled, and `kubectl get clusterissuer kafka-mtls-ca` shows `Ready`.

## Enable the GKE side

The slot agent image is built by `build-images.yaml` only after its source reaches `main`, so `clusters/gke/oci-link/` is merged unwired first.

1. After the merge, take the `wg-slot-agent` digest from the workflow run.
2. In a pull request, replace the all-zero digest under `images:` in `clusters/gke/oci-link/kustomization.yaml` and add a Flux `Kustomization` named `oci-link` (path `./clusters/gke/oci-link`, `dependsOn: governance`) to `clusters/gke/layers.yaml`. Preflight rejects the all-zero digest, so the layer cannot be wired by mistake.
3. After reconciliation, `kubectl -n oci-link get configmap wg-slot-public -o yaml` lists `slot-<n>.pub` and `slot-<n>.address` for each pooled slot.

## Build (Gate 4)

### Cutover checklist (FR-027)

1. The Spec 012 smoke test passes on GKE, and every public hostname is served by GKE.
2. The cutover bundle restore succeeded.
3. Delete `vps-k3s` in the OCI Console, including its boot volume, so the A1 allowance is free. Only then create the Kafka VMs.

### Terraform

`infra/oci/terraform/terraform.tfvars` (gitignored) holds:

```hcl
tenancy_ocid                = "ocid1.tenancy..."
compartment_ocid            = "ocid1.compartment..."
image_display_names         = { arm64 = "<exact aarch64 Minimal name>", amd64 = "<exact amd64 Minimal name>" }
bastion_client_cidrs        = ["<operator public IP>/32", "<control center public IP>/32"]
kafka_runner_api_public_key = "-----BEGIN PUBLIC KEY-----..."
```

`kafka_runner_api_public_key` is optional. Leave the line out until the control center exists: Terraform then creates the runner's user, group, and Bastion policy without an API key, and the runner cannot sign in. Adding the key later is the first step of [Runner keys](#runner-keys).

The budget recipient is passed as `TF_VAR_budget_alert_email`, never written to a file. List the image names with `oci compute image list --compartment-id <c> --operating-system "Canonical Ubuntu" --operating-system-version "24.04 Minimal" --shape VM.Standard.A1.Flex` (and `VM.Standard.E2.1.Micro`), and pick the newest of each.

```bash
cd infra/oci/terraform
terraform init
terraform plan -out tfplan
terraform apply tfplan
```

If A1 capacity is refused (`Out of host capacity`), retry later. Changing an A1 VM's fault domain breaks the "three fault domains" rule in research R4, so change it only together with the matching voter or broker placement.

### Bastion sessions for Ansible

Managed SSH sessions last up to 3 hours. Create one per VM with `oci bastion session create-managed-ssh` (target user `ubuntu`), then write `infra/oci/ansible/inventory/bastion.local.yml` (gitignored):

```yaml
all:
  vars:
    oci_region: ap-chuncheon-1
    bastion_ssh_key: ~/.ssh/oci-kafka
    bastion_sessions:
      a1-1: ocid1.bastionsession...
      a1-2: ocid1.bastionsession...
      micro-1: ocid1.bastionsession...
      micro-2: ocid1.bastionsession...
```

The control center writes the same file for its own runs.

### WireGuard key exchange

1. Copy each pooled slot's `slot-<n>.pub` from `oci-link/wg-slot-public` into `infra/oci/ansible/group_vars/brokers/wireguard_slots.yml`.
2. Start `kafka.site` from the control center. The `wireguard` role prints each broker's public key.
3. Put each broker's public key, reserved public IP (`terraform output broker_public_ips`) with port `51820`, and its `/32` VCN address into `clusters/gke/oci-link/wg-peers.json`, and merge. The DaemonSet rolls, and each node's tunnel comes up. The agent refuses the whole file if any entry is malformed: the key must be base64 of 32 bytes, the endpoint an IP and port (no hostname), and the address a `/32` inside `10.200.0.0/24`.

   ```json
   [
     {"name": "broker-1", "publicKey": "<wg pubkey of a1-1>", "endpoint": "<a1-1 public IP>:51820", "address": "10.200.0.11/32"},
     {"name": "broker-2", "publicKey": "<wg pubkey of a1-2>", "endpoint": "<a1-2 public IP>:51820", "address": "10.200.0.12/32"},
     {"name": "broker-3", "publicKey": "<wg pubkey of micro-1>", "endpoint": "<micro-1 public IP>:51820", "address": "10.200.0.13/32"}
   ]
   ```

### Run the build

Start `kafka.site` from the control center. It runs `site.yml` one VM at a time, controllers first. On the first build there is no cluster to wait for, so it moves on as soon as each VM's Kafka service is started. Every later run first refuses a cluster that is not fully replicated (`DeployBlocked`), and waits up to 15 minutes after each VM for full replication.

### Break-glass: run Ansible from the operator's machine

Use this only when the control center is down. Pause its automation first so two runs never overlap.

```bash
cd infra/oci/ansible
ansible-galaxy collection install -r requirements.yml
ansible-playbook site.yml -e applied_commit=$(git rev-parse HEAD)
```

The operator's kubeconfig must be allowed to create `CertificateRequest`s in `oci-link`. The runner's GKE identity is the only one the admission policy allows, so the break-glass group (`platform:break-glass`) is needed for the certificate steps.

### Package pins

`group_vars/all/main.yml` pins the base packages (Podman, WireGuard, nftables, and the rest of `base_packages`) to exact versions and holds them in dpkg, and automatic updates are off (FR-013). The `patch` run installs the pins first and then upgrades every other package, so a pinned package moves only when its pin changes in Git. Ubuntu keeps only the newest version of a package in `noble-updates`, so a rebuild fails when a pin is gone from the archive. Bump the pins in a pull request, then start `kafka.rolling-change` with tag `patch`.

### Log loss across the cutover gap (FR-017)

Fluent Bit buffers up to 256 MiB per node while no broker is reachable. Before the gap, note `fluentbit.buffer.bytes` in `ops-relay status`. Records are lost only if a node's buffer fills. With the measured rate above, the gap that fills a buffer is `256 MiB ÷ (raw bytes per day ÷ nodes ÷ 86400)` seconds.

## Rolling changes (FR-014)

### Start a rolling change

Start `kafka.rolling-change` from the control center and confirm with the passkey step-up. It runs `rolling-change.yml` one VM at a time, controllers first (`a1-1`, `a1-2`, `micro-2`, then `micro-1`):

| Action | Tags | What happens on each VM |
|---|---|---|
| Restart only | none | restart `kafka`, then wait |
| Patch | `patch` | install and hold the pinned versions, `apt` dist-upgrade of everything else, restart, wait |
| Patch and reboot | `patch,reboot` | as above, but reboot the VM instead of restarting Kafka |

Before each VM the run requires full replication. After each VM it waits for the broker to register, for no under-replicated and no under-min-ISR partitions, and, on controllers, for that controller's quorum lag to reach 0. A cluster that is not fully replicated at the start stops the run with `DeployBlocked`, and no VM is touched.

### One broker down

Writes continue. Fluent Bit's acknowledgements pause for a few seconds while partition leadership moves, then resume (the rehearsal measures the gap, and SC-002 allows 30 s). `ops-relay status` shows the broker down and the partitions under-replicated until it returns. Full replication comes back on its own after the restart (SC-003 allows 15 minutes).

### Two brokers down

`logs.*` topics need 2 in-sync replicas, so with only one broker up every write is refused with `NOT_ENOUGH_REPLICAS`. Nothing acknowledged is lost. Fluent Bit keeps records on each node's disk buffer, up to 256 MiB per node, and sends them when a second broker returns. Start the stopped brokers one at a time. The rehearsal (`kafka-rehearsal.sh test story2`) checks that every buffered line arrives exactly once.

How long the buffer lasts at the measured rate: `256 MiB ÷ (raw bytes per day ÷ GKE nodes ÷ 86400)` seconds, using the "Measured log rate" section above. Past that, the oldest buffered chunks are dropped.

### Resize the slot pool or rotate a WireGuard slot key (FR-017)

The pool is declared in Git (`clusters/gke/oci-link/kustomization.yaml`, the `wg-slot-pool` literal) and works like a connection pool. Keep at least the GKE maximum node count plus one slot.

1. **Add the new slots.** Add their numbers to `wg-slot-pool` and merge. Flux re-runs the `wg-slot-keygen` Job, which adds keys only for the new slots. Copy each new `slot-<n>.pub` from `oci-link/wg-slot-public` into `infra/oci/ansible/group_vars/brokers/wireguard_slots.yml` and merge. The automated `kafka.site` run adds the peers on the brokers.
2. **Remove old slots (rotation only).** In a second merge, remove the old numbers from `wg-slot-pool` and `wireguard_slots.yml`. A node holding an old slot releases it and claims a free one, so its Fluent Bit buffers for a few seconds. The keygen Job deletes an old slot's key only once its Lease has no live holder, and retries until then.

## Health

### Live view

```bash
kubectl -n observability exec deploy/ops-relay -- /ops-relay status
kubectl -n observability exec deploy/ops-relay -- /ops-relay status --json
```

It shows the cluster (brokers registered, controllers reporting, the active controller, under-replicated and offline partitions), one line per VM (produce p99, bytes in, heap, disk, the soonest certificate expiry), one line per GKE node (its slot, Fluent Bit buffer, WireGuard handshake age), and every open incident. Values come from the 30 s samples, so nothing on screen is older than about a minute. There is no Service or port: the view is reachable only through `kubectl exec`.

### What each message means

Every message is one change of state. A repeat of the same state sends nothing.

| Message | Meaning | First step |
|---|---|---|
| `DOWN vm/<vm> — broker N fenced (observed by …)` | the controller stopped trusting that broker: it crashed, stopped, or lost its session | `status`; then the VM's `kafka.service` through the Bastion |
| `DOWN vm/<vm> — kafka.service left active` | systemd saw Kafka stop or fail on that VM | `journalctl -u kafka` on the VM |
| `DOWN vm/<vm> — broker down (no heartbeat for 3 min)` | the VM's agent is silent and the cluster has fewer brokers | treat as the VM lost; see Rebuild |
| `DOWN vm/<vm> — agent silent, broker healthy (…)` | only the agent stopped; Kafka still serves | restart `ops-agent` on the VM |
| `DOWN cluster/unreachable` | the relay has had no working Kafka connection for 60 s | check WireGuard and the broker VMs; silent-VM and tunnel alerts are held back meanwhile |
| `DOWN gke/<node>/tunnel` | every WireGuard handshake on that node is older than 180 s | `kubectl -n oci-link logs` for the slot agent on that node |
| `WARN disk/<vm>` | at the 6 h trend, free space reaches 20% within 3 days | check retention and Spec 014 usage on that VM |
| `WARN gke/<node>/buffer` | that node's Fluent Bit buffer is over half of 256 MiB | Kafka is slow or unreachable from that node |
| `DOWN deploy/<playbook>` | an automated run failed or was blocked | the control center's run log |

`OK` with the same subject closes it. A message that starts `(delayed, event at HH:MM UTC)` was rebuilt after a relay restart and still open.

### VM-side fallback (FR-029)

When GKE or the relay is down, read the cluster from a VM through the Bastion with the VM's own `ops-cli` identity, which can describe but never change anything:

```bash
podman exec --env=KAFKA_LOG4J_OPTS=-Dlog4j2.configurationFile=/opt/kafka/config/tools-log4j2.yaml kafka \
  /opt/kafka/bin/kafka-topics.sh --bootstrap-server 10.200.0.11:9092 \
  --command-config /etc/kafka-node/clients/ops-cli.properties --describe --under-replicated-partitions
```

Swap the tool for `kafka-metadata-quorum.sh ... describe --status` (quorum and leader) or `kafka-broker-api-versions.sh` (registered brokers). The `KAFKA_LOG4J_OPTS` override keeps the tool's own logging off stdout.

### The last hour of metrics

```bash
podman exec --env=KAFKA_LOG4J_OPTS=-Dlog4j2.configurationFile=/opt/kafka/config/tools-log4j2.yaml kafka \
  /opt/kafka/bin/kafka-console-consumer.sh --bootstrap-server 10.200.0.11:9092 \
  --command-config /etc/kafka-node/clients/ops-cli.properties --topic ops.platform.metrics \
  --from-beginning --timeout-ms 10000 | tail -n 500
```

`ops-cli` holds Describe only, so reading the topic needs the relay's group or a super-user identity; on a VM use the broker identity's `admin.properties` instead of `ops-cli.properties`.

### Switch the relay on

The relay image is built by CI only after its source reaches `main`, so `clusters/gke/observability/ops-relay.yaml` is merged unwired, like the slot agent.

1. Take the `ops-relay` digest from the `build-images.yaml` run.
2. In one pull request:
   - add `ops-relay.yaml` to `clusters/gke/observability/kustomization.yaml` with an `images:` entry for `ghcr.io/nacfson/ops-relay` at that digest
   - add `kafka:observability:ops-relay` to `NOTIFY_GRANTS` in `deploy/platform/vault-proxies/notify-proxy.yaml`
   - allow ingress from `observability/ops-relay` in `notify-proxy-policy` (`deploy/platform/vault/network-policy.yaml`)
3. Store the Discord webhook for the `kafka` channel with `scripts/notify-ctl.sh`, as for the existing channels.

## Rebuild a lost VM (Story 3)

Rebuild one VM at a time. While a VM is out, the cluster runs on 2 of 3 brokers or 2 of 3 voters: writes continue, but a second loss would stop them.

### Before and after

1. Pause the control center's automation (`kafka.pause`), so a merge does not start `kafka.site` mid-repair.
2. Watch `ops-relay status` throughout. The lost VM shows as DOWN, and some partitions as under-replicated.
3. Resume automation (`kafka.resume`) once `ops-relay status` shows brokers 3/3, controllers 3/3, and under-replicated 0.

### Broker-only VM (micro-1)

1. Recreate the instance with Terraform, limited to it:
   ```bash
   terraform plan -target='oci_core_instance.node["micro-1"]' -out tfplan
   terraform apply tfplan
   ```
   The reserved public IP is kept (`prevent_destroy`) and reattaches to the new VNIC.
2. Start `kafka.rebuild-node` with `limit: micro-1`. It runs `rebuild-node.yml`, which formats the empty disk with the committed cluster ID and `node.id` 3 and never with `--initial-controllers`, refuses a disk that belongs to another cluster, starts Kafka, and waits up to 60 minutes for full replication.
3. The broker has a new WireGuard key. Put the printed public key into `clusters/gke/oci-link/wg-peers.json` and merge. Until then, GKE reaches the other two brokers, which is enough for writes.

### Controller VM (micro-2, or an A1 VM)

1. Recreate the instance with Terraform, as above, with the VM's key.
2. Start `kafka.replace-controller` with `limit: <vm>`. It runs `replace-controller.yml`:
   - reads the quorum from a surviving controller and records the lost voter's directory ID
   - stops Kafka on the target if it still runs
   - `remove-controller --controller-id N --controller-directory-id <old>`
   - rebuilds the VM and formats it to join (`--no-initial-controllers`), so it starts as an observer
   - `add-controller` from the new node, then waits for 3 voters with lag 0 and for full replication
3. For an A1 VM, which is also a broker, do the WireGuard step of the broker rebuild too.

`--initial-controllers` is used only on the very first build. Formatting a replacement with it would start a second quorum.

### A1 out of host capacity

`terraform apply` can fail with `Out of host capacity` for `VM.Standard.A1.Flex`. Retry later. Keep the fault domain unless the layout in research R4 changes with it: brokers and voters must each stay in three fault domains.

### Drill results

| Date | VM | Procedure | Start | Fully replicated | Duration | Records lost |
|---|---|---|---|---|---|---|
| | micro-1 | `kafka.rebuild-node` | | | | |
| | micro-2 | `kafka.replace-controller` | | | | |

SC-004 asks for each under 60 minutes, with nothing lost.

## Automated runs (Story 6)

The control center applies merged changes and renews certificates without anyone at a keyboard. The contract between the two repositories is [contracts/ansible-runner.md](../../specs/013-oci-kafka-cluster/contracts/ansible-runner.md).

### What triggers what

| Change | Playbook | When |
|---|---|---|
| a merge to `main` touching `infra/kafka/` or `infra/oci/ansible/`, with required checks green | `site.yml` | within about 5 minutes of the merge |
| none | `renew-certs.yml` | daily; renews only certificates with 30 days or fewer left |
| operator action | `rolling-change.yml`, `rebuild-node.yml`, `replace-controller.yml` | on request, with the passkey step-up |

Every playbook first runs `preflight.yml`. If a broker is missing, a partition is under-replicated, or a VM cannot be reached, the run stops before touching anything and the relay sends `DOWN deploy/<playbook>` with `blocked`. After the cluster recovers, the next merge or the next daily run goes through.

### Pause and resume

`kafka.pause` stops automatic runs; operator actions still work. Pause before any repair from the Rebuild section, and resume after `ops-relay status` shows the cluster whole again.

### A failed or blocked run

The relay message names the playbook, the commit, and for a failure the VM and the step (`deploy/site.yml — 4f2c9e1 failed on micro-1 at kafka/restart`). The full log is in the control center's run history for 30 days. Fix the cause in a pull request; the merge starts a new run. `any_errors_fatal` stops a run at the first failing VM, so at most one VM is left half-changed.

### What stays manual

- Terraform (Clarification 2.4.1).
- The WireGuard key exchange: slot public keys into `wireguard_slots.yml`, broker public keys into `wg-peers.json`.
- Pinning the slot agent and relay image digests after CI builds them.

### Runner keys

The runner's OCI API key and SSH key live on the control center under `/var/lib/nacfson-cc/credentials/kafka-runner/` (mode `0600`). The first time, create the keys on the control center, put the public API key in `kafka_runner_api_public_key`, then `terraform plan -out tfplan` and `terraform apply tfplan`. The plan adds only the API key. Rotate them every 90 days:

1. Create new keys on the control center.
2. Put the new public API key in `kafka_runner_api_public_key`, then `terraform plan -out tfplan` and `terraform apply tfplan`. The old key keeps working until removed.
3. Switch the control center to the new keys and confirm one `renew-certs` run.
4. Remove the old API key in the OCI Console and delete the old files.

If a key leaks, treat it as a compromise of all four Kafka VMs. The runner's OCI policy allows only Bastion sessions, but a session gives SSH as `ubuntu`, and `ubuntu` has `sudo`, so whoever holds the API key and an SSH key can be root on every VM, with the broker and controller keys and the Kafka data. It cannot change OCI instances, networks, or volumes.

1. Delete the API key in the OCI Console (Identity, user `kafka-runner`, API keys), and end any open sessions on the Kafka Bastion.
2. Rotate the runner keys as above.
3. Rebuild the VMs one at a time (Rebuild section), so every VM gets new host, WireGuard, and Kafka keys.
4. Issue new certificates for every VM identity (`kafka.renew-certs` with `force_renew`), and review the ACLs with `kafka-acls.sh --list` against `infra/kafka/cluster.yaml`.
5. If the leaked GKE credential could have requested certificates, rotate the Kafka CA: the admission policy limits requests to non-CA certificates of at most 90 days, but those certificates stay valid until they expire.

## Spec 014 handoff

- **Disk**: each A1 VM has about 14 GB left for Spec 014 after the OS, Kafka's 20 GB budget, and 20% free space (research R12). The micro VMs have nothing to spare.
- **Memory**: per research R16, A1-1 reserves 2.5 GB for Flink and A1-2 3 GB for Flink and VictoriaLogs. Kafka's own `MemoryMin` equals its `MemoryMax`, so it cannot be squeezed.
- **CPU and OOM**: Spec 014 units must set `OOMScoreAdjust` of 500 or more and `CPUWeight=100`; Kafka has `-900` and `1000`.
- **Consumer identities**: add each consumer to `identities` in `infra/kafka/cluster.yaml` with read-only grants, `key_location` on its VM or in-cluster Secret. `kafka-declarations.py check` and the `kafka_declared` module do the rest.
- **Kafka client**: `services/error-pipeline/internal/kafkaclient` (franz-go) is shared; `error-detector` moves to it when it reads Kafka instead of VictoriaLogs.

## Moving Kafka into GKE (FR-025 point 6)

The declarations carry no OCI detail, so a move is mostly deletion:

1. Run Strimzi in GKE with the rendered resources (`kafka-declarations.py render --target strimzi`); topics, users, and ACLs match.
2. Keep `ClusterIssuer/kafka-mtls-ca`: client certificates stay valid across the move.
3. Mirror the topics from OCI to the new cluster (MirrorMaker 2), switch Fluent Bit's `Brokers` and `bootstrap.gke` in one pull request, and let the check confirm they agree.
4. Delete `infra/oci/`, `clusters/gke/oci-link/`, and the `wg-slot-agent` service, then the VMs with Terraform.

## First-month cost check (quickstart Scenario 11)

After the first full billing month:

1. The OCI cost report for the tenancy shows $0.
2. The $1 budget alert never fired (it would have emailed `TF_VAR_budget_alert_email`).
3. Google Cloud egress to OCI (Fluent Bit traffic) is under $1 in the GCP billing report.

Record the three numbers here.

## Rehearsal (Story 5)

The rehearsal runs the production settings on a workstation: four `docker.io/apache/kafka:4.3.2` containers in the real node roles, rendered from `infra/kafka/cluster.yaml`, with mutual TLS from a per-run CA, the declared topics and ACLs, the real GKE Fluent Bit configuration (only the broker addresses differ), and the real ops agent configuration and Lua rules.

Prerequisites: Podman 5 with `podman-compose`, Python 3.11+ with PyYAML, Go, and `ansible-core` (set `ANSIBLE_BIN` if it is not on `PATH`).

```bash
./scripts/kafka-rehearsal.sh up
./scripts/kafka-rehearsal.sh test all
./scripts/kafka-rehearsal.sh down
```

`test <suite>` runs one suite: `story1`, `story2`, `story3`, `security`, `alerts`, `declared`, `ops_events_lua`. `capture-logs` re-captures the Kafka log fixtures after an image change. `KAFKA_TEST_OUTAGE_SECONDS` shortens the Story 2 outages for a quick local pass (default 600).

### Results (2026-11-20, `test all`, 10-minute outages)

24 tests passed in 72 minutes:

| Suite | Tests | Covers |
|---|---|---|
| Story 1 | 8 | 100,000 numbered lines through the GKE Fluent Bit config arrive exactly once and in order per pod within each partition; a paused partition leader forces retries without duplicates; an undeclared topic and an oversized record are refused |
| Topics and ACLs | 2 | the `kafka_declared` module converges drift in one run and reports nothing on the next |
| Story 2 | 3 | each broker down for 10 minutes in turn without losing an acknowledged record; two brokers down refuse writes and Fluent Bit delivers everything after one returns; a rolling restart never stops ingestion |
| Security | 2 | a client with a certificate from another CA is refused; `fluent-bit-gke` cannot read or write outside its grants; every refusal is in the broker logs |
| Alerts | 4 | a stopped broker, a quick restart, a silent agent, and a cut relay network each send exactly the expected messages |
| Event rules | 2 | `ops_events.lua` in Fluent Bit 4.2.8 turns the captured Kafka and systemd lines into exactly the expected events |
| Story 3 | 3 | a wiped broker rejoins as node 3 with nothing lost; controller 4 is removed, rebuilt, and added back while writes continue; an empty rebuild has every declared topic before clients connect |

### What the rehearsal changed in the design

- The image must be named `docker.io/apache/kafka`: Podman has no default search registry.
- Kafka's default logging writes controller lines (broker fencing) to a file the ops agent never reads; `infra/kafka/log4j2.yaml` sends everything to the journal.
- Admin commands inside the Kafka container must reset `KAFKA_LOG4J_OPTS` and `KAFKA_OPTS`, or they print log noise and start a second JMX exporter.
- A stopped active controller never logs "Fencing broker"; the registration records every controller logs are the second signal.
- A replaced controller is formatted to join (`--no-initial-controllers`) and added only after it has caught up as an observer; stale observer rows for the old directory remain for a while and are ignored.
