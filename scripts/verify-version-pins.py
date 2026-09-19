#!/usr/bin/env python3
"""Verify pinned binary URLs and image digests for linux/amd64."""

from __future__ import annotations

import os
import subprocess
import sys
import urllib.request
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parents[1]
VERSIONS = ROOT / "platform-config" / ".platform" / "versions.yaml"


def run(cmd: list[str]) -> str:
    env = os.environ.copy()
    env["PATH"] = str(Path.home() / ".local" / "bin") + os.pathsep + env.get("PATH", "")
    proc = subprocess.run(cmd, capture_output=True, text=True, env=env)
    if proc.returncode != 0:
        raise SystemExit(f"command failed: {' '.join(cmd)}: {proc.stderr.strip()}")
    return proc.stdout.strip()


def head_ok(url: str) -> None:
    req = urllib.request.Request(url, method="HEAD", headers={"User-Agent": "platform-pin"})
    with urllib.request.urlopen(req, timeout=30) as resp:
        if resp.status >= 400:
            raise SystemExit(f"URL not resolvable ({resp.status}): {url}")


def main() -> int:
    data = yaml.safe_load(VERSIONS.read_text(encoding="utf-8"))
    if data.get("platform") != "linux/amd64":
        raise SystemExit("platform pin must be linux/amd64")

    for name, meta in data["binaries"].items():
        print(f"check binary {name} {meta['version']}")
        head_ok(meta["url"])

    for section in ("fluxControllers", "images"):
        for name, meta in data[section].items():
            image = meta["image"]
            expected = meta["digest"]
            print(f"check image {name} {image}")
            actual = run(["crane", "digest", "--platform", "linux/amd64", image])
            if actual != expected:
                raise SystemExit(f"digest mismatch for {image}: expected {expected}, got {actual}")

    print("VERSION_PINS_OK")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
