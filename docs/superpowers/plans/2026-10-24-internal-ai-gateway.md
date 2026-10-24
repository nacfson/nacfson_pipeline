# Internal AI Gateway (`vault-ai-proxy`) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build and wire the internal AI gateway (`vault-ai-proxy` in namespace `vault`) with Kubernetes-verified pod attribution, a shared in-memory + OpenBao-persisted loopback Kill Switch (`127.0.0.1:8081`), a 5-point `/startupz` live vulnerability self-audit, and end-to-end verification across local `git commit`, GitHub Actions CI (`gitops.yaml`), and K3s FluxCD reconciliation.

**Architecture:** A single zero-dependency Go 1.22 binary (`services/vault-proxies/cmd/ai-proxy`) runs two HTTP servers sharing one `*AIProxy` struct (`sync.RWMutex`) in RAM: `DataMux()` on `0.0.0.0:8080` (`eth0`, cluster-facing for `/v1/chat/completions`, `/startupz`, `/readyz`, `/healthz` with zero `/admin/*` routes) and `AdminMux()` on `127.0.0.1:8081` (`lo`, loopback-only for `/admin/status`, `/admin/revoke`, `/admin/enable`, `/admin/quota`, `/admin/set-key`, and `/` local UI). Direct `generativelanguage.googleapis.com` egress is removed from `proj-pn` and granted solely to `vault-ai-proxy`, while `pn-backend` keeps free `api.wiktapi.dev` egress and routes AI misses to `http://ai-gateway.vault.svc:8080/v1`.

**Tech Stack:** Go 1.22 standard library only (`net/http`, `sync`, `encoding/json`, `net/netip`), Kubernetes `NetworkPolicy` & Cilium `CiliumNetworkPolicy` (`cilium.io/v2`), OpenBao KV v2 + `auth/kubernetes`, Python 3.12 (`scripts/platform-preflight.py`, `scripts/review-cycle.py`, `unittest`), GitHub Actions (`.github/workflows/gitops.yaml`, `.github/workflows/build-images.yaml`).

**Spec:** [`docs/superpowers/specs/2026-10-24-internal-ai-gateway-design.md`](../specs/2026-10-24-internal-ai-gateway-design.md) & [`docs/internal-ai-gateway.html`](../../internal-ai-gateway.html)

## Global Constraints

- **Zero Third-Party Go Dependencies:** `services/vault-proxies/go.mod` MUST remain Go `1.22` standard library only (zero `require` directives).
- **Zero Plaintext Secrets in Git or Pods:** Never commit `AIza...` keys or mount Kubernetes `Secret` objects into `proj-*` or `vault-ai-proxy`. Upstream Gemini API key lives in OpenBao `kv/data/ai/gemini-api-key` and is read/written by `vault-ai-proxy` using its Kubernetes ServiceAccount JWT via `POST /v1/auth/kubernetes/login`.
- **Strict Dual-Listener Separation:** `DataMux()` (`0.0.0.0:8080`) MUST register only `/v1/chat/completions`, `/startupz`, `/readyz`, and `/healthz`. `AdminMux()` (`127.0.0.1:8081`) MUST bind strictly to loopback (`127.0.0.1`) and never be exposed on any Kubernetes `Service` or `IngressRoute`.
- **Three-Probe Contract (`CONTRACT_PROBES_READINESS.md`):** `Deployment/vault-ai-proxy` MUST declare `startupProbe` (`GET :8080/startupz`), `readinessProbe` (`GET :8080/readyz`), `livenessProbe` (`GET :8080/healthz`), and `restartPolicy: Always`.
- **Architecture Handbook Synchronization:** Modifying `deploy/`, `services/`, `contracts/`, `scripts/platform-preflight.py`, `ARCHITECTURE.md`, or `DEPENDENCIES.md` requires co-updating `docs/platform-architecture.html` and passing `python3 scripts/check-arch-doc-sync.py --worktree`.
- **Review Receipt Gate:** Before every `git commit`, run the `code-review` skill (`.agents/skills/code-review/SKILL.md`) so `python3 scripts/review-cycle.py check-gate --scope staged --require-pipeline code-review` passes.

