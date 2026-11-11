# Implementation Plan: GKE Platform Portability & Full-State Cutover

**Branch**: `012-gke-platform-migration` | **Date**: 2026-11-10 | **Spec**: [spec.md](spec.md)

**Input**: Feature specification from [`specs/012-gke-platform-migration/spec.md`](spec.md)

**HTML version**: [agy_plan/html/012-gke-platform-migration.html](../../agy_plan/html/012-gke-platform-migration.html)

---

## Summary

Deliver a cloud-agnostic, declarative **Google Kubernetes Engine (`clusters/gke/`)** environment overlay alongside a synchronized **`clusters/local-k3s/`** overlay, enabling a single-shot full-state cutover from Oracle Cloud `vps-k3s` to GKE while freeing OCI Always Free capacity for the replicated Kafka + Flink log pipeline (`specs/013` and `specs/014`).

Core technical pillars (all resolved in [`research.md`](research.md) `R1`–`R9`):
- **Design 1 Hybrid Node Topology & Zero-LB Edge Ingress (`FR-001`, `FR-003`)**: Run a Zonal GKE Standard cluster in GCP project **`nacfson-pipeline`** (`asia-northeast3-a` Seoul, `--logging=NONE --monitoring=NONE`, GKE Dataplane V2) with **`1 × e2-medium` On-Demand** (`ondemand-core`, \$31.38/mo PAYG / \$19.77/mo 1-yr in Seoul) + **`1 × e2-small` Spot** (`spot-controllers`, \$7.81/mo in Seoul) for ~\$53.31/mo PAYG / ~\$41.70/mo 1-yr CUD all-in. Bind non-root `Traefik` (`v3.3.4`) directly to `hostPort: 80/443` on Node 1's static IPv4 (eliminating the \$18+/mo GCP LoadBalancer), keep **100% of live user traffic, OIDC token introspection (`keycloak` right-sized to `832Mi` with `--features=persistent-user-sessions`), and `ReadWriteOnce` PVCs** on the On-Demand node 24/7, and offload only PVC-free `cert-manager` ×3 and `flux-system` ×2 to the tiny Spot node.
- **Cloud Build + Public GAR & Portable `NetworkPolicy` (`FR-004`, `FR-005`)**: Migrate container builds/hosting to Google Cloud Build (2,500 min/mo Free Tier) and a publicly readable Google Artifact Registry (`asia-northeast3-docker.pkg.dev/nacfson-pipeline/nacfson-public`, `< 0.5 GB/mo` Free Tier via cleanup policy) so both `gke` and `local-k3s` pull identical `@sha256` digests without credentials, retiring `vault-registry-proxy`. Replace all 4 `CiliumNetworkPolicy` blocks directly in `deploy/` with portable `networking.k8s.io/v1` `NetworkPolicy` rules blocking RFC1918 and `169.254.169.254/32` on outbound `:443` callers.
- **Fluent Bit Solution B Node-Local Recovery & Kafka Output (`FR-006`, `FR-007`)**: Replace the cluster-wide `ReadWriteOnce` `fluent-bit-state` PVC with per-node `hostPath` `/var/lib/fluent-bit-state` (`tail.db` + `storage.type filesystem` + `Emitter_Storage.type filesystem`, capped at `256 MiB`/node), omit in-cluster `StatefulSet/victorialogs` on GKE from day 1, and configure `[OUTPUT] kafka` (`acks=-1`, `logs.proj.valid` & `logs.proj.quarantine`).
- **Full-State Cutover Bundle & `gcpckms` Auto-Unseal (`FR-008`–`FR-011`)**: Extend `scripts/db-migrate-dump.sh` (`--keycloak`) and add `scripts/cutover-bundle.sh` to export, SHA-256 verify, and restore `openbao.snap` (auto-rebinding GKE K8s auth CA/JWT and migrating seal to GCP Cloud KMS `gcpckms` in `nacfson-pipeline` / `asia-northeast3` via Workload Identity), `keycloak.dump.gz`, `proj_*.dump.gz`, edge TLS Secrets, and `victoria-logs-data.tar.gz`, followed by immediate Gate 4 repurposing of `vps-k3s` into OCI Kafka Broker VM 1.

---

## Technical Context

**Language/Version**: YAML manifests (Kustomize via `kubectl kustomize`, Flux `kustomize.toolkit.fluxcd.io/v1`); Python `3.11+` (`scripts/platform-preflight.py`, `scripts/render-invariants.py`); Go `1.22` stdlib-only (`gateway/`, `services/vault-proxies/`); Bash (`scripts/db-migrate-dump.sh`, `scripts/vault-configure-k8s-auth.sh`, `scripts/cutover-bundle.sh`, `scripts/render-validate.sh`)

