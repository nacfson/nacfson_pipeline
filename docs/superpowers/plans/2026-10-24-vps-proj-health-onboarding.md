# VPS `proj-health` Onboarding & OpenBao Seed Parity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Promote `proj-health` (`health.nacfson.cloud`) workloads to `vps-k3s`, add the missing `proj-health` OpenBao credentials to `vault-seed-inventory.sh` and `vault-rotate.sh`, and recover the stalled `database` → `identity` → `ingress` → `tls` → `projects` reconciliation chain on the VPS.

**Architecture:** `clusters/vps-k3s/projects/proj-health.yaml` declares the Flux `Kustomization` in namespace `proj-health` (`serviceAccountName: health-reconciler`) targeting `./deploy/projects/health/workloads` with `health.nacfson.cloud` patches on `IngressRoute/project-health-ingress` and `Deployment/health-backend`, and is registered in `clusters/vps-k3s/projects/kustomization.yaml`. `clusters/local-k3s/projects/proj-health.yaml` drops its duplicate `spec.images` override so `deploy/projects/health/workloads/*.yaml` remains the single digest source for `update-project-images`. `scripts/vault-seed-inventory.sh` and `scripts/vault-rotate.sh` gain `kv/database/health-user` and `kv/projects/health/client-secret` so `postgres-init-job` (`PROJECT_SLUGS="pn,health"`), `vault-db-proxy`, and `keycloak` succeed on every cluster.

**Tech Stack:** FluxCD `kustomize.toolkit.fluxcd.io/v1`, Traefik `IngressRoute`, OpenBao KV v2 (`bao`), Bash (`shellcheck`), Python 3 `unittest` (`tests/preflight/`).

**Spec:** [`docs/superpowers/specs/2026-10-24-vps-proj-health-onboarding-design.md`](../specs/2026-10-24-vps-proj-health-onboarding-design.md), [`contracts/README.md`](../../../contracts/README.md) (§Quick Onboarding Checklist Steps 0–7), [`contracts/CONTRACT_TLS_INGRESS.md`](../../../contracts/CONTRACT_TLS_INGRESS.md), [`contracts/CONTRACT_PROBES_READINESS.md`](../../../contracts/CONTRACT_PROBES_READINESS.md), [`contracts/CONTRACT_DATABASE.md`](../../../contracts/CONTRACT_DATABASE.md).

## Global Constraints

- Never push directly to `main`. Create an isolated git worktree branched from `origin/main` so the uncommitted files on the main checkout are untouched.
- Project namespaces (`proj-health`) MUST NOT contain `Certificate`, `Issuer`, `TLSStore`, `TLSOption`, or `kubernetes.io/tls` `Secret` resources, and `IngressRoute/project-health-ingress` MUST keep `entryPoints: [websecure]` and `tls: {}`.
- `PUBLIC_APP_URL` (`https://health.nacfson.cloud` on `vps-k3s`, `https://health.example.com` on `local-k3s`) MUST appear in `ALLOWED_ORIGINS` on `Deployment/health-backend`.
- Image digests live in `deploy/projects/health/workloads/{backend,frontend}-deployment.yaml` (`@sha256:...`); Flux `Kustomization/proj-health` manifests under `clusters/*/projects/` MUST NOT set `spec.images`.
- Whenever `clusters/` or `contracts/` change, `docs/platform-architecture.html` MUST be updated in the same commit (`scripts/check-arch-doc-sync.py`).
- Before `git commit`, run the `code-review` skill on the staged diff so the review receipt contains `"pipeline": "code-review"`.

## Review Focus

Most likely first. Each line is pinned by a test in the task named in brackets.

