# GitHub Copilot Instructions — `nacfson_pipeline`

`nacfson_pipeline` is a declarative, multi-tenant Kubernetes GitOps platform (`gke`, `local-k3s`, and transitional `vps-k3s`). Every code change and pull request review is governed by [`.specify/memory/constitution.md`](../.specify/memory/constitution.md), [`REVIEW.md`](../REVIEW.md), [`AGENTS.md`](../AGENTS.md), and [`contracts/README.md`](../contracts/README.md).

## Non-Negotiable Platform & Security Rules

1. **No Comments in Code (Constitution v2.4.0)**:
   - Flag any newly added line, block, or trailing comments, commented-out code, Python docstrings, Go doc comments, or comments inside code embedded in manifests or workflows as blocking (`Important`). Explanations belong in Markdown documents.
   - Allowed exceptions: shebang lines (`#!`), Go compiler directives (`//go:build`, `//go:embed`), container build parser directives (`# syntax=`), linter/analyzer directives (`# shellcheck`, `# noqa`, `//nolint`), user-facing help strings, generated lockfiles (`.terraform.lock.hcl`, `go.sum`), and unchanged vendored files (`deploy/platform/cni/upstream/`, `deploy/platform/cert-manager/upstream/`, `.specify/scripts/`).
2. **Zero Third-Party Go Dependencies**:
   - `gateway/` and `services/vault-proxies/` must build with the Go `1.22` standard library only. Never allow external modules in `go.mod`.
3. **Zero Plaintext Secrets in Git or Pods**:
   - Backend credentials live exclusively inside OpenBao (`deploy/platform/vault/`) and execute through `services/vault-proxies/`. Never commit secrets or mount backend credentials into application pods via Kubernetes `Secret` objects or environment variables.
   - The only permitted Kubernetes TLS `Secret` resources are edge certificate keys in `platform` (`auth-tls`, `pn-tls`) and ACME account keys in `cert-manager`.
4. **Platform-Owned Edge TLS & Ingress Allowlist**:
   - Project namespaces (`proj-*`) must never contain `Certificate`, `Issuer`, `TLSStore`, `TLSOptions`, or TLS `Secret` resources, and project `IngressRoute` manifests must never set `tls.secretName`.
   - Public auth routes live only in `deploy/platform/ingress/ingress-allowlist.yaml`. Flux `postBuild` variable substitution is permitted only on the `ingress` layer for `AUTH_HOST`.
5. **Restricted Pod Security, Guaranteed QoS & $1/n$ Budgeting**:
   - Project containers must set `runAsNonRoot: true`, drop `ALL` Linux capabilities, disable service-account token auto-mounting, expose `ClusterIP` services only, and declare `requests == limits` fitting within their $1/n$ compute budget (`scripts/platform-preflight.py`).
   - Third-party infra images must use exact pinned version tags (never `:latest`); hosted project images (`deploy/projects/`) must use immutable `@sha256:...` digests.
   - `CiliumNetworkPolicy` egress rules must never allow unrestricted `0.0.0.0/0` or `world` without explicit FQDN/port locks.
6. **Probe Separation & Browser CORS**:
   - Dependent workloads must separate `startupProbe` (initial cluster hop), `readinessProbe` (continuous cluster hop / disconnect signal), and `livenessProbe` (process-local only), declare `restartPolicy: Always`, and include `PUBLIC_APP_URL` in backend `ALLOWED_ORIGINS`.
7. **Mandatory Architecture Handbook Sync**:
   - Any modification under `deploy/`, `clusters/`, `gateway/`, `services/`, `bootstrap/`, `contracts/`, `specs/`, `.specify/memory/constitution.md`, `scripts/platform-preflight.py`, `scripts/render-invariants.py`, `ARCHITECTURE.md`, or `DEPENDENCIES.md` must update `docs/platform-architecture.html` in the same pull request (`scripts/check-arch-doc-sync.py`).

## Code Review Standards (`REVIEW.md`)

- **Focus on Concrete Failure Mechanisms**: Prioritize authentication/OIDC/session-revocation bypasses, cross-host cookie leakage, secret exposure, overly broad RBAC or `CiliumNetworkPolicy` egress, $1/n$ budget/QoS violations, contract drift, and concurrency/correctness bugs.
- **Cite Exact Evidence & Avoid Noise**: Ground every comment in surrounding code context. Omit subjective style, formatting, or naming preferences. Cap minor non-blocking improvements (`Nit`) at **at most 2** per review pass, and group repeated instances of the same defect pattern rather than posting duplicate comments.
- **Skipped Vendored Paths**: Skip unchanged vendored manifests under `deploy/platform/cert-manager/upstream/`, `deploy/platform/cni/upstream/`, and `**/gotk-components.yaml` unless auditing an upstream version bump.
