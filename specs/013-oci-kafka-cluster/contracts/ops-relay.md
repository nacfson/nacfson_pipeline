# Contract: Ops Relay (alerts and live health view)

**Implements**: FR-023, FR-028, FR-030, SC-011, SC-013, SC-014 | **Binary**: `services/error-pipeline/cmd/ops-relay` | **Namespace**: `observability`

## Inputs

| Input | How | Identity |
|---|---|---|
| `ops.platform.events`, `ops.platform.metrics` | Kafka consumer group `ops-relay`, mutual TLS | `ops-relay` (read and describe only) |
| Cluster membership | Kafka Admin describe-cluster every 30 s on the same connection | `ops-relay` |
| `oci-link` Events and Leases | Kubernetes watch | ServiceAccount `ops-relay`, Role: events/leases get/list/watch in `oci-link` |

The `oci-link` Events include the slot agent's (`NoFreeSlot`, `TunnelStale`, `TunnelRecovered`) and the Ansible runner's (`DeployOK`, `DeployFailed`, `DeployBlocked`, see [ansible-runner.md](ansible-runner.md#result-events)).

## Output: notifications

Sent through the existing `vault-notify-proxy` API exactly as `error-detector` sends today (shared `internal/notify` package). One message per state transition:

```text
[kafka] DOWN  vm/micro-1 — broker 3 fenced (observed by a1-1) · 2 partitions with 2/3 copies
[kafka] OK    vm/micro-1 — recovered after 14 min · all partitions 3/3
[kafka] WARN  disk/a1-2 — 23% free, at the current rate crosses 20% in 2.4 days
[kafka] DOWN  cluster/unreachable — no working Kafka connection from GKE for 60 s (sent directly)
[kafka] DOWN  vm/micro-2 — agent silent for 3 min, controller quorum 2/3
[kafka] DOWN  deploy/site.yml — 4f2c9e1 failed on micro-1 at kafka/restart
[kafka] OK    deploy/site.yml — 7a10b3d applied to 4 VMs in 31 min
```

Delayed messages (sent after a relay restart) are prefixed `(delayed, event at 09:12 UTC)`.

## Output: live health view (FR-028)

`kubectl -n observability exec deploy/ops-relay -- ops-relay status` talks to the Unix socket `/run/ops-relay/status.sock` inside the pod. No Service, port, or route exists.

```text
ops-relay status · 2026-11-17 09:20:31 UTC · last event 4 s ago · last metric 12 s ago
cluster   brokers 3/3   controllers 3/3 (leader 1)   under-replicated 0   offline 0
broker    1 a1-1     produce p99 6 ms   in 41 KB/s   heap 612/1024 MB   disk 71% free   cert 58 d
broker    2 a1-2     produce p99 7 ms   in 40 KB/s   heap 590/1024 MB   disk 70% free   cert 58 d
broker    3 micro-1  produce p99 11 ms  in 40 KB/s   heap 301/384 MB    disk 69% free   cert 58 d
ctrl      4 micro-2  heap 180/256 MB    disk 88% free   cert 58 d
gke       node-a slot 0  buffer 0 B / 256 MiB   handshake 21 s
gke       node-b slot 1  buffer 0 B / 256 MiB   handshake 9 s
incidents none open
```

`ops-relay status --json` prints the same data as one JSON object for scripts and the runbook.

## Process rules

- Readiness: true after state has been rebuilt from the last 24 h of `ops.platform.events` and the last 6 h of `ops.platform.metrics` (so trend rules work right after a restart). Liveness: process only (Constitution VII spirit; no peer checks).
- Requests = limits 10m / 64Mi. `NetworkPolicy`: egress to `10.200.0.11-13/32:9092`, `vault-notify-proxy:8080`, the Kubernetes API, and DNS. No ingress.
- One replica. Restarts lose no messages, because they are rebuilt from the topic (data-model §7).
