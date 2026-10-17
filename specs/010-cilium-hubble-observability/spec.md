# Feature Specification: Cilium + Hubble CNI & Visual Platform Network Observability

**Feature Branch**: `010-cilium-hubble-observability`

**Created**: 2026-10-16

**Status**: Ready for Planning

**Input**: User description: "Ok, let's specify for this ideas. Change to Cilium + Hubble and moreover making observable platform to debug it. Our first design, we have to use Hubble UI Alone but we would expand to Grafna for future understand? You have to specify this content into our spec more explicitly."

## Clarifications

### Session 2026-10-16

- Q: Should the initial observability design deploy Hubble UI alone now and reserve Grafana (with Prometheus/Loki) as a future expansion path? → A: Yes — the initial deployment MUST use **Hubble UI alone** (keeping memory overhead minimal on the single-node cluster without deploying Grafana, Prometheus, or Loki now), while structuring Cilium and Hubble metrics/flow export interfaces so that **Grafana** can be connected in a future expansion without re-architecting the CNI or Hubble layer.
- Q: How should the Cilium and Hubble UI manifests be owned and budgeted across host bootstrap and continuous GitOps reconciliation? → A: Option A — Single Git-owned manifest bundle (`deploy/platform/cni/`) with explicit CPU/memory requests and limits, applied during Ansible bootstrap so the node reaches Ready, and continuously reconciled and budgeted by FluxCD and `platform-preflight.py`.
- Q: How should we allow OpenBao to keep reaching the K3s API Server under Cilium? → A: Option B — Replace OpenBao's broad `ipBlock: 0.0.0.0/0` egress workaround in `deploy/platform/vault/` with a strict `CiliumNetworkPolicy` targeting `toEntities: ["kube-apiserver"]` on ports `443`/`6443` (blocking external internet egress from OpenBao), while keeping all tenant project namespaces (`proj-*`) on standard Kubernetes `NetworkPolicy` manifests.
- Q: How should the operator open Hubble UI in a browser to inspect network flows while keeping the dashboard off the public internet? → A: Option A — Keep `hubble-ui` as a private `ClusterIP` service and provide a one-command helper script (`scripts/open-hubble-ui.sh`) that opens a local tunnel to `http://127.0.0.1:12000` (zero public ingress exposure).

## Context

Today, the cluster relies on the host distribution's default network plugin and packet filter. When an internal request fails between a project frontend, project backend, the authentication gateway, Vault proxies, or the shared database, the operator must manually run command-line log and cluster inspection commands to guess where traffic stopped. Worse, when a default-deny network policy blocks an unpermitted connection, the packet is dropped silently with no visual record of which policy rejected it.

This feature replaces the default cluster network layer with **Cilium and Hubble** and adopts a **phased observability architecture**:
1. **Initial Design (Current Scope — Hubble UI Alone)**: Deploy Cilium, Hubble Relay, and **Hubble UI** as the standalone visual browser interface for inspecting live service dependency maps and real-time `Forwarded` vs. `Dropped / Policy Denied` network flows, without deploying heavy external monitoring backends (Grafana, Prometheus, or Loki) in the initial footprint.
2. **Future Expansion Path (Grafana Integration Readiness)**: Configure Hubble's flow and metrics interfaces cleanly so that a future **Grafana** observability stack (Grafana dashboards, Prometheus metrics scraping, and Loki historical flow + container log correlation) can attach directly to Hubble without replacing or redesigning the CNI or tenant workloads.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Visual diagnosis of internal network flows and policy drops via Hubble UI alone (Priority: P1)

When debugging a project or platform network issue in the initial deployment, the operator opens **Hubble UI** in a browser. The operator selects a namespace (such as `proj-pn`, `identity`, or `vault`) and immediately sees a live dependency map of communicating workloads alongside a real-time table of network flows showing source workload, destination workload, port, DNS resolution, and policy verdict (`Forwarded` vs. `Dropped / Policy Denied`)—using Hubble UI alone without running command-line network debugging commands or requiring a separate Grafana stack.

**Why this priority**: Eliminating silent network policy drops with minimal memory overhead on the single-node VPS is the immediate operational goal.