## Review Focus

1. **`X-Forwarded-For` / Header Spoofing on `:8080`:** A compromised pod sends `X-Forwarded-For: 10.42.0.99` or `X-Project-ID: pn`; `vault-ai-proxy` MUST ignore all HTTP headers for caller identity and extract the peer IP solely from `net.SplitHostPort(r.RemoteAddr)`.
2. **Oversized or Malformed JSON Body on `/v1/chat/completions`:** A buggy pod sends a 50 MiB payload or invalid JSON; `DataMux()` MUST cap request bodies with `http.MaxBytesReader` (`1 MiB`) and return `400 Bad Request` without panicking or exhausting `64Mi` pod memory.
3. **UTC Day Rollover on Daily Quota (`UsedToday`):** When the UTC date changes (`time.Now().UTC().Format("2006-01-02") != grant.CurrentUTCDate`), `UsedToday` MUST reset to `0`, while a project manually stopped by the operator (`ManualRevoked: true`) MUST stay `STOPPED` across midnight rollover until explicitly re-enabled via `/admin/enable`.
4. **Concurrent Loopback Flip During Active AI Calls (`-race`):** Simultaneous `POST /admin/revoke` on `:8081` and `POST /v1/chat/completions` on `:8080` MUST be race-free under `go test -race ./...`.
5. **OpenBao Temporarily Sealed/Unreachable During `/admin/revoke`:** If OpenBao (`openbao:8200`) is unreachable when the operator sends `POST /admin/revoke?project=pn`, the in-memory switch `p.projects["pn"].Enabled` MUST still flip to `false` immediately (fail-safe stop in RAM first) before attempting persistence.

---

### Task 1: Implement `vault-ai-proxy` Go Binary, 5-Point `/startupz` Self-Audit & Unit Test Suite

**Files:**
- Create: `services/vault-proxies/cmd/ai-proxy/main.go`
- Create: `services/vault-proxies/cmd/ai-proxy/main_test.go`
- Create: `services/vault-proxies/Dockerfile.ai-proxy`
- Modify: `.github/workflows/build-images.yaml:23-36`

**Interfaces:**
- Produces:
  - `type ProjectGrant struct { Project string; Namespace string; PodLabel string; AllowedModels map[string]bool; Enabled bool; ManualRevoked bool; DailyLimit int; UsedToday int; BlockedCount int; CurrentUTCDate string }`
  - `type PodIdentity struct { Namespace string; PodName string; AppLabel string }`
  - `type AIProxyConfig struct { DataAddr string; AdminAddr string; UpstreamBaseURL string; OpenBaoAddr string; OpenBaoRole string; SAJWTPath string; InitialAPIKey string; DefaultDailyLimit int; ResolvePodIP func(ctx context.Context, ip string) (*PodIdentity, error) }`
  - `func NewAIProxy(cfg AIProxyConfig) *AIProxy`
  - `func (p *AIProxy) DataMux() http.Handler` — routes `POST /v1/chat/completions`, `GET /startupz`, `GET /readyz`, `GET /healthz`
  - `func (p *AIProxy) AdminMux() http.Handler` — routes `GET /`, `GET /admin/status`, `POST /admin/revoke`, `POST /admin/enable`, `POST /admin/quota`, `POST /admin/set-key`, `POST /admin/reload-key`
  - `func (p *AIProxy) RunStartupSelfTest() error` — executes the 5-point live vulnerability & kill-switch self-audit on `__selftest__`

- [ ] **Step 1: Write the failing unit & behavioral test suite in `services/vault-proxies/cmd/ai-proxy/main_test.go`**

