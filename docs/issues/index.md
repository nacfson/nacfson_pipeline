# Filed GitHub issues

Navigation: [`INDEX.md`](../../INDEX.md) → this file → [`FIX_GithubIssue<number>.md`](FIX_GithubIssue57.md).

## Rule

When a GitHub issue is filed or analyzed:

1. Add `docs/issues/FIX_GithubIssue<number>.md`. Do not add a second `troubleshooting.md` per issue.
2. Add a row to this index. This file is the aggregate. The root [`INDEX.md`](../../INDEX.md) only links here.
3. If the issue changes a platform rule, update the live contract in [`contracts/`](../../contracts/) in the same change. Historical `specs/*/contracts/` notes do not override that live contract.
4. Say what was checked and what was not. A closed GitHub issue is not proof the cluster matches the note.

Opening this repo in OpenCode runs scripts/sync-github-issues.py once. That refreshes this index and creates a missing FIX_ file. It does not rewrite an existing FIX_ body and it does not edit contracts/.

## Index

| Issue | State | Note | Live contract |
| :--- | :--- | :--- | :--- |
| [#65](https://github.com/nacfson/nacfson_pipeline/issues/65) Static project hub on the apex domain | OPEN | [`FIX_GithubIssue65.md`](FIX_GithubIssue65.md) | [`CONTRACT_TLS_INGRESS.md`](../../contracts/CONTRACT_TLS_INGRESS.md) |
| [#63](https://github.com/nacfson/nacfson_pipeline/issues/63) No declared load balancer in front of Traefik | OPEN | [`FIX_GithubIssue63.md`](FIX_GithubIssue63.md) | [`CONTRACT_TLS_INGRESS.md`](../../contracts/CONTRACT_TLS_INGRESS.md) |
| [#62](https://github.com/nacfson/nacfson_pipeline/issues/62) pn public egress is not declared | CLOSED | [`FIX_GithubIssue62.md`](FIX_GithubIssue62.md) | [`CONTRACT_ISOLATION_BUDGET.md`](../../contracts/CONTRACT_ISOLATION_BUDGET.md) |
| [#58](https://github.com/nacfson/nacfson_pipeline/issues/58) Logout `redirect_uri` is not allowlisted | OPEN | [`FIX_GithubIssue58.md`](FIX_GithubIssue58.md) | Not in the live contract. Sign-in allowlist is [`CONTRACT_AUTH_DOOR.md`](../../contracts/CONTRACT_AUTH_DOOR.md) §3.1 |
| [#57](https://github.com/nacfson/nacfson_pipeline/issues/57) Logout claims revoked when Keycloak deletion fails | OPEN | [`FIX_GithubIssue57.md`](FIX_GithubIssue57.md) | [`CONTRACT_AUTH_DOOR.md`](../../contracts/CONTRACT_AUTH_DOOR.md) §3.4 |
| [#54](https://github.com/nacfson/nacfson_pipeline/issues/54) Keycloak login form still shown for Google SSO | CLOSED | [`FIX_GithubIssue54.md`](FIX_GithubIssue54.md) | [`CONTRACT_AUTH_DOOR.md`](../../contracts/CONTRACT_AUTH_DOOR.md) §4 |
| [#48](https://github.com/nacfson/nacfson_pipeline/issues/48) GHCR image update should follow release tags | CLOSED | [`FIX_GithubIssue48.md`](FIX_GithubIssue48.md) | Not a live contract. Images are digest-pinned. |
| [#18](https://github.com/nacfson/nacfson_pipeline/issues/18) Preflight rejected pn egress to Keycloak | CLOSED | [`FIX_GithubIssue18.md`](FIX_GithubIssue18.md) | [`CONTRACT_AUTH_DOOR.md`](../../contracts/CONTRACT_AUTH_DOOR.md) §2 |
| [#4](https://github.com/nacfson/nacfson_pipeline/issues/4) SSO token and idle lifetimes | CLOSED | [`FIX_GithubIssue4.md`](FIX_GithubIssue4.md) | Not in the live contract. |
