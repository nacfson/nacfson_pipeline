#!/usr/bin/env python3
"""Onboarding registration and build-contract validation helpers."""

from __future__ import annotations

import json
import re
from pathlib import Path
from typing import Any

import yaml
from jsonschema import Draft7Validator

ROOT = Path(__file__).resolve().parents[1]
PLATFORM = ROOT / "platform-config"
REG_SCHEMA = PLATFORM / ".platform" / "schemas" / "project-registration-v1.json"
BUILD_SCHEMA = PLATFORM / ".platform" / "schemas" / "source-build-v1.json"

FORBIDDEN_BUILD_KEYS = {
    "registryNamespace",
    "imageDigest",
    "accessPolicy",
    "secrets",
    "hostname",
    "replicas",
    "resources",
    "persistence",
}


class OnboardingError(Exception):
    def __init__(self, code: str, message: str, location: str | None = None):
        self.code = code
        self.location = location
        super().__init__(f"{code}: {message}" + (f" @ {location}" if location else ""))


def load_yaml(path: Path) -> Any:
    return yaml.safe_load(path.read_text(encoding="utf-8"))


def validate_registration_doc(doc: dict[str, Any], rel: str) -> dict[str, Any]:
    schema = json.loads(REG_SCHEMA.read_text(encoding="utf-8"))
    errors = sorted(Draft7Validator(schema).iter_errors(doc), key=lambda e: list(e.path))
    if errors:
        err = errors[0]
        path = ".".join(str(p) for p in err.path) or "<root>"
        raise OnboardingError("registration_schema", err.message, f"{rel}:{path}")

    meta_name = doc["metadata"]["name"]
    project = doc["spec"]["project"]
    deployment = doc["spec"]["deploymentPath"]
    registry = doc["spec"]["registryNamespace"]
    if meta_name != project:
        raise OnboardingError("registration_identity", "metadata.name must equal spec.project", rel)
    if deployment != f"applications/{project}":
        raise OnboardingError("registration_path", "deploymentPath must be applications/<project>", rel)
    if registry != f"projects/{project}":
        raise OnboardingError("registration_registry", "registryNamespace must be projects/<project>", rel)

    # Reject source-owned platform fields if smuggled under unexpected keys (schema already forbids).
    runtime = doc["spec"]["runtimePolicy"]
    for forbidden in ("imageDigest", "resources", "replicas", "secrets", "persistence"):
        if forbidden in runtime:
            raise OnboardingError("source_owned_field", f"runtimePolicy must not include {forbidden}", rel)
    return {
        "project": project,
        "repo": f"{doc['spec']['repository']['forgejoOwner']}/{doc['spec']['repository']['forgejoName']}".lower(),
        "deploymentPath": deployment,
        "registryNamespace": registry,
        "path": rel,
    }


def validate_build_doc(doc: dict[str, Any], rel: str) -> None:
    schema = json.loads(BUILD_SCHEMA.read_text(encoding="utf-8"))
    errors = sorted(Draft7Validator(schema).iter_errors(doc), key=lambda e: list(e.path))
    if errors:
        err = errors[0]
        path = ".".join(str(p) for p in err.path) or "<root>"
        raise OnboardingError("build_schema", err.message, f"{rel}:{path}")
    for key in FORBIDDEN_BUILD_KEYS:
        if key in doc:
            raise OnboardingError("build_platform_field", f"build contract must not set {key}", rel)
    for field in ("context", "dockerfile"):
        value = doc[field]
        if value.startswith("/") or ".." in Path(value).parts:
            raise OnboardingError("build_path_escape", f"{field} escapes checkout", rel)


def iter_registrations(apps: Path) -> list[dict[str, Any]]:
    out: list[dict[str, Any]] = []
    if not apps.exists():
        return out
    for path in sorted(apps.glob("*/registration.yaml")):
        if ".project-template" in path.parts:
            continue
        doc = load_yaml(path)
        if not isinstance(doc, dict):
            raise OnboardingError("registration_invalid", "registration must be a mapping", str(path))
        out.append(validate_registration_doc(doc, str(path.relative_to(ROOT))))
    return out


def validate_registration_uniqueness(regs: list[dict[str, Any]]) -> None:
    seen = {
        "project": {},
        "repo": {},
        "deploymentPath": {},
        "registryNamespace": {},
    }
    for reg in regs:
        for key in seen:
            value = reg[key]
            if value in seen[key]:
                raise OnboardingError(
                    "registration_collision",
                    f"duplicate {key} {value} also used by {seen[key][value]}",
                    reg["path"],
                )
            seen[key][value] = reg["path"]


def validate_registration_not_in_kustomize(apps: Path) -> None:
    for kust in apps.rglob("kustomization.yaml"):
        if ".project-template" in kust.parts:
            # template also must not list registration as a resource
            pass
        doc = load_yaml(kust) or {}
        resources = doc.get("resources") or []
        for res in resources:
            name = str(res)
            if name.endswith("registration.yaml") or name == "registration.yaml":
                raise OnboardingError(
                    "registration_as_resource",
                    "registration.yaml must not be listed as a Kustomize resource",
                    str(kust.relative_to(ROOT)),
                )


def validate_template_registration() -> None:
    path = PLATFORM / "applications" / ".project-template" / "registration.yaml"
    doc = load_yaml(path)
    validate_registration_doc(doc, str(path.relative_to(ROOT)))
    validate_registration_not_in_kustomize(PLATFORM / "applications")


def validate_example_build_contract() -> None:
    path = PLATFORM / ".platform" / "build.example.yaml"
    validate_build_doc(load_yaml(path), str(path.relative_to(ROOT)))
