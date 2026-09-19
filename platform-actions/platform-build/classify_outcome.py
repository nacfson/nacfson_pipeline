#!/usr/bin/env python3
import sys
msg = sys.stdin.read().lower()
if "contract-failed" in msg or "schema" in msg:
    print("contract-failed"); raise SystemExit(0)
if "test" in msg and "failed" in msg:
    print("test-failed"); raise SystemExit(0)
if "build" in msg and "failed" in msg:
    print("build-failed"); raise SystemExit(0)
print("valid"); raise SystemExit(0)
