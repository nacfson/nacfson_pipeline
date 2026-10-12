# Validation Log: Spec 008 Platform-Owned TLS Termination

**Feature**: [spec.md](spec.md) · **Plan**: [plan.md](plan.md) · **Date**: 2026-10-12

---

## Approvals

- **Constitution Amendment 2.2.0**: Approved and merged into `origin/main` via PR #31 (commit `8d41126` merged in `b49a17c`) on 2026-10-12. This also ratified the draft lineage 2.0.0 and 2.1.0 per maintainer review.

---

## Baseline

Executed on `origin/main` (`b49a17c`) inside worktree `../nacfson_pipeline-008`:

```text
=== Running render-validate and preflight for local-k3s ===
All render invariants passed successfully for environment: local-k3s
Platform Preflight Status: PASSED
Allocatable: CPU=4000m, Mem=8192Mi
Platform Reservation: CPU=1695m, Mem=4064.0Mi
Project Count: 1, Slice: CPU=2305.0m, Mem=4128.0Mi

=== Running render-validate and preflight for vps-k3s ===
All render invariants passed successfully for environment: vps-k3s
Platform Preflight Status: PASSED
Allocatable: CPU=4000m, Mem=24576Mi
Platform Reservation: CPU=1695m, Mem=4064.0Mi
Project Count: 1, Slice: CPU=2305.0m, Mem=20512.0Mi

=== Running preflight tests ===
.............. [100%]
14 passed in 0.81s
```

Result: **Clean baseline**. All schemas, render invariants, preflight QoS/capacity calculations, and preflight unit tests pass.

---

## Cluster Preconditions (T006)

*Pending execution on the VPS VM:*
- `sudo k3s kubectl get crd tlsstores.traefik.io`
- `sudo k3s kubectl get tlsstore -A`
- `sudo k3s kubectl get certificate -A`
- `sudo k3s kubectl describe challenge -n proj-pn | tail -15`

---

## Preflight TLS Implementation Validation (T020)

Executed on branch `tmp/platform-tls-store-20261012-2245` in worktree `../nacfson_pipeline-008`:

### 1. Preflight Unit Tests
```text
pytest tests/preflight -v
============================== 21 passed in 1.23s ==============================
```
All 21 test cases passed, including:
- `test_15_valid_tls_render_passed`: minimal valid render with `TLSStore platform/default` and route `tls: {}` passes.
- `test_16_tls_project_certificate_rejected`: rejects project-scoped Certificate (TLS-1).
- `test_17_tls_route_secret_name_rejected`: rejects route `tls.secretName` (TLS-2).
- `test_18_tls_route_web_entrypoint_rejected`: rejects route with non-websecure entrypoints (TLS-3).
- `test_19_tls_route_host_regexp_rejected`: rejects route match with HostRegexp (TLS-4).
- `test_20_tls_uncovered_host_rejected`: rejects route with host not in covered_hosts (TLS-5).
- `test_21_tls_store_missing_secret_rejected`: rejects TLSStore referencing non-existent Certificate (TLS-6).

### 2. Render Invariants and Preflight Status
- `local-k3s`:
  - `render-invariants`: All render invariants passed successfully.
  - `platform-preflight`: Status `PASSED`, 0 violations.
  - Project `proj-pn` IngressRoute has `tls: {}` and `entryPoints: [websecure]`.
- `vps-k3s`:
  - `render-invariants`: All render invariants passed successfully.
  - `platform-preflight`: Status `PASSED`, 0 violations.
  - Project `proj-pn` IngressRoute has `tls: {}` and `entryPoints: [websecure]`.

---

## Verification Script and Linter Validation (T023)

Executed on branch `tmp/platform-tls-store-20261012-2245` in worktree `../nacfson_pipeline-008`:

### 1. ShellCheck on `scripts/verify-tls.sh`
```bash
shellcheck scripts/verify-tls.sh
```
Result: Exited 0 with no warnings or errors.

### 2. Yamllint on `deploy/platform/tls/`
```bash
yamllint deploy/platform/tls/
```
Result: Exited 0 with no warnings or errors across all manifests (`certificates.yaml`, `cluster-issuers.yaml`, `kustomization.yaml`, `tls-store.yaml`).

### 3. Render Invariants & Preflight Validation
```bash
./scripts/render-validate.sh local-k3s
./scripts/render-validate.sh vps-k3s
```
Result: Both environments rendered cleanly and passed all schema validation, invariant checks, and preflight rules.



---

## PR A Cluster Verification (T025, 2026-10-12 23:10 KST)

PR #32 merged as `036df80`; all Flux layers Ready at that revision.

- `TLSStore platform/default` present; `platform/auth-tls` and `platform/pn-tls` Ready (`pn-tls` issued within ~1 min via ClusterIP solver).
- `proj-pn`: no Certificate/CertificateRequest/Order/Challenge, no `kubernetes.io/tls` Secret, only `project-pn-isolation` NetworkPolicy (T026 cleanup not needed).
- Images: backend `…@sha256:662c869b…`, frontend `…@sha256:1da19ae7…` running.
- External: `pn.nacfson.cloud` https 200, Let's Encrypt YE1, expires 2027-01-10; `auth.nacfson.cloud` Let's Encrypt YE1 (404 on `/` is the allowlist); both http → 301 https.
