#!/usr/bin/env python3
"""Structural validation for ansible/ host + k3s + Flux bootstrap tree.

Checks required role/playbook files, ownership guardrails (no apply of
platform-config/infrastructure from Ansible), version pins vs
platform-config/.platform/versions.yaml, and optionally
ansible-playbook --syntax-check when ansible-playbook is on PATH.
"""

from __future__ import annotations

import re
import shutil
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
ANSIBLE = ROOT / "ansible"
VERSIONS = ROOT / "platform-config" / ".platform" / "versions.yaml"

REQUIRED_PATHS = [
    "ansible.cfg",
    "README.md",
    "requirements.yml",
    "inventories/home-lab/hosts.yaml.example",
    "inventories/home-lab/group_vars/all.yaml",
    "inventories/home-lab/group_vars/application.yaml",
    "inventories/home-lab/group_vars/registry.yaml",
    "playbooks/site.yml",
    "playbooks/host-prep.yml",
    "playbooks/k3s.yml",
    "playbooks/flux.yml",
    "scripts/render-inventory.py",
    "roles/common/tasks/main.yml",
    "roles/host_firewall/tasks/main.yml",
    "roles/host_firewall/templates/platform_host_fw.nft.j2",
    "roles/storage/tasks/main.yml",
    "roles/k3s/tasks/main.yml",
    "roles/k3s/templates/k3s.service.j2",
    "roles/k3s/templates/encryption-config.json.j2",
    "roles/k3s/templates/local-path-retain-storageclass.yaml.j2",
    "roles/flux_bootstrap/tasks/main.yml",
    "roles/flux_bootstrap/templates/gotk-sync.yaml.j2",
    "roles/backup_agent/tasks/main.yml",
]

# Ansible must not apply platform-config infrastructure / app trees.
FORBIDDEN_CONTENT = [
    re.compile(r"platform-config/infrastructure", re.I),
    re.compile(r"bootstrap-control-plane\.sh"),
    re.compile(r"bootstrap-worker\.sh"),
    re.compile(r"kubectl\s+apply\s+-k\s+.*platform-config", re.I),
]

ROLE_SCAN_GLOBS = [
    "roles/**/*.yml",
    "roles/**/*.yaml",
    "roles/**/*.j2",
    "playbooks/**/*.yml",
    "playbooks/**/*.yaml",
]


class ValidationError(Exception):
    pass


def fail(msg: str) -> None:
    raise ValidationError(msg)


def check_required_paths() -> None:
    for rel in REQUIRED_PATHS:
        path = ANSIBLE / rel
        if not path.exists():
            fail(f"missing required path: ansible/{rel}")


def check_no_forbidden_ownership() -> None:
    for pattern in ROLE_SCAN_GLOBS:
        for path in ANSIBLE.glob(pattern):
            if not path.is_file():
                continue
            text = path.read_text(encoding="utf-8")
            for pat in FORBIDDEN_CONTENT:
                if pat.search(text):
                    fail(
                        f"ownership violation in {path.relative_to(ROOT)}: "
                        f"matched {pat.pattern} — Ansible must not apply "
                        "platform-config/infrastructure or legacy kubeadm bootstrap scripts"
                    )


def parse_versions_yaml(text: str) -> dict[str, str]:
    """Minimal extract of binaries.k3s/flux version + url without requiring PyYAML."""
    out: dict[str, str] = {}
    section = None
    for line in text.splitlines():
        if re.match(r"^\s+k3s:\s*$", line):
            section = "k3s"
            continue
        if re.match(r"^\s+flux:\s*$", line):
            section = "flux"
            continue
        if section and re.match(r"^\s+\w+:", line) and not re.match(
            r"^\s+(version|url):", line
        ):
            # left nested section
            if not line.strip().startswith("version") and not line.strip().startswith("url"):
                if re.match(r"^\s{2}\w+:", line) and not re.match(r"^\s{4}", line):
                    section = None
        m_ver = re.match(r"^\s+version:\s*(\S+)\s*$", line)
        m_url = re.match(r"^\s+url:\s*(\S+)\s*$", line)
        if section and m_ver:
            out[f"{section}_version"] = m_ver.group(1).strip().strip("\"'")
        if section and m_url:
            out[f"{section}_url"] = m_url.group(1).strip().strip("\"'")
            section = None  # url is last field we care about per binary
    return out


