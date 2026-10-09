# Contract: Constitution amendment 2.0.0

This amendment is required in the same candidate revision as any project-door change from this feature. Maintainer approval of the major bump is required before the amendment is committed.

## Rationale

Visitors must be able to open a project without the sign-in service standing on that road. The sign-in service remains the only shared way to establish who a user is, and only when that project asks.

## Proposed replacement, Principle III

Keep the public-ingress limits, the standard-library gateway, the `(issuer, subject)` identity, and the rule that identity does not grant business permissions.

Replace the sentence that the gateway forwards a project token on the protected request with:

> The shared sign-in service establishes who a user is only after a project asks. It does not select the project's published addresses. A project that uses shared sign-in verifies a confirmed identity for signature, issuer, audience, and expiration. A project that does not use shared sign-in has no connection to the sign-in service.

## Proposed replacement, fail-closed session authority

Replace the rule that the gateway validates a session before proxying every request with:

> When a project asks the sign-in service to confirm a session, failure or unavailability leaves the visitor unsigned-in for that part. A project that does not use sign-in continues to answer while the sign-in service is unavailable. The sign-in service is not placed on the project's door.

Online sign-out of a confirmed session stays. The auth-host allowlist stays. NodePort and LoadBalancer stay rejected.

## Impact

| Area | Effect |
| --- | --- |
| Security | `none` projects have no platform identity. `shared` projects still reject a missing, forged, expired, or foreign identity on the parts that need a known user. Admin routes stay private. |
| Resource quota | Unchanged. |
| Portability | Unchanged. The door remains configuration, not application code. |

## Migration

Project PN declares `shared`. Its site and API addresses stay. `forward-auth` is removed from those routes. Ports 80 and 8080 stay aligned with its NetworkPolicy. Egress to the gateway on port 8080 is added for session confirmation only.
