"""Manifest tests for deploy/platform/observability.

`render()` is the shared helper Task 3 tests import.
"""

from __future__ import annotations

import os
import subprocess
import unittest

import yaml

BASE_DIR = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
OBSERVABILITY_DIR = os.path.join(BASE_DIR, "deploy", "platform", "observability")


def render() -> list[dict]:
    """Run `kubectl kustomize deploy/platform/observability` and return documents."""
    result = subprocess.run(
        ["kubectl", "kustomize", OBSERVABILITY_DIR],
        check=True,
        capture_output=True,
        text=True,
    )
    docs = [doc for doc in yaml.safe_load_all(result.stdout) if doc]
    return docs


def _by_kind_name(docs: list[dict], kind: str, name: str) -> dict:
    matches = [
        doc
        for doc in docs
        if doc.get("kind") == kind and doc.get("metadata", {}).get("name") == name
    ]
    if len(matches) != 1:
        raise AssertionError(f"expected one {kind}/{name}, found {len(matches)}")
    return matches[0]


class TestVictoriaLogsStore(unittest.TestCase):
    def test_namespace_is_privileged(self):
        docs = render()
        ns = _by_kind_name(docs, "Namespace", "observability")
        meta = ns["metadata"]
        self.assertEqual(
            meta.get("annotations", {}).get("kustomize.toolkit.fluxcd.io/prune"),
            "disabled",
        )
        labels = meta["labels"]
        for key in (
            "pod-security.kubernetes.io/enforce",
            "pod-security.kubernetes.io/warn",
            "pod-security.kubernetes.io/audit",
        ):
            self.assertEqual(labels[key], "privileged")
        self.assertNotIn("restricted", labels.values())
        self.assertNotIn("baseline", labels.values())

    def test_retention_flags_and_disk_cap_below_pvc(self):
        docs = render()
        sts = _by_kind_name(docs, "StatefulSet", "victorialogs")
        args = sts["spec"]["template"]["spec"]["containers"][0]["args"]
        self.assertIn("-retentionPeriod=30d", args)
        self.assertIn("-retention.maxDiskSpaceUsageBytes=8589934592", args)
        storage = sts["spec"]["volumeClaimTemplates"][0]["spec"]["resources"]["requests"][
            "storage"
        ]
        self.assertEqual(storage, "10Gi")
        self.assertEqual(8589934592, 8 * 1024**3)
        self.assertLess(8589934592, 10 * 1024**3)

    def test_victorialogs_hardening_and_resources(self):
        docs = render()
        sts = _by_kind_name(docs, "StatefulSet", "victorialogs")
        pod = sts["spec"]["template"]["spec"]
        self.assertIs(pod["automountServiceAccountToken"], False)
        container = pod["containers"][0]
        sec = container["securityContext"]
        self.assertIs(sec["allowPrivilegeEscalation"], False)
        self.assertEqual(sec["capabilities"]["drop"], ["ALL"])
        self.assertIs(pod["securityContext"]["runAsNonRoot"], True)
        resources = container["resources"]
        self.assertEqual(resources["requests"]["cpu"], "100m")
        self.assertEqual(resources["requests"]["memory"], "384Mi")
        self.assertEqual(resources["limits"]["cpu"], "500m")
        self.assertEqual(resources["limits"]["memory"], "384Mi")

    def test_service_clusterip_9428_and_nothing_public(self):
        docs = render()
        svc = _by_kind_name(docs, "Service", "victorialogs")
        self.assertEqual(svc["spec"].get("type", "ClusterIP"), "ClusterIP")
        ports = svc["spec"]["ports"]
        self.assertTrue(any(port.get("port") == 9428 for port in ports))
        kinds = {doc.get("kind") for doc in docs}
        self.assertNotIn("Secret", kinds)
        self.assertNotIn("IngressRoute", kinds)
        self.assertNotIn("Ingress", kinds)
        for doc in docs:
            if doc.get("kind") == "Service":
                self.assertEqual(doc["spec"].get("type", "ClusterIP"), "ClusterIP")
                self.assertNotEqual(doc["spec"].get("clusterIP"), "None")

    def test_network_policies(self):
        docs = render()
        policies = [doc for doc in docs if doc.get("kind") == "NetworkPolicy"]
        rendered = yaml.dump(policies)
        self.assertNotIn("0.0.0.0/0", rendered)

        default_deny = [
            policy
            for policy in policies
            if policy["spec"].get("podSelector") == {}
            and policy["spec"].get("policyTypes") == ["Ingress", "Egress"]
            and policy["spec"].get("ingress") == []
        ]
        self.assertEqual(len(default_deny), 1)
        egress = default_deny[0]["spec"]["egress"]
        self.assertEqual(len(egress), 1)
        rule = egress[0]
        self.assertEqual(
            rule["to"],
            [
                {
                    "namespaceSelector": {
                        "matchLabels": {"kubernetes.io/metadata.name": "kube-system"}
                    },
                    "podSelector": {"matchLabels": {"k8s-app": "kube-dns"}},
                }
            ],
        )
        self.assertEqual(
            rule["ports"],
            [
                {"protocol": "UDP", "port": 53},
                {"protocol": "TCP", "port": 53},
            ],
        )

        selected = [
            policy
            for policy in policies
            if policy["spec"].get("podSelector", {}).get("matchLabels", {}).get(
                "app.kubernetes.io/name"
            )
            == "victorialogs"
        ]
        self.assertEqual(len(selected), 1)
        ingress = selected[0]["spec"]["ingress"]
        self.assertEqual(len(ingress), 1)
        sources = ingress[0]["from"]
        self.assertEqual(len(sources), 1)
        self.assertEqual(
            sources[0]["podSelector"]["matchLabels"],
            {"app.kubernetes.io/name": "fluent-bit"},
        )
        self.assertEqual(ingress[0]["ports"], [{"protocol": "TCP", "port": 9428}])

    def test_image_is_pinned(self):
        docs = render()
        sts = _by_kind_name(docs, "StatefulSet", "victorialogs")
        image = sts["spec"]["template"]["spec"]["containers"][0]["image"]
        tag = image.rsplit(":", 1)[-1]
        self.assertNotEqual(tag, "latest")
        self.assertRegex(tag, r"\d")


if __name__ == "__main__":
    unittest.main()
