# Implementation Plan: Cilium + Hubble CNI & Visual Platform Network Observability

**Branch**: `010-cilium-hubble-observability` | **Date**: 2026-10-16 | **Spec**: [spec.md](spec.md)

**Input**: Feature specification from [`specs/010-cilium-hubble-observability/spec.md`](spec.md)

**HTML version**: [agy_plan/html/010-cilium-hubble-observability.html](../../agy_plan/html/010-cilium-hubble-observability.html)

---

## Summary

Replace K3s's default Flannel CNI and `kube-router` network policy controller (`--flannel-backend=none --disable-network-policy`) with a declarative, Git-owned **Cilium `v1.20.3` + Hubble Relay + Hubble UI (`v0.13.6`)** CNI layer ([`deploy/platform/cni/`](../../deploy/platform/cni/)).

The implementation follows a phased observability strategy:
- **Phase 1 (this feature — `FR-003`, `FR-004`, `FR-008`, `FR-010`, `FR-013`)**: Deploy **Cilium + Hubble Relay + Hubble UI alone** (`0` Grafana, Prometheus, or Loki pods), providing a live service-dependency map and real-time `Forwarded` / `Dropped` flow verdict tables (`hubble-event-buffer-capacity: "4095"`) accessed locally via [`scripts/open-hubble-ui.sh`](../../scripts/open-hubble-ui.sh) (`http://127.0.0.1:12000`) and verified automatically via `./scripts/open-hubble-ui.sh --verify`.
- **Phase 2 (future expansion `EXPAND-01`, pre-wired now — `FR-005`, `SC-002`)**: Enable Hubble's internal OpenMetrics endpoint (`:9965/metrics` with `dns`, `drop`, `tcp`, `flow` metrics) and Hubble Relay gRPC stream (`:4245`) so a future Grafana + Prometheus/Loki layer can attach without modifying CNI or tenant manifests.
- **Entity-Scoped Vault Egress (`FR-006`, `SC-003`)**: Replace OpenBao's broad `ipBlock: 0.0.0.0/0` egress workaround in [`deploy/platform/vault/network-policy.yaml`](../../deploy/platform/vault/network-policy.yaml) with a `CiliumNetworkPolicy` targeting `toEntities: ["kube-apiserver"]` on ports `443` and `6443` (blocking external internet egress from OpenBao) and add explicit backup-to-OpenBao `:8200` egress while keeping all tenant `proj-*` namespaces on 100% standard `networking.k8s.io/v1` `NetworkPolicy` manifests.

---

## Technical Context

**Language/Version**: YAML manifests (Kustomize via `kubectl kustomize`, Flux `kustomize.toolkit.fluxcd.io/v1`); Ansible `ansible-core==2.21.4` (`bootstrap/`); Python `3.12` (`scripts/platform-preflight.py`, `scripts/render-invariants.py`); Bash (`scripts/open-hubble-ui.sh`, `scripts/render-validate.sh`)

**Primary Dependencies**:
- Cilium `v1.20.3` (`quay.io/cilium/cilium:v1.20.3@sha256:95cfa5c2427cc6b452c00908778d0566311f572589284af949d06a013db1a849`, `quay.io/cilium/operator-generic:v1.20.3@sha256:efdd8d89d6d8f63d21a065fa2f9e17614a2b3ac9371b221af9355f63829e2dbc`)
- Hubble Relay `v1.20.3` (`quay.io/cilium/hubble-relay:v1.20.3@sha256:c6f819d9c5e0b35ccac63b9f2f1ae213fd9be50588e15d5a444d5d305ad317b5`)
- Hubble UI `v0.13.6` (`quay.io/cilium/hubble-ui:v0.13.6@sha256:049a80a03585c043d0c3121bdc518c72b0fd42bae07e4bcce0fdb8f1e88041d0`, `quay.io/cilium/hubble-ui-backend:v0.13.6@sha256:83b3fc763f3d49948c306bf49b08277383e195904b9ca5c0c0022b2112628787`)
- K3s `v1.35.9+k3s1`, FluxCD `2.9.5`

