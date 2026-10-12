# Contract: Project Route TLS

**Consumers**: Project owners (for example Project PN) · **Provider**: Platform
**Enforced by**: `scripts/platform-preflight.py` → `check_edge_tls` (required check `platform-preflight`, per environment)
**Related**: [data-model.md §4](../data-model.md#4-project-route), [preflight-tls-rules.md](preflight-tls-rules.md)

## 1. What a project declares

```yaml
apiVersion: traefik.io/v1alpha1
kind: IngressRoute
metadata:
  name: project-pn-ingress
  namespace: proj-pn
spec:
  entryPoints:
    - websecure
  tls: {}                 # serve HTTPS; the platform picks the certificate by hostname
  routes:
    - match: Host(`pn.example.com`)              # hostname patched per environment
      kind: Rule
      middlewares:
        - name: strip-forged-headers
          namespace: platform
      services:
        - name: pn-frontend
          port: 80
```

That is the whole TLS surface of a project. There is nothing else to declare.

## 2. What the platform guarantees in return

| Guarantee | Requirement |
|---|---|
| A publicly trusted certificate for every covered hostname on vps-k3s; self-signed on local-k3s | FR-001, FR-012 |
| Certificate selected by the requested hostname | FR-005 |
| Automatic issuance and renewal (≥ 30 days of validity left), with no project action | FR-006 |
| `http://` → permanent redirect to `https://` | FR-007 |
| Project quota, NetworkPolicies and admission rules are unaffected by issuance | FR-004 |
| Upstream traffic to project Services stays plain HTTP with `X-Forwarded-Proto: https` | FR-011 |

## 3. What a project MUST NOT declare

| Not allowed | Preflight rule |
|---|---|
| `tls.secretName`, `tls.certResolver`, `tls.store`, `tls.options`, `tls.domains` | TLS-2 |
| No `tls` block on the route | TLS-2 |
| Entry point other than `websecure` | TLS-3 |
| A route without a literal `` Host(`…`) ``, or with `HostRegexp` | TLS-4 |
| A hostname not in the platform inventory | TLS-5 |
| `Certificate`, `CertificateRequest`, `Issuer`, `Order`, `Challenge`, `TLSStore`, `TLSOption` or a `kubernetes.io/tls` Secret in the project namespace | TLS-1 |

## 4. Adding a new hostname

1. **Platform change** (see [certificate-inventory.md](certificate-inventory.md)): add the Certificate, list it in the `TLSStore`, and patch the hostname per environment.
2. **Project change**: add `` Host(`<hostname>`) `` to the route.
3. Both may land in the same PR. Preflight evaluates the combined render.
