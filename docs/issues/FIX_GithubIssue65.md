# FIX_GithubIssue65

- Issue: https://github.com/nacfson/nacfson_pipeline/issues/65
- State: OPEN. Not built in this note.
- Live contract: [`contracts/CONTRACT_TLS_INGRESS.md`](../../contracts/CONTRACT_TLS_INGRESS.md). Apex host is not listed there yet.

## What is wrong

Nothing serves `https://nacfson.cloud`. Traefik has no `IngressRoute` for the apex, so the request 404s on the fallback certificate. Project hosts (`pn`, `health`, `auth`) are routed. The apex is not.

## Contract

TLS for a new public host belongs in the platform inventory: a `Certificate` in `platform`, an entry in `TLSStore/default`, `tls: {}` on the route, and no TLS resources in a project namespace. The issue leaves ownership open: `deploy/platform/hub/` or a `proj-hub` tenant. That choice is not made here.

## How to check

After a host exists: `https://nacfson.cloud` returns the static page over the platform certificate, each registered project in `clusters/vps-k3s/projects/` has a link, and `verify-tls.sh --external` includes the apex. Port 80 should already redirect to HTTPS.

## Fixed when

The page is in Git, the certificate is in the platform inventory, and adding a project updates the page in the same change. DNS for the apex is manual and outside Git.

## Not checked

No request was sent to `https://nacfson.cloud` for this note.
