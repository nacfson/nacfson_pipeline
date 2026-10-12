# Data Model: Platform-Owned TLS Termination

**Feature**: [spec.md](spec.md) · **Research**: [research.md](research.md)

This feature has no database. The "data" is the set of declarative objects that Flux reconciles. Each entity below maps a spec entity to concrete objects, fields and validation rules.

```text
Public hostname ──covered by──▶ Edge certificate ──listed in──▶ Certificate inventory (TLSStore default)
       ▲                                                              │
       └──────────── referenced by Host() in ── Project route ◀───────┘ (served by SNI)
```

---

## 1. Public hostname

| Field | Source | Rule |
|---|---|---|
| `name` | `Certificate.spec.dnsNames[]` (platform), `Host()` matchers (routes) | Lowercase FQDN. No wildcard (FR-009). |
| `owner` | Namespace of the route that serves it | Exactly one owner: one project namespace, or `platform`. |

**Validation**:

- A hostname appears in the `dnsNames` of at most one edge certificate in the inventory.
- A project route may only use hostnames present in the inventory (FR-010).

**Current values**:

| Environment | Hostnames |
|---|---|
| vps-k3s | `auth.nacfson.cloud`, `pn.nacfson.cloud` |
| local-k3s | `auth.example.com`, `pn.example.com` |

---

## 2. Edge certificate

The concrete object is a cert-manager `Certificate` (`cert-manager.io/v1`) plus the Secret it writes.

| Field | Value | Rule |
|---|---|---|
| `metadata.namespace` | `platform` | MUST be `platform` (FR-002). |
| `metadata.name` | `<host-label>-tls` (`auth-tls`, `pn-tls`) | Unique in `platform`. |
| `metadata.labels["app.kubernetes.io/part-of"]` | `platform` | Platform-owned. |
| `spec.secretName` | Same as `metadata.name` | Listed in the inventory. |
| `spec.dnsNames` | `[<one hostname>]` | One hostname per certificate (FR-009). Patched per environment. |
| `spec.issuerRef` | `ClusterIssuer` `selfsigned` (base/local) or `letsencrypt-prod` (vps) | Patched per environment only (FR-012). |
| `spec.privateKey` | `ECDSA`, `256`, `rotationPolicy: Always` | Unchanged from PR #26. |
| `spec.renewBefore` | `720h` | At most 30 days before expiry (FR-006). |
| `status.conditions[Ready]`, `status.notAfter`, `status.renewalTime`, `status.lastFailureTime` | Written by cert-manager | Read-only operator view (FR-015). |

**Secret** (`kubernetes.io/tls`, written by cert-manager): namespace `platform`, never copied elsewhere (FR-014). Readers are listed in [research R8](research.md#r8-who-can-read-edge-private-keys).

### State transitions

```text
              create / dnsNames change
                       │
                       ▼
   ┌──────────────▶ Issuing ───success──▶ Ready ───renewalTime reached──┐
   │                  │                     ▲                          │
   │               failure                  └──────success─────────────┤
   │                  ▼                                                │
   └──backoff──── Failed (Ready=False on first issue;                  │
                  Ready=True + Issuing=False on renewal) ◀──failure────┘
                       │
                  notAfter passed
                       ▼
                    Expired (Ready=False)
```

| State | Visible signal (`verify-tls.sh`) |
|---|---|
| Ready | `Ready=True`, `renewalTime` in the future |
| Renewal overdue | `Ready=True`, `renewalTime` more than 1 h in the past → **alert** (≥ 30 days before expiry) |
| Failed (first issue) | `Ready=False` → **alert** |
| Near expiry | fewer than 14 days to `notAfter` → **alert** |

---

## 3. Certificate inventory

The concrete object is a Traefik `TLSStore` (`traefik.io/v1alpha1`).

| Field | Value | Rule |
|---|---|---|
| `metadata.name` | `default` | At most one in the render. Required whenever a project route exists (otherwise no hostname is covered). |
| `metadata.namespace` | `platform` | MUST be `platform`. |
| `spec.certificates[].secretName` | `auth-tls`, `pn-tls` | Each MUST match an edge certificate's `spec.secretName` in `platform`. |
| `spec.defaultCertificate` | *(unset)* | MUST stay unset, so unknown or missing SNI gets Traefik's generated certificate, never a project certificate. |
| `spec.defaultGeneratedCert` | *(unset)* | MUST stay unset. |

**Derived value**: `covered_hosts` = union of `dnsNames` of all edge certificates whose `secretName` is listed. Preflight computes this per environment.

---

## 4. Project route

The concrete object is a Traefik `IngressRoute` in a project namespace.

| Field | Rule |
|---|---|
| `spec.entryPoints` | Exactly `[websecure]`. Port 80 is handled by the platform redirect. |
| `spec.tls` | MUST be present and empty: `tls: {}`. No `secretName`, `certResolver`, `store`, `options` or `domains`. |
| `spec.routes[].match` | MUST contain at least one literal `` Host(`…`) ``. MUST NOT use `HostRegexp`. Every hostname ∈ `covered_hosts`. |
| `spec.routes[].middlewares` | Includes `platform/strip-forged-headers` (existing door rule). |
| `spec.routes[].services` | Same-namespace ClusterIP Services on allowed ports (existing door rules). |

**Project namespace (forbidden objects)**: `Certificate`, `CertificateRequest`, `Issuer`, `Order`, `Challenge`, `TLSStore`, `TLSOption`, and `Secret` of type `kubernetes.io/tls`.

---

## 5. Supporting platform objects

| Object | Change |
|---|---|
| `ClusterIssuer` `letsencrypt-staging`, `letsencrypt-prod` | Add `solvers[].http01.ingress.serviceType: ClusterIP`. |
| `ValidatingAdmissionPolicy` `gitops-managed-namespaces` | PR B: cert-manager clause also requires `request.namespace in ['platform', 'cert-manager']`. |
| `NetworkPolicy` `proj-pn/allow-acme-http01-solver` | Deleted (PR A). |
| `IngressRoute` `platform/public-auth-allowlist` | PR B: `tls: {}` instead of `secretName: auth-tls`. |
