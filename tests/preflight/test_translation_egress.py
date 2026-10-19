import copy
import importlib.util
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
PREFLIGHT = os.path.join(BASE, "scripts", "platform-preflight.py")


def _preflight():
    spec = importlib.util.spec_from_file_location("platform_preflight", PREFLIGHT)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


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


class TestTranslationEgressPreflight(unittest.TestCase):
    def test_real_policy_is_accepted(self):
        module = _preflight()
        docs = [_load(POLICY), _load(PORTABLE)]
        self.assertEqual(module.check_translation_egress(docs), [])

    def test_extra_host_is_rejected(self):
        module = _preflight()
        doc = copy.deepcopy(_load(POLICY))
        doc["spec"]["egress"][1]["toFQDNs"].append({"matchName": "evil.example"})
        violations = module.check_translation_egress([doc, _load(PORTABLE)])
        self.assertTrue(any("evil.example" in item["detail"] for item in violations))
        self.assertTrue(all(item["rule"] == 2 for item in violations))

    def test_world_cidr_is_rejected(self):
        module = _preflight()
        doc = copy.deepcopy(_load(POLICY))
        doc["spec"]["egress"].append(
            {"toCIDR": ["0.0.0.0/0"], "toPorts": [{"ports": [{"port": "443", "protocol": "TCP"}]}]}
        )
        violations = module.check_translation_egress([doc, _load(PORTABLE)])
        self.assertTrue(any("0.0.0.0/0" in item["detail"] for item in violations))

    def test_missing_policy_is_rejected_when_isolation_is_present(self):
        module = _preflight()
        violations = module.check_translation_egress([_load(PORTABLE)])
        self.assertTrue(any("missing" in item["detail"] for item in violations))

    def test_frontend_selector_is_rejected(self):
        module = _preflight()
        doc = copy.deepcopy(_load(POLICY))
        doc["spec"]["endpointSelector"]["matchLabels"] = {
            "app.kubernetes.io/name": "pn-frontend"
        }
        violations = module.check_translation_egress([doc, _load(PORTABLE)])
        self.assertTrue(any("pn-backend" in item["detail"] for item in violations))

    def test_udp_443_is_rejected(self):
        module = _preflight()
        doc = copy.deepcopy(_load(POLICY))
        doc["spec"]["egress"][1]["toPorts"] = [
            {"ports": [{"port": "443", "protocol": "UDP"}]}
        ]
        violations = module.check_translation_egress([doc, _load(PORTABLE)])
        self.assertTrue(any("TCP 443" in item["detail"] for item in violations))

    def test_public_cidr_other_than_world_literal_is_rejected(self):
        module = _preflight()
        doc = copy.deepcopy(_load(POLICY))
        doc["spec"]["egress"].append({"toCIDR": ["1.1.1.1/32"]})
        doc["spec"]["egress"].append({"toCIDR": ["0.0.0.0/1", "128.0.0.0/1"]})
        violations = module.check_translation_egress([doc, _load(PORTABLE)])
        self.assertTrue(violations)
        self.assertTrue(all(item["rule"] == 2 for item in violations))

    def test_to_entities_all_is_rejected(self):
        module = _preflight()
        doc = copy.deepcopy(_load(POLICY))
        doc["spec"]["egress"].append({"toEntities": ["all"]})
        violations = module.check_translation_egress([doc, _load(PORTABLE)])
        self.assertTrue(violations)

    def test_star_moved_onto_public_fqdn_is_rejected(self):
        module = _preflight()
        doc = copy.deepcopy(_load(POLICY))
        doc["spec"]["egress"][0]["toPorts"][0]["rules"]["dns"] = [
            {"matchName": "api.wiktapi.dev"},
            {"matchName": "generativelanguage.googleapis.com"},
        ]
        doc["spec"]["egress"][1]["toFQDNs"] = [{"matchPattern": "*"}]
        violations = module.check_translation_egress([doc, _load(PORTABLE)])
        self.assertTrue(violations)

    def test_dns_star_must_select_kube_dns(self):
        module = _preflight()
        doc = copy.deepcopy(_load(POLICY))
        doc["spec"]["egress"][0]["toEndpoints"][0]["matchLabels"]["k8s:k8s-app"] = "not-dns"
        violations = module.check_translation_egress([doc, _load(PORTABLE)])
        self.assertTrue(any("kube-dns" in item["detail"] for item in violations))

    def test_isolation_port_443_without_destination_is_rejected(self):
        module = _preflight()
        isolation = copy.deepcopy(_load(PORTABLE))
        isolation["spec"]["egress"].append({"ports": [{"protocol": "TCP", "port": 443}]})
        violations = module.check_translation_egress([_load(POLICY), isolation])
        self.assertTrue(violations)

    def test_second_cilium_policy_for_frontend_is_rejected(self):
        module = _preflight()
        extra = {
            "apiVersion": "cilium.io/v2",
            "kind": "CiliumNetworkPolicy",
            "metadata": {"name": "frontend-world", "namespace": "proj-pn"},
            "spec": {
                "endpointSelector": {"matchLabels": {"app.kubernetes.io/name": "pn-frontend"}},
                "egress": [{"toEntities": ["world"]}],
            },
        }
        violations = module.check_translation_egress([_load(POLICY), _load(PORTABLE), extra])
        self.assertTrue(violations)

    def test_ingress_from_all_is_rejected(self):
        module = _preflight()
        doc = copy.deepcopy(_load(POLICY))
        doc["spec"]["ingress"] = [{"fromEntities": ["all"]}]
        violations = module.check_translation_egress([doc, _load(PORTABLE)])
        self.assertTrue(violations)


class TestTranslationHandbook(unittest.TestCase):
    def test_cni_flow_allows_only_the_two_hosts(self):
        with open(HANDBOOK, encoding="utf-8") as fh:
            text = fh.read()
        self.assertIn("pn-backend only: api.wiktapi.dev:443 and generativelanguage.googleapis.com:443", text)
        self.assertNotIn("internet) -> eBPF DROP", text)

    def test_simulator_writes_the_flannel_drop_to_a_live_element(self):
        with open(HANDBOOK, encoding="utf-8") as fh:
            text = fh.read()
        self.assertNotIn("getElementById('cni-sim-src')", text)
        self.assertIn("cni-scenario-list').innerHTML", text)
        self.assertIn("cni-junior-explain').textContent", text)
        self.assertIn("kube-router ignores CiliumNetworkPolicy", text)