- Stale `spec.images` override in `clusters/*/projects/proj-health.yaml`: Flux `spec.images` silently overwrites the `@sha256:...` pins in `deploy/projects/health/workloads/*.yaml`, defeating `update-project-images`. Neither `local-k3s` nor `vps-k3s` `proj-health.yaml` may contain `spec.images`. [Task 1]
- Unpatched `IngressRoute/project-health-ingress` hostname in `vps-k3s`: leaving base `Host(\`health.example.com\`)` in the `vps-k3s` render fails `check_edge_tls` (Rule TLS-5) because `Certificate/health-tls` in `vps-k3s` covers `health.nacfson.cloud`. Both `/spec/routes/0/match` and `/spec/routes/1/match` must match `health.nacfson.cloud`. [Task 1]
- Missing `PUBLIC_APP_URL` in `ALLOWED_ORIGINS` on `vps-k3s` `health-backend`: browser API calls from `https://health.nacfson.cloud` fail CORS unless `https://health.nacfson.cloud` is in `ALLOWED_ORIGINS` and equals `PUBLIC_APP_URL`. [Task 1]
- Missing `kv/database/health-user` and `kv/projects/health/client-secret` in `scripts/vault-seed-inventory.sh`: `postgres-init-job` (`PROJECT_SLUGS="pn,health"`) and `keycloak` (`secret-fetcher`) run `curl -fsSL` under `set -euo pipefail`, so an unseeded `health` secret crashes `postgres-init-job` and blocks `database` → `identity` → `ingress` → `tls`. [Task 2]
- Handbook drift in `docs/platform-architecture.html`: `DAG_LAYERS['project-boundaries-health']` lists `850m / 1536Mi` instead of `700m / 768Mi` (`deploy/projects/health/boundary/resource-quota.yaml`), and `DAG_LAYERS['projects'].yaml['vps-k3s']` omits `proj-health.yaml`. [Task 1]

---

## File Structure

| File | Responsibility |
| :--- | :--- |
| `clusters/vps-k3s/projects/proj-health.yaml` | Create. Flux `Kustomization` (`proj-health` in `proj-health`, `serviceAccountName: health-reconciler`) with `health.nacfson.cloud` patches for `IngressRoute/project-health-ingress` and `Deployment/health-backend`. |
| `clusters/vps-k3s/projects/kustomization.yaml` | Modify. Register `proj-health.yaml` alongside `proj-pn.yaml`. |
| `clusters/local-k3s/projects/proj-health.yaml` | Modify. Remove `spec.images` override and declare explicit `health.example.com` patches matching `clusters/local-k3s/projects/proj-pn.yaml`. |
| `docs/platform-architecture.html` | Modify. Sync `DAG_LAYERS['project-boundaries-health']` quota (`700m / 768Mi`) and `DAG_LAYERS['projects'].yaml['vps-k3s']` (`proj-pn.yaml & proj-health.yaml`). |
| `scripts/vault-seed-inventory.sh` | Modify. Seed `kv/database/health-user` (`username="user_health"`) and `kv/projects/health/client-secret` (`client_id="project-health"`). |
| `scripts/vault-rotate.sh` | Modify. Support `postgres-health-db-password` (`kv/database/health-user`) and `project-health-client-secret` (`kv/projects/health/client-secret`). |
| `tests/preflight/test_platform_preflight.py` | Modify. Add `test_25_project_health_vps_and_local_render_and_vault_seeds` asserting rendered `vps-k3s` and `local-k3s` `proj-health` invariants and Vault seed coverage. |

---

### Task 1: Promote `proj-health` to `clusters/vps-k3s/projects/` and sync the handbook

**Files:**
- Create: `clusters/vps-k3s/projects/proj-health.yaml`
- Modify: `clusters/vps-k3s/projects/kustomization.yaml:1-5`
- Modify: `clusters/local-k3s/projects/proj-health.yaml:1-29`
- Modify: `docs/platform-architecture.html:1962-1996`
- Modify: `tests/preflight/test_platform_preflight.py:415-420`

**Interfaces:**
- Consumes: `./deploy/projects/health/workloads` (`IngressRoute/project-health-ingress`, `Deployment/health-backend`, `Deployment/health-frontend`, `Service/health-backend`, `Service/health-frontend`) and `Certificate/health-tls` in `clusters/vps-k3s/layers.yaml`.
- Produces: `clusters/vps-k3s/projects/proj-health.yaml` and `clusters/local-k3s/projects/proj-health.yaml` without `spec.images`, rendered by `./scripts/render-validate.sh vps-k3s` and `./scripts/render-validate.sh local-k3s` with `projectCount == 2` in `platform-preflight.py`.

- [ ] **Step 1: Create an isolated worktree from `origin/main`**

```bash
git fetch origin main
git worktree add .worktrees/vps-proj-health -b feat/vps-proj-health origin/main
cd .worktrees/vps-proj-health
```

Expected: `git status --short` is empty and `git rev-parse --short HEAD` matches `origin/main`.

- [ ] **Step 2: Write the failing test in `tests/preflight/test_platform_preflight.py`**

Add `test_25_project_health_vps_and_local_render_accepted` to `TestPlatformPreflight`:

```python
    def test_25_project_health_vps_and_local_render_accepted(self):
        """Both local-k3s and vps-k3s render proj-pn + proj-health (projectCount == 2) without spec.images overrides."""
        import yaml

        for env, expected_host in (("local-k3s", "health.example.com"), ("vps-k3s", "health.nacfson.cloud")):
            proj_kustom = os.path.join(BASE_DIR, "clusters", env, "projects", "kustomization.yaml")
            with open(proj_kustom, encoding="utf-8") as fh:
                kdoc = yaml.safe_load(fh)
            self.assertIn("proj-health.yaml", kdoc.get("resources") or [])

            proj_health_path = os.path.join(BASE_DIR, "clusters", env, "projects", "proj-health.yaml")
            self.assertTrue(os.path.exists(proj_health_path), f"Missing {proj_health_path}")
            with open(proj_health_path, encoding="utf-8") as fh:
                hdoc = yaml.safe_load(fh)
            self.assertEqual(hdoc["metadata"]["name"], "proj-health")
            self.assertEqual(hdoc["metadata"]["namespace"], "proj-health")
            self.assertEqual(hdoc["spec"]["serviceAccountName"], "health-reconciler")
            self.assertNotIn("images", hdoc["spec"], f"{proj_health_path} must not override workload digest pins via spec.images")

            render_res = subprocess.run(
                [os.path.join(BASE_DIR, "scripts", "render-validate.sh"), env],
                capture_output=True,
                text=True,
                check=True,
            )
            rendered_file = render_res.stdout.strip().splitlines()[-1]
            try:
                rc, data, err = self.run_preflight(
                    rendered_file,
                    os.path.join(BASE_DIR, "clusters", env, "capacity.yaml"),
                    env=env,
                )
                self.assertEqual(rc, 0, err or data.get("rejectionReason"))
                self.assertEqual(data.get("validationStatus"), "PASSED")
                self.assertEqual(data.get("projectCount"), 2)

                with open(rendered_file, encoding="utf-8") as rf:
                    rdocs = [d for d in yaml.safe_load_all(rf) if isinstance(d, dict)]
                ing = next(
                    d for d in rdocs
                    if d.get("kind") == "IngressRoute"
                    and (d.get("metadata") or {}).get("name") == "project-health-ingress"
                )
                self.assertEqual(ing["spec"]["routes"][0]["match"], f"Host(`{expected_host}`)")
                self.assertEqual(ing["spec"]["routes"][1]["match"], f"Host(`{expected_host}`) && PathPrefix(`/api`)")

                dep = next(
                    d for d in rdocs
                    if d.get("kind") == "Deployment"
                    and (d.get("metadata") or {}).get("name") == "health-backend"
                )
                env_list = dep["spec"]["template"]["spec"]["containers"][0]["env"]
                env_map = {e["name"]: e["value"] for e in env_list}
                self.assertEqual(env_map.get("PUBLIC_APP_URL"), f"https://{expected_host}")
                self.assertIn(f"https://{expected_host}", env_map.get("ALLOWED_ORIGINS", "").split(","))
            finally:
                if os.path.exists(rendered_file):
                    os.remove(rendered_file)
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `python3 -m unittest tests.preflight.test_platform_preflight.TestPlatformPreflight.test_25_project_health_vps_and_local_render_accepted -v`
Expected: FAIL (`clusters/local-k3s/projects/proj-health.yaml` contains `images`, and `proj-health.yaml` is missing from `clusters/vps-k3s/projects/kustomization.yaml`).

- [ ] **Step 4: Create `clusters/vps-k3s/projects/proj-health.yaml`, update `clusters/vps-k3s/projects/kustomization.yaml` and `clusters/local-k3s/projects/proj-health.yaml`, and sync `docs/platform-architecture.html`**

Create `clusters/vps-k3s/projects/proj-health.yaml` mirroring [`clusters/vps-k3s/projects/proj-pn.yaml`](../../../clusters/vps-k3s/projects/proj-pn.yaml):
- `metadata.name: proj-health`, `metadata.namespace: proj-health`
- `spec.serviceAccountName: health-reconciler`
- `spec.dependsOn`: `project-boundaries-health`, `ingress`, `vault-proxies` (all in `namespace: flux-system`)
- `spec.path: ./deploy/projects/health/workloads`
- `spec.patches`:
  - `IngressRoute/project-health-ingress`: replace `/spec/routes/0/match` with ``Host(`health.nacfson.cloud`)`` and `/spec/routes/1/match` with ``Host(`health.nacfson.cloud`) && PathPrefix(`/api`)``.
  - `Deployment/health-backend`: container `backend` env `ALLOWED_ORIGINS="http://localhost:8081,http://localhost:19006,tauri://localhost,http://tauri.localhost,https://health.nacfson.cloud"` and `PUBLIC_APP_URL="https://health.nacfson.cloud"`.

Update `clusters/local-k3s/projects/proj-health.yaml` to remove `spec.images` and apply the same two patches for `health.example.com` (matching `clusters/local-k3s/projects/proj-pn.yaml`).

Add `- proj-health.yaml` to `clusters/vps-k3s/projects/kustomization.yaml`.

In `docs/platform-architecture.html`:
- Update `DAG_LAYERS['project-boundaries-health']` description and YAML from `850m CPU, 1536Mi RAM` / `project-health-sa` to `700m CPU, 768Mi RAM` / `health-workload-sa` (matching `deploy/projects/health/boundary/`).
- Update `DAG_LAYERS['projects'].yaml['vps-k3s']` to list both `proj-pn.yaml` and `proj-health.yaml` (`Host(\`pn.nacfson.cloud\`)` and `Host(\`health.nacfson.cloud\`)`).

