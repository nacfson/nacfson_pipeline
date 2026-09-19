#!/usr/bin/env python3
"""Structural validation for infra/ OpenTofu layout.

Always checks ownership boundaries and required files. If `tofu` or `terraform`
is on PATH, also runs init/validate for environments/home-lab.
"""

from __future__ import annotations

import re
import shutil
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
INFRA = ROOT / "infra"

REQUIRED_PATHS = [
    "README.md",
    "modules/node/main.tf",
    "modules/node/variables.tf",
    "modules/node/outputs.tf",
    "modules/dns/main.tf",
    "modules/dns/variables.tf",
    "modules/dns/outputs.tf",
    "modules/firewall/main.tf",
    "modules/firewall/variables.tf",
    "modules/firewall/outputs.tf",
    "modules/backup_target/main.tf",
    "modules/backup_target/variables.tf",
    "modules/backup_target/outputs.tf",
    "environments/home-lab/main.tf",
    "environments/home-lab/variables.tf",
    "environments/home-lab/outputs.tf",
    "environments/home-lab/versions.tf",
    "environments/home-lab/providers.tf",
    "environments/home-lab/terraform.tfvars.example",
    "environments/cloud/main.tf",
    "handoff/.gitkeep",
]

FORBIDDEN_PROVIDER_PATTERNS = [
    re.compile(r'provider\s+"kubernetes"', re.I),
    re.compile(r'provider\s+"helm"', re.I),
    re.compile(r'provider\s+"kubectl"', re.I),
    re.compile(r'source\s*=\s*"hashicorp/kubernetes"', re.I),
    re.compile(r'source\s*=\s*"hashicorp/helm"', re.I),
    re.compile(r'resource\s+"kubernetes_', re.I),
    re.compile(r'resource\s+"helm_', re.I),
]


class ValidationError(Exception):
    pass


def fail(msg: str) -> None:
    raise ValidationError(msg)


def check_required_paths() -> None:
    for rel in REQUIRED_PATHS:
        path = INFRA / rel
        if not path.exists():
            fail(f"missing required path: infra/{rel}")


def check_no_k8s_providers() -> None:
    for path in INFRA.rglob("*.tf"):
        text = path.read_text(encoding="utf-8")
        for pat in FORBIDDEN_PROVIDER_PATTERNS:
            if pat.search(text):
                fail(f"forbidden Kubernetes/Helm usage in {path.relative_to(ROOT)}: {pat.pattern}")


def check_readme_ownership() -> None:
    readme = (INFRA / "README.md").read_text(encoding="utf-8")
    for token in ("OpenTofu", "Ansible", "Flux", "Kubernetes"):
        if token not in readme:
            fail(f"infra/README.md must document {token} ownership")


def find_tofu() -> str | None:
    for name in ("tofu", "terraform"):
        path = shutil.which(name)
        if path:
            return path
    return None


def run_tofu_validate() -> None:
    binary = find_tofu()
    if not binary:
        print("NOTE: tofu/terraform not installed; skipped init/validate")
        return

    env_dir = INFRA / "environments" / "home-lab"
    tfvars_example = env_dir / "terraform.tfvars.example"
    tfvars = env_dir / "terraform.tfvars"
    created_tfvars = False
    if not tfvars.exists():
        tfvars.write_text(tfvars_example.read_text(encoding="utf-8"), encoding="utf-8")
        created_tfvars = True

    try:
        subprocess.run([binary, "init", "-backend=false", "-input=false"], cwd=env_dir, check=True)
        subprocess.run([binary, "validate"], cwd=env_dir, check=True)
    finally:
        if created_tfvars and tfvars.exists():
            tfvars.unlink()


def main() -> int:
    try:
        if not INFRA.is_dir():
            fail("infra/ directory missing")
        check_required_paths()
        check_no_k8s_providers()
        check_readme_ownership()
        run_tofu_validate()
    except ValidationError as exc:
        print(f"VALIDATION_FAIL opentofu-infra: {exc}", file=sys.stderr)
        return 1
    except subprocess.CalledProcessError as exc:
        print(f"VALIDATION_FAIL opentofu-infra: tofu command failed: {exc}", file=sys.stderr)
        return 1

    print("VALIDATION_OK opentofu-infra")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