**Storage**: N/A (bounded in-memory Hubble ring buffer `hubble-event-buffer-capacity: "4095"` inside `cilium-agent`; zero persistent volumes added — `FR-008`)

**Testing**:
- `./scripts/render-validate.sh local-k3s` & `./scripts/render-validate.sh vps-k3s` (`flux build`, `kubeconform` with Datree `cilium.io/v2` CRD schema, `render-invariants.py`)
- `python3 -m unittest discover tests/preflight` & `scripts/platform-preflight.py` (`SC-004`)
- `shellcheck scripts/open-hubble-ui.sh`, `./scripts/open-hubble-ui.sh --verify` (`FR-013`), & `ansible-playbook ... bootstrap/bootstrap.yml --syntax-check`

**Target Platform**: Single-node K3s on Oracle Cloud ARM64 VPS (`vps-k3s`, `4000m` CPU / `24576Mi` RAM) and local Linux K3s (`local-k3s`, `4000m` CPU / `8192Mi` RAM)

**Project Type**: Declarative GitOps multi-tenant Kubernetes platform infrastructure

**Performance Goals**:
- CNI + Hubble pods reach `Ready` within bounded 300s bootstrap timeouts (`FR-002`).
- Hubble UI displays `Forwarded` and `Dropped` flow verdicts within 5 seconds of traffic generation (`SC-001`).

**Constraints**:
- Zero Grafana, Prometheus, or Loki pods deployed in Phase 1 (`FR-003`, `SC-002`).
- Zero `Secret` or extra `Namespace` objects in `deploy/platform/cni/` (Constitution Principle V).
- Zero public `IngressRoute` exposure for `hubble-ui`, `hubble-relay`, or `hubble-metrics` (`FR-010`, `SC-005`).
- All containers and `initContainers` in `deploy/platform/cni/` declare explicit CPU/memory `requests` and `limits` counted in `platformReservations` (`+225m` CPU, `+1248Mi` memory — `FR-009`, `SC-004`).
- All tenant `proj-*` namespaces remain on standard `networking.k8s.io/v1` `NetworkPolicy` (`FR-006`, `SC-003`).

**Scale/Scope**: 1 CNI layer (`deploy/platform/cni/`), 2 environment layer graphs (`local-k3s`, `vps-k3s`), 1 Vault `CiliumNetworkPolicy` + 1 backup `NetworkPolicy`, 4 bootstrap playbook/task files, 1 operator tunnel & verification script (`scripts/open-hubble-ui.sh`).

All technical decisions are documented in [research.md](research.md). Zero `NEEDS CLARIFICATION` markers remain.

---

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

Checked against [`.specify/memory/constitution.md`](../../.specify/memory/constitution.md) (**v2.2.0**):

| Principle | Assessment | Result |
| :--- | :--- | :--- |
| **I. Declarative GitOps** | `deploy/platform/cni/` is committed in Git, staged idempotently during initial VM bootstrap so K3s + CoreDNS reach `Ready` (`--flannel-backend=none`), and continuously reconciled by FluxCD (`cni` layer in `clusters/*/layers.yaml`). All Cilium (`v1.20.3`) and Hubble UI (`v0.13.6`) images are pinned by exact version tag and `@sha256:...` digest. | **PASS** |
| **II. Strict Workload Isolation & Restricted PSS** | Project namespaces (`proj-*`) retain `Restricted` PSS, `ClusterIP`-only services, and standard `NetworkPolicy`. Cilium eBPF enforces all ingress/egress policies and replaces OpenBao's broad `0.0.0.0/0` egress workaround with `toEntities: ["kube-apiserver"]`. | **PASS (improves)** |
| **III. Defense-in-Depth Identity & Public Surface** | `hubble-ui` and `hubble-relay` remain `ClusterIP`-only in `kube-system` with zero public ingress routes. Local operator access uses `scripts/open-hubble-ui.sh` (`127.0.0.1:12000`). | **PASS** |
| **IV. Bounded Resource Allocation & $1/n$ Budgeting** | Every container and `initContainer` in `deploy/platform/cni/` declares explicit `requests` and `limits`. `scripts/platform-preflight.py` counts all `kube-system` containers in `platformReservations` (`1695m → 1920m` CPU, `4064Mi → 5312Mi` memory), leaving `2080m` CPU / `2880Mi` RAM on `local-k3s` and `2080m` CPU / `19264Mi` RAM on `vps-k3s`. | **PASS** |
| **V. Zero Plaintext Secrets & Edge TLS Exception** | Cilium is rendered with `hubble.tls.enabled=false`, `envoy.enabled=false`, and `tls.secretsNamespace.create=false`, producing **zero** `Secret` objects in Git or in `kube-system`. | **PASS** |
| **VI. Environmental Portability** | Retaining `kubeProxyReplacement=false` allows `deploy/platform/cni/` to use standard in-cluster service routing (`10.43.0.1:443`) identically across `local-k3s` and `vps-k3s`. Project namespaces use only standard `networking.k8s.io/v1` `NetworkPolicy` for EKS/GKE portability. | **PASS** |
| **VII. Distinct Lifecycle Signals & Browser CORS** | Project PN's startup/readiness/liveness probes and CORS allowlist remain intact and are visually observable in Hubble UI. | **PASS** |

