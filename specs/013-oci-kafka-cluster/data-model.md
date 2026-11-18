# Data Model: OCI Replicated Kafka Log Ingestion Cluster

**Feature**: [spec.md](spec.md) | **Research**: [research.md](research.md)

This feature has no database. Its "data" is configuration declared in Git, a few Kubernetes objects, and records on Kafka topics. Field-level formats live in [contracts/](contracts/); this page names each entity, its fields, its rules, and how it changes over time.

## 1. Kafka Node VM

Declared in `infra/kafka/cluster.yaml` (`nodes:`). The OCI layer renders it into Terraform and Ansible variables.

| Field | Type | Rule |
|---|---|---|
| `name` | `a1-1` \| `a1-2` \| `micro-1` \| `micro-2` | unique |
| `node_id` | int | 1–4, never reused for a different VM |
| `roles` | subset of {`broker`, `controller`} | exactly 3 nodes have `broker`, exactly 3 have `controller` (FR-002) |
| `private_ip` | IPv4 | `10.200.0.11`–`.14`, unchanged across rebuilds (FR-005) |
| `fault_domain` | `FD-1` \| `FD-2` \| `FD-3` | brokers in three distinct fault domains; voters in three distinct fault domains (R4) |
| `shape` | `A1.Flex 1/6` \| `E2.1.Micro` | from the Always Free list only (FR-026) |
| `arch` | `arm64` \| `amd64` | follows the shape |
| `kafka_memory_max` | bytes | from R16; at most the VM's memory minus the OS and the reserved Spec 014 memory |
| `wireguard` | bool | true only for broker nodes |

**Lifecycle**: `planned` → `created` (Terraform) → `configured` (Ansible) → `joined` (broker registered, or controller added to the quorum) → `serving`. On loss it goes `lost` → `rebuilt` → `joined`. A controller additionally passes through `removed-from-quorum` before `rebuilt` (R11).

## 2. Topic Declaration

Declared in `infra/kafka/cluster.yaml` (`topics:`).

| Field | Rule |
|---|---|
| `name` | `logs.proj.valid`, `logs.proj.quarantine`, `ops.platform.events`, `ops.platform.metrics`; Spec 014 adds more |
| `partitions` | 3 for `logs.*`, 1 for `ops.*` |
| `replication_factor` | always 3 |
| `min_insync_replicas` | 2 for `logs.*` (FR-003), 1 for `ops.*` (FR-023, FR-030) |
| `retention_ms` / `retention_bytes` | R12 table; the sum per broker must not exceed the per-broker Kafka budget |
| `max_message_bytes` | at least Fluent Bit's largest record after multiline joining (plan states both values) |
| `message_timestamp_type` | `LogAppendTime` for `ops.*` (broker-assigned time), `CreateTime` for `logs.*` |

The `check` in `scripts/kafka-declarations.py` enforces each rule above.

## 3. Kafka Client Identity and ACL

Declared in `infra/kafka/cluster.yaml` (`identities:`). ACLs are listed under each identity.

| Field | Rule |
|---|---|
| `name` | certificate CN and Kafka principal (`User:<name>`) |
| `key_location` | `vm:<node>`, `vm:all` (a separate key on every VM; only `ops-cli`), or `cluster:<namespace>/<secret>`; never `git` |
| `grants` | list of (resource type, name or prefix, operations); anything not listed is denied |
| `super_user` | true only for `broker-*` and `controller-*` |

