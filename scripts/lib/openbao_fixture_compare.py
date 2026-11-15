#!/usr/bin/env python3
import json
import sys

SEMANTIC_KEYS = (
    "type",
    "initialized",
    "sealed",
    "t",
    "n",
    "migration",
    "recovery_seal",
    "storage_type",
    "version",
)


def semantic(status):
    view = {key: status.get(key) for key in SEMANTIC_KEYS}
    view["keys"] = sorted(status)
    return view


def differences(captured, fixture):
    left = semantic(captured)
    right = semantic(fixture)
    return [
        f"{key}: captured={left[key]!r} fixture={right[key]!r}"
        for key in left
        if left[key] != right[key]
    ]


def main(argv):
    if len(argv) != 3:
        print("usage: openbao_fixture_compare.py CAPTURED.json FIXTURE.json", file=sys.stderr)
        return 2
    with open(argv[1], encoding="utf-8") as fh:
        captured = json.load(fh)
    with open(argv[2], encoding="utf-8") as fh:
        fixture = json.load(fh)
    found = differences(captured, fixture)
    for line in found:
        print(line)
    return 1 if found else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
