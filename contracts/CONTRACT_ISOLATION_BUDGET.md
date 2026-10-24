# CONTRACT_ISOLATION_BUDGET: Workload Isolation, Restricted PSS & $1/n$ Resource Budgeting

**Status**: Enforced (`platform-preflight.py`, `render-invariants.py`, `manual-change-policy.yaml`)  
**Governing Principles**: [Constitution Principles I, II, IV](../.specify/memory/constitution.md#L6-L18)  
**Related Specs**: [`specs/001-project-platform/`](../specs/001-project-platform/) & [`specs/004-flux-gitops-reconciliation/`](../specs/004-flux-gitops-reconciliation/)

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
   - All project `Service` objects MUST be `type: ClusterIP`. `NodePort`, `LoadBalancer`, `hostNetwork`, `hostPID`, `hostIPC`, and `hostPath` volumes are strictly rejected in project namespaces. The platform `observability` collector is the documented exception: one read-only `hostPath` of `/var/log/pods` in that privileged platform namespace. The observability reservation is not yet measured and must not be added by hand.

---

## 2. NetworkPolicy Boundary (`deploy/projects/<slug>/boundary/network-policy.yaml`)

Each project namespace enforces a default-deny `Ingress` + `Egress` policy with only these permitted paths:
- **Ingress**:
  - From `platform` namespace and `kube-system` (`app.kubernetes.io/name: traefik`) on declared door ports (`80`, `8080`).
  - Intra-namespace from `<slug>-frontend` to `<slug>-backend` on port `8080`.
- **Egress**:
  - To `kube-system/kube-dns` on UDP/TCP port `53`.
  - To `vault/vault-db-proxy` on TCP port `5432` (**never** directly to `identity/postgres`).
  - To `identity/gateway` on TCP port `8080` **only** when `signIn: shared` is declared in `sign-in.yaml`.
  - Intra-namespace from `<slug>-frontend` to `<slug>-backend` on TCP port `8080`.
- **Translation egress (Project PN backend only):** not a rule in the portable `NetworkPolicy`. `CiliumNetworkPolicy/pn-backend-translation-egress` (`deploy/projects/pn/boundary/translation-egress.yaml`) selects only `app.kubernetes.io/name: pn-backend` and allows TCP 443 to exactly `api.wiktapi.dev` and `generativelanguage.googleapis.com`. DNS to kube-dns on port 53 uses `matchPattern: "*"` so in-cluster names still resolve. No other public name, no `0.0.0.0/0`, and no kube-router world fallback. Enforced only while Cilium owns the pod.

---

## 3. Deterministic $1/n$ Compute Budget & Guaranteed QoS

[`scripts/platform-preflight.py`](../scripts/platform-preflight.py) computes each project's resource slice from the node's observed [`capacity.yaml`](../clusters/vps-k3s/capacity.yaml):

$$\text{Project Slice} = \frac{\text{Node Allocatable} - \text{System Reserved} - \text{Platform Reservation}}{n_{\text{projects}}}$$

1. **Guaranteed QoS (`requests == limits`)**: Every container and initContainer in `proj-<slug>` MUST set `requests.cpu == limits.cpu` and `requests.memory == limits.memory`.
2. **Peak Rollout Accounting**: A project's peak CPU and memory footprint (accounting for `replicas + maxSurge`, `StatefulSet` replicas, `Job` parallelism, and `CronJob` concurrency) MUST NOT exceed `Project Slice`.
3. **Interim Memory Freeze**: Candidate revisions MUST NOT lower an existing project container's memory limit below the baseline render.
