# Platform Operations & Incident Troubleshooting Guide (`TROUBLESHOOTING.md`)

This runbook documents production incidents, root causes, diagnostic commands, and permanent fixes for `nacfson_pipeline` on `vps-k3s` (Oracle Cloud 2-OCPU ARM64) and `local-k3s`, including OAuth sign-in failures between project backends and the platform gateway (§10).

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

---

## 7. Signed Out After a Break, and Sign-In Asks Google Again

### Symptoms
- Sign-in succeeds, but after a while the next action in `https://pn.nacfson.cloud` fails with `401` (`session rejected by gateway`) and the app shows the signed-out state.
- Signing in again goes through Google instead of returning silently.
- `kubectl logs -n identity deploy/keycloak` shows, at the time of the sign-out:
  ```text
  WARN  [org.keycloak.events] type="INTROSPECT_TOKEN_ERROR", clientId="gateway-client", error="token_introspection_failed", reason="... JWT check failed"
  WARN  [org.keycloak.events] type="REFRESH_TOKEN_ERROR", clientId="gateway-client", error="invalid_token", reason="Token is not active", grant_type="refresh_token"
  ```

### Root Cause
- `Token is not active` means the refresh token sealed in the session handle had passed its expiry. Keycloak and the client secret are working.
- The gateway refreshes only when a protected project API call arrives after the 5-minute access token has expired or has less than 30 seconds left. With no such call for one SSO idle period, the Keycloak session expires. Frontend-only activity does not count. See [`contracts/CONTRACT_AUTH_DOOR.md`](contracts/CONTRACT_AUTH_DOOR.md) §5.
- Until issue #93, the realm import set no lifespans and the live realm ran on the Keycloak 24 default idle of 30 minutes.

### Diagnosis
- Different reasons point elsewhere: `Stale token` (refresh-token rotation), `Session not active` (Keycloak restarted or the session was deleted), `invalid_client_credentials` (client secret drift between OpenBao and the Keycloak database).
- Read the live values with step 1 of [`docs/operations-runbook.md`](docs/operations-runbook.md) §7 (**Applying realm session lifetimes**).

