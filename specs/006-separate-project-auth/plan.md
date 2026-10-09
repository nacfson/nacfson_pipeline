# Implementation Plan: Separate Project Entry from Sign-In

**Branch**: `006-separate-project-auth` | **Date**: 2026-10-09 | **Spec**: [spec.md](./spec.md)

**Input**: Feature specification from `specs/006-separate-project-auth/spec.md`

## Summary

Visitors reach a project on that project's own door. The door uses only ports the project's NetworkPolicy already allows from the platform ingress. The shared sign-in service, Keycloak behind the existing Go gateway, only answers who the user is, and only after a project asks.

Project PN keeps `signIn: shared`, the host route to `pn-frontend:80`, and the `/api` route to `pn-backend:8080`. `forward-auth` comes off those routes. A missing `signIn` declaration means `none`. Constitution 1.4.1 still forbids this door, so amendment 2.0.0 lands in the same candidate revision. Research is in [research.md](./research.md).

## Technical Context

**Language/Version**: Go 1.22+ standard library for the existing gateway; YAML for IngressRoute, NetworkPolicy, and the project declaration.

**Primary Dependencies**: Existing Traefik IngressRoute, existing gateway, existing Keycloak realm, existing Project PN NetworkPolicy. No new runtime module.

**Storage**: N/A. The sign-in choice is a declared field. Session state stays with the sign-in service.

**Testing**: `go test` for session return and confirmation; manifest checks for the door/network match; `curl` for the visitor trials in [quickstart.md](./quickstart.md).

**Target Platform**: The current platform targets: local k3s, VPS k3s, and the same declaration on EKS and GKE.

**Project Type**: Kubernetes platform change plus a narrow gateway behavior change.

**Performance Goals**: A `none` project answers while the sign-in service is stopped, 10 of 10 trials. A `shared` project shows its public address before any sign-in redirect, 10 of 10 trials.

**Constraints**: Gateway stays on the Go standard library. Auth-host allowlist stays limited to sign-in, callback, and sign-out. Admin, master realm, health, and metrics stay private. ClusterIP only. The door change and constitution 2.0.0 ship together.

**Scale/Scope**: One existing project, Project PN, plus the declaration rule for later projects. Two visitor addresses on PN. No new project is introduced by this feature.

## Constitution Check

*GATE: Evaluated against constitution 1.4.1 before research. Re-checked after Phase 1 design.*

| Principle / rule | Before research | After design |
| --- | --- | --- |
| I. Declarative GitOps | Pass. The door, choice, and amendment are Git declarations. | Pass. |
| II. Workload isolation | Pass if the door cannot add a port, a Service type, or a peer the NetworkPolicy does not already allow. | Pass. [project-door.md](./contracts/project-door.md) rejects those candidates. |
| III. Identity, current text | Fail. 1.4.1 says the gateway forwards a project token and checks the session before proxying. | Pass only when [constitution-amendment.md](./contracts/constitution-amendment.md) is in the same revision. The rewritten rule keeps the standard-library gateway, `(issuer, subject)`, and the ban on business permissions from identity alone. |
| IV. Budgets | Pass. No resource change. | Pass. |
| V. Secrets and data isolation | Pass. No new secret delivery. Projects still cannot reach the Keycloak database. | Pass. Confirmation egress is to the gateway only, and only for `shared`. |
| VI. Portability | Pass. Host names stay environment configuration. | Pass. |
| Fail-closed session authority, current text | Fail. The universal pre-proxy check would make a `none` project depend on Keycloak. | Pass under the amended wording: a failed confirmation leaves that part unsigned-in, and a `none` project still answers. |
| Public surface allowlist | Pass if project hosts are not added to the auth allowlist. | Pass. [session-return.md](./contracts/session-return.md) keeps the current allowlist. |

Initial result: two failures, both justified in Complexity Tracking. A revision that edits the Project PN door without the 2.0.0 amendment is still a gate failure.

Post-design result: pass, contingent on that amendment.

## Project Structure

### Documentation (this feature)

```text
specs/006-separate-project-auth/
├── plan.md
├── research.md
├── data-model.md
├── quickstart.md
├── contracts/
│   ├── project-door.md
│   ├── sign-in-choice.md
│   ├── session-return.md
│   └── constitution-amendment.md
└── tasks.md                 # not created by this command
```

### Source code (repository root)

```text
.specify/memory/constitution.md
SPEC.md
deploy/platform/ingress/traefik-middleware.yaml
deploy/platform/ingress/ingress-allowlist.yaml
deploy/projects/pn/workloads/ingress-route.yaml
deploy/projects/pn/boundary/network-policy.yaml
gateway/internal/auth/
gateway/tests/
specs/001-project-platform/contracts/project-manifest-spec.yaml
```

**Structure Decision**: This feature edits the existing platform and Project PN. It does not add a service. `ingress-allowlist.yaml` is listed because the plan checks that it stays unchanged.

## Complexity Tracking

| Violation | Why Needed | Simpler Alternative Rejected Because |
| --- | --- | --- |
| Principle III and the fail-closed rule in constitution 1.4.1 require a sign-in check before the project answers. | The accepted specification says the project answers first, and a project may have no connection to sign-in. | Keeping ForwardAuth on every project route preserves 1.4.1 and rejects the specification. A minor bump would hide a removed universal gate. |
| Amendment 2.0.0 must be approved by the maintainer before it is committed. | The constitution requires that approval for a major bump. | Writing the door change first and amending later leaves a revision that contradicts 1.4.1. |
