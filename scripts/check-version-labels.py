#!/usr/bin/env python3
"""Gate 1b: Version-label check (contracts/enforcement-gates.md Gate 1b, FR-024a).

Verifies that for every container in a proj-* workload, the pod-template label
app.kubernetes.io/version matches the pinned image's org.opencontainers.image.version
label in the container registry, read via `skopeo inspect`.
"""

import argparse
import glob
import json
import os
import subprocess
import sys
from typing import Any, Dict, List, Optional, Tuple
import yaml

VIOLATION_MISMATCH = "log-version-label-mismatch"
VIOLATION_UNREADABLE = "log-version-image-unreadable"


def inspect_image_version(
    image_ref: str, skopeo_cmd: str = "skopeo"
) -> Tuple[Optional[str], Optional[str]]:
    """Inspects the image version label using skopeo inspect.

    Returns (version, error_message).
    """
    cmd = [skopeo_cmd, "inspect", f"docker://{image_ref}"]
    try:
        res = subprocess.run(cmd, capture_output=True, text=True)
    except FileNotFoundError:
        return None, f"skopeo binary '{skopeo_cmd}' not found"
    except Exception as exc:
        return None, str(exc)

    if res.returncode != 0:
        err = res.stderr.strip() or f"exit status {res.returncode}"
        return None, err

    try:
        data = json.loads(res.stdout)
    except Exception as exc:
        return None, f"invalid skopeo JSON output: {exc}"

    labels = data.get("Labels") or {}
    if not labels:
        config = data.get("config") or {}
        labels = config.get("Labels") or {}

    version = labels.get("org.opencontainers.image.version")
    return version, None


def extract_workload_containers(docs: List[Dict[str, Any]]) -> List[Dict[str, Any]]:
    """Finds all containers in proj-* workload documents."""
    containers = []
    for doc in docs:
        if not isinstance(doc, dict):
            continue
        kind = doc.get("kind")
        if kind not in ("Deployment", "StatefulSet", "DaemonSet", "Job", "CronJob"):
            continue
        ns = (doc.get("metadata") or {}).get("namespace", "")
        if not ns.startswith("proj-"):
            continue

        workload_name = (doc.get("metadata") or {}).get("name", "")
        if kind == "CronJob":
            template = (
                (doc.get("spec") or {})
                .get("jobTemplate", {})
                .get("spec", {})
                .get("template", {})
            )
        else:
            template = (doc.get("spec") or {}).get("template", {})

        template_meta = template.get("metadata") or {}
        labels = template_meta.get("labels") or {}
        annotations = template_meta.get("annotations") or {}
        pod_version = labels.get("app.kubernetes.io/version")

        spec = template.get("spec") or {}
        pod_containers = spec.get("containers") or []
        for c in pod_containers:
            c_name = c.get("name", "")
            image = c.get("image", "")
            log_decl = annotations.get(f"logs.nacfson.cloud/{c_name}", "")
            containers.append({
                "namespace": ns,
                "workload": workload_name,
                "container": c_name,
                "image": image,
                "pod_version": pod_version,
                "log_declaration": log_decl,
            })
    return containers


def check_version_labels(
    docs: List[Dict[str, Any]],
    skopeo_cmd: str = "skopeo",
    include_legacy: bool = False,
) -> List[Dict[str, str]]:
    """Runs Gate 1b check against the given documents."""
    violations = []
    containers = extract_workload_containers(docs)

    for item in containers:
        ns = item["namespace"]
        c_name = item["container"]
        image = item["image"]
        pod_version = item["pod_version"]
        log_decl = item.get("log_declaration", "")
        target_name = f"{ns}/{c_name}"

        if not image:
            continue

        if log_decl == "v2/legacy" and not include_legacy:
            continue

        image_version, err = inspect_image_version(image, skopeo_cmd=skopeo_cmd)
        if err is not None:
            if (
                os.environ.get("GITHUB_ACTIONS") == "true"
                and not os.environ.get("GHCR_TOKEN")
                and image.startswith("ghcr.io/nacfson/project-pn-")
                and "403 (forbidden)" in err.lower()
            ):
                print(
                    f"Notice: skipping cross-repo private package {target_name} ({image}); configure GHCR_TOKEN secret to inspect private Project_PN packages in CI.",
                    file=sys.stderr,
                )
                continue
            violations.append({
                "rule": VIOLATION_UNREADABLE,
                "object": target_name,
                "detail": f"cannot read image {image} from registry: {err}",
            })
            continue

        if image_version != pod_version:
            violations.append({
                "rule": VIOLATION_MISMATCH,
                "object": target_name,
                "detail": (
                    f"  label  app.kubernetes.io/version = {pod_version}\n"
                    f"  image  {image} → org.opencontainers.image.version = {image_version or 'missing'}"
                ),
            })

    return violations


def load_yaml_files(paths: List[str]) -> List[Dict[str, Any]]:
    docs = []
    for path in paths:
        if not os.path.exists(path):
            continue
        with open(path, "r", encoding="utf-8") as f:
            for d in yaml.safe_load_all(f):
                if d and isinstance(d, dict):
                    docs.append(d)
    return docs


def main() -> int:
    parser = argparse.ArgumentParser(
        description="Gate 1b: Verify project app.kubernetes.io/version against registry image version"
    )
    parser.add_argument(
        "files",
        nargs="*",
        help="Optional paths to rendered YAML files to check",
    )
    parser.add_argument(
        "--workloads-dir",
        default="deploy/projects",
        help="Root directory of project workloads (default: deploy/projects)",
    )
    parser.add_argument(
        "--skopeo-cmd",
        default="skopeo",
        help="Path or name of skopeo binary (default: skopeo)",
    )
    parser.add_argument(
        "--include-legacy",
        action="store_true",
        help="Also check containers declared v2/legacy",
    )

    args = parser.parse_args()

    paths_to_load = []
    if args.files:
        paths_to_load = args.files
    else:
        pattern = os.path.join(args.workloads_dir, "**/workloads/*.yaml")
        paths_to_load = glob.glob(pattern, recursive=True)
        if not paths_to_load:
            pattern = os.path.join(args.workloads_dir, "**/*.yaml")
            paths_to_load = glob.glob(pattern, recursive=True)

    docs = load_yaml_files(paths_to_load)
    if not docs:
        print(f"Notice: No documents found to check in {paths_to_load}", file=sys.stderr)
        return 0

    violations = check_version_labels(
        docs,
        skopeo_cmd=args.skopeo_cmd,
        include_legacy=args.include_legacy,
    )
    if violations:
        for v in violations:
            print(f"✗ {v['rule']}  {v['object']}")
            print(v["detail"])
        print(f"\nGate 1b failed: {len(violations)} violation(s) found.", file=sys.stderr)
        return 1

    print("Gate 1b passed: all project workloads have matching app.kubernetes.io/version labels.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
