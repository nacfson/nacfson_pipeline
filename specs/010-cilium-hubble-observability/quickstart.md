# Quickstart & Validation Guide: Cilium + Hubble CNI & Visual Platform Network Observability

**Feature**: [spec.md](spec.md) · **Plan**: [plan.md](plan.md) · **Contracts**: [contracts/](contracts/)

This guide walks through offline CI validation, cluster bootstrap/reconciliation verification, visual network inspection in Hubble UI, policy drop verification, and Prometheus metrics validation.

---

## 1. Offline Render, Schema & Preflight Budget Gate (`FR-009`, `SC-004`)

Run the standard repository quality gates from the repo root before pushing:

```bash
# 1. Unit tests for platform-preflight (budget, QoS, isolation, door, TLS, CNI & Hubble rules)
python3 -m unittest discover tests/preflight

# 2. Full Flux/Kustomize render, kubeconform schema check (including CiliumNetworkPolicy), and render-invariants
CAND_LOCAL=$(./scripts/render-validate.sh local-k3s | tail -n 1)
CAND_VPS=$(./scripts/render-validate.sh vps-k3s | tail -n 1)

# 3. Platform preflight budget check on both environments
python3 ./scripts/platform-preflight.py --environment local-k3s --candidate "$CAND_LOCAL" --capacity clusters/local-k3s/capacity.yaml --json
python3 ./scripts/platform-preflight.py --environment vps-k3s --candidate "$CAND_VPS" --capacity clusters/vps-k3s/capacity.yaml --json

# 4. ShellCheck & Ansible syntax check
shellcheck scripts/open-hubble-ui.sh
ansible-playbook -i 'syntax-check-vm,' -u ubuntu -e 'k3s_version=v1.35.9+k3s1' -e 'gitops_environment=local-k3s' bootstrap/bootstrap.yml --syntax-check
```

**Expected outcome**:
- Both `render-validate.sh` invocations exit `0` (`kubeconform` validates `CiliumNetworkPolicy/openbao-apiserver-egress` and all `deploy/platform/cni/` resources; `render-invariants.py` passes all 9 invariants).
- `platform-preflight.py` reports `"validationStatus": "PASSED"` with `platformReservations` accounting for the CNI + Hubble containers (`1920m` CPU, `5312Mi` memory).

---

## 2. Automated Runtime Verification (`FR-013`, `SC-001`, `SC-002`, `SC-003`, `SC-005`)

On a bootstrapped `local-k3s` or `vps-k3s` cluster (where K3s runs with `--flannel-backend=none --disable-network-policy`):

```bash
# Run all 5 automated FR-013 checks (pod readiness, zero Grafana pods, OpenBao CiliumNetworkPolicy,
# flow verdict counters, :9965/metrics scrapability, and ClusterIP-only private exposure)
./scripts/open-hubble-ui.sh --verify
```

**Expected outcome**:
- `daemonset/cilium`, `deployment/cilium-operator`, `deployment/hubble-relay`, and `deployment/hubble-ui` are `Running` (`Ready`), with `0` Grafana/Prometheus/Loki pods deployed (`SC-002`).
- `CiliumNetworkPolicy/openbao-apiserver-egress` (`toEntities: ["kube-apiserver"]`) is active in `vault` alongside standard `NetworkPolicy` objects (`SC-003`).
- Hubble `:9965/metrics` responds with `hubble_flows_processed_total`, `hubble_drop_total`, `hubble_dns_queries_total`, and `hubble_tcp_flags_total` (`SC-002`).
- Zero public `IngressRoute` or `Ingress` resources expose Hubble endpoints (`SC-005`).

---

## 3. Visual Service Map & `DROPPED` Verdict Verification in Hubble UI (`SC-001`, `SC-005`, `SC-006`)

### 3.1 Open Hubble UI Locally (`US1`, `US5`)

```bash
./scripts/open-hubble-ui.sh
```

Open `http://127.0.0.1:12000` in your browser and select the `proj-pn` namespace:
- Observe the live service dependency graph (`kube-system/traefik` → `proj-pn/pn-frontend:80` → `proj-pn/pn-backend:8080` → `identity/gateway:8080` and `vault/vault-db-proxy:5432`).
- Confirm legitimate flows appear in the bottom table with verdict **`FORWARDED`**.

### 3.2 Observe Policy Drops & Control-Plane Fault Isolation (`US1`, `US2`, `SC-006`)

- Filter by Verdict: **`Dropped`** in Hubble UI to inspect any connection blocked by `NetworkPolicy` or `CiliumNetworkPolicy` within 5 seconds of occurrence (`Policy denied`, showing source workload, destination IP/port, and direction).
- Restart `deployment/hubble-ui` or `deployment/hubble-relay` (`kubectl -n kube-system rollout restart deploy/hubble-ui deploy/hubble-relay`) while querying `https://pn.nacfson.cloud/api/healthz` and confirm `0` dropped packets on live application traffic (`SC-006`).