**Primary Dependencies**:
- GKE Standard (Zonal `asia-northeast3-a`, GCP Project `nacfson-pipeline`, Kubernetes `1.31+` / `1.35` schema target, GKE Dataplane V2, Workload Identity `nacfson-pipeline.svc.id.goog`, `--logging=NONE --monitoring=NONE`)
- Traefik `v3.3.4` (`traefik:v3.3.4`), FluxCD `v2.9.5`, cert-manager `v1.21.2`
- OpenBao `2.0.0` (`seal "gcpckms"` via GCP Cloud KMS + Workload Identity), PostgreSQL `16.8-alpine`, Keycloak `24.0.5` (`--features=persistent-user-sessions`)
- Fluent Bit `4.2.8` (`fluent/fluent-bit:4.2.8` with `in_tail` SQLite + filesystem buffering + `out_kafka` `librdkafka`)
- Google Cloud Build & Google Artifact Registry (`asia-northeast3-docker.pkg.dev/nacfson-pipeline/nacfson-public`)

**Storage**:
- GCE Persistent Disks (`standard-rwo`, `28 GiB` total on `ondemand-core` only: `openbao-0` `10Gi`, `postgres-0` `10Gi`, `error-detector-state` `1Gi`, `postgres-backup-pvc` `2Gi`, `openbao-backup-pvc` `5Gi`)
- Node-local `hostPath` `/var/lib/fluent-bit-state` (`DirectoryOrCreate`, capped at `256 MiB`/node) on `DaemonSet/fluent-bit`

**Testing**:
- `python3 -m unittest discover tests/preflight` & `python3 -m unittest discover tests/hooks`
- `./scripts/render-validate.sh local-k3s`, `./scripts/render-validate.sh gke`, `./scripts/render-validate.sh vps-k3s` (`flux build`, `kubeconform`, `render-invariants.py`, `platform-preflight.py`)
- `(cd gateway && go test -v ./... && go vet ./...)` & `(cd services/vault-proxies && go test -v ./... && go vet ./...)`
- `python3 scripts/check-arch-doc-sync.py --worktree`
- Gate 2 `local-k3s` Fluent Bit outage/restart drill & Gate 3 `scripts/cutover-bundle.sh` + `scripts/smoke-auth-chain.sh` restore drill

**Target Platform**: Zonal GKE Standard (`clusters/gke/`: GCP project `nacfson-pipeline`, zone `asia-northeast3-a` Seoul, `1 × e2-medium` On-Demand + `1 × e2-small` Spot, `linux/amd64`) and local Linux K3s (`clusters/local-k3s/`: `4000m` CPU / `8192Mi` RAM)

**Project Type**: Declarative GitOps multi-tenant Kubernetes platform infrastructure, preflight governance tooling, and state-migration automation

**Performance Goals**:
- Offline render + schema + multi-node preflight validation across `local-k3s` and `gke` completes in `< 60s` (`SC-001`).
- Zero lost or duplicated log lines across `fluent-bit` pod restarts during a 2-minute downstream outage (`SC-002`).
- Zero HTTPS certificate warning window and 100% `(issuer, subject)` user identity continuity across DNS cutover (`SC-003`, `SC-004`).

**Constraints**:
- Gate 1 hard gate: zero cloud VMs or clusters provisioned or modified until 100% of offline + `local-k3s` checks pass.
- Zero GCP LoadBalancer (`Service` `type: ClusterIP` + `Traefik` `hostPort: 80/443`).
- Zero `ReadWriteOnce` PVCs, tenant pods, or `keycloak`/`gateway` pods on the Spot node.
- Zero third-party Go modules in `gateway/` and `services/vault-proxies/`.
- Zero plaintext secrets in Git or Kubernetes `Secret`s (only edge TLS `Secret`s in `platform` and ACME account keys in `cert-manager`; Cloud KMS uses keyless GKE Workload Identity).

**Scale/Scope**: 1 new environment overlay (`clusters/gke/`), 1 new Traefik platform layer (`deploy/platform/traefik/`), 1 Constitution minor amendment (`2.3.0`), 4 `CiliumNetworkPolicy` → `NetworkPolicy` migrations, `vault-registry-proxy` retirement, multi-node `platform-preflight.py` extension, and 2 backup/cutover scripts (`scripts/db-migrate-dump.sh`, `scripts/cutover-bundle.sh`).

All unknowns are resolved in [`research.md`](research.md). Zero `NEEDS CLARIFICATION` markers remain.

---

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

