# nacfson_pipeline Canonical Platform & Tenant Contracts (`contracts/`)

This directory is the unified, authoritative catalog of **`CONTRACT_*.md`** specifications governing every hosted project (`deploy/projects/<slug>/`) and platform boundary in **`nacfson_pipeline`**.

While [`specs/001-*` … `specs/009-*`](../specs/) preserve historical feature packages from Spec-Driven Development, the **`CONTRACT_*.md`** documents in this folder define the **live, consolidated rules** enforced at CI/pre-promotion time by [`scripts/platform-preflight.py`](../scripts/platform-preflight.py) and [`scripts/render-invariants.py`](../scripts/render-invariants.py).

---

## Contract Catalog

| Contract File | Scope & Constitutional Authority | Enforced By |
| :--- | :--- | :--- |
| [`CONTRACT_DATABASE.md`](CONTRACT_DATABASE.md) | **Internal PostgreSQL, Zero-Plaintext `vault-db-proxy` & Alternative DB (MySQL/NoSQL) Architecture**<br/>*(Constitution Principles IV, V, VI & Specs 002, 005)* | [`services/vault-proxies/cmd/db-proxy/main.go`](../services/vault-proxies/cmd/db-proxy/main.go)<br/>[`scripts/platform-preflight.py`](../scripts/platform-preflight.py) (`check_database_and_readiness_contract`) |
| [`CONTRACT_PROBES_READINESS.md`](CONTRACT_PROBES_READINESS.md) | **Startup, Readiness (`/readyz`), Liveness (`/healthz`), DB-Hop Readiness & Browser CORS**<br/>*(Constitution Principle VII & Spec 007)* | [`scripts/platform-preflight.py`](../scripts/platform-preflight.py)<br/>[`deploy/platform/vault-proxies/db-proxy.yaml`](../deploy/platform/vault-proxies/db-proxy.yaml) |
| [`CONTRACT_AUTH_DOOR.md`](CONTRACT_AUTH_DOOR.md) | **Direct Project Door, Sign-In Declaration (`none` vs `shared`), `(issuer, subject)` Identity & Session Revocation**<br/>*(Constitution Principle III & Specs 001, 006, 009)* | [`gateway/`](../gateway/)<br/>[`scripts/platform-preflight.py`](../scripts/platform-preflight.py) (`check_project_doors`) |
| [`CONTRACT_TLS_INGRESS.md`](CONTRACT_TLS_INGRESS.md) | **Platform-Owned Edge TLS Inventory, Default `TLSStore` & Single-Copy Ingress Allowlist**<br/>*(Constitution Principle V Edge Key Exception & Spec 008)* | [`scripts/platform-preflight.py`](../scripts/platform-preflight.py) (`check_edge_tls`)<br/>[`scripts/render-invariants.py`](../scripts/render-invariants.py) |
| [`CONTRACT_ISOLATION_BUDGET.md`](CONTRACT_ISOLATION_BUDGET.md) | **Restricted Pod Security, NetworkPolicy Egress Boundaries, Guaranteed QoS & $1/n$ Compute Slicing**<br/>*(Constitution Principles I, II, IV & Spec 004)* | [`scripts/platform-preflight.py`](../scripts/platform-preflight.py)<br/>[`deploy/platform/governance/manual-change-policy.yaml`](../deploy/platform/governance/manual-change-policy.yaml) |
| [`CONTRACT_LOG_SCHEMA.md`](CONTRACT_LOG_SCHEMA.md) | **Required `proj-*` log pattern (v2, OpenTelemetry-aligned): stdout/stderr only, platform envelope, one JSON line per event with `time`/`level`/`msg`, `exception.*` error fields, runtime crash signatures, producer-owned `event.name` schemas**<br/>*(Log history design, amended 2026-10-24; error pipeline, 2026-10-29)* | Manifest test against the Fluent Bit ConfigMap in `deploy/platform/observability/`. No runtime schema fetch. The error detector reads §2–§4 at query time. |

---

## Quick Onboarding Checklist for a New Project (`proj-<slug>`)

