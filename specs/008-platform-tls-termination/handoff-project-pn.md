# Handoff: Project PN — What Changes for the App (Spec 008)

**Audience**: Project PN application developers
**Date**: 2026-10-12
**Spec**: [spec.md](spec.md) (User Story 2, FR-003, FR-011)
**Status**: Implemented for PR A (centralized TLSStore, route `tls: {}`, `pn-tls` in `platform`). PR B completes auth route migration and scopes admission.

## 1. Summary

The platform terminates TLS for `pn.nacfson.cloud` at Traefik. The platform owns the certificate, the private key and renewal. The PN app receives **plain HTTP** on its container port. The app does not need any TLS server code.

```text
Visitor ──HTTPS (trusted cert)──▶ Traefik (platform) ──plain HTTP──▶ PN pods (proj-pn)
                                       │
                                       └── certificate + key live in namespace "platform"
```

## 2. What the Platform Guarantees

| Guarantee | Detail |
|---|---|
| Trusted HTTPS on the internet | Visitor → Traefik uses a publicly trusted certificate (Let's Encrypt on the VPS). |
| HTTP → HTTPS redirect | `http://pn.nacfson.cloud/...` gets a permanent (301/308) redirect to HTTPS. |
| Automatic renewal | The platform renews the certificate with at least 30 days of validity left. No project action is needed. |
| Original scheme is forwarded | Traefik sets `X-Forwarded-Proto: https` and `X-Forwarded-Host`. The `strip-forged-headers` middleware does not remove these headers. |

## 3. What the App MUST Remove (or Never Add)

- Certificate or key files, and code that loads them.
- A TLS listener (port 443, `ListenAndServeTLS`, `ssl_context`, etc.).
- Any HTTP → HTTPS redirect inside the app. The platform already does it. A second redirect can cause a loop, because the app sees `http` on the wire.

## 4. What the App MUST Keep

| # | Keep | Why |
|---|---|---|
| 1 | **HTTPS-aware URLs** | Absolute links, OAuth redirect URIs and callback URLs must use `https://pn.nacfson.cloud`. Build them from configuration or from `X-Forwarded-Proto`/`X-Forwarded-Host`, not from the raw request scheme (which is `http`). |
| 2 | **`Secure` cookies** | Session and refresh cookies must keep the `Secure` flag (and `HttpOnly`, `SameSite`). Browsers use HTTPS, so this works. |
| 3 | **Trusting forwarded headers** | If the framework has a "trust proxy" or "forwarded headers" setting, enable it for the in-cluster Traefik only. |
| 4 | **Outgoing HTTPS** | Calls to external services (Google OAuth, external APIs) still use HTTPS with normal certificate verification. Never disable verification. |
| 5 | **Internal calls stay plain HTTP** | Calls to platform services inside the cluster (for example `http://gateway.identity.svc...` for JWKS, refresh, logout) stay plain HTTP. No change. |

> [!WARNING]
> Traffic **inside** the cluster (Traefik → PN pods, PN → identity gateway) is not encrypted. Encryption between services (mTLS or a service mesh) is out of scope for spec 008. Do not send data that needs end-to-end encryption on the assumption that the cluster network is encrypted.

## 5. What Changes in This Repo for PN (Done by the Platform)

The platform team delivers these changes in PR A (and scopes admission in PR B). PN developers do not need to act.

| File | Change | PR |
|---|---|---|
| `deploy/projects/pn/workloads/ingress-route.yaml` | `tls: { secretName: pn-tls }` → `tls: {}` | PR A |
| `deploy/projects/pn/boundary/acme-solver-network-policy.yaml` | Deleted (HTTP-01 solvers run in `platform`) | PR A |
| `deploy/platform/tls/certificates.yaml` | Certificate for `pn.nacfson.cloud` moved from `proj-pn` to `platform` | PR A |
| `deploy/platform/tls/tls-store.yaml` | Added `TLSStore default` listing `auth-tls` and `pn-tls` | PR A |
| `deploy/platform/governance/manual-change-policy.yaml` | cert-manager exception scoped to `['platform', 'cert-manager']` | PR B |

After this change, the `proj-pn` namespace contains only Deployments, Services, ConfigMaps, the IngressRoute and the boundary objects (namespace, quota, network policy, RBAC). It contains no certificate, key, validation pod or TLS firewall exception.

## 6. Publishing a New Hostname Later

1. Ask the platform to add the hostname to the platform certificate list.
2. Add the route with `tls: {}` in the project IngressRoute.
3. If step 1 is missing, pre-deploy validation rejects the route (FR-010).

## 7. Verification (After the Platform Rollout)

```bash
# Trusted certificate, no insecure flag
curl -sS -o /dev/null -w '%{http_code}\n' https://pn.nacfson.cloud/

# Redirect from HTTP
curl -sS -o /dev/null -w '%{http_code} %{redirect_url}\n' http://pn.nacfson.cloud/

# Issuer must be Let's Encrypt, not "TRAEFIK DEFAULT CERT"
echo | openssl s_client -connect pn.nacfson.cloud:443 -servername pn.nacfson.cloud 2>/dev/null \
  | openssl x509 -noout -issuer -subject -enddate
```

App-side checks:

- Sign-in through Google OAuth completes and returns to `https://pn.nacfson.cloud/...`.
- Session cookies show `Secure` in browser dev tools.
- The iOS app completes API calls with no TLS errors.
