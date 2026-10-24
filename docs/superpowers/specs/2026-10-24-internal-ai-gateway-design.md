# Internal AI Gateway (`vault-ai-proxy`) & Per-Project Kill Switch — Design

Date: 2026-10-24 · Size: project (new `vault-proxies` service + K8s/Cilium boundary migration + operator CLI/UI + mandatory GitOps CI & readiness verification) · Status: draft for operator approval

Visual Reference: [`docs/internal-ai-gateway.html`](../../internal-ai-gateway.html)

Marking: **(operator)** = decided by the operator. **(my call)** = my proposal, can be changed.

---

## 1. Intent & Why

Hosted tenant projects in `nacfson_pipeline` (starting with `Project_PN` in `proj-pn`) need fallback LLM enrichment (`POST /v1/chat/completions`) without holding upstream AI API keys or opening direct, unmonitored egress from project pods to `generativelanguage.googleapis.com`.

`Project_PN` (`003a154..5e3cd9a`) has already eliminated direct `googleapis` credentials and fallback chains: `pn-backend` now tries free `WiktApi` (`api.wiktapi.dev`) first, and on a miss sends a plain URL + model request (`POST {ENRICH_PRIMARY_BASE_URL}/chat/completions` with `model: ENRICH_PRIMARY_MODEL` and **no `Authorization` header**), logging any non-200 error to `stdout` and stopping immediately.

We now need a **cluster-internal AI gateway (`vault-ai-proxy`)** inside `nacfson_pipeline` that:
1. Enforces **Kubernetes-verified pod identity and permissions** so only explicitly declared pods (such as `proj-pn/pn-backend`) can reach the AI system. **(operator)**
2. Keeps the upstream Gemini API key strictly inside the `vault` namespace (`OpenBao`), never in project pods or Kubernetes Secrets. **(operator)**
3. Provides a **middle-management tracking and Kill Switch system** (manual `STOP`/`START` via pod-loopback `127.0.0.1:8081` directly mutating the in-process RAM table + automatic daily quota circuit-breaker) so any project's AI calls can be tracked and stopped immediately without taking down the project pod or breaking free WiktApi lookups. **(operator)**
4. **Verifies that the Kill Switch and boundary rules are working on every GitOps run** (both in CI via `.github/workflows/gitops.yaml` + `scripts/platform-preflight.py` and in-cluster during FluxCD reconciliation via `/readyz`). **(operator)**

---

## 2. Goals, Non-Goals, and Anti-Goals

### Goals
- Deploy `vault-ai-proxy` (`Service/ai-gateway.vault.svc:8080`) in the `vault` namespace (`deploy/platform/vault-proxies/ai-proxy.yaml`), built from Go 1.22 standard library code in `services/vault-proxies/cmd/ai-proxy/`. **(operator)**
- Remove `generativelanguage.googleapis.com` from `proj-pn`'s [`deploy/projects/pn/boundary/translation-egress.yaml`](../../../deploy/projects/pn/boundary/translation-egress.yaml) and move that external FQDN egress exclusively onto `vault-ai-proxy` in `vault`. **(operator)**
- Verify caller pod identity through **Kubernetes** (combining `NetworkPolicy` `namespaceSelector` + `podSelector` at the kernel layer with `kube-apiserver` Pod-IP resolution at the gateway layer), never trusting self-reported headers from the calling pod. **(operator)**
- Separate the cluster-facing data listener (`0.0.0.0:8080`, `dataMux`) from the pod-local loopback admin listener (`127.0.0.1:8081`, `adminMux`) inside the same Go process so `adminMux` mutates the in-memory Kill-Switch table (`*AIProxy` protected by `sync.RWMutex`) directly in `< 1µs` while remaining completely unreachable from project pods or public ingress. **(operator)**
- Persist per-project Kill-Switch state (`enabled`, `daily_limit`) in OpenBao (`kv/ai/projects/<slug>`) so a stopped project remains stopped across `vault-ai-proxy` pod restarts. **(my call)**
- Provide operator signal delivery scripts (`scripts/ai-gateway-ctl.sh` and `scripts/open-ai-gateway-ui.sh`) and automatic daily quota circuit-breaking. **(my call)**
- Enforce automated Kill-Switch and boundary verification on **every GitOps run** in `.github/workflows/gitops.yaml`, `scripts/platform-preflight.py`, and in-cluster `/readyz`. **(operator)**