**Independent Test**: Open Hubble UI in a browser, trigger both a permitted request (for example, Project PN frontend to backend) and a blocked request (for example, a pod attempting an unpermitted egress port), and confirm both flows appear visually on the Hubble UI service map and flow stream with their exact allow/deny verdicts.

**Acceptance Scenarios**:

1. **Given** running platform and project workloads with Cilium and Hubble UI deployed alone, **When** the operator opens Hubble UI and selects a namespace, **Then** Hubble UI displays a live visual map of active workload-to-workload connections and a filterable list of recent network flows.
2. **Given** a workload attempting a network connection that is blocked by a namespace network policy, **When** the connection is rejected, **Then** Hubble UI highlights the dropped flow and identifies the source workload, destination address/port, traffic direction (ingress or egress), and `Policy Denied` verdict within 5 seconds.
3. **Given** an operator investigating internal DNS or service-to-service connectivity, **When** the operator filters by verdict (`Dropped` or `Forwarded`) or by workload name in Hubble UI, **Then** matching flows are isolated visually without running any command-line log inspection commands.

---

### User Story 2 - Seamless enforcement of existing portable network policies and tightened Vault API egress (Priority: P1)

All existing platform and project network isolation rules (default-deny ingress/egress and explicit allowlists) and public HTTPS routing continue to function identically after switching the network layer to Cilium, without requiring tenant projects to rewrite their standard network policies into vendor-specific definitions, while tightening OpenBao's Kubernetes API Server egress in `vault` from `0.0.0.0/0` to strictly `toEntities: ["kube-apiserver"]`.

**Why this priority**: Network observability must strengthen—never weaken—workload isolation (Constitution Principle II) or break environmental portability for hosted projects (Constitution Principle VI).

**Independent Test**: Run the platform isolation verification suite and end-to-end project traffic checks after switching to Cilium, confirming that OpenBao reaches the Kubernetes API Server for `TokenReview` while external internet egress from OpenBao is denied, all permitted project paths succeed, and zero tenant project manifests require vendor-specific policy objects.

**Acceptance Scenarios**:

1. **Given** existing standard network policies in `platform`, `identity`, `vault`, and `proj-pn` plus the `CiliumNetworkPolicy` granting OpenBao egress only to `toEntities: ["kube-apiserver"]`, **When** Cilium is active, **Then** OpenBao authenticates Kubernetes ServiceAccount tokens against the cluster API server while external internet egress from OpenBao is blocked, every allowed service hop succeeds, and every unpermitted cross-namespace hop is blocked.
2. **Given** public HTTPS requests arriving at the platform ingress entry point for published hostnames, **When** traffic is routed to project and authentication services, **Then** TLS termination and request routing succeed with no change to project routes.
3. **Given** a project's declared manifests, **When** validated for multi-environment portability, **Then** the project continues to use only standard `NetworkPolicy` definitions rather than vendor-specific custom resources.

---

### User Story 3 - Deterministic host bootstrap and 1/n resource budget compliance (Priority: P2)

When bootstrapping a fresh host or validating candidate changes before deployment, the host disables the legacy bundled network plugin, applies the Git-versioned Cilium + Hubble manifest bundle (`deploy/platform/cni/`) during bootstrap so the node and core DNS reach Ready status, and continuously reconciles that same layer via FluxCD. Only the Cilium + Hubble UI footprint is included in the current platform reservation (with zero resource reservation wasted on undeployed Grafana/Prometheus/Loki components), preserving maximum $1/n$ resource slices for hosted projects.

**Why this priority**: Without deterministic bootstrap ordering, a node started without its legacy network plugin remains unready; keeping a single Git-owned manifest bundle (`deploy/platform/cni/`) satisfies both initial host readiness and continuous FluxCD / `platform-preflight.py` budget accounting (Constitution Principles I & IV).

**Independent Test**: Run pre-deployment resource validation across both `local-k3s` and `vps-k3s` profiles, and execute the host bootstrap workflow to verify the node, core DNS, Cilium, Flux reconciler, and Hubble UI all reach Ready status within bounded timeouts.

**Acceptance Scenarios**:

