import importlib.util
import subprocess
import unittest

from tests.kafka import harness

_spec = importlib.util.spec_from_file_location("kafka_declared", harness.REPO_ROOT / "infra" / "oci" / "ansible" / "library" / "kafka_declared.py")
mod = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(mod)


def run(cmd: list[str]) -> str:
    return harness.target("rehearsal").admin(cmd[0], *cmd[1:]).stdout


class ConvergeOnRehearsal(unittest.TestCase):
    def setUp(self):
        self.doc = harness.declarations()

    def converge(self, apply: bool) -> dict:
        return mod.converge(run, self.doc["topics"], self.doc["identities"], apply=apply)

    def test_converged_cluster_reports_no_changes(self):
        self.converge(apply=True)
        self.assertEqual(self.converge(apply=False), {"changed": False, "actions": []})

    def test_drift_is_corrected_and_second_run_is_clean(self):
        run(["kafka-configs.sh", "--alter", "--entity-type", "topics", "--entity-name", "ops.platform.metrics", "--add-config", "retention.ms=1000,cleanup.policy=compact"])
        run(["kafka-acls.sh", "--add", "--allow-principal", "User:fluent-bit-gke", "--operation", "Read", "--topic", "logs.proj.valid"])
        first = self.converge(apply=True)
        self.assertTrue(first["changed"])
        self.assertEqual(len(first["actions"]), 3, first["actions"])
        self.assertEqual(self.converge(apply=False), {"changed": False, "actions": []})
        acls = mod.parse_acls(run(["kafka-acls.sh", "--list"]))
        self.assertNotIn(("TOPIC", "logs.proj.valid", "LITERAL", "User:fluent-bit-gke", "READ", "ALLOW"), acls)


if __name__ == "__main__":
    unittest.main()
