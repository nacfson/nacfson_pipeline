# FIX_GithubIssue58

- Issue: https://github.com/nacfson/nacfson_pipeline/issues/58
- State: OPEN. Reproduced in Brave 155 against this tree's gateway. The code was not changed.
- Live contract: not written. Sign-in allowlist is [`contracts/CONTRACT_AUTH_DOOR.md`](../../contracts/CONTRACT_AUTH_DOOR.md) §3.1 only.

## What is wrong

`HandleLogout` copies `redirect_uri` from the form or query into `http.Redirect`. It never calls `returnHostAllowed`. `/auth/logout` is public on `AUTH_HOST`.

A signed-out POST does not redirect. It returns `401`. A valid session does. Brave followed both an unlisted `http` host and `https://evil.attacker.test/phish` (that name was pinned to `127.0.0.1`, so the browser did not leave the machine). Brave sent a `javascript:` logout POST and did not navigate to it. The gateway still returned `302` with that `Location`.

The project BFF logout drops the body and returns JSON. A project-door logout will not show this.

## Contract

`specs/001-project-platform/contracts/session-revocation.md` shows one project-door `redirect_uri` and a `302` to it. It does not require `PROJECT_RETURN_HOSTS`, and it does not forbid any other URL. The live contract does not mention `redirect_uri`.

## How to check

Run the gateway with `PROJECT_RETURN_HOSTS` set. POST `/auth/logout` with a valid session and a host that is not on that list. The `Location` header must not be that host. A signed-out POST must stay on the auth host with `401`. `GET /auth/logout` is `405` and proves nothing about the redirect.

## Fixed when

A disallowed `redirect_uri` is ignored or rejected, and an allowlisted `https` host with no userinfo still redirects. `gateway/tests/logout_test.go` does not set `AllowedReturnHosts`, so a green run of that file is not this check.

## Not checked

No signed-in browser session against `https://auth.nacfson.cloud`. Local gateway pods were `ImagePullBackOff`.