- [ ] **Step 5: Run tests and validation gates**

Run:
```bash
python3 -m unittest discover tests/preflight -v
./scripts/render-validate.sh local-k3s
./scripts/render-validate.sh vps-k3s
python3 scripts/check-arch-doc-sync.py --worktree
```
Expected: All unit tests pass, both renders exit 0 with `projectCount: 2`, and `check-arch-doc-sync.py` exits 0.

---

### Task 2: Add `proj-health` credentials to `vault-seed-inventory.sh` and `vault-rotate.sh`

**Files:**
- Modify: `scripts/vault-seed-inventory.sh:167-187`
- Modify: `scripts/vault-rotate.sh:34-72,122-126,204-207`
- Modify: `tests/preflight/test_platform_preflight.py`

**Interfaces:**
- Consumes: `PROJECT_SLUGS: "pn,health"` in `deploy/platform/database/postgres-init-job.yaml` and `kv/data/projects/health/client-secret` in `deploy/platform/identity/keycloak-deployment.yaml`.
- Produces: `seed_secret "kv/database/health-user" username="user_health" password="..."` and `seed_secret "kv/projects/health/client-secret" client_id="project-health" client_secret="..."` in `scripts/vault-seed-inventory.sh`; rotation targets `postgres-health-db-password` and `project-health-client-secret` in `scripts/vault-rotate.sh`.

- [ ] **Step 1: Write the failing test in `tests/preflight/test_platform_preflight.py`**

Add `test_26_vault_seed_and_rotate_cover_all_project_slugs` to `TestPlatformPreflight`:

```python
    def test_26_vault_seed_and_rotate_cover_all_project_slugs(self):
        """vault-seed-inventory.sh and vault-rotate.sh must cover every slug in postgres-init-job PROJECT_SLUGS."""
        import yaml

        init_job_path = os.path.join(BASE_DIR, "deploy", "platform", "database", "postgres-init-job.yaml")
        with open(init_job_path, encoding="utf-8") as fh:
            job_doc = yaml.safe_load(fh)
        init_envs = {
            e["name"]: e["value"]
            for e in job_doc["spec"]["template"]["spec"]["initContainers"][0]["env"]
        }
        slugs = [s.strip() for s in init_envs["PROJECT_SLUGS"].split(",") if s.strip()]
        self.assertIn("pn", slugs)
        self.assertIn("health", slugs)

        with open(os.path.join(BASE_DIR, "scripts", "vault-seed-inventory.sh"), encoding="utf-8") as fh:
            seed_script = fh.read()
        with open(os.path.join(BASE_DIR, "scripts", "vault-rotate.sh"), encoding="utf-8") as fh:
            rotate_script = fh.read()

        for slug in slugs:
            self.assertIn(f'seed_secret "kv/database/{slug}-user"', seed_script)
            self.assertIn(f'username="user_{slug}"', seed_script)
            self.assertIn(f'seed_secret "kv/projects/{slug}/client-secret"', seed_script)
            self.assertIn(f'client_id="project-{slug}"', seed_script)
            self.assertIn(f"postgres-{slug}-db-password) echo \"kv/database/{slug}-user\"", rotate_script)
            self.assertIn(f"project-{slug}-client-secret) echo \"kv/projects/{slug}/client-secret\"", rotate_script)
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `python3 -m unittest tests.preflight.test_platform_preflight.TestPlatformPreflight.test_26_vault_seed_and_rotate_cover_all_project_slugs -v`
Expected: FAIL (`seed_secret "kv/database/health-user"` not found in `scripts/vault-seed-inventory.sh`).

- [ ] **Step 3: Update `scripts/vault-seed-inventory.sh` and `scripts/vault-rotate.sh`**

In `scripts/vault-seed-inventory.sh`:
- Add `seed_secret "kv/database/health-user" username="user_health" password="$(openssl rand -hex 24)"`
- Add `seed_secret "kv/projects/health/client-secret" client_id="project-health" client_secret="$(openssl rand -hex 24)"`

In `scripts/vault-rotate.sh`:
- Add `postgres-health-db-password` (`kv/database/health-user`) and `project-health-client-secret` (`kv/projects/health/client-secret`) to `usage()`, `get_vault_path()`, `test-adoption`, and `checklist`.

- [ ] **Step 4: Run tests and `tests/vault/test_matrix_verification.sh`**

Run:
```bash
python3 -m unittest discover tests/preflight -v
./tests/vault/test_matrix_verification.sh
```
Expected: All `tests/preflight` tests and all 9 sections of `test_matrix_verification.sh` pass.

- [ ] **Step 5: Run `code-review` skill, commit, and open PR**

Stage the modified files, run the `code-review` skill to record a verified review receipt (`"pipeline": "code-review"`), commit on `feat/vps-proj-health`, push, and open a PR targeting `main`.

---

### Task 3: Live VPS (`oracle-free`) Recovery & End-to-End Verification

**Files:**
- None (live cluster operations on `vps-k3s` via `scripts/vault-seed-inventory.sh`, `scripts/vault-configure-k8s-auth.sh`, and `flux`).

**Interfaces:**
- Consumes: merged PR from Tasks 1–2 and unsealed OpenBao (`openbao-0` in `vault`) on `vps-k3s` (`168.110.101.69`).
- Produces: all Flux Kustomizations `Ready=True`, `Certificate/health-tls` `Ready=True`, `proj-health` pods `Running (1/1)`, and `https://health.nacfson.cloud` returning HTTP `200` with a valid Let's Encrypt certificate and live flows visible in Hubble UI.

