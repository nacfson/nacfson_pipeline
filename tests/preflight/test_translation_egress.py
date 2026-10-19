import os
import unittest

import yaml

BASE = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
POLICY = os.path.join(BASE, "deploy", "projects", "pn", "boundary", "translation-egress.yaml")
PORTABLE = os.path.join(BASE, "deploy", "projects", "pn", "boundary", "network-policy.yaml")
KUSTOMIZATION = os.path.join(BASE, "deploy", "projects", "pn", "boundary", "kustomization.yaml")
BACKEND = os.path.join(BASE, "deploy", "projects", "pn", "workloads", "backend-deployment.yaml")
HANDBOOK = os.path.join(BASE, "docs", "platform-architecture.html")
HOSTS = frozenset({"api.wiktapi.dev", "generativelanguage.googleapis.com"})


def _load(path):
    with open(path, encoding="utf-8") as fh:
        return yaml.safe_load(fh)


def _walk_key(node, key, found):
    if isinstance(node, dict):
        for item_key, item_value in node.items():
            if item_key == key:
                found.append(item_value)
            else:
                _walk_key(item_value, key, found)
    elif isinstance(node, list):
        for item in node:
            _walk_key(item, key, found)


class TestPnTranslationEgress(unittest.TestCase):
    def test_policy_names_only_the_two_translation_hosts(self):
        doc = _load(POLICY)
        self.assertEqual(doc["apiVersion"], "cilium.io/v2")
        self.assertEqual(doc["kind"], "CiliumNetworkPolicy")
        self.assertEqual(doc["metadata"]["name"], "pn-backend-translation-egress")
        self.assertEqual(doc["metadata"]["namespace"], "proj-pn")
        self.assertEqual(
            doc["spec"]["endpointSelector"]["matchLabels"],
            {"app.kubernetes.io/name": "pn-backend"},
        )
        self.assertNotIn("ingress", doc["spec"])
        rules = doc["spec"]["egress"]
        self.assertEqual(len(rules), 2)
        dns = next(rule for rule in rules if "toEndpoints" in rule)
        self.assertEqual(
            dns["toEndpoints"],
            [
                {
                    "matchLabels": {
                        "k8s:io.kubernetes.pod.namespace": "kube-system",
                        "k8s:k8s-app": "kube-dns",
                    }
                }
            ],
        )
        self.assertEqual(
            dns["toPorts"][0]["ports"],
            [{"port": "53", "protocol": "ANY"}],
        )
        fqdn = next(rule for rule in rules if "toFQDNs" in rule)
        self.assertEqual({item["matchName"] for item in fqdn["toFQDNs"]}, HOSTS)
        self.assertEqual(
            fqdn["toPorts"],
            [{"ports": [{"port": "443", "protocol": "TCP"}]}],
        )
        patterns = []
        names = []
        _walk_key(doc, "matchPattern", patterns)
        _walk_key(doc, "matchName", names)
        self.assertEqual(patterns, ["*"])
        self.assertEqual(set(names), HOSTS)
        dumped = yaml.dump(doc)
        self.assertNotIn("0.0.0.0/0", dumped)
        self.assertNotIn("world", dumped)

    def test_portable_policy_has_no_public_egress(self):
        doc = _load(PORTABLE)
        self.assertEqual(doc["kind"], "NetworkPolicy")
        self.assertEqual(doc["metadata"]["name"], "project-pn-isolation")
        dumped = yaml.dump(doc)
        self.assertNotIn("443", dumped)
        self.assertNotIn("ipBlock", dumped)
        self.assertNotIn("toFQDNs", dumped)
        self.assertNotIn("0.0.0.0/0", dumped)

    def test_boundary_kustomization_includes_the_policy(self):
        doc = _load(KUSTOMIZATION)
        self.assertIn("translation-egress.yaml", doc["resources"])

    def test_project_boundaries_wait_for_cilium(self):
        for env in ("local-k3s", "vps-k3s"):
            path = os.path.join(BASE, "clusters", env, "layers.yaml")
            with open(path, encoding="utf-8") as fh:
                docs = [item for item in yaml.safe_load_all(fh) if item]
            layer = next(
                item
                for item in docs
                if item.get("kind") == "Kustomization"
                and item.get("metadata", {}).get("name") == "project-boundaries"
            )
            names = [item["name"] for item in layer["spec"]["dependsOn"]]
            self.assertIn("cni", names)
            self.assertIn("governance", names)

    def test_backend_does_not_receive_the_enrich_key(self):
        with open(BACKEND, encoding="utf-8") as fh:
            text = fh.read()
        self.assertNotIn("ENRICH_PRIMARY_API_KEY", text)
        self.assertNotIn("ENRICH_FALLBACK_BASE_URL", text)

    def test_handbook_drops_translation_on_flannel(self):
        with open(HANDBOOK, encoding="utf-8") as fh:
            text = fh.read()
        self.assertIn("api.wiktapi.dev", text)
        self.assertIn("generativelanguage.googleapis.com", text)
        self.assertIn("pn-translation", text)
        self.assertIn("currentCniScenario === 'pn-translation'", text)
