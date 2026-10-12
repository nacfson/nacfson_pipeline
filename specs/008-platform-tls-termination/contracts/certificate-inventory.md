# Contract: Certificate Inventory

**Owner**: Platform · **Location**: `deploy/platform/tls/` (base), `clusters/<env>/layers.yaml` (environment patches)
**Related**: [data-model.md §2–3](../data-model.md#2-edge-certificate)

## 1. Files

| File | Holds |
|---|---|
| `deploy/platform/tls/cluster-issuers.yaml` | `selfsigned`, `letsencrypt-staging`, `letsencrypt-prod` (ACME solvers use `serviceType: ClusterIP`) |
| `deploy/platform/tls/certificates.yaml` | One `Certificate` per hostname, all in namespace `platform` |
| `deploy/platform/tls/tls-store.yaml` | `TLSStore` `platform/default` listing every certificate Secret |
| `clusters/vps-k3s/layers.yaml` (`tls` layer patches) | Real hostname and `letsencrypt-prod` per Certificate |

## 2. Certificate shape

```yaml
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: pn-tls
  namespace: platform
  labels:
    app.kubernetes.io/part-of: platform
spec:
  secretName: pn-tls
  dnsNames:
    - pn.example.com            # patched per environment
  issuerRef:
    group: cert-manager.io
    kind: ClusterIssuer
    name: selfsigned            # patched per environment
  renewBefore: 720h
  privateKey:
    algorithm: ECDSA
    size: 256
    rotationPolicy: Always
```

## 3. Store shape

```yaml
apiVersion: traefik.io/v1alpha1
kind: TLSStore
metadata:
  name: default
  namespace: platform
  labels:
    app.kubernetes.io/part-of: platform
spec:
  certificates:
    - secretName: auth-tls
    - secretName: pn-tls
  # defaultCertificate stays unset: unknown or missing SNI gets Traefik's generated certificate.
```

## 4. Procedure: add a hostname

1. Append a `Certificate` (`<label>-tls`) to `certificates.yaml` with base values (`<label>.example.com`, `selfsigned`).
2. Append `- secretName: <label>-tls` to `tls-store.yaml`.
3. Add a `tls` layer patch in `clusters/vps-k3s/layers.yaml` for the real hostname and `letsencrypt-prod`.
4. Make sure DNS for the hostname points to the public entry point, and port 80 is reachable.
5. `platform-preflight` must pass for both environments (TLS-6 checks that the store and the certificates agree).

## 5. Procedure: remove a hostname

Remove the project route's `Host()` first (or in the same PR), then the store entry, the Certificate and the patch. Preflight TLS-5 blocks the reverse order.

## 6. Invariants (checked by preflight TLS-6)

- At most one `TLSStore` named `default`, and only in namespace `platform`. Any project route needs it (TLS-5).
- Every `spec.certificates[].secretName` matches a `Certificate.spec.secretName` in `platform`.
- `spec.defaultCertificate` and `spec.defaultGeneratedCert` are unset.
- No hostname appears in two inventory certificates.
- Edge `Certificate` objects exist only in namespace `platform`.