def parse_group_vars_pins(text: str) -> dict[str, str]:
    keys = (
        "k3s_version",
        "k3s_binary_url",
        "flux_version",
        "flux_tarball_url",
        "platform_arch",
    )
    out: dict[str, str] = {}
    for key in keys:
        m = re.search(rf"^{re.escape(key)}:\s*[\"']?([^\"'#\n]+)[\"']?\s*$", text, re.M)
        if m:
            out[key] = m.group(1).strip()
    return out


def check_version_pins() -> None:
    if not VERSIONS.is_file():
        fail("platform-config/.platform/versions.yaml missing")
    pinned = parse_versions_yaml(VERSIONS.read_text(encoding="utf-8"))
    group_vars = ANSIBLE / "inventories" / "home-lab" / "group_vars" / "all.yaml"
    ansible_pins = parse_group_vars_pins(group_vars.read_text(encoding="utf-8"))

    expected = {
        "k3s_version": pinned.get("k3s_version"),
        "k3s_binary_url": pinned.get("k3s_url"),
        "flux_version": pinned.get("flux_version"),
        "flux_tarball_url": pinned.get("flux_url"),
    }
    for key, want in expected.items():
        if not want:
            fail(f"could not parse {key} from versions.yaml")
        got = ansible_pins.get(key)
        if got != want:
            fail(f"group_vars/all.yaml {key}={got!r} != versions.yaml {want!r}")

    if ansible_pins.get("platform_arch") != "amd64":
        fail("platform_arch must be amd64")


def check_readme_ownership() -> None:
    readme = (ANSIBLE / "README.md").read_text(encoding="utf-8")
    for token in ("Ansible", "Flux", "OpenTofu", "must not", "platform-config"):
        if token not in readme:
            fail(f"ansible/README.md must document ownership token: {token}")


def ensure_inventory_for_syntax() -> Path:
    """Return a .yaml inventory path ansible can parse (not *.example)."""
    hosts = ANSIBLE / "inventories" / "home-lab" / "hosts.yaml"
    if hosts.is_file():
        return hosts
    example = ANSIBLE / "inventories" / "home-lab" / "hosts.yaml.example"
    tmp = ANSIBLE / "inventories" / "home-lab" / ".hosts.syntax-check.yaml"
    tmp.write_text(example.read_text(encoding="utf-8"), encoding="utf-8")
    return tmp


def run_syntax_check() -> None:
    binary = shutil.which("ansible-playbook")
    if not binary:
        print("NOTE: ansible-playbook not installed; skipped syntax-check")
        return

    inventory = ensure_inventory_for_syntax()
    playbook = ANSIBLE / "playbooks" / "site.yml"
    tmp_inventory = inventory.name == ".hosts.syntax-check.yaml"
    try:
        # Collections from requirements.yml must be importable for FQCN modules.
        collections_path = Path.home() / ".ansible" / "collections"
        env = dict(**{k: v for k, v in __import__("os").environ.items()})
        subprocess.run(
            [binary, "--syntax-check", "-i", str(inventory), str(playbook)],
            cwd=ANSIBLE,
            check=True,
            env=env,
        )
    finally:
        if tmp_inventory and inventory.exists():
            inventory.unlink()


def main() -> int:
    try:
        if not ANSIBLE.is_dir():
            fail("ansible/ directory missing")
        check_required_paths()
        check_no_forbidden_ownership()
        check_version_pins()
        check_readme_ownership()
        run_syntax_check()
    except ValidationError as exc:
        print(f"VALIDATION_FAIL ansible-bootstrap: {exc}", file=sys.stderr)
        return 1
    except subprocess.CalledProcessError as exc:
        print(
            f"VALIDATION_FAIL ansible-bootstrap: ansible-playbook failed: {exc}",
            file=sys.stderr,
        )
        return 1

    print("VALIDATION_OK ansible-bootstrap")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