Write tests covering all 7 Spec Gate 3 requirements plus the 5 Review Focus edge cases:
- `TestStartupSelfAuditPassesAndCatchesVulnerabilities`: verifies `GET /startupz` returns `200 OK` when healthy, and returns `500` if (a) `AdminBindHost` is non-loopback `0.0.0.0:8081`, (b) an `/admin/*` route is injected on `DataMux()`, or (c) the kill-switch self-drill fails to block `__selftest__`.
- `TestDataMuxAllowsVerifiedPodAndInjectsOpenBaoKey`: mock `ResolvePodIP` returns `proj-pn/pn-backend`; `POST /v1/chat/completions` with `{"model":"gemini-3.1-flash-lite","messages":[{"role":"user","content":"hi"}]}` and no `Authorization` header returns `200 OK`, increments `UsedToday` to `1`, and upstream mock Gemini receives `Authorization: Bearer test-openbao-gemini-key`.
- `TestKillSwitchRevokeAndEnableInRAM`: calling `POST /admin/revoke?project=pn` on `AdminMux()` flips `Enabled=false` and `ManualRevoked=true`; subsequent `POST /v1/chat/completions` on `DataMux()` returns `403 Forbidden` with `0` upstream Gemini calls; `POST /admin/enable?project=pn` restores `200 OK`.
- `TestDataMuxRejectsAdminRoutesAndCallerAuthHeader`: calling `POST /admin/enable?project=pn` on `DataMux()` returns `404 Not Found`; calling `POST /v1/chat/completions` with `Authorization: Bearer caller-key` returns `403 Forbidden`.
- `TestSpoofedXForwardedForAndUnauthorizedPodRejected`: request from `10.42.0.88` (`pn-frontend`) with header `X-Forwarded-For: 10.42.0.12` is resolved by `r.RemoteAddr` (`10.42.0.88`) and rejected with `403 Forbidden`; unpermitted model `gemini-2.5-pro` returns `403 Forbidden`; body `> 1 MiB` returns `400 Bad Request`.
- `TestDailyQuotaAutoBreakerAndUTCRollover`: hitting `DailyLimit` (`2/2`) automatically flips `Enabled=false` and blocks call #3 with `403 Forbidden`; UTC date rollover resets `UsedToday=0` for quota-tripped projects but preserves `Enabled=false` when `ManualRevoked==true`.
- `TestSetKeyPersistsToOpenBaoViaK8sAuthAndHotReloads`: `POST /admin/set-key` with `{"api_key":"new-key"}` logs into mock OpenBao `POST /v1/auth/kubernetes/login`, writes `POST /v1/kv/data/ai/gemini-api-key`, and updates `p.geminiKey` in RAM immediately; also verifies `POST /admin/revoke` still flips RAM `Enabled=false` even if OpenBao returns `503`.

- [ ] **Step 2: Run `go test` to verify it fails**

Run: `(cd services/vault-proxies && go test -v ./cmd/ai-proxy/...)`
Expected: FAIL (`no Go files` / `NewAIProxy` undefined).

- [ ] **Step 3: Implement `services/vault-proxies/cmd/ai-proxy/main.go` and `services/vault-proxies/Dockerfile.ai-proxy`**

Implement `NewAIProxy`, `DataMux()`, `AdminMux()`, `RunStartupSelfTest()`, `kube-apiserver` pod-IP resolver (`GET https://$KUBERNETES_SERVICE_HOST:$KUBERNETES_SERVICE_PORT/api/v1/pods?fieldSelector=status.podIP=<ip>` with 60s TTL cache), and OpenBao K8s ServiceAccount login (`POST $OPENBAO_ADDR/v1/auth/kubernetes/login` + `GET/POST $OPENBAO_ADDR/v1/kv/data/ai/...`). Add `services/vault-proxies/Dockerfile.ai-proxy` (matching `Dockerfile.db-proxy` with `ENTRYPOINT ["/vault-ai-proxy"]` and `EXPOSE 8080`) and add `vault-ai-proxy` to the build matrix in `.github/workflows/build-images.yaml`.

- [ ] **Step 4: Run Go tests with `-race` and `go vet`**

Run: `(cd services/vault-proxies && go test -v -race ./... && go vet ./...)`
Expected: PASS (all `db-proxy`, `registry-proxy`, and `ai-proxy` tests pass with zero race warnings).

---

### Task 2: Add Platform Manifests (`ai-proxy.yaml`), Vault RBAC/Policies & Project PN Wiring

