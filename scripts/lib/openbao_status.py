#!/usr/bin/env python3
import json
import sys

UNREACHABLE = "unreachable"

STATES = (
    UNREACHABLE,
    "uninitialized",
    "sealed-migration",
    "sealed-auto",
    "unsealed-auto",
    "sealed-shamir",
    "unsealed-shamir",
)


def classify_status(status):
    if not isinstance(status, dict):
        return UNREACHABLE
    initialized = status.get("initialized")
    sealed = status.get("sealed")
    if not isinstance(initialized, bool) or not isinstance(sealed, bool):
        return UNREACHABLE
    if not initialized:
        return "uninitialized"
    if status.get("migration") is True:
        return "sealed-migration"
    seal = "auto" if status.get("recovery_seal") is True else "shamir"
    return f"{'sealed' if sealed else 'unsealed'}-{seal}"


def classify(text):
    try:
        return classify_status(json.loads(text))
    except ValueError:
        return UNREACHABLE


if __name__ == "__main__":
    print(classify(sys.stdin.read()))
