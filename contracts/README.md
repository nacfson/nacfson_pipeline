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

---

## Quick Onboarding Checklist for a New Project (`proj-<slug>`)

1. **Namespace & Boundary (`deploy/projects/<slug>/boundary/`)**: Follow [`CONTRACT_ISOLATION_BUDGET.md`](CONTRACT_ISOLATION_BUDGET.md) (`Restricted` PSS, UID `10001`, `ClusterIP`-only Services, `requests == limits` within $1/n$ budget).
2. **Database (`proj_<slug>` / `user_<slug>`)**: Add `<slug>` to `PROJECT_SLUGS` in [`postgres-statefulset.yaml`](../deploy/platform/database/postgres-statefulset.yaml) and [`postgres-init-job.yaml`](../deploy/platform/database/postgres-init-job.yaml), add `<slug>` to `ALLOWED_PROJECTS` in [`db-proxy.yaml`](../deploy/platform/vault-proxies/db-proxy.yaml), and wire backend env vars per [`CONTRACT_DATABASE.md`](CONTRACT_DATABASE.md).
3. **Probes & Browser CORS**: Expose `/healthz` (process-local liveness) and `/readyz` (DB-hop readiness) on backend `:8080`, wire frontend `startupProbe` / `readinessProbe` / `livenessProbe`, and include `PUBLIC_APP_URL` in `ALLOWED_ORIGINS` per [`CONTRACT_PROBES_READINESS.md`](CONTRACT_PROBES_READINESS.md).
4. **Door & Sign-In (`deploy/projects/<slug>/sign-in.yaml`)**: Declare `signIn: none` or `signIn: shared` and attach `strip-forged-headers` per [`CONTRACT_AUTH_DOOR.md`](CONTRACT_AUTH_DOOR.md).
5. **Edge TLS (`deploy/platform/tls/certificates.yaml`)**: Add `<slug>-tls` to the `platform` namespace inventory and set `tls: {}` on the project `IngressRoute` per [`CONTRACT_TLS_INGRESS.md`](CONTRACT_TLS_INGRESS.md).