- [ ] **Step 1: Verify OpenBao is unsealed and seed the missing `health` secrets + policies on `vps-k3s`**

From `debian-0` (with SSH tunnel `ssh -fN -L 6443:127.0.0.1:6443 opc@168.110.101.69` and `export KUBECONFIG=~/.kube/nacfson-vps-k3s.yaml`, or via `ssh oracleCloud`):

```bash
KUBE_EXEC="ssh opc@168.110.101.69 sudo k3s kubectl" ./scripts/vault-seed-inventory.sh
KUBE_EXEC="ssh opc@168.110.101.69 sudo k3s kubectl" ./scripts/vault-configure-k8s-auth.sh
```

Expected: `Already exists` for existing `pn`/platform secrets, `Seeding kv/database/health-user...` and `Seeding kv/projects/health/client-secret...`, and `db-proxy` / `keycloak-broker` HCL policies synced.

- [ ] **Step 2: Re-trigger `database` (`postgres-init-job`) and downstream Flux layers on `oracle-free`**

On `[opc@oracle-free ~]$`:

```bash
# Re-reconcile git source and layers now that Vault has health-user and health/client-secret
sudo k3s flux reconcile source git flux-system
sudo k3s flux reconcile kustomization vault-proxies --with-source
sudo k3s flux reconcile kustomization database --with-source
sudo k3s flux reconcile kustomization identity --with-source
sudo k3s flux reconcile kustomization ingress --with-source
sudo k3s flux reconcile kustomization tls --with-source
sudo k3s flux reconcile kustomization projects --with-source
sudo k3s flux reconcile kustomization proj-health -n proj-health
```

Expected: `sudo k3s kubectl get kustomizations.kustomize.toolkit.fluxcd.io -A` shows `READY=True` across all layers (`database`, `vault-proxies`, `identity`, `ingress`, `tls`, `projects`, `proj-pn`, and `proj-health`).

- [ ] **Step 3: Verify `health-tls` certificate, `https://health.nacfson.cloud`, and Hubble UI flows**

On `debian-0`:

```bash
curl -Iv https://health.nacfson.cloud
```

Expected:
- TLS handshake succeeds with `issuer: C=US, O=Let's Encrypt` and `subject: CN=health.nacfson.cloud` (no self-signed warning).
- `HTTP/2 200` from `health-frontend`.
- In Hubble UI (`http://127.0.0.1:12005`), selecting `proj-health` shows live `FORWARDED` flows from `traefik` → `health-frontend:80` / `health-backend:8080` and `health-backend` → `vault-db-proxy:5432`.
