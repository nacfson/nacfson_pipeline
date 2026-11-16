# Debugging `{"error":"unsigned_in","message":"Sign-in service unreachable"}`

[`scripts/debug-signin.py`](../scripts/debug-signin.py) tests each candidate cause of a failed `/oauth/callback` code exchange. Every check prints `PASS`, `FAIL`, `WARN` or `INFO`, and the run ends with a per-cause summary (`CLEAR`, `CHECK`, `SUSPECT`).

## What the error means

The gateway returns this 503 for **any** failed code-for-token exchange with Keycloak ([`gateway/internal/auth/handler.go`](../gateway/internal/auth/handler.go)). The real reason is in the Keycloak `CODE_TO_TOKEN_ERROR` event:

| Keycloak `error` | `userId` | Tag | Meaning |
|---|---|---|---|
| `invalid_client_credentials` | any | `SECRET` | Client secret in the Keycloak DB differs from OpenBao ([TROUBLESHOOTING.md §5](../TROUBLESHOOTING.md)) |
| `user_session_not_found` | `null` | `LINK` | Login session exists and the code is valid, but the session's `gateway-client` link is missing |
| `user_session_not_found` | set | `ACTIVE` | Session reached its idle limit or 10h max between code and exchange |
| `invalid_code` | any | `CODE` | Code reused, callback doubled, session deleted, or `redirect_uri` mismatch |
| `expired_code` | any | `SLOW` | Callback arrived after `accessCodeLifespan` |

## Requirements

- `python3` (stdlib only) and a `kubectl` context that can read the cluster and `exec` into `identity` pods.
- The default is plain `kubectl` with your kubeconfig. If `KUBECTL` starts with `ssh` (for example `KUBECTL="ssh oracleCloud sudo k3s kubectl"`), every argument is shell-quoted for the remote shell.
- Defaults: `--auth-host auth.nacfson.cloud`, `--return-url https://pn.nacfson.cloud/`. Traefik is auto-detected in `platform` (gke) or `kube-system` (K3s built-in); override with `--traefik-ns`.

## Step 1: read-only checks

```bash
python3 scripts/debug-signin.py all --since 24h
```

`all` runs the five subcommands below. They only read (`get`, `logs`) and `exec`. They create, change, or delete nothing, so the [manual-change policy](../specs/004-flux-gitops-reconciliation/contracts/manual-change-policy.md) (which never matches `exec`) does not apply.

| Subcommand | Cause tested | What it does |
|---|---|---|
| `network` | Firewall / network | Lists `NetworkPolicy` and `CiliumNetworkPolicy` in `identity`. From inside the Keycloak pod (bash `/dev/tcp`, no extra pod), calls the Keycloak token endpoint (GET, which Keycloak answers with 405 and records no event), Keycloak discovery (and checks the issuer equals `https://<auth-host>/realms/platform`) and gateway `/healthz` with timings, and TCP-connects to Google. Compares public DNS with the Traefik LB IP. Checks that public `/oauth/start` redirects to the Keycloak authorize URL on the auth host. Reads Hubble dropped flows from every agent pod when the CLI is reachable |
| `config` | Configuration drift | Checks gateway and Keycloak env against the auth host; replica counts (more than one Keycloak without a shared cache loses sessions); restarts and `OOMKilled`; `kubectl top`; realm session limits in PostgreSQL versus the realm import in the repo; client-session overrides; `gateway-client` flags and redirect URI |
| `secrets` | Client secret drift | Hashes each OpenBao-fetched secret file **inside** the Keycloak pod (`sha256sum`, else `openssl`) and compares with SHA-256 of the `client.secret` column. Only 10-character hash prefixes are printed. Secrets never leave the pod; if no hash tool exists the client is skipped with `WARN` |
| `history` | Log evidence | Counts Keycloak events, classifies each `CODE_TO_TOKEN_ERROR` with the table above, counts `Code already used` warnings and Infinispan, memory, and JDBC errors. The Traefik duplicate-callback and burst checks need the Traefik access log, which is **off by default**; without it they are skipped with `INFO` |
| `sessions` | 10h lifespan / missing link | In one `exec`, logs in to the Keycloak admin API as `session-revocation-client` (`manage-users`) with a private temp config deleted on exit. Lists each live session's age, time left before the 10h max, idle time, and attached clients. Flags sessions near the limit or without `gateway-client` |

The `kcadm` login passes the revocation secret as a process argument inside the Keycloak container for the few seconds the login runs. It is not visible outside the pod's process namespace and is never sent to the operator machine.

## Step 2: reproduce the race

The `race` subcommand drives the real public flow, skipping Google by reusing your Keycloak SSO cookie:

1. Sign in once in a browser.
2. In DevTools → Application → Cookies → `https://auth.nacfson.cloud`, copy `KEYCLOAK_IDENTITY` and `KEYCLOAK_SESSION`.
3. Save them to a private file as one line: `KEYCLOAK_IDENTITY=…; KEYCLOAK_SESSION=…` (`chmod 600`).

```bash
python3 scripts/debug-signin.py race --sso-cookie-file ~/kc-cookie.txt --mode sequential --workers 2 --rounds 5
python3 scripts/debug-signin.py race --sso-cookie-file ~/kc-cookie.txt --mode parallel   --workers 2 --rounds 10
python3 scripts/debug-signin.py race --sso-cookie-file ~/kc-cookie.txt --mode parallel   --workers 4 --rounds 10 --shared-auth-session
shred -u ~/kc-cookie.txt
```

Each worker calls `/oauth/start`, then the Keycloak authorize URL with the SSO cookie, then `/oauth/callback`. In `parallel` mode the workers start the authorize step and the callback step at the same moment. `--shared-auth-session` also shares one `AUTH_SESSION_ID`, like browser tabs do.

- `sequential` is the control and must show no failures.
- If `parallel` fails and `sequential` does not, the race is confirmed. The script exits `3` and prints the `history --since` command to match the failures with the Keycloak events.
- The cookie is sent only to `https://<auth-host>`. If `/oauth/start` redirects anywhere else (for example, a drifted `KEYCLOAK_PUBLIC_URL`), the cookie is withheld and the worker reports `WARN`.
- The cookie is read from the file only, never printed. Delete the file afterwards.

## Step 3: full Keycloak trace (break-glass)

Successful Keycloak events are hidden at the default log level. Turning on the trace changes `flux-system` and `identity`, which the manual-change policy guards. The write commands therefore require `--break-glass <operator>`. The script then adds `--as=break-glass:<operator> --as-group=platform:break-glass` to those writes only ([operations §12](../specs/004-flux-gitops-reconciliation/contracts/operations.md)). Reads and `exec` keep your normal identity.

```bash
python3 scripts/debug-signin.py --break-glass "$USER" trace-on
python3 scripts/debug-signin.py capture
python3 scripts/debug-signin.py analyze /tmp/signin-trace-XXXX/keycloak.log
python3 scripts/debug-signin.py --break-glass "$USER" trace-off
```

- `trace-on` suspends the Flux `identity` Kustomization, sets `KC_LOG_LEVEL` (events, OIDC, managers, authentication, Infinispan sessions at `DEBUG`) and restarts Keycloak. All users sign in again. If setting the variable or the rollout fails, it rolls back automatically (removes the variable, resumes Flux).
- `capture` follows the Keycloak, gateway and Traefik logs until Ctrl-C (or `--seconds N`). By default it writes to a new private temp directory (`mkdtemp`, files `0600`), outside the repository. Reproduce the failure while it runs, by hand or with `race`.
- `analyze` builds a timeline per session for every `CODE_TO_TOKEN_ERROR` and gives a verdict:
  - `RACE`: two or more sign-ins for the same client on the same session within `--window` seconds (default 5) before the failure.
  - `LIFESPAN`: the session was first seen within 2 minutes of the realm's max lifespan (read from the realm import) before the failure.
  - `REUSE`: Keycloak logged `Code already used` for the session.
  - `UNPROVEN`: none of the above. The debug lines that mention the session are printed for manual review.
  - Errors without a session (`sessionId="null"`) are reported one by one with their tag, never grouped.
- `trace-off` removes `KC_LOG_LEVEL`, resumes Flux, requests a reconcile and waits for the rollout. Always run it.

**Git reconciliation.** The trace is a temporary break-glass change. `trace-off` returns `identity` to the exact Git state, so nothing needs to be committed. If you cannot run `trace-off`, resume the `identity` Kustomization under the break-glass identity; Flux then reconciles `deploy/keycloak` back to Git.

**Sensitive data.** With the trace on, Keycloak logs contain session IDs, user emails, IP addresses and code identifiers. Fluent Bit ships them to VictoriaLogs with the platform retention, and `capture` stores them locally. Keep the trace window short and delete the capture directory when done. `signin-trace*/` is in `.gitignore` in case `--out` points into the repository.

## Exit codes

| Code | Meaning |
|---|---|
| `0` | No `FAIL` |
| `1` | At least one `FAIL` |
| `2` | Invalid input, expired SSO cookie, or a break-glass command run without `--break-glass` |
| `3` | `race` reproduced the failure |

## Tests

```bash
python3 -m unittest discover tests/debug_signin
```
