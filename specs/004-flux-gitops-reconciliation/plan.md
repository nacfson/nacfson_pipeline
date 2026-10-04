# Implementation Plan: Flux GitOps Reconciliation

**Branch**: short-lived branches from `main`, each merged through a pull request | **Date**: 2026-10-03 | **Spec**: [spec.md](spec.md)

**Input**: Feature specification in `specs/004-flux-gitops-reconciliation/spec.md`.

The setup script reports `004-flux-gitops-reconciliation` as its branch field. The work was drafted on `feature/002-internal-cluster-vault`. No branch-creation hook is configured.

**Delivery workflow**: there is no long-lived feature branch. Each task or small task group is a short-lived branch from `main` and merges through a pull request that passes the required checks (the gate applies once T053 is in place). The first PR (T001) brings over the spec artifacts, constitution v1.4.1 and `.specify/feature.json`.

## Summary

Install Flux 2.9.5 (only `source-controller` and `kustomize-controller`) as ordinary workloads in the existing K3s cluster. The spec 003 Ansible bootstrap installs it as a new, idempotent final stage, so a clean VM reaches a running reconciler with no separate manual step. Flux pulls the now-public GitHub repository anonymously and applies the existing `deploy/` declarations through a small graph of Flux `Kustomization` layers with explicit `dependsOn`.

Environment differences (hostnames, cookie domain) use inline Flux patches in each environment's entrypoint. Flux variable substitution is **forbidden**: a local `flux build --dry-run` showed it rewrites Keycloak's `${VAULT:keycloak_db_password}` to `null` and blanks 23 shell `${…}` references in Jobs and CronJobs.

Persistent data and the namespaces that hold it are protected from pruning. Project workloads are reconciled under a project-scoped ServiceAccount. A new `platform-preflight` CI check renders every layer with the same Flux build engine, validates schemas, and enforces the 1/n budget, isolation rules, and the memory-limit freeze before anything can merge to the protected `main`.

The cluster itself rejects manual changes: a built-in ValidatingAdmissionPolicy, applied by the `governance` layer, denies writes in managed scope unless they come from Flux, the project reconciler, system components, or a deliberately impersonated break-glass group. The workstation keeps only a read-only, short-lived kubeconfig.

The manual `apply-release.sh`, `rollback-release.sh`, `preflight-budget.sh` path, and the duplicate environment kustomizations are retired.

## Technical Context

**Language/Version**: YAML (Kubernetes, Kustomize, Flux v1 APIs, `admissionregistration.k8s.io/v1` with CEL); Ansible (existing spec 003 playbook) for the install stage; Python 3.12 for the `platform-preflight` script; Bash for CI glue and `operator-kubeconfig.sh`.

**Primary Dependencies**:
- Flux CLI and controllers 2.9.5 (`source-controller` v1.9.5, `kustomize-controller` v1.9.5), pinned.
- Kubernetes ValidatingAdmissionPolicy (built into the K3s 1.35 API server; no extra component).
- kubeconform, pinned version, with Flux 2.9.5 `crd-schemas` and the Traefik CRD schema catalog.
- PyYAML, pinned, for the preflight script.
- Ansible per `bootstrap/requirements.txt` (unchanged).
- gitleaks (existing workflow).
- No Helm controller, image automation, notification controller, SOPS, Kyverno/Gatekeeper, or Forgejo mirror.

**Storage**: No new storage. Existing PVCs (`postgres-data-postgres-0`, both backup PVCs) and StatefulSet-templated PVCs (`data-openbao-0`) are protected or outside Flux's inventory ([research R6](research.md#r6-prune-protection-for-persistent-data)).

**Testing**:
- CI: offline `flux build kustomization --dry-run` for every layer in every environment, kubeconform, the layer and policy invariants, and `platform-preflight` unit fixtures (over budget, zero projects, NodePort/hostPath/privileged, memory shrink, missing measurement). `ansible-playbook --syntax-check` on the extended playbook (existing lint workflow).
- Live acceptance on the operator's local VM (the test VM: disposable, K3s v1.35, environment `local-k3s`), following [quickstart.md](quickstart.md): bootstrap install and re-run, self-upgrade from Git, convergence, drift revert, prune, PVC survival, revert rollback, suspend/resume, recovery after reboot and with an unreachable source, tenant denial, manual-change denial. The VPS gets only the non-destructive smoke steps in quickstart E.

**Target Platform**: K3s `v1.35.9+k3s1` single node (spec 003), environments `local-k3s` and `vps-k3s`. The manifests stay portable to EKS/GKE (Principle VI); Flux and ValidatingAdmissionPolicy behave the same there.

