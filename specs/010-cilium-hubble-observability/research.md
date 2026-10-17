# Research: Cilium + Hubble CNI & Visual Platform Network Observability

**Feature**: [spec.md](spec.md) · **Plan**: [plan.md](plan.md) · **Date**: 2026-10-16

Each section resolves a technical or architectural decision from the specification and Technical Context. Format: Decision, Rationale, Alternatives considered.

---

## R1. Static Manifest Vendoring vs. Helm Controller for Cilium (`FR-002`, `FR-009`)

**Decision**: Render Cilium `v1.20.3` (`quay.io/cilium/cilium:v1.20.3@sha256:...` and `quay.io/cilium/hubble-ui:v0.13.6@sha256:...`) from the official `cilium/cilium` Helm chart into a single Git-owned static manifest bundle under [`deploy/platform/cni/`](../../deploy/platform/cni/) (`upstream/cilium.yaml` + `kustomization.yaml`), matching the pattern established for [`deploy/platform/cert-manager/`](../../deploy/platform/cert-manager/).

**Rationale**:
- In `nacfson_pipeline`, FluxCD runs only `source-controller` and `kustomize-controller` (no `helm-controller`), keeping control-plane RAM overhead minimal on single-node K3s.
- [`scripts/render-validate.sh`](../../scripts/render-validate.sh) and [`scripts/platform-preflight.py`](../../scripts/platform-preflight.py) inspect static Kustomize renders directly via `flux build kustomization --dry-run`.
- Having the full CNI manifest bundle in `deploy/platform/cni/` allows Ansible bootstrap ([`bootstrap/tasks/install.yml`](../../bootstrap/tasks/install.yml)) to apply the exact same directory via `k3s kubectl apply --server-side -k` before `verify.yml` checks `node_ready` and `coredns_ready`, and then lets FluxCD (`cni` layer in `clusters/*/layers.yaml`) continuously reconcile drift and prune stale objects.

**Alternatives considered**:

| Alternative | Rejected because |
| :--- | :--- |
| K3s `/var/lib/rancher/k3s/server/manifests/` `HelmChart` CRD | K3s `helm-install` jobs run outside FluxCD and are invisible to `scripts/render-validate.sh` and `scripts/platform-preflight.py` (violates `FR-009` and Constitution Principle I). |
| Imperative `cilium install` CLI during bootstrap | Leaves CNI state unmanaged by GitOps and drifts silently across upgrades. |
| Adding Flux `helm-controller` | Adds another always-on controller pod to `flux-system` for a single static chart and prevents `bootstrap/tasks/install.yml` from applying the CNI before Flux is installed. |

---

## R2. K3s CNI Flags & `kube-proxy` Compatibility for Environment Portability (`FR-001`, Principle VI)

**Decision**: Configure [`bootstrap/templates/k3s.service.j2`](../../bootstrap/templates/k3s.service.j2) with:

```ini
ExecStart=/usr/local/bin/k3s server --flannel-backend=none --disable-network-policy
```

and configure Cilium with `ipam.mode=kubernetes` and `kubeProxyReplacement=false` (retaining K3s's built-in `kube-proxy` for ClusterIP `10.43.0.1:443` service translation while Cilium's eBPF datapath owns Pod IP routing, `NetworkPolicy` / `CiliumNetworkPolicy` enforcement, and Hubble flow telemetry).

**Rationale**:
- `--flannel-backend=none` disables Flannel VXLAN so Cilium is the sole CNI plugin.
- `--disable-network-policy` disables K3s's embedded `kube-router` network policy controller so Cilium's eBPF datapath is the single authoritative policy enforcement engine (emitting `FORWARDED` and `DROPPED` verdicts to Hubble).
- Keeping `kubeProxyReplacement=false` allows `cilium-agent` and `cilium-operator` to connect to the Kubernetes API server via the standard `KUBERNETES_SERVICE_HOST=10.43.0.1:443` ClusterIP without hardcoding node-specific VM IP addresses into `deploy/platform/cni/`. As a result, the exact same `deploy/platform/cni/` manifest renders identically for `local-k3s` and `vps-k3s` (Constitution Principle VI).

**Alternatives considered**:

| Alternative | Rejected because |
| :--- | :--- |
| `--disable-kube-proxy` with `kubeProxyReplacement=true` | Requires hardcoding `k8sServiceHost: <VM_IP>` and `k8sServicePort: 6443` per environment or deploying extra per-node ConfigMaps before the CNI starts. |
| Leaving K3s `--disable-network-policy` unset | Runs both `kube-router` iptables policies and Cilium eBPF policies concurrently, causing silent iptables drops that bypass Hubble's `DROPPED` verdict stream. |

