# Research: Separate Project Entry from Sign-In

## 1. Constitution version for the gate change

- **Decision**: Propose constitution **2.0.0**. The amendment is part of the same candidate revision as the door change. A revision that changes a project door without that amendment fails the gate.
- **Rationale**: Version 1.4.1 requires the gateway to validate a session before proxying, and it says the gateway forwards a project token on that path. This feature removes that universal check. The constitution calls an incompatible governance shift or a weakening of a core security rule a major version. Maintainer approval of the bump is required before the amendment is committed.
- **Alternatives considered**: Minor 1.5.0, treating the change as a phased addition. Rejected because the universal pre-project check is removed, not added. Shipping the door change under 1.4.1 with a note was rejected because the constitution forbids changes that contradict it.

## 2. Where sign-in sits relative to the project door

- **Decision**: Project IngressRoutes do not use the `forward-auth` middleware. The visitor road stays the published project address. Sign-in starts only after the project asks, and the browser returns to that same address.
- **Rationale**: The specification requires the project to answer before any sign-in request, and it requires the original road to remain. Today's Project PN routes call `forward-auth` before `pn-frontend` and `pn-backend`, which is the wall this feature removes.
- **Alternatives considered**: Leave ForwardAuth on the door and teach it to skip some projects. Rejected because the sign-in service would still sit on the road. Move each project onto the auth host. Rejected because the origin path would change.

## 3. Sign-in choice

- **Decision**: Each project declares `signIn` as `shared` or `none`. A missing declaration means `none`. The existing first project, Project PN, declares `shared`. New projects stay `none` until they declare `shared`.
- **Rationale**: The specification says a new omission means the project does not use sign-in, and the assumption says the first project keeps sign-in after the visitor arrives.
- **Alternatives considered**: Infer the choice from the presence of a realm client. Rejected because an old client would keep a project on sign-in after the operator chose `none`. One platform-wide switch. Rejected because projects must be able to choose differently.

## 4. Door and internal network match

- **Decision**: Every project IngressRoute backend port must already appear in that project's NetworkPolicy as an ingress port allowed from the platform namespace. Preflight rejects any other port, any `forward-auth` middleware, and any service outside the project namespace. The previously accepted door stays in place when a candidate is rejected.
- **Rationale**: Project PN already pairs the site with port 80 and `/api` with port 8080, and `project-pn-isolation` allows those ports from the platform namespace. The feature keeps that pair and checks it.
- **Alternatives considered**: Generate the NetworkPolicy from the IngressRoute. Rejected because the internal network is the project's own setting; the door follows it, not the reverse. Allow any ClusterIP port. Rejected because a door could open an entry the project did not allow.

## 5. How a confirmed identity reaches the project

- **Decision**: Every project route strips client-supplied `X-User-Subject`, `X-User-Issuer`, and `Authorization`. A `shared` project asks the browser to visit the existing sign-in pages and return to the same project address. The return sets an HttpOnly Secure cookie on that project host. The project checks signature, issuer, audience, and expiry, and rejects an identity meant for another project. A `none` project never reads that cookie as a signed-in user.
- **Rationale**: The specification forbids treating a presented claim as a signed-in user when the project does not use sign-in, and it forbids the sign-in service from choosing the road. Header injection on the door was the old wall.
- **Alternatives considered**: Keep gateway-injected `Authorization` on the success path. Rejected because that header is written before the project answers. Give `none` projects a different strip middleware and leave `Authorization` on `shared` projects. Rejected because a copied bearer token would still look like a platform identity.

## 6. What happens when sign-in is unavailable

- **Decision**: `none` projects do not call the sign-in service, so they keep answering. A `shared` project still answers the parts that do not need a known user. For a part that needs a known user, the project asks the gateway to confirm the session. Any failure, including an unreachable sign-in service, leaves the visitor unsigned-in for that part. Only a `shared` project may open egress to `gateway.identity` on port 8080. That egress is not a visitor door.
- **Rationale**: The specification separates "the project still answers" from "the visitor is not treated as signed in." An unexpired token alone cannot satisfy the unavailable-service case.
- **Alternatives considered**: Fail the whole project closed when Keycloak is down. Rejected for both `none` and for the public parts of a `shared` project. Trust a local token until expiry. Rejected because the visitor would stay signed in while the sign-in service is down. Open project egress to Keycloak itself. Rejected because confirmation can go through the existing gateway, and the project does not need Keycloak's admin or database ports.

## 7. Auth host

- **Decision**: `platform/public-auth-allowlist` stays as it is: sign-in pages, the return callback, and sign-out on the auth host, with no project addresses added. Administrative, master-realm, health, and metrics routes stay off that list.
- **Rationale**: The specification keeps those public pages available to visitors who are not yet signed in, and it does not replace the project road with the auth host.
- **Alternatives considered**: Put project routes on the auth host. Rejected because the origin path would change.
