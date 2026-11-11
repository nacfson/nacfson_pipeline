# Phase 1 Data Model: GKE Platform Portability & Full-State Cutover

**Feature**: [`specs/012-gke-platform-migration/spec.md`](spec.md)
**Date**: 2026-11-10

---

## 1. Entity: `ClusterCapacityProfile` (`clusters/<env>/capacity.yaml`)

Declares measured Kubernetes node allocatable capacity, system reservations, node count, and optional heterogeneous node pools for `scripts/platform-preflight.py` multi-node $1/n$ budget and per-pool placement validation.

### Fields

| Field | Type | Required | Description / Constraints |
| :--- | :--- | :--- | :--- |
| `environment` | `string` | Yes | Must match `--environment` (`local-k3s`, `vps-k3s`, `gke`). |
| `nodeName` | `string` | Conditional | Primary node or cluster identifier (`gke-zonal-standard`). |
| `observedAt` | `string` (RFC3339) | Yes | Timestamp of capacity measurement or official GKE machine spec calculation. |
| `nodeCount` | `integer` | No (default `1`) | Total worker nodes (`2` for `clusters/gke/capacity.yaml`). Multiplies `DaemonSet` CPU/memory reservations. |
| `allocatable.cpuMillicores` | `integer` | Yes | Sum of `node.status.allocatable.cpu` across all pools (`1880` on GKE: `940 + 940`). Must be `> 0`. |
| `allocatable.memoryMib` | `integer` | Yes | Sum of `node.status.allocatable.memory` across all pools (`4408` on GKE: `2972 + 1436`). Must be `> 0`. |
| `systemReserved.cpuMillicores` | `integer` | Yes | Aggregate `kube-system` / CNI / Workload Identity CPU reservation (`350` on GKE). Must be `> 0`. |
| `systemReserved.memoryMib` | `integer` | Yes | Aggregate `kube-system` memory reservation (`576` on GKE with `--logging=NONE --monitoring=NONE`). Must be `> 0`. |
| `pools` | `list<NodePoolAllocation>` | No | Optional heterogeneous node pools (`ondemand-core`, `spot-controllers`). When present, `sum(pool.allocatable) == allocatable` and `len(pools) == nodeCount` (for 1-node-per-pool topologies). |

---

## 2. Entity: `NodePoolAllocation` (Element of `ClusterCapacityProfile.pools`)

Models an individual GKE node pool so `platform-preflight.py` verifies both per-node CPU/memory schedulability and zero-`RWO`-PVC-on-Spot isolation.

### Fields

| Field | Type | Required | Description / Constraints |
| :--- | :--- | :--- | :--- |
| `name` | `string` | Yes | Pool identifier (`ondemand-core`, `spot-controllers`). |
| `machineType` | `string` | Yes | GCE machine type (`e2-medium`, `e2-small`). |
| `nodeCount` | `integer` | Yes | Nodes in this pool (`1` each in Design 1). |
| `provisioning` | `enum(standard, spot)` | Yes | `standard` (On-Demand) or `spot` (GKE Spot VM). |
| `allocatable.cpuMillicores` | `integer` | Yes | `940` (`e2-medium`) / `940` (`e2-small`). |
| `allocatable.memoryMib` | `integer` | Yes | `2972` (`e2-medium`) / `1436` (`e2-small`). |
| `systemReserved.cpuMillicores` | `integer` | Yes | `kube-system` pods on this node (`200` on `ondemand-core`, `150` on `spot-controllers`). |
| `systemReserved.memoryMib` | `integer` | Yes | `kube-system` memory on this node (`320` on `ondemand-core`, `256` on `spot-controllers`). |
| `labels` | `map<string,string>` | Yes | Node labels matched by `nodeSelector` (e.g., `cloud.google.com/gke-spot: "true"` or `cloud.google.com/gke-provisioning: "spot"`). |
| `taints` | `list<string>` | No | Taints on pool (`["cloud.google.com/gke-spot=true:NoSchedule"]` on `spot-controllers`). |

### Validation Invariants (`platform-preflight.py`)

1. **DaemonSet Fan-Out**: Every `DaemonSet` (`fluent-bit`) schedules once per node in every pool that its `tolerations` match (`spot-controllers` requires `fluent-bit` to tolerate `cloud.google.com/gke-spot=true:NoSchedule` so logs from Spot controllers are collected or observed cleanly).
2. **Zero `RWO` PVC on Spot**: Any `StatefulSet`, `Deployment`, `Job`, or `CronJob` mounting a `PersistentVolumeClaim` (`openbao`, `postgres`, `error-detector`, `postgres-daily-backup`, `openbao-daily-snapshot`) MUST NOT target a `provisioning: spot` pool.
3. **24/7 Auth & Tenant Continuity**: All `proj-*` workloads, `traefik`, `gateway`, and `keycloak` MUST schedule on `provisioning: standard` (`ondemand-core`).
4. **Per-Pool Fit**: For every pool `p`, `p.systemReserved + sum(DaemonSets on p) + sum(steady workloads assigned to p) <= p.allocatable` for both CPU requests and memory requests/limits.

---

## 3. Entity: `CollectorRecoveryState` (`/var/lib/fluent-bit-state`)

Node-local persistent directory mounted at `/state` inside `DaemonSet/fluent-bit` (namespace `observability`).

### Directory Layout & Fields

