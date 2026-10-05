# Implementation Plan: Automated Post-Vault Workload Orchestration

**Branch**: `005-post-vault-automation` | **Date**: 2026-10-05 | **Spec**: [specs/005-post-vault-automation/spec.md](file:///home/nacfson/Projects/nacfson_pipeline/specs/005-post-vault-automation/spec.md)

---

## 1. Summary

Transition the platform from the one-time manual OpenBao Vault unseal gate to 100% autonomous, hands-off convergence. This eliminates the current runtime blockers (`ErrImagePull` on proxy services and missing database password files on `postgres-0`) by:
1. Providing automated runtime secret ingestion via native Kubernetes ServiceAccount auth into in-memory `tmpfs` mounts.
2. Automating multi-arch OCI image building for Go proxy services via GitHub Actions pushing to GHCR.
3. Establishing automated, zero-touch GitOps convergence across all 10 platform layers with scheduled backup retention.

---

## 2. Technical Context

- **Language/Version**: Go 1.22 (proxies, gateway), POSIX Shell (init scripts, verification), YAML (Kubernetes/Flux)
- **Primary Dependencies**: OpenBao v2.0.0, K3s v1.35.9+k3s1, FluxCD v2.3.0, PostgreSQL 16.8-alpine, Keycloak 24.0.5
- **Storage**: K3s `local-path` PersistentVolumeClaims (`data-openbao-0`, `openbao-backup-pvc`, `postgres-data-postgres-0`, `postgres-backup-pvc`)
- **Testing**: `tests/vault/`, `tests/preflight/`, `scripts/verify-platform.sh`
- **Target Platform**: Oracle Cloud VPS (aarch64 / ARM64, Oracle Linux 9.8) & Local K3s (x86_64)
- **Project Type**: GitOps Platform Infrastructure & Microservices

---

## 3. Constitution Check

*GATE: Verified against Constitution v1.4.1*

| Principle | Requirement | Plan Alignment | Status |
|---|---|---|---|
| **Principle I (GitOps Single Source of Truth)** | Changes delivered via pull-based GitOps; immutable image digests | All manifests declared in Git; GitHub Actions publishes multi-arch images to GHCR with commit SHAs | **PASS** |
| **Principle II (Restricted Pod Security)** | Restricted PSA, non-root, drop ALL capabilities, zero hostPath | Secret fetcher initContainer runs under non-root UID 999 with all capabilities dropped; emptyDir memory volumes | **PASS** |
| **Principle III (Defense-in-Depth Identity)** | ServiceAccount token isolation; private admin endpoints | Workloads authenticate to Vault using scoped ServiceAccount TokenReview API | **PASS** |
| **Principle IV (1/n Resource Budgeting)** | Explicit CPU/memory limits; bounded resource envelopes | InitContainers allocate tiny ephemeral bounds (25m CPU, 32Mi RAM); 0 MB steady-state | **PASS** |
| **Principle V (Zero Plaintext Secrets)** | Vault is sole secret authority; zero plaintext in Git/K8s Secrets | Secrets injected into tmpfs RAM (`0400`) at container boot; zero Kubernetes Secret manifests | **PASS** |
| **Principle VI (Environmental Portability)** | Compatible across local K3s and cloud VPS without code changes | Multi-arch OCI builds (`amd64` + `arm64`) run identically on all node architectures | **PASS** |

---

## 4. Project Structure & Changes

```text
.github/
└── workflows/
    └── build-images.yaml          # [NEW] Multi-arch GitHub Actions CI for Go services

services/vault-proxies/
├── Dockerfile.db-proxy            # [NEW] Multi-stage distroless build for db-proxy
├── Dockerfile.registry-proxy      # [NEW] Multi-stage distroless build for registry-proxy
└── cmd/

deploy/platform/
├── database/
│   └── postgres-statefulset.yaml  # [MODIFY] Add secret-fetcher initContainer
├── identity/
│   └── keycloak-deployment.yaml   # [MODIFY] Add secret-fetcher initContainer
└── vault/
    └── kustomization.yaml         # [MODIFY] Configure Vault Kubernetes Auth & roles

specs/005-post-vault-automation/
├── spec.md
├── plan.md
├── research.md
├── data-model.md
├── quickstart.md
├── contracts/
│   ├── workload-secret-injection.md
│   └── ci-image-publishing.md
└── checklists/
    └── requirements.md
```

---

## 5. Verification Plan

### Automated Tests
1. **CI Image Publishing**: Push commits to trigger GitHub Actions; verify multi-arch images appear on `ghcr.io/nacfson/*`.
2. **Postgres Startup**: `kubectl wait --for=condition=ready pod/postgres-0 -n identity --timeout=60s`.
3. **Database Initialization**: `kubectl wait --for=condition=complete job/postgres-init-job -n identity --timeout=90s`.
4. **Proxy Readiness**: Verify `vault-db-proxy` and `vault-registry-proxy` pods reach `1/1 Running`.
5. **GitOps Layer Cascade**: Verify all 10 kustomizations in `kubectl get kustomizations -A` report `Ready: True`.
6. **Master Suite**: Execute `./scripts/verify-platform.sh` to confirm 100% test pass rate.