**Gate Decision**: **PASSED** (0 violations, 0 amendments required).
**Post-Design Re-Check (after Phase 1)**: `data-model.md`, `contracts/*`, and `quickstart.md` introduce zero constitutional violations.

---

## Project Structure

### Documentation (this feature)

```text
specs/010-cilium-hubble-observability/
├── spec.md                                     # Feature specification (clarified)
├── plan.md                                     # This file (/speckit-plan output)
├── research.md                                 # Phase 0 architectural decisions (R1–R7)
├── data-model.md                               # Phase 1 entity schemas & state transitions
├── quickstart.md                               # Phase 1 end-to-end validation guide
├── contracts/                                  # Phase 1 interface & policy contracts
│   ├── cni-layer-and-bootstrap.md
│   ├── openbao-apiserver-egress.md
│   └── hubble-observability-and-tunnel.md
├── checklists/
│   └── requirements.md                         # 16/16 passing spec quality checklist
└── tasks.md                                    # Phase 2 output (/speckit-tasks)
```

### Source Code & Manifests (repository root)

```text
deploy/platform/cni/
├── kustomization.yaml                          # NEW: Kustomize entrypoint + resource bounds patches
└── upstream/
    └── cilium.yaml                             # NEW: Rendered Cilium v1.20.3 + Hubble Relay + Hubble UI v0.13.6

deploy/platform/vault/
└── network-policy.yaml                         # MODIFIED: Replace OpenBao 0.0.0.0/0 egress with CiliumNetworkPolicy
                                                #           toEntities: ["kube-apiserver"] + add openbao-backup-policy

clusters/local-k3s/
└── layers.yaml                                 # MODIFIED: Add `cni` Kustomization; add `cni` to `vault.spec.dependsOn`

clusters/vps-k3s/
└── layers.yaml                                 # MODIFIED: Add `cni` Kustomization; add `cni` to `vault.spec.dependsOn`

bootstrap/
├── bootstrap.yml                               # MODIFIED: Clean up stale flannel.1/cni0/CNI dirs in Stage 0
├── templates/k3s.service.j2                    # MODIFIED: Add `--flannel-backend=none --disable-network-policy`
└── tasks/
    ├── preflight.yml                           # MODIFIED: Assert controller `deploy/platform/cni` directory exists
    ├── install.yml                             # MODIFIED: Stage & apply `deploy/platform/cni` after K3s start
    └── verify.yml                              # MODIFIED: Verify `daemonset/cilium` & `deployment/cilium-operator` rollout

scripts/
└── open-hubble-ui.sh                           # NEW: Operator helper to tunnel Hubble UI (:12000) & run --verify (FR-013)

tests/preflight/
└── test_platform_preflight.py                  # MODIFIED: Add unit test verifying CNI reservation, Vault CNP & Hubble privacy

.github/workflows/
└── lint.yaml                                   # MODIFIED: Add `deploy/platform/cni/upstream/` to yamllint upstream ignore list

docs/
└── bootstrap-scope.md                          # MODIFIED: Document Cilium CNI bootstrap staging & K3s flags (FR-012)

SPEC.md                                         # MODIFIED: Reflect Cilium + Hubble UI alone now & Grafana expansion (FR-012)
ARCHITECTURE.md                                 # MODIFIED: Update Flux layer DAG & CNI/Hubble observability section (FR-012)
DEPENDENCIES.md                                 # MODIFIED: Record pinned Cilium v1.20.3 & Hubble UI v0.13.6 versions (FR-012)
INDEX.md                                        # MODIFIED: Register spec 010, `deploy/platform/cni/`, and `scripts/open-hubble-ui.sh`
```

