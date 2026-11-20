import unittest
from pathlib import Path

import yaml

REPO_ROOT = Path(__file__).resolve().parents[2]
ANSIBLE_VARS = REPO_ROOT / "infra" / "oci" / "ansible" / "group_vars" / "all" / "kafka.generated.yml"
NODES_DIR = REPO_ROOT / "tests" / "kafka" / "rehearsal" / "nodes"
ADDRESS_KEYS = {"listeners", "advertised.listeners", "controller.quorum.bootstrap.servers"}


def rehearsal_properties(node_id: int) -> dict:
    lines = (NODES_DIR / f"node-{node_id}.properties").read_text(encoding="utf-8").splitlines()
    return dict(line.split("=", 1) for line in lines if line)


class RehearsalParity(unittest.TestCase):
    def setUp(self):
        self.ansible = yaml.safe_load(ANSIBLE_VARS.read_text(encoding="utf-8"))

    def test_broker_settings_match_except_addresses(self):
        for name, node in self.ansible["kafka_nodes"].items():
            with self.subTest(node=name):
                prod = {k: v for k, v in node["server_properties"].items() if k not in ADDRESS_KEYS}
                rehearsal = {k: v for k, v in rehearsal_properties(node["node_id"]).items() if k not in ADDRESS_KEYS}
                self.assertEqual(rehearsal, prod)

    def test_same_image_and_cluster_id(self):
        compose = yaml.safe_load((REPO_ROOT / "tests" / "kafka" / "rehearsal" / "compose.generated.yaml").read_text(encoding="utf-8"))
        for svc in compose["services"].values():
            self.assertEqual(svc["image"], self.ansible["kafka_image"])
        fmt = (NODES_DIR / "node-1.format").read_text(encoding="utf-8").split()
        self.assertEqual(fmt[fmt.index("--cluster-id") + 1], self.ansible["kafka_cluster_id"])

    def test_topics_and_acls_come_from_the_same_declarations(self):
        harness_src = (REPO_ROOT / "tests" / "kafka" / "harness.py").read_text(encoding="utf-8")
        self.assertIn("infra\" / \"kafka\" / \"cluster.yaml", harness_src)
        decl = yaml.safe_load((REPO_ROOT / "infra" / "kafka" / "cluster.yaml").read_text(encoding="utf-8"))
        self.assertEqual(self.ansible["kafka_topics"], decl["topics"])

    def test_rehearsal_fluent_bit_differs_only_in_brokers(self):
        from tests.kafka.test_story1 import rehearsal_conf
        gke = (REPO_ROOT / "clusters" / "gke" / "observability" / "fluent-bit-kafka.conf").read_text(encoding="utf-8").splitlines()
        rehearsal = rehearsal_conf().splitlines()
        self.assertEqual(len(gke), len(rehearsal))
        changed = [(a, b) for a, b in zip(gke, rehearsal) if a != b]
        self.assertEqual(len(changed), 2)
        for a, b in changed:
            self.assertTrue(a.strip().startswith("Brokers 10.200.0."), a)
            self.assertTrue(b.strip().startswith("Brokers broker-"), b)

if __name__ == "__main__":
    unittest.main()