---

## R3. Zero-Secret Cilium & Hubble Rendering (`FR-002`, Principle V)

**Decision**: Render `cilium/cilium` (`v1.20.3`) with:
- `envoy.enabled=false`
- `tls.secretsNamespace.create=false`
- `tls.secretsNamespace.name=kube-system`
- `tls.secretSync.enabled=false`
- `hubble.tls.enabled=false`

**Rationale**:
- Constitution Principle V (and Amendment 2.2.0) strictly prohibits committing `Secret` objects or private keys to Git and restricts runtime TLS `Secret` objects to `platform` (`auth-tls`, `pn-tls`) and `cert-manager` (ACME account keys).
- By default, `helm template cilium` generates a `cilium-secrets` `Namespace` (which would violate [`scripts/render-invariants.py`](../../scripts/render-invariants.py) Invariant 8 unless registered as a managed namespace) and generates plaintext PEM private keys for Hubble mTLS (`hubble-server-certs`, `hubble-relay-client-certs`), which would fail `gitleaks` secret scanning and violate Principle V.
- On our single-node K3s architecture, `hubble-relay` connects to the node-local `cilium-agent` (`hubble-peer.kube-system.svc.cluster.local:4244`) inside `kube-system` over the loopback/cluster network. Disabling `hubble.tls.enabled` and standalone `cilium-envoy` eliminates all generated `Secret` and `Namespace` objects from `deploy/platform/cni/` and saves an entire `cilium-envoy` DaemonSet worth of RAM.

**Alternatives considered**:

| Alternative | Rejected because |
| :--- | :--- |
| `hubble.tls.auto.method=helm` | Commits PEM private keys into `deploy/platform/cni/upstream/cilium.yaml` (violates Principle V and fails `gitleaks`). |
| `hubble.tls.auto.method=certmanager` | Requires amending Principle V (Amendment 2.2.0) to permit TLS `Secret` objects in `kube-system` and creates a circular boot dependency between `cni` and `cert-manager`. |

---

## R4. OpenBao → Kubernetes API Server Egress & Backup Egress under Cilium (`FR-006`, `US2`)

**Decision**:
1. Remove the `ipBlock: { cidr: 0.0.0.0/0 }` egress rule from `openbao-ingress-policy` in [`deploy/platform/vault/network-policy.yaml`](../../deploy/platform/vault/network-policy.yaml).
2. Add a `CiliumNetworkPolicy` (`apiVersion: cilium.io/v2`, `kind: CiliumNetworkPolicy`) named `openbao-apiserver-egress` in `namespace: vault` selecting `app.kubernetes.io/name: openbao` and allowing egress exclusively to `toEntities: ["kube-apiserver"]` on TCP ports `443` and `6443`.
3. Add a standard `NetworkPolicy` (`openbao-backup-policy`) in `deploy/platform/vault/network-policy.yaml` allowing the `openbao-daily-snapshot` CronJob (`app.kubernetes.io/name: openbao-backup`) egress to `app.kubernetes.io/name: openbao` on TCP port `8200` (and Kubernetes API `toEntities: ["kube-apiserver"]` is not needed by backup pods because they send their SA JWT to OpenBao `:8200`, and OpenBao performs the `TokenReview`).
4. Keep all tenant project namespaces (`proj-*`) on 100% standard `networking.k8s.io/v1` `NetworkPolicy` manifests.

**Rationale**:
- Under Cilium's identity-based eBPF datapath, `ipBlock: 0.0.0.0/0` in a standard `NetworkPolicy` matches only Cilium Identity `2` (`reserved:world`, external internet) and excludes internal cluster entities (`reserved:host` Identity `1` and `reserved:kube-apiserver` Identity `7`). When `openbao` connects to `10.43.0.1:443` for Kubernetes ServiceAccount `TokenReview`, DNAT translates the destination to `<NODE_IP>:6443` (`reserved:kube-apiserver`), which `ipBlock: 0.0.0.0/0` drops!
- Furthermore, `ipBlock: 0.0.0.0/0` unintentionally allowed `openbao` to initiate outbound connections to any public internet IP on ports `443`/`6443`.
- Replacing that rule with `CiliumNetworkPolicy` `toEntities: ["kube-apiserver"]` simultaneously fixes `TokenReview` under Cilium AND blocks all external internet (`world`) egress from `openbao`.
- Datree's `CRDs-catalog` (used by `kubeconform` in [`scripts/render-validate.sh`](../../scripts/render-validate.sh)) already includes `cilium.io/ciliumnetworkpolicy_v2.json`, so `render-validate.sh` validates `openbao-apiserver-egress` with zero script changes.

