# Keycloak holder: realm import must start

## Intent

Keycloak is the internal holder of a Google sign-in. It is not a login page and it is not a second account system. The login click must reach Google. Today it dies earlier: Keycloak 24.0.5 refuses to start because the realm file contains `hideOnLogin`.

## Why

A login click already passes the project and the gateway. Traefik then answers `no available server` because the Keycloak process exits during realm import. The container log is `Unrecognized field "hideOnLogin"`. Until that process stays up, Keycloak cannot hold anything.

## Goals

- Keycloak `24.0.5` starts and becomes Ready after the realm file is imported.
- The Google identity provider in `platform-realm.json` contains only fields that Keycloak 24.0.5 `IdentityProviderRepresentation` accepts.
- The login click still skips Keycloak's form by `kc_idp_hint=google` in the gateway.
- Google tokens are still not kept (`storeToken` stays `false`).
- The existing Keycloak Postgres database remains the holder. No new database.

## Non-goals

- Do not upgrade Keycloak.
- Do not add a database, a table, or a credential beyond what Keycloak 24.0.5 already writes for a brokered Google login.
- Do not store the person's Google password. It is never received.
- Do not store Google's `code`, `state`, `access_token`, `id_token`, or `refresh_token`.
- Do not set `hideOnLogin` to `true`. This server cannot read that name at all. The value in Git today is `false`, which would not hide a page even on a newer server.
- Do not remove `kc_idp_hint=google`.
- Do not override an existing realm on startup. `kc.sh start --import-realm` skips a realm that already exists so a restart does not wipe holder state. The current failure is a JSON parse error, which happens before that skip. Removing the unknown field is what lets the existing database start.
- Do not create an identity that has no email. Email is copied only when Google sends it. Project PN still requires that copy.

## What Google gives, and what Keycloak holds

| Google gives | Keycloak holds |
|---|---|
| `code`, `state` | Nothing. Checked or traded, then dropped. |
| `access_token`, `id_token`, `expires_in`, `token_type`, sometimes `refresh_token` | Nothing from Google. `storeToken` is `false`. |
| `sub` | A peg named `google.<sub>`, plus a link to that Google id. No password. |
| `email`, `given_name`, `family_name` | A copy on the peg, only if Google sent them. |
| Google password | Never received. |
| — | The platform's Google client id and client secret, copied from OpenBao. |
| — | The gateway client secrets, copied from OpenBao. |
| — | Keycloak's own session id, expiry, and the tokens it mints for the gateway. |
| — | Keycloak's own signing keys. |

The browser pass `session_handle` is sealed by the gateway with a key from OpenBao. It is not a row in the Keycloak database.

## Constraint that blocks the crash

The running image is `quay.io/keycloak/keycloak:24.0.5`. Its import error lists the only accepted identity-provider fields:

`enabled`, `firstBrokerLoginFlowAlias`, `trustEmail`, `linkOnly`, `providerId`, `displayName`, `alias`, `postBrokerLoginFlowAlias`, `addReadTokenRoleOnCreate`, `authenticateByDefault`, `internalId`, `updateProfileFirstLogin`, `updateProfileFirstLoginMode`, `config`, `storeToken`

`hideOnLogin` is not in that list. Delete it. Do not replace it with another unknown field.

## Fresh database versus the database that already exists

On a new database, `kc.sh start --import-realm` loads `platform-realm.json`, including the peg rule `google.<sub>` and `storeToken: false`.

On the database that already exists, startup import skips the realm after the file parses. That is Keycloak's rule, quoted from its import guide: if the realm already exists, the import is skipped so a restart does not recreate the realm and lose state. This change does not turn on `--override`. Override is a separate server launch, requires every Keycloak node to be stopped, and can replace realm configuration. That is out of scope.

If a first Google login still shows a Keycloak review or link screen after this fix, that is the old realm still in the database. It is not a reason to add `hideOnLogin` back, and it is not part of this change.

## Operator check

After Flux reconciles `origin/main` with this change:

- `identity/keycloak` is `1/1` Ready.
- The Keycloak log does not contain `Unrecognized field "hideOnLogin"`.
- `https://auth.nacfson.cloud/realms/platform/protocol/openid-connect/auth` no longer returns `503` with body `no available server`.
