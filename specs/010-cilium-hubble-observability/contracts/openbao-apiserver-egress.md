# Contract: OpenBao API Server Egress & Vault NetworkPolicy under Cilium

**Feature**: [spec.md](../spec.md) (`FR-006`, `FR-007`, `US2`, `SC-003`) · **Decisions**: [research.md](../research.md) R4

---

## 1. Problem Addressed

In [`deploy/platform/vault/network-policy.yaml`](../../../deploy/platform/vault/network-policy.yaml), `openbao-ingress-policy` previously declared:

```yaml
    # Kubernetes API Server TokenReview egress (auth/kubernetes)
    - to:
        - ipBlock:
            cidr: 0.0.0.0/0
      ports:
        - protocol: TCP
          port: 443
        - protocol: TCP
          port: 6443
```

Under Cilium's eBPF identity datapath:
1. `ipBlock: 0.0.0.0/0` maps exclusively to `reserved:world` (Identity `2`, external internet) and **does not match** `reserved:kube-apiserver` (Identity `7`) or `reserved:host` (Identity `1`). After DNAT translates `10.43.0.1:443` to `<NODE_IP>:6443`, Cilium drops `openbao`'s `TokenReview` request with `POLICY_DENIED`.
2. `ipBlock: 0.0.0.0/0` unintentionally permitted `openbao` to open outbound connections to arbitrary public internet hosts on ports `443` and `6443`.

---

## 2. Required Policy Declarations (`deploy/platform/vault/network-policy.yaml`)

### 2.1 Remove `ipBlock: 0.0.0.0/0` from `openbao-ingress-policy`

`NetworkPolicy/openbao-ingress-policy` (`networking.k8s.io/v1`) retains only intra-vault Raft clustering egress on TCP port `8201`:

```yaml
  egress:
    # Intra-vault clustering & Raft sync
    - to:
        - podSelector:
            matchLabels:
              app.kubernetes.io/name: openbao
      ports:
        - protocol: TCP
          port: 8201
```

### 2.2 Add Entity-Scoped `CiliumNetworkPolicy/openbao-apiserver-egress`

Add the following `CiliumNetworkPolicy` in [`deploy/platform/vault/network-policy.yaml`](../../../deploy/platform/vault/network-policy.yaml):

```yaml
apiVersion: cilium.io/v2
kind: CiliumNetworkPolicy
metadata:
  name: openbao-apiserver-egress
  namespace: vault
  labels:
    app.kubernetes.io/name: openbao-policy
    app.kubernetes.io/part-of: platform-vault
spec:
  endpointSelector:
    matchLabels:
      app.kubernetes.io/name: openbao
  egress:
    - toEntities:
        - kube-apiserver
      toPorts:
        - ports:
            - port: "443"
              protocol: TCP
            - port: "6443"
              protocol: TCP
```

### 2.3 Add `NetworkPolicy/openbao-backup-policy` for Scheduled Snapshots

Because `vault-default-deny` denies all non-DNS egress in `namespace: vault`, explicitly allow the daily Raft snapshot CronJob (`app.kubernetes.io/name: openbao-backup`) to reach `openbao` on TCP port `8200`:

```yaml
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: openbao-backup-policy
  namespace: vault
  labels:
    app.kubernetes.io/name: openbao-backup-policy
    app.kubernetes.io/part-of: platform-vault
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: openbao-backup
  policyTypes:
    - Egress
  egress:
    - to:
        - podSelector:
            matchLabels:
              app.kubernetes.io/name: openbao
      ports:
        - protocol: TCP
          port: 8200
```

---

## 3. Tenant Project Portability Boundary (`proj-*`)

- Tenant project namespaces (`proj-pn` and future `proj-*` namespaces) MUST use standard Kubernetes `networking.k8s.io/v1` `NetworkPolicy` exclusively (`FR-006`, `SC-003`).
- `scripts/render-invariants.py` Invariant 6 continues to forbid `CiliumNetworkPolicy` in project workload layers (`ALLOWED_PROJECT_KINDS = {"Deployment", "Service", "ConfigMap", "IngressRoute"}`).
- `scripts/platform-preflight.py` `check_edge_tls` and `check_project_doors` continue to validate `proj-pn` standard `NetworkPolicy` rules unchanged (`FR-007`).