**Project Type**: GitOps configuration, one Ansible stage, and CI validation (no application code changes).

**Performance Goals**: merged change applied in ≤ 5 min (source poll 1 min plus immediate Kustomization trigger on a new artifact); drift and prune corrected in ≤ 10 min (Kustomization interval 5 min); status for all layers from one `flux get kustomizations` call.

**Constraints**:
- Reconciler memory reservation ≤ 256 MiB (memory limits of 128 MiB per controller).
- No repository credential in the cluster; no inbound access to the API; no kubeconfig with write access off the node.
- No postBuild substitution.
- No source-content edits to workload manifests beyond annotations, explicit namespaces, the OpenBao probe correction ([R5](research.md#r5-sealed-vault-must-read-as-not-ready-without-being-killed)), resource bounds on `postgres-init-job` (the only unbounded container found by the render audit), and exact version tags for `postgres` and `keycloak` (constitution v1.4.1 Principle I). New governance files are additions, not edits.
- The bootstrap never updates or downgrades an existing Flux.

**Scale/Scope**: 2 environments, 9 layers, 1 project (`pn`), about 55 rendered objects. One cluster per environment; no management cluster.

## Constitution Check

**Pre-research gate: PASS** (against constitution v1.4.1).

| Principle / Gate | Design treatment | Result |
| --- | --- | --- |
| I. Continuous GitOps | In-cluster pull-based Flux; protected `main`; `dependsOn` ordering; drift correction and prune; PVCs and data namespaces prune-disabled; no secrets; anonymous HTTPS pull from the public repo; the one-time installation is automated by the bootstrap; the "manual changes prohibited" rule is now enforced by the cluster, with break-glass (suspend/resume, unseal, emergencies) through a deliberate impersonated group; rollback = `git revert`; images: Flux pinned to exact tag `v1.9.5`, `postgres` and `keycloak` moved from floating to exact tags, project PN images keep their digests; the three project-built `:latest` images are a recorded exception (Complexity Tracking) | PASS (justified exception) |
| II. Workload isolation | Project pods unchanged. Project workloads are reconciled by `pn-reconciler`, scoped by a Role to workload kinds in `proj-pn`. Flux controllers mount API tokens; see Complexity Tracking. The break-glass group and read-only SA are operator identities, never mounted in pods. | PASS (justified exception) |
| III. Identity / decoupled authz | No change to ingress allowlist, gateway, or Keycloak exposure. Hostnames are moved into environment patches only. | PASS |
| IV. Bounded resources / 1/n | Flux controllers patched to explicit requests and limits and counted in the platform reservation. `platform-preflight` computes the rendered peak footprint (replicas + surge, Jobs, CronJobs), enforces requests == limits for projects, and the 1/n slice. The admission policy adds no pods. | PASS |
| V. Persistence and zero plaintext secrets | Flux stores, decrypts, and delivers no secrets; no SOPS. Vault placeholders and shell references are preserved because substitution is forbidden. PostgreSQL and vault data are protected from pruning. The workstation token is short-lived and never stored as a Secret; `view` excludes Secrets. | PASS |
| VI. Portability | One entrypoint per environment with inline patches; shared `deploy/` bases unchanged; ValidatingAdmissionPolicy is upstream Kubernetes | PASS |
| Gate: Capacity & Budget | `platform-preflight` is a required check per environment; fails closed without a node measurement; includes the Flux footprint. The first VPS deployment is checked only against local-k3s; see Complexity Tracking. | PASS (justified exception) |
| Gate: Memory freeze | `platform-preflight` compares the candidate's project memory limits with the merge-base render and rejects decreases | PASS |
| Gate: Multi-arch images | Flux images are multi-arch (GHCR `fluxcd/*`); no new custom images | PASS |
| Gate: Manifest validation | Pinned Flux build engine (the same one the controller uses) plus pinned kubeconform for K8s 1.35 | PASS |
| Gate: Repository secret hygiene | gitleaks on every PR. Publication precondition: full-history scan plus rotation of the credentials SOPS-encrypted in `be85073` | PASS (precondition task) |

**Post-design re-check: PASS.** The Phase 1 artifacts add no new services, secrets, or exposure. Behaviour changes to existing components are: the OpenBao probe split (makes the sealed state visible, Story 2.3), the bootstrap's new final stage (R10, R17), and the admission policy that denies manual writes (R16). The new privilege is the break-glass group (`cluster-admin`), reachable only by impersonation from the node's K3s admin credential, which already has full rights.

## Project Structure

### Documentation (this feature)

```text
specs/004-flux-gitops-reconciliation/
├── plan.md              # This file
├── research.md          # Phase 0 decisions R1–R18
├── data-model.md        # Spec entities → concrete Flux/K8s objects and files
├── quickstart.md        # Live validation guide (test VM, then VPS)
├── validation.md        # Live validation evidence (created during implementation)
├── contracts/
│   ├── reconciliation-layers.md      # Layer graph, per-layer settings, annotations, env patches
│   ├── platform-preflight.md         # CI gate inputs/outputs/exit codes/rules
│   ├── operations.md                 # Install, status, deploy, rollback, break-glass, unseal, publication
│   ├── bootstrap-reconciler-stage.md # NEW: Ansible stage 4
│   └── manual-change-policy.md       # NEW: admission policy, break-glass, read-only identity
├── checklists/requirements.md
└── tasks.md             # /speckit-tasks output
```

### Source Code (repository root)

```text
bootstrap/                                 # spec 003 playbook, extended
├── bootstrap.yml                          # + gitops_environment input; + stage 4; new next_action
└── tasks/
    ├── preflight.yml                      # + assert clusters/<env>/flux-system exists
    └── reconciler.yml                     # NEW: detect → copy → apply → verify

clusters/                                  # NEW: one entrypoint per environment
├── local-k3s/
│   ├── kustomization.yaml                 # resources: flux-system, layers.yaml (capacity.yaml, projects/ excluded)
│   ├── flux-system/
│   │   ├── kustomization.yaml             # gotk-components + gotk-sync + controller resource patches
│   │   ├── gotk-components.yaml           # generated: flux install 2.9.5 --export
│   │   └── gotk-sync.yaml                 # GitRepository (public HTTPS, main) + root Kustomization
│   ├── layers.yaml                        # 8 platform Flux Kustomizations (incl. projects) + inline env patches
│   ├── projects/                          # applied by the projects layer (R18)
│   │   ├── kustomization.yaml             # lists proj-pn.yaml
│   │   └── proj-pn.yaml                   # Flux Kustomization proj-pn (ns proj-pn) + its env patches
│   └── capacity.yaml                      # observed node allocatable (preflight input only)
└── vps-k3s/                               # same shape, different patches/capacity

deploy/
├── platform/
│   ├── governance/    + kustomization.yaml; explicit ns on 2 objects; prune-disabled namespaces
│   │                  + manual-change-policy.yaml, break-glass-rbac.yaml, operator-access.yaml (NEW)
│   ├── vault/         kustomization.yaml (proxies removed); probe split; prune-disabled ns + backup PVC
│   ├── vault-proxies/ NEW dir: db-proxy.yaml, registry-proxy.yaml (moved) + kustomization.yaml
│   ├── database/      + kustomization.yaml; prune-disabled PVCs; force + resources on init Job
│   ├── identity/      + kustomization.yaml (no content change)
│   └── ingress/       + kustomization.yaml (no content change)
├── projects/pn/
│   ├── boundary/      namespace, service-account, resource-quota, network-policy (moved)
│   │                  + reconciler-rbac.yaml (NEW: pn-reconciler SA, Role, RoleBinding)
│   └── workloads/     backend-*, frontend-deployment, ingress-route (moved)
├── kustomization.yaml          # REMOVED (replaced by clusters/*)
└── environments/               # REMOVED (replaced by clusters/*)

scripts/
├── platform-preflight.py       # NEW
├── requirements-preflight.txt  # NEW (pinned PyYAML)
├── render-validate.sh          # NEW: render every layer, kubeconform, invariants (CI and local)
├── render-invariants.py        # NEW: layer and policy invariants on the rendered output
├── operator-kubeconfig.sh      # NEW: read-only, 24 h workstation kubeconfig
├── verify-isolation.sh         # + break-glass mode for its test pod
├── verify-platform.sh          # retired-script calls replaced by render-validate / platform-preflight
├── vault-init.sh               # message no longer suggests a manual apply
├── apply-release.sh            # REMOVED
├── rollback-release.sh         # REMOVED
└── preflight-budget.sh         # REMOVED (superseded by platform-preflight.py)

tests/preflight/                # NEW: fixtures + unittest for platform-preflight
tests/vault/test_matrix_verification.sh   # deploy/environments renders replaced by render-validate.sh
.github/workflows/gitops.yaml   # NEW: render-validate + platform-preflight (matrix: env)
.github/workflows/secret-scan.yaml   # trigger widened to all PRs into main
.github/workflows/lint.yaml     # + clusters/ in yamllint; + gitops_environment in syntax-check
.gitignore                      # .deploy-baseline line removed
README.md                       # retired-script references replaced (pre-existing untracked file)
docs/operations-runbook.md      # REWRITTEN for the reconciled model
docs/bootstrap-scope.md         # AMENDED: bootstrap installs the reconciler only (R17)
SPEC.md                         # SYNCED first: §2, DEPLOY-01, DEPLOY-04, availability/secrets lines
```

**Structure Decision**: Keep `deploy/` as the shared, environment-neutral base (Principle VI) and add `clusters/<env>/` as the Flux entrypoints, following the standard Flux repository layout. Split `vault` into `vault` and `vault-proxies`, and split `projects/pn` into `boundary` and `workloads`. Both splits exist so the layer graph can express real dependencies and tenant boundaries ([R4](research.md#r4-layer-graph-dependencies-and-health), [R8](research.md#r8-project-scoped-reconciliation)). The manual-change policy lives in `governance`, the first layer, so it is active before any workload layer applies. Flux installation joins the existing bootstrap as one task file rather than a new playbook.

## Complexity Tracking

| Violation | Why Needed | Simpler Alternative Rejected Because |
|-----------|------------|-------------------------------------|
| Flux controller pods mount ServiceAccount API tokens (Principle II says workloads run with token automount disabled) | A reconciler must call the Kubernetes API; this is the feature itself. The restriction's purpose (project pods never reach the API) is unchanged: the token-bearing pods exist only in `flux-system`, and project workloads are applied under the scoped `pn-reconciler` identity. | Running Flux outside the cluster (host process, CI push, management cluster) needs the API exposed or an admin kubeconfig off-cluster, which is worse under Principles I and V. |
| OpenBao probe change (beyond "annotations only") | Spec Story 2.3 requires a sealed vault to show as not-ready. Today readiness reports a sealed vault as Ready (`sealedcode=204`), while liveness restarts it after about 75 s sealed. | Leaving the probes as they are makes Flux report the vault layer healthy while sealed, so dependents start against a sealed vault, and liveness keeps killing the pod during manual unseal. |
| `postgres-init-job` resource bounds and explicit `governance` namespaces (content edits) | FR-013 and Principle IV require bounded containers, and the init Job is the only unbounded one; the gate would block every PR. Two governance objects have no namespace, so where they land depends on who applies them (R12). Both are listed in the spec's Unchanged-components assumption. | Exempting the Job from the gate weakens the rule for everyone; leaving the namespace implicit makes the reconciled result differ from the manual one. |
| Removing `preflight-budget.sh` (not listed in FR-014) | `platform-preflight.py` replaces it. Keeping both would leave two diverging budget calculations; the old one hard-codes reservations and candidate figures that already disagree with the manifests (backend 250m vs 300m). | Wrapping the old script would keep the hard-coded figures that the rendered-manifest requirement (DEPLOY-04) is meant to remove. |
| Bootstrap scope widened (spec 003 said deployment is out of scope) | FR-002 removes the manual install step. The stage installs only Flux and is skipped when Flux exists (R10, R17). | A separate playbook or manual command keeps a second step; the K3s auto-deploy directory would fight Flux's self-upgrade. |
| Break-glass group bound to `cluster-admin` | FR-016 needs a write path for emergencies that the policy recognises. It is reachable only by impersonation from the node's K3s admin credential, which already holds full rights, so no new principal gains power. | Exempting `system:masters` from the policy would let every admin write without a deliberate step, which defeats Story 7. |
| First VPS deployment checked only against local-k3s capacity (Capacity & Budget gate) | `clusters/vps-k3s/capacity.yaml` can only be measured after the VPS bootstrap, and the bootstrap's last stage starts Flux, which applies `main` at once. Mitigation: before the VPS bootstrap, the operator confirms the VPS is at least as large as the measured test node and stops otherwise ([quickstart E0](quickstart.md#e-production-vps-k3s)); `platform-preflight (vps-k3s)` becomes a required check right after the measurement. Decided by the operator on 2026-10-03 (`/speckit-analyze` finding D2). | A Flux-only VPS entrypoint until measurement (one extra PR, and an entrypoint that temporarily differs from local-k3s) was rejected by the operator as an awkward design. |
| Three project-built images keep `:latest` (`platform-auth-gateway`, `vault-db-proxy`, `vault-registry-proxy`; constitution Principle I requires digests) | A digest exists only after the image is built and published, which is the image-publishing work listed as out of scope in the spec. Flux will apply these references as they are. The images are not published yet (a known blocker named in the spec), so their layers report not-ready and nothing runs from them; the image-publishing work must switch them to digests when it publishes them. Decided on 2026-10-04 (`/speckit-analyze` finding D5). | Publishing the images inside this feature would widen its scope to CI image builds; removing the three workloads would break `identity` and `vault-proxies`. |