1. **Given** a target host being bootstrapped, **When** the cluster server starts with the legacy bundled network plugin and legacy policy controller disabled, **Then** bootstrap applies the Git-owned `deploy/platform/cni/` manifest bundle and verifies Cilium so the node and cluster DNS reach Ready status before FluxCD reconciliation proceeds.
2. **Given** the CPU and memory requests and limits declared in `deploy/platform/cni/` for Cilium, Hubble Relay, and Hubble UI, **When** pre-deployment capacity validation (`platform-preflight.py`) runs for `local-k3s` and `vps-k3s`, **Then** the platform reservation automatically counts those components and all project workloads still fit within their deterministic $1/n$ slices.

---

### User Story 4 - Seamless future expansion path from Hubble UI alone to Grafana (Priority: P2)

While the initial deployment operates with **Hubble UI alone**, Hubble exposes standardized flow and metrics interfaces (`drop`, `flow`, `dns`, `tcp`) inside the cluster network so that a future **Grafana** observability layer (Grafana dashboards, Prometheus metrics scraper, and Loki historical flow/container log storage) can be added as an independent platform layer without modifying Cilium's CNI datapath, replacing Hubble UI, or changing any project manifests.

**Why this priority**: Designing the Hubble telemetry boundary up front prevents rework when the operator later expands from live single-namespace Hubble UI inspection to historical multi-namespace Grafana dashboards and container log correlation (`EXPAND-01` principle: simple now, expandable later).

**Independent Test**: Verify on the deployed cluster that only Hubble UI (and zero Grafana/Prometheus/Loki pods) is running in the initial release, while Hubble's internal metrics endpoint (`:9965/metrics`) and Hubble Relay gRPC service are active and scrapable inside the cluster for future Grafana integration.

**Acceptance Scenarios**:

1. **Given** the initial deployment of this feature, **When** the cluster workloads are inspected, **Then** Cilium, Hubble Relay, and Hubble UI are active, and zero Grafana, Prometheus, or Loki workloads are required or deployed.
2. **Given** the running Hubble instance, **When** an internal cluster consumer queries the Hubble metrics endpoint, **Then** structured Prometheus-compatible flow and drop metrics (`drop`, `flow`, `dns`, `tcp`) are served and ready for a future Grafana/Prometheus scraper.
3. **Given** a future addition of a Grafana observability layer, **When** Grafana (with Prometheus/Loki) is attached to Hubble's existing metrics endpoint and Hubble Relay stream, **Then** it integrates without restarting the CNI datapath, modifying `k3s.service`, or altering any project namespace manifests.

---

### User Story 5 - Private operator-only access to Hubble UI and telemetry endpoints (Priority: P2)

Hubble UI, Hubble Relay, and Hubble's future-Grafana metrics endpoints are accessible only within the cluster or to the authorized operator through a single-command private tunnel helper (`scripts/open-hubble-ui.sh` on `http://127.0.0.1:12000`), and are never exposed anonymously on public internet ingress.

**Why this priority**: Internal network topology, pod IP addresses, and live traffic metadata are sensitive operational data that must remain protected under the public-surface allowlist (Constitution Principle III).

**Independent Test**: Attempt to reach Hubble UI, Hubble Relay, and Hubble metrics endpoints through the public ingress hostnames from an unauthenticated external client (expecting rejection), and verify the operator can open Hubble UI in a browser on `http://127.0.0.1:12000` by running `scripts/open-hubble-ui.sh`.

**Acceptance Scenarios**:

1. **Given** the public ingress entry point, **When** an external client requests Hubble UI, Hubble Relay, or Hubble metrics paths on any public hostname, **Then** the request is rejected and no observability UI or telemetry data is served publicly.
2. **Given** an authorized operator running `scripts/open-hubble-ui.sh`, **When** the operator opens `http://127.0.0.1:12000` in a local browser, **Then** Hubble UI loads and streams live cluster flows across namespaces.

---

### Edge Cases