**Files:**
- Create: `deploy/platform/vault-proxies/ai-proxy.yaml`
- Create: `deploy/platform/vault/policies/ai-proxy.hcl`
- Modify: `deploy/platform/vault-proxies/kustomization.yaml:1-8`
- Modify: `deploy/platform/vault/service-accounts.yaml:1-39`
- Modify: `deploy/platform/vault/network-policy.yaml`
- Modify: `deploy/platform/vault/openbao-config.yaml`
- Modify: `deploy/projects/pn/boundary/translation-egress.yaml`
- Modify: `deploy/projects/pn/boundary/network-policy.yaml`
- Modify: `deploy/projects/pn/workloads/backend-deployment.yaml`

**Interfaces:**
- Consumes: `vault-ai-proxy` ports (`:8080` data/probes, `127.0.0.1:8081` loopback admin) and env vars (`DATA_ADDR=0.0.0.0:8080`, `ADMIN_ADDR=127.0.0.1:8081`, `OPENBAO_ADDR=http://openbao.vault.svc:8200`, `UPSTREAM_AI_BASE_URL=https://generativelanguage.googleapis.com/v1beta/openai`, `ALLOWED_PROJECT_GRANTS=pn:proj-pn:pn-backend:gemini-3.1-flash-lite:500`) from Task 1.
- Produces:
  - `vault/Deployment/vault-ai-proxy` (`replicas: 1`, `requests: {cpu: 25m, memory: 32Mi}`, `limits: {cpu: 100m, memory: 64Mi}`, `startupProbe: /startupz`, `readinessProbe: /readyz`, `livenessProbe: /healthz`, `restartPolicy: Always`)
  - `vault/Service/ai-gateway` (`ClusterIP`, port `8080` only — never `8081`)
  - `vault/ServiceAccount/vault-ai-proxy` + `ClusterRole/vault-ai-proxy-pod-reader` (`get,list` on `pods`) + `ClusterRoleBinding/vault-ai-proxy-pod-reader`
  - `vault/NetworkPolicy/ai-proxy-policy` & `vault/CiliumNetworkPolicy/ai-proxy-egress` (`toEntities: ["kube-apiserver"]`, `toFQDNs: [{matchName: "generativelanguage.googleapis.com"}]` on TCP `443`)
  - `proj-pn/CiliumNetworkPolicy/pn-backend-translation-egress` restricted to `api.wiktapi.dev` only
  - `proj-pn/NetworkPolicy/project-pn-isolation` allowing egress to `vault-ai-proxy` on TCP `8080`
  - `proj-pn/Deployment/pn-backend` env vars `ENRICH_PRIMARY_BASE_URL: "http://ai-gateway.vault.svc:8080/v1"` and `ENRICH_PRIMARY_MODEL: "gemini-3.1-flash-lite"`

- [ ] **Step 1: Create `deploy/platform/vault-proxies/ai-proxy.yaml` and register in `deploy/platform/vault-proxies/kustomization.yaml`**

Define `Deployment/vault-ai-proxy` and `Service/ai-gateway` in `deploy/platform/vault-proxies/ai-proxy.yaml` with `runAsNonRoot: true`, `runAsUser: 10001`, `readOnlyRootFilesystem: true`, `allowPrivilegeEscalation: false`, `capabilities.drop: ["ALL"]`, `startupProbe: /startupz:8080`, `readinessProbe: /readyz:8080`, `livenessProbe: /healthz:8080`, and `Service/ai-gateway` exposing only port `8080`. Append `- ai-proxy.yaml` to `deploy/platform/vault-proxies/kustomization.yaml`.

- [ ] **Step 2: Add `vault-ai-proxy` ServiceAccount, Pod-Reader RBAC, OpenBao Policy & Vault NetworkPolicies**

