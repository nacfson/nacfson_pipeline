# FIX_GithubIssue54

- Issue: https://github.com/nacfson/nacfson_pipeline/issues/54
- State: CLOSED on GitHub. This note did not sign in through a browser.
- Live contract: [`contracts/CONTRACT_AUTH_DOOR.md`](../../contracts/CONTRACT_AUTH_DOOR.md) §4

## What is wrong

The report is one sentence: a user still has to pass the Keycloak login form even when sign-in is Google SSO. The intended path is headless: the project owns the login page, and `/oauth/start` sends the browser to Google with `kc_idp_hint=google`, not to a Keycloak username form.

## Contract

§4 says Keycloak creates the user on first Google login and must not show Review Profile, Confirm Link, or a password form. `firstBrokerLoginFlowAlias` must be `google jit first broker login`. If that flow is missing or `hideOnLogin` / the IdP hint is wrong, the browser stops on Keycloak. That is the check, not a new rule.

## How to check

From a project sign-in button, the next host after the project backend is Google account selection, not `auth.<domain>` rendering a Keycloak username form. A stale realm import that still uses the built-in `first broker login` flow reproduces the report.

## Fixed when

A fresh browser session reaches Google without a Keycloak username page, and the realm import in Git matches §4.

## Not checked

No Google login was completed. Issue is closed; the realm file was not diffed against a live Keycloak in this pass.
