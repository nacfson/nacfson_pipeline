#!/usr/bin/env python3
"""
check-arch-doc-sync.py — Canonical Architecture Handbook Synchronization Gate.

Enforces two invariants:
1. Content Freshness: docs/platform-architecture.html must reference the live
   Constitution version, all spec numbers (001..N), all Flux Kustomization
   layers in clusters/vps-k3s/layers.yaml, all 8 interactive learning tabs,
   and current platform resource budget reservations.
2. Co-Update Rule: When architecture-affecting files (deploy/, clusters/,
   gateway/, services/, bootstrap/, contracts/, specs/, constitution.md,
   platform-preflight.py, render-invariants.py) are modified in Git,
   docs/platform-architecture.html MUST also be updated.
"""

from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
HTML_REL_PATH = "docs/platform-architecture.html"
HTML_PATH = REPO_ROOT / HTML_REL_PATH

ARCH_WATCH_PREFIXES = (
    "deploy/",
    "clusters/",
    "gateway/",
    "services/",
    "bootstrap/",
    "contracts/",
    "specs/",
    ".specify/memory/constitution.md",
    "scripts/platform-preflight.py",
    "scripts/render-invariants.py",
    "ARCHITECTURE.md",
    "DEPENDENCIES.md",
)

REQUIRED_TABS = (
    "id=\"tab-topology\"",
    "id=\"tab-flows\"",
    "id=\"tab-cni\"",
    "id=\"tab-authsim\"",
    "id=\"tab-dag\"",
    "id=\"tab-oss\"",
    "id=\"tab-budget\"",
    "id=\"tab-junior\"",
)

REQUIRED_FLOW_KEYS = (
    "oidc:",
    "vault:",
    "bootstrap:",
    "gitops:",
    "tls:",
    "cni:",
    "probes:",
)


def get_constitution_version() -> str | None:
    const_path = REPO_ROOT / ".specify/memory/constitution.md"
    if not const_path.exists():
        return None
    text = const_path.read_text(encoding="utf-8")
    m = re.search(r"\*\*Version\*\*:\s*([0-9]+\.[0-9]+\.[0-9]+)", text)
    return m.group(1) if m else None


def get_spec_numbers() -> list[str]:
    specs_dir = REPO_ROOT / "specs"
    if not specs_dir.exists():
        return []
    nums = []
    for entry in sorted(specs_dir.iterdir()):
        if entry.is_dir():
            m = re.match(r"^(\d{3})-", entry.name)
            if m:
                nums.append(m.group(1))
    return nums


def get_flux_layer_names() -> list[str]:
    layers_file = REPO_ROOT / "clusters/vps-k3s/layers.yaml"
    if not layers_file.exists():
        return []
    text = layers_file.read_text(encoding="utf-8")
    names = []
    for doc in text.split("---"):
        if "kind: Kustomization" in doc and "kustomize.toolkit.fluxcd.io" in doc:
            m = re.search(r"metadata:\s*\n\s*name:\s*([a-z0-9-]+)", doc)
            if m:
                names.append(m.group(1))
    return names