- **Node starts before Cilium is ready**: Pods must not be scheduled onto an uninitialized network or bypass network policy enforcement while the CNI daemon is starting; node readiness must wait until Cilium is operational.
- **High volume of dropped or allowed flows on a constrained single node**: Because the initial design uses Hubble UI alone without persistent external storage, flow retention in memory must use a bounded ring buffer so a burst of network traffic cannot exhaust host RAM or disk space.
- **Hubble UI or Hubble Relay temporarily unavailable**: If Hubble UI or Hubble Relay restarts or fails, Cilium packet forwarding and network policy enforcement must continue uninterrupted with zero impact on live project traffic.
- **Existing host with legacy Flannel interfaces (`flannel.1`, `cni0`)**: Transitioning a host previously bootstrapped with bundled Flannel must clean up stale Flannel/CNI bridge interfaces during host preparation so they do not conflict with Cilium routing.
- **Cross-namespace flows involving platform proxies**: Flows from `proj-pn` to `vault-db-proxy` and from `vault-db-proxy` to `postgres` must resolve human-readable namespace and workload names in Hubble UI rather than displaying only raw IP addresses.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The platform MUST use Cilium as its cluster CNI network layer and network policy enforcement engine, replacing the host distribution's bundled Flannel network plugin and embedded `kube-router` policy controller.
- **FR-002**: The pinned Cilium, Hubble Relay, and Hubble UI manifests MUST reside in a single Git-owned platform directory (`deploy/platform/cni/`): the host bootstrap workflow MUST disable the bundled Flannel backend (`--flannel-backend=none`) and embedded network policy controller (`--disable-network-policy`), apply `deploy/platform/cni/` during initial cluster installation so Cilium, the node, and CoreDNS reach Ready status within bounded timeouts, and register `cni` as a FluxCD GitOps layer for continuous reconciliation.
- **FR-003**: For the initial observability design, the platform MUST deploy **Hubble UI alone** (alongside Cilium and Hubble Relay) as the standalone browser interface for visual network flow and service-map debugging, and MUST NOT deploy Grafana, Prometheus, or Loki in this initial release.
- **FR-004**: Hubble UI MUST display, for each observed network flow, the source namespace and workload identity, destination namespace and workload identity (or external IP/DNS), port, protocol, traffic direction, timestamp, and policy verdict (`Forwarded` vs. `Dropped`).
- **FR-005**: To support seamless **future expansion to Grafana**, Hubble MUST expose its internal Prometheus-compatible metrics interface (`drop`, `flow`, `dns`, `tcp`) and Hubble Relay flow stream inside the cluster so that a future Grafana (with Prometheus/Loki) layer can subscribe without reconfiguring the CNI datapath or modifying project workloads.
- **FR-006**: Cilium MUST enforce all existing standard `NetworkPolicy` declarations across `platform`, `identity`, `vault`, and project namespaces (`proj-*`) without requiring tenant projects to adopt vendor-specific `CiliumNetworkPolicy` custom resources; in `deploy/platform/vault/`, OpenBao's broad `ipBlock: 0.0.0.0/0` egress workaround MUST be replaced with a `CiliumNetworkPolicy` allowing egress strictly to `toEntities: ["kube-apiserver"]` on ports `443` and `6443`.
- **FR-007**: The existing public ingress controller (Traefik) and platform-owned TLS termination architecture MUST remain unchanged so public routing and certificate management are unaffected by the CNI change.
- **FR-008**: Flow storage in Hubble MUST be bounded in memory using a fixed-capacity ring buffer and MUST NOT write unbounded flow logs to host disk storage in the initial Hubble-UI-only deployment.
- **FR-009**: All Cilium, Hubble Relay, and Hubble UI containers in `deploy/platform/cni/` MUST declare explicit CPU and memory requests and limits so `platform-preflight.py` automatically includes them in the platform resource reservation during pre-deployment capacity validation.
- **FR-010**: Hubble UI, Hubble Relay, and Hubble metrics endpoints MUST use `ClusterIP` (or cluster-internal) exposure only and MUST NOT be exposed on the public ingress allowlist, `NodePort`, or `LoadBalancer` services; a single-command operator helper script (`scripts/open-hubble-ui.sh`) MUST be provided to tunnel `svc/hubble-ui` to `http://127.0.0.1:12000`.
- **FR-011**: Failure or restart of Hubble UI or Hubble Relay MUST NOT disrupt Cilium pod-to-pod packet forwarding or weaken network policy enforcement.
- **FR-012**: Platform architecture, dependency, and specification documentation (`SPEC.md`, `ARCHITECTURE.md`, `DEPENDENCIES.md`, and `docs/bootstrap-scope.md`) MUST be updated to reflect the Cilium + Hubble UI initial architecture and the designated future expansion path to Grafana.
- **FR-013**: An automated verification script MUST validate that Cilium is healthy, standard network policies and OpenBao's `kube-apiserver` entity policy are actively enforced, dropped flows are recorded with policy-denied verdicts, Hubble metrics are internally scrapable for future Grafana expansion, and Hubble UI is reachable privately and blocked publicly.

