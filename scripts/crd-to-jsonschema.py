#!/usr/bin/env python3
import argparse
import json
import sys
from pathlib import Path

import yaml


def strict(node):
    if isinstance(node, list):
        return [strict(n) for n in node]
    if not isinstance(node, dict):
        return node
    node = {k: strict(v) for k, v in node.items()}
    if node.pop("x-kubernetes-int-or-string", False):
        node.pop("type", None)
        node["oneOf"] = [{"type": "string"}, {"type": "integer"}]
    if node.pop("x-kubernetes-preserve-unknown-fields", False):
        node.setdefault("additionalProperties", True)
    if "properties" in node and "additionalProperties" not in node:
        node["additionalProperties"] = False
    for key in [k for k in node if k.startswith("x-kubernetes-")]:
        node.pop(key)
    return node


def convert(crd_path: Path, out_dir: Path) -> list[Path]:
    written = []
    for crd in yaml.safe_load_all(crd_path.read_text(encoding="utf-8")):
        if not crd or crd.get("kind") != "CustomResourceDefinition":
            continue
        group = crd["spec"]["group"]
        kind = crd["spec"]["names"]["kind"].lower()
        for version in crd["spec"]["versions"]:
            schema = strict(version["schema"]["openAPIV3Schema"])
            schema.setdefault("properties", {})
            schema["properties"]["apiVersion"] = {"type": "string", "enum": [f"{group}/{version['name']}"]}
            schema["properties"]["kind"] = {"type": "string", "enum": [crd["spec"]["names"]["kind"]]}
            schema["properties"].setdefault("metadata", {"type": "object"})
            path = out_dir / group / f"{kind}_{version['name']}.json"
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(json.dumps(schema, indent=2, sort_keys=True) + "\n", encoding="utf-8")
            written.append(path)
    return written


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(prog="crd-to-jsonschema")
    parser.add_argument("--out", type=Path, required=True)
    parser.add_argument("crds", type=Path, nargs="+")
    args = parser.parse_args(argv)
    for crd in args.crds:
        for path in convert(crd, args.out):
            print(path)
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