### Permanent Fix
- The idle timeout is 30 minutes by design: the realm import declares `ssoSessionIdleTimeout=1800`, the same value the live realm runs. A sign-out after 30 minutes with no protected API call is expected. To change it, edit the realm import and contract, then apply it to the live realm with the runbook procedure, because startup import skips an existing realm (automatic sync: issue #119). Details: [`docs/issues/FIX_GithubIssue93.md`](docs/issues/FIX_GithubIssue93.md).

---

## 8. `blog-web` Image Rollout Stuck and `proj-blog` Kustomization Not Ready

### Symptoms
- A new `blog-web` image pin merges, but `https://blog.nacfson.cloud` keeps serving the old image.
- `kubectl get pods -n proj-blog` shows the old `blog-web` pod `Running` next to a new `blog-web` pod stuck in `Pending`.
- `kubectl describe pod -n proj-blog <pending-pod>` reports:
  ```text
  Warning  FailedScheduling  default-scheduler  0/1 nodes are available: 1 Insufficient cpu.
  ```
- `kubectl get kustomization -n proj-blog proj-blog` reports a health-check timeout, and Flux keeps retrying.

### Root Cause
- `blog-web` rolled out with `maxSurge: 1, maxUnavailable: 0`: start the new pod first, then stop the old one. During the swap the node must fit a second `100m` pod.
- §1 set `maxSurge: 0` on `proj-pn` and `proj-health`, but `proj-blog` was onboarded just before and kept `maxSurge: 1`. Steady-state CPU requests on the 2-OCPU node sit at about `1935m / 2000m`, so the extra `100m` pod cannot be scheduled.
- `maxUnavailable: 0` keeps the old pod serving, and `proj-blog` (`wait: true`, `timeout: 5m`) never becomes healthy.
- Preflight did not catch it: at the time, [`clusters/vps-k3s/capacity.yaml`](clusters/vps-k3s/capacity.yaml) recorded `4000m` allocatable CPU, so the surge fit on paper. It now records the real `2000m`.

### Diagnostic Commands
```bash
ssh oracleCloud "sudo k3s kubectl get pods -n proj-blog -o wide"
ssh oracleCloud "sudo k3s kubectl get events -n proj-blog --sort-by=.lastTimestamp | tail -20"
ssh oracleCloud "sudo k3s kubectl describe node | grep -A8 'Allocated resources'"
```

### Permanent Fix
- [`deploy/projects/blog/workloads/deployment.yaml`](deploy/projects/blog/workloads/deployment.yaml) now uses `maxSurge: 0, maxUnavailable: 1`: stop the old pod, then start the new one on the CPU it freed. Each rollout has a few seconds of downtime because `blog-web` runs a single replica.
- Every single-replica tenant on `vps-k3s` must use `maxSurge: 0` unless the node keeps spare CPU for the surge pod.
- Follow-up: correct `capacity.yaml` to the node's real allocatable CPU and memory so preflight rejects surge that does not fit.

---

## 9. Every Private Image Stuck in `ImagePullBackOff` (`403 Forbidden`) After Rotating the GHCR Token in OpenBao

### Symptoms
- Pods that use any private `ghcr.io/nacfson/*` image stay in `ImagePullBackOff`. On 2026-11-03 this hit `proj-health` (`health-backend`, `health-frontend`) and `identity/gateway` for about 22 hours.
- `kubectl describe pod` shows only `Back-off pulling image ...` and hides the real error.
- Writing a new PAT to OpenBao (`kv/platform/ghcr-pull-token`) changes nothing. Kubelet retries about every 5 minutes, and every retry fails.
- The k3s journal shows containerd requesting a token from `ghcr.io` directly and being refused:
  ```text
  failed to authorize: failed to fetch oauth token: unexpected status from GET request to
  https://ghcr.io/token?scope=repository%3Anacfson%2Fhealth-backend%3Apull&service=ghcr.io: 403 Forbidden
  ```

### Root Cause
- On `vps-k3s` (`oracle-free`), containerd authenticates to GHCR with a **static PAT in `/etc/rancher/k3s/registries.yaml`** (`configs: "ghcr.io": auth: {username, password}`). That file has no `mirrors:` section, so `vault-registry-proxy` is not in the pull path.
- The PAT in that file had been revoked on GitHub, so every token request returned `403`.
- k3s reads `registries.yaml` only at startup. After editing the file, you must restart k3s for the change to apply.
- Rotating the token in OpenBao cannot help, for two reasons:
  1. The node never reads OpenBao.
  2. `vault-registry-proxy` never reads OpenBao either. [`services/vault-proxies/cmd/registry-proxy/main.go`](services/vault-proxies/cmd/registry-proxy/main.go) loads `VAULT_ADDR` but never uses it. Its upstream token comes only from the `GHCR_PULL_TOKEN` env var, which [`deploy/platform/vault-proxies/registry-proxy.yaml`](deploy/platform/vault-proxies/registry-proxy.yaml) does not set, or from an in-memory `/admin/adoption-check` promotion.
- [`deploy/platform/vault/registries.yaml.template`](deploy/platform/vault/registries.yaml.template), spec 002 and [`docs/ghcr-token-rotation-runbook.md`](docs/ghcr-token-rotation-runbook.md) all describe an OpenBao → proxy → containerd mirror path. That path is not deployed on this node.

### Diagnostic Commands
Run these on the VPS host. They are read-only and redact secrets:
```bash
# 1. The real pull error (BackOff events hide it)
sudo journalctl -u k3s --since "30 min ago" | grep -iE "ghcr|ErrImagePull" | tail -5

# 2. Which credential path containerd uses: a `configs:` auth block, a `mirrors:` entry, or both
sudo sed -E 's/((password|token|auth|identitytoken)[^:]*:).*/\1 <redacted>/I' /etc/rancher/k3s/registries.yaml

# 3. Was the file edited after k3s started? If so, the edit has not been loaded yet
sudo ls -l --time-style=full-iso /etc/rancher/k3s/registries.yaml
systemctl show k3s -p ActiveEnterTimestamp
```
Reading the error:
- `403` from `https://ghcr.io/token`: GHCR rejected the credential (revoked or under-scoped PAT), or the request was anonymous for a private package.
- `404`: the digest does not exist in GHCR. This is a different problem; re-pin the image instead.

### Fix (applied 2026-11-03)
The edit and restart change the node, so the operator runs them on the VPS host:
```bash
# 1. Check the new PAT against GHCR first (hidden input). Expect 200.
#    GHCR needs a classic PAT with read:packages; fine-grained PATs do not work with the registry.
read -rs NEW_PAT
curl -s -o /dev/null -w '%{http_code}\n' -u "nacfson:$NEW_PAT" \
  "https://ghcr.io/token?scope=repository:nacfson/health-backend:pull&service=ghcr.io"
unset NEW_PAT

# 2. Back up the file, then replace only the `password:` value
sudo cp -p /etc/rancher/k3s/registries.yaml /etc/rancher/k3s/registries.yaml.bak-$(date +%Y%m%d)
sudoedit /etc/rancher/k3s/registries.yaml

# 3. Reload. The API server is briefly unavailable; running containers keep running.
sudo systemctl restart k3s

# 4. Verify with the exact pull containerd performs
sudo k3s crictl pull ghcr.io/nacfson/health-backend@sha256:<pinned-digest>

# 5. Retry stuck pods now instead of waiting for the 5-minute backoff
kubectl delete pod -n proj-health -l app.kubernetes.io/part-of=project-health
kubectl delete pod -n identity -l app.kubernetes.io/name=gateway
kubectl get pods -A | grep -vE "Running|Completed"   # expect only the header

# 6. The backup still contains the revoked PAT; delete it
sudo rm /etc/rancher/k3s/registries.yaml.bak-*
```

### Open Follow-up
Until the repo and the node agree, rotating the GHCR pull token on `vps-k3s` means editing `registries.yaml` and restarting k3s, as above. Writing to OpenBao alone does not rotate it. To make OpenBao the source of truth, three changes are needed, and together they meet Constitution Principle V (no secrets on the node):
1. `vault-registry-proxy` reads `kv/platform/ghcr-pull-token` from OpenBao.
2. The proxy is reachable from the host on `127.0.0.1:5000`.
3. The node's `registries.yaml` switches to the mirror template with no `configs:` block.

---

## 10. OAuth Callback Fails With `gateway rejected session handle with status 401` or `gateway returned invalid identity claims`

**Related**: [`README.md`](README.md) (Main-Domain Naming Rule), [`docs/architecture-auth-routing.md`](docs/architecture-auth-routing.md), [`docs/project-auth-verification.md`](docs/project-auth-verification.md)

### Symptoms

During the OAuth login callback (`GET /api/auth/callback?ticket=...`) on `Project PN`, the backend (`auth_handler.go:190-194` / `gateway_client.go:198-200`) logs one of two errors:

1. **`gateway rejected session handle with status 401`**
   ```text
   [AUTH_FAILURE] gateway session confirmation on callback failed from 10.42.0.1: gateway rejected session handle with status 401
   ```
   - **Who rejected it**: The internal **Platform Gateway** (`GET http://gateway.identity.svc:8080/auth` in [`gateway/internal/auth/handler.go`](gateway/internal/auth/handler.go)) returned `HTTP 401 Unauthorized`.

2. **`gateway returned invalid identity claims`**
   ```text
   [AUTH_FAILURE] gateway session confirmation on callback failed from 10.42.0.1: gateway returned invalid identity claims
   ```
   - **Who rejected it**: The **Platform Gateway** returned `HTTP 200 OK`, **but** the **Project Backend** (`parseConfirmedIdentityResponse()` in `gateway_client.go:198-200`) rejected the response headers/claims (`ident == nil`) because the audience (derived from host), issuer, subject, or email check failed.

### Root Cause in Plain Language

1. **Google sign-in itself succeeded**:
   The user signed in with Google, Google confirmed their identity to the platform's central login broker (Keycloak), and the platform Gatekeeper (`gateway`) created a valid login pass and handed a one-time ticket to your App (`Project PN`).
2. **Ticket exchange succeeded, but the immediate double-check failed**:
   Your App traded that one-time ticket for the login pass (Step 1 succeeded), and then immediately asked the Gatekeeper a second time to verify the pass and return the user's ID card (Step 2 failed).
3. **Why Step 2 failed (Two Root Causes)**:
   - **Cause A — Public Main Domain Name vs. Internal Server Name Mismatch**:
     - When you sign in through the browser, your login pass is stamped with your **public main domain name** (`pn.nacfson.cloud` $\rightarrow$ `project-pn`).
     - When your App checks that pass inside the cluster network, if the request uses an **internal server name** (`pn-backend` or `gateway.identity.svc`) instead of the configured public domain name (`pn.nacfson.cloud`), the derived name becomes `project-pn-backend` instead of `project-pn`.
     - Because `"project-pn-backend"` does not match `"project-pn"`, either the Gatekeeper rejects the pass (`401`), or the Gatekeeper returns `"project-pn"` and your App rejects it as a mismatch (`gateway returned invalid identity claims`).
   - **Cause B — Missing Email on the Internal Pass (Contract & Image Drift)**:
     - Your App requires three fields on the confirmed identity response: **Issuer**, **Subject (User ID)**, and **Email** (`X-User-Email`).
     - When the Gatekeeper does not include `X-User-Email` (either because the Gatekeeper container is running a pre-fix image digest or because `email` was only present in the OIDC ID token and not extracted), your App treats the `200 OK` response as incomplete and logs `gateway returned invalid identity claims`.

### Where It Fails in the OAuth 2.0 / OIDC Flow

In [Google's official OpenID Connect / OAuth 2.0 Web Server documentation](https://developers.google.com/identity/openid-connect/openid-connect), the server flow has **6 numbered steps**. Below is how `nacfson_pipeline` executes each numbered step across the Project Frontend, Project Backend, Platform Gateway, and Keycloak:

#### 1. Create an Anti-Forgery State Token & Set Authorization Parameters
* **Google Documentation Rule**: Generate a unique anti-forgery `state` token (and PKCE verifier/challenge) before redirecting the browser.
* **Platform Execution**:
  1. **1.1 (`pn-frontend` $\rightarrow$ `pn-backend`)**: The user clicks Sign In on `https://pn.nacfson.cloud`, calling `GET https://pn.nacfson.cloud/api/auth/login`.
  2. **1.2 (`pn-backend` $\rightarrow$ `Platform Gateway`)**: `pn-backend` redirects the browser to `GET https://auth.nacfson.cloud/oauth/start?return=https://pn.nacfson.cloud/api/auth/callback`.
  3. **1.3 (`Platform Gateway`)**: In [`gateway/internal/auth/handler.go`](gateway/internal/auth/handler.go), the Gateway generates a PKCE `verifier` and `challenge`, seals `StatePayload` (`target_url`, `nonce`, `ts`, `vd`) using AES-256-GCM into `state`, and sets the host-only `OIDC_AUTH_STATE` binding cookie.

#### 2. Send an Authentication Request to Google
* **Google Documentation Rule**: Redirect the user's browser to `https://accounts.google.com/o/oauth2/v2/auth` with `client_id`, `response_type=code`, `scope=openid email profile`, `redirect_uri`, and `state`.
* **Platform Execution**:
  1. **2.1 (`Platform Gateway` $\rightarrow$ `Keycloak`)**: The Gateway redirects the browser to `https://auth.nacfson.cloud/realms/platform/protocol/openid-connect/auth` with `client_id=gateway-client`, `redirect_uri=https://auth.nacfson.cloud/oauth/callback`, `state=<gateway_state>`, `code_challenge=<challenge>`, and `kc_idp_hint=google`.
  2. **2.2 (`Keycloak` $\rightarrow$ `Google`)**: Because `kc_idp_hint=google` is set, Keycloak ([`deploy/platform/identity/keycloak-realm-config.yaml`](deploy/platform/identity/keycloak-realm-config.yaml)) skips its own login screen, generates its broker `state`, and redirects the browser to `https://accounts.google.com/o/oauth2/v2/auth` with `redirect_uri=https://auth.nacfson.cloud/realms/platform/broker/google/endpoint`.

#### 3. Google Prompts User for Consent / Sign-In
* **Google Documentation Rule**: Google authenticates the user and prompts for account selection / consent.
* **Platform Execution**:
  1. **3.1 (`Browser` on `accounts.google.com`)**: The user selects their Google account directly on Google's sign-in page.

#### 4. Handle the OAuth 2.0 Server Response & Confirm Anti-Forgery State Token
* **Google Documentation Rule**: Google redirects the browser back to the registered `redirect_uri` with `?code=...&state=...`, and the receiving server confirms `state`.
* **Platform Execution**:
  1. **4.1 (`Google` $\rightarrow$ `Keycloak`)**: Google redirects the browser to `https://auth.nacfson.cloud/realms/platform/broker/google/endpoint?code=<google_code>&state=<kc_state>`.
  2. **4.2 (`Traefik` $\rightarrow$ `Keycloak`)**: Traefik ([`deploy/platform/ingress/ingress-allowlist.yaml`](deploy/platform/ingress/ingress-allowlist.yaml)) routes `/realms/platform/broker/` to `keycloak-service.identity.svc:8080`, where Keycloak validates `<kc_state>`.

#### 5. Exchange Code for Access Token and ID Token
* **Google Documentation Rule**: The confidential server sends a `POST` request to `https://oauth2.googleapis.com/token` to exchange `code` for tokens.
* **Platform Execution**:
  1. **5.1 (`Keycloak` $\leftrightarrow$ `Google`)**: Keycloak sends `<google_code>` and `${GOOGLE_CLIENT_SECRET}` to `https://oauth2.googleapis.com/token` and receives Google's `id_token` and `access_token`.
  2. **5.2 (`Keycloak` $\rightarrow$ `Platform Gateway`)**: On first Google SSO, Keycloak runs `google jit first broker login` and creates the local user (AUTH-01) before minting `<keycloak_code>`. Subsequent logins reuse `FederatedIdentity(google, sub)`. Keycloak then stores the root user session in PostgreSQL and redirects the browser to `https://auth.nacfson.cloud/oauth/callback?code=<keycloak_code>&state=<gateway_state>`.
  3. **5.3 (`Platform Gateway` $\leftrightarrow$ `Keycloak`)**: The Gateway verifies `<gateway_state>` and the PKCE `verifier`, then calls `POST http://keycloak-service.identity.svc:8080/realms/platform/protocol/openid-connect/token` ([`gateway/internal/keycloak/oidc.go`](gateway/internal/keycloak/oidc.go)) to exchange `<keycloak_code>` for a Keycloak JWT (`access_token`) and `refresh_token`.

#### 6. Obtain User Information from the Token & Authenticate the User
* **Google Documentation Rule**: Extract the verified user identity claims (`iss`, `sub`, `email`), provision/load the user in the app database, and establish the app session.
* **Platform Execution (Where the Failure Occurs)**:
  1. **6.1 (`Platform Gateway` $\rightarrow$ `pn-backend`)**: The Gateway seals `{sid, sub, iss, aud: "project-pn", tokens, rt}` into `v1.<session_handle>`, issues a 60-second single-use `ticket`, and redirects the browser to `https://pn.nacfson.cloud/api/auth/callback?ticket=<ticket>`.
  2. **6.2 (`pn-backend` $\leftrightarrow$ `Platform Gateway` `POST /auth/exchange`) — SUCCEEDS**:
     `pn-backend` exchanges `ticket` at `http://gateway.identity.svc:8080/auth/exchange` and receives `200 OK` with `{session_handle, subject, issuer, audience: "project-pn"}`.
  3. **6.3 (`pn-backend` $\leftrightarrow$ `Platform Gateway` `GET /auth`) — FAILS HERE (`auth_handler.go:190-194`)**:
     `pn-backend` immediately calls `GET http://gateway.identity.svc:8080/auth` (`ConfirmSessionHandle`) with `Authorization: Bearer <session_handle>`:
     - **Failure Mode 1 (`401`)**: `HandleForwardAuth` rejects the handle if `X-Forwarded-Host` resolves to an internal host instead of `pn.nacfson.cloud`, or if Keycloak `/token/introspect` returns `active: false` (e.g., when `KC_HOSTNAME_URL` was not set on Keycloak).
     - **Failure Mode 2 (`gateway returned invalid identity claims`)**: `HandleForwardAuth` returns `200 OK`, but `parseConfirmedIdentityResponse(resp, effectiveHost)` in `gateway_client.go:198-200` returns `ident == nil` because `effectiveHost` was `"pn-backend"` (deriving `"project-pn-backend"` $\neq$ `"project-pn"`) or `X-User-Email` was missing from the response headers.
  4. **6.4 (`pn-backend` $\rightarrow$ `pn-frontend`)**:
     Once 6.2/6.3 succeeds, `pn-backend` redirects to `https://pn.nacfson.cloud/?code=<30s_pickup_code>`, and `pn-frontend` exchanges `code` at `POST /api/auth/session` for `{session_handle, user, requirements}`.

### Technical Breakdown of the Two Failure Modes

| Failure Mode | Code Location | Exact Condition Triggering the Error |
| :--- | :--- | :--- |
| **`gateway rejected session handle with status 401`** | [`gateway/internal/auth/handler.go`](gateway/internal/auth/handler.go) (`HandleForwardAuth`) | 1. **Host/Audience mismatch (`handler.go:150-154`)**: `deriveProjectAudience(targetHost)` does not match `session.Audience` (`"project-pn"`) because `X-Forwarded-Host` was missing or set to an internal service name.<br/>2. **Keycloak Introspection (`handler.go:165-209`)**: `/token/introspect` returned `{"active": false}` and token refresh failed (fixed by setting `KC_HOSTNAME_URL` and `KC_HOSTNAME_STRICT_BACKCHANNEL=false` on Keycloak).<br/>3. **Issuer mismatch (`handler.go:145-148`)**: `session.Issuer` did not match `KEYCLOAK_PUBLIC_URL` or `KEYCLOAK_ISSUER_URL`. |
| **`gateway returned invalid identity claims`** | `Project_PN` `gateway_client.go:198-200` (`parseConfirmedIdentityResponse`) | `GET /auth` returned `200 OK`, but `parseConfirmedIdentityResponse(resp, effectiveHost)` returned `nil` because:<br/>1. **Audience mismatch**: `DeriveProjectAudience(effectiveHost)` derived `"project-pn-backend"` from an internal `r.Host` instead of `"project-pn"` from `pn.nacfson.cloud`.<br/>2. **Missing `X-User-Email`**: Gateway container was pinned to a pre-PR-#42 digest (`sha256:3a9d08b3...`) instead of the post-PR-#42 digest (`sha256:55ae4634...`), or `email` was not present in the access token.<br/>3. **Missing/invalid `Authorization: Bearer <jwt>`, `X-User-Subject`, or `X-User-Issuer`**. |

### Permanent Prevention Rules

To guarantee this issue cannot recur when adding new projects or updating services, enforce these four rules:

#### Rule 1 (Primary): Domain-Based Naming Rule (Always Use the Configured Main Domain)
* **Never** derive project identity, audience, or `X-Forwarded-Host` from raw internal Kubernetes request headers (`r.Host` such as `pn-backend`, `pn-backend.proj-pn.svc:8080`, or `gateway.identity.svc:8080`).
* **Always** derive the project name and audience deterministically from the project's configured public domain under the platform's main domain (`PUBLIC_APP_URL`):
  - **Main Platform Domain**: `nacfson.cloud` (production `vps-k3s`) / `example.com` (`local-k3s`)
  - **Auth Broker Domain**: `auth.<main-domain>` (`https://auth.nacfson.cloud`)
  - **Project Public Domain (`PUBLIC_APP_URL`)**: `https://<project-slug>.<main-domain>` (e.g., `https://pn.nacfson.cloud`)
  - **Canonical Project Host (`X-Forwarded-Host`)**: `<project-slug>.<main-domain>` (e.g., `pn.nacfson.cloud`)
  - **Canonical Project Audience (`X-Project-Audience`)**: `project-<project-slug>` (e.g., `project-pn`)

#### Rule 2: Complete Identity in One Step on Ticket Exchange
* `POST /auth/exchange` should return the complete verified user identity (`session_handle`, `subject`, `issuer`, `audience`, and `email`) so `/api/auth/callback` does not need a redundant second network hop (`GET /auth`) just to read user claims.

#### Rule 3: Single Shared Verification Contract
* Hosted Go backends should use the shared verification logic ([`gateway/pkg/projectauth/middleware.go`](gateway/pkg/projectauth/middleware.go)) rather than maintaining custom per-project header parsers that can drift from `gateway/internal/auth/handler.go`.

#### Rule 4: Explicit Per-Field Error Messages & Post-Merge Image Digest Pinning
* Never collapse multiple validation checks into `ident == nil` with a generic `"gateway returned invalid identity claims"` message. Log the exact field that failed (`audience mismatch: got X want Y`, `missing X-User-Email`, `issuer mismatch`).
* When modifying `gateway/` source code, wait for `.github/workflows/build-images.yaml` to publish the new multi-arch image on `main`, then pin the resulting `@sha256:<digest>` in [`deploy/platform/identity/gateway-deployment.yaml`](deploy/platform/identity/gateway-deployment.yaml).
