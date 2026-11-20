import importlib.util
import unittest
from pathlib import Path

import yaml

REPO_ROOT = Path(__file__).resolve().parents[2]
FIXTURES = REPO_ROOT / "tests" / "fixtures" / "kafka-quorum" / "4.3.2"
_spec = importlib.util.spec_from_file_location("kafka_declared", REPO_ROOT / "infra" / "oci" / "ansible" / "library" / "kafka_declared.py")
mod = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(mod)
DECL = yaml.safe_load((REPO_ROOT / "infra" / "kafka" / "cluster.yaml").read_text(encoding="utf-8"))


def fixture(name: str) -> str:
    return (FIXTURES / name).read_text(encoding="utf-8")


class ParserTests(unittest.TestCase):
    def test_acl_list_matches_the_declarations(self):
        self.assertEqual(mod.parse_acls(fixture("acls-list-all.txt")), mod.desired_acls(DECL["identities"]))

    def test_dynamic_configs(self):
        got = mod.parse_dynamic_configs(fixture("configs-describe-logs.proj.valid.txt"))
        topic = next(t for t in DECL["topics"] if t["name"] == "logs.proj.valid")
        self.assertEqual(got, mod.topic_configs(topic))

    def test_topic_shape(self):
        self.assertEqual(mod.parse_topic_describe(fixture("topics-describe-ops.platform.events.txt")), {"partitions": 1, "replication_factor": 3})

    def test_topic_list(self):
        self.assertIn("logs.proj.valid", mod.parse_topic_list(fixture("topics-list.txt")))


class PlanTests(unittest.TestCase):
    def state(self):
        configs = mod.parse_dynamic_configs(fixture("configs-describe-logs.proj.valid.txt"))
        topics = [t for t in DECL["topics"] if t["name"] == "logs.proj.valid"]
        return topics, {
            "topics": {"logs.proj.valid"},
            "shapes": {"logs.proj.valid": {"partitions": 3, "replication_factor": 3}},
            "configs": {"logs.proj.valid": configs},
            "acls": mod.parse_acls(fixture("acls-list-all.txt")),
        }

    def test_converged_state_plans_nothing(self):
        topics, state = self.state()
        self.assertEqual(mod.plan(state, topics, DECL["identities"]), ([], []))

    def test_drifted_config_and_stray_acl_are_corrected(self):
        topics, state = self.state()
        state["configs"]["logs.proj.valid"]["retention.ms"] = "1"
        state["configs"]["logs.proj.valid"]["cleanup.policy"] = "compact"
        state["acls"].add(("TOPIC", "logs.proj.valid", "LITERAL", "User:fluent-bit-gke", "READ", "ALLOW"))
        actions, errors = mod.plan(state, topics, DECL["identities"])
        self.assertEqual(errors, [])
        flat = [" ".join(a) for a in actions]
        self.assertIn("--add-config retention.ms=604800000", " | ".join(flat))
        self.assertIn("--delete-config cleanup.policy", " | ".join(flat))
        self.assertTrue(any(a.startswith("kafka-acls.sh --remove --force --allow-principal User:fluent-bit-gke --operation Read --topic logs.proj.valid") for a in flat), flat)

    def test_shrinking_partitions_or_changing_replication_is_refused(self):
        topics, state = self.state()
        state["shapes"]["logs.proj.valid"] = {"partitions": 4, "replication_factor": 2}
        _, errors = mod.plan(state, topics, DECL["identities"])
        self.assertEqual(len(errors), 2)

    def test_missing_topic_is_created_with_its_configs(self):
        topics, state = self.state()
        state["topics"] = set()
        actions, _ = mod.plan(state, topics, DECL["identities"])
        self.assertEqual(actions[0][:4], ["kafka-topics.sh", "--create", "--topic", "logs.proj.valid"])
        self.assertIn("min.insync.replicas=2", actions[0])


if __name__ == "__main__":
    unittest.main()
