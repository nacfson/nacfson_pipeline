#!/usr/bin/env python3
"""Pure-Python Kustomize & Flux Kustomization renderer fallback.

Used automatically by `scripts/render-validate.sh` and offline unit tests when
the `kubectl` or `flux` CLI binaries are not installed in the local sandbox
environment. Supports the repository's Kustomize feature subset (`resources`,
`namespace`, `configMapGenerator`, strategic-merge / JSON-6902 / `$patch: delete`
`patches`, and Flux `spec.patches` + `spec.postBuild.substitute`).
"""

from __future__ import annotations

import argparse
import copy
import os
import re
import shutil
import subprocess
import sys
from pathlib import Path

import yaml

CLUSTER_SCOPED_KINDS = {
    "Namespace",
    "ClusterRole",
    "ClusterRoleBinding",
    "CustomResourceDefinition",
    "ValidatingAdmissionPolicy",
    "ValidatingAdmissionPolicyBinding",
    "ValidatingWebhookConfiguration",
    "MutatingWebhookConfiguration",
    "ClusterIssuer",
    "IngressClass",
}

STRATEGIC_LIST_KEYS: dict[str, str] = {
    "containers": "name",
    "initContainers": "name",
    "ephemeralContainers": "name",
    "volumes": "name",
    "volumeMounts": "mountPath",
    "env": "name",
}

SUBST_RE = re.compile(r"\$\{([A-Za-z_][A-Za-z0-9_]*)(?::-(.*?))?\}")


def has_kubectl() -> bool:
    return shutil.which("kubectl") is not None


def has_flux() -> bool:
    return shutil.which("flux") is not None


def _unescape_pointer(token: str) -> str:
    return token.replace("~1", "/").replace("~0", "~")


def _apply_json6902(doc: dict, ops: list[dict]) -> dict:
    out = copy.deepcopy(doc)
    for op_spec in ops:
        op = op_spec.get("op")
        raw_path = str(op_spec.get("path") or "")
        if not raw_path.startswith("/"):
            raise ValueError(f"Invalid JSON-6902 path: {raw_path!r}")
        parts = [_unescape_pointer(p) for p in raw_path.lstrip("/").split("/")]
        parent = out
        for part in parts[:-1]:
            if isinstance(parent, list):
                parent = parent[int(part)]
            else:
                if part not in parent or parent[part] is None:
                    parent[part] = {}
                parent = parent[part]
        last = parts[-1]
        if op == "add":
            val = copy.deepcopy(op_spec.get("value"))
            if isinstance(parent, list):
                if last == "-":
                    parent.append(val)
                else:
                    parent.insert(int(last), val)
            else:
                parent[last] = val
        elif op == "replace":
            val = copy.deepcopy(op_spec.get("value"))
            if isinstance(parent, list):
                parent[int(last)] = val
            else:
                parent[last] = val
        elif op == "remove":
            if isinstance(parent, list):
                parent.pop(int(last))
            else:
                parent.pop(last, None)
        else:
            raise ValueError(f"Unsupported JSON-6902 op: {op!r}")
    return out


def _merge_strategic_list(base_list: list, patch_list: list, merge_key: str) -> list:
    result = [copy.deepcopy(item) for item in base_list]
    for p_item in patch_list:
        if not isinstance(p_item, dict) or merge_key not in p_item:
            result.append(copy.deepcopy(p_item))
            continue
        key_val = p_item[merge_key]
        if p_item.get("$patch") == "delete":
            result = [
                existing
                for existing in result
                if not (isinstance(existing, dict) and existing.get(merge_key) == key_val)
            ]
            continue
        matched = False
        for idx, existing in enumerate(result):
            if isinstance(existing, dict) and existing.get(merge_key) == key_val:
                result[idx] = _strategic_merge(existing, p_item)
                matched = True
                break
        if not matched:
            result.append(copy.deepcopy(p_item))
    return result


def _strategic_merge(base: dict, patch: dict) -> dict:
    if patch.get("$patch") == "replace":
        cleaned = copy.deepcopy(patch)
        cleaned.pop("$patch", None)
        return cleaned
    out = copy.deepcopy(base)
    for k, v in patch.items():
        if k == "$patch":
            continue
        if v is None:
            out.pop(k, None)
        elif isinstance(v, dict) and isinstance(out.get(k), dict):
            out[k] = _strategic_merge(out[k], v)
        elif isinstance(v, list) and isinstance(out.get(k), list) and k in STRATEGIC_LIST_KEYS:
            out[k] = _merge_strategic_list(out[k], v, STRATEGIC_LIST_KEYS[k])
        else:
            out[k] = copy.deepcopy(v)
    return out


def _matches_target(doc: dict, target: dict) -> bool:
    if not target:
        return True
    if target.get("kind") and doc.get("kind") != target["kind"]:
        return False
    meta = doc.get("metadata") or {}
    if target.get("name") and meta.get("name") != target["name"]:
        return False
    if target.get("namespace"):
        doc_ns = meta.get("namespace") or ""
        if doc_ns and doc_ns != target["namespace"]:
            return False
    if target.get("group") or target.get("version"):
        api_version = str(doc.get("apiVersion") or "")
        if "/" in api_version:
            grp, ver = api_version.split("/", 1)
        else:
            grp, ver = "", api_version
        if target.get("group") and grp != target["group"]:
            return False
        if target.get("version") and ver != target["version"]:
            return False
    return True


