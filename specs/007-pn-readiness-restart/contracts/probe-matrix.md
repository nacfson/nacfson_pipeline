# Contract: Project PN Probe Matrix (Cluster Hop)

**Scope**: `deploy/projects/pn/workloads/frontend-deployment.yaml`, `backend-deployment.yaml`, `deploy/platform/vault-proxies/db-proxy.yaml`  
**Canonical Contracts**: [`contracts/CONTRACT_PROBES_READINESS.md`](../../../contracts/CONTRACT_PROBES_READINESS.md), [`contracts/CONTRACT_DATABASE.md`](../../../contracts/CONTRACT_DATABASE.md)

**Non-goals**: Image rebuilds, NetworkPolicy changes, IngressRoute changes, notification sinks, asserting browser CORS from kubelet probes (see [browser-api-access.md](./browser-api-access.md)).

## Matrix

| Workload | Role | Mechanism | Path | Port | Pass means | Fail means |
| --- | --- | --- | --- | --- | --- | --- |
| `pn-frontend` | startup | `httpGet` | `/api/auth/language-options` | `80` | FE reached the BE auth router through nginx `location /api/` (**cluster hop incl. `/api/` proxy**) | In-window: keep Starting; **threshold exhausted: kill + restart** |
| `pn-frontend` | readiness | `httpGet` | `/healthz` | `80` | Connected FE→BE on cluster hop (disconnect signal clear) | NotReady = cluster unconnected; **process NOT restarted**; **does not prove CORS** |
| `pn-frontend` | liveness | `httpGet` | `/` | `80` | Local site process up | Kill + restart |
| `pn-backend` | readiness | `httpGet` | `/readyz` | `8080` | End-to-end DB dependency (`BE -> vault-db-proxy -> proj_pn`) reachable | NotReady for API; **process NOT restarted** |
| `pn-backend` | liveness | `httpGet` | `/healthz` | `8080` | Backend process up (DB-free) | Kill + restart |
| `vault-db-proxy` | readiness | `httpGet` | `/readyz` | `8080` | Upstream PostgreSQL (`postgres-service.identity.svc:5432`) TCP reachable | NotReady for DB proxy; **process NOT restarted** |
| `vault-db-proxy` | liveness | `httpGet` | `/healthz` | `8080` | Proxy process up (DB-free) | Kill + restart |

## Timing (declared defaults)

| Probe | `periodSeconds` | `timeoutSeconds` | `failureThreshold` | Notes |
| --- | --- | --- | --- | --- |
| FE startup | `5` | `3` | `36` | ≈180s boot window |
| FE readiness | `5` | `3` | `3` | Cluster disconnect visible ~15s after sustained fail |
| FE liveness | `10` | default/`3` | default/`3` | Process-local |
| BE readiness | `5` | `3` | `3` | Dependency readiness (`vault-db-proxy` -> `proj_pn`) |
| BE liveness | `10` | default/`3` | default/`3` | Process-local |
| DB Proxy readiness | `5` | default/`3` | default/`3` | Upstream PostgreSQL TCP reachability (`/readyz`) |
| DB Proxy liveness | `10` | default/`3` | default/`3` | Process-local (`/healthz`) |

## Restart policy

```yaml
spec:
  template:
    spec:
      restartPolicy: Always
```

Required on both frontend and backend Pod templates.

## Invariants

1. FE readiness MUST target the process-availability hop (`/healthz` on FE port 80). FE startup MUST target a public, DB-free `/api/...` route (`/api/auth/language-options`) so it also proves the nginx `/api/` proxy (amended 2026-10-13; was: same path as readiness).
2. FE liveness MUST NOT target `/healthz` or `/readyz`.
3. BE readiness MUST be `/readyz`, not `/healthz`.
4. Probes MUST remain pod-local kubelet checks (not public door requirements).
5. Candidates that add NodePort/LoadBalancer, new egress peers, or privileged probe sidecars are rejected.
6. FE Ready MUST NOT be documented or automated as proof of browser `/api` CORS success ([browser-api-access.md](./browser-api-access.md)).

## Acceptance checks

- Frontend Deployment YAML contains `startupProbe.httpGet.path: /api/auth/language-options` and `readinessProbe.httpGet.path: /healthz` and `livenessProbe.httpGet.path: /`.
- Backend Deployment YAML contains `readinessProbe.httpGet.path: /readyz` and `livenessProbe.httpGet.path: /healthz`.
- Both declare `restartPolicy: Always`.
- `ingress-route.yaml` and `network-policy.yaml` are unchanged by this feature.

## Auth-chain mismatch coverage (amendment 2026-10-13)

Probes alone passed while sign-in was broken. What each layer can and cannot see:

| Link | Covered by | Notes |
| --- | --- | --- |
| nginx `/healthz` -> backend | FE readiness | Separate nginx block from `/api/` |
| nginx `/api/` -> backend auth router | **FE startup** (`/api/auth/language-options`) | Boot-time only; it does not run again after the first success |
| Traefik public route to `/oauth/start`, gateway reachable, return host accepted | `scripts/smoke-auth-chain.sh` | Outside the pod: kubelet cannot test it |
| Public `/api/auth/me` answers 401 JSON (not 404 or 5xx) | `scripts/smoke-auth-chain.sh` | |
| SPA treats a 401 as signed-out | Project_PN frontend tests | Not an infrastructure probe |

Non-goals stay: no probe sidecars and no public-door probes (Invariants 4 and 5).
