import importlib.util
import subprocess
import time
import unittest

from tests.kafka import harness
from tests.kafka.test_story2 import StreamProducer, create_topic, delete_topic, stored_values

_spec = importlib.util.spec_from_file_location("kafka_quorum", harness.REPO_ROOT / "infra" / "oci" / "ansible" / "library" / "kafka_quorum.py")
quorum_mod = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(quorum_mod)
_spec = importlib.util.spec_from_file_location("kafka_declared", harness.REPO_ROOT / "infra" / "oci" / "ansible" / "library" / "kafka_declared.py")
declared_mod = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(declared_mod)

QUORUM_FIXTURES = harness.REPO_ROOT / "tests" / "fixtures" / "kafka-quorum" / "4.3.2"


def quorum(save_as: str = "") -> dict:
    t = harness.target("rehearsal")
    status = t.admin("kafka-metadata-quorum.sh", "describe", "--status").stdout
    replication = t.admin("kafka-metadata-quorum.sh", "describe", "--replication").stdout
    if save_as:
        (QUORUM_FIXTURES / f"describe-status.{save_as}.txt").write_text(status, encoding="utf-8")
        (QUORUM_FIXTURES / f"describe-replication.{save_as}.txt").write_text(replication, encoding="utf-8")
    q = quorum_mod.parse_status(status)
    q["replication"] = quorum_mod.parse_replication(replication)
    return q


def observer_caught_up(node_id: int, old_dir: str) -> bool:
    rows = quorum()["replication"]
    return any(r["node_id"] == node_id and r["directory_id"] != old_dir and r["status"] == "Observer" and r["lag"] == 0 for r in rows)


def three_voters_caught_up():
    q = quorum()
    voters_ok = sorted(v["id"] for v in q["voters"]) == [1, 2, 4]
    lag = max(r["lag"] for r in q["replication"] if r["status"] in ("Leader", "Follower"))
    return q if voters_ok and lag == 0 else None


def format_join(node_id: int) -> None:
    args = (harness.REHEARSAL / "nodes" / f"node-{node_id}.join.format").read_text(encoding="utf-8").split()
    harness.compose("run", "--rm", "--no-deps", "--entrypoint", f"{harness.KAFKA_BIN}/kafka-storage.sh", f"node-{node_id}", *args)


def recreate(node_id: int) -> None:
    harness.compose("up", "-d", "--no-deps", f"node-{node_id}")


class BrokerWipe(unittest.TestCase):
    def test_broker_rejoins_as_node_3_and_nothing_is_lost(self):
        t = harness.target("rehearsal")
        topic = f"rehearsal.s3-broker-{int(time.time())}"
        create_topic(topic)
        producer = StreamProducer(topic)
        try:
            time.sleep(10)
            harness.wipe_node(3)
            format_join(3)
            recreate(3)
            harness.wait_brokers(t)
            harness.wait_fully_replicated(t)
            time.sleep(5)
        finally:
            producer.stop()
        acked = {v for _, v in producer.successes}
        self.assertEqual(producer.errors, [])
        self.assertEqual(acked - stored_values(topic, len(acked) + 1000), set())
        meta = subprocess.run(["podman", "exec", harness.container(3), "cat", "/var/lib/kafka/data/meta.properties"],
                              capture_output=True, text=True, check=True).stdout
        self.assertIn(f"cluster.id={harness.declarations()['cluster']['id']}", meta)
        self.assertIn("node.id=3", meta)
        delete_topic(topic)


class ControllerWipe(unittest.TestCase):
    def test_controller_4_is_swapped_while_writes_continue(self):
        t = harness.target("rehearsal")
        topic = f"rehearsal.s3-ctrl-{int(time.time())}"
        create_topic(topic)
        before = quorum("before-replace")
        old_dir = next(v["directory_id"] for v in before["voters"] if v["id"] == 4)
        producer = StreamProducer(topic)
        try:
            time.sleep(10)
            harness.wipe_node(4)
            t.admin("kafka-metadata-quorum.sh", "remove-controller", "--controller-id", "4", "--controller-directory-id", old_dir)
            removed = quorum("after-remove")
            self.assertEqual(sorted(v["id"] for v in removed["voters"]), [1, 2])
            format_join(4)
            recreate(4)
            harness.wait_for(lambda: observer_caught_up(4, old_dir), 180, what="node 4 caught up as an observer")
            subprocess.run(
                [*harness.kafka_exec(harness.container(4)), "sh", "-c",
                 "cat /etc/kafka-node/server.properties > /tmp/add-controller.properties && "
                 "echo security.protocol=SSL >> /tmp/add-controller.properties && "
                 "/opt/kafka/bin/kafka-metadata-quorum.sh --bootstrap-controller node-1:9093,node-2:9093 "
                 "--command-config /tmp/add-controller.properties add-controller"],
                capture_output=True, text=True, check=True, timeout=120)
            after = harness.wait_for(three_voters_caught_up, 120, what="three voters with no lag")
            quorum("after-add")
            time.sleep(5)
        finally:
            producer.stop()
        new_dir = next(v["directory_id"] for v in after["voters"] if v["id"] == 4)
        self.assertNotEqual(new_dir, old_dir)
        self.assertEqual(producer.errors, [])
        acked = {v for _, v in producer.successes}
        self.assertEqual(acked - stored_values(topic, len(acked) + 1000), set())
        delete_topic(topic)


class EmptyRecreate(unittest.TestCase):
    def test_recreated_cluster_has_every_declared_topic_and_refuses_implicit_ones(self):
        root = str(harness.REPO_ROOT)
        subprocess.run(["./scripts/kafka-rehearsal.sh", "down"], cwd=root, capture_output=True, text=True, check=True)
        subprocess.run(["./scripts/kafka-rehearsal.sh", "up"], cwd=root, capture_output=True, text=True, check=True, timeout=900)
        doc = harness.declarations()

        def run(cmd):
            return harness.target("rehearsal").admin(cmd[0], *cmd[1:]).stdout

        self.assertEqual(declared_mod.converge(run, doc["topics"], doc["identities"], apply=False), {"changed": False, "actions": []})
        from tests.kafka.test_story1 import produce
        produce("broker-1", "logs.proj.implicit", '{"msg":"x"}')
        self.assertNotIn("logs.proj.implicit", run(["kafka-topics.sh", "--list"]).split())


if __name__ == "__main__":
    unittest.main()
