# Contract: Preflight TLS Rules

**Implemented in**: `scripts/platform-preflight.py`, new function `check_edge_tls(candidate_docs, project_docs, platform_docs)`
**Called from**: `main()`, next to `check_project_doors`. Violations use `"rule": 2` (isolation and door), so the existing JSON output and exit codes are unchanged.
**Tests**: `tests/preflight/test_platform_preflight.py` with new fixtures in `tests/preflight/fixtures/`

## Rules

| ID | Scope | Rejects | `detail` message (prefix) |
|---|---|---|---|
| TLS-1 | Project namespaces | Kinds `Certificate`, `CertificateRequest`, `Issuer`, `Order`, `Challenge`, `TLSStore`, `TLSOption`; `Secret` with `type: kubernetes.io/tls` | `TLS objects are platform-owned; <Kind> is not allowed in project namespace <ns>` |
| TLS-2 | Project `IngressRoute` (PR A); also platform `IngressRoute` on `websecure` (PR B) | `spec.tls` missing, or containing any key | `route must declare tls: {} and let the platform select the certificate` |
| TLS-3 | Project `IngressRoute` | `spec.entryPoints` ≠ `[websecure]` | `project route entry point must be websecure only` |
| TLS-4 | Project `IngressRoute` routes | `match` with no literal `` Host(`…`) ``, or containing `HostRegexp` | `route match must name its hostname with Host()` |
| TLS-5 | Project `IngressRoute` routes | Hostname ∉ `covered_hosts` | `hostname <h> has no platform certificate; add it to deploy/platform/tls first` |
| TLS-6 | Platform render | More than one `TLSStore` named `default`, or one outside `platform`; listed `secretName` without a matching `Certificate` in `platform`; `defaultCertificate`/`defaultGeneratedCert` set; one hostname in two certificates; an edge `Certificate` outside `platform` | `certificate inventory: <reason>` |

All project-scoped messages end with the existing suffix `; last accepted door is not replaced`.

A render with no `TLSStore` is not a TLS-6 violation by itself. If any project route exists, `covered_hosts` is empty and TLS-5 rejects it with a clear message.

## Hostname extraction

- Pattern: every `` Host(`a`[, `b` …]) `` group in `match`. Each backticked literal is one hostname, compared lowercase.
- `HostSNI`, `HostRegexp` and `HostHeader` count as "no literal Host" (TLS-4).

## Fixtures

| Fixture | Expected |
|---|---|
| `valid_render.yaml` | Unchanged. It has no `IngressRoute`, so it still passes. |
| `valid_tls_render.yaml` (new) | Passes. Contains the store, two platform Certificates, and the PN route with `tls: {}`. |
| `tls_project_certificate.yaml` | TLS-1 |
| `tls_route_secret_name.yaml` | TLS-2 |
| `tls_route_web_entrypoint.yaml` | TLS-3 |
| `tls_route_host_regexp.yaml` | TLS-4 |
| `tls_uncovered_host.yaml` | TLS-5 |
| `tls_store_missing_secret.yaml` | TLS-6 |

## Compatibility

- `door_port_mismatch.yaml` and `sign_in_none_door.yaml` contain an `IngressRoute`. Add `tls: {}`, a `TLSStore` and a matching platform `Certificate` to each, so the only violation is the one the test expects.
- Rules apply to the candidate render only. The baseline is not checked. This matches the existing door checks.
