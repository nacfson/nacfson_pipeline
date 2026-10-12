# Implementation Plan: Platform-Owned TLS Termination

**Branch**: `008-platform-tls-termination` | **Date**: 2026-10-12 | **Spec**: [spec.md](spec.md)

**Input**: Feature specification from `specs/008-platform-tls-termination/spec.md`

**HTML version**: [agy_plan/html/platform-tls-termination-plan.html](../../agy_plan/html/platform-tls-termination-plan.html)

## Summary

The platform terminates TLS for every public hostname at Traefik, and holds every certificate and key in namespace `platform`. A single Traefik `TLSStore` named `default` lists the platform's certificate Secrets, and Traefik picks one by SNI. Project routes declare only `tls: {}`.

The change also:

- moves `pn-tls` out of `proj-pn`, which removes the quota collision that keeps `pn.nacfson.cloud` untrusted;
- sets the ACME solver Service to `ClusterIP`;
- deletes the PN solver NetworkPolicy, and later narrows the cert-manager admission exception;
- adds preflight rules that reject project-side TLS objects and uncovered hostnames;
- adds a read-only renewal check script;
- amends Constitution Principle V for edge TLS keys.

Rollout is two PRs, so `auth.nacfson.cloud` never loses its trusted certificate ([research R6](research.md#r6-migration-order-without-losing-the-sign-in-certificate-user-story-4-sc-007)).

## Technical Context

**Language/Version**: YAML manifests (Kustomize via `kubectl kustomize`, Flux `kustomize.toolkit.fluxcd.io/v1`); Python 3.12 (preflight); Bash (verify script)

**Primary Dependencies**: cert-manager v1.21.2 (vendored), Traefik v3 as bundled with k3s `v1.35.9+k3s1` (`traefik.io/v1alpha1` `IngressRoute`, `TLSStore`), FluxCD, Let's Encrypt ACME (HTTP-01)

**Storage**: Kubernetes Secrets of type `kubernetes.io/tls` in namespace `platform`, plus ACME account Secrets in `cert-manager` (needs Amendment 2.2.0, [R7](research.md#r7-constitution-principle-v-and-edge-tls-keys-fr-014))

**Testing**: `scripts/render-validate.sh <env>` (kustomize, flux build, kubeconform, render-invariants); `pytest tests/preflight`; `scripts/platform-preflight.py`; `scripts/verify-tls.sh` (new); external `curl`/`openssl`

**Target Platform**: k3s single node on the Oracle VM (vps-k3s, `168.110.101.69`) and local k3s (local-k3s)

**Project Type**: GitOps infrastructure repository (platform layers plus project layers)

**Performance Goals**: None beyond normal TLS handshake cost. Certificate issued within 15 min of reconcile (SC-007).

**Constraints**:

- No NodePort or LoadBalancer in projects.
- No TLS artifacts in project namespaces.
- The sign-in host stays trusted throughout.
- Environment differences only through Flux patches.
- No `kube-system` changes (Traefik is k3s-managed).

**Scale/Scope**: 2 hostnames per environment today (`auth`, `pn`); the procedure scales per hostname. Let's Encrypt rate limits are not a concern at this size.

All unknowns are resolved in [research.md](research.md). No `NEEDS CLARIFICATION` remains.

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

Checked against the committed constitution, **v2.1.0** (`origin/main`).

| Principle | Assessment | Result |
|---|---|---|
| I. Declarative GitOps | Every change is in Git and reconciled by Flux in dependency order. Flux neither generates nor delivers key material; cert-manager does. The one possible break-glass delete of stale `proj-pn` Challenges is documented and leaves Git unchanged. | PASS |
| II. Isolation & Restricted PSS | Removes cert-manager writes and solver pods from `proj-pn`. The solver Service becomes ClusterIP (no node port anywhere). Project namespaces keep Restricted PSS and quota unchanged. | PASS (improves) |
| III. Identity & public surface | Public routes unchanged. The ACME path is answered on port 80 only during a challenge, only for platform-listed hosts (same as PR #26). Admin endpoints stay private. | PASS |
| IV. Bounded resources | No new long-running containers. Transient solver pods run in `platform` and use cert-manager's default solver requests/limits; they are counted in the platform reservation, not a project slice. | PASS |
| V. Zero plaintext secrets / Vault | Edge TLS keys are Kubernetes Secrets in `platform`. Principle V forbids Secrets for backend credentials delivered to nodes or project pods. Edge keys are ingress material, but the text does not say so. | **CONDITIONAL**: requires Amendment 2.2.0 approval before PR A merges (see Complexity Tracking) |
| VI. Portability | Issuer and hostnames differ only through `clusters/<env>/layers.yaml` patches. Projects need no code change (FR-011). | PASS |
| VII. Lifecycle signals | Probes untouched. `PUBLIC_APP_URL` stays `https://…`. | PASS |
| Constraint: Controlled ingress bypass | Preflight gains TLS-1…TLS-6. NodePort rejection unchanged. | PASS |

**Gate decision**: Proceed with design. Implementation of PR A is **blocked** until the maintainer approves Amendment 2.2.0.

**Post-design re-check (after Phase 1)**: The data model, contracts and quickstart add no new violations. V remains conditional on the amendment; everything else passes.

## Project Structure

### Documentation (this feature)

```text
specs/008-platform-tls-termination/
├── spec.md
├── plan.md                         # This file
├── research.md                     # Phase 0
├── data-model.md                   # Phase 1
├── quickstart.md                   # Phase 1
├── contracts/
│   ├── project-route-tls.md
│   ├── certificate-inventory.md
│   └── preflight-tls-rules.md
├── checklists/requirements.md
├── handoff-project-pn.md
├── handoff-session.md
└── tasks.md                        # Phase 2 (/speckit-tasks, not created here)
```

### Source Code (repository root)

```text
deploy/platform/tls/
├── cluster-issuers.yaml            # PR A: ACME solvers serviceType: ClusterIP
├── certificates.yaml               # PR A: pn-tls → namespace platform; renewBefore 720h on both
├── tls-store.yaml                  # PR A: NEW TLSStore platform/default {auth-tls, pn-tls}
└── kustomization.yaml              # PR A: add tls-store.yaml

deploy/platform/ingress/
└── ingress-allowlist.yaml          # PR B: tls: {} (was secretName: auth-tls)

deploy/platform/governance/
└── manual-change-policy.yaml       # PR B: cert-manager clause limited to platform, cert-manager

deploy/projects/pn/
├── workloads/ingress-route.yaml    # PR A: tls: {} (was secretName: pn-tls)
└── boundary/
    ├── acme-solver-network-policy.yaml   # PR A: DELETE
    └── kustomization.yaml                # PR A: drop the resource

scripts/
├── platform-preflight.py           # PR A: check_edge_tls (TLS-1..6); PR B: TLS-2 for platform routes
└── verify-tls.sh                   # PR A: NEW read-only status and external check

tests/preflight/
├── test_platform_preflight.py      # PR A: cases for TLS-1..6
└── fixtures/                       # PR A: valid_tls_render.yaml, tls_*.yaml; update door_port_mismatch, sign_in_none_door

.specify/memory/constitution.md     # PR A: Amendment 2.2.0 (Principle V clarification), on top of origin/main
docs/tls-certificates.md            # PR A: platform-owned model; PR B: final state
```

Not changed: `clusters/*/layers.yaml` (the existing `tls` layer patches target `kind: Certificate, name: pn-tls` with no namespace, so they still apply), and `clusters/*/projects/proj-pn.yaml` (the route host patches are unchanged).

**Structure Decision**: Keep the existing layer layout. The inventory lives in the existing `tls` layer (`dependsOn: cert-manager, ingress, project-boundaries`). No new Flux layer is needed.

## Key Diffs (PR A)

```diff
# deploy/platform/tls/cluster-issuers.yaml (both ACME issuers)
     solvers:
       - http01:
           ingress:
             ingressClassName: traefik
+            serviceType: ClusterIP
```

```diff
# deploy/platform/tls/certificates.yaml
-# Traefik reads a TLS Secret only from the IngressRoute's own namespace,
-# so each certificate lives next to the route that serves it.
+# All edge certificates live in namespace platform and are served through
+# TLSStore platform/default (tls-store.yaml). Projects never hold TLS objects.
 ...
-  name: pn-tls
-  namespace: proj-pn
-  labels:
-    app.kubernetes.io/part-of: project-pn
+  name: pn-tls
+  namespace: platform
+  labels:
+    app.kubernetes.io/part-of: platform
 spec:
   secretName: pn-tls
+  renewBefore: 720h
```

```diff
# deploy/projects/pn/workloads/ingress-route.yaml
-  # Certificate proj-pn/pn-tls (deploy/platform/tls). HTTP is redirected by platform/http-to-https.
-  tls:
-    secretName: pn-tls
+  # The platform selects the certificate by hostname (TLSStore platform/default).
+  tls: {}
```

```diff
# deploy/platform/governance/manual-change-policy.yaml (PR B)
         (request.userInfo.username.startsWith('system:serviceaccount:cert-manager:') &&
+          request.namespace in ['platform', 'cert-manager'] &&
           request.resource.group in [...] &&
```

## Rollout

| Step | Action | Exit criterion |
|---|---|---|
| 0 | Maintainer approves Amendment 2.2.0 | Approval recorded in the PR |
| 1 | PR A (temp branch from `origin/main`, explicit file staging) | CI green: render-validate and platform-preflight for both environments |
| 2 | Flux reconciles PR A | `verify-tls.sh --external` passes for both hosts; `proj-pn` has no cert-manager objects (break-glass cleanup if needed) |
| 3 | PR B | CI green |
| 4 | Flux reconciles PR B | `auth` stays trusted during the sampling loop ([quickstart §6](quickstart.md#6-migration-safety-user-story-4-sc-007)); final quickstart pass |

## Risks

| Risk | Mitigation |
|---|---|
| The stale `proj-pn` Challenge keeps a finalizer after the Certificate moves | The cert-manager admission exception still covers `proj-pn` until PR B. Break-glass delete as fallback. |
| Let's Encrypt rate limit after repeated failures | Switch the `tls` layer patch to `letsencrypt-staging` to debug. Production limits are per hostname set and are not reached at 2 hosts. |
| Another `TLSStore default` appears (e.g., k3s add-on) | TLS-6 in preflight. Traefik logs a conflict. Operators check with `kubectl get tlsstore -A`. |
| Traefik cluster-wide Secret read | Accepted and documented ([R8](research.md#r8-who-can-read-edge-private-keys)). Follow-up: `defaultTLSResourcesNamespace` through a `kube-system` HelmChartConfig. |

## Complexity Tracking

| Violation | Why Needed | Simpler Alternative Rejected Because |
|---|---|---|
| Principle V: private keys stored as Kubernetes Secrets (edge TLS in `platform`, ACME accounts in `cert-manager`) | Traefik must hold the key in-process for every TLS handshake. It cannot call Vault per handshake. This already exists for `auth-tls` since PR #26. | Vault plus a sync controller still ends in a Kubernetes Secret for Traefik, with more moving parts. Amendment 2.2.0 scopes the exception to platform namespaces and forbids any project copy. |
