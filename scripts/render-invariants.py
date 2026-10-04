#!/usr/bin/env python3
"""
CI Render Invariants Check (contracts/reconciliation-layers.md §5, contracts/manual-change-policy.md §5).
Verifies structural, security, annotation, and layer boundary invariants against rendered manifests and clusters/<env>/.
"""

import argparse
import os
import sys
import yaml

CLUSTER_SCOPED_KINDS = {
    "Namespace",
    "ClusterRole",
    "ClusterRoleBinding",
    "CustomResourceDefinition",
    "ValidatingAdmissionPolicy",
    "ValidatingAdmissionPolicyBinding"
}

ALLOWED_PROJECT_KINDS = {"Deployment", "Service", "ConfigMap", "IngressRoute"}


def load_yaml_docs(file_path):
    if not os.path.exists(file_path):
        return []
    with open(file_path, "r", encoding="utf-8") as f:
        return [doc for doc in yaml.safe_load_all(f) if doc is not None]


def main():
    parser = argparse.ArgumentParser(description="Render Invariants Check")
    parser.add_argument("--environment", required=True, help="Target environment")
    parser.add_argument("--rendered", required=True, help="Concatenated rendered YAML output")
    args = parser.parse_args()

    errors = []
    base_dir = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))
    env_dir = os.path.join(base_dir, "clusters", args.environment)

    if not os.path.isdir(env_dir):
        sys.stderr.write(f"Error: cluster directory does not exist: {env_dir}\n")
        sys.exit(2)

    # 1. Invariant 1: No Flux Kustomization in clusters/<env>/ has spec.postBuild
    # Invariant 2: No GitRepository has spec.secretRef, and every URL starts with https://
    for root, _, files in os.walk(env_dir):
        for file in files:
            if file.endswith((".yaml", ".yml")):
                fpath = os.path.join(root, file)
                for doc in load_yaml_docs(fpath):
                    kind = doc.get("kind", "")
                    spec = doc.get("spec", {})
                    name = doc.get("metadata", {}).get("name", file)
                    if kind == "Kustomization":
                        if "postBuild" in spec:
                            errors.append(f"Invariant 1 violation in {fpath}: Kustomization '{name}' declares forbidden spec.postBuild")
                    elif kind == "GitRepository":
                        if "secretRef" in spec:
                            errors.append(f"Invariant 2 violation in {fpath}: GitRepository '{name}' declares forbidden spec.secretRef")
                        url = spec.get("url", "")
                        if not url.startswith("https://"):
                            errors.append(f"Invariant 2 violation in {fpath}: GitRepository '{name}' URL '{url}' does not start with https://")

    # Invariant 6 (part 1): No project layer object is in layers.yaml
    layers_yaml_path = os.path.join(env_dir, "layers.yaml")
    if os.path.exists(layers_yaml_path):
        for doc in load_yaml_docs(layers_yaml_path):
            if doc.get("kind") == "Kustomization":
                k_name = doc.get("metadata", {}).get("name", "")
                k_ns = doc.get("metadata", {}).get("namespace", "")
                if k_ns != "flux-system" or k_name.startswith("proj-"):
                    errors.append(f"Invariant 6 violation: Project layer '{k_name}' forbidden in {layers_yaml_path}")

    # Inspect rendered docs
    rendered_docs = load_yaml_docs(args.rendered)

    # Invariant 3: Every rendered namespaced object has an explicit metadata.namespace
    for doc in rendered_docs:
        kind = doc.get("kind", "")
        name = doc.get("metadata", {}).get("name", "unnamed")
        if kind not in CLUSTER_SCOPED_KINDS:
            ns = doc.get("metadata", {}).get("namespace")
            if not ns:
                errors.append(f"Invariant 3 violation: Object {kind}/{name} has no explicit metadata.namespace")

    # Invariant 5: Required annotations in §3
    required_annotations = [
        ("Namespace", "identity", "kustomize.toolkit.fluxcd.io/prune", "disabled"),
        ("Namespace", "platform", "kustomize.toolkit.fluxcd.io/prune", "disabled"),
        ("Namespace", "governance", "kustomize.toolkit.fluxcd.io/prune", "disabled"),
        ("Namespace", "vault", "kustomize.toolkit.fluxcd.io/prune", "disabled"),
        ("Namespace", "proj-pn", "kustomize.toolkit.fluxcd.io/prune", "disabled"),
        ("PersistentVolumeClaim", "postgres-data-postgres-0", "kustomize.toolkit.fluxcd.io/prune", "disabled"),
        ("PersistentVolumeClaim", "postgres-backup-pvc", "kustomize.toolkit.fluxcd.io/prune", "disabled"),
        ("PersistentVolumeClaim", "openbao-backup-pvc", "kustomize.toolkit.fluxcd.io/prune", "disabled"),
        ("Job", "postgres-init-job", "kustomize.toolkit.fluxcd.io/force", "enabled"),
    ]

    found_annotations = set()
    for doc in rendered_docs:
        kind = doc.get("kind", "")
        name = doc.get("metadata", {}).get("name", "")
        ann = doc.get("metadata", {}).get("annotations", {}) or {}
        for req_kind, req_name, ann_key, ann_val in required_annotations:
            if kind == req_kind and name == req_name:
                if ann.get(ann_key) == ann_val:
                    found_annotations.add((req_kind, req_name, ann_key, ann_val))

    for req in required_annotations:
        if req not in found_annotations:
            errors.append(f"Invariant 5 violation: Missing required annotation {req[2]}: {req[3]} on {req[0]}/{req[1]}")

    # Invariant 6 (part 2): Project layer properties & restricted kinds
    proj_dir = os.path.join(env_dir, "projects")
    if os.path.isdir(proj_dir):
        for pfile in os.listdir(proj_dir):
            if pfile.endswith((".yaml", ".yml")) and pfile != "kustomization.yaml":
                for doc in load_yaml_docs(os.path.join(proj_dir, pfile)):
                    if doc.get("kind") == "Kustomization":
                        spec = doc.get("spec", {})
                        if not spec.get("serviceAccountName"):
                            errors.append(f"Invariant 6 violation in {pfile}: Project Kustomization missing serviceAccountName")
                        if "targetNamespace" in spec:
                            errors.append(f"Invariant 6 violation in {pfile}: Project Kustomization declares forbidden targetNamespace")

    for doc in rendered_docs:
        flux_ns = doc.get("metadata", {}).get("labels", {}).get("kustomize.toolkit.fluxcd.io/namespace")
        if flux_ns and flux_ns not in {"flux-system"}:
            # Project layer object
            kind = doc.get("kind", "")
            name = doc.get("metadata", {}).get("name", "")
            ns = doc.get("metadata", {}).get("namespace", "")
            if kind not in ALLOWED_PROJECT_KINDS:
                errors.append(f"Invariant 6 violation: Project layer rendered forbidden kind {kind} ({name})")
    # Invariant 8: Manual-change policy invariants (contracts/manual-change-policy.md §5)
    deploy_dir = os.path.join(base_dir, "deploy")
    deploy_namespaces = set()
    for root, _, files in os.walk(deploy_dir):
        for file in files:
            if file.endswith((".yaml", ".yml")):
                for doc in load_yaml_docs(os.path.join(root, file)):
                    if doc.get("kind") == "Namespace":
                        ns_name = doc.get("metadata", {}).get("name")
                        if ns_name:
                            deploy_namespaces.add(ns_name)

    expected_selector_namespaces = {"default", "flux-system"} | deploy_namespaces
    forbidden_selector_namespaces = {"kube-system", "kube-public", "kube-node-lease"}

    policies = {}
    bindings = {}
    cluster_role_bindings = {}

    for doc in rendered_docs:
        kind = doc.get("kind", "")
        name = doc.get("metadata", {}).get("name", "")
        if kind == "ValidatingAdmissionPolicy":
            policies[name] = doc
        elif kind == "ValidatingAdmissionPolicyBinding":
            bindings[name] = doc
        elif kind == "ClusterRoleBinding":
            cluster_role_bindings[name] = doc

    # Item 1: failurePolicy: Fail and validationActions: [Deny]
    for pol_name in ["gitops-managed-namespaces", "gitops-managed-cluster-kinds"]:
        if pol_name not in policies:
            errors.append(f"Invariant 8 violation: Missing ValidatingAdmissionPolicy '{pol_name}'")
        else:
            pol_doc = policies[pol_name]
            fp = pol_doc.get("spec", {}).get("failurePolicy")
            if fp != "Fail":
                errors.append(f"Invariant 8 violation: ValidatingAdmissionPolicy '{pol_name}' failurePolicy is '{fp}', expected 'Fail'")

    for bind_name in ["gitops-managed-namespaces", "gitops-managed-cluster-kinds"]:
        if bind_name not in bindings:
            errors.append(f"Invariant 8 violation: Missing ValidatingAdmissionPolicyBinding '{bind_name}'")
        else:
            bind_doc = bindings[bind_name]
            va = bind_doc.get("spec", {}).get("validationActions")
            if va != ["Deny"]:
                errors.append(f"Invariant 8 violation: ValidatingAdmissionPolicyBinding '{bind_name}' validationActions is '{va}', expected ['Deny']")

    # Item 2 & 3: namespaceSelector matches expected and contains no forbidden namespaces
    if "gitops-managed-namespaces" in bindings:
        bind_doc = bindings["gitops-managed-namespaces"]
        match_res = bind_doc.get("spec", {}).get("matchResources", {})
        ns_sel = match_res.get("namespaceSelector", {})
        expressions = ns_sel.get("matchExpressions", [])
        values = []
        for expr in expressions:
            if expr.get("key") == "kubernetes.io/metadata.name" and expr.get("operator") == "In":
                values = expr.get("values", [])
                break

        val_set = set(values)
        if val_set != expected_selector_namespaces:
            errors.append(
                f"Invariant 8 violation: gitops-managed-namespaces namespaceSelector values {sorted(val_set)} "
                f"do not match expected {{default, flux-system}} ∪ deploy/ namespaces {sorted(expected_selector_namespaces)}"
            )

        leaked_forbidden = val_set & forbidden_selector_namespaces
        if leaked_forbidden:
            errors.append(f"Invariant 8 violation: namespaceSelector contains forbidden namespaces: {sorted(leaked_forbidden)}")

    # Item 4: platform-break-glass binds exactly Group platform:break-glass to cluster-admin
    if "platform-break-glass" not in cluster_role_bindings:
        errors.append("Invariant 8 violation: Missing ClusterRoleBinding 'platform-break-glass'")
    else:
        crb = cluster_role_bindings["platform-break-glass"]
        ref_name = crb.get("roleRef", {}).get("name")
        if ref_name != "cluster-admin":
            errors.append(f"Invariant 8 violation: platform-break-glass roleRef is '{ref_name}', expected 'cluster-admin'")
        subjects = crb.get("subjects", [])
        if len(subjects) != 1:
            errors.append(f"Invariant 8 violation: platform-break-glass subjects count is {len(subjects)}, expected exactly 1")
        else:
            sub = subjects[0]
            if sub.get("kind") != "Group" or sub.get("name") != "platform:break-glass":
                errors.append(f"Invariant 8 violation: platform-break-glass subject is {sub}, expected Group 'platform:break-glass'")

    # Item 5: operator-readonly-view binds only ClusterRole view
    if "operator-readonly-view" not in cluster_role_bindings:
        errors.append("Invariant 8 violation: Missing ClusterRoleBinding 'operator-readonly-view'")
    else:
        crb = cluster_role_bindings["operator-readonly-view"]
        ref_name = crb.get("roleRef", {}).get("name")
        if ref_name != "view":
            errors.append(f"Invariant 8 violation: operator-readonly-view roleRef is '{ref_name}', expected 'view'")

    if errors:
        sys.stderr.write("Render Invariants Violations:\n")
        for err in errors:
            sys.stderr.write(f"  ✗ {err}\n")
        sys.exit(1)

    print(f"All render invariants passed successfully for environment: {args.environment}")
    sys.exit(0)


if __name__ == "__main__":
    main()
