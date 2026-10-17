# CONTRACT_TLS_INGRESS: Platform-Owned Edge TLS & Ingress Routing

**Status**: Enforced (`platform-preflight.py`, `render-invariants.py`)  
**Governing Principles**: [Constitution Principle V (Edge TLS Key Exception) & Amendment 2.2.0](../.specify/memory/constitution.md#L22-L23)  
**Related Specs**: [`specs/008-platform-tls-termination/`](../specs/008-platform-tls-termination/) & [`docs/tls-certificates.md`](../docs/tls-certificates.md)

---

## 1. Platform-Owned Certificate Inventory (`deploy/platform/tls/`)

All public edge TLS certificates, Let's Encrypt `ClusterIssuer`s, and the default Traefik `TLSStore` belong exclusively to the platform:

1. **Namespace Confinement**:
   - `Certificate` objects (`auth-tls`, `pn-tls`, `<slug>-tls`) and `TLSStore/default` MUST reside in the `platform` namespace ([`deploy/platform/tls/certificates.yaml`](../deploy/platform/tls/certificates.yaml), [`deploy/platform/tls/tls-store.yaml`](../deploy/platform/tls/tls-store.yaml)).
   - Only edge TLS `Secret`s in `platform` and ACME account keys in `cert-manager` are permitted as Kubernetes `Secret` objects.
2. **`TLSStore/default` Rules**:
   - Exactly one `TLSStore` named `default` may exist in `platform`.
   - `defaultCertificate` and `defaultGeneratedCert` MUST remain unset so Traefik selects certificates strictly by SNI hostname from `spec.certificates`.
   - Every `secretName` listed in `TLSStore/default` MUST match a `Certificate` in `platform`, and no hostname may be duplicated across multiple `Certificate` objects.

---

## 2. Project `IngressRoute` Rules (`TLS-1` through `TLS-6`)

Every project `IngressRoute` (`deploy/projects/<slug>/workloads/ingress-route.yaml`) is checked by `check_edge_tls` in [`scripts/platform-preflight.py`](../scripts/platform-preflight.py):

| Rule ID | Requirement | Rejection Condition |
| :--- | :--- | :--- |
| **TLS-1** | **Zero Project TLS Objects** | Any `Certificate`, `CertificateRequest`, `Issuer`, `Order`, `Challenge`, `TLSStore`, `TLSOption`, or `kubernetes.io/tls` `Secret` in `proj-<slug>`. |
| **TLS-2** | **Empty `tls: {}` Block** | Missing `spec.tls` or any key inside `spec.tls` (e.g., `tls.secretName` is forbidden). |
| **TLS-3** | **HTTPS EntryPoint Only** | `spec.entryPoints` is not exactly `["websecure"]`. |
| **TLS-4** | **Literal `Host(\`...\`)` Match** | Missing `Host(\`...\`)` or use of `HostRegexp`, `HostSNI`, or `HostHeader`. |
| **TLS-5** | **Hostname Covered by Platform Inventory** | Any `Host(\`<hostname>\`)` not listed in `spec.dnsNames` of a `platform` `Certificate` registered in `TLSStore/default`. |

---

## 3. Single-Copy Public Auth Ingress Allowlist

- Public sign-in routes live exclusively in [`deploy/platform/ingress/ingress-allowlist.yaml`](../deploy/platform/ingress/ingress-allowlist.yaml).
- Flux `postBuild.substitute` is permitted **only** on the `ingress` Kustomization for `AUTH_HOST` (enforced by Invariant 1 in [`scripts/render-invariants.py`](../scripts/render-invariants.py)).
- Keycloak `/admin/*`, master realm, internal JWKS, and management port `9000` MUST NEVER be exposed on public ingress routes.
