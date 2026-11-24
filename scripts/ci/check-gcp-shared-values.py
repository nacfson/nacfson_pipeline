#!/usr/bin/env python3

import argparse
import ipaddress
import json
import re
import sys
from pathlib import Path

import yaml

sys.path.insert(0, str(Path(__file__).resolve().parent))
import gke_edge  # noqa: E402  (shared edge rules, Spec 015)

SHARED = "infra/gcp/shared-values.json"
CAPACITY = "clusters/gke/capacity.yaml"
LAYERS = "clusters/gke/layers.yaml"
SERVICE_ACCOUNTS = "deploy/platform/vault/service-accounts.yaml"
CLOUDBUILD = "deploy/gcp/cloudbuild.yaml"
TRAEFIK_SERVICE = "deploy/platform/traefik/service.yaml"
REGISTRY_TF = "infra/gcp/gke/registry.tf"
INFRA_DIR = "infra/gcp"

CHECK9_EXEMPT = {"backend.tf"}


class SourceError(Exception):
    pass


class Checker:
    def __init__(self, root: Path):
        self.root = root
        self.failures: list[dict] = []


    def path(self, rel: str) -> Path:
        p = self.root / rel
        if not p.is_file():
            raise SourceError(f"missing source file: {rel}")
        return p

    def text(self, rel: str) -> str:
        return self.path(rel).read_text(encoding="utf-8")

    def json(self, rel: str):
        try:
            return json.loads(self.text(rel))
        except json.JSONDecodeError as exc:
            raise SourceError(f"unparseable JSON in {rel}: {exc}") from exc

    def yaml_docs(self, rel: str) -> list:
        try:
            return [d for d in yaml.safe_load_all(self.text(rel)) if d is not None]
        except yaml.YAMLError as exc:
            raise SourceError(f"unparseable YAML in {rel}: {exc}") from exc

    def fail(self, check_id: int, field: str, infra, repo, source: str):
        self.failures.append({"id": check_id, "field": field, "infra": infra, "repo": repo, "source": source})


    def run(self) -> None:
        s = self.json(SHARED)
        try:
            self.check_pools(s)
            self.check_kms(s)
            self.check_ksa(s)
            self.check_cloudbuild(s)
            self.check_cleanup(s)
            self.check_traefik(s)
            self.check_literals(s)
            self.check_cidrs(s)
            self.check_naming(s)
        except (KeyError, TypeError) as exc:
            raise SourceError(f"{SHARED} is missing a field: {exc}") from exc

    def check_pools(self, s) -> None:
        docs = self.yaml_docs(CAPACITY)
        repo_pools = {p["name"]: p for p in (docs[0].get("pools") or [])} if docs else {}
        infra_pools = {p["name"]: p for p in s["node_pools"]}
        if set(infra_pools) != set(repo_pools):
            self.fail(1, "node_pools[].name", sorted(infra_pools), sorted(repo_pools), CAPACITY)
        for name in sorted(set(infra_pools) & set(repo_pools)):
            i, r = infra_pools[name], repo_pools[name]
            if i["machine_type"] != r.get("machineType"):
                self.fail(1, f"node_pools[{name}].machine_type", i["machine_type"], r.get("machineType"), CAPACITY)
            if i["node_count"] != r.get("nodeCount"):
                self.fail(1, f"node_pools[{name}].node_count", i["node_count"], r.get("nodeCount"), CAPACITY)
            repo_spot = r.get("provisioning") == "spot"
            if bool(i["spot"]) != repo_spot:
                self.fail(2, f"node_pools[{name}].spot", i["spot"], r.get("provisioning"), CAPACITY)
            if sorted(i["taints"]) != sorted(r.get("taints") or []):
                self.fail(2, f"node_pools[{name}].taints", sorted(i["taints"]), sorted(r.get("taints") or []), CAPACITY)

    def check_kms(self, s) -> None:
        text = self.text(LAYERS)
        blocks = re.findall(r'seal\s+"gcpckms"\s*\{(.*?)\}', text, re.S)
        if not blocks:
            self.fail(3, "kms", "seal \"gcpckms\" block", None, LAYERS)
        expected = {
            "project": s["project_id"],
            "region": s["region"],
            "key_ring": s["kms"]["key_ring"],
            "crypto_key": s["kms"]["crypto_key"],
        }
        for block in blocks:
            found = dict(re.findall(r'(\w+)\s*=\s*"([^"]*)"', block))
            for key, want in expected.items():
                if found.get(key) != want:
                    field = key if key in ("project", "region") else f"kms.{key}"
                    self.fail(3, field, want, found.get(key), LAYERS)

        want_gsa = f'{s["kms"]["gsa_account_id"]}@{s["project_id"]}.iam.gserviceaccount.com'
        annotations = self.ksa_gsa_annotations(s["kms"]["ksa"])
        if not annotations:
            self.fail(4, "kms.gsa_account_id", want_gsa, None, LAYERS)
        for got in annotations:
            if got != want_gsa:
                self.fail(4, "kms.gsa_account_id", want_gsa, got, LAYERS)

    def ksa_gsa_annotations(self, ksa: str) -> list:
        namespace, _, name = ksa.partition("/")
        found = []
        for doc in self.yaml_docs(LAYERS):
            for patch in ((doc.get("spec") or {}).get("patches") or []):
                body = patch.get("patch") if isinstance(patch, dict) else None
                if not isinstance(body, str):
                    continue
                try:
                    patch_docs = [d for d in yaml.safe_load_all(body) if isinstance(d, dict)]
                except yaml.YAMLError as exc:
                    raise SourceError(f"unparseable patch in {LAYERS}: {exc}") from exc
                for pd in patch_docs:
                    meta = pd.get("metadata") or {}
                    if pd.get("kind") == "ServiceAccount" and meta.get("name") == name and meta.get("namespace") == namespace:
                        value = (meta.get("annotations") or {}).get("iam.gke.io/gcp-service-account")
                        if value is not None:
                            found.append(value)
        return found

    def check_ksa(self, s) -> None:
        namespace, _, name = s["kms"]["ksa"].partition("/")
        found = any(
            d.get("kind") == "ServiceAccount"
            and (d.get("metadata") or {}).get("name") == name
            and (d.get("metadata") or {}).get("namespace") == namespace
            for d in self.yaml_docs(SERVICE_ACCOUNTS)
        )
        if not found:
            self.fail(5, "kms.ksa", s["kms"]["ksa"], None, SERVICE_ACCOUNTS)

    def check_cloudbuild(self, s) -> None:
        docs = self.yaml_docs(CLOUDBUILD)
        subs = (docs[0].get("substitutions") or {}) if docs else {}
        if subs.get("_GAR_REGION") != s["region"]:
            self.fail(6, "region", s["region"], subs.get("_GAR_REGION"), CLOUDBUILD)
        if subs.get("_GAR_REPO") != s["registry"]["repository_id"]:
            self.fail(6, "registry.repository_id", s["registry"]["repository_id"], subs.get("_GAR_REPO"), CLOUDBUILD)

    def check_cleanup(self, s) -> None:
        source = s["registry"]["cleanup_source"]
        policies = self.json(source)
        registry_tf = strip_hcl_comments(self.text(REGISTRY_TF))
        if "local.shared.registry.cleanup_source" not in registry_tf:
            self.fail(7, "registry.cleanup_source", source, "not read by registry.tf", REGISTRY_TF)
        if not isinstance(policies, list) or not policies:
            self.fail(7, "registry.cleanup_source", "non-empty list", type(policies).__name__, source)
            return
        for i, p in enumerate(policies):
            pid = p.get("name") if isinstance(p, dict) else None
            label = f"cleanup[{pid or i}]"
            if not pid:
                self.fail(7, f"{label}.name", "non-empty id", pid, source)
                continue
            action = (p.get("action") or {}).get("type")
            if action not in ("Delete", "Keep"):
                self.fail(7, f"{label}.action", "Delete|Keep", action, source)
            older = (p.get("condition") or {}).get("olderThan")
            if older is not None and not re.fullmatch(r"\d+s", str(older)):
                self.fail(7, f"{label}.olderThan", "<seconds>s", older, source)
            keep = (p.get("mostRecentVersions") or {}).get("keepCount")
            if "mostRecentVersions" in p and not (isinstance(keep, int) and keep > 0):
                self.fail(7, f"{label}.keepCount", "positive integer", keep, source)

    def check_traefik(self, s) -> None:
        svc = next((d for d in self.yaml_docs(TRAEFIK_SERVICE) if gke_edge.is_edge_service(d)), None)
        if svc is None:
            self.fail(8, "ingress.address_name", s["ingress"]["address_name"], "no Service traefik", TRAEFIK_SERVICE)
            return
        got = ((svc.get("metadata") or {}).get("annotations") or {}).get(gke_edge.LB_ADDRESS_ANNOTATION)
        if got != s["ingress"]["address_name"]:
            self.fail(8, "ingress.address_name", s["ingress"]["address_name"], got, TRAEFIK_SERVICE)
        got_class = (svc.get("spec") or {}).get("loadBalancerClass")
        if got_class != gke_edge.LB_CLASS:
            self.fail(8, "loadBalancerClass", gke_edge.LB_CLASS, got_class, TRAEFIK_SERVICE)

    def check_literals(self, s) -> None:
        values = set(_string_leaves(s))
        infra = self.root / INFRA_DIR
        for tf in sorted(infra.rglob("*.tf")):
            if ".terraform" in tf.parts or tf.name in CHECK9_EXEMPT:
                continue
            rel = tf.relative_to(self.root).as_posix()
            for literal in _string_literals(strip_hcl_comments(tf.read_text(encoding="utf-8"))):
                if literal in values:
                    self.fail(9, "literal", f"local.shared reference", f'"{literal}"', rel)

    def check_cidrs(self, s) -> None:
        private = ipaddress.ip_network("10.0.0.0/8")
        nets = {}
        for key in ("nodes_cidr", "pods_cidr", "services_cidr"):
            raw = s["network"][key]
            try:
                net = ipaddress.ip_network(raw)
            except ValueError:
                self.fail(10, f"network.{key}", "valid CIDR", raw, SHARED)
                continue
            if not net.subnet_of(private):
                self.fail(10, f"network.{key}", "inside 10.0.0.0/8", raw, SHARED)
            nets[key] = net
        keys = sorted(nets)
        for i, a in enumerate(keys):
            for b in keys[i + 1:]:
                if nets[a].overlaps(nets[b]):
                    self.fail(10, f"network.{a}", f"no overlap with {b}", f"{nets[a]} overlaps {nets[b]}", SHARED)

    def check_naming(self, s) -> None:
        if not s["zone"].startswith(s["region"] + "-"):
            self.fail(11, "zone", f'{s["region"]}-<x>', s["zone"], SHARED)
        want_pool = f'{s["project_id"]}.svc.id.goog'
        if s["workload_pool"] != want_pool:
            self.fail(11, "workload_pool", want_pool, s["workload_pool"], SHARED)


