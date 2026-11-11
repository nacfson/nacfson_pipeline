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
PREFLIGHT = os.path.join(BASE, "scripts", "platform-preflight.py")
REQUIRED_EXCEPT_CIDRS = {
    "10.0.0.0/8",
    "172.16.0.0/12",
    "192.168.0.0/16",
    "169.254.169.254/32",
}


def _preflight():
    spec = importlib.util.spec_from_file_location("platform_preflight", PREFLIGHT)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def _load(path):
    with open(path, encoding="utf-8") as fh:
        return yaml.safe_load(fh)


class TestPnTranslationEgress(unittest.TestCase):
    def test_policy_is_portable_k8s_networkpolicy_blocking_private_and_metadata_cidrs(self):
        doc = _load(POLICY)
        self.assertEqual(doc["apiVersion"], "networking.k8s.io/v1")
        self.assertEqual(doc["kind"], "NetworkPolicy")
        self.assertEqual(doc["metadata"]["name"], "pn-backend-translation-egress")
        self.assertEqual(doc["metadata"]["namespace"], "proj-pn")
        self.assertEqual(
            doc["spec"]["podSelector"]["matchLabels"],
            {"app.kubernetes.io/name": "pn-backend"},
        )
        self.assertEqual(doc["spec"]["policyTypes"], ["Egress"])
        self.assertNotIn("ingress", doc["spec"])

        rules = doc["spec"]["egress"]
        self.assertEqual(len(rules), 2)

        dns_rule = next(r for r in rules if any("namespaceSelector" in p for p in r.get("to", [])))
        self.assertEqual(
            dns_rule["to"],
            [
                {
                    "namespaceSelector": {"matchLabels": {"kubernetes.io/metadata.name": "kube-system"}},
                    "podSelector": {"matchLabels": {"k8s-app": "kube-dns"}},
                }
            ],
        )
        self.assertEqual(
            dns_rule["ports"],
            [{"protocol": "UDP", "port": 53}, {"protocol": "TCP", "port": 53}],
        )

        https_rule = next(r for r in rules if any("ipBlock" in p for p in r.get("to", [])))
        self.assertEqual(https_rule["ports"], [{"protocol": "TCP", "port": 443}])
        ip_block = https_rule["to"][0]["ipBlock"]
        self.assertEqual(ip_block["cidr"], "0.0.0.0/0")
        self.assertTrue(REQUIRED_EXCEPT_CIDRS.issubset(set(ip_block.get("except") or [])))

    def test_isolation_policy_has_no_public_egress(self):
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

    def test_project_boundaries_wait_for_governance_across_all_environments(self):
        for env in ("local-k3s", "vps-k3s", "gke"):
            path = os.path.join(BASE, "clusters", env, "layers.yaml")
            if not os.path.exists(path):
                continue
            with open(path, encoding="utf-8") as fh:
                docs = [item for item in yaml.safe_load_all(fh) if item]
            layer = next(
                item
                for item in docs
                if item.get("kind") == "Kustomization"
                and item.get("metadata", {}).get("name") == "project-boundaries"
            )
            names = [item["name"] for item in layer["spec"]["dependsOn"]]
            self.assertIn("governance", names)

    def test_backend_does_not_receive_the_enrich_key(self):
        with open(BACKEND, encoding="utf-8") as fh:
            text = fh.read()
        self.assertNotIn("ENRICH_PRIMARY_API_KEY", text)
        self.assertNotIn("ENRICH_FALLBACK_BASE_URL", text)


class TestTranslationEgressPreflight(unittest.TestCase):
    def test_real_policy_is_accepted(self):
        module = _preflight()
        docs = [_load(POLICY), _load(PORTABLE)]
        self.assertEqual(module.check_translation_egress(docs), [])

    def test_missing_metadata_or_rfc1918_except_is_rejected(self):
        module = _preflight()
        for dropped in ("169.254.169.254/32", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"):
            doc = copy.deepcopy(_load(POLICY))
            https_rule = next(r for r in doc["spec"]["egress"] if any("ipBlock" in p for p in r.get("to", [])))
            https_rule["to"][0]["ipBlock"]["except"] = [
                c for c in https_rule["to"][0]["ipBlock"]["except"] if c != dropped
            ]
            violations = module.check_translation_egress([doc, _load(PORTABLE)])
            self.assertTrue(
                any(dropped in item["detail"] or "except" in item["detail"] for item in violations),
                f"expected rejection when dropping {dropped}, got {violations}",
            )
            self.assertTrue(all(item["rule"] == 2 for item in violations))

    def test_cilium_network_policy_is_rejected(self):
        module = _preflight()
        cnp = {
            "apiVersion": "cilium.io/v2",
            "kind": "CiliumNetworkPolicy",
            "metadata": {"name": "pn-backend-translation-egress", "namespace": "proj-pn"},
            "spec": {"endpointSelector": {"matchLabels": {"app.kubernetes.io/name": "pn-backend"}}},
        }
        violations = module.check_translation_egress([cnp, _load(PORTABLE)])
        self.assertTrue(violations)

    def test_missing_policy_is_rejected_when_isolation_is_present(self):
        module = _preflight()
        violations = module.check_translation_egress([_load(PORTABLE)])
        self.assertTrue(any("missing" in item["detail"] for item in violations))

    def test_frontend_or_empty_selector_is_rejected(self):
        module = _preflight()
        for bad_sel in ({}, {"app.kubernetes.io/name": "pn-frontend"}):
            doc = copy.deepcopy(_load(POLICY))
            doc["spec"]["podSelector"]["matchLabels"] = bad_sel
            violations = module.check_translation_egress([doc, _load(PORTABLE)])
            self.assertTrue(any("pn-backend" in item["detail"] for item in violations))

    def test_udp_443_or_port_80_is_rejected(self):
        module = _preflight()
        for bad_ports in (
            [{"protocol": "UDP", "port": 443}],
            [{"protocol": "TCP", "port": 443}, {"protocol": "TCP", "port": 80}],
        ):
            doc = copy.deepcopy(_load(POLICY))
            https_rule = next(r for r in doc["spec"]["egress"] if any("ipBlock" in p for p in r.get("to", [])))
            https_rule["ports"] = bad_ports
            violations = module.check_translation_egress([doc, _load(PORTABLE)])
            self.assertTrue(any("TCP" in item["detail"] and "443" in item["detail"] for item in violations))

    def test_isolation_port_443_without_destination_is_rejected(self):
        module = _preflight()
        isolation = copy.deepcopy(_load(PORTABLE))
        isolation["spec"]["egress"].append({"ports": [{"protocol": "TCP", "port": 443}]})
        violations = module.check_translation_egress([_load(POLICY), isolation])
        self.assertTrue(violations)

    def test_ingress_rules_on_translation_policy_rejected(self):
        module = _preflight()
        doc = copy.deepcopy(_load(POLICY))
        doc["spec"]["ingress"] = [{}]
        violations = module.check_translation_egress([doc, _load(PORTABLE)])
        self.assertTrue(violations)


class TestTranslationHandbook(unittest.TestCase):
    def test_handbook_documents_pn_translation_and_ai_proxy(self):
        with open(HANDBOOK, encoding="utf-8") as fh:
            text = fh.read()
        self.assertIn("api.wiktapi.dev", text)
        self.assertIn("generativelanguage.googleapis.com", text)
        self.assertIn("pn-translation", text)
        self.assertIn("vault/vault-ai-proxy", text)
