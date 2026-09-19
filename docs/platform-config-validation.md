# platform-config validation

## Command

From the repository root:

```bash
python3 -m venv .venv
. .venv/bin/activate
pip install -r requirements-validation.txt
export PATH="$HOME/.local/bin:$PATH"   # if kustomize/helm installed locally
./scripts/validate-platform-config.py
```

This is the required production-branch check for `platform-config` changes.

## Prerequisites

| Tool | Purpose | Tested version |
|---|---|---|
| Python 3.11+ | validation entry point | 3.14 |
| PyYAML, jsonschema | YAML/schema checks | see `requirements-validation.txt` |
| kustomize | offline render of roots/units | v5.6.0 |
| helm | chart lint/render + schema | v3.17.1 |

No cluster access, SOPS private keys, or cloud credentials are required. Rendering must succeed without decrypting Secrets.

## What it checks

- production root and infrastructure/applications unit rendering
- Flux dependency order and SOPS decryption wiring by in-cluster Secret name only
- `charts/web-process` lint/render and values schema
- HelmRelease chart path boundary (`./charts/web-process`)
- project path ownership boundaries
- provider/node identity rejection
- plaintext Secret detection (reports file location only)
- deterministic clean renders
- negative fixtures under `validation/fixtures/`

## Clean clone expectation

A clean checkout that installs only the prerequisites above must exit `0` with:

```text
VALIDATION_OK platform-config
```
