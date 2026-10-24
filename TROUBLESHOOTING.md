# Platform Operations & Incident Troubleshooting Guide (`TROUBLESHOOTING.md`)

This runbook documents production incidents, root causes, diagnostic commands, and permanent fixes for `nacfson_pipeline` on `vps-k3s` (Oracle Cloud 2-OCPU ARM64) and `local-k3s`.

---

## 1. Project Pods Unreachable (`503 Service Unavailable` on `https://pn.nacfson.cloud`) & `pn-backend` Stuck in `0/1 Pending`

### Symptoms
- Visiting `https://pn.nacfson.cloud` returns HTTP `503 Service Unavailable`.
- `kubectl get pods -n proj-pn` shows:
  - `pn-backend`: `0/1 Pending`
  - `pn-frontend`: `0/1 Running` (readiness probe failing with `502 Bad Gateway` / `Connection refused` to `pn-backend:8080`)
- `kubectl describe pod -n proj-pn -l app.kubernetes.io/name=pn-backend` reports:
  ```text
  Warning  FailedScheduling  default-scheduler  0/1 nodes are available: 1 Insufficient cpu.
  ```

### Root Cause
- The Oracle Cloud `vps-k3s` node (`oracle-free`) has **2 OCPUs (`2000m` total allocatable CPU)**.
- When `observability` (`fluent-bit: 50m` + `victorialogs: 100m` = `150m`), `proj-health` (`300m`), and `vault-ai-proxy` (`25m`) were added alongside `proj-pn` (`300m` backend + `100m` frontend = `400m`), aggregate cluster CPU requests reached **`2135m / 2000m`** (`135m` over the node's physical CPU capacity).
- Whichever backend pod rolled last (`pn-backend`) could not schedule because only `165m` CPU remained free on the node.
- Because `pn-backend` was `Pending`, `pn-frontend`'s continuous cluster-hop `readinessProbe` (`/healthz` ➔ `pn-backend:8080`) marked `pn-frontend` `Ready=False`, and Traefik withheld routes (`503`).

### Diagnostic Commands
```bash
# 1. Check pod scheduling status across all namespaces
ssh oracleCloud "sudo k3s kubectl get pods -A"

# 2. Check node CPU/memory request saturation against the 2000m ceiling
ssh oracleCloud "sudo k3s kubectl describe node | grep -A 35 'Non-terminated Pods:'"
```

### Permanent Fix
- Right-size tenant project Guaranteed QoS CPU requests (`requests.cpu == limits.cpu`) to fit within the 2-OCPU (`2000m`) physical envelope:
  - [`deploy/projects/pn/workloads/backend-deployment.yaml`](deploy/projects/pn/workloads/backend-deployment.yaml): `cpu: "150m"` (memory remains `512Mi`)
  - [`deploy/projects/pn/workloads/frontend-deployment.yaml`](deploy/projects/pn/workloads/frontend-deployment.yaml): `cpu: "50m"` (memory remains `128Mi`)
  - [`deploy/projects/health/workloads/backend-deployment.yaml`](deploy/projects/health/workloads/backend-deployment.yaml): `cpu: "150m"`, `maxSurge: 0`, `maxUnavailable: 1`
  - [`deploy/projects/health/workloads/frontend-deployment.yaml`](deploy/projects/health/workloads/frontend-deployment.yaml): `cpu: "50m"`, `maxSurge: 0`, `maxUnavailable: 1`
- This reduces aggregate cluster CPU requests from `2135m` to **`1835m / 2000m`**, leaving `165m` free CPU headroom for Jobs (`postgres-init-job`, `postgres-daily-backup`, `openbao-daily-snapshot`).

---

## 2. `scripts/open-observe.sh` Tunnel Exits, Missing AI Gateway UI, or Empty Logs

### Symptoms
- Running `./scripts/open-observe.sh` either:
  1. Exits unexpectedly (`Error: a tunnel exited.`) when `kubectl port-forward` drops an idle stream,
  2. Fails on startup with `Error: API tunnel port 16443 is already in use.` when a previous tunnel is still bound,
  3. Shows only `pn-frontend` `502` probe errors in VictoriaLogs (`http://127.0.0.1:9428/select/vmui/`) because `pn-backend` is `Pending`, or
  4. Does not expose the Internal AI Gateway UI (`http://127.0.0.1:18081/`).

### Root Cause
1. Previously, `open-observe.sh` used `wait -n` and immediately terminated the entire session whenever any child `kubectl port-forward` closed after an idle timeout or pod rollout.
2. `open-observe.sh` only forwarded Hubble UI (`:12000`) and VictoriaLogs (`:9428`), omitting `vault/deploy/vault-ai-proxy` (`:18081`).

### Permanent Fix & Usage
[`scripts/open-observe.sh`](scripts/open-observe.sh) now forwards all **3 platform UIs** over the single `127.0.0.1:16443` SSH API tunnel and automatically restarts any dropped `kubectl port-forward` child process:
- **Hubble UI**: `http://127.0.0.1:12000`
- **VictoriaLogs**: `http://127.0.0.1:9428/select/vmui/`
- **AI Gateway UI**: `http://127.0.0.1:18081/`

If a previous session left ports bound, free them before starting:
```bash
fuser -k 16443/tcp 12000/tcp 9428/tcp 18081/tcp 2>/dev/null || true
./scripts/open-observe.sh
```

---

## 3. `database` Flux Layer Stalled (`Job/identity/postgres-init-job status: Failed`)

### Symptoms
- `kubectl get kustomizations -n flux-system` shows:
  ```text
  database   False   health check failed: [Job/identity/postgres-init-job status: 'Failed']
  identity   False   dependency 'flux-system/database' is not ready
  ingress    False   dependency 'flux-system/identity' is not ready
  tls        False   dependency 'flux-system/ingress' is not ready
  ```

### Root Cause
- When a new project slug is added to `PROJECT_SLUGS` (e.g., `pn,health` in [`deploy/platform/database/postgres-init-job.yaml`](deploy/platform/database/postgres-init-job.yaml)), `postgres-init-job` queries OpenBao for `kv/data/database/<slug>-user`.
- If that secret has not been seeded into OpenBao yet (or if `postgres-0`'s `postgres` password is out of sync with `kv/database/postgres-admin`), `postgres-init-job` fails after `backoffLimit: 5` and remains in `Failed` state until re-seeded and recreated.

### Permanent Fix
Run [`scripts/vault-seed-inventory.sh`](scripts/vault-seed-inventory.sh) (or [`scripts/vault-reset-unseal-key.sh`](scripts/vault-reset-unseal-key.sh) if the Unseal Key was lost), then recreate the job:
```bash
./scripts/vault-seed-inventory.sh
ssh oracleCloud "sudo k3s kubectl --as=system:admin --as-group=platform:break-glass delete job -n identity postgres-init-job --ignore-not-found"
kubectl kustomize deploy/platform/database | ssh oracleCloud "sudo k3s kubectl --as=system:admin --as-group=platform:break-glass apply -f -"
```

---

## 4. Lost OpenBao Unseal Key & Root Token Recovery (Zero PostgreSQL Data Loss)

### Symptoms
- Both the OpenBao Shamir Unseal Key and Root Token are lost, preventing secret updates or unsealing after a restart.

### Root Cause & Why PostgreSQL Data Is Safe
- OpenBao stores only **role passwords** (`kv/database/*`), not PostgreSQL tables or rows.
- Inside `identity/postgres-0`, local container Unix socket connections (`psql -U postgres` with no `-h` flag) are trusted by PostgreSQL for the local `postgres` user without requiring the old password.

### Permanent Fix
Run the automated zero-data-loss recovery script:
```bash
./scripts/vault-reset-unseal-key.sh
```
1. Wipes only `/var/lib/openbao/data/vault.db` and `/var/lib/openbao/data/raft` inside `vault/openbao-0`.
2. Initializes OpenBao (`bao operator init`), prints the **New Unseal Key** (at both `[2/6]` and the final banner), and unseals `openbao-0`.
3. Re-applies least-privilege K8s Auth roles ([`scripts/vault-configure-k8s-auth.sh`](scripts/vault-configure-k8s-auth.sh)) and seeds fresh internal passwords ([`scripts/vault-seed-inventory.sh`](scripts/vault-seed-inventory.sh) `--force`).
4. Syncs the new `postgres` admin password over `postgres-0`'s local Unix socket (`ALTER ROLE postgres WITH PASSWORD ...`), runs `postgres-init-job` to update `keycloak_user`, `user_pn`, `user_health`, and `backup_role`, syncs rotated OIDC client secrets (`gateway-client`, `session-revocation-client`, `project-pn`, `project-health`) into the `keycloak` PostgreSQL `client` table, and preserves existing Google OAuth credentials (`identity_provider_config`) with **zero database data loss**.
5. Revokes the initial Root Token via an `EXIT` trap.

---

## 5. `{"error":"unsigned_in","message":"Sign-in service unreachable"}` After OpenBao Reset or Secret Rotation

### Symptoms
- During Google OIDC sign-in on `https://pn.nacfson.cloud`, redirecting back to `https://auth.nacfson.cloud/oauth/callback?code=...&state=...` returns HTTP `503 Service Unavailable`:
  ```json
  {"error":"unsigned_in","message":"Sign-in service unreachable"}
  ```
- `kubectl logs -n identity deploy/keycloak` shows:
  ```text
  INFO  [org.keycloak.exportimport.util.ImportUtils] (main) Realm 'platform' already exists. Import skipped
  WARN  [org.keycloak.events] type="CODE_TO_TOKEN_ERROR", clientId="gateway-client", error="invalid_client_credentials", grant_type="authorization_code"
  ```

### Root Cause
- In Quarkus Keycloak 24 (`kc.sh start --import-realm`), `--import-realm` always uses `Strategy: IGNORE_EXISTING` and skips importing `platform-realm.json` when the `platform` realm already exists in PostgreSQL (`identity/postgres-0`).
- When OpenBao secrets (`kv/gateway/client-secret`, `kv/gateway/session-revocation-secret`, `kv/projects/pn/client-secret`, `kv/projects/health/client-secret`) are regenerated by `vault-reset-unseal-key.sh` or `vault-seed-inventory.sh --force`:
  1. `identity/gateway` restarts and loads the **new** `kv/gateway/client-secret` from OpenBao.
  2. `identity/keycloak` restarts, fetches the new secret into `/opt/keycloak/conf/secrets/gateway_client_secret`, **but skips realm import**, leaving the **old** `secret` in PostgreSQL's `keycloak.client` table (and Infinispan's realm cache).
  3. When `gateway` calls Keycloak's `/protocol/openid-connect/token` endpoint (`ExchangeCode` in [`gateway/internal/auth/handler.go`](gateway/internal/auth/handler.go)), Keycloak rejects `gateway-client` with `invalid_client_credentials`, and `gateway` fails closed with `{"error":"unsigned_in","message":"Sign-in service unreachable"}`.

### Permanent Fix
- [`scripts/vault-reset-unseal-key.sh`](scripts/vault-reset-unseal-key.sh) (Step `[5/6]`) now automatically syncs the newly generated OpenBao client secrets (`gateway-client`, `session-revocation-client`, `project-pn`, `project-health`) directly into PostgreSQL's `keycloak.client` table (`identity/postgres-0`), preserves existing Google OAuth credentials from `keycloak.identity_provider_config` into OpenBao `kv/identity/google-oauth`, and restarts `deployment/keycloak` so Infinispan reloads the updated secrets.
- [`scripts/vault-set-google-oauth.sh`](scripts/vault-set-google-oauth.sh) also syncs updated Google OAuth `clientId` and `clientSecret` into `keycloak.identity_provider_config` and restarts `deployment/keycloak`.

---

## 6. `scripts/ai-gateway-ctl.sh set-key` Returning `400` or `/bin/sh: -c requires an argument` Over SSH

### Symptoms
- Running `./scripts/ai-gateway-ctl.sh set-key` against a remote VPS (`KUBE_EXEC="ssh oracleCloud sudo k3s kubectl"`) returned `curl: (22) The requested URL returned error: 400` on the primary path and `/bin/sh: -c requires an argument` on the fallback path.

### Root Cause
1. **Background `ssh` consuming `stdin`**: Inside `with_loopback_tunnel`, launching `ssh -L ... &` without `-n </dev/null` caused `ssh` to drain the piped `{"api_key":"..."}` JSON from `stdin` before `curl --data-binary @-` could read it, sending an empty body (`400 Bad Request`).
2. **Remote quote stripping**: Passing `/bin/sh -c '...'` through `KUBE_EXEC="ssh oracleCloud ..."` caused `ssh` to strip outer quotes on the remote shell.

### Permanent Fix
- [`scripts/ai-gateway-ctl.sh`](scripts/ai-gateway-ctl.sh) now passes `-n </dev/null` to background `ssh` / `kubectl port-forward` processes so `stdin` is preserved exclusively for `curl --data-binary @-`.
- [`scripts/ai-gateway-ctl.sh`](scripts/ai-gateway-ctl.sh), [`scripts/vault-reset-unseal-key.sh`](scripts/vault-reset-unseal-key.sh), and [`scripts/vault-configure-k8s-auth.sh`](scripts/vault-configure-k8s-auth.sh) stream inputs directly via `exec -i` (`stdin`) without nested `/bin/sh -c` strings.