### Non-Goals
- Modifying `gateway/` (the Go Auth Gateway in `identity` that handles human browser Google OAuth / Keycloak OIDC login).
- Changing `Project_PN`'s request/response JSON schema (`/v1/chat/completions`).
- Creating a new tenant project (`proj-*`) repository; this is a platform component in `nacfson_pipeline` so `n_projects` stays `1`.

### Anti-Goals
- Putting any AI API key in Git, Kubernetes `Secret` objects, or `proj-*` pod environment variables (violates Constitution Principle V).
- Exposing `/admin/*` kill-switch endpoints on `0.0.0.0:8080`, on a Kubernetes `Service`, or on public Traefik ingress.
- Allowing `pn-frontend` or any unapproved pod to connect to `ai-gateway.vault.svc:8080`.

---

## 3. Constraints

- **Go 1.22 Standard Library Only**: `services/vault-proxies/` MUST build with zero third-party Go modules (`go.mod` unchanged).
- **Zero Plaintext Secrets (Constitution Principle V)**: Upstream Gemini API key (`kv/ai/gemini-api-key`) lives only in OpenBao and in `vault-ai-proxy` memory.
- **Deterministic $1/n$ Resource Budget (Constitution Principle IV)**: `vault-ai-proxy` declares explicit CPU/memory requests and limits (`requests: 25m CPU / 32Mi RAM`, `limits: 100m CPU / 64Mi RAM`, matching `vault-db-proxy` and `vault-registry-proxy`) counted in `platformReservations` by `scripts/platform-preflight.py`.
- **Canonical Architecture Handbook Sync**: Because `deploy/`, `services/`, `scripts/platform-preflight.py`, and `ARCHITECTURE.md` change, `docs/platform-architecture.html` MUST be updated in the same implementation PR to pass `scripts/check-arch-doc-sync.py`.

---

## 4. Approaches Considered

1. **Approach 1 (Chosen): Dedicated `vault-ai-proxy` Pod in `deploy/platform/vault-proxies/`** **(operator)**
   - Adds a third Go stdlib binary (`cmd/ai-proxy`) and Deployment (`vault-ai-proxy`) in the `vault` namespace alongside `vault-db-proxy` and `vault-registry-proxy`.
   - *Why chosen*: Keeps `vault-db-proxy` air-gapped from the internet (zero `:443` egress), keeps `vault-registry-proxy` isolated to kubelet image pulls, gives AI traffic its own clean node in Hubble UI, and costs only `32Mi` RAM while preserving `n = 1` project budgeting.
2. **Approach 2 (Rejected): Merge AI Gateway into `vault-db-proxy` or `vault-registry-proxy`**
   - *Why rejected*: Violates least-privilege isolation by either giving the PostgreSQL proxy outbound internet egress or exposing the pod holding the GHCR pull token to application pods.
3. **Approach 3 (Rejected): Pod-less Cilium Envoy L7 Header Injection**
   - *Why rejected*: Requires storing the API key in a Kubernetes `Secret` (violating Constitution Principle V) and cannot provide per-project loopback kill-switch state or `kube-apiserver` pod attribution.

---

## 5. Detailed Design

### 5.1 Dual-Listener & Shared In-Memory Kill-Switch Architecture (`services/vault-proxies/cmd/ai-proxy/`)

`vault-ai-proxy` runs as a **single Go process** holding a single `*AIProxy` struct in memory protected by `sync.RWMutex`:

```text
┌────────────────────────────────────────────────────────────────────────────┐
│ Pod: vault-ai-proxy (Single Go Process in vault namespace)                 │
│                                                                            │
│  [eth0: 0.0.0.0:8080] ──► proxy.DataMux()                                  │
│    • POST /v1/chat/completions                                             │
│    • GET  /startupz, /readyz, /healthz                                     │
│    • (Zero /admin/* routes; rejects caller Authorization headers with 403) │
│           │                                                                │
│           ▼  (Direct Go pointer read: p.mu.RLock() < 1µs)                  │
│  ┌──────────────────────────────────────────────────────────────────────┐  │
│  │ Shared In-Memory Table: p.projects["pn"] (*ProjectGrant)             │  │
│  │   Namespace: "proj-pn", PodLabel: "pn-backend"                       │  │
│  │   AllowedModels: {"gemini-3.1-flash-lite": true}                     │  │
│  │   Enabled: true/false, DailyLimit: 500, UsedToday: 142, Blocked: 0   │  │
│  └──────────────────────────────────────────────────────────────────────┘  │
│           ▲  (Direct Go pointer write: p.mu.Lock() < 1µs + OpenBao save)   │
│           │                                                                │
│  [lo: 127.0.0.1:8081] ──► proxy.AdminMux()                                 │
│    • GET  /                (Built-in local operator HTML dashboard)        │
│    • GET  /admin/status    (JSON usage & switch table for all projects)    │
│    • POST /admin/revoke    (?project=pn -> Enabled=false + OpenBao write)  │
│    • POST /admin/enable    (?project=pn -> Enabled=true  + OpenBao write)  │
│    • POST /admin/quota     (?project=pn&daily_limit=500)                   │
│    • POST /admin/set-key   (Saves Gemini key to OpenBao via K8s SA + RAM)  │
└────────────────────────────────────────────────────────────────────────────┘
```

- **Why `127.0.0.1:8081` is unreachable from pods**:
  - In Linux, `127.0.0.1` (`lo`) is private to the `vault-ai-proxy` pod's own network namespace. Packets from `pn-backend` arrive on `eth0` (`10.42.x.x`), where nothing listens on `:8081` (`Connection Refused`), and `dataMux` on `:8080` has no `/admin/*` handlers (`404 Not Found`).
  - Only the operator on the workstation/host via `kubectl port-forward` or `kubectl exec` (which instructs the host K3s `kubelet` to enter the container network namespace directly) can connect to `127.0.0.1:8081`.

### 5.2 Two-Layer Kubernetes-Verified Pod Permission

Caller identity is never trusted from HTTP headers supplied by the calling pod:

1. **Layer 1 — Kernel Admission (`NetworkPolicy` + `CiliumNetworkPolicy`)**:
   - In `deploy/projects/pn/boundary/network-policy.yaml`: allow egress from `proj-pn` to `namespaceSelector: vault`, `podSelector: app.kubernetes.io/name=vault-ai-proxy` on TCP `8080`.
   - In `deploy/platform/vault/network-policy.yaml`: add `ai-proxy-policy` allowing ingress on TCP `8080` **only** from `namespaceSelector: proj-pn` + `podSelector: app.kubernetes.io/name=pn-backend`. `pn-frontend` and all other pods are dropped by Cilium eBPF before TCP handshake completion.
2. **Layer 2 — `kube-apiserver` Pod-IP Attribution inside `vault-ai-proxy`**:
   - When a request arrives on `:8080`, `vault-ai-proxy` extracts the peer IP from `r.RemoteAddr` and resolves it against `kube-apiserver` (`GET /api/v1/pods?fieldSelector=status.podIP=<ip>`, using its `vault-ai-proxy` ServiceAccount with a minimal `ClusterRole` granting `get,list` on `pods`, cached in RAM by IP for `60s`).
   - It verifies that `(pod.metadata.namespace, pod.metadata.labels["app.kubernetes.io/name"])` matches a registered project grant (`proj-pn` / `pn-backend`) and that the requested `model` in the JSON body is in `AllowedModels`.
   - If the pod IP does not match an authorized pod or the model is unpermitted, `vault-ai-proxy` logs a denied audit event and returns `403 Forbidden`.

### 5.3 Kill Switch, Credential Seeding, Auto-Quota Breaker, and Signal Delivery