| Path | Format | Owner / Mode | Lifecycle & Cap |
| :--- | :--- | :--- | :--- |
| `/var/lib/fluent-bit-state/tail.db` | SQLite 3 WAL | `0:0` (`fluent-bit`) | Tracks `(inode, offset)` per `/var/log/pods/proj-*_*/*/*.log` file across pod restarts. |
| `/var/lib/fluent-bit-state/chunks/tail.0/` | Fluent Bit `.flb` chunk files | `0:0` | Raw tailed log chunks buffered before filter pipeline completion. |
| `/var/lib/fluent-bit-state/chunks/multiline.*/` & `rt_quarantine/` | Fluent Bit `.flb` chunk files | `0:0` | Filesystem-backed emitter buffers (`emitter_storage.type filesystem`, `Emitter_Storage.type filesystem`). |
| `/var/lib/fluent-bit-state/chunks/` (Output backlog) | Fluent Bit `.flb` chunk files | `0:0` | Unacknowledged `[OUTPUT] kafka` chunks capped at `192M` (`logs.proj.valid`) + `64M` (`logs.proj.quarantine`) = **`256 MiB` max per node**. |

### State Transitions

```text
[Container writes stdout/stderr]
       │
       ▼
[INPUT tail reads line] ──► Persists chunk in /state/chunks + updates /state/tail.db
       │
       ▼
[Lua Contract v3 filter] ──► Routes valid (proj.*) vs quarantine (quarantine.proj.*) via fs emitter
       │
       ├──► Sink reachable (HTTP 2xx on local-k3s / Kafka acks=-1 on GKE) ──► Chunk fs-unlinked & offset committed
       │
       └──► Sink unreachable or Pod SIGTERM/crash ──► Chunks remain in /var/lib/fluent-bit-state/chunks (<= 256 MiB)
                                                       └──► Next Pod start reloads chunks & tail.db -> zero loss/duplication
```

---

## 4. Entity: `PortableEgressPolicy` (`networking.k8s.io/v1` `NetworkPolicy`)

Standard Kubernetes L3/L4 `NetworkPolicy` replacing `cilium.io/v2` `CiliumNetworkPolicy` across `deploy/`.

### Validation Rules (`platform-preflight.py`)

| Target Pod (`namespace/app.kubernetes.io/name`) | Policy Name | Permitted Public Egress | Mandatory `except` CIDRs | Permitted Internal Egress |
| :--- | :--- | :--- | :--- | :--- |
| `proj-pn/pn-backend` | `pn-backend-translation-egress` | `0.0.0.0/0` TCP `:443` | `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`, `169.254.169.254/32` | Inherited from `project-pn-isolation` (`kube-dns:53`, `vault-db-proxy:5432`, `vault-ai-proxy:8080`, `gateway:8080`) |
| `vault/vault-ai-proxy` | `ai-proxy-policy` | `0.0.0.0/0` TCP `:443` | `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`, `169.254.169.254/32` | `openbao:8200`, `kube-apiserver` (`:443/:6443`) |
| `vault/vault-notify-proxy` | `notify-proxy-policy` | `0.0.0.0/0` TCP `:443` | `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`, `169.254.169.254/32` | `openbao:8200`, `kube-apiserver` (`:443/:6443`) |
| `vault/openbao` | `openbao-ingress-policy` | **None** (`0.0.0.0/0` forbidden) | N/A | `openbao:8201`, `kube-apiserver` (`:443/:6443`); on GKE overlay only: `169.254.169.254/32:80,988` (Workload Identity) + `199.36.153.8/30:443` (`private.googleapis.com` for Cloud KMS) |

---

## 5. Entity: `FullStateMigrationBundle` (Out-of-Git Cutover Archive)

Local operator staging directory (`chmod 0700`, never inside the Git worktree) produced at Gate 3/4 and verified before restore.

### Artifact Manifest (`manifest.sha256`)

| File | Source (`vps-k3s`) | Target (`local-k3s` Drill / `gke` Cutover) | Verification Gate |
| :--- | :--- | :--- | :--- |
| `openbao.snap` | `openbao-0` (`bao operator raft snapshot save`) | `openbao-0` (`bao operator raft snapshot restore` → `vault-configure-k8s-auth.sh` → `vault-reconcile.sh --confirm` → `bao operator unseal -migrate` on GKE) | SHA-256 + `bao status` (`sealed: false`) + proxy login check |
| `keycloak.dump.gz` | `postgres-0` (`db-migrate-dump.sh --keycloak --export`) | `postgres-0` (`db-migrate-dump.sh --keycloak --file`) | SHA-256 + realm `platform` user count & `(issuer, subject)` smoke test |
| `proj_pn.dump.gz` | `postgres-0` (`db-migrate-dump.sh --project pn --export`) | `postgres-0` (`db-migrate-dump.sh --project pn --file`) | SHA-256 + `\dt` table count as `user_pn` |
| `proj_health.dump.gz` | `postgres-0` (`db-migrate-dump.sh --project health --export`) | `postgres-0` (`db-migrate-dump.sh --project health --file`) | SHA-256 + `\dt` table count as `user_health` |
| `edge-tls-secrets.yaml` | `platform/{auth,pn,health,blog}-tls` + `cert-manager/letsencrypt-prod-account` (stripped of `uid`/`resourceVersion`) | Applied to `platform` & `cert-manager` via `platform:break-glass` before DNS flip | SHA-256 + `openssl x509 -noout -subject -dates` + HTTPS SNI check |
| `victoria-logs-data.tar.gz` | `victorialogs-0:/victoria-logs-data` | Verified at Gate 3/4; imported onto OCI VM 4 in Spec 013 | SHA-256 + `tar -tzf` integrity check |
