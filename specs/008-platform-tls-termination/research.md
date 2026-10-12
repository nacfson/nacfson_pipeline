# Research: Platform-Owned TLS Termination

**Feature**: [spec.md](spec.md) · **Plan**: [plan.md](plan.md) · **Date**: 2026-10-12

Each section resolves one unknown from the plan's Technical Context. Format: Decision, Rationale, Alternatives considered.

---

## R1. Where certificates live and how Traefik finds them

**Decision**: Every edge `Certificate` and its Secret lives in namespace `platform`. A single Traefik `TLSStore` named `default`, in namespace `platform`, lists each Secret in `spec.certificates[].secretName`. Project `IngressRoute`s declare `tls: {}` with no `secretName`. Traefik selects the certificate by the SNI hostname the client sends.

**Rationale**:

- Traefik reads `IngressRoute.spec.tls.secretName` only from the route's own namespace. That rule is what forced PR #26 to put `pn-tls` (and its validation workload) into `proj-pn`.
- The Traefik docs say: "Traefik currently only uses the TLS Store named `default` … it is used by default on IngressRoute … there never is a need to actually reference it". `certificates[n].secretName` adds key/certificate pairs to that store. Source: [Traefik TLSStore reference](https://doc.traefik.io/traefik/reference/routing-configuration/kubernetes/crd/tls/tlsstore/).
- Only one store named `default` may exist cluster-wide, so the platform owning it is natural. Projects cannot create a `TLSStore` (render invariant 6 limits the project layer to Deployment, Service, ConfigMap and IngressRoute).
- Without `defaultCertificate`, Traefik keeps serving its generated default certificate for a missing or unknown SNI. That meets the spec's edge cases: a raw-IP visitor never receives a project's certificate.

**Alternatives considered**:

| Alternative | Rejected because |
|---|---|
| Keep per-project Certificates and fix only the solver `serviceType` | Fixes the outage, but keeps TLS objects, a firewall exception and an admission exception in every project (violates FR-002, FR-003, FR-013). |
| Replicate Secrets into project namespaces (reflector, ExternalSecrets) | Copies private keys into project namespaces (violates FR-002). Adds a controller. |
| Traefik built-in ACME (`certResolver`) | Stores keys in Traefik's `acme.json` in `kube-system`. That means a k3s `HelmChartConfig` change outside Flux-managed namespaces, and it is single-replica only. Loses cert-manager status objects (FR-015). |
| Wildcard `*.nacfson.cloud` through DNS-01 | Needs a DNS-provider API credential in the cluster. Out of scope (spec FR-009). Can be added later behind the same `TLSStore`. |
| Kubernetes Gateway API with cross-namespace `ReferenceGrant` | Bigger migration of every route. Not needed to meet the spec. |

---

## R2. HTTP-01 solver Service type

**Decision**: Set `spec.acme.solvers[].http01.ingress.serviceType: ClusterIP` on `letsencrypt-staging` and `letsencrypt-prod`.

**Rationale**: The cert-manager CRD documents `serviceType` as "If unset, defaults to NodePort". A NodePort is a perimeter bypass under Constitution Principle II. It is also why the `proj-pn` quota (`services.nodeports: "0"`) rejected the solver. Traefik reaches the solver through the in-cluster Service, so ClusterIP is sufficient. The vendored cert-manager v1.21.2 CRDs contain the field (6 occurrences in `upstream/cert-manager.yaml`).

**Alternatives considered**: Raise the project quota to allow NodePorts (violates Principle II). Leave NodePort for platform-only issuance (works today only because `platform` has no quota; still opens a node port during every challenge).

---

## R3. Where to enforce "project hostname must be covered" (FR-010)

**Decision**: Add a TLS door check to `scripts/platform-preflight.py` (`check_edge_tls`). It runs on the full rendered candidate for each environment in the `platform-preflight` required check.

**Rationale**: Hostnames are patched per environment (`clusters/<env>/projects/proj-pn.yaml`, `clusters/<env>/layers.yaml`). Only the rendered output shows the final pairs of hostnames and certificates. Preflight already loads the whole render, separates project and platform documents, and holds the existing door checks (`check_project_doors`). An admission policy (CEL) cannot see other objects such as the `TLSStore`, so it cannot check coverage.

**Alternatives considered**: `render-invariants.py` (runs on render too, but it holds structural invariants and has no project/door model). A ValidatingAdmissionPolicy (cannot cross-reference objects). A runtime check only (too late; FR-010 says "before deployment").

---

## R4. Which project-side TLS objects to reject (FR-002, FR-003)

**Decision**: `check_edge_tls` rejects, in project namespaces:

1. Kinds `Certificate`, `CertificateRequest`, `Issuer`, `Order`, `Challenge` (`cert-manager.io`, `acme.cert-manager.io`), and `TLSStore`, `TLSOption` (`traefik.io`).
2. `Secret` of type `kubernetes.io/tls`.
3. An `IngressRoute` whose `spec.tls` is missing, or has any key other than none (`secretName`, `certResolver`, `store`, `options`, `domains`).
4. An `IngressRoute` with an entry point other than `websecure`.
5. A route `match` without a literal `` Host(`…`) ``, or one using `HostRegexp`.

**Rationale**: These are exactly the artifacts PR #26 put into `proj-pn`, plus the ways a route could pick its own certificate. Rules 3–5 also stop a route that would catch any hostname and be answered with an unrelated certificate.

**Alternatives considered**: Rely on render invariant 6 alone. It covers only the project workloads layer, not objects that platform layers place into a project namespace (the PR #26 pattern).

---

## R5. Renewal timing and visibility (FR-006, FR-015, SC-005)

**Decision**:

- Set `renewBefore: 720h` (30 days) on every edge Certificate.
- Operator status comes from cert-manager status fields, read with the existing read-only binding.
- Add `scripts/verify-tls.sh` (read-only). It prints hostnames, `Ready`, `notAfter`, `renewalTime` and the last failure for each Certificate in `platform`, and exits non-zero when one of these is true:
  - `Ready` is not `True`
  - `renewalTime` is more than 1 hour in the past (renewal overdue)
  - fewer than 14 days of validity remain
- With `--external`, it also checks each hostname from outside with `openssl s_client` (issuer is not `TRAEFIK DEFAULT CERT`, certificate verifies).

**Rationale**:

- cert-manager's default renewal point is 2/3 of the lifetime. That gives 30 days for a 90-day certificate, but fewer days if lifetimes shrink. An explicit `renewBefore` makes FR-006 independent of the CA's lifetime policy.
- A failing renewal leaves `Ready=True` until expiry. Checking "renewal overdue" is what makes a failure visible 30 days before expiry, which is ≥ 14 days (SC-005).
- The vendored cert-manager aggregates `cert-manager-view` into ClusterRole `view` (`aggregate-to-view: "true"`), and `operator-readonly-view` binds `view`. So the operator can read Certificates without Secret access.

**Alternatives considered**: Prometheus alerting on `certmanager_certificate_expiration_timestamp_seconds` (no metrics stack in the cluster; push alerting is deferred like the Principle VII notification sinks). Rely on Let's Encrypt expiry emails (Let's Encrypt ended expiry emails in 2025).

---

## R6. Migration order without losing the sign-in certificate (User Story 4, SC-007)

**Decision**: Two pull requests.

| Step | Change | Why this order |
|---|---|---|
| PR A | ClusterIP solver; `pn-tls` Certificate moves to `platform`; `TLSStore default` lists `auth-tls` and `pn-tls`; PN route `tls: {}`; delete the PN solver NetworkPolicy; preflight TLS checks; `verify-tls.sh`; Principle V amendment; docs. **Auth route keeps `secretName: auth-tls`.** | Auth keeps its current, already-working reference during the change, so there is no window where auth depends on the new store. PN is already untrusted, so any reconcile order is safe for it. |
| Verify | `verify-tls.sh --external` shows both hosts trusted. Confirm `proj-pn` has no cert-manager objects (break-glass delete of stale `challenge,order` only if needed). | Stale Challenges carry finalizers that cert-manager removes. The cert-manager admission exception must still cover `proj-pn` during this cleanup. |
| PR B | Auth route `tls: {}`; narrow the cert-manager admission exception to namespaces `platform` and `cert-manager`; preflight applies the route rules to platform routes too. | The store already serves `auth-tls`, so switching auth is a no-op for visitors. Narrowing the exception last cannot strand a finalizer. |

**Rationale**: Flux applies layers in dependency order (`tls` depends on `ingress`). If auth switched to `tls: {}` in the same change, the `ingress` layer could apply before the `tls` layer creates the store, and auth would briefly serve the default certificate.

**Alternatives considered**: One PR (the reconcile-order risk above). Suspend Flux during the change (manual break-glass, avoidable).

---

## R7. Constitution Principle V and edge TLS keys (FR-014)

**Decision**: Propose **Amendment 2.2.0 (MINOR)** on top of the committed 2.1.0. It adds one clarifying paragraph to Principle V. The paragraph says:

- Edge TLS serving keys and ACME account keys are platform ingress material, not backend credentials.
- They may be stored as Kubernetes Secrets only in namespaces `platform` (edge certificates) and `cert-manager` (ACME accounts).
- They are written only by cert-manager and read only by the ingress controller and cert-manager.
- They MUST NOT be placed in, copied to, or mounted by any project namespace or pod.

**Rationale**:

- Principle V's prohibition targets backend credentials delivered to application nodes or project pods. Traefik needs the key in-process for every TLS handshake. A Vault round-trip per handshake is not possible, and the edge ingress cannot use Vault's "execute on behalf" model.
- The platform already stores `auth-tls` this way since PR #26, so the amendment documents existing practice and tightens it.
- MINOR fits: it is a scoped clarification plus a tightening (namespace limits), not a removal of the principle.
- It needs maintainer approval before PR A merges (Governance: amendment process).

**Alternatives considered**: Deliver keys from Vault through a sync controller (still produces a Kubernetes Secret for Traefik, with more moving parts). Treat it as MAJOR (overstates the change; the backend-credential rule is unchanged).

---

## R8. Who can read edge private keys

**Decision**: Accept and document the readers:

- Traefik's k3s-managed ServiceAccount (its chart RBAC reads Secrets cluster-wide)
- cert-manager
- Flux's controllers (cluster-admin for reconciliation)
- break-glass

Project ServiceAccounts and `operator-readonly-view` (`view` has no Secret access) cannot read them. Tightening Traefik's cluster-wide Secret read (for example, Traefik `defaultTLSResourcesNamespace` plus namespaced RBAC) needs a `kube-system` HelmChartConfig. It is out of scope and recorded as follow-up.

**Rationale**: FR-014 is about keeping keys away from projects. The listed readers are all platform components that already hold equal or greater power.

---

## R9. Local environment

**Decision**: There is no structural difference. Base manifests keep `example.com` hostnames and the `selfsigned` ClusterIssuer. `clusters/vps-k3s/layers.yaml` patches hostnames and `letsencrypt-prod` (existing patches keep working because they target `kind: Certificate, name: pn-tls`, with no namespace). The preflight coverage check runs per environment on that environment's render.

**Rationale**: Meets Principle VI and FR-012.