def check_static_invariants() -> list[str]:
    errors: list[str] = []
    if not HTML_PATH.exists():
        return [f"Missing canonical handbook: {HTML_REL_PATH}"]

    html = HTML_PATH.read_text(encoding="utf-8")

    # 1. Constitution version check
    version = get_constitution_version()
    if version and f"v{version}" not in html:
        errors.append(
            f"{HTML_REL_PATH} is missing current Constitution version 'v{version}' "
            f"(from .specify/memory/constitution.md)."
        )

    # 2. All Spec numbers check
    for spec_num in get_spec_numbers():
        if f"Spec {spec_num}" not in html and f"Specs 001–{spec_num}" not in html:
            errors.append(
                f"{HTML_REL_PATH} does not reference 'Spec {spec_num}' from specs/{spec_num}-*."
            )

    # 3. All Flux Kustomization layers check
    for layer in get_flux_layer_names():
        if f"'{layer}'" not in html and f"selectDagLayer('{layer}')" not in html:
            errors.append(
                f"{HTML_REL_PATH} DAG_LAYERS is missing Flux Kustomization layer '{layer}' "
                f"(defined in clusters/vps-k3s/layers.yaml)."
            )

    # 4. Required interactive tabs
    for tab_marker in REQUIRED_TABS:
        if tab_marker not in html:
            errors.append(f"{HTML_REL_PATH} is missing required interactive tab {tab_marker}.")

    # 5. Required end-to-end flows
    for flow_key in REQUIRED_FLOW_KEYS:
        if flow_key not in html:
            errors.append(f"{HTML_REL_PATH} FLOWS object is missing flow '{flow_key}'.")

    # 6. Live budget constants check (1845m CPU, 5920 MiB platform reservation including vault-ai-proxy, vault-notify-proxy and error-detector)
    if "1845" not in html or "5920" not in html:
        errors.append(
            f"{HTML_REL_PATH} budget calculator does not match platform reservation "
            f"(1845m CPU / 5920 MiB)."
        )

    return errors


def get_changed_files(mode: str) -> list[str]:
    try:
        if mode == "staged":
            out = subprocess.check_output(
                ["git", "diff", "--cached", "--name-only"],
                cwd=REPO_ROOT,
                text=True,
                stderr=subprocess.DEVNULL,
            )
            return [line.strip() for line in out.splitlines() if line.strip()]
        if mode == "worktree":
            out = subprocess.check_output(
                ["git", "status", "--porcelain"],
                cwd=REPO_ROOT,
                text=True,
                stderr=subprocess.DEVNULL,
            )
            files = []
            for line in out.splitlines():
                if len(line) > 3:
                    path_part = line[3:].strip()
                    if " -> " in path_part:
                        path_part = path_part.split(" -> ", 1)[1]
                    files.append(path_part)
            return files
    except Exception:
        return []
    return []


def is_arch_affecting(rel_path: str) -> bool:
    for prefix in ARCH_WATCH_PREFIXES:
        if rel_path == prefix or rel_path.startswith(prefix):
            return True
    return False


def check_git_co_update(mode: str) -> list[str]:
    changed = get_changed_files(mode)
    if not changed:
        return []

    arch_touched = [f for f in changed if is_arch_affecting(f)]
    if arch_touched and HTML_REL_PATH not in changed:
        sample = ", ".join(arch_touched[:5])
        more = f" (+{len(arch_touched) - 5} more)" if len(arch_touched) > 5 else ""
        stage_hint = (
            f"Update and `git add {HTML_REL_PATH}` before committing."
            if mode == "staged"
            else f"Update `{HTML_REL_PATH}` to reflect your changes before finishing."
        )
        return [
            f"Architecture-affecting files were modified ({sample}{more}) "
            f"without updating `{HTML_REL_PATH}`. {stage_hint}"
        ]
    return []


def main() -> int:
    parser = argparse.ArgumentParser(description="Verify docs/platform-architecture.html synchronization.")
    parser.add_argument(
        "--staged",
        action="store_true",
        help="Check Git staged index for co-updates (used by .githooks/pre-commit).",
    )
    parser.add_argument(
        "--worktree",
        action="store_true",
        help="Check Git working tree + index for co-updates (used by AI agent Stop hooks).",
    )
    parser.add_argument(
        "--json",
        action="store_true",
        help="Output result as JSON.",
    )
    args = parser.parse_args()

    errors = check_static_invariants()
    if args.staged:
        errors.extend(check_git_co_update("staged"))
    elif args.worktree:
        errors.extend(check_git_co_update("worktree"))

    if args.json:
        print(json.dumps({"ok": len(errors) == 0, "errors": errors}, indent=2))
    else:
        if errors:
            print("[ARCH-DOC-SYNC FAILED]", file=sys.stderr)
            for err in errors:
                print(f"  - {err}", file=sys.stderr)
        else:
            print("[ARCH-DOC-SYNC PASS] docs/platform-architecture.html is synchronized.")

    return 1 if errors else 0


if __name__ == "__main__":
    sys.exit(main())
