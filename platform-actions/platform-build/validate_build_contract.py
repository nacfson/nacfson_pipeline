#!/usr/bin/env python3
import json, sys
from pathlib import Path
import yaml
from jsonschema import Draft7Validator

ROOT = Path(__file__).resolve().parents[2]
SCHEMA = ROOT / "platform-config" / ".platform" / "schemas" / "source-build-v1.json"

def main() -> int:
    if len(sys.argv) != 2:
        print("usage: validate_build_contract.py <build.yaml>", file=sys.stderr)
        return 2
    path = Path(sys.argv[1])
    doc = yaml.safe_load(path.read_text())
    schema = json.loads(SCHEMA.read_text())
    errors = sorted(Draft7Validator(schema).iter_errors(doc), key=lambda e: list(e.path))
    if errors:
        print(f"contract-failed: {errors[0].message}", file=sys.stderr)
        return 1
    for field in ("context", "dockerfile"):
        value = Path(doc[field])
        if value.is_absolute() or ".." in value.parts:
            print(f"contract-failed: {field} escapes checkout", file=sys.stderr)
            return 1
    print("contract-ok")
    return 0

if __name__ == "__main__":
    raise SystemExit(main())