### Key Entities

- **Network Flow Record**: A structured event captured by Hubble representing a packet or connection attempt, including source workload identity, destination workload identity, protocol/port, direction (ingress/egress), policy verdict (`Forwarded` or `Dropped`), drop reason, and timestamp.
- **Service Dependency Map**: A live visual graph rendered in Hubble UI showing namespaces, workloads, and directed edges representing active or blocked network communication between them.
- **Grafana Expansion Interface**: The cluster-internal Hubble metrics (`:9965/metrics`) and Hubble Relay gRPC stream endpoints exposed in the initial design so a future Grafana + Prometheus/Loki stack can attach without altering the CNI layer.
- **Platform Resource Reservation**: The aggregate CPU and memory budget reserved for shared platform infrastructure (including Cilium, Hubble Relay, and Hubble UI) subtracted from node allocatable capacity before computing $1/n$ project slices.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: 100% of permitted and policy-denied network connections between platform and project workloads appear in **Hubble UI** with accurate source/destination workload names, port, and policy verdict (`Forwarded` or `Dropped`) within 5 seconds of occurrence, requiring 0 command-line log queries (`kubectl logs`) to identify a blocked network connection.
- **SC-002**: The initial deployment runs **Hubble UI alone** with 0 Grafana, Prometheus, or Loki pods deployed, while 100% of Hubble's internal Grafana-ready telemetry endpoints (`drop`, `flow`, `dns`, and `tcp` metrics plus Hubble Relay stream) respond validly inside the cluster for future Grafana expansion.
- **SC-003**: 100% of existing network isolation tests (default-deny ingress/egress, explicit namespace allowlists across `platform`, `identity`, `vault`, and `proj-pn`, and OpenBao's `kube-apiserver`-only egress) pass under Cilium without modifying any tenant project's standard `NetworkPolicy` manifests.
- **SC-004**: Pre-deployment resource and schema validation (`platform-preflight` and `render-validate`) passes for 100% of supported environments (`local-k3s` and `vps-k3s`) with the Cilium + Hubble UI resource footprint included within the platform reservation and project $1/n$ slices preserved.
- **SC-005**: 0 Hubble UI, Hubble Relay, or Hubble metrics endpoints are reachable through public ingress hostnames, while 100% of namespaces are viewable in Hubble UI by an operator connecting through `scripts/open-hubble-ui.sh`.
- **SC-006**: Restarting or scaling down Hubble UI or Hubble Relay causes 0 dropped packets or service interruptions on active project and authentication traffic.

## Assumptions & Future Expansion

- **Phased Observability Rollout (`Hubble UI Alone` → `Grafana` Later)**:
  - **Phase 1 (This Specification)**: Only Cilium, Hubble Relay, and **Hubble UI** are deployed. Operators inspect live per-namespace service maps and real-time `Forwarded` / `Policy Denied` flow logs directly in Hubble UI with minimal RAM consumption.
  - **Phase 2 (Future Specification)**: **Grafana** (backed by Prometheus for Hubble metrics and Grafana Loki for historical Hubble flow logs + pod stdout/stderr container logs) will be added as an optional platform observability layer consuming the Hubble interfaces prepared in `FR-005`.
- Operators access Hubble UI via `scripts/open-hubble-ui.sh` (`http://127.0.0.1:12000`) connecting to its `ClusterIP` service, consistent with Constitution Principle III and `ISOLATE-01` for administrative consoles.
- Traefik remains the platform ingress controller for edge HTTP/HTTPS routing and TLS termination; Cilium is adopted for CNI pod networking, standard `NetworkPolicy` enforcement, and Hubble network observability.
- Standard Kubernetes `NetworkPolicy` manifests remain the required tenant contract so workloads stay portable to EKS and GKE without vendor lock-in.
