# Quickstart: Validate 007 Probe Matrix, Disconnect Signal, and Browser API Access

**Prerequisites**: Cluster with `proj-pn` reconciled; `kubectl` context working; ability to port-forward or exec per break-glass norms. Contracts: [probe-matrix.md](./contracts/probe-matrix.md), [disconnect-signal.md](./contracts/disconnect-signal.md), [browser-api-access.md](./contracts/browser-api-access.md).

## 0. Manifest static checks (no cluster required)

From repo root:

```bash
rg -n "startupProbe|readinessProbe|livenessProbe|restartPolicy|ALLOWED_ORIGINS|PUBLIC_APP_URL" \
  deploy/projects/pn/workloads/frontend-deployment.yaml \
  deploy/projects/pn/workloads/backend-deployment.yaml \
  clusters/vps-k3s/projects/proj-pn.yaml \
  clusters/local-k3s/projects/proj-pn.yaml
```

**Expect**:

| File | Must show |
| --- | --- |
| frontend | `restartPolicy: Always`; startup `/healthz`; readiness `/healthz`; liveness `/` |
| backend | `restartPolicy: Always`; readiness `/readyz`; liveness `/healthz`; `ALLOWED_ORIGINS`; `PUBLIC_APP_URL` |
| vps patch | `PUBLIC_APP_URL=https://pn.nacfson.cloud` and that origin in `ALLOWED_ORIGINS` |
| local patch | `PUBLIC_APP_URL=https://pn.example.com` and that origin in `ALLOWED_ORIGINS` |

Confirm door and policy untouched:

```bash
git diff -- deploy/projects/pn/workloads/ingress-route.yaml \
  deploy/projects/pn/boundary/network-policy.yaml
```

**Expect**: empty diff for this feature.

## 1. Happy path status hops

```bash
kubectl -n proj-pn port-forward svc/pn-frontend 8081:80 &
kubectl -n proj-pn port-forward svc/pn-backend 8082:8080 &
curl -fsS http://127.0.0.1:8081/ | head -c 20
curl -fsS http://127.0.0.1:8081/healthz
curl -fsS http://127.0.0.1:8082/healthz
curl -fsS http://127.0.0.1:8082/readyz
```

**Expect**: FE `/` returns site bytes; FE `/healthz` and BE `/healthz` return ok; BE `/readyz` returns ready when DB path works.

```bash
kubectl -n proj-pn get pods -l 'app.kubernetes.io/part-of=project-pn' -o wide
kubectl -n proj-pn get endpoints pn-frontend pn-backend
```

**Expect**: Both pods Ready; both Endpoints populated. **Remember**: this does **not** prove browser CORS (see §6).

## 2. Frontend disconnect signal (post-startup, cluster hop)

With FE already Ready, scale backend to zero (or block BE) briefly:

```bash
kubectl -n proj-pn scale deploy/pn-backend --replicas=0
# wait > readiness failure window (~15s with failureThreshold 3 * 5s)
kubectl -n proj-pn get pods -l app.kubernetes.io/name=pn-frontend
kubectl -n proj-pn describe pod -l app.kubernetes.io/name=pn-frontend | sed -n '/Conditions:/,/Volumes:/p'
kubectl -n proj-pn get endpoints pn-frontend
kubectl -n proj-pn get events --field-selector involvedObject.kind=Pod --sort-by=.lastTimestamp | tail -20
```

**Expect**:

- FE pod Running but **Ready False** (or 0/1).
- `pn-frontend` Endpoints empty.
- Events show readiness probe failures on `/healthz`.
- FE **restart count does not climb** solely due to readiness (alive `/` still passes).

Restore:

```bash
kubectl -n proj-pn scale deploy/pn-backend --replicas=1
# wait < 60s
kubectl -n proj-pn get pods -l app.kubernetes.io/name=pn-frontend
kubectl -n proj-pn get endpoints pn-frontend
```

**Expect**: FE Ready again; Endpoints restored without manual pod delete.

## 3. Backend dependency readiness

With backend process up but data path broken (env-specific: stop vault-db-proxy or Postgres in a lab only):

```bash
curl -fsS -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8082/healthz
curl -fsS -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8082/readyz || true
kubectl -n proj-pn get pods -l app.kubernetes.io/name=pn-backend
kubectl -n proj-pn get endpoints pn-backend
```

**Expect**: `/healthz` 200; `/readyz` 503; backend NotReady; Endpoints for `pn-backend` empty; no restart storm from readiness alone.

## 4. Startup gate (cold FE while BE down)

```bash
kubectl -n proj-pn scale deploy/pn-backend --replicas=0
kubectl -n proj-pn rollout restart deploy/pn-frontend
kubectl -n proj-pn get pods -l app.kubernetes.io/name=pn-frontend -w
```

**Expect**: FE stays Starting / not Ready while the startup window is open. If BE stays down past `failureThreshold` (≈180s), the FE container is **killed and restarted** (startup-failure restart; restart count increases). This is not a readiness-only disconnect event.

```bash
kubectl -n proj-pn scale deploy/pn-backend --replicas=1
```

**Expect**: FE startup succeeds and becomes Ready without door manifest changes.

## 5. Constitution draft note

Principle VII / Amendment 2.1.0 live in `.specify/memory/constitution.md` as a **working-tree draft** covering cluster hop **and** browser origin allowlist. Do not treat them as committed governance until maintainer approval.

## 6. Browser API CORS allowlist (public door)

Set origin to the environment public app URL:

```bash
# VPS example:
ORIGIN=https://pn.nacfson.cloud
API=$ORIGIN/api/auth/me

echo "=== preflight ==="
curl -skS -D- -o /dev/null -X OPTIONS \
  -H "Origin: $ORIGIN" \
  -H "Access-Control-Request-Method: GET" \
  -H "Access-Control-Request-Headers: content-type,cache-control,pragma" \
  "$API" | tr -d '\r' | rg -i 'HTTP/|access-control-allow-origin|vary'

echo "=== GET with Origin ==="
curl -skS -D- -o /dev/null \
  -H "Origin: $ORIGIN" \
  -H "Cache-Control: no-cache" \
  -H "Pragma: no-cache" \
  "$API" | tr -d '\r' | rg -i 'HTTP/|access-control-allow-origin|content-type'
```

**Expect**:

- OPTIONS and/or GET show `access-control-allow-origin: $ORIGIN`.
- Missing ACAO while FE/BE Ready ⇒ **fail SC-009** (this is the production failure class).

Also confirm live env:

```bash
kubectl -n proj-pn get deploy pn-backend -o jsonpath='{range .spec.template.spec.containers[0].env[*]}{.name}={.value}{"\n"}{end}' \
  | rg 'ALLOWED_ORIGINS|PUBLIC_APP_URL'
```

**Expect**: `PUBLIC_APP_URL` equals `$ORIGIN`; `ALLOWED_ORIGINS` contains that value.

## Pass criteria mapping

| SC | Quickstart section |
| --- | --- |
| SC-001 / SC-001b | §4 |
| SC-002 / SC-006 / SC-007 | §2 |
| SC-003 | §3 |
| SC-004 | §2 restore |
| SC-005 / SC-008 | §0 |
| SC-009 | §6 |
| SC-010 | §1 note + §5 + contracts |
