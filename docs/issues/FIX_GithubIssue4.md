# FIX_GithubIssue4

- Issue: https://github.com/nacfson/nacfson_pipeline/issues/4
- State: CLOSED on GitHub. Lifetimes were not read from the live realm in this note.
- Live contract: none. Session length is not in `contracts/CONTRACT_*.md`.

## What is wrong

The request is a set of platform defaults, not a bug report: access token 5–15 minutes, SSO idle 7 days, SSO max 30 days, silent refresh while the SSO session is valid. Those mirror an older central-auth shape (1h access / 7d idle / 30d max) under Keycloak.

## Contract

No live contract records those numbers. The gateway `SessionTimeout` fallback in code is not the same thing as the Keycloak SSO idle and max lifespan. Putting the numbers only in a realm import, with no contract line, is how this request gets lost again.

## How to check

In the realm import, compare access-token lifespan, SSO idle, and SSO max with the numbers above. Then sign in, wait past the access-token lifetime but inside the idle window, and confirm the gateway refreshes instead of forcing a new Google login.

## Fixed when

The realm settings match the chosen numbers, a contract line names them, and a refresh inside the idle window does not send the browser back to Google.

## Not checked

The realm import was not compared with these numbers, and no session was refreshed.