1. **Zero-Vault-Token Credential Seeding & Manual Stop / Start (`127.0.0.1:8081` UI + `scripts/ai-gateway-ctl.sh`)**: **(my call)**
   - **Zero Vault Root Token Needed**: Because `vault-ai-proxy` authenticates to OpenBao automatically using its Kubernetes ServiceAccount JWT (`auth/kubernetes/login`), the operator never needs to remember or enter an OpenBao Root Token to set or rotate the Gemini API key.
   - **1-Click Browser UI (`POST 127.0.0.1:8081/admin/set-key`)**: Pasting the Gemini API key (`AIzaSy...`) into the local loopback UI (`http://127.0.0.1:18081`) or running `./scripts/ai-gateway-ctl.sh set-key` sends the key over the loopback tunnel to `vault-ai-proxy`, which writes `kv/data/ai/gemini-api-key` to OpenBao's encrypted disk PVC using its K8s ServiceAccount token and hot-reloads the key in RAM immediately (zero pod restart). Also registers `gemini-api-key` in [`scripts/vault-rotate.sh`](../../../scripts/vault-rotate.sh) and [`scripts/vault-seed-inventory.sh`](../../../scripts/vault-seed-inventory.sh).
   - **Forgotten Root Token Bypass**: For general OpenBao administration when the Root Token is forgotten, scripts can also mint a short-lived Kubernetes JWT for `openbao-backup` (`kubectl create token openbao-backup -n vault`), which is already bound to the `operator-admin` policy (`kv/*` read/write/delete), or regenerate a Root Token from the Unseal Key via `./scripts/vault-seed-inventory.sh`.
   - `./scripts/ai-gateway-ctl.sh status` — queries `127.0.0.1:8081/admin/status` via `kubectl` (auto-falling back to `ssh oracleCloud sudo k3s kubectl` like `scripts/vault-rotate.sh`).
   - `./scripts/ai-gateway-ctl.sh stop <project>` — calls `POST 127.0.0.1:8081/admin/revoke?project=<project>`, flipping `Enabled = false` in RAM immediately and writing `enabled=false` to OpenBao `kv/ai/projects/<project>`.
   - `./scripts/ai-gateway-ctl.sh start <project>` — calls `POST 127.0.0.1:8081/admin/enable?project=<project>`.
   - `./scripts/ai-gateway-ctl.sh limit <project> <daily_limit>` — updates the daily request cap in RAM and OpenBao.
2. **Local Browser Dashboard (`scripts/open-ai-gateway-ui.sh`)**: **(my call)**
   - Follows the exact pattern of `scripts/open-hubble-ui.sh` and `scripts/open-victorialogs-ui.sh`: opens a local loopback port-forward (`http://127.0.0.1:18081` ➔ `vault-ai-proxy:8081`) so the operator can view live counters and click `[STOP]` / `[START]` in a browser, plus supports `--verify`.
3. **Automatic Daily Quota Circuit Breaker**: **(operator)**
   - When `UsedToday >= DailyLimit` (default `500` calls/UTC day for `pn`), `vault-ai-proxy` automatically trips `Enabled = false` for that project and returns `403 Forbidden` (`{"error":"daily AI quota exceeded for project pn; switch tripped to STOPPED"}`) without calling Gemini.

### 5.4 Upstream Gemini Egress & `Project_PN` Wiring

- **Remove Direct Gemini Egress from `proj-pn`**:
  - Update [`deploy/projects/pn/boundary/translation-egress.yaml`](../../../deploy/projects/pn/boundary/translation-egress.yaml) to remove `generativelanguage.googleapis.com`, leaving **only** `api.wiktapi.dev` on TCP `443`.
- **Grant Gemini Egress Only to `vault-ai-proxy`**:
  - Add `CiliumNetworkPolicy/ai-proxy-egress` in `deploy/platform/vault/network-policy.yaml` allowing `vault-ai-proxy` egress to `toEntities: ["kube-apiserver"]` (`:443/:6443`) and `toFQDNs: [{matchName: "generativelanguage.googleapis.com"}]` (`:443`), plus portable `NetworkPolicy` egress to `openbao:8200` and `kube-dns:53`.