0. **Out-of-Band Vault Seeds (BEFORE any Git change)**: Seed `kv/database/<slug>-user` and `kv/projects/<slug>/client-secret` via [`scripts/vault-seed-inventory.sh`](../scripts/vault-seed-inventory.sh) on every target cluster. `postgres-init-job` curls `kv/data/database/<slug>-user` under `set -euo pipefail`, so a missing seed breaks database init for all tenants.
1. **Namespace & Boundary (`deploy/projects/<slug>/boundary/`)**: Follow [`CONTRACT_ISOLATION_BUDGET.md`](CONTRACT_ISOLATION_BUDGET.md) (`Restricted` PSS, UID `10001`, `ClusterIP`-only Services, `requests == limits` within $1/n$ budget, `kustomize.toolkit.fluxcd.io/prune: disabled`, and `<slug>-reconciler` SA/Role/RoleBinding).
2. **Database (`proj_<slug>` / `user_<slug>`)**:
   - Add `<slug>` to `PROJECT_SLUGS` in [`postgres-statefulset.yaml`](../deploy/platform/database/postgres-statefulset.yaml) and both containers of [`postgres-init-job.yaml`](../deploy/platform/database/postgres-init-job.yaml).
   - Add `<slug>` to `ALLOWED_PROJECTS` in [`db-proxy.yaml`](../deploy/platform/vault-proxies/db-proxy.yaml).
   - Add `kv/data/database/<slug>-user` read permission to [`deploy/platform/vault/policies/db-proxy.hcl`](../deploy/platform/vault/policies/db-proxy.hcl).
   - Admit ingress from `name: proj-<slug>` on TCP `5432` in `db-proxy-policy` in [`deploy/platform/vault/network-policy.yaml`](../deploy/platform/vault/network-policy.yaml).
   - Wire backend env vars (`DATABASE_URL`, `DB_HOST=vault-db-proxy.vault.svc`, `DB_NAME=proj_<slug>`, `DB_USER=project:<slug>:workload:backend`) per [`CONTRACT_DATABASE.md`](CONTRACT_DATABASE.md).
3. **Probes & Browser CORS**: Expose `/healthz` (process-local liveness) and `/readyz` (DB-hop readiness) on backend `:8080`, wire frontend `startupProbe` / `readinessProbe` / `livenessProbe`, and include `PUBLIC_APP_URL` in `ALLOWED_ORIGINS` per [`CONTRACT_PROBES_READINESS.md`](CONTRACT_PROBES_READINESS.md).
3b. **Logs**: Print one JSON object per line to stdout/stderr with `time`, `level`, and `msg`; add `exception.*` fields on `error`/`fatal`. Do not add a log file path or a log PVC. Follow [`CONTRACT_LOG_SCHEMA.md`](CONTRACT_LOG_SCHEMA.md). A new `event.name` is registered in that file after the project repository owns the body schema.
4. **Door, Sign-In & Keycloak (`deploy/projects/<slug>/sign-in.yaml`)**:
   - Declare `signIn: none` or `signIn: shared` (in both `deploy/projects/<slug>/sign-in.yaml` and `boundary/sign-in.yaml`) and attach `strip-forged-headers` per [`CONTRACT_AUTH_DOOR.md`](CONTRACT_AUTH_DOOR.md).
   - For `signIn: shared`, add client `project-<slug>` and scope `project-<slug>-audience` in [`keycloak-realm-config.yaml`](../deploy/platform/identity/keycloak-realm-config.yaml), SQL-attach `project-<slug>-audience` to `gateway-client` in [`postgres-init-job.yaml`](../deploy/platform/database/postgres-init-job.yaml), expose `PROJECT_<SLUG>_CLIENT_SECRET` in [`keycloak-deployment.yaml`](../deploy/platform/identity/keycloak-deployment.yaml), grant `kv/data/projects/<slug>/*` in [`keycloak-broker.hcl`](../deploy/platform/vault/policies/keycloak-broker.hcl), and append the project door host to `PROJECT_RETURN_HOSTS` in `clusters/<env>/layers.yaml`.
5. **Edge TLS (`deploy/platform/tls/`)**: Add `<slug>-tls` to [`certificates.yaml`](../deploy/platform/tls/certificates.yaml) (`local-rehearsal-ca` in base, patched to `letsencrypt-prod` in `clusters/vps-k3s/layers.yaml`), add `secretName: <slug>-tls` to [`tls-store.yaml`](../deploy/platform/tls/tls-store.yaml), and set `tls: {}` on the project `IngressRoute` per [`CONTRACT_TLS_INGRESS.md`](CONTRACT_TLS_INGRESS.md).
6. **Governance & Render Invariants**:
   - Add `proj-<slug>` to `gitops-managed-namespaces` and allow `system:serviceaccount:proj-<slug>:<slug>-reconciler` in the CEL expressions in [`manual-change-policy.yaml`](../deploy/platform/governance/manual-change-policy.yaml).
   - Add `("Namespace", "proj-<slug>", "kustomize.toolkit.fluxcd.io/prune", "disabled")` to Invariant 5 in [`scripts/render-invariants.py`](../scripts/render-invariants.py).
7. **Cluster Layers & Per-Project Reconciler**:
   - Add `project-boundaries-<slug>` (never prefixed `proj-`) to `clusters/{local,vps}-k3s/layers.yaml` and wire it into `tls` and `projects` `dependsOn`.
   - Add `clusters/<env>/projects/proj-<slug>.yaml` (`serviceAccountName: <slug>-reconciler`, digest-pinned images) to `clusters/<env>/projects/kustomization.yaml` (rehearsal `local-k3s` first, then `vps-k3s` after smoke checks pass).
