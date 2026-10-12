# Contract: Browser API Origin Access (CORS Allowlist)

**Purpose**: Define the mandatory configuration and validation for Project PN **browser → public `/api` → backend** access, which is **not** proved by FE `startupProbe` / `readinessProbe` on `/healthz`.

**Scope**:

- `deploy/projects/pn/workloads/backend-deployment.yaml` env
- `clusters/vps-k3s/projects/proj-pn.yaml` BE env patches
- `clusters/local-k3s/projects/proj-pn.yaml` BE env patches

**Non-goals**: Changing IngressRoute path split; widening NetworkPolicy; rebuilding images; guaranteeing arbitrary client networks/extensions; encoding ACAO checks into kubelet `httpGet` readiness.

## Why this exists

Proven production failure:

1. FE→BE cluster `/healthz` probes succeeded (pods Ready).
2. Public `/api/auth/me` returned HTTP 401 to server-side curl.
3. SPA still showed “Could not connect to the backend server.”
4. Root cause: browser `fetch` sends non-safelisted headers (`Cache-Control`, `Pragma`) → CORS **preflight**; `Origin: https://pn.nacfson.cloud` was **not** in backend `ALLOWED_ORIGINS` (defaults were localhost/Tauri) → no `Access-Control-Allow-Origin` → browser treats as network failure (UI non-401 path).

```text
Ready cluster hop ≠ browser API success
```

## Required declarations

| Variable | Meaning | Rule |
| --- | --- | --- |
| `PUBLIC_APP_URL` | Canonical public app origin for the environment (scheme + host, no trailing slash required after trim) | MUST equal the environment's public SPA door origin |
| `ALLOWED_ORIGINS` | Comma-separated CORS allowlist consumed by the backend | MUST include `PUBLIC_APP_URL`; MAY include approved local/dev origins |

### Environment mapping

| Environment | `PUBLIC_APP_URL` | `ALLOWED_ORIGINS` MUST include |
| --- | --- | --- |
| VPS (`clusters/vps-k3s`) | `https://pn.nacfson.cloud` | `https://pn.nacfson.cloud` |
| local (`clusters/local-k3s`) | `https://pn.example.com` | `https://pn.example.com` |

Base Deployment MAY list both public hosts plus localhost/Tauri defaults; Flux patches MUST set the environment-correct `PUBLIC_APP_URL` and a matching allowlist.

## Validation contract (SC-009)

Against the **public** API door (preferred) or equivalent backend URL:

```bash
ORIGIN="$PUBLIC_APP_URL"   # e.g. https://pn.nacfson.cloud
API="$ORIGIN/api/auth/me"

curl -skS -D- -o /dev/null -X OPTIONS \
  -H "Origin: $ORIGIN" \
  -H "Access-Control-Request-Method: GET" \
  -H "Access-Control-Request-Headers: content-type,cache-control,pragma" \
  "$API"
```

**Pass**: Response includes `access-control-allow-origin` equal to `$ORIGIN` (case-insensitive header name).

**Fail**: Missing ACAO for that Origin while cluster probes are green → feature non-compliant.

Optional follow-up GET with `Origin: $ORIGIN` SHOULD also reflect ACAO when CORS middleware applies.

## Invariants

1. FE Ready on `/healthz` is **necessary** for cluster hop and **not sufficient** for this contract.
2. Removing the public origin from `ALLOWED_ORIGINS` MUST be treated as a regression even if pods stay Ready.
3. Door Host rules may change via existing Flux IngressRoute patches; when the public SPA host changes, `PUBLIC_APP_URL` / `ALLOWED_ORIGINS` MUST change in the same revision set.
4. Status/probe paths remain subject to the public-surface allowlist; this contract does **not** authorize exposing Keycloak or platform-admin health publicly.

## Acceptance checks

- Backend Deployment (rendered for each env) contains `PUBLIC_APP_URL` and `ALLOWED_ORIGINS` with the public origin included.
- Quickstart §6 (or CI equivalent) records a passing CORS preflight for that origin.
- Spec/plan/operator docs state FE Ready ≠ browser API success (SC-010).