**Structure Decision**: Follow the existing platform layer pattern established by [`deploy/platform/cert-manager/`](../../deploy/platform/cert-manager/): vendor the deterministic upstream render in `deploy/platform/cni/upstream/cilium.yaml`, enforce container resource requests/limits in `deploy/platform/cni/kustomization.yaml`, wire `cni` as a root Flux `Kustomization` in `clusters/*/layers.yaml` (before `vault`), and apply `deploy/platform/cni/` during `bootstrap/tasks/install.yml` so K3s nodes reach `Ready` with `--flannel-backend=none`.

---

## Key Diffs

### 1. K3s Systemd Unit (`bootstrap/templates/k3s.service.j2`)

```diff
 ExecStartPre=/sbin/modprobe overlay
 ExecStartPre=/sbin/modprobe br_netfilter
-ExecStart=/usr/local/bin/k3s server
+ExecStart=/usr/local/bin/k3s server --flannel-backend=none --disable-network-policy
```

### 2. Flux Layer DAG (`clusters/local-k3s/layers.yaml` & `clusters/vps-k3s/layers.yaml`)

```diff
+apiVersion: kustomize.toolkit.fluxcd.io/v1
+kind: Kustomization
+metadata:
+  name: cni
+  namespace: flux-system
+spec:
+  interval: 5m
+  retryInterval: 1m
+  timeout: 5m
+  prune: true
+  wait: true
+  sourceRef:
+    kind: GitRepository
+    name: flux-system
+  path: ./deploy/platform/cni
+---
 apiVersion: kustomize.toolkit.fluxcd.io/v1
 kind: Kustomization
 metadata:
   name: vault
   namespace: flux-system
 spec:
   ...
   dependsOn:
     - name: governance
+    - name: cni
```

### 3. OpenBao API Server Egress (`deploy/platform/vault/network-policy.yaml`)

```diff
   egress:
     # Intra-vault clustering & Raft sync
     - to:
         - podSelector:
             matchLabels:
               app.kubernetes.io/name: openbao
       ports:
         - protocol: TCP
           port: 8201
-    # Kubernetes API Server TokenReview egress (auth/kubernetes)
-    - to:
-        - ipBlock:
-            cidr: 0.0.0.0/0
-      ports:
-        - protocol: TCP
-          port: 443
-        - protocol: TCP
-          port: 6443
+---
+apiVersion: cilium.io/v2
+kind: CiliumNetworkPolicy
+metadata:
+  name: openbao-apiserver-egress
+  namespace: vault
+  labels:
+    app.kubernetes.io/name: openbao-policy
+    app.kubernetes.io/part-of: platform-vault
+spec:
+  endpointSelector:
+    matchLabels:
+      app.kubernetes.io/name: openbao
+  egress:
+    - toEntities:
+        - kube-apiserver
+      toPorts:
+        - ports:
+            - port: "443"
+              protocol: TCP
+            - port: "6443"
+              protocol: TCP
```

---

## Complexity Tracking

> No Constitution Check violations exist; table intentionally empty.

| Violation | Why Needed | Simpler Alternative Rejected Because |
| :--- | :--- | :--- |
| *None* | — | — |
