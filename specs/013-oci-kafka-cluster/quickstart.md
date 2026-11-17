# Quickstart: Validating the OCI Kafka Cluster

**Feature**: [spec.md](spec.md) | **Plan**: [plan.md](plan.md)

Scenarios 1–5 run on a workstation with no cloud access and must pass before anything is created in OCI. Scenario 0 runs on the GKE cluster during Spec 012 Gate 4 step 2, before the restore. Scenarios 6–11 are live. The operator runs every live command; agents never log in to the VMs.

## Prerequisites

- Workstation: Podman 5.x with `podman compose`, Python 3.11+, Go 1.22, Terraform `>= 1.9`, `ansible-core` from `bootstrap/requirements.txt`, kubeconform.
- Log-rate numbers measured (research R12) and recorded in `docs/runbooks/oci-kafka.md`.
- For live scenarios: the OCI CLI signed in, the OCI Bastion session tooling, `kubectl` for GKE.

## Scenario 0: Spike S1, WireGuard source NAT under Dataplane V2 (blocks the slot agent)

1. During Gate 4 step 2 (empty GKE cluster), create Micro-1 only (`terraform apply -target` the one instance). If `vps-k3s`'s boot volume leaves less than 50 GB of the 200 GB allowance, that VM is billed for a few days (the $1 budget alert shows it), or run this scenario right after `vps-k3s` is deleted instead.
   Then run `ansible-playbook infra/oci/ansible/spike-s1.yml`, which brings up `wg0` with slot 0's peer and a plain TCP listener on `10.200.0.13:9092`.
2. Apply `clusters/gke/oci-link/` (slot agent and keygen) with a one-slot `wg-peers`.
3. `kubectl run s1 --rm -it --image=busybox:1.37.0 -- nc -zv -w 3 10.200.0.13 9092`

**Expected**: connection succeeds, and on Micro-1 `conntrack -L` shows source `10.201.0.1`. **If it fails**: apply the hostNetwork fallback ([contracts/wg-slot-agent.md](contracts/wg-slot-agent.md#spike-s1-and-fallback)), rerun step 3 from a `hostNetwork` pod, and record which path was taken in the runbook.

## Scenario 1: Offline checks (SC-008, SC-012)

```bash
python3 scripts/kafka-declarations.py check
python3 -m unittest discover tests/infra
python3 -m unittest discover tests/preflight
./scripts/render-validate.sh gke
(cd services/error-pipeline && go test ./... && go vet ./...)
(cd services/wg-slot-agent && go test ./... && go vet ./...)
(cd infra/oci/terraform && terraform fmt -check && terraform init -backend=false && terraform validate)
python3 scripts/check-arch-doc-sync.py --worktree
```

**Expected**: all pass. `tests/infra/test_kafka_declarations.py` shows each of the eight [check rules](contracts/kafka-declarations.md#check-ci-offline-no-credentials) failing on its mutated copy.

## Scenario 2: Rehearsal, durable ingestion and fixture capture (Story 1, SC-001)

```bash
./scripts/kafka-rehearsal.sh up
./scripts/kafka-rehearsal.sh test story1
./scripts/kafka-rehearsal.sh capture-logs
```

**Expected**: 100,000 numbered lines written by a fake `proj-*` source are read back exactly once, in order per pod. Each `logs.*` partition reports 3 replicas, with ISR 3. `capture-logs` writes the real broker and controller log lines to `tests/fixtures/kafka-logs/4.3.2/`, and the Lua rule tests then pass against them.

## Scenario 3: Rehearsal failover (Story 2, SC-002, SC-003, SC-011, SC-013, SC-014)

```bash
./scripts/kafka-rehearsal.sh test story2
./scripts/kafka-rehearsal.sh test alerts
```

**Expected**:
- **One broker stopped at a time** (nodes 1, 2, 3): acknowledgements resume within 30 s, no acknowledged record is lost, full replication returns within 15 min of the restart, and the fake notify endpoint receives exactly one DOWN and one OK each.
- **Two brokers stopped**: writes are refused (not acknowledged), Fluent Bit buffers, and no loss after one returns.
- **Broker restarted within 2 minutes**: exactly one DOWN and one OK.
- **Agent stopped for 4 minutes**: one "agent silent, broker healthy".
- **Relay's network cut**: one `cluster/unreachable`, no silent-VM messages.
- **Live view**: `ops-relay status` shows the stopped broker within 60 s, and a synthetic disk-fill series produces one WARN.

## Scenario 4: Rehearsal rebuild drill (Story 3)

```bash
./scripts/kafka-rehearsal.sh test story3
```

**Expected**:
- **Broker volume wiped** (node 3): the broker rejoins as node 3 with the same cluster ID, partitions return to 3/3, and nothing is lost.
- **Controller volume wiped** (node 4): `remove-controller` then `add-controller` restores 3 voters, and writes continue throughout.

The command output from both steps is saved as fixtures for the `replace-controller.yml` checks.

## Scenario 5: Rehearsal security (SC-010)

```bash
./scripts/kafka-rehearsal.sh test security
```

**Expected**: a client without a CA-signed certificate is refused. `fluent-bit-gke` cannot read any topic or write `ops.platform.events`. All three refusals appear in broker logs.

## Scenario 6: Live build at Gate 4 (FR-017, FR-027)

1. Confirm the Spec 012 smoke test passes on GKE and the bundle restore succeeded, then delete `vps-k3s` (runbook checklist).
2. `terraform plan -out tfplan` in `infra/oci/terraform`, review it, `terraform apply tfplan`. If A1 capacity is refused, retry in another fault domain per the runbook.
3. `ansible-playbook -i inventory/oci.yml site.yml` through the Bastion. This configures the base OS, WireGuard, certificates (CSR flow), Kafka formatting with `--initial-controllers`, topics and ACLs, and the ops agent.
4. Merge the Flux changes for `clusters/gke/oci-link/` and `clusters/gke/observability/`, then wait for reconciliation.

**Expected**: `ops-relay status` shows brokers 3/3, controllers 3/3, under-replicated 0, and every GKE node holding a slot. Fluent Bit buffers drain to 0.

## Scenario 7: Live ingestion and failover (SC-001, SC-002, SC-011, SC-014)

Repeat Scenarios 2 and 3 against the live cluster with the `live` target of the test runner, which uses a test `proj-*` namespace for the numbered source. Stop brokers with `systemctl stop kafka` through the Bastion, one at a time.

## Scenario 8: Live exposure check (SC-005)

From a machine outside OCI and GCP: `nmap -Pn -p 1-65535 <each reserved public IP>` and `nmap -sU -p 51820 ...`.

**Expected**: no TCP port open. UDP 51820 shows `open|filtered`, because WireGuard does not answer unauthenticated packets.

## Scenario 9: Live node replacement (SC-009)

Delete the GKE Spot node (`gcloud compute instances delete` of its VM).

**Expected**: within 5 minutes the replacement node holds a slot and its Fluent Bit delivers. The dead node's slot shows as free in `ops-relay status`.

## Scenario 10: Live rebuild drill (Story 3, SC-004)

Terminate Micro-1 in the OCI Console. Follow the runbook's rebuild section (Terraform for the one instance, then the Ansible `rebuild-node.yml` limited to it). Then do the same for Micro-2 with `replace-controller.yml`.

**Expected**: each completes in under 60 minutes with no lost records, and messages arrive as in Scenario 3.

## Scenario 11: First-month cost (SC-006)

After the first full billing month: the OCI cost report shows $0, the $1 budget alert never fired, and Google Cloud egress to OCI is under $1.
