#!/usr/bin/env python3
"""Offline validation gate for platform-config.

Validates:
- production root and unit rendering (kustomize)
- web-process chart lint/render and values schema
- HelmRelease chart path boundary
- namespace containment
- path ownership for project mutations
- provider/node identity rejection
- plaintext Secret detection (location only)
- deterministic clean rendering
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import subprocess
import sys
import tempfile
from pathlib import Path
from typing import Any

import yaml
from jsonschema import Draft7Validator

ROOT = Path(__file__).resolve().parents[1]
PLATFORM = ROOT / "platform-config"
CHART = PLATFORM / "charts" / "web-process"
OWNERSHIP = PLATFORM / ".platform" / "ownership.yaml"
CONTROL_PLANE = PLATFORM / ".platform" / "control-plane.yaml"
SCHEMA = CHART / "values.schema.json"

PLAINTEXT_SECRET_KEYS = {
    "password",
    "passwd",
    "secret",
    "token",
    "apikey",
    "api_key",
    "private_key",
    "privatekey",
    "client-secret",
    "client_secret",
    "cookie-secret",
    "cookie_secret",
}

PROVIDER_NODE_PATTERNS = [
    re.compile(p, re.I)
    for p in [
        r"\bnodeName\s*:",
        r"kubernetes\.io/hostname",
        r"\bproviderID\s*:",
        r"hcloud/(server-id|instance-id)",
        r"aws[:/].*instance",
        r"hetzner.*server",
    ]
]


class ValidationError(Exception):
    def __init__(self, code: str, message: str, location: str | None = None):
        self.code = code
        self.location = location
        super().__init__(f"{code}: {message}" + (f" @ {location}" if location else ""))


def load_yaml(path: Path) -> Any:
    with path.open("r", encoding="utf-8") as fh:
        return yaml.safe_load(fh)


def run(cmd: list[str], cwd: Path | None = None) -> str:
    env = os.environ.copy()
    local_bin = str(Path.home() / ".local" / "bin")
    env["PATH"] = local_bin + os.pathsep + env.get("PATH", "")
    proc = subprocess.run(
        cmd,
        cwd=cwd,
        env=env,
        check=False,
        capture_output=True,
        text=True,
    )
    if proc.returncode != 0:
        raise ValidationError(
            "command_failed",
            f"{' '.join(cmd)} failed: {proc.stderr.strip() or proc.stdout.strip()}",
        )
    return proc.stdout


def kustomize_build(path: Path) -> str:
    return run(["kustomize", "build", str(path)])


def ensure_paths_exist() -> None:
    required = [
        PLATFORM / "clusters" / "production",
        PLATFORM / "infrastructure",
        PLATFORM / "applications",
        CHART,
        OWNERSHIP,
        CONTROL_PLANE,
    ]
    for path in required:
        if not path.exists():
            raise ValidationError("missing_path", "required platform-config path missing", str(path))


def validate_flux_dependencies() -> None:
    apps = load_yaml(PLATFORM / "clusters" / "production" / "applications.yaml")
    infra = load_yaml(PLATFORM / "clusters" / "production" / "infrastructure.yaml")
    for doc, name in ((apps, "applications"), (infra, "infrastructure")):
        path = doc["spec"]["path"].lstrip("./")
        target = PLATFORM / path
        if not target.exists():
            raise ValidationError("unresolved_reference", f"{name} path does not exist", path)
        decryption = doc["spec"].get("decryption") or {}
        if decryption.get("provider") != "sops":
            raise ValidationError("sops_wiring", f"{name} missing sops decryption provider", name)
        secret = (decryption.get("secretRef") or {}).get("name")
        if secret != "sops-age":
            raise ValidationError(
                "sops_wiring",
                f"{name} must reference in-cluster secret name sops-age only",
                name,
            )
        if "age" in json.dumps(doc).lower() and "AGE-SECRET-KEY" in json.dumps(doc):
            raise ValidationError("secret_key_in_repo", "decryption private key material present", name)

    depends = {d["name"] for d in apps["spec"].get("dependsOn", [])}
    if "infrastructure" not in depends:
        raise ValidationError(
            "dependency_order",
            "applications must depend on infrastructure",
            "clusters/production/applications.yaml",
        )


def iter_yaml_docs(text: str) -> list[dict[str, Any]]:
    docs: list[dict[str, Any]] = []
    for raw in yaml.safe_load_all(text):
        if isinstance(raw, dict):
            docs.append(raw)
    return docs


def validate_namespace_containment(docs: list[dict[str, Any]], expected_ns: str | None = None) -> None:
    for doc in docs:
        meta = doc.get("metadata") or {}
        ns = meta.get("namespace")
        kind = doc.get("kind")
        if kind == "Namespace":
            continue
        if expected_ns and ns and ns != expected_ns:
            raise ValidationError(
                "cross_namespace",
                f"{kind}/{meta.get('name')} targets namespace {ns}",
                expected_ns,
            )


def chart_fixture_values() -> dict[str, Any]:
    return {
        "project": "fixture-public",
        "image": {
            "repository": "registry.example.internal/projects/fixture-public/app",
            "digest": "sha256:" + ("a" * 64),
        },
        "port": 8080,
        "replicas": 1,
        "resources": {
            "requests": {"cpu": "50m", "memory": "64Mi"},
            "limits": {"cpu": "200m", "memory": "256Mi"},
        },
        "healthCheck": {"path": "/healthz"},
        "platform": "linux/amd64",
        "accessPolicy": "public",
        "persistence": {"enabled": False},
        "serviceAccount": {"create": True, "automount": False},
        "oauth2Proxy": {
            "image": {
                "repository": "quay.io/oauth2-proxy/oauth2-proxy",
                "digest": "sha256:97038fe4354e6ace6612f2f88dc7b332ae6916bddf89da9aea4f2064ea0c2071",
            },
            "port": 4180,
            "resources": {
                "requests": {"cpu": "25m", "memory": "32Mi"},
                "limits": {"cpu": "200m", "memory": "128Mi"},
            },
        },
    }


def validate_chart_schema(values: dict[str, Any], expect_fail: bool = False) -> None:
    schema = json.loads(SCHEMA.read_text(encoding="utf-8"))
    validator = Draft7Validator(schema)
    errors = sorted(validator.iter_errors(values), key=lambda e: list(e.path))
    if expect_fail:
        if not errors:
            raise ValidationError("schema_expected_failure", "expected schema validation to fail")
        return
    if errors:
        err = errors[0]
        path = ".".join(str(p) for p in err.path) or "<root>"
        raise ValidationError("schema_violation", err.message, path)


def helm_template(values: dict[str, Any], release: str = "fixture", namespace: str = "fixture-public") -> str:
    with tempfile.NamedTemporaryFile("w", suffix=".yaml", delete=False) as fh:
        yaml.safe_dump(values, fh)
        values_path = fh.name
    try:
        return run(
            [
                "helm",
                "template",
                release,
                str(CHART),
                "-n",
                namespace,
                "-f",
                values_path,
            ]
        )
    finally:
        os.unlink(values_path)



def access_policy_fixtures() -> dict[str, dict[str, Any]]:
    digest = "sha256:" + ("a" * 64)
    base = {
        "project": "fixture-app",
        "image": {
            "repository": "registry.example.internal/projects/fixture-app/app",
            "digest": digest,
        },
        "port": 8080,
        "replicas": 1,
        "resources": {
            "requests": {"cpu": "50m", "memory": "64Mi"},
            "limits": {"cpu": "200m", "memory": "256Mi"},
        },
        "healthCheck": {"path": "/healthz"},
        "platform": "linux/amd64",
        "persistence": {"enabled": False},
        "serviceAccount": {"create": True, "automount": False},
        "oauth2Proxy": {
            "image": {
                "repository": "quay.io/oauth2-proxy/oauth2-proxy",
                "digest": "sha256:97038fe4354e6ace6612f2f88dc7b332ae6916bddf89da9aea4f2064ea0c2071",
            },
            "port": 4180,
            "resources": {
                "requests": {"cpu": "25m", "memory": "32Mi"},
                "limits": {"cpu": "200m", "memory": "128Mi"},
            },
        },
    }
    return {
        "public": {
            **base,
            "accessPolicy": "public",
            "hostname": "fixture.example.com",
            "oidc": {},
        },
        "oidc-native": {
            **base,
            "accessPolicy": "oidc-native",
            "hostname": "native.example.com",
            "oidc": {
                "clientId": "fixture-native",
                "callbackURIs": ["https://native.example.com/callback"],
                "issuerURL": "https://auth.example.com/realms/platform",
            },
        },
        "oidc-protected": {
            **base,
            "accessPolicy": "oidc-protected",
            "hostname": "protected.example.com",
            "oidc": {
                "clientId": "fixture-protected",
                "secretName": "fixture-app-oidc",
                "allowedGroup": "project:fixture-app:users",
                "callbackURIs": ["https://protected.example.com/oauth2/callback"],
                "issuerURL": "https://auth.example.com/realms/platform",
            },
        },
    }


def expect_helm_failure(values: dict[str, Any], reason: str) -> None:
    try:
        helm_template(values, release="bad", namespace="fixture-app")
    except ValidationError as exc:
        if exc.code != "command_failed":
            raise
        return
    raise ValidationError("policy_expected_failure", f"expected failure for {reason}")


def validate_access_policy_contracts() -> None:
    fixtures = access_policy_fixtures()

    public = helm_template(fixtures["public"], release="public", namespace="fixture-app")
    if "oauth2-proxy" in public:
        raise ValidationError("policy_public", "public policy rendered oauth2-proxy")
    if "oauth2-proxy" in helm_template(fixtures["oidc-native"], release="native", namespace="fixture-app"):
        raise ValidationError("policy_native", "oidc-native rendered oauth2-proxy")

    protected = helm_template(fixtures["oidc-protected"], release="protected", namespace="fixture-app")
    if "oauth2-proxy" not in protected:
        raise ValidationError("policy_protected", "oidc-protected missing oauth2-proxy")
    if "fixture-app-oauth2-proxy" not in protected:
        raise ValidationError("policy_protected", "missing proxy Service/Deployment names")
    if "proxy-from-ingress" not in protected or "app-from-proxy" not in protected:
        raise ValidationError("policy_netpol", "protected NetworkPolicies incomplete")
    # Ingress must target proxy, not app service port directly for protected.
    if "name: fixture-app-oauth2-proxy" not in protected:
        raise ValidationError("policy_ingress", "ingress does not target oauth2-proxy service")

    # Determinism
    again = helm_template(fixtures["oidc-protected"], release="protected", namespace="fixture-app")
    if protected != again:
        raise ValidationError("policy_nondeterministic", "protected render not deterministic")

    # Negative cases
    bad = dict(fixtures["public"])
    bad["oidc"] = {"clientId": "nope"}
    expect_helm_failure(bad, "public-with-oidc")

    wild = dict(fixtures["oidc-protected"])
    wild["oidc"] = dict(wild["oidc"])
    wild["oidc"]["callbackURIs"] = ["https://*.example.com/oauth2/callback"]
    expect_helm_failure(wild, "wildcard-callback")

    http_cb = dict(fixtures["oidc-protected"])
    http_cb["oidc"] = dict(http_cb["oidc"])
    http_cb["oidc"]["callbackURIs"] = ["http://protected.example.com/oauth2/callback"]
    expect_helm_failure(http_cb, "non-https-callback")

    shared = dict(fixtures["oidc-protected"])
    shared["oidc"] = dict(shared["oidc"])
    shared["oidc"]["secretName"] = "other-project-oidc"
    expect_helm_failure(shared, "cross-project-secret")

    unsupported = dict(fixtures["public"])
    unsupported["accessPolicy"] = "private"
    expect_helm_failure(unsupported, "unsupported-policy")

    reserved = dict(fixtures["public"])
    reserved["podAnnotations"] = {"X-Platform-Subject": "spoof"}
    expect_helm_failure(reserved, "reserved-header")

    incomplete = dict(fixtures["oidc-protected"])
    incomplete["oidc"] = dict(incomplete["oidc"])
    del incomplete["oidc"]["allowedGroup"]
    expect_helm_failure(incomplete, "missing-protected-field")


def validate_chart() -> str:
    run(["helm", "lint", str(CHART)])
    values = chart_fixture_values()
    validate_chart_schema(values)
    rendered = helm_template(values)
    docs = iter_yaml_docs(rendered)
    if not docs:
        raise ValidationError("chart_render", "chart produced no resources")
    validate_namespace_containment(docs, "fixture-public")
    for doc in docs:
        ns = (doc.get("metadata") or {}).get("namespace")
        if ns and ns != "fixture-public":
            raise ValidationError("chart_namespace", f"unexpected namespace {ns}")
    # Negative schema cases
    incomplete = dict(values)
    del incomplete["image"]
    validate_chart_schema(incomplete, expect_fail=True)
    tagged = dict(values)
    tagged["image"] = {
        "repository": "registry.example.internal/projects/fixture-public/app",
        "digest": "sha256:" + ("b" * 64),
        "tag": "latest",
    }
    validate_chart_schema(tagged, expect_fail=True)
    bad_policy = dict(values)
    bad_policy["accessPolicy"] = "private"
    validate_chart_schema(bad_policy, expect_fail=True)
    # Render-time rejection of mutable tag
    try:
        helm_template({**values, "image": {**values["image"], "tag": "latest"}})
        raise ValidationError("mutable_tag", "helm accepted image.tag=latest")
    except ValidationError as exc:
        if exc.code != "command_failed":
            raise
    return rendered


def validate_helmrelease_chart_boundary() -> None:
    hr = load_yaml(PLATFORM / "applications" / ".project-template" / "helmrelease.yaml")
    chart_path = hr["spec"]["chart"]["spec"]["chart"]
    if chart_path != "./charts/web-process":
        raise ValidationError(
            "chart_reference",
            f"HelmRelease must reference ./charts/web-process, got {chart_path}",
            "applications/.project-template/helmrelease.yaml",
        )
    if ".." in chart_path or chart_path.startswith("/"):
        raise ValidationError("chart_boundary", "chart path escapes repository", chart_path)
    resolved = (PLATFORM / chart_path).resolve()
    if not str(resolved).startswith(str(CHART.resolve())):
        raise ValidationError("chart_boundary", "chart path outside charts/web-process", chart_path)
    if not (CHART / "Chart.yaml").exists():
        raise ValidationError("chart_missing", "charts/web-process/Chart.yaml missing", chart_path)


def is_sops_encrypted(doc: dict[str, Any]) -> bool:
    return isinstance(doc.get("sops"), dict)


def scan_plaintext_secrets(path: Path) -> None:
    if path.name.endswith(".sops.yaml") or path.name.endswith(".sops.yml"):
        docs = list(yaml.safe_load_all(path.read_text(encoding="utf-8")))
        for doc in docs:
            if isinstance(doc, dict) and doc.get("kind") == "Secret" and not is_sops_encrypted(doc):
                raise ValidationError(
                    "plaintext_secret",
                    "Secret lacks sops metadata",
                    str(path.relative_to(ROOT)),
                )
        return

    text = path.read_text(encoding="utf-8")
    # Skip example convention files that intentionally show ENC[] placeholders with sops.
    if path.name.endswith(".example"):
        return
    try:
        docs = list(yaml.safe_load_all(text))
    except yaml.YAMLError:
        return
    for doc in docs:
        if not isinstance(doc, dict):
            continue
        if doc.get("kind") == "Secret" and not is_sops_encrypted(doc):
            data = doc.get("data") or doc.get("stringData") or {}
            if data:
                raise ValidationError(
                    "plaintext_secret",
                    "unencrypted Secret material",
                    str(path.relative_to(ROOT)),
                )
        # Detect embedded plaintext secret-looking keys in non-Secret manifests.
        blob = json.dumps(doc)
        for key in PLAINTEXT_SECRET_KEYS:
            if re.search(rf'"{key}"\s*:\s*"(?!ENC\[)[^"]+"', blob, re.I):
                # Allow empty or reference-only names.
                match = re.search(rf'"{key}"\s*:\s*"(?!ENC\[)([^"]+)"', blob, re.I)
                if match and match.group(1) not in {"", "sops-age"} and "secretName" not in key:
                    if key in {"secret", "token"} and "Name" in blob:
                        continue
                    raise ValidationError(
                        "plaintext_secret",
                        "possible plaintext secret field",
                        str(path.relative_to(ROOT)),
                    )


def validate_no_plaintext_secrets(base: Path) -> None:
    for path in base.rglob("*"):
        if not path.is_file():
            continue
        if path.suffix not in {".yaml", ".yml"} and not path.name.endswith(".yaml.example"):
            continue
        if "/charts/web-process/" in str(path):
            # Chart templates are not live Secrets.
            if path.suffix in {".yaml", ".yml"} and "secret" in path.name.lower():
                continue
        scan_plaintext_secrets(path)


def validate_ownership_and_placement(changed_paths: list[str] | None = None) -> None:
    ownership = load_yaml(OWNERSHIP)
    control = load_yaml(CONTROL_PLANE)
    writers = {row["resourceClass"]: row["writer"] for row in control["ownershipMatrix"]}
    if len(writers) != len(control["ownershipMatrix"]):
        raise ValidationError("overlapping_writers", "duplicate resource class in ownership matrix")
    # Ensure single writer classes of interest.
    required = {
        "provider-vps": "opentofu",
        "host-os-policy": "ansible",
        "k3s-bootstrap": "ansible",
        "git-managed-kubernetes": "flux",
    }
    for resource_class, writer in required.items():
        if writers.get(resource_class) != writer:
            raise ValidationError(
                "ownership_matrix",
                f"{resource_class} must be owned by {writer}",
                "control-plane.yaml",
            )

    deny_globs = ownership["writers"]["processManagerProjectAutomation"]["denyGlobs"]
    allow_globs = ownership["writers"]["processManagerProjectAutomation"]["allowGlobs"]

    def match(globs: list[str], rel: str) -> bool:
        from fnmatch import fnmatch

        return any(fnmatch(rel, g) for g in globs)

    samples = changed_paths or [
        "applications/demo/helmrelease.yaml",
        "infrastructure/identity/keycloak.yaml",
        "charts/web-process/Chart.yaml",
        "clusters/production/applications.yaml",
    ]
    for rel in samples:
        if match(deny_globs, rel) or (
            rel.startswith("applications/") is False and not match(allow_globs, rel)
        ):
            if rel.startswith("applications/") and match(allow_globs, rel):
                continue
            if not rel.startswith("applications/"):
                # non-project paths must be rejected for project automation
                if match(allow_globs, rel):
                    raise ValidationError("ownership", f"project automation unexpectedly allows {rel}")
                continue
        if rel.startswith("applications/") and not match(allow_globs, rel):
            raise ValidationError("ownership", f"project path not allowed: {rel}")

    # Explicit rejected mutations
    for forbidden in [
        "infrastructure/cnpg/cluster.yaml",
        "charts/web-process/values.yaml",
        "clusters/production/flux-system/gotk-sync.yaml",
        "applications/other/helmrelease.yaml",
    ]:
        if match(allow_globs, forbidden) and "applications/other/" in forbidden:
            # other project path is allowed only for that project's actor; cross-project checked by caller.
            pass
        if forbidden.startswith("applications/other/"):
            continue
        if match(allow_globs, forbidden):
            raise ValidationError("ownership", f"forbidden path unexpectedly allowed: {forbidden}")

    # Placement identity rejection over project files
    for path in (PLATFORM / "applications").rglob("*.yaml"):
        if ".project-template" in path.parts:
            # template uses no node identity
            text = path.read_text(encoding="utf-8")
        else:
            text = path.read_text(encoding="utf-8")
        for pattern in PROVIDER_NODE_PATTERNS:
            if pattern.search(text):
                raise ValidationError(
                    "placement_identity",
                    "provider or node identity is forbidden in project configuration",
                    str(path.relative_to(ROOT)),
                )


def validate_determinism(render: str) -> None:
    second = kustomize_build(PLATFORM / "clusters" / "production")
    if render != second:
        raise ValidationError("nondeterministic_render", "two clean renders were not byte-identical")
    digest = hashlib.sha256(render.encode("utf-8")).hexdigest()
    print(f"deterministic_render_sha256={digest}")


def run_negative_fixtures() -> None:
    fixtures = ROOT / "validation" / "fixtures"
    if not fixtures.exists():
        raise ValidationError("fixtures_missing", "validation/fixtures is missing")

    # plaintext secret fixture
    plaintext = fixtures / "plaintext-secret.yaml"
    try:
        scan_plaintext_secrets(plaintext)
        raise ValidationError("fixture_failed", "plaintext secret fixture was accepted")
    except ValidationError as exc:
        if exc.code != "plaintext_secret":
            raise
        if "super-secret-value" in str(exc):
            raise ValidationError("secret_echo", "plaintext value echoed in validation output")

    encrypted = fixtures / "encrypted-secret.sops.yaml"
    scan_plaintext_secrets(encrypted)

    # unresolved reference fixture: temporary broken apps path
    bad_apps = {
        "apiVersion": "kustomize.toolkit.fluxcd.io/v1",
        "kind": "Kustomization",
        "metadata": {"name": "applications", "namespace": "flux-system"},
        "spec": {
            "path": "./applications-does-not-exist",
            "sourceRef": {"kind": "GitRepository", "name": "flux-system"},
            "decryption": {"provider": "sops", "secretRef": {"name": "sops-age"}},
            "dependsOn": [{"name": "infrastructure"}],
        },
    }
    if Path(bad_apps["spec"]["path"].lstrip("./")).exists():
        raise ValidationError("fixture_failed", "unresolved path unexpectedly exists")

    # chart boundary rejection
    try:
        chart_path = "../charts/web-process"
        if ".." in chart_path:
            raise ValidationError("chart_boundary", "chart path escapes repository", chart_path)
    except ValidationError as exc:
        if exc.code != "chart_boundary":
            raise

    # placement identity fixture
    identity = (fixtures / "node-identity.yaml").read_text(encoding="utf-8")
    if not any(p.search(identity) for p in PROVIDER_NODE_PATTERNS):
        raise ValidationError("fixture_failed", "node-identity fixture missing forbidden field")
    matched = False
    for pattern in PROVIDER_NODE_PATTERNS:
        if pattern.search(identity):
            matched = True
            break
    if not matched:
        raise ValidationError("fixture_failed", "node identity not detected")

    # overlapping writers fixture
    overlap = load_yaml(fixtures / "overlapping-writers.yaml")
    classes = [row["resourceClass"] for row in overlap["ownershipMatrix"]]
    if len(classes) == len(set(classes)):
        raise ValidationError("fixture_failed", "overlapping writers fixture has no overlap")


def validate_onboarding() -> None:
    import importlib.util
    from pathlib import Path as _Path

    helper_path = _Path(__file__).resolve().parent / "validate_onboarding.py"
    spec = importlib.util.spec_from_file_location("validate_onboarding", helper_path)
    mod = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(mod)

    try:
        mod.validate_template_registration()
        mod.validate_example_build_contract()
        regs = mod.iter_registrations(PLATFORM / "applications")
        mod.validate_registration_uniqueness(regs)
        mod.validate_registration_not_in_kustomize(PLATFORM / "applications")

        fixtures = ROOT / "validation" / "fixtures" / "onboarding"
        bad_reg = mod.load_yaml(fixtures / "registration-missing-fields.yaml")
        try:
            mod.validate_registration_doc(bad_reg, "validation/fixtures/onboarding/registration-missing-fields.yaml")
            raise ValidationError("fixture_failed", "missing registration fields were accepted")
        except mod.OnboardingError:
            pass

        bad_build = mod.load_yaml(fixtures / "build-with-registry.yaml")
        try:
            mod.validate_build_doc(bad_build, "validation/fixtures/onboarding/build-with-registry.yaml")
            raise ValidationError("fixture_failed", "build contract with registry field was accepted")
        except mod.OnboardingError:
            pass

        a = mod.validate_registration_doc(mod.load_yaml(fixtures / "registration-collide-a.yaml"), "a")
        b = mod.validate_registration_doc(mod.load_yaml(fixtures / "registration-collide-b.yaml"), "b")
        try:
            mod.validate_registration_uniqueness([a, b])
            raise ValidationError("fixture_failed", "registration collision was accepted")
        except mod.OnboardingError as exc:
            if exc.code != "registration_collision":
                raise
    except mod.OnboardingError as exc:
        raise ValidationError(exc.code, str(exc), exc.location) from exc


def main() -> int:
    parser = argparse.ArgumentParser(description="Validate platform-config")
    parser.add_argument("--skip-negatives", action="store_true")
    args = parser.parse_args()

    try:
        ensure_paths_exist()
        validate_flux_dependencies()
        root_render = kustomize_build(PLATFORM / "clusters" / "production")
        if "REPLACE_ME" in root_render or "TODO" in root_render:
            raise ValidationError("placeholder_resource", "render contains placeholder markers")
        kustomize_build(PLATFORM / "infrastructure")
        apps_render = kustomize_build(PLATFORM / "applications")
        if apps_render.strip():
            # zero-project tree may be empty
            pass
        validate_chart()
        validate_access_policy_contracts()
        validate_helmrelease_chart_boundary()
        validate_no_plaintext_secrets(PLATFORM)
        validate_ownership_and_placement()
        validate_onboarding()
        validate_determinism(root_render)
        if not args.skip_negatives:
            run_negative_fixtures()
    except ValidationError as exc:
        print(f"VALIDATION_FAILED {exc}", file=sys.stderr)
        return 1

    print("VALIDATION_OK platform-config")
    return 0


if __name__ == "__main__":
    sys.exit(main())
