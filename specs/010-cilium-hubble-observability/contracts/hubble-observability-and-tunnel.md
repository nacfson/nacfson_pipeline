# Contract: Hubble Observability, Local Operator Tunnel & Automated Verification

**Feature**: [spec.md](../spec.md) (`FR-003`, `FR-004`, `FR-005`, `FR-008`, `FR-010`, `FR-011`, `FR-013`, `US1`, `US4`, `US5`, `SC-001`, `SC-002`, `SC-005`, `SC-006`) · **Decisions**: [research.md](../research.md) R7

---

## 1. Private Cluster Services & Ring Buffer (`kube-system`)

All Hubble services in [`deploy/platform/cni/`](../../../deploy/platform/cni/) MUST be `type: ClusterIP` (or headless `ClusterIP: None`) in `namespace: kube-system`:

| Service Name | Type | Service Port → Target Port | Purpose | Public Ingress |
| :--- | :--- | :--- | :--- | :--- |
| `hubble-peer` | `ClusterIP` | `4244/TCP` → `4244` | Node-local Cilium agent Hubble gRPC peer | **Forbidden** |
| `hubble-relay` | `ClusterIP` | `4245/TCP` → `4245` | Cluster-wide aggregated Hubble gRPC flow stream (`FR-005`) | **Forbidden** |
| `hubble-ui` | `ClusterIP` | `80/TCP` → `8081` | Hubble UI web application (`FR-003`, `FR-010`) | **Forbidden** |
| `hubble-metrics` | `ClusterIP` (`None`) | `9965/TCP` → `9965` | Prometheus-compatible OpenMetrics endpoint (`FR-005`) | **Forbidden** |

### Bounded In-Memory Ring Buffer (`FR-008`)
- `ConfigMap/cilium-config` MUST set `hubble-event-buffer-capacity: "4095"` so flow storage remains bounded in RAM without writing unbounded flow logs to host disk.

### Security Invariant (`FR-010`, `SC-005`)
- No `IngressRoute`, `Ingress`, `NodePort`, or `LoadBalancer` may expose `hubble-ui`, `hubble-relay`, or `hubble-metrics`.

---

## 2. Operator Local Tunnel & Automated Verification Script (`scripts/open-hubble-ui.sh`)

### Synopsis

```bash
# Mode 1: Interactive operator browser tunnel (default)
./scripts/open-hubble-ui.sh [--port <local-port>]

# Mode 2: Non-interactive automated verification (FR-013)
./scripts/open-hubble-ui.sh --verify
```

### Behavioral Contract

#### Mode 1: Operator Browser Tunnel (`./scripts/open-hubble-ui.sh [--port <local-port>]`)
1. Binds exclusively to `127.0.0.1:${PORT:-12000}` (never `0.0.0.0`).
2. Verifies `daemonset/cilium`, `deployment/hubble-relay`, and `deployment/hubble-ui` in `kube-system` have ready replicas.
3. Prints `Hubble UI available at: http://127.0.0.1:12000` and executes `kubectl -n kube-system port-forward svc/hubble-ui "${PORT}:80" --address 127.0.0.1`.

#### Mode 2: Automated Verification (`./scripts/open-hubble-ui.sh --verify` — `FR-013`)
Executes all 5 `FR-013` verification assertions and exits `0` on success (or `1` on any failure) without starting a blocking tunnel:
1. **Cilium & Hubble Health (`SC-001`, `SC-002`)**: Verifies `daemonset/cilium`, `deployment/cilium-operator`, `deployment/hubble-relay`, and `deployment/hubble-ui` are `Ready` in `kube-system`, and confirms zero `grafana`, `prometheus`, or `loki` pods exist.
2. **Policy Enforcement & OpenBao Egress (`FR-006`, `SC-003`)**: Verifies `CiliumNetworkPolicy/openbao-apiserver-egress` (`toEntities: ["kube-apiserver"]`) is active in `vault` alongside standard `NetworkPolicy` resources in `vault` and `proj-pn`.
3. **Flow Verdict Capture (`FR-004`, `SC-001`)**: Queries the Cilium/Hubble observer (`cilium-dbg` / `hubble` / metrics counters in `daemonset/cilium`) to confirm `FORWARDED` and `DROPPED` verdict counters/events are active.
4. **Future-Grafana Metrics Scrapability (`FR-005`, `SC-002`)**: Scrapes `:9965/metrics` inside `kube-system` and asserts HTTP `200` with `hubble_flows_processed_total`, `hubble_drop_total`, `hubble_dns_queries_total`, and `hubble_tcp_flags_total`.
5. **Private-Only Access & Public Ingress Rejection (`FR-010`, `SC-005`)**: Asserts `svc/hubble-ui`, `svc/hubble-relay`, and `svc/hubble-metrics` are `ClusterIP` only and that zero `IngressRoute` or `Ingress` objects across all namespaces reference Hubble services.

### Linting
- Must pass `shellcheck` (`v0.11.0`) in `.github/workflows/lint.yaml`.

---

## 3. Future Grafana & Prometheus Expansion Contract (`EXPAND-01`, `FR-005`, `SC-002`)

To guarantee that a future Grafana + Prometheus/Loki observability layer can attach without modifying `deploy/platform/cni/` or any tenant project:

1. **Prometheus Metrics Endpoint (`:9965/metrics`)**:
   - Enabled via `hubble-metrics-server: ":9965"` and `hubble-metrics: "dns drop tcp flow"` in `ConfigMap/cilium-config`.
   - Scraping `http://hubble-metrics.kube-system.svc.cluster.local:9965/metrics` returns HTTP `200` with metric families:
     - `hubble_flows_processed_total`
     - `hubble_drop_total`
     - `hubble_dns_queries_total`
     - `hubble_tcp_flags_total`
2. **Cilium Agent & Operator Metrics (`:9962/metrics`, `:9963/metrics`)**:
   - Enabled via `prometheus.enabled=true` and `operator.prometheus.enabled=true`.
3. **Hubble Relay gRPC Stream (`:4245`)**:
   - `hubble-relay.kube-system.svc.cluster.local:4245` exposes the standard `observer.Observer` gRPC service for future flow collectors or `hubble observe` CLI inspection.