**Alternatives considered**:

| Alternative | Rejected because |
| :--- | :--- |
| Set `--policy-cidr-match-mode=nodes` on Cilium and keep `ipBlock: 0.0.0.0/0` | Still leaves `openbao` open to the entire public internet (`0.0.0.0/0`) on ports `443` and `6443`. |
| Convert `proj-pn` policies to `CiliumNetworkPolicy` | Violates `FR-006` and Constitution Principle VI (project manifests must remain portable to EKS/GKE without requiring Cilium CRDs in tenant namespaces). |

---

## R5. FluxCD Layer DAG Ordering & Bootstrap Staging (`FR-002`, `FR-003`)

**Decision**:
1. Add a root Flux `Kustomization` layer named `cni` (`path: ./deploy/platform/cni`, `wait: true`, `prune: true`) to [`clusters/local-k3s/layers.yaml`](../../clusters/local-k3s/layers.yaml) and [`clusters/vps-k3s/layers.yaml`](../../clusters/vps-k3s/layers.yaml).
2. Make the `vault` layer in `layers.yaml` depend on both `governance` and `cni` (`dependsOn: [{name: governance}, {name: cni}]`).
3. In [`bootstrap/tasks/install.yml`](../../bootstrap/tasks/install.yml), after K3s starts and `/etc/rancher/k3s/k3s.yaml` is written, idempotently stage `deploy/platform/cni/` to `/var/lib/nacfson-bootstrap/cni/` and apply it via `k3s kubectl apply --server-side -k /var/lib/nacfson-bootstrap/cni/` (when `daemonset/cilium` is not yet present) before [`bootstrap/tasks/verify.yml`](../../bootstrap/tasks/verify.yml) checks `node_ready` and `coredns_ready`.

**Rationale**:
- Because `deploy/platform/vault/network-policy.yaml` now contains a `CiliumNetworkPolicy` (`cilium.io/v2`), the `ciliumnetworkpolicies.cilium.io` CRD (installed by `cilium-operator` in the `cni` layer) must be established before Flux applies the `vault` layer. Making `vault` depend on `cni` (with `wait: true` on `cni`) guarantees deterministic CRD ordering.
- Applying `deploy/platform/cni/` at the end of `bootstrap/tasks/install.yml` ensures that when K3s boots with `--flannel-backend=none`, Cilium initializes the node network so `bootstrap/tasks/verify.yml` passes `node_ready` and `coredns_ready` within the 300-second bound.
- [`deploy/platform/governance/manual-change-policy.yaml`](../../deploy/platform/governance/manual-change-policy.yaml) already permits `request.userInfo.username.startsWith('system:serviceaccount:kube-system:')` on both namespaced and cluster-scoped resources, allowing `cilium` and `cilium-operator` to manage `CiliumEndpoint`, `CiliumIdentity`, `CiliumNode`, and `CustomResourceDefinition` objects without admission denials.

**Alternatives considered**:

| Alternative | Rejected because |
| :--- | :--- |
| Apply `cni` only via FluxCD after `verify.yml` | `verify.yml` waits for `node_ready` and `coredns_ready` before `reconciler.yml` runs; without a CNI, the node stays `NotReady` and `verify.yml` times out after 300s. |
| Have `vault` depend only on `governance` | On a fresh Flux reconcile, `vault` could race ahead of `cilium-operator` creating the `CiliumNetworkPolicy` CRD. |

---

## R6. Resource Sizing & `platform-preflight.py` Reservation Accounting (`FR-009`, `SC-003`)

**Decision**: Declare explicit CPU and memory `requests` and `limits` on every container and `initContainer` in `deploy/platform/cni/`:

