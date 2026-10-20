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
| [#65](https://github.com/nacfson/nacfson_pipeline/issues/65) Add a static project hub page on the main domain (nacfson.cloud) that routes to every project in the cluster | OPEN | [`FIX_GithubIssue65.md`](FIX_GithubIssue65.md) | [`CONTRACT_TLS_INGRESS.md`](../../contracts/CONTRACT_TLS_INGRESS.md) |
| [#63](https://github.com/nacfson/nacfson_pipeline/issues/63) Edge ingress: no defined load balancer in front of Traefik; 80/443 exposure relies on implicit k3s ServiceLB and hand-set firewall rules | OPEN | [`FIX_GithubIssue63.md`](FIX_GithubIssue63.md) | [`CONTRACT_TLS_INGRESS.md`](../../contracts/CONTRACT_TLS_INGRESS.md) |
| [#62](https://github.com/nacfson/nacfson_pipeline/issues/62) proj-pn backend has no defined public egress; outbound internet paths are implicit or unrestricted | CLOSED | [`FIX_GithubIssue62.md`](FIX_GithubIssue62.md) | [`CONTRACT_ISOLATION_BUDGET.md`](../../contracts/CONTRACT_ISOLATION_BUDGET.md) |
| [#58](https://github.com/nacfson/nacfson_pipeline/issues/58) [bot-found] gateway: open redirect in POST /auth/logout (redirect_uri not validated against project return hosts) | OPEN | [`FIX_GithubIssue58.md`](FIX_GithubIssue58.md) | Not in the live contract. Sign-in allowlist is [`CONTRACT_AUTH_DOOR.md`](../../contracts/CONTRACT_AUTH_DOOR.md) §3.1 |
| [#57](https://github.com/nacfson/nacfson_pipeline/issues/57) [bot-found] gateway: /auth/logout reports "revoked" even when Keycloak revocation fails, and revocation ignores session-revocation-client credentials | OPEN | [`FIX_GithubIssue57.md`](FIX_GithubIssue57.md) | [`CONTRACT_AUTH_DOOR.md`](../../contracts/CONTRACT_AUTH_DOOR.md) §3.4 |
| [#54](https://github.com/nacfson/nacfson_pipeline/issues/54) KeyCloak login issue. | CLOSED | [`FIX_GithubIssue54.md`](FIX_GithubIssue54.md) | [`CONTRACT_AUTH_DOOR.md`](../../contracts/CONTRACT_AUTH_DOOR.md) §4 |
| [#48](https://github.com/nacfson/nacfson_pipeline/issues/48) Modifying GHCR Pull architecture | CLOSED | [`FIX_GithubIssue48.md`](FIX_GithubIssue48.md) | Not a live contract. Images are digest-pinned. |
| [#18](https://github.com/nacfson/nacfson_pipeline/issues/18) CI: GitOps Reconciliation Gate failing on main (test_14_project_pn_shared_door_accepted) | CLOSED | [`FIX_GithubIssue18.md`](FIX_GithubIssue18.md) | [`CONTRACT_AUTH_DOOR.md`](../../contracts/CONTRACT_AUTH_DOOR.md) §2 |
| [#4](https://github.com/nacfson/nacfson_pipeline/issues/4) The SSO pipeline | CLOSED | [`FIX_GithubIssue4.md`](FIX_GithubIssue4.md) | Not in the live contract. |
| [#69](https://github.com/nacfson/nacfson_pipeline/issues/69) On-demand network-policy check matching VPS/k3s rules, also small enough for CI | OPEN | [`FIX_GithubIssue69.md`](FIX_GithubIssue69.md) | Not checked. |