- **Wire `pn-backend` Env Vars**:
  - In [`deploy/projects/pn/workloads/backend-deployment.yaml`](../../../deploy/projects/pn/workloads/backend-deployment.yaml), add:
    - `ENRICH_PRIMARY_BASE_URL: "http://ai-gateway.vault.svc:8080/v1"`
    - `ENRICH_PRIMARY_MODEL: "gemini-3.1-flash-lite"`

### 5.5 Mandatory Verification on Every `git commit`, CI Run, and FluxCD Reconciliation **(operator)**

Because the Kill Switch and boundary isolation are critical security controls, they are verified automatically at four gates across local `git commit`, GitHub Actions CI, and in-cluster K3s startup:

1. **Gate 1 — Local `git commit` Hook & Review-Cycle Engine (`.githooks/pre-commit` + `scripts/review-cycle.py`)**:
   - `.githooks/pre-commit` blocks any staged `AIza...` Google API key leak and enforces `docs/platform-architecture.html` sync (`scripts/check-arch-doc-sync.py --staged`).
   - Update `run_domain_verification` in `scripts/review-cycle.py` so staging any file in `services/vault-proxies/`, `deploy/platform/vault/`, or `deploy/platform/vault-proxies/` automatically executes `cd services/vault-proxies && go test -v ./... && go vet ./...` and `python3 -m unittest discover tests/preflight` before a commit receipt can pass.
2. **Gate 2 — Static Preflight Invariants (`scripts/platform-preflight.py` + `tests/preflight/`)**:
   - Fails if any resource in `proj-*` references `generativelanguage.googleapis.com` or any `*API_KEY*` env var.
   - Fails if `Service/ai-gateway` (or any Service/IngressRoute) exposes admin port `8081`.
   - Fails if `ai-proxy-policy` allows `:8080` ingress from an unrestricted `podSelector: {}`.
   - Fails if `Deployment/vault-ai-proxy` omits `startupProbe: /startupz`, `readinessProbe: /readyz`, or `livenessProbe: /healthz`.
3. **Gate 3 — Live Behavioral Kill-Switch Suite in `.github/workflows/gitops.yaml`**:
   - Currently `.github/workflows/gitops.yaml` does not run `services/vault-proxies` Go tests. Add a mandatory `vault-proxies-tests` job (`cd services/vault-proxies && go test -v -race ./... && go vet ./...`) on every PR and push to `main` (and make `render-validate` depend on it), asserting:
     1. Verified pod (`proj-pn/pn-backend`) with switch `ON` receives `200 OK` and upstream receives `Authorization: Bearer <openbao-key>`.
     2. Sending `POST /admin/revoke?project=pn` on loopback flips the RAM table immediately; the next `/v1/chat/completions` call returns `403 Forbidden` and upstream Gemini receives **zero** requests.
     3. Sending `POST /admin/enable?project=pn` restores `200 OK`.
     4. Calling `/admin/*` on the data mux (`:8080`) returns `404 Not Found`.
     5. Reaching `DailyLimit` automatically trips the project switch to `STOPPED` (`403 Forbidden`).
     6. Unverified pod IP or unpermitted model returns `403 Forbidden`.
     7. `/startupz` 5-point vulnerability self-audit returns `200 OK` when healthy and `500` when any route leak, non-loopback socket, or broken kill-switch state is injected.
