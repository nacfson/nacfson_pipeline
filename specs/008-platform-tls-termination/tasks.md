---
description: "Task list for 008 Platform-Owned TLS Termination"
---

# Tasks: Platform-Owned TLS Termination

**Input**: Design documents from `specs/008-platform-tls-termination/`

**Prerequisites**: [plan.md](plan.md), [spec.md](spec.md), [research.md](research.md), [data-model.md](data-model.md), [contracts/](contracts/), [quickstart.md](quickstart.md)

**Tests**: The spec requires "pre-deployment validation MUST reject" (FR-010), so the preflight rules and their fixtures are part of the deliverable. They are included in US2. No other test tasks are generated.

**Organization**: Tasks are grouped by user story. PR A delivers Setup, Foundational, US1, US2 and US3. PR B delivers US4. Each story ends with a checkpoint.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependency on an unfinished task)
- **[Story]**: User story from spec.md (US1–US4)
- Paths are relative to the repository root. In this repo, "source" means manifests under `deploy/`, scripts under `scripts/`, and tests under `tests/`.

## Ground Rules (apply to every task)

- Work in a **separate git worktree** created from `origin/main` (T001). The main working tree belongs to other sessions; never edit or stage `clusters/*/projects/proj-pn.yaml`, `deploy/projects/pn/workloads/backend-deployment.yaml`, `specs/007-*` or the main tree's `.specify/memory/constitution.md`.
- Stage files by explicit path only. Ship with the `temp-branch-pr` skill (`/home/nacfson/.gemini/config/skills/temp-branch-pr/SKILL.md`). Never push to `main`.
- After each manifest change, run `./scripts/render-validate.sh local-k3s` and `./scripts/render-validate.sh vps-k3s` inside the worktree.

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: An isolated workspace with working validation tools and a recorded baseline.

- [x] T001 Create an isolated worktree on a temp branch from the latest main: `git fetch origin && git worktree add ../nacfson_pipeline-008 -b tmp/platform-tls-store-$(date +%Y%m%d-%H%M) origin/main`, then copy `specs/008-platform-tls-termination/` and `agy_plan/html/platform-tls-termination-plan.html` from the main tree into the worktree (`../nacfson_pipeline-008/specs/008-platform-tls-termination/`, `../nacfson_pipeline-008/agy_plan/html/`)
- [x] T002 [P] Put the validation tools on PATH: `kubectl` and `yamllint` from `/home/nacfson/.gemini/antigravity-cli/brain/dba9ec25-07b2-4dde-8c69-a5574485ee0a/scratch/bin` and `.../scratch/venv/bin`; install `flux` and `kubeconform` at the versions pinned in `.github/workflows/gitops.yaml` (`FLUX_VERSION`, `KUBECONFORM_VERSION`); `pip install -r scripts/requirements-preflight.txt pytest` into the scratch venv
- [x] T003 Record the baseline in the worktree: run `./scripts/render-validate.sh local-k3s`, `./scripts/render-validate.sh vps-k3s`, `python3 scripts/platform-preflight.py --environment <env> --candidate <render> --capacity clusters/<env>/capacity.yaml` for both environments, and `python3 -m pytest tests/preflight -q`; save the outputs to `specs/008-platform-tls-termination/validation.md` under a "Baseline" heading (all must pass before any change)

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Governance and cluster facts that block any deploy.

**⚠️ CRITICAL**: PR A must not merge until T004 is approved and T006 is confirmed.

