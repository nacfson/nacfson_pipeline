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

## 3. Single L4 Pass-Through LoadBalancer Edge (`deploy/platform/traefik/`)

**Changed by Spec 015 (2026-11-14).** The earlier zero-LoadBalancer edge (Traefik `hostPort: 80/443` on the
`ondemand-core` node's static IP) is replaced by exactly one GKE-managed regional L4 pass-through load
balancer, created from `Service/platform/traefik` (`type: LoadBalancer`,
`loadBalancerClass: networking.gke.io/l4-regional-external`, `externalTrafficPolicy: Local`) on the reserved
address `gke-ingress-ip` owned by `infra/gcp/gke`. Traefik no longer declares `hostPort`, keeps
`strategy: Recreate` and the `cloud.google.com/gke-provisioning: standard` node selector, and still
terminates TLS. Project Services stay `ClusterIP`; preflight still rejects project `NodePort` and `LoadBalancer`.

Full contract, render invariant and preflight rule:
[`specs/015-gke-terraform-infra/contracts/traefik-l4-edge.md`](../../015-gke-terraform-infra/contracts/traefik-l4-edge.md).

---

## 4. Governance Admission Allowlist (`FR-002`)

`deploy/platform/governance/manual-change-policy.yaml` MUST permit GKE control-plane and addon principals alongside existing K3s principals without opening manual mutation paths:
- Usernames: `system:gke-common-webhooks`, `system:gcp-controller-manager`, `system:addon-manager`, `system:cluster-autoscaler`, and `request.userInfo.username.startsWith('system:serviceaccount:gke-managed-')`.
- Cluster-scoped CRDs (`gitops-managed-cluster-kinds`): `.gke.io`, `.cloud.google.com`, `.networking.gke.io` suffixes alongside `.cattle.io` and `.k3s.io`.