4. **Gate 4 — Three-Probe Architecture (`startupProbe: /startupz`, `readinessProbe: /readyz`, `livenessProbe: /healthz`) & Live Vulnerability Self-Audit**:
   - Following `contracts/CONTRACT_PROBES_READINESS.md` and Constitution Principle VII, `deploy/platform/vault-proxies/ai-proxy.yaml` declares all three probes and `restartPolicy: Always`:
     - **`startupProbe: GET :8080/startupz` (5-Point Live Kill-Switch & Vulnerability Self-Audit)**: Executed by `kubelet` before `readinessProbe` begins and before `Service/ai-gateway` routes any traffic to the pod:
       1. *Loopback Socket Audit*: Asserts `adminListener.Addr()` is strictly loopback (`127.0.0.1:8081`, `IP.IsLoopback() == true`, never `0.0.0.0`).
       2. *`:8080` Route Isolation Audit*: Fires an internal synthetic `POST /admin/enable?project=__selftest__` against `DataMux()` (`:8080`) and asserts it returns `404 Not Found` (zero admin route leakage on the cluster port).
       3. *Caller Auth Spoofing Audit*: Fires an internal synthetic `POST /v1/chat/completions` with `Authorization: Bearer fake` against `DataMux()` and asserts it returns `403 Forbidden`.
       4. *Live Kill-Switch Flip & Block Drill*: Exercises the real `AdminMux()` + `DataMux()` RAM pointer path on a reserved `__selftest__` slot (`revoke` ➔ asserts `Enabled == false` in RAM ➔ asserts `DataMux()` blocks with `403 Forbidden` and makes 0 upstream calls ➔ cleans up `__selftest__`).
       5. *OpenBao State Sync*: Loads persisted project switch states (`kv/ai/projects/pn`) from `openbao:8200`. If any of checks 1–5 fail, `/startupz` returns `500 Internal Server Error`, quarantining the pod (`Ready=False`) and halting FluxCD (`wait: true`).
     - **`readinessProbe: GET :8080/readyz` (every 10s)**: Continuously verifies that the startup self-test passed, the `:8081` listener remains strictly loopback, and `openbao:8200` TCP reachability is healthy. Failure marks `Ready=False` (withholding endpoints without restart storms).
     - **`livenessProbe: GET :8080/healthz` (every 15s)**: Process-local only (`200 OK`, zero external network calls per `CONTRACT_PROBES_READINESS.md` §3.3).

### 5.6 Zero-Standing-RAM Scrub, Least-Privilege Backup (`raft-snapshot.hcl`), and Unseal Key Regeneration **(operator)**

1. **RAM `[]byte` Credential Scrub on Kill-Switch `STOP`**:
   - `vault-ai-proxy` stores `geminiKey` as a mutable `[]byte` slice in RAM (never an immutable `string`).
   - Whenever the Kill Switch flips to `STOPPED` (`/admin/revoke` or daily quota trip), `wipeGeminiKeyLocked()` overwrites every byte of `geminiKey` with `0x00` and releases the slice (`p.geminiKey = nil`). When re-enabled (`START`), `vault-ai-proxy` reloads the key from OpenBao.
2. **Option A Encrypted Snapshot-Only Policy (`deploy/platform/vault/policies/raft-snapshot.hcl`)**:
   - `ServiceAccount/openbao-backup` is bound strictly to `raft-snapshot.hcl` (`path "sys/storage/raft/snapshot" { capabilities = ["read"] }`), granting access only to the AES-256-GCM encrypted Raft snapshot stream and returning `403 permission denied` on all `kv/*` secrets.
   - `deploy/platform/governance/manual-change-policy.yaml` restricts non-break-glass `serviceaccounts/token` creation strictly to `governance/operator-readonly`, and `scripts/platform-preflight.py` enforces Constitution Clarification 2.2.1 on `observability` (`hostPath: /var/log/pods` with `readOnly: true` on `DaemonSet/fluent-bit` only, `privileged: false`).
3. **Zero-Data-Loss OpenBao Unseal Key Regeneration (`scripts/vault-reset-unseal-key.sh`)**:
   - If the operator loses both the Shamir Unseal Key and Root Token, `./scripts/vault-reset-unseal-key.sh` wipes only OpenBao's `/var/lib/openbao/data/*` directory, runs `bao operator init` to issue a brand-new Unseal Key, re-seeds internal credentials (`vault-seed-inventory.sh --force`), updates `postgres-0` role passwords over the local Unix socket (`psql -U postgres` + `postgres-init-job`) with **zero PostgreSQL data loss**, and revokes the initial Root Token via an `EXIT` trap.

---

## 6. What Is Left to the Builder

- Exact HTML/CSS layout of the built-in single-page operator dashboard served on `127.0.0.1:8081/`.
- Adding `vault-ai-proxy` image build matrix entry to `.github/workflows/build-images.yaml` and updating `docs/platform-architecture.html`, `ARCHITECTURE.md`, `DEPENDENCIES.md`, and `contracts/CONTRACT_ISOLATION_BUDGET.md` to pass `scripts/check-arch-doc-sync.py`.
