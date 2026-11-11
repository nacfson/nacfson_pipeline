# CONTRACT_ISOLATION_BUDGET: Workload Isolation, Restricted PSS & $1/n$ Resource Budgeting

**Status**: Enforced (`platform-preflight.py`, `render-invariants.py`, `manual-change-policy.yaml`)  
**Governing Principles**: [Constitution Principles I, II, IV & Amendment 2.3.0](../.specify/memory/constitution.md)  
**Related Specs**: [`specs/001-project-platform/`](../specs/001-project-platform/), [`specs/004-flux-gitops-reconciliation/`](../specs/004-flux-gitops-reconciliation/), & [`specs/012-gke-platform-migration/`](../specs/012-gke-platform-migration/)

---

## 1. Namespace & Restricted Pod Security Contract (`proj-<slug>`)

Every project namespace (`deploy/projects/<slug>/boundary/`) and workload (`deploy/projects/<slug>/workloads/`) MUST enforce:

1. **Pod Security Admission**: Namespace labels MUST set `pod-security.kubernetes.io/enforce: restricted`.
2. **Non-Root & Capability Drop**:
   - `runAsNonRoot: true`, `runAsUser: 10001`, `runAsGroup: 10001`, `seccompProfile.type: RuntimeDefault`.
   - `allowPrivilegeEscalation: false`, `privileged: false`, `readOnlyRootFilesystem: true`.
   - `capabilities.drop: ["ALL"]` (adding capabilities other than `NET_BIND_SERVICE` is rejected).
3. **Zero K8s API Access**:
   - `automountServiceAccountToken: false` is mandatory on all project pods.
4. **Allowed Project Manifest Kinds & Services**:
   - Project layers may render only `Deployment`, `Service`, `ConfigMap`, and `IngressRoute` (enforced by Invariant 6 in [`scripts/render-invariants.py`](../scripts/render-invariants.py)).
   - All project `Service` objects MUST be `type: ClusterIP` (and on `gke`, `Service` `type: LoadBalancer` is rejected cluster-wide in favor of `Traefik` `hostPort: 80/443`). `NodePort`, `LoadBalancer`, `hostNetwork`, `hostPID`, `hostIPC`, and `hostPath` volumes are strictly rejected in project namespaces. Under Constitution Amendment `2.3.0`, only `DaemonSet/fluent-bit` in platform namespace `observability` may mount `/var/log/pods` (`readOnly: true`) and `/var/lib/fluent-bit-state` (`readOnly: false`, `DirectoryOrCreate`, capped at `256 MiB`/node).

---

## 2. Cloud-Agnostic `NetworkPolicy` Boundary (`deploy/projects/<slug>/boundary/network-policy.yaml`)

Zero CNI-specific `cilium.io/v2` `CiliumNetworkPolicy` manifests exist in `deploy/` or `clusters/{local-k3s,gke}/`. Each project namespace enforces standard `networking.k8s.io/v1` default-deny `Ingress` + `Egress` policies identically on `local-k3s` (Cilium with `policy-cidr-match-mode: "nodes"`) and `gke` (GKE Dataplane V2):
- **Ingress**:
  - From `platform` namespace (`Deployment/traefik` on GKE) and `kube-system` (`app.kubernetes.io/name: traefik` on K3s) on declared door ports (`80`, `8080`).
  - Intra-namespace from `<slug>-frontend` to `<slug>-backend` on port `8080`.
- **Egress**:
  - To `kube-system/kube-dns` on UDP/TCP port `53`.
  - To `vault/vault-db-proxy` on TCP port `5432` (**never** directly to `identity/postgres`).
  - To `vault/vault-ai-proxy` (`Service/ai-gateway.vault.svc`) on TCP port `8080` (**never** directly to `generativelanguage.googleapis.com` or admin port `8081`).
  - To `identity/gateway` on TCP port `8080` **only** when `signIn: shared` is declared in `sign-in.yaml`.
  - Intra-namespace from `<slug>-frontend` to `<slug>-backend` on TCP port `8080`.
- **Portable Public HTTPS (`:443`) Egress (`pn-backend`, `vault-ai-proxy`, `vault-notify-proxy`):**
  - `NetworkPolicy/pn-backend-translation-egress` (`deploy/projects/pn/boundary/translation-egress.yaml`), `NetworkPolicy/ai-proxy-policy`, and `NetworkPolicy/notify-proxy-policy` (`deploy/platform/vault/network-policy.yaml`) select only their exact single caller pod (`pn-backend`, `vault-ai-proxy`, `vault-notify-proxy`) and allow TCP `:443` to `ipBlock: {cidr: 0.0.0.0/0, except: [10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 169.254.169.254/32, 100.64.0.0/10]}`, blocking all private RFC1918 networks, CGNAT, and cloud instance metadata / Workload Identity (`169.254.169.254/32`). General `0.0.0.0/0` egress remains strictly forbidden on `vault/openbao`.

---

## 3. Deterministic Multi-Node $1/n$ Compute Budget & Guaranteed QoS

[`scripts/platform-preflight.py`](../scripts/platform-preflight.py) computes each project's resource slice from the environment's observed `capacity.yaml` ([`local-k3s`](../clusters/local-k3s/capacity.yaml), [`gke`](../clusters/gke/capacity.yaml), [`vps-k3s`](../clusters/vps-k3s/capacity.yaml)):

$$\text{Project Slice} = \frac{\text{Cluster Allocatable} - \text{Platform Reservations}}{n_{\text{projects}}}$$

1. **Multi-Node & Per-Pool Accounting (`clusters/gke/capacity.yaml` Design 1)**:
   - On 2-node GKE (`1 × e2-medium` `ondemand-core` `940m/2972Mi` + `1 × e2-small` `spot-controllers` `940m/1436Mi` = `1880m / 4408Mi` allocatable), `DaemonSet` workloads (`fluent-bit`) are multiplied by the number of tolerated nodes (`2×`), platform `Deployment`s reserve `max(replicas, 1)` slots, and `platformReservations = (970m, 3152Mi)` yields a $1/3$ per-project budget of **`(303m, 418Mi)`**.
   - Only PVC-free background controllers (`flux-system` ×2, `cert-manager` ×3) may schedule onto `spot-controllers`; `keycloak`, `gateway`, `traefik`, `postgres`, `openbao`, `proj-*`, and all `ReadWriteOnce` PVCs remain strictly on `ondemand-core` with `>= 50m` CPU and `>= 192 MiB` memory headroom reserved for midnight backup `CronJob`s.
2. **Guaranteed QoS (`requests == limits`)**: Every container and initContainer in `proj-<slug>` MUST set `requests.cpu == limits.cpu` and `requests.memory == limits.memory`.
3. **Peak Rollout Accounting**: A project's peak CPU and memory footprint (accounting for `replicas + maxSurge`, `StatefulSet` replicas, `Job` parallelism, and `CronJob` concurrency) MUST NOT exceed `Project Slice`.
4. **Interim Memory Freeze**: Candidate revisions within the same environment MUST NOT lower an existing project container's memory limit below that environment's baseline render.