In `deploy/platform/vault/service-accounts.yaml`, add `ServiceAccount/vault-ai-proxy`, `ClusterRole/vault-ai-proxy-pod-reader` (`apiGroups: [""]`, `resources: ["pods"]`, `verbs: ["get", "list"]`), and `ClusterRoleBinding/vault-ai-proxy-pod-reader`. Create `deploy/platform/vault/policies/ai-proxy.hcl` (and mirror in `deploy/platform/vault/openbao-config.yaml`) granting `["create", "read", "update"]` on `kv/data/ai/*` and `kv/metadata/ai/*`. In `deploy/platform/vault/network-policy.yaml`, add `NetworkPolicy/ai-proxy-policy` (ingress `:8080` only from `namespaceSelector: proj-pn` + `podSelector: app.kubernetes.io/name=pn-backend`; egress to `openbao:8200` and `kube-dns:53`) and `CiliumNetworkPolicy/ai-proxy-egress` (egress to `kube-apiserver` `:443/:6443`, `kube-dns:53`, and `toFQDNs: [{matchName: "generativelanguage.googleapis.com"}]` on TCP `443`). Also allow `vault-ai-proxy` ingress to `openbao:8200` in `openbao-strict-isolation`.

- [ ] **Step 3: Remove direct Gemini egress from `proj-pn` and wire `pn-backend` to `http://ai-gateway.vault.svc:8080/v1`**

In `deploy/projects/pn/boundary/translation-egress.yaml`, remove `generativelanguage.googleapis.com` so `toFQDNs` contains only `matchName: "api.wiktapi.dev"`. In `deploy/projects/pn/boundary/network-policy.yaml`, add an egress rule allowing `proj-pn` to `namespaceSelector: vault`, `podSelector: app.kubernetes.io/name=vault-ai-proxy` on TCP `8080`. In `deploy/projects/pn/workloads/backend-deployment.yaml`, add `ENRICH_PRIMARY_BASE_URL: "http://ai-gateway.vault.svc:8080/v1"` and `ENRICH_PRIMARY_MODEL: "gemini-3.1-flash-lite"`.

- [ ] **Step 4: Validate Kustomize build & kubeconform schema on both clusters**

Run: `kubectl kustomize deploy/platform/vault && kubectl kustomize deploy/platform/vault-proxies && kubectl kustomize deploy/projects/pn`
Expected: Valid YAML output for all three Kustomizations with zero syntax errors.

---

### Task 3: Add Operator CLI (`ai-gateway-ctl.sh`), Local UI Launcher (`open-ai-gateway-ui.sh`) & Vault Script Integration

**Files:**
- Create: `scripts/ai-gateway-ctl.sh`
- Create: `scripts/open-ai-gateway-ui.sh`
- Modify: `scripts/vault-configure-k8s-auth.sh`
- Modify: `scripts/vault-rotate.sh`
- Modify: `scripts/vault-seed-inventory.sh`

**Interfaces:**
- Consumes: `127.0.0.1:8081` loopback admin API (`/admin/status`, `/admin/revoke`, `/admin/enable`, `/admin/quota`, `/admin/set-key`, `/admin/reload-key`) from Task 1.
- Produces:
  - `./scripts/ai-gateway-ctl.sh <status|stop|start|limit|set-key|self-test>`
  - `./scripts/open-ai-gateway-ui.sh [--port 18081] [--verify] [--no-browser]`

- [ ] **Step 1: Create `scripts/ai-gateway-ctl.sh` and `scripts/open-ai-gateway-ui.sh`**

Implement `scripts/ai-gateway-ctl.sh` with commands:
- `status`: queries `http://127.0.0.1:8081/admin/status` inside `deploy/vault-ai-proxy` (using `kubectl` or `ssh oracleCloud sudo k3s kubectl` fallback matching `scripts/vault-rotate.sh`).
- `stop <project>` / `start <project>`: sends `POST http://127.0.0.1:8081/admin/revoke?project=<project>` or `/admin/enable?project=<project>`.
- `limit <project> <daily_limit>`: sends `POST http://127.0.0.1:8081/admin/quota?project=<project>&daily_limit=<daily_limit>`.
- `set-key`: prompts silently (`read -r -s`) for `AIzaSy...` key and sends `POST http://127.0.0.1:8081/admin/set-key` (or falls back to `openbao-backup` K8s ServiceAccount login if `vault-ai-proxy` is not running yet—never requiring a Vault Root Token!).
- `self-test`: triggers `GET http://127.0.0.1:8080/startupz` and prints the 5-point vulnerability self-audit result.
Implement `scripts/open-ai-gateway-ui.sh` matching `scripts/open-hubble-ui.sh` (`kubectl port-forward -n vault deploy/vault-ai-proxy 18081:8081` + browser open + `--verify`). Also register `ai-proxy` policy/role in `scripts/vault-configure-k8s-auth.sh` and `gemini-api-key` (`kv/ai/gemini-api-key`) in `scripts/vault-rotate.sh` and `scripts/vault-seed-inventory.sh`.