Evaluated against [`.specify/memory/constitution.md`](../../.specify/memory/constitution.md) (**v2.3.0**, amended 2026-11-11 per [`research.md`](research.md#r9-constitution-amendment-230-scope)):

| Principle / Gate | Assessment | Result |
| :--- | :--- | :--- |
| **I. Declarative GitOps & Immutable Digests** | `clusters/gke/` and `deploy/platform/traefik/` are 100% Git-reconciled via FluxCD. Multi-arch (`linux/amd64,linux/arm64`) hosted project images keep immutable `@sha256:...` digests on public `GAR` (Constitution `v2.3.0`); third-party images (`traefik:v3.3.4`, `fluent/fluent-bit:4.2.8`, `keycloak:24.0.5`, `openbao:2.0.0`) use exact version tags. | **PASS** |
| **II. Strict Workload Isolation & Restricted PSS** | All `proj-*` namespaces retain `Restricted` PSS, `ClusterIP`-only services, non-root containers, and portable `networking.k8s.io/v1` `NetworkPolicy` blocking RFC1918 and `169.254.169.254/32` metadata server access. | **PASS** |
| **III. Defense-in-Depth Identity & Stdlib Gateway** | `gateway/` stays 100% Go `1.22` stdlib-only. Keeping right-sized `keycloak` (`832Mi`) on On-Demand `e2-medium` preserves online token introspection 24/7 while keeping `/admin/` private. Restoring the `keycloak` DB preserves `(issuer, subject)` continuity. | **PASS** |
| **IV. Bounded Resource Allocation & $1/n$ Budgeting** | `platform-preflight.py` multiplies DaemonSets by `nodeCount: 2`, checks per-pool (`ondemand-core` vs. `spot-controllers`) CPU/memory fit, and verifies cluster-wide $1/3$ slice (`303m` CPU / `418 MiB` RAM >= `proj-pn` `150m` / `384 MiB`). Scoping `pn-backend` `256Mi` to `clusters/gke/` preserves `local-k3s` baseline under the Interim Memory Limit Freeze. | **PASS** |
| **V. Zero Plaintext Secrets & Edge TLS / Cloud KMS Exceptions** | Retiring `vault-registry-proxy` eliminates GHCR PAT handling. `openbao-0` `gcpckms` auto-unseal uses keyless GKE Workload Identity (`ServiceAccount/openbao` IAM binding, zero K8s `Secret` objects — Constitution `v2.3.0`). Pre-seeded TLS `Secret`s stay strictly in `platform` and `cert-manager` out-of-git. | **PASS** |
| **VI. Environmental Portability Without Code Changes** | Replacing `CiliumNetworkPolicy` with standard `networking.k8s.io/v1` `NetworkPolicy` and removing hardcoded `storageClassName: local-path` allows identical tenant/platform manifests to run on `local-k3s` and `gke` with zero application code changes. | **PASS (improves)** |
| **VII. Distinct Lifecycle Signals & Browser CORS** | `proj-pn` and `proj-health` startup/readiness/liveness probes and `PUBLIC_APP_URL` / `ALLOWED_ORIGINS` patches carry over identically to `clusters/gke/projects/`. | **PASS** |
| **Clarification 2.2.1 → Amendment 2.3.0 (`hostPath`)** | Permits `DaemonSet/fluent-bit` in `observability` to mount `/var/lib/fluent-bit-state` (`readOnly: false`) alongside `/var/log/pods` (`readOnly: true`); preflight rejects any other `hostPath`. | **PASS** |

**Pre-Research Gate Decision**: **PASSED** (Constitution `v2.3.0` updated in `.specify/memory/constitution.md`, justified in Complexity Tracking below).
**Post-Design Re-Check (after Phase 1)**: [`data-model.md`](data-model.md), [`contracts/*`](contracts/), and [`quickstart.md`](quickstart.md) introduce zero unratified constitutional deviations.

---

## Project Structure

### Documentation (this feature)

```text
specs/012-gke-platform-migration/
├── spec.md                                                 # Clarified feature specification (Design 1 locked)
├── plan.md                                                 # This file (/speckit-plan output)
├── research.md                                             # Phase 0 architectural decisions (R1–R9)
├── data-model.md                                           # Phase 1 entities, schemas & state transitions
├── quickstart.md                                           # Phase 1 4-Gate validation & cutover runbook
├── contracts/                                              # Phase 1 preflight, network & cutover contracts
│   ├── gke-capacity-and-placement.md
│   ├── portable-network-policy-and-gar.md
│   └── fluentbit-recovery-and-cutover-bundle.md
├── checklists/
│   └── requirements.md                                     # 13/13 passing spec quality checklist
└── tasks.md                                                # Phase 2 output (/speckit-tasks — NOT created here)
```

### Source Code & Manifests (repository root)

```text
.specify/memory/
└── constitution.md                                         # UPDATED (v2.3.0): /var/lib/fluent-bit-state, public GAR, gcpckms

deploy/
├── gcp/
│   ├── cloudbuild.yaml                                     # NEW: Declarative multi-arch Cloud Build pipeline -> public GAR
│   └── gar-cleanup-policy.json                             # NEW: Keep latest 2 digests, purge >7d untagged (<0.5 GB Free Tier)
├── platform/
│   ├── cni/
│   │   └── kustomization.yaml                              # MODIFIED: Set policy-cidr-match-mode=nodes in cilium-config for local-k3s
│   ├── governance/
│   │   └── manual-change-policy.yaml                       # MODIFIED: Allowlist GKE system principals & .gke.io/.cloud.google.com CRDs
│   ├── identity/
│   │   └── keycloak-deployment.yaml                        # MODIFIED: Enable --features=persistent-user-sessions
│   ├── observability/
│   │   ├── error-detector.yaml                             # MODIFIED: Omit hardcoded storageClassName: local-path
│   │   ├── fluent-bit-daemonset.yaml                       # MODIFIED: hostPath /var/lib/fluent-bit-state + Spot toleration
│   │   ├── fluent-bit-state-pvc.yaml                       # DELETED: Replaced by per-node /var/lib/fluent-bit-state
│   │   ├── kustomization.yaml                              # MODIFIED: Drop fluent-bit-state-pvc.yaml
│   │   └── fluent-bit/
│   │       └── fluent-bit.conf                             # MODIFIED: Add storage.path /state/chunks & filesystem emitters
│   ├── traefik/                                            # NEW: GKE edge ingress layer (Traefik v3.3.4, hostPort 80/443, ClusterIP, 30m/96Mi)
│   │   ├── crds.yaml
│   │   ├── deployment.yaml
│   │   ├── kustomization.yaml
│   │   ├── rbac.yaml
│   │   └── service.yaml
│   ├── vault/
│   │   ├── kustomization.yaml                              # MODIFIED: Remove registry-proxy policy reference if any
│   │   ├── network-policy.yaml                             # MODIFIED: Replace 3 CiliumNetworkPolicies with standard NetworkPolicy; drop registry-proxy-policy
│   │   ├── registries.yaml.template                        # DELETED: Retired K3s registry mirror template
│   │   ├── service-accounts.yaml                           # MODIFIED: Drop ServiceAccount/vault-registry-proxy; add ServiceAccount/openbao
│   │   └── policies/
│   │       └── registry-proxy.hcl                          # DELETED: Retired with vault-registry-proxy
│   └── vault-proxies/
│       ├── kustomization.yaml                              # MODIFIED: Remove registry-proxy.yaml
│       └── registry-proxy.yaml                             # DELETED: Retired with public GAR migration
└── projects/
    └── pn/boundary/
        └── translation-egress.yaml                         # MODIFIED: Replace CiliumNetworkPolicy with standard NetworkPolicy (:443 - RFC1918 - 169.254.169.254/32)

services/vault-proxies/
├── Dockerfile.registry-proxy                               # DELETED: Retired registry proxy container build
└── cmd/registry-proxy/                                     # DELETED: Retire unused registry proxy binary

clusters/
├── gke/                                                    # NEW: Declarative GKE Standard Design 1 overlay
│   ├── capacity.yaml                                       # NEW: 2-node profile (1x e2-medium On-Demand + 1x e2-small Spot)
│   ├── layers.yaml                                         # NEW: Flux DAG (traefik, Spot selectors, project-pn-quota 416Mi, keycloak 832Mi, openbao gcpckms)
│   ├── flux-system/                                        # NEW: Flux controllers patched with Spot selector/toleration & 128Mi limits
│   │   ├── gotk-components.yaml
│   │   ├── gotk-sync.yaml
│   │   └── kustomization.yaml
│   ├── observability/                                      # NEW: Deletes victorialogs + victorialogs-ingress, pauses error-detector (replicas: 0), [OUTPUT] kafka
│   │   ├── fluent-bit-kafka.conf
│   │   └── kustomization.yaml
│   └── projects/                                           # NEW: Project Flux Kustomizations (patches pn/health/blog resources on GKE)
│       ├── kustomization.yaml
│       ├── proj-blog.yaml
│       ├── proj-health.yaml
│       └── proj-pn.yaml
└── local-k3s/
    └── layers.yaml                                         # VERIFIED/UPDATED: Kept in lockstep with portable NetworkPolicy & Solution B

scripts/
├── cutover-bundle.sh                                       # NEW: Full-state export / SHA-256 verify / restore + gcpckms seal-migration orchestrator
├── db-migrate-dump.sh                                      # MODIFIED: Add --keycloak support alongside --project <slug>
├── platform-preflight.py                                   # MODIFIED: Multi-node DaemonSet & pool fit, portable NetworkPolicy, GAR digest & hostPath checks
├── render-invariants.py                                    # MODIFIED: Support gke environment invariants & zero-LoadBalancer check
└── vault-configure-k8s-auth.sh                             # MODIFIED: GKE kube-root-ca.crt support + remove vault-registry-proxy role

tests/preflight/
└── test_platform_preflight.py                              # MODIFIED: Unit tests for GKE 2-pool math, Spot RWO rejection, portable NetworkPolicy & Solution B

.github/workflows/
└── image-bump-request.yaml                                 # MODIFIED: Accept GAR <region>-docker.pkg.dev digests alongside GHCR

docs/
└── platform-architecture.html                              # MODIFIED: Synchronized topology, GKE Design 1 budget math, GAR & Fluent Bit recovery
```

**Structure Decision**: Preserve the repository's Kustomize base + `clusters/<env>/` overlay architecture (`deploy/` holds cloud-agnostic base manifests shared by `local-k3s` and `gke`, while `clusters/gke/` holds GKE-specific capacity, node-pool placement patches, `Traefik` `hostPort` layer wiring, `openbao` `gcpckms` seal config, and Fluent Bit `[OUTPUT] kafka` overlay).

---

## Key Diffs

### 1. Portable Outbound HTTPS `NetworkPolicy` (`deploy/projects/pn/boundary/translation-egress.yaml`)

```yaml
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: pn-backend-translation-egress
  namespace: proj-pn
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: pn-backend
  policyTypes:
    - Egress
  egress:
    - to:
        - ipBlock:
            cidr: 0.0.0.0/0
            except:
              - 10.0.0.0/8
              - 172.16.0.0/12
              - 192.168.0.0/16
              - 169.254.169.254/32
              - 100.64.0.0/10
      ports:
        - protocol: TCP
          port: 443
```

### 2. Fluent Bit Node-Local Recovery State (`deploy/platform/observability/fluent-bit-daemonset.yaml`)

```diff
       tolerations:
+        - key: cloud.google.com/gke-spot
+          operator: Equal
+          value: "true"
+          effect: NoSchedule
       volumes:
         - name: varlogpods
           hostPath:
             path: /var/log/pods
             type: Directory
         - name: state
-          persistentVolumeClaim:
-            claimName: fluent-bit-state
+          hostPath:
+            path: /var/lib/fluent-bit-state
+            type: DirectoryOrCreate
```

---

## Complexity Tracking

| Constitutional Evolution (`Amendment 2.3.0`) | Why Needed | Simpler Alternative Rejected Because |
| :--- | :--- | :--- |
| `/var/lib/fluent-bit-state` (`readOnly: false`) `hostPath` on `DaemonSet/fluent-bit` in `observability` | Multi-node GKE (`ondemand-core` + `spot-controllers`) cannot mount a single cluster-wide `ReadWriteOnce` PVC across 2 nodes (`Multi-Attach error`), and buffered log chunks must survive pod restarts and cross-cloud WAN outages. | `emptyDir` loses in-flight chunks and SQLite offsets on every `fluent-bit` restart (`SC-002` failure); `ReadWriteMany` Filestore costs ~\$200/mo minimum on GCP. |
| Public Google Artifact Registry (`GAR`) `@sha256` hosting alongside `GHCR` | Both GKE nodes and external `local-k3s` rehearsal clusters must pull identical `@sha256` digests with zero Kubernetes `imagePullSecrets` while retiring `vault-registry-proxy` and staying inside GCP Free Tier. | Keeping `vault-registry-proxy` fails on GKE because managed GKE nodes do not read K3s `/etc/rancher/k3s/registries.yaml`; private GAR requires plaintext pull secrets on `local-k3s` (violating Principle V). |
| GCP Cloud KMS (`gcpckms`) seal wrapper on `openbao-0` via GKE Workload Identity | Planned GKE node OS upgrades automatically drain/reboot `ondemand-core`; Shamir seal leaves `openbao-0` sealed and causes full platform outage until manual operator intervention. | Keeping manual Shamir unseal breaks unattended recovery on managed GKE node auto-upgrades; Workload Identity uses short-lived node metadata tokens with zero plaintext keys in Git or K8s `Secret`s. |
