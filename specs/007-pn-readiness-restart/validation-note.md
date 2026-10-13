# Validation note (007 implement — revised)

**Date**: 2026-10-12

## Static (quickstart §0) — PASS (probes + allowlist declarations)

- FE: `restartPolicy: Always`; `startupProbe` `/healthz` `failureThreshold: 36`; `readinessProbe` `/healthz` `failureThreshold: 3`; `livenessProbe` `/`
- BE: `restartPolicy: Always`; `readinessProbe` `/readyz`; `livenessProbe` `/healthz`; env `ALLOWED_ORIGINS` + `PUBLIC_APP_URL` present
- VPS Flux patch: `PUBLIC_APP_URL=https://pn.nacfson.cloud` with matching allowlist entry
- local Flux patch: `PUBLIC_APP_URL=https://pn.example.com` with matching allowlist entry
- `network-policy.yaml` / `ingress-route.yaml`: no feature diff
- No notification sinks added under `deploy/projects/pn` or `deploy/platform`
- Spec/plan/contracts state FE Ready ≠ browser API success (SC-010)

## Cluster probe scenarios (quickstart §1–§4)

Prior note: local API was ServiceUnavailable for full §1–§4. Re-run when cluster healthy for SC-001–SC-007 live evidence. In-cluster FE→BE `/healthz` was previously observed healthy on VPS during diagnosis.

## Browser CORS (quickstart §6) — PENDING after reconcile

Pre-fix (diagnosis): `Origin: https://pn.nacfson.cloud` OPTIONS/GET to public `/api/auth/me` lacked `Access-Control-Allow-Origin` while localhost origins received ACAO — matching the SPA connect banner with Ready pods.

Post-merge expectation: after Flux applies `ALLOWED_ORIGINS` / `PUBLIC_APP_URL`, §6 MUST show ACAO for `https://pn.nacfson.cloud`. Live `kubectl set env` is blocked by `gitops-managed-namespaces` (Git merge required).

## Constitution

Principle VII / Amendment 2.1.0 expanded for browser origin allowlist; maintainer approval still required before treating the constitution bump as binding governance.

**Amendment 2026-10-13**: FE `startupProbe` path changed from `/healthz` to `/api/auth/language-options` (auth-chain mismatch coverage). Readiness is unchanged on `/healthz`. Static check: `rg -n "language-options" deploy/projects/pn/workloads/frontend-deployment.yaml`. Live public checks: `scripts/smoke-auth-chain.sh`.
