# OpenBao 2.0.0 captured `bao status -format=json` fixtures

Every file under `status/` is real `bao status -format=json` output from OpenBao 2.0.0, the version of the cluster image `openbao/openbao:2.0.0`. Nothing here is hand-written. A test that needs OpenBao status output loads one of these files. `tests/vault/test_no_handwritten_openbao_status.py` fails the build when a test under `tests/` carries its own status JSON.

## Why these exist

Two cutover bugs reached production on 2026-11-15 because the test stubs replayed guessed tool output.

- PR #163: `bao status` inside the pod needs `VAULT_ADDR=http://127.0.0.1:8200` and prints multi-line JSON.
- PR #165: after a Shamir to Cloud KMS seal migration, OpenBao 2.0 reports `"type": "shamir"` with `"recovery_seal": true`, not `"type": "gcpckms"`.

`scripts/lib/openbao_status.py` is the one classifier for what a status means. `tests/vault/test_openbao_status_classifier.py` asserts that every file here classifies to the state its filename names.

## Where each file came from

| File | State | Source |
| :--- | :--- | :--- |
| `uninitialized.json` | `uninitialized` | Rehearsal step 2: target with raft storage and no seal stanza, before `bao operator init`. |
| `sealed-shamir.json` | `sealed-shamir` | Rehearsal step 3: right after `bao operator init -key-shares=1 -key-threshold=1`. |
| `unsealed-shamir.json` | `unsealed-shamir` | Rehearsal step 3: after `bao operator unseal`. |
| `sealed-migration.json` | `sealed-migration` | Rehearsal step 5: target restarted with a `seal "transit"` stanza added, before `bao operator unseal -migrate`. |
| `unsealed-auto.json` | `unsealed-auto` | Rehearsal step 5: right after `bao operator unseal -migrate`. |
| `sealed-auto.json` | `sealed-auto` | Rehearsal step 6: target restarted while the transit key refuses to decrypt. See the notes below. |
| `gke-unsealed-auto-gcpckms.json` | `unsealed-auto` | Real capture from the production GKE cluster on 2026-11-15, after the migration to Cloud KMS (`gcpckms`). Copied byte for byte. |

The six rehearsal files come from `scripts/rehearse-openbao-seal-migration.sh`. It runs the OpenBao 2.0.0 release binary on `127.0.0.1` with temporary directories: a `bao server -dev` transit provider, and a raft-backed target that goes through Shamir init, unseal, a KV write, the migration to transit auto-unseal, and restarts with no unseal key. It reads the KV secret back after the unattended unseal.

The GKE file came from:

```bash
kubectl exec -n vault openbao-0 -- env VAULT_ADDR=http://127.0.0.1:8200 bao status -format=json
```

## Refresh the fixtures

```bash
./scripts/rehearse-openbao-seal-migration.sh --write-fixtures tests/fixtures/openbao/2.0.0/status
```

This rewrites the six rehearsal files. It never touches `gke-unsealed-auto-gcpckms.json`. Refresh that one with the `kubectl exec` command above, against a cluster that is on Cloud KMS and unsealed. When the image version in `deploy/platform/vault/openbao-statefulset.yaml` changes, add a sibling `tests/fixtures/openbao/<version>/` directory and update `BAO_VERSION` and `BAO_SHA256` in the rehearsal script. `test_rehearsal_binary_and_fixtures_match_the_deployed_image` fails until they agree.

## Check the fixtures

```bash
./scripts/rehearse-openbao-seal-migration.sh --check-fixtures tests/fixtures/openbao/2.0.0/status
```

CI runs this on pull requests that touch the cutover and vault scripts, the classifier, these fixtures, or `deploy/platform/vault/`. It compares each fresh capture to the committed file on the semantic keys `type`, `initialized`, `sealed`, `t`, `n`, `migration`, `recovery_seal`, `storage_type` and `version`, and on the exact set of keys. It ignores values that change on every run: cluster id and name, nonce, timestamps, raft indexes, leader addresses, and `build_date`.

## Binary pin

Without `BAO_BIN`, the rehearsal downloads `bao_2.0.0_Linux_x86_64.tar.gz` from the OpenBao v2.0.0 release and refuses to run it unless its sha256 is `0c49fd54d133cf95bd4d916a4a6b8f1583d3293f31880371f7a4e1412234fec6`. That value matches the `checksums-linux.txt` asset published with the release. The release also ships signature files (`.sig`, `.pem`, `.gpgsig`); the rehearsal does not verify them.

## What the fixtures do not prove

- Transit auto-unseal stands in for `gcpckms`. Both are auto-unseal seals that keep recovery keys, and `tests/vault/test_openbao_status_classifier.py` asserts that `unsealed-auto.json` and the real `gke-unsealed-auto-gcpckms.json` agree on every semantic key and on the key set. That agreement covers `unsealed-auto` only. That `sealed-auto` and `sealed-migration` have the same shape under `gcpckms` is an inference, because only the `unsealed-auto` state was captured from production.
- OpenBao 2.0.0 refuses to start when the transit provider is unreachable or sealed, so an outage of the provider cannot produce `sealed-auto`. The rehearsal reaches it by rotating the transit key and setting `min_decryption_version=2`. Startup can still encrypt, the stored barrier key cannot be decrypted, and the node stays sealed with `recovery_seal` true. The rehearsal then restores `min_decryption_version=1`, restarts the target and asserts it unseals with no key.
- The fixtures are single-node raft with `t=1` and `n=1`, like the cluster. Multi-node and multi-share clusters are not covered.