The fixed set is in [research.md R9](research.md#r9-acls-and-authorization). Spec 014 adds consumer identities with read-only grants.

**Certificate lifecycle**: `requested` (CSR or Certificate created) → `issued` (90 days) → `renewing` (from day 60) → `issued`. If renewal fails, it moves to `expiring` (≤ 14 days, ops event `cert.expiring`) and then `expired` (connections refused).

## 4. WireGuard Key Slot

The private keys are in Secret `oci-link/wg-slot-keys`, the public keys and addresses in ConfigMap `oci-link/wg-slot-public`, and the holder in Lease `oci-link/wg-slot-<n>`.

| Field | Rule |
|---|---|
| `slot` | a number listed in ConfigMap `oci-link/wg-slot-pool` (declared in Git), within 0–13; `0,1,2,3` at start |
| `address` | `10.201.0.<n+1>/32` |
| `public_key` | copied into `infra/oci/ansible/group_vars/brokers/wg_slots.yml` through a pull request |
| `holder` | GKE node name, or empty |
| `lease_duration` / `renew_interval` | 90 s / 30 s |
| `annotations` | `oci-link.nacfson.io/fluent-bit-buffer-bytes`, `oci-link.nacfson.io/handshake-age-seconds`, written each renewal (R14) |

**State transitions**: `free` → `held(node)` on an exclusive Lease update; `held` → `free` on a graceful release, on lease expiry, or when the holder node is gone from the Node API (within 5 minutes, SC-009). Two holders at once is impossible, because the Lease update uses optimistic concurrency (resourceVersion).

## 5. Ops Event

A record on `ops.platform.events`. Format: [contracts/ops-events-and-metrics.md](contracts/ops-events-and-metrics.md). Key = `subject`.

| Field | Rule |
|---|---|
| `schema` | integer, currently 1 |
| `kind` | from the contract's kind list |
| `state` | `down` \| `up` \| `warn` \| `ok` |
| `subject` | `kafka/broker-<id>`, `kafka/controller-<id>`, `kafka/partition/<topic>-<p>`, `unit/<vm>/<unit>`, `disk/<vm>`, `cert/<vm>/<identity>`, `vm/<vm>` |
| `vm`, `observed_by` | VM names; can differ (a neighbour reports a dead VM) |

## 6. Health Metric Sample

A record on `ops.platform.metrics`. Key = `source`. One record per metric per sample (30 s).

| Field | Rule |
|---|---|
| `source` | `vm/<vm>` or `gke/<node>` |
| `metric` | from the contract's metric list |
| `value`, `unit` | number and unit string |

GKE-side samples come from Lease annotations and do not pass through Kafka. The relay merges both sources into one view.

## 7. Alert Incident (relay memory)

Held in the relay's memory and rebuilt on start from 24 hours of events and 6 hours of metrics.

| Field | Rule |
|---|---|
| `subject` | the incident's root subject (`vm/<vm>` when a VM-level cause is known) |
| `state` | `OK` \| `DOWN` \| `WARN` (per-subject state machine) |
| `opened_at`, `events[]`, `messages_sent[]` | grouping as in FR-023 |

**Transitions** (one message per arrow):
- `OK → DOWN`: a down event, or 3 minutes of heartbeat silence
- `OK → WARN`: a warn event, or a trend rule
- `DOWN → OK`, `WARN → OK`: the clearing event

Repeated events in the same state send nothing. The relay's own `cluster/unreachable` subject goes `OK → DOWN` after 60 s without a working connection. While it is `DOWN`, heartbeat-silence and stale-tunnel transitions are held back (FR-023).

## 8. Ansible Run (control center)

Held by the control center. Its result reaches this repository's world as a Kubernetes Event ([contracts/ansible-runner.md](contracts/ansible-runner.md)).

| Field | Rule |
|---|---|
| `commit` | a commit on `main` whose required checks succeeded |
| `playbook` | one of the allowlisted playbooks |
| `trigger` | `merge`, `daily`, or `operator` |
| `state` | `queued` → `running` → `ok` \| `failed` \| `blocked` |
| `finished_vms[]`, `failed_vm`, `failed_step` | set as the run progresses |

**Applied commit**: `/etc/nacfson/applied-commit` on each VM, written as the last task on that VM. It's the merge watcher's comparison point.

**Automation state**: `active` or `paused`. It changes only through the operator's passkey step-up.