- [ ] **Step 2: Verify script syntax with `bash -n`**

Run: `bash -n scripts/ai-gateway-ctl.sh scripts/open-ai-gateway-ui.sh scripts/vault-configure-k8s-auth.sh scripts/vault-rotate.sh scripts/vault-seed-inventory.sh`
Expected: Exit code `0` with zero syntax errors.

---

### Task 4: Enforce Preflight Rules, `git commit` Hook Verification (`review-cycle.py`), GitHub Actions CI (`gitops.yaml`) & Architecture Handbook Sync

**Files:**
- Modify: `scripts/platform-preflight.py:266-375,457-482,980-995`
- Modify: `tests/preflight/test_translation_egress.py`
- Create: `tests/preflight/test_ai_gateway_contract.py`
- Modify: `scripts/review-cycle.py:633-646`
- Modify: `tests/review_cycle/test_review_engine.py`
- Modify: `.github/workflows/gitops.yaml:16-48`
- Modify: `docs/platform-architecture.html`
- Modify: `scripts/check-arch-doc-sync.py:142-148`
- Modify: `ARCHITECTURE.md`
- Modify: `DEPENDENCIES.md`
- Modify: `contracts/CONTRACT_ISOLATION_BUDGET.md`
- Modify: `contracts/CONTRACT_PROBES_READINESS.md`

**Interfaces:**
- Consumes: Manifests from Task 2 and `services/vault-proxies/cmd/ai-proxy` test suite from Task 1.
- Produces:
  - `check_ai_gateway_contract(projects_dict, platform_docs)` in `scripts/platform-preflight.py`
  - Updated `TRANSLATION_HOSTS = frozenset({"api.wiktapi.dev"})` in `scripts/platform-preflight.py`
  - Automatic execution of `cd services/vault-proxies && go test -v ./... && go vet ./...` in both `scripts/review-cycle.py` (on local `git commit` whenever `services/vault-proxies/` or `deploy/platform/vault*` changes) and `.github/workflows/gitops.yaml` (on every PR and push to `main`).

- [ ] **Step 1: Write failing preflight & review-cycle unit tests in `tests/preflight/test_ai_gateway_contract.py`, `tests/preflight/test_translation_egress.py`, and `tests/review_cycle/test_review_engine.py`**

Add tests in `tests/preflight/test_ai_gateway_contract.py` asserting that `check_ai_gateway_contract` rejects:
1. Any project container declaring `*API_KEY*` env vars or direct `generativelanguage.googleapis.com` in `ENRICH_PRIMARY_BASE_URL`.
2. `Service/ai-gateway` exposing port `8081` or any port other than `8080`.
3. `Deployment/vault-ai-proxy` missing `startupProbe` (`/startupz`), `readinessProbe` (`/readyz`), or `livenessProbe` (`/healthz`).
4. `CiliumNetworkPolicy/ai-proxy-egress` allowing wildcard FQDN or world CIDR instead of `generativelanguage.googleapis.com:443` and `kube-apiserver`.
Update `tests/preflight/test_translation_egress.py` so `HOSTS = frozenset({"api.wiktapi.dev"})` and verify `generativelanguage.googleapis.com` is rejected in `proj-pn`. Add a test in `tests/review_cycle/test_review_engine.py` verifying that modifying `deploy/platform/vault-proxies/ai-proxy.yaml` triggers `cd services/vault-proxies && go test -v ./... && go vet ./...` in `run_domain_verification`.

- [ ] **Step 2: Run unit tests to verify they fail**

Run: `python3 -m unittest discover tests/preflight && python3 -m unittest discover tests/review_cycle`
Expected: FAIL (`check_ai_gateway_contract` missing and `TRANSLATION_HOSTS` mismatch).

