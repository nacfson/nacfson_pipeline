# Keycloak 24.0.5 Realm Import Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Keycloak 24.0.5 start again by deleting the one realm field it cannot parse, without adding storage or bringing back a Keycloak login form.

**Architecture:** The gateway already sends the browser to Google with `kc_idp_hint=google`. Keycloak only has to stay up so it can hold that sign-in in the Postgres database it already uses. The crash is a JSON parse error on `hideOnLogin` during `kc.sh start --import-realm`. Delete that field and lock the identity-provider keys to the 15 names Keycloak 24.0.5 accepts. Do not change import strategy, the database, or the gateway redirect.

**Tech Stack:** Keycloak 24.0.5 realm JSON in a ConfigMap, Python unittest over the GitOps YAML, Flux reconciliation of `deploy/platform/identity`.

**Spec:** `docs/superpowers/specs/2026-10-18-keycloak-holder-design.md`

## Global Constraints

- Image stays `quay.io/keycloak/keycloak:24.0.5`.
- Accepted identity-provider fields are exactly: `enabled`, `firstBrokerLoginFlowAlias`, `trustEmail`, `linkOnly`, `providerId`, `displayName`, `alias`, `postBrokerLoginFlowAlias`, `addReadTokenRoleOnCreate`, `authenticateByDefault`, `internalId`, `updateProfileFirstLogin`, `updateProfileFirstLoginMode`, `config`, `storeToken`.
- `storeToken` stays `false`.
- `kc_idp_hint=google` stays in `gateway/internal/auth/handler.go`.
- Do not set `hideOnLogin` to `true` and do not add a replacement field.
- Do not add a database, table, or credential.
- Do not change `kc.sh start --import-realm` to an override import.
- Do not commit the unrelated dirty files already in this checkout. Branch from `origin/main` at `ea869434a0428e20d4ae76c9f38a4f06b73f24cf`.
- A change under `deploy/` must touch `docs/platform-architecture.html` in the same commit.

## Review Focus

- An identity-provider field other than `hideOnLogin` that Keycloak 24.0.5 also rejects must fail the Git test before it is committed. Task 1 pins the 15-name allowlist.
- A fix that deletes `kc_idp_hint=google` would show Keycloak's form again. Task 1 reads the gateway source and requires that string.
- A fix that sets `storeToken` to `true` would keep Google tokens. Task 1 requires `storeToken` false.
- Startup import skips a realm that already exists. This plan must not "fix" that by adding `--override`. Task 1 `test_startup_import_does_not_override_existing_realm` requires `kc.sh start --import-realm` and rejects `--override`.
- A ConfigMap edit does not by itself change the pod template. Task 2 changes `platform.nacfson.io/realm-scope-sync` so the pod restarts onto the corrected file.

---

### Task 1: Lock the Keycloak 24.0.5 field allowlist

**Files:**
- Modify: `tests/preflight/test_keycloak_jit_provisioning.py`
- Test: `tests/preflight/test_keycloak_jit_provisioning.py`

**Interfaces:**
- Consumes: `load_platform_realm()` and `TestKeycloakGoogleJitProvisioning._google_idp()` already in that file on `origin/main`.
- Produces: `KC24_IDENTITY_PROVIDER_FIELDS` and `test_google_idp_fields_are_known_to_keycloak_24_0_5`, which Task 2 makes pass.

- [ ] **Step 1: Branch from the commit that contains the crashing field**

```bash
git fetch origin main
git switch -c fix/keycloak-24-realm-import origin/main
```

Expected: branch starts at `ea86943`, and `git grep hideOnLogin -- deploy/platform/identity/keycloak-realm-config.yaml` prints the Google provider line. Do not carry over the dirty files from the previous checkout.

- [ ] **Step 2: Write the failing test**

Add this constant next to `JIT_FLOW_ALIAS` in `tests/preflight/test_keycloak_jit_provisioning.py`:

```python
KC24_IDENTITY_PROVIDER_FIELDS = frozenset(
    {
        "enabled",
        "firstBrokerLoginFlowAlias",
        "trustEmail",
        "linkOnly",
        "providerId",
        "displayName",
        "alias",
        "postBrokerLoginFlowAlias",
        "addReadTokenRoleOnCreate",
        "authenticateByDefault",
        "internalId",
        "updateProfileFirstLogin",
        "updateProfileFirstLoginMode",
        "config",
        "storeToken",
    }
)
```

Add these two methods on `TestKeycloakGoogleJitProvisioning`:

```python
def test_google_idp_fields_are_known_to_keycloak_24_0_5(self) -> None:
    google = self._google_idp()
    unknown = set(google) - KC24_IDENTITY_PROVIDER_FIELDS
    self.assertEqual(
        unknown,
        set(),
        "Keycloak 24.0.5 realm import rejects unknown IdentityProviderRepresentation fields",
    )
    self.assertNotIn("hideOnLogin", google)
    self.assertFalse(google.get("storeToken"))

def test_login_form_skip_stays_on_gateway_hint(self) -> None:
    handler = os.path.join(BASE_DIR, "gateway", "internal", "auth", "handler.go")
    with open(handler, encoding="utf-8") as fh:
        text = fh.read()
    self.assertIn("kc_idp_hint=google", text)

def test_startup_import_does_not_override_existing_realm(self) -> None:
    deployment = os.path.join(
        BASE_DIR, "deploy", "platform", "identity", "keycloak-deployment.yaml"
    )
    with open(deployment, encoding="utf-8") as fh:
        text = fh.read()
    self.assertIn("kc.sh start --import-realm", text)
    self.assertNotIn("--override", text)
    self.assertIn("quay.io/keycloak/keycloak:24.0.5", text)
```

- [ ] **Step 3: Run the new test to verify it fails**

Run: `python3 -m unittest tests.preflight.test_keycloak_jit_provisioning.TestKeycloakGoogleJitProvisioning.test_google_idp_fields_are_known_to_keycloak_24_0_5 -v`

Expected: FAIL naming `hideOnLogin`. The gateway hint test is not run in this step.

- [ ] **Step 4: Commit the failing test**

```bash
git add tests/preflight/test_keycloak_jit_provisioning.py
git commit -m "test(identity): reject Keycloak 24 unknown identity-provider fields"
```

### Task 2: Delete the unknown field and roll the pod

**Files:**
- Modify: `deploy/platform/identity/keycloak-realm-config.yaml` (Google identity provider object; the `hideOnLogin` line)
- Modify: `deploy/platform/identity/keycloak-deployment.yaml:22` (`platform.nacfson.io/realm-scope-sync`)
- Modify: `docs/platform-architecture.html` identity layer `desc` at the `identity` DAG entry
- Test: `tests/preflight/test_keycloak_jit_provisioning.py`

**Interfaces:**
- Consumes: `KC24_IDENTITY_PROVIDER_FIELDS` and `test_google_idp_fields_are_known_to_keycloak_24_0_5` from Task 1.
- Produces: a realm file whose Google provider keys are a subset of that allowlist, and a pod template annotation `2026-10-18-kc24-import`.

- [ ] **Step 1: Delete the crashing line**

In the Google object inside `platform-realm.json`, delete this line and its comma:

```json
"hideOnLogin": false,
```

Do not edit `storeToken`, `firstBrokerLoginFlowAlias`, the mappers, the container command, or `gateway/internal/auth/handler.go`.

- [ ] **Step 2: Change the pod template annotation**

In `deploy/platform/identity/keycloak-deployment.yaml`, set:

```yaml
platform.nacfson.io/realm-scope-sync: "2026-10-18-kc24-import"
```

Leave the image at `quay.io/keycloak/keycloak:24.0.5` and leave the command as `kc.sh start --import-realm`.

- [ ] **Step 3: Record the rule in the handbook**

In the `identity` DAG entry `desc` in `docs/platform-architecture.html`, keep the existing sentence and append:

```text
Realm import must parse on Keycloak 24.0.5, so the Google identity provider must not contain hideOnLogin. The login form stays skipped by kc_idp_hint=google, not by a Keycloak login-page switch.
```

- [ ] **Step 4: Run the identity tests**

Run: `python3 -m unittest tests.preflight.test_keycloak_jit_provisioning -v`

Expected: PASS, including `test_google_idp_fields_are_known_to_keycloak_24_0_5`, `test_login_form_skip_stays_on_gateway_hint`, and `test_startup_import_does_not_override_existing_realm`.

- [ ] **Step 5: Run the handbook sync check**

Run: `python3 scripts/check-arch-doc-sync.py --worktree`

Expected: exit 0 and `[ARCH-DOC-SYNC PASS]`.

- [ ] **Step 6: Commit**

```bash
git add deploy/platform/identity/keycloak-realm-config.yaml deploy/platform/identity/keycloak-deployment.yaml docs/platform-architecture.html
git commit -m "fix(identity): drop hideOnLogin so Keycloak 24.0.5 can import the realm"
```

- [ ] **Step 7: After the commit is on the cluster's main branch, confirm the holder is up**

This step is operator verification. Do not kubectl-apply the realm.

```bash
ssh oracleCloud 'sudo k3s kubectl -n identity get pod -l app.kubernetes.io/name=keycloak'
ssh oracleCloud 'sudo k3s kubectl -n identity logs -l app.kubernetes.io/name=keycloak --tail=40'
curl -sS -D - -o /tmp/kc-auth.txt -w "%{http_code}\n" --max-time 20 "https://auth.nacfson.cloud/realms/platform/protocol/openid-connect/auth"
```

Expected: pod `1/1` Ready; log has no `Unrecognized field "hideOnLogin"`; the auth URL is not `503` with body `no available server`. A redirect is success. If the pod is Ready but a first Google login still shows a Keycloak review or link screen, stop. That is the old realm left in place by startup import, and it is outside this plan.
