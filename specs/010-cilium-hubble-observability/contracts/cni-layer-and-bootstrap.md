# Contract: Git-Owned CNI Layer, Bootstrap Staging & Resource Budgeting

**Feature**: [spec.md](../spec.md) (`FR-001`, `FR-002`, `FR-009`, `US3`, `SC-004`) · **Decisions**: [research.md](../research.md) R1, R2, R3, R5, R6

---

## 1. K3s Systemd Unit & Host Preparation Contract (`bootstrap/`)

1. **Legacy CNI Interface Cleanup (`bootstrap/bootstrap.yml` Stage 0)**:
   Before starting K3s on a fresh or re-prepared host, clean up any lingering Flannel or CNI bridge interfaces and state (`/etc/cni/net.d`, `/var/lib/cni`, `flannel.1`, `cni0`) alongside `/var/lib/containerd` and `/etc/rancher`.
2. **K3s Server Flags (`bootstrap/templates/k3s.service.j2`)**:
   The K3s server unit MUST start with Flannel and K3s's built-in network policy controller disabled so Cilium owns Pod networking and policy enforcement while K3s `kube-proxy` remains active for ClusterIP (`10.43.0.1:443`) reachability:

```ini
ExecStart=/usr/local/bin/k3s server --flannel-backend=none --disable-network-policy
```

---

## 2. Git-Owned CNI Bundle (`deploy/platform/cni/`)

The directory `deploy/platform/cni/` MUST contain:
1. `upstream/cilium.yaml`: Rendered from `cilium/cilium` Helm chart version `1.20.3` with:
   - `--namespace kube-system`
   - `--set operator.replicas=1`
   - `--set ipam.mode=kubernetes`
   - `--set kubeProxyReplacement=false`
   - `--set envoy.enabled=false`
   - `--set tls.secretsNamespace.create=false`
   - `--set tls.secretsNamespace.name=kube-system`
   - `--set tls.secretSync.enabled=false`
   - `--set hubble.enabled=true`
   - `--set hubble.tls.enabled=false`
   - `--set hubble.eventBufferCapacity=4095`
   - `--set hubble.relay.enabled=true`
   - `--set hubble.ui.enabled=true`
   - `--set hubble.metrics.enabled="{dns,drop,tcp,flow}"`
   - `--set prometheus.enabled=true`
   - `--set operator.prometheus.enabled=true`
   - Kustomize patch (equivalent to `--set operator.unmanagedPodWatcher.restart=false`): `cilium-config` `unmanaged-pod-watcher-interval: "0"`. Leaving the watcher on while any node is still Flannel-attached makes the operator delete CoreDNS every ~30s, and on a CPU-full node each replacement preempts lower-priority pods (incident 2026-10-17: `postgres-0`). Pods are moved onto Cilium explicitly during host migration.
2. `kustomization.yaml`: References `upstream/cilium.yaml` and enforces explicit CPU/memory `requests` and `limits` across all containers and `initContainers`:
   - `DaemonSet/cilium` (`initContainers` × 6): `requests: {cpu: 10m, memory: 16Mi}`, `limits: {cpu: 100m, memory: 64Mi}`
   - `DaemonSet/cilium` (`cilium-agent`): `requests: {cpu: 100m, memory: 192Mi}`, `limits: {cpu: 500m, memory: 384Mi}`
   - `Deployment/cilium-operator` (`cilium-operator`): `requests: {cpu: 25m, memory: 64Mi}`, `limits: {cpu: 200m, memory: 192Mi}`
   - `Deployment/hubble-relay` (`hubble-relay`): `requests: {cpu: 15m, memory: 48Mi}`, `limits: {cpu: 150m, memory: 128Mi}`
   - `Deployment/hubble-ui` (`frontend`): `requests: {cpu: 10m, memory: 32Mi}`, `limits: {cpu: 100m, memory: 64Mi}`
   - `Deployment/hubble-ui` (`backend`): `requests: {cpu: 15m, memory: 48Mi}`, `limits: {cpu: 100m, memory: 96Mi}`

### Invariants
- Zero `Secret` resources in `deploy/platform/cni/`.
- Zero `Namespace` resources in `deploy/platform/cni/`.
- Every namespaced object in `deploy/platform/cni/` declares `metadata.namespace: kube-system`.
- All container images are pinned to exact upstream version tags and `@sha256:...` digests.

---

## 3. Ansible Bootstrap CNI Staging (`bootstrap/tasks/install.yml`)

Because K3s starts with `--flannel-backend=none`, the node remains `NotReady` (`NetworkPluginNotReady`) and `coredns` remains `Pending` until Cilium is installed.

1. **Controller Preflight (`bootstrap/tasks/preflight.yml`)**: Verify `{{ playbook_dir }}/../deploy/platform/cni` exists on the Ansible controller.
2. **Idempotent CNI Apply (`bootstrap/tasks/install.yml`)**: After `/etc/rancher/k3s/k3s.yaml` is created and `/readyz` responds:
   - Check if `k3s kubectl -n kube-system get daemonset cilium` already exists (`cni_preexisting`).
   - When `not cni_preexisting`:
     - Copy `{{ playbook_dir }}/../deploy/platform/cni/` to `/var/lib/nacfson-bootstrap/cni/` on the target VM.
     - Run `k3s kubectl apply --server-side -k /var/lib/nacfson-bootstrap/cni/`.
3. **Readiness Verification (`bootstrap/tasks/verify.yml`)**: Wait up to 300s for `daemonset/cilium` and `deployment/cilium-operator` in `kube-system` to roll out alongside `node_ready` and `coredns_ready`.

---

## 4. FluxCD Layer Graph (`clusters/<env>/layers.yaml`)

Both `clusters/local-k3s/layers.yaml` and `clusters/vps-k3s/layers.yaml` MUST include:

```yaml
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: cni
  namespace: flux-system
spec:
  interval: 5m
  retryInterval: 1m
  timeout: 5m
  prune: true
  wait: true
  sourceRef:
    kind: GitRepository
    name: flux-system
  path: ./deploy/platform/cni
```

And the `vault` Kustomization MUST list both `governance` and `cni` in `spec.dependsOn`:

```yaml
  dependsOn:
    - name: governance
    - name: cni
```

---

## 5. `platform-preflight.py` Reservation Accounting (`FR-009`, `SC-004`)

When `scripts/render-validate.sh <env>` renders `clusters/<env>/layers.yaml`, the `cni` layer is included in the candidate YAML. `scripts/platform-preflight.py` automatically counts all `kube-system` containers in `deploy/platform/cni/` toward `platformReservations`:

- **Added Platform Reservation**: `+225m` CPU, `+1248Mi` memory (`1695m` → `1920m` CPU; `4064Mi` → `5312Mi` memory).
- **Per-Project Slice (`n = 1`)**:
  - `local-k3s`: `2080m` CPU, `2880Mi` memory (`validationStatus: PASSED`)
  - `vps-k3s`: `2080m` CPU, `19264Mi` memory (`validationStatus: PASSED`)