def _string_leaves(value):
    if isinstance(value, str):
        yield value
    elif isinstance(value, dict):
        for v in value.values():
            yield from _string_leaves(v)
    elif isinstance(value, list):
        for v in value:
            yield from _string_leaves(v)


def strip_hcl_comments(text: str) -> str:
    out, i, in_str = [], 0, False
    while i < len(text):
        ch = text[i]
        if in_str:
            out.append(ch)
            if ch == "\\" and i + 1 < len(text):
                out.append(text[i + 1])
                i += 2
                continue
            if ch == '"':
                in_str = False
            i += 1
        elif ch == '"':
            in_str = True
            out.append(ch)
            i += 1
        elif ch == "#" or text.startswith("//", i):
            while i < len(text) and text[i] != "\n":
                i += 1
        elif text.startswith("/*", i):
            end = text.find("*/", i + 2)
            i = len(text) if end == -1 else end + 2
        else:
            out.append(ch)
            i += 1
    return "".join(out)


def _string_literals(text: str):
    return re.findall(r'"((?:[^"\\]|\\.)*)"', text)


def main() -> int:
    parser = argparse.ArgumentParser(description="Cross-check infra/gcp/shared-values.json against the repository.")
    parser.add_argument("--repo-root", default=str(Path(__file__).resolve().parents[2]))
    parser.add_argument("--json", action="store_true", help="print a JSON report")
    args = parser.parse_args()

    checker = Checker(Path(args.repo_root))
    try:
        checker.run()
    except SourceError as exc:
        if args.json:
            print(json.dumps({"ok": False, "error": str(exc), "failures": []}))
        else:
            print(f"ERROR {exc}", file=sys.stderr)
        return 2

    if args.json:
        print(json.dumps({"ok": not checker.failures, "failures": checker.failures}, indent=2))
    else:
        for f in checker.failures:
            print(f'CHECK {f["id"]} FAIL {f["field"]}: infra={f["infra"]} repo={f["repo"]} ({f["source"]})')
        if not checker.failures:
            print("check-gcp-shared-values: all 11 checks passed")
    return 1 if checker.failures else 0


if __name__ == "__main__":
    sys.exit(main())
