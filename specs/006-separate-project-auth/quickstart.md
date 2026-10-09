# Quickstart: Separate Project Entry from Sign-In

Validate the contracts in [project-door.md](contracts/project-door.md), [sign-in-choice.md](contracts/sign-in-choice.md), and [session-return.md](contracts/session-return.md). The entities are in [data-model.md](data-model.md).

Prerequisites: the candidate revision is rendered, and a local cluster is available for the visitor checks. The manifest checks can run before a cluster is used.

## 1. Door match

Render the Project PN door and its NetworkPolicy.

Expected:

- The `/api` route has priority 100 and targets `pn-backend` port 8080.
- The host route has priority 10 and targets `pn-frontend` port 80.
- Both routes name `strip-forged-headers` and do not name `forward-auth`.
- The NetworkPolicy allows ingress on 80 and 8080 from the platform namespace.

A candidate that points the host route at port 9090, or that adds `forward-auth`, is rejected. The previous door is unchanged.

## 2. Project that does not use sign-in

Use a project whose declaration omits `signIn`, or sets `none`. Stop the sign-in service.

```bash
curl -skI "https://<project-host>/"
```

Expected: the project answers. The response is not a redirect to the sign-in host. Repeat for 10 trials.

Send `X-User-Subject: forged` and `Authorization: Bearer forged` on the same request.

Expected: the project answers and does not treat the sender as a signed-in user.

## 3. Project PN, sign-in after arrival

```bash
curl -skI "https://pn.example.com/"
curl -skI "https://pn.example.com/api/"
```

Expected: both answers come from the project. Neither is a redirect to the sign-in host. Repeat the site request for 10 trials.

Open the part of Project PN that needs a known user.

Expected: the browser goes to the auth host and returns to the same `pn.example.com` address that was open. A return value pointing at another project is rejected.

Stop the sign-in service and open a part that does not need a known user.

Expected: that part still answers.

Open a part that needs a known user while the sign-in service is stopped.

Expected: the visitor is unsigned-in for that part.

## 4. Isolation

Confirm Project PN's `signIn: shared` egress, if present, names only the gateway on port 8080.

A second project with `signIn: none` has no route and no egress toward the sign-in service. Changing that second declaration does not change Project PN's door or NetworkPolicy.

## Validation record (2026-10-09)

| Criterion | Result |
| --- | --- |
| SC-001 | Not run against a live host. No cluster answered `kubectl --request-timeout=3s get nodes`. The `sign_in_none_door` fixture is accepted with `signIn` omitted, no `forward-auth`, and no gateway egress. |
| SC-002 | Not run as 10 live trials. Rendered Project PN routes have no `forward-auth`. Gateway confirmation without a cookie returns HTTP 401 and does not redirect. |
| SC-003 | Passed. Project PN renders host priority 10 to `pn-frontend:80` and `/api` priority 100 to `pn-backend:8080`, with platform ingress on 80 and 8080. Port 9090 is `REJECTED_ISOLATION` and the accepted door file is unchanged. `forward-auth` on a project route is rejected. |
| SC-004 | Not run as a live public task. The none-project fixture has no sign-in connection and is accepted. |
| SC-005 | Passed in `go test` for `gateway/tests`. A return to `https://pn.example.com/dashboard` is sent back to that address with an HttpOnly Secure cookie scoped to `pn.example.com`. A return to `https://other.example.com` is HTTP 400 and does not call the sign-in service. |

`python3 -m unittest discover tests/preflight` passed (14 tests). `go test ./...` in `gateway/` passed. `scripts/verify-revocation.sh` checks 1–3 passed. Check 4 waits on `kubectl get nodes` and was stopped because no cluster responded.