def apply_patches(docs: list[dict], patches_spec: list[dict], base_dir: Path | None = None) -> list[dict]:
    current = [copy.deepcopy(d) for d in docs]
    for entry in patches_spec or []:
        if not isinstance(entry, dict):
            continue
        target = entry.get("target") or {}
        patch_raw = entry.get("patch")
        if patch_raw is None and entry.get("path") and base_dir is not None:
            patch_raw = (base_dir / entry["path"]).read_text(encoding="utf-8")
        if patch_raw is None:
            continue
        parsed = yaml.safe_load(patch_raw) if isinstance(patch_raw, str) else patch_raw
        if parsed is None:
            continue

        # If target is omitted on a strategic merge patch, infer kind/name/namespace from patch body
        effective_target = dict(target)
        if not effective_target and isinstance(parsed, dict):
            if parsed.get("kind"):
                effective_target["kind"] = parsed["kind"]
            p_meta = parsed.get("metadata") or {}
            if p_meta.get("name"):
                effective_target["name"] = p_meta["name"]
            if p_meta.get("namespace"):
                effective_target["namespace"] = p_meta["namespace"]

        next_docs: list[dict] = []
        for doc in current:
            if not _matches_target(doc, effective_target):
                next_docs.append(doc)
                continue
            if isinstance(parsed, dict) and parsed.get("$patch") == "delete":
                continue
            if isinstance(parsed, list):
                next_docs.append(_apply_json6902(doc, parsed))
            elif isinstance(parsed, dict):
                next_docs.append(_strategic_merge(doc, parsed))
            else:
                next_docs.append(doc)
        current = next_docs
    return current


def _apply_configmap_generators(
    docs: list[dict], generators: list[dict], kdir: Path, namespace: str | None
) -> list[dict]:
    out = [copy.deepcopy(d) for d in docs]
    for gen in generators or []:
        cm_name = gen["name"]
        behavior = gen.get("behavior", "create")
        cm_ns = gen.get("namespace") or namespace
        data: dict[str, str] = {}
        for fspec in gen.get("files") or []:
            if "=" in fspec:
                key, rel = fspec.split("=", 1)
            else:
                rel = fspec
                key = os.path.basename(rel)
            data[key] = (kdir / rel).read_text(encoding="utf-8")
        for lspec in gen.get("literals") or []:
            key, val = str(lspec).split("=", 1)
            data[key] = val

        if behavior in ("replace", "merge"):
            replaced = False
            for idx, existing in enumerate(out):
                if (
                    existing.get("kind") == "ConfigMap"
                    and (existing.get("metadata") or {}).get("name") == cm_name
                ):
                    updated = copy.deepcopy(existing)
                    if behavior == "replace":
                        updated["data"] = data
                    else:
                        merged = dict(updated.get("data") or {})
                        merged.update(data)
                        updated["data"] = merged
                    if cm_ns:
                        updated.setdefault("metadata", {})["namespace"] = cm_ns
                    out[idx] = updated
                    replaced = True
                    break
            if replaced:
                continue

        cm_doc: dict = {
            "apiVersion": "v1",
            "kind": "ConfigMap",
            "metadata": {"name": cm_name},
            "data": data,
        }
        if cm_ns:
            cm_doc["metadata"]["namespace"] = cm_ns
        out.append(cm_doc)
    return out


def render_kustomize_dir_docs(kdir: str | Path) -> list[dict]:
    kpath = Path(kdir)
    kust_file = kpath / "kustomization.yaml"
    if not kust_file.exists():
        kust_file = kpath / "kustomization.yml"
    if not kust_file.exists():
        raise FileNotFoundError(f"No kustomization.yaml in {kpath}")

    kust = yaml.safe_load(kust_file.read_text(encoding="utf-8")) or {}
    docs: list[dict] = []

    for res in kust.get("resources") or []:
        res_path = kpath / res
        if res_path.is_dir():
            docs.extend(render_kustomize_dir_docs(res_path))
        else:
            raw = res_path.read_text(encoding="utf-8")
            docs.extend([d for d in yaml.safe_load_all(raw) if isinstance(d, dict)])

    namespace = kust.get("namespace")
    if kust.get("configMapGenerator"):
        docs = _apply_configmap_generators(docs, kust["configMapGenerator"], kpath, namespace)

    if namespace:
        for doc in docs:
            if doc.get("kind") not in CLUSTER_SCOPED_KINDS:
                doc.setdefault("metadata", {})["namespace"] = namespace

    if kust.get("patches"):
        docs = apply_patches(docs, kust["patches"], base_dir=kpath)

    if namespace:
        for doc in docs:
            if doc.get("kind") not in CLUSTER_SCOPED_KINDS:
                doc.setdefault("metadata", {})["namespace"] = namespace

    return docs


