import copy
import importlib.util
import os
import unittest

import yaml

BASE = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
PREFLIGHT = os.path.join(BASE, "scripts", "platform-preflight.py")
NOTIFY_PROXY_YAML = os.path.join(BASE, "deploy", "platform", "vault-proxies", "notify-proxy.yaml")
VAULT_NETPOL_YAML = os.path.join(BASE, "deploy", "platform", "vault", "network-policy.yaml")


def _preflight():
    spec = importlib.util.spec_from_file_location("platform_preflight", PREFLIGHT)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def _load_all(path):
    with open(path, encoding="utf-8") as fh:
        return [doc for doc in yaml.safe_load_all(fh) if doc is not None]


def _by_kind_name(docs, kind, name):
    for doc in docs:
        if doc.get("kind") == kind and (doc.get("metadata") or {}).get("name") == name:
            return doc
    raise AssertionError(f"{kind}/{name} not found")


def _container(docs):
    dep = _by_kind_name(docs, "Deployment", "vault-notify-proxy")
    return dep["spec"]["template"]["spec"]["containers"][0]


class TestPlaceholderImageDigest(unittest.TestCase):
    def setUp(self):
        self.module = _preflight()
        self.docs = copy.deepcopy(_load_all(NOTIFY_PROXY_YAML))

    def test_published_digest_passes(self):
        self.assertEqual(self.module.check_placeholder_image_digests(self.docs), [])

    def test_all_zero_digest_rejected(self):
        _container(self.docs)["image"] = "ghcr.io/nacfson/vault-notify-proxy@sha256:" + "0" * 64
        violations = self.module.check_placeholder_image_digests(self.docs)
        self.assertEqual(len(violations), 1)
        self.assertIn("placeholder digest", violations[0]["detail"])


class TestNotifyGatewayPreflightContract(unittest.TestCase):
    def setUp(self):
        self.module = _preflight()
        self.platform_docs = _load_all(NOTIFY_PROXY_YAML) + _load_all(VAULT_NETPOL_YAML)

    def _check(self, docs):
        return self.module.check_notify_gateway_contract(docs)

    def _details(self, docs):
        return " | ".join(item["detail"] for item in self._check(docs))

    def test_valid_notify_gateway_passes(self):
        self.assertEqual(self._check(self.platform_docs), [])

    def test_service_exposing_8081_rejected(self):
        # The admin listener must never be reachable through the Service.
        docs = copy.deepcopy(self.platform_docs)
        svc = _by_kind_name(docs, "Service", "notify-gateway")
        svc["spec"]["ports"].append(
            {"name": "admin", "port": 8081, "targetPort": 8081, "protocol": "TCP"}
        )
        self.assertIn("8080", self._details(docs))

    def test_missing_probe_rejected(self):
        docs = copy.deepcopy(self.platform_docs)
        c = _container(docs)
        del c["livenessProbe"]
        self.assertIn("/healthz", self._details(docs))

    def test_unrestricted_ingress_rejected(self):
        docs = copy.deepcopy(self.platform_docs)
        netpol = _by_kind_name(docs, "NetworkPolicy", "notify-proxy-policy")
        netpol["spec"]["ingress"] = [{"ports": [{"port": 8080, "protocol": "TCP"}]}]
        self.assertIn("notify-proxy-policy", self._details(docs))

    def test_egress_fqdn_other_than_discord_rejected(self):
        # A wildcard would let the proxy reach any Discord-adjacent host, and a
        # second matchName would widen the door beyond Discord itself.
        for broken in (
            [{"matchPattern": "*.discord.com"}],
            [{"matchName": "discord.com"}, {"matchName": "evil.example"}],
        ):
            docs = copy.deepcopy(self.platform_docs)
            cnp = _by_kind_name(docs, "CiliumNetworkPolicy", "notify-proxy-egress")
            for rule in cnp["spec"]["egress"]:
                if "toFQDNs" in rule:
                    rule["toFQDNs"] = broken
            self.assertIn("discord.com", self._details(docs), f"accepted {broken!r}")

    def test_latest_tag_rejected(self):
        docs = copy.deepcopy(self.platform_docs)
        _container(docs)["image"] = "ghcr.io/nacfson/vault-notify-proxy:latest"
        self.assertIn("digest", self._details(docs))


if __name__ == "__main__":
    unittest.main()