- [ ] **Step 3: Implement preflight checks in `scripts/platform-preflight.py`, hook gate in `scripts/review-cycle.py`, and CI job in `.github/workflows/gitops.yaml`**

- In `scripts/platform-preflight.py`: set `TRANSLATION_HOSTS = frozenset({"api.wiktapi.dev"})`, implement `check_ai_gateway_contract(projects_dict, platform_docs)`, and call it in `main()`.
- In `scripts/review-cycle.py`: update `run_domain_verification` so changing any file under `services/vault-proxies/`, `deploy/platform/vault/`, or `deploy/platform/vault-proxies/` runs `cd services/vault-proxies && go test -v ./... && go vet ./...`.
- In `.github/workflows/gitops.yaml`: add a `vault-proxies-tests` job (`actions/setup-go@v5` with Go `1.22` running `cd services/vault-proxies && go test -v -race ./... && go vet ./...`) and add `vault-proxies-tests` to `render-validate.needs`.

- [ ] **Step 4: Synchronize `docs/platform-architecture.html`, `scripts/check-arch-doc-sync.py`, `ARCHITECTURE.md`, `DEPENDENCIES.md`, and `contracts/`**

Update platform reservation numbers from `1595m` CPU / `5312 MiB` RAM to `1620m` CPU / `5376 MiB` RAM (reflecting `vault-ai-proxy`'s `25m` CPU request / `64Mi` memory limit) and document `vault-ai-proxy` (`:8080` data/probes, `127.0.0.1:8081` loopback kill switch) in `docs/platform-architecture.html`, `scripts/check-arch-doc-sync.py`, `ARCHITECTURE.md`, `DEPENDENCIES.md`, `contracts/CONTRACT_ISOLATION_BUDGET.md`, and `contracts/CONTRACT_PROBES_READINESS.md`.

- [ ] **Step 5: Run the full verification & quality gate suite**

Run:
```bash
(cd gateway && go test -v ./... && go vet ./...)
(cd services/vault-proxies && go test -v -race ./... && go vet ./...)
python3 -m unittest discover tests/preflight
python3 -m unittest discover tests/review_cycle
./scripts/render-validate.sh local-k3s
./scripts/render-validate.sh vps-k3s
python3 scripts/check-arch-doc-sync.py --worktree
```
Expected: ALL commands exit `0` (`Status: PASSED` on both `local-k3s` and `vps-k3s`, `[ARCH-DOC-SYNC PASS]`).

---

### Task 5: Zero-Extraction Hardening & Zero-Data-Loss OpenBao Unseal Key Regeneration

- **Files**:
  - Create: `deploy/platform/vault/policies/raft-snapshot.hcl`
  - Create: `scripts/vault-reset-unseal-key.sh`
  - Modify: `services/vault-proxies/cmd/ai-proxy/main.go`
  - Modify: `services/vault-proxies/cmd/ai-proxy/main_test.go`
  - Modify: `scripts/vault-configure-k8s-auth.sh`
  - Modify: `deploy/platform/governance/manual-change-policy.yaml`
  - Modify: `scripts/platform-preflight.py`
  - Modify: `tests/preflight/test_ai_gateway_contract.py`
- **Steps**:
  - [x] **Step 1**: Store `geminiKey` as mutable `[]byte` in `vault-ai-proxy`, scrub bytes to `0x00` (`wipeGeminiKeyLocked()`) on Kill-Switch `STOP` or quota trip, and block HTTP redirects (`CheckRedirect`).
  - [x] **Step 2**: Bind `openbao-backup` to `deploy/platform/vault/policies/raft-snapshot.hcl` (`sys/storage/raft/snapshot` read-only) in `scripts/vault-configure-k8s-auth.sh`, restrict `serviceaccounts/token` to `governance/operator-readonly` in `manual-change-policy.yaml`, and enforce `observability` `hostPath` isolation in `scripts/platform-preflight.py`.
  - [x] **Step 3**: Add `scripts/vault-reset-unseal-key.sh` to regenerate a lost OpenBao Unseal Key and sync new PostgreSQL role passwords into `postgres-0` over the local Unix socket without losing database tables or rows.
