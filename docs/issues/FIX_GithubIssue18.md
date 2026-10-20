# FIX_GithubIssue18

- Issue: https://github.com/nacfson/nacfson_pipeline/issues/18
- State: CLOSED on GitHub. The failing run was not replayed in this note.
- Live contract: [`contracts/CONTRACT_AUTH_DOOR.md`](../../contracts/CONTRACT_AUTH_DOOR.md) §2

## What is wrong

`test_14_project_pn_shared_door_accepted` failed on `main` after PR #17. Preflight rejected Project PN shared sign-in egress because it pointed at Keycloak on port 8080 instead of the gateway. The error was: `shared sign-in egress must be the gateway on port 8080, got keycloak ports [8080]`.

## Contract

`signIn: shared` may egress to `identity/gateway` on port 8080 only. Direct egress to Keycloak is forbidden. The test was enforcing that rule. The manifest was wrong, or the test was reading a stale door. The issue itself called that a hunch.

## How to check

`python3 -m unittest tests.preflight.test_platform_preflight.TestPlatformPreflight.test_14_project_pn_shared_door_accepted` exits 0. The rendered pn NetworkPolicy egress to `identity` names `gateway`, not `keycloak`.

## Fixed when

That test passes on `main` and the rendered door matches §2. Later green preflight runs are the evidence. This note did not re-run the test.

## Not checked

The Actions run cited in the issue was not opened again.
