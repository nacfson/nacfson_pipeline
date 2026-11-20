# Contract: Kafka Declarations (`infra/kafka/cluster.yaml`)

**Implements**: FR-003, FR-015, FR-024, FR-025, SC-008, SC-012 | **Tool**: `scripts/kafka-declarations.py`

The single, location-neutral source for the cluster. It must not mention OCI, VMs' shapes, Terraform, or Ansible. Those layers read it, never the other way round.

## Shape

```yaml
schema: 1
cluster:
  id: <22-char base64 UUID from kafka-storage random-uuid>
  kafka_version: 4.3.2
  image: docker.io/apache/kafka:4.3.2
  image_digest: sha256:<multi-arch index digest>
nodes:
  - { name: a1-1,    node_id: 1, roles: [broker, controller], address: 10.200.0.11, initial_directory_id: <22-char base64url> }
  - { name: a1-2,    node_id: 2, roles: [broker, controller], address: 10.200.0.12, initial_directory_id: <22-char base64url> }
  - { name: micro-1, node_id: 3, roles: [broker],             address: 10.200.0.13 }
  - { name: micro-2, node_id: 4, roles: [controller],         address: 10.200.0.14, initial_directory_id: <22-char base64url> }
listeners:
  client: { port: 9092, tls: mutual }
  controller: { port: 9093, tls: mutual }
broker_settings:
  auto.create.topics.enable: false
  compression.type: producer
  allow.everyone.if.no.acl.found: false
  default.replication.factor: 3
  min.insync.replicas: 2
topics:
  - name: logs.proj.valid
    partitions: 3
    replication_factor: 3
    min_insync_replicas: 2
    retention_ms: 604800000
    retention_bytes: 5368709120
    segment_bytes: 134217728
    max_message_bytes: 1048588
    timestamp_type: CreateTime
identities:
  - name: fluent-bit-gke
    key_location: cluster:observability/fluent-bit-kafka-client
    grants:
      - { resource: topic, name: logs.proj.valid, ops: [Write, Describe] }
      - { resource: topic, name: logs.proj.quarantine, ops: [Write, Describe] }
      - { resource: cluster, ops: [IdempotentWrite] }
bootstrap:
  gke: [10.200.0.11:9092, 10.200.0.12:9092, 10.200.0.13:9092]
  rehearsal: [broker-1:9092, broker-2:9092, broker-3:9092]
```

The full topic and identity list is in [research.md R9 and R12](../research.md).

## Renders

| Command | Output | Consumer |
|---|---|---|
| `render --target ansible` | `infra/oci/ansible/group_vars/all/kafka.generated.yml` | Ansible roles `kafka`, `certs`, `acls`, `topics` |
| `render --target compose` | `tests/kafka/rehearsal/compose.generated.yaml` plus broker properties | `scripts/kafka-rehearsal.sh` |
| `render --target strimzi` | `build/strimzi/*.yaml` (`Kafka`, `KafkaNodePool` ×2, `KafkaTopic` ×4, `KafkaUser` ×N), not committed and never applied | kubeconform with pinned Strimzi 1.3.1 schemas |

Generated files carry no comments (Constitution, No Comments in Code). CI re-renders and fails on a diff.

## `check` (CI, offline, no credentials)

Fails, naming the field, when:

1. A `Brokers` line in `clusters/gke/observability/fluent-bit-kafka.conf` differs from `bootstrap.gke`.
2. A `Topics` line there names a topic that is not declared, or a declared `logs.*` topic is not produced by Fluent Bit.
3. Any `logs.*` topic has `replication_factor < 3` or `min_insync_replicas < 2`, or any topic has `replication_factor < 3`.
4. The Fluent Bit output lacks `rdkafka.security.protocol ssl`, the CA, certificate, or key location, or `rdkafka.compression.codec zstd`.
5. `fluent-bit-gke` has any grant other than Write and Describe on its two topics plus cluster IdempotentWrite, or any identity other than `broker-*`/`controller-*` is a super user.
6. Exactly three nodes have `broker`, exactly three have `controller`, node IDs or addresses repeat, an address leaves `10.200.0.0/24`, or a controller lacks an `initial_directory_id` (used only by the first `format --initial-controllers`; a replaced controller gets a new directory ID at runtime).
7. The sum of `retention_bytes × partitions` plus one segment per partition exceeds the per-broker budget (20 GB).
8. The Strimzi render fails kubeconform.

SC-008 and SC-012 are proven by `tests/infra/test_kafka_declarations.py`, which mutates a copy of the file for each rule and expects a failure.