- [x] T004 Write Constitution Amendment 2.2.0 in the worktree's `.specify/memory/constitution.md` (completed and merged in origin/main via PR #31)
- [x] T005 Ask the maintainer to approve Amendment 2.2.0 (approved and merged in origin/main via PR #31)
- [ ] T006 [P] Ask the user to run on the VM and paste the output: `sudo k3s kubectl get crd tlsstores.traefik.io` (must exist), `sudo k3s kubectl get tlsstore -A` (must be empty, or no store named `default` other than ours), `sudo k3s kubectl get certificate -A` and `sudo k3s kubectl describe challenge -n proj-pn | tail -15` (expected: quota rejection of `services.nodeports`); record the results in `specs/008-platform-tls-termination/validation.md` under "Cluster preconditions"

**Checkpoint**: Amendment approved, Traefik `TLSStore` CRD present, no conflicting `default` store.

---

## Phase 3: User Story 1 - Visitors reach every project over trusted HTTPS (Priority: P1) 🎯 MVP

**Goal**: `https://pn.nacfson.cloud` serves a publicly trusted certificate, and `auth.nacfson.cloud` stays trusted.

**Independent Test**: From outside the cluster, `curl https://pn.nacfson.cloud/` succeeds without `-k`, and `openssl s_client -servername pn.nacfson.cloud` shows a Let's Encrypt issuer (quickstart §4).

### Implementation for User Story 1

- [x] T007 [P] [US1] Add `serviceType: ClusterIP` under `spec.acme.solvers[0].http01.ingress` (next to `ingressClassName: traefik`) for both `letsencrypt-staging` and `letsencrypt-prod` in `deploy/platform/tls/cluster-issuers.yaml`, with a comment: "cert-manager defaults the solver Service to NodePort; Principle II forbids node ports" (research R2)
- [x] T008 [P] [US1] Move Certificate `pn-tls` to the platform in `deploy/platform/tls/certificates.yaml`: set `metadata.namespace: platform`, label `app.kubernetes.io/part-of: platform`, keep `secretName: pn-tls`, `dnsNames: [pn.example.com]`, `issuerRef selfsigned` and the ECDSA 256 `rotationPolicy: Always` key; replace the header comment "Traefik reads a TLS Secret only from the IngressRoute's own namespace…" with "All edge certificates live in namespace platform and are served through TLSStore platform/default (tls-store.yaml). Projects never hold TLS objects." and the per-certificate comment with "Served through TLSStore platform/default"
- [x] T009 [P] [US1] Create `deploy/platform/tls/tls-store.yaml` with `apiVersion: traefik.io/v1alpha1`, `kind: TLSStore`, `metadata.name: default`, `metadata.namespace: platform`, label `app.kubernetes.io/part-of: platform`, `spec.certificates: [{secretName: auth-tls}, {secretName: pn-tls}]`; per data-model §3, `spec.defaultCertificate` and `spec.defaultGeneratedCert` "MUST stay unset" (add a comment saying so, and why: unknown or missing SNI must get Traefik's generated certificate, never a project certificate)
- [x] T010 [US1] Add `- tls-store.yaml` to `resources` in `deploy/platform/tls/kustomization.yaml` and update its header comment to mention the store (depends on T009)
- [x] T011 [P] [US1] Replace `tls: {secretName: pn-tls}` with `tls: {}` in `deploy/projects/pn/workloads/ingress-route.yaml`, and change the comment to "The platform selects the certificate by hostname (TLSStore platform/default). HTTP is redirected by platform/http-to-https."
- [x] T012 [US1] Run render-validate for both environments in the worktree; confirm in the vps render that `Certificate pn-tls` has `namespace: platform`, `dnsNames: [pn.nacfson.cloud]`, `issuerRef.name: letsencrypt-prod` (the existing `tls` layer patch in `clusters/vps-k3s/layers.yaml` targets `kind: Certificate, name: pn-tls` with no namespace, so it must still apply), that the `TLSStore` is present, and that `serviceType: ClusterIP` appears twice; record the results in `specs/008-platform-tls-termination/validation.md`

**Checkpoint**: The render contains the platform-owned PN certificate and the store. After deploy, US1's independent test passes.

---

## Phase 4: User Story 2 - A project publishes a hostname with routing only (Priority: P2)

**Goal**: The project namespace holds no TLS machinery, and preflight rejects any project TLS object or uncovered hostname.

**Independent Test**: The `proj-pn` render contains only routing and application objects (plus the boundary objects), and `pytest tests/preflight` shows one rejection per rule TLS-1…TLS-6 (contracts/preflight-tls-rules.md).

### Implementation for User Story 2

- [x] T013 [P] [US2] Delete `deploy/projects/pn/boundary/acme-solver-network-policy.yaml` and remove `- acme-solver-network-policy.yaml` from `resources` in `deploy/projects/pn/boundary/kustomization.yaml`
- [x] T014 [US2] Implement `check_edge_tls(project_docs, platform_docs)` in `scripts/platform-preflight.py`, next to `check_project_doors`, returning violations with `"rule": 2`, project messages ending with the existing suffix `"; last accepted door is not replaced"`, and these exact rules from contracts/preflight-tls-rules.md:
  - **TLS-1**: project kinds `Certificate`, `CertificateRequest`, `Issuer`, `Order`, `Challenge`, `TLSStore`, `TLSOption`, or `Secret` with `type: kubernetes.io/tls` → "TLS objects are platform-owned; <Kind> is not allowed in project namespace <ns>"
  - **TLS-2**: project `IngressRoute` whose `spec.tls` is missing or has any key (`secretName`, `certResolver`, `store`, `options`, `domains`) → "route must declare tls: {} and let the platform select the certificate"
  - **TLS-3**: `spec.entryPoints` ≠ `["websecure"]` → "project route entry point must be websecure only"
  - **TLS-4**: route `match` without a literal `` Host(`…`) ``, or containing `HostRegexp`, `HostSNI` or `HostHeader` → "route match must name its hostname with Host()"
  - **TLS-5**: hostname not in `covered_hosts` → "hostname <h> has no platform certificate; add it to deploy/platform/tls first"
  - **TLS-6**: more than one `TLSStore` named `default` or one outside `platform`; a listed `secretName` with no `Certificate` in `platform` having that `spec.secretName`; `defaultCertificate`/`defaultGeneratedCert` set; one hostname in two inventory certificates; an edge `Certificate` outside `platform` → "certificate inventory: <reason>"
  
  Compute `covered_hosts` = lowercase union of `spec.dnsNames` of `platform` Certificates whose `spec.secretName` is listed in `TLSStore platform/default`. Extract hostnames with the regex `Host\(([^)]*)\)` and then each backticked literal inside the group. A render with no `TLSStore` is not a TLS-6 violation by itself.
- [x] T015 [US2] Call `check_edge_tls` in `main()` of `scripts/platform-preflight.py` right after `door_violations = check_project_doors(projects_dict)`, extending `violations` and setting `rejection_reason` to the first TLS violation's `detail` when no earlier reason is set, using the same pattern as the door block (depends on T014)
- [x] T016 [P] [US2] Create fixture `tests/preflight/fixtures/valid_tls_render.yaml`: a minimal passing render (copy the structure of `valid_render.yaml`) plus `TLSStore platform/default` listing `auth-tls` and `pn-tls`, `Certificate platform/auth-tls` (`auth.example.com`) and `Certificate platform/pn-tls` (`pn.example.com`), and a `proj-pn` `IngressRoute` with `entryPoints: [websecure]`, `tls: {}`, `` Host(`pn.example.com`) `` routes using `strip-forged-headers` and existing Services/ports
- [x] T017 [P] [US2] Create the six negative fixtures in `tests/preflight/fixtures/`, each a copy of `valid_tls_render.yaml` with one change:
  - `tls_project_certificate.yaml`: adds `Certificate proj-pn/pn-tls` (TLS-1)
  - `tls_route_secret_name.yaml`: route `tls: {secretName: pn-tls}` (TLS-2)
  - `tls_route_web_entrypoint.yaml`: `entryPoints: [web, websecure]` (TLS-3)
  - `tls_route_host_regexp.yaml`: a match ``HostRegexp(`.+`)`` (TLS-4)
  - `tls_uncovered_host.yaml`: host `shop.example.com` (TLS-5)
  - `tls_store_missing_secret.yaml`: store lists `ghost-tls` with no Certificate (TLS-6)
- [x] T018 [US2] Update `tests/preflight/fixtures/door_port_mismatch.yaml` and `tests/preflight/fixtures/sign_in_none_door.yaml`: add `tls: {}` and `entryPoints: [websecure]` to their `IngressRoute`, plus a `TLSStore platform/default` and a matching `platform` `Certificate` for their hostname, so the only violation is the one each test already expects
- [x] T019 [US2] Add tests to `tests/preflight/test_platform_preflight.py`, following its existing subprocess pattern: `valid_tls_render.yaml` passes; each of the six `tls_*.yaml` fixtures is rejected with rule 2 and a `detail` starting with the message prefix from T014 (depends on T014–T018)
- [x] T020 [US2] Run `python3 -m pytest tests/preflight -q` and the preflight for both rendered environments in the worktree; all must pass; record the results in `specs/008-platform-tls-termination/validation.md`

**Checkpoint**: The project namespace holds no TLS artifacts in the render, and preflight enforces FR-002, FR-003 and FR-010.

---

## Phase 5: User Story 3 - The operator relies on automatic, observable certificate renewal (Priority: P3)

**Goal**: Certificates renew with ≥ 30 days of validity left, and a failing renewal is visible ≥ 14 days before expiry with read-only access.

**Independent Test**: `./scripts/verify-tls.sh` exits 0 on a healthy cluster, and exits non-zero (naming the hostname) when a Certificate is not Ready, its renewal is overdue, or fewer than 14 days remain (quickstart §3).

### Implementation for User Story 3

- [x] T021 [US3] Add `renewBefore: 720h` to both Certificates (`auth-tls`, `pn-tls`) in `deploy/platform/tls/certificates.yaml`, with the comment "renew when 30 days remain (FR-006), independent of CA lifetime" (data-model §2: "`spec.renewBefore` `720h`: At most 30 days before expiry (FR-006)"); depends on T008 (same file)
- [x] T022 [P] [US3] Create executable `scripts/verify-tls.sh` (`set -euo pipefail`, read-only, uses the current kubeconfig, or `KUBECTL="sudo k3s kubectl"` when set):
  - For each `certificate` in namespace `platform`, print name, `spec.dnsNames`, the `Ready` condition, `status.notAfter`, `status.renewalTime` and `status.lastFailureTime`.
  - Exit 1 if any Certificate has `Ready` ≠ `True`, `renewalTime` more than 1 h in the past, or fewer than 14 days to `notAfter`.
  - With `--external`, for each hostname run `openssl s_client -connect <h>:443 -servername <h> -verify_return_error`, and fail if verification fails or the issuer contains `TRAEFIK DEFAULT CERT`.
  - Support `--help`, and keep the script ShellCheck-clean (follow the style of `scripts/verify-platform.sh`).
- [x] T023 [US3] Run `shellcheck scripts/verify-tls.sh` and `yamllint deploy/platform/tls/`; then render-validate both environments; record the results in `specs/008-platform-tls-termination/validation.md`

**Checkpoint**: Renewal timing is explicit and operator-visible.

---

## Phase 6: User Story 4 - Existing deployment migrates without extra downtime (Priority: P3)

**Goal**: Ship PR A, verify, then ship PR B (auth to the store, narrowed admission exception) with `auth` trusted throughout.

**Independent Test**: The quickstart §6 sampling loop shows `auth.nacfson.cloud` never serving `TRAEFIK DEFAULT CERT` during either reconcile, and `proj-pn` ends with no certificate, key, solver, TLS NetworkPolicy or TLS admission exception.

### PR A delivery

- [ ] T024 [US4] Ship PR A with the `temp-branch-pr` skill from the worktree branch (T001): stage only the files changed in T004, T007–T011, T013–T019, T021–T022 plus `specs/008-platform-tls-termination/**`, `agy_plan/html/platform-tls-termination-plan.html` and the docs from T032–T034 (by explicit path); commit `feat(platform): serve all edge certificates from a platform TLS store`; open the PR with links to `specs/008-platform-tls-termination/plan.md` and the amendment approval; report branch, SHA and PR URL
- [ ] T025 [US4] After CI is green and PR A merges, ask the user to run the quickstart §6 sampling loop from outside during the reconcile, then quickstart §3 and §4 on the VM and from outside; expected results: `pn` serves a Let's Encrypt certificate within 15 minutes, `auth` never shows `TRAEFIK DEFAULT CERT`, `kubectl get certificate -A` lists only `platform/auth-tls` and `platform/pn-tls` as Ready, and `kubectl get secret -n proj-pn --field-selector type=kubernetes.io/tls` and `kubectl get secret -n proj-pn -l cert-manager.io/next-private-key` return no resources; record the results in `specs/008-platform-tls-termination/validation.md`
- [ ] T026 [US4] If `sudo k3s kubectl get certificate,certificaterequest,order,challenge -n proj-pn` or TLS/next-private-key secrets still exist 10 minutes after the reconcile, give the user the break-glass cleanup `sudo k3s kubectl --as=break-glass:nacfson --as-group=platform:break-glass delete challenge,order,certificate,certificaterequest -n proj-pn --all`, ask them to record it per operations §12, and re-check until empty

### PR B delivery

- [ ] T027 [P] [US4] Replace `tls: {secretName: auth-tls}` with `tls: {}` in `deploy/platform/ingress/ingress-allowlist.yaml`, and update its comment to "Certificate selected by hostname from TLSStore platform/default" (only after T025 confirms the store serves `auth-tls`)
- [ ] T028 [P] [US4] Narrow the cert-manager clause in ValidatingAdmissionPolicy `gitops-managed-namespaces` in `deploy/platform/governance/manual-change-policy.yaml` by inserting `request.namespace in ['platform', 'cert-manager'] &&` right after `request.userInfo.username.startsWith('system:serviceaccount:cert-manager:') &&` (only after T026 confirms `proj-pn` holds no cert-manager objects)
- [ ] T029 [US4] Extend TLS-2 in `check_edge_tls` in `scripts/platform-preflight.py` to also check `IngressRoute`s in namespace `platform` whose `spec.entryPoints` contains `websecure` (they must declare `tls: {}`), and add fixture `tests/preflight/fixtures/tls_platform_route_secret_name.yaml` plus a test case in `tests/preflight/test_platform_preflight.py` (depends on T027 so the real render passes)
- [ ] T030 [US4] Run render-validate, preflight for both environments, and `python3 -m pytest tests/preflight -q`; then ship PR B with the `temp-branch-pr` skill (new temp branch from updated `origin/main`, explicit staging of T027–T029 and doc updates), commit `refactor(platform): serve sign-in certificate from the TLS store and scope cert-manager admission`
- [ ] T031 [US4] After PR B merges, ask the user to re-run the quickstart §6 sampling loop during the reconcile, then quickstart §7 and §4; expected results: `auth` stays trusted throughout, `kubectl get ingressroute -n platform public-auth-allowlist -o jsonpath='{.spec.tls}'` returns `{}`, and both hosts are trusted; record the results in `specs/008-platform-tls-termination/validation.md`

**Checkpoint**: The migration is complete. All spec success criteria are measurable as met.

---

## Phase 7: Polish & Cross-Cutting Concerns

**Purpose**: Documentation and final validation.

- [x] T032 [P] Update `docs/tls-certificates.md`: set the status to the platform-owned model; replace the "Traefik reads a TLS Secret only from the route's namespace" explanation with the `TLSStore platform/default` design; update the "What changed" table (store, ClusterIP solver, `pn-tls` in `platform`, PN NetworkPolicy removed, preflight TLS rules, `verify-tls.sh`); add an "Add a hostname" section linking `specs/008-platform-tls-termination/contracts/certificate-inventory.md`; add an "Operator checks" section with `./scripts/verify-tls.sh [--external]`
- [x] T033 [P] Update `specs/008-platform-tls-termination/handoff-project-pn.md` §5 to show the delivered state (with PR numbers), and set the state table in `specs/008-platform-tls-termination/handoff-session.md` to "implemented"
- [x] T034 [P] Update the Mermaid diagrams in `docs/tls-certificates.md` (request flow and Flux layer order) to show the store in `platform` and no TLS objects in `proj-pn`
- [ ] T035 Run the full `specs/008-platform-tls-termination/quickstart.md` (§1–§7), mark each spec success criterion SC-001…SC-007 as met or not met with evidence in `specs/008-platform-tls-termination/validation.md`, and list any follow-ups (for example, Traefik `defaultTLSResourcesNamespace`)
- [ ] T036 Remove the worktree after both PRs merge (`git worktree remove ../nacfson_pipeline-008`), and confirm the main working tree still shows only the other sessions' uncommitted files

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies.
- **Foundational (Phase 2)**: Depends on T001. T004 and T006 can run in parallel. T005 depends on T004. Blocks the PR A merge (T024).
- **US1 (Phase 3)**: Depends on Setup. Implementation can run alongside Phase 2; deploy waits for Phase 2.
- **US2 (Phase 4)**: Depends on Setup. T014–T019 need US1's manifest shape (T008, T009, T011) only so that T020's real render passes.
- **US3 (Phase 5)**: T021 depends on T008 (same file). T022 is independent.
- **US4 (Phase 6)**: Depends on US1–US3, Phase 2 and T032–T033 (shipped with PR A). PR B tasks (T027–T031) depend on T025 and T026.
- **Polish (Phase 7)**: T032–T034 before T024 (they ship in PR A, and are updated again in PR B); T035–T036 last.

### User Story Dependencies

```text
Setup ──▶ Foundational (approval gate) ───────────────┐
  │                                                   ▼
  ├──▶ US1 (store + pn move) ──▶ US2 (preflight) ──▶ PR A (T024) ──▶ verify (T025–T026) ──▶ PR B (T027–T031)
  └──▶ US3 (renewal visibility) ───────────────────────▲
```

### Within Each User Story

- Manifests before render checks.
- Preflight function (T014) before wiring (T015) before tests (T019).
- Verify on the cluster before narrowing the admission exception (T028).

---

## Parallel Examples

### User Story 1

```text
Task: "T007 Add serviceType: ClusterIP in deploy/platform/tls/cluster-issuers.yaml"
Task: "T008 Move Certificate pn-tls to platform in deploy/platform/tls/certificates.yaml"
Task: "T009 Create deploy/platform/tls/tls-store.yaml"
Task: "T011 tls: {} in deploy/projects/pn/workloads/ingress-route.yaml"
```

### User Story 2

```text
Task: "T013 Delete deploy/projects/pn/boundary/acme-solver-network-policy.yaml"
Task: "T016 Create tests/preflight/fixtures/valid_tls_render.yaml"
Task: "T017 Create six tls_*.yaml negative fixtures"
```

### User Story 3 and 4

```text
Task: "T022 Create scripts/verify-tls.sh"            # parallel with US2
Task: "T027 Auth route tls: {} in deploy/platform/ingress/ingress-allowlist.yaml"
Task: "T028 Narrow cert-manager clause in deploy/platform/governance/manual-change-policy.yaml"
```

---

## Implementation Strategy

### MVP First (User Story 1)

1. Phase 1 Setup, then Phase 2 (amendment approval and cluster preconditions).
2. Phase 3 (US1): pn becomes trusted once deployed. This alone fixes the outage.
3. **STOP and VALIDATE** with quickstart §4.

Shipping US1 alone is possible but leaves the dead NetworkPolicy and no guardrails. The recommended PR A bundles US1 + US2 + US3.

### Incremental Delivery

1. PR A = Setup + Foundational + US1 + US2 + US3 + docs → deploy → verify (T025–T026).
2. PR B = US4 finishing steps (auth to store, narrowed exception, platform-route rule) → deploy → verify (T031).
3. Final quickstart run and SC evidence (T035).

---

## Notes

- `[P]` tasks touch different files and have no unfinished dependencies.
- Cluster-side checks (T006, T025, T026, T031) need the user to run commands on the VM, because there is no SSH tunnel from this workstation.
- Avoid editing `clusters/*/layers.yaml` and `clusters/*/projects/proj-pn.yaml`: the existing patches already produce the right hostnames and issuers.
