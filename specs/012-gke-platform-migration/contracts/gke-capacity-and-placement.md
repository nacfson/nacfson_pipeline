# Contract: GKE Multi-Node Capacity, Pool Placement & Zero-LB Edge Ingress

**Feature**: [`specs/012-gke-platform-migration/spec.md`](../spec.md)
**Enforced By**: `scripts/platform-preflight.py`, `scripts/render-invariants.py`, `deploy/platform/governance/manual-change-policy.yaml`

---

## 1. Multi-Node `capacity.yaml` Schema (`clusters/gke/capacity.yaml`)

`scripts/platform-preflight.py` MUST accept optional `nodeCount` (default `1`) and `pools` fields while remaining 100% backward-compatible with single-node `clusters/local-k3s/capacity.yaml` and `clusters/vps-k3s/capacity.yaml`:

```yaml
environment: gke
nodeName: gke-zonal-standard
observedAt: "2026-11-10T00:00:00Z"
nodeCount: 2
allocatable:
  cpuMillicores: 1880
  memoryMib: 4408
systemReserved:
  cpuMillicores: 350
  memoryMib: 576
pools:
  - name: ondemand-core
    machineType: e2-medium
    nodeCount: 1
    provisioning: standard
    allocatable:
      cpuMillicores: 940
      memoryMib: 2972
    systemReserved:
      cpuMillicores: 200
      memoryMib: 320
    labels:
      kubernetes.io/os: "linux"
      cloud.google.com/gke-provisioning: "standard"
    taints: []
  - name: spot-controllers
    machineType: e2-small
    nodeCount: 1
    provisioning: spot
    allocatable:
      cpuMillicores: 940
      memoryMib: 1436
    systemReserved:
      cpuMillicores: 150
      memoryMib: 256
    labels:
      kubernetes.io/os: "linux"
      cloud.google.com/gke-spot: "true"
      cloud.google.com/gke-provisioning: "spot"
    taints:
      - "cloud.google.com/gke-spot=true:NoSchedule"
```

---

## 2. Preflight Multi-Node & Per-Pool Invariants (`FR-001`, `FR-003`)

1. **Pool Sum Consistency**: When `pools` is declared, `sum(p.nodeCount) == nodeCount`, `sum(p.allocatable) == allocatable`, and `sum(p.systemReserved) == systemReserved`; mismatch fails Rule 1 (`STATUS_REJECTED_NO_MEASUREMENT`).
2. **DaemonSet Multiplier**: Every platform `DaemonSet` (`fluent-bit`) has its container `requests.cpu` and `limits.memory` multiplied by the number of nodes whose taints it tolerates (`2 ×` on `gke` once `fluent-bit` declares toleration for `cloud.google.com/gke-spot=true:NoSchedule`).
3. **Spot Workload Allowlist**: Only PVC-free background controller `Deployment`s (`cert-manager`, `cert-manager-cainjector`, `cert-manager-webhook` in `cert-manager`, and `source-controller`, `kustomize-controller` in `flux-system`) MAY carry `nodeSelector: {"cloud.google.com/gke-spot": "true"}` and the `cloud.google.com/gke-spot=true:NoSchedule` toleration.
4. **Zero `RWO` PVC or Live Auth/Tenant Pods on Spot**: Preflight MUST reject any manifest in `clusters/gke/` that schedules `proj-*` workloads, `keycloak`, `gateway`, `traefik`, `openbao`, `postgres`, `error-detector`, or any workload mounting a `PersistentVolumeClaim` onto a `provisioning: spot` pool.
5. **Per-Pool Schedulability**: Steady-state CPU `requests` and memory `requests`/`limits` assigned to each pool MUST NOT exceed `pool.allocatable - pool.systemReserved`, and `ondemand-core` MUST retain `>= 50m` `requests.cpu` and `>= 192 MiB` `requests.memory` headroom for concurrent `00:00 UTC` backup `CronJob`s (`postgres-daily-backup` `128Mi` + `openbao-daily-snapshot` `64Mi`).
6. **Cluster-Wide $1/n$ Slice**: `applicationCapacity = allocatable - platformReservations` divided across `project_count = 3` MUST yield `slice_cpu >= 150m` and `slice_mem >= 384Mi` (`(303m, 418Mi)` under Design 1 with `platformReservations = (970m, 3152Mi)`).

---

## 3. Zero-LoadBalancer Edge Ingress Contract (`deploy/platform/traefik/`)

1. `Deployment/traefik` runs in namespace `platform` (`replicas: 1`, `strategy.type: Recreate`, `runAsNonRoot: true`, `runAsUser: 65532`, `automountServiceAccountToken: true` for CRD watch only, `capabilities.drop: ["ALL"]`, `resources: {requests: {cpu: 30m, memory: 64Mi}, limits: {cpu: 200m, memory: 96Mi}}`).
2. Container `traefik` (`traefik:v3.3.4`) binds `--entrypoints.web.address=:8000` (`hostPort: 80`) and `--entrypoints.websecure.address=:8443` (`hostPort: 443`) and pins `nodeSelector: {"cloud.google.com/gke-provisioning": "standard"}`.
3. `Service/traefik` in namespace `platform` MUST use `type: ClusterIP` (preflight rejects `Service` `type: LoadBalancer` in `clusters/gke/` to prevent accidental \$18/mo forwarding rules).

---

## 4. Governance Admission Allowlist (`FR-002`)

`deploy/platform/governance/manual-change-policy.yaml` MUST permit GKE control-plane and addon principals alongside existing K3s principals without opening manual mutation paths:
- Usernames: `system:gke-common-webhooks`, `system:gcp-controller-manager`, `system:addon-manager`, `system:cluster-autoscaler`, and `request.userInfo.username.startsWith('system:serviceaccount:gke-managed-')`.
- Cluster-scoped CRDs (`gitops-managed-cluster-kinds`): `.gke.io`, `.cloud.google.com`, `.networking.gke.io` suffixes alongside `.cattle.io` and `.k3s.io`.