def _substitute_vars(node, mapping: dict[str, str]):
    if isinstance(node, str):
        def _repl(match: re.Match) -> str:
            var_name = match.group(1)
            default_val = match.group(2)
            if var_name in mapping:
                return str(mapping[var_name])
            if default_val is not None:
                return default_val
            return match.group(0)

        return SUBST_RE.sub(_repl, node)
    if isinstance(node, list):
        return [_substitute_vars(item, mapping) for item in node]
    if isinstance(node, dict):
        return {k: _substitute_vars(v, mapping) for k, v in node.items()}
    return node


def dump_docs(docs: list[dict]) -> str:
    if not docs:
        return ""
    return yaml.safe_dump_all(docs, sort_keys=False)


def kustomize_build(kdir: str | Path) -> str:
    """Render a Kustomize directory using `kubectl kustomize` if available, else Python fallback."""
    if has_kubectl():
        res = subprocess.run(
            ["kubectl", "kustomize", str(kdir)],
            capture_output=True,
            text=True,
            check=True,
        )
        return res.stdout
    return dump_docs(render_kustomize_dir_docs(kdir))


def flux_build_kustomization(
    flux_doc: dict,
    kustomization_file: str | Path,
    repo_root: str | Path,
    env: str,
) -> str:
    """Render a Flux Kustomization using `flux build` if available, else Python fallback."""
    meta = flux_doc.get("metadata") or {}
    spec = flux_doc.get("spec") or {}
    name = meta["name"]
    ns = meta.get("namespace", "flux-system")
    rel_path = str(spec["path"]).replace("<env>", env)
    full_path = Path(repo_root) / rel_path.lstrip("./")

    if has_flux():
        cmd = [
            "flux",
            "build",
            "kustomization",
            name,
            "--namespace",
            ns,
            "--path",
            str(full_path),
            "--kustomization-file",
            str(kustomization_file),
            "--dry-run",
        ]
        res = subprocess.run(cmd, capture_output=True, text=True)
        if res.returncode != 0:
            raise RuntimeError(f"Error building layer {name}: {res.stderr}")
        return res.stdout

    docs = render_kustomize_dir_docs(full_path)
    if spec.get("patches"):
        docs = apply_patches(docs, spec["patches"], base_dir=Path(kustomization_file).parent)

    target_ns = spec.get("targetNamespace")
    if target_ns:
        for doc in docs:
            if doc.get("kind") not in CLUSTER_SCOPED_KINDS:
                doc.setdefault("metadata", {})["namespace"] = target_ns

    subst = (spec.get("postBuild") or {}).get("substitute") or {}
    if subst:
        docs = [_substitute_vars(doc, subst) for doc in docs]

    for doc in docs:
        d_meta = doc.setdefault("metadata", {})
        labels = d_meta.setdefault("labels", {})
        labels.setdefault("kustomize.toolkit.fluxcd.io/name", name)
        labels.setdefault("kustomize.toolkit.fluxcd.io/namespace", ns)

    return dump_docs(docs)


def main() -> int:
    parser = argparse.ArgumentParser(description="Kustomize / Flux render helper")
    sub = parser.add_subparsers(dest="cmd", required=True)

    p_kust = sub.add_parser("kustomize", help="Render a Kustomize directory")
    p_kust.add_argument("path", help="Directory containing kustomization.yaml")

    p_env = sub.add_parser("env", help="Render flux-system + layers.yaml + projects/ for an environment")
    p_env.add_argument("environment", help="Environment name (local-k3s, vps-k3s, gke)")
    p_env.add_argument("--root", default=".", help="Repository root")

    args = parser.parse_args()
    if args.cmd == "kustomize":
        sys.stdout.write(kustomize_build(args.path))
        return 0

    root = Path(args.root).resolve()
    env = args.environment
    chunks: list[str] = []

    chunks.append(kustomize_build(root / "clusters" / env / "flux-system"))

    layers_file = root / "clusters" / env / "layers.yaml"
    layer_docs = [
        d
        for d in yaml.safe_load_all(layers_file.read_text(encoding="utf-8"))
        if isinstance(d, dict)
    ]
    for doc in layer_docs:
        chunks.append(flux_build_kustomization(doc, layers_file, root, env))

    proj_dir = root / "clusters" / env / "projects"
    if proj_dir.is_dir():
        for pf in sorted(os.listdir(proj_dir)):
            if pf.endswith((".yaml", ".yml")) and pf != "kustomization.yaml":
                filepath = proj_dir / pf
                p_docs = [
                    d
                    for d in yaml.safe_load_all(filepath.read_text(encoding="utf-8"))
                    if isinstance(d, dict)
                ]
                for doc in p_docs:
                    chunks.append(flux_build_kustomization(doc, filepath, root, env))

    sys.stdout.write("\n---\n".join(c.strip() for c in chunks if c.strip()) + "\n")
    return 0


if __name__ == "__main__":
    sys.exit(main())
