# Runbook: Rotate `ghcr-pull-token` in OpenBao

**Created**: 2026-11-03
**Related**: [`spec002-vault-guide.md`](spec002-vault-guide.md) §2 (rotation lifecycle), [`ghcr-token-lifecycle.html`](ghcr-token-lifecycle.html)

## 1. Overview

The GHCR pull token is stored in OpenBao KV v2 at `kv/platform/ghcr-pull-token` (fields `token`, `owner`, `active`). The `registry-proxy` uses it to pull images from `ghcr.io`, so worker nodes never hold it.

Rotation means writing a new KV version. Writing needs a root or admin token, because the `registry-proxy` policy (`deploy/platform/vault/policies/registry-proxy.hcl`) is read-only. The unseal key alone cannot write: it only unseals the vault. If you hold only the unseal key, regenerate a root token from it first (Step 2).

## 2. Prerequisites

- A shell on the VPS with `kubectl` access to the `vault` namespace (pod `openbao-0`).
- The OpenBao unseal key (base64, about 44 characters, saved at `bao operator init`). The root token is a different value.
- A new GitHub PAT with only the `read:packages` scope.
- One shell session for the whole procedure. The OTP and nonce live in shell variables.

## 3. Procedure

### Step 1: Create the new PAT on GitHub

GitHub → Settings → Developer settings → Personal access tokens. Grant `read:packages` only, so the pull token cannot create or revoke tokens. Keep the old PAT until Step 5.

### Step 2: Generate a root token from the unseal key

This is OpenBao's `generate-root` ceremony:

| Value | Meaning |
| :--- | :--- |
| OTP | One-time pad you generate. It masks the new token in transit. |
| Nonce | ID of this one generation attempt, printed by `-init`. |
| Unseal key | Authorizes the attempt. If the vault needs several key shares, submit each with the same nonce. |
| Encoded token | The root token masked with the OTP. Useless without it. |

```bash
BAO="kubectl exec -n vault openbao-0 -- env VAULT_ADDR=http://127.0.0.1:8200 bao"

# Start fresh and get a one-time pad
$BAO operator generate-root -cancel
OTP=$($BAO operator generate-root -generate-otp | tr -d '\r\n ')

# Start the attempt; copy the Nonce it prints
$BAO operator generate-root -init -otp="$OTP"
read -r NONCE

# Submit the unseal key (hidden input), then copy the Encoded Token
read -rs UNSEAL_KEY
$BAO operator generate-root -nonce="$NONCE" "$UNSEAL_KEY"
read -r ENCODED

# Decode: the output is the root token
$BAO operator generate-root -decode="$ENCODED" -otp="$OTP"
```

Before submitting, check `echo "${#UNSEAL_KEY}"`. A key is about 44 characters. A 40-character value is likely a GitHub token pasted by mistake. If the vault is sealed, unseal it first with `$BAO operator unseal`.

### Step 3: Write the new token

```bash
read -rs BAO_TOKEN   # root token from Step 2
read -rs NEW_PAT     # new GitHub PAT (40 chars, starts ghp_)

kubectl exec -n vault openbao-0 -- env \
  VAULT_ADDR=http://127.0.0.1:8200 VAULT_TOKEN="$BAO_TOKEN" \
  bao kv put kv/platform/ghcr-pull-token token="$NEW_PAT" owner=platform active=true
```

Success prints the path `kv/data/platform/ghcr-pull-token` and a metadata table with `version` 2 or higher and `destroyed false`. The previous token stays as the earlier version, so you can roll back.

> Do not run `scripts/vault-seed-inventory.sh --force`. It overwrites all 11 seeded secrets and hardcodes a placeholder token.

### Step 4: Verify

A successful write only proves the value was stored. Check the rest:

1. **Stored shape** (prints length and prefix, not the token). Expect `40 ghp_`:
   ```bash
   kubectl exec -n vault openbao-0 -- env VAULT_ADDR=http://127.0.0.1:8200 VAULT_TOKEN="$BAO_TOKEN" \
     bao kv get -field=token kv/platform/ghcr-pull-token | awk '{print length($0), substr($0,1,4)}'
   ```
2. **Adoption check.** The `registry-proxy` probes `ghcr.io` with the new token and promotes it on success (`ADOPTED`); on failure it returns `REJECTED` (HTTP 422) and the old token stays active:
   ```bash
   kubectl run tmp-curl -n vault --rm -it --restart=Never --image=curlimages/curl -- \
     curl -s -X POST http://vault-registry-proxy.vault.svc:5000/admin/adoption-check \
     -d "{\"token\":\"$NEW_PAT\"}"
   ```
3. **Real pull.** Restart a pod that uses a `ghcr.io` image and watch the `registry-proxy` logs.

The proxy reads `GHCR_PULL_TOKEN` from its environment (`services/vault-proxies/cmd/registry-proxy/main.go`) and keeps a promoted token only in memory. Confirm whether the deployment re-reads OpenBao on start, and restart it if the new token does not take effect.

> Security note: `/admin/adoption-check` has no authentication, and the proxy accepts the literal tokens `test-valid` and `ghp_valid_*` without contacting GHCR (see `security-and-complexity-audit.md`).

### Step 5: Clean up

Only after Step 4 passes:

1. Delete the **old** PAT on GitHub.
2. Revoke the root token and clear the variables:
   ```bash
   kubectl exec -n vault openbao-0 -- env VAULT_ADDR=http://127.0.0.1:8200 VAULT_TOKEN="$BAO_TOKEN" \
     bao token revoke -self
   unset BAO_TOKEN NEW_PAT UNSEAL_KEY OTP ENCODED NONCE
   ```

Root tokens made this way do not expire. Revoke yours, and generate a new one when needed.

## 4. Troubleshooting

| Symptom | Cause | Fix |
| :--- | :--- | :--- |
| `bash: admin-token: No such file or directory` | `<admin-token>` copied literally; bash read `<` as a redirect | Use a real value via a variable (`"$BAO_TOKEN"`) |
| `flag provided but not defined: -non` | Flag mistyped | Use `-nonce=` |
| `lt: command not found` and background jobs | `<NONCE>` placeholder or HTML-escaped `&lt;` pasted | Substitute the real value; use `"$NONCE"` |
| `'key' must be a valid hex or base64 string` | Wrong or truncated value in `UNSEAL_KEY` (for example a 40-char GitHub token) | Check `${#UNSEAL_KEY}` (about 44), paste the real unseal key, retry with the same nonce |
| Nonce rejected | Attempt was cancelled or replaced | Run `-cancel`, then repeat from `-generate-otp` (new OTP, new nonce) |
| Vault is sealed | Not yet unsealed | `$BAO operator unseal`, then retry |
| `kv put` refused after a restore | Post-restore safety gate (`RESTORATION_SAFETY_GATE`, see `scripts/vault-reconcile.sh`) | Resolve via the reconcile procedure |
| `REJECTED` from adoption check | `ghcr.io` refused the token | Check scope `read:packages`, typos, expiry; the old token remains active |