| Workload (`kube-system`) | Container / InitContainer | CPU Request | CPU Limit | Memory Request | Memory Limit |
| :--- | :--- | :--- | :--- | :--- | :--- |
| `DaemonSet/cilium` | 6 × `initContainers` (`config`, `mount-cgroup`, `apply-sysctl-overwrites`, `mount-bpf-fs`, `clean-cilium-state`, `install-cni-binaries`) | `10m` each (`60m` sum) | `100m` each | `16Mi` each | `64Mi` each (`384Mi` sum) |
| `DaemonSet/cilium` | `cilium-agent` | `100m` | `500m` | `192Mi` | `384Mi` |
| `Deployment/cilium-operator` | `cilium-operator` (1 replica) | `25m` | `200m` | `64Mi` | `192Mi` |
| `Deployment/hubble-relay` | `hubble-relay` (1 replica) | `15m` | `150m` | `48Mi` | `128Mi` |
| `Deployment/hubble-ui` | `frontend` (1 replica) | `10m` | `100m` | `32Mi` | `64Mi` |
| `Deployment/hubble-ui` | `backend` (1 replica) | `15m` | `100m` | `48Mi` | `96Mi` |
| **Total CNI + Hubble Footprint** | **All containers + initContainers** | **`225m`** | — | — | **`1248Mi`** |

**Rationale**:
- [`scripts/platform-preflight.py`](../../scripts/platform-preflight.py) (lines 641–660 and 702–719) requires every container and `initContainer` in all rendered layers to declare `requests` and `limits` (Rule 4) and adds `requests.cpu` and `limits.memory` of all containers + `initContainers` in `PLATFORM_NAMESPACES` (which already includes `kube-system`) to `platformReservations`.
- By default, Cilium's `install-cni-binaries` init container sets `limits.memory: 1Gi` and leaves the other 5 init containers unbounded. Sizing each init container at `10m`/`16Mi` request and `100m`/`64Mi` limit satisfies Rule 4 while keeping the total CNI + Hubble reservation to `+225m` CPU and `+1248Mi` RAM.
- Resulting budget math (`platformReservations` increases from `1695m` CPU / `4064Mi` RAM to `1920m` CPU / `5312Mi` RAM):
  - `local-k3s` (`4000m` CPU / `8192Mi` RAM): `calculatedPerProjectBudget` = `2080m` CPU / `2880Mi` RAM (vs. `proj-pn` peak `400m` CPU / `640Mi` RAM → `PASSED`).
  - `vps-k3s` (`4000m` CPU / `24576Mi` RAM): `calculatedPerProjectBudget` = `2080m` CPU / `19264Mi` RAM (vs. `proj-pn` peak `400m` CPU / `640Mi` RAM → `PASSED`).

**Alternatives considered**:

| Alternative | Rejected because |
| :--- | :--- |
| Keep upstream default `install-cni-binaries` `1Gi` limit | Wastes `1024Mi` of budget accounting in `platform-preflight.py` for a 2-second file-copy init container. |
| Modify `platform-preflight.py` to ignore `initContainers` in `kube-system` | Weakens Rule 4 and deviates from `specs/004-flux-gitops-reconciliation/contracts/platform-preflight.md`. |

---

## R7. Phased Observability: Hubble UI Alone Now + Prometheus/gRPC Expansion Later (`FR-010`–`FR-013`)

**Decision**:
- **Phase 1 (deployed now)**: Deploy `hubble-relay` (`ClusterIP` `:4245` -> `:4245`) and `hubble-ui` (`ClusterIP` `:80` -> `:8081`) in `kube-system` with zero public `IngressRoute`. Provide [`scripts/open-hubble-ui.sh`](../../scripts/open-hubble-ui.sh) to preflight-check pod readiness and port-forward `svc/hubble-ui` to `http://127.0.0.1:12000`.
- **Phase 2 hooks (enabled now, zero extra pods)**: Configure `cilium-agent` with `hubble-metrics-server: ":9965"` and metrics `{dns, drop, tcp, flow}` plus `prometheus.enabled: true` (`:9962`) and `operator.prometheus.enabled: true` (`:9963`), exposed via `Service/hubble-metrics` (`:9965` in `kube-system`).

**Rationale**:
- Deploying Hubble UI alone now delivers immediate visual service-map topology and live `FORWARDED` / `DROPPED` flow tables while consuming zero extra RAM for Prometheus, Loki, or Grafana pods.
- Pre-enabling `:9965/metrics` (`hubble_flows_processed_total`, `hubble_drop_total`, `hubble_dns_queries_total`, `hubble_tcp_flags_total`) and `hubble-relay:4245` gRPC means a future Grafana/Prometheus/Loki observability layer (`EXPAND-01`) can scrape and stream data immediately without modifying `deploy/platform/cni/` or restarting CNI pods.
