# Contract: Ops Events and Health Metrics

**Topics**: `ops.platform.events`, `ops.platform.metrics` | **Version**: schema 1 | **Implements**: Spec 013 FR-023, FR-030

This is the live copy of the contract designed in [Spec 013](../specs/013-oci-kafka-cluster/contracts/ops-events-and-metrics.md). Producers are the OCI event agents; the GKE-side metrics come from Lease annotations. The consumer is `ops-relay`, and later Spec 014. A field rename or removal is a new schema number and a pull request that changes this contract, the Lua rules, and the relay together.

## Common rules

- One JSON object per Kafka record, UTF-8, no trailing newline.
- `time` is not sent by producers. Consumers use the record's broker timestamp (`LogAppendTime`).
- Unknown fields must be ignored by consumers. Missing required fields make the record invalid; the relay counts and drops it.

## Event record (`ops.platform.events`, key = `subject`)

```json
{
  "schema": 1,
  "kind": "broker.fenced",
  "state": "down",
  "subject": "kafka/broker-3",
  "vm": "micro-1",
  "observed_by": "a1-1",
  "source": "kafka-controller-log",
  "detail": "Fencing broker 3 because its session has timed out"
}
```

| Field | Required | Values |
|---|---|---|
| `schema` | yes | `1` |
| `kind` | yes | table below |
| `state` | yes | `down`, `up`, `warn`, `ok` |
| `subject` | yes | see [Spec 013 data-model.md §5](../specs/013-oci-kafka-cluster/data-model.md#5-ops-event) |
| `vm` | yes | VM the subject belongs to |
| `observed_by` | yes | VM whose agent produced the record |
| `source` | yes | `kafka-controller-log`, `kafka-broker-log`, `systemd`, `sample`, `cert-check`, `heartbeat` |
| `detail` | no | original log line or short text, at most 1 KiB |

| `kind` | `state` | Produced when | Cleared by |
|---|---|---|---|
| `broker.fenced` | down | controller log: broker session timed out or broker shut down | `broker.unfenced` (up) |
| `partition.isr_shrink` | down | leader log: ISR shrink for a partition | `partition.isr_expand` (up) |
| `unit.failed` | down | systemd: `kafka.service` or `wg-quick@wg0.service` left `active` | `unit.active` (up) |
| `disk.low` | warn | free space on `/var/lib/kafka` crossed below 20% | `disk.ok` (ok) |
| `cert.expiring` | warn | a local certificate has fewer than 14 days left | `cert.renewed` (ok) |
| `agent.heartbeat` | ok | every 60 s | (none) |

The exact Kafka log lines that map to `broker.fenced`, `broker.unfenced`, `partition.isr_shrink`, and `partition.isr_expand` are captured from `apache/kafka:4.3.2` during the rehearsal and stored under [`tests/fixtures/kafka-logs/4.3.2/`](../tests/fixtures/kafka-logs/4.3.2/). The Lua rules are tested against those files only.

## Metric record (`ops.platform.metrics`, key = `source`)

```json
{ "schema": 1, "source": "vm/micro-1", "metric": "kafka.heap.used", "value": 251658240, "unit": "bytes" }
```

| `metric` | `unit` | From |
|---|---|---|
| `kafka.brokers.active` | count | JMX exporter (controller nodes) |
| `kafka.brokers.fenced` | count | JMX exporter (controller nodes) |
| `kafka.controller.active` | count | JMX exporter |
| `kafka.partitions.under_replicated` | count | JMX exporter (brokers) |
| `kafka.partitions.under_min_isr` | count | JMX exporter (brokers) |
| `kafka.partitions.offline` | count | JMX exporter (controller nodes) |
| `kafka.produce.latency_p99` | ms | JMX exporter (brokers) |
| `kafka.bytes_in` / `kafka.bytes_out` | bytes/s | JMX exporter (brokers) |
| `kafka.heap.used` / `kafka.gc.time` | bytes / ms per min | JMX exporter |
| `os.mem.available` | bytes | `/proc/meminfo` |
| `os.disk.free_ratio` | ratio | `df /var/lib/kafka` |
| `os.cpu.steal` | ratio | `/proc/stat` |
| `cert.days_left.min` | days | certificate check |

GKE-side values, read by the relay from Lease annotations and shown in the same view with `source = gke/<node>`:

| `metric` | `unit` | Annotation on `oci-link/wg-slot-<n>` |
|---|---|---|
| `fluentbit.buffer.bytes` | bytes | `oci-link.nacfson.io/fluent-bit-buffer-bytes` |
| `wireguard.handshake_age` | seconds (max over peers) | `oci-link.nacfson.io/handshake-age-seconds` |

## Relay rules this contract feeds

| Rule | Input | Fires |
|---|---|---|
| VM silent | no `agent.heartbeat` for `vm/<vm>` for 180 s (not within 180 s of relay start; not while `cluster/unreachable` is DOWN) | DOWN for `vm/<vm>`, labelled "broker down" if `kafka.brokers.active` dropped, otherwise "agent silent, broker healthy" |
| Disk trend | `os.disk.free_ratio` slope over the last 6 h projects crossing 0.20 within 72 h (needs 6 h of samples from that VM, so a rebuilt VM is covered only by `disk.low` until then) | WARN `disk/<vm>` |
| Buffer filling | `fluentbit.buffer.bytes` above 50% of 256 MiB on any node | WARN `gke/<node>/buffer` |

Tunnel alerts come only from the slot agent's `TunnelStale` and `TunnelRecovered` Events (DOWN and OK for `gke/<node>/tunnel`), and are held back while `cluster/unreachable` is DOWN. `wireguard.handshake_age` is shown in the live view and raises nothing.
