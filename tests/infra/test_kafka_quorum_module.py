import importlib.util
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
FIXTURES = REPO_ROOT / "tests" / "fixtures" / "kafka-quorum" / "4.3.2"
_spec = importlib.util.spec_from_file_location("kafka_quorum", REPO_ROOT / "infra" / "oci" / "ansible" / "library" / "kafka_quorum.py")
mod = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(mod)


def fixture(name: str) -> str:
    return (FIXTURES / name).read_text(encoding="utf-8")


class QuorumParsing(unittest.TestCase):
    def test_status_voters_observers_and_leader(self):
        s = mod.parse_status(fixture("describe-status.txt"))
        self.assertEqual(s["leader"], 1)
        self.assertEqual([v["id"] for v in s["voters"]], [1, 2, 4])
        self.assertEqual(s["voters"][2]["directory_id"], "YkxbiitTRYiC1W3aHMCsLg")
        self.assertEqual([o["id"] for o in s["observers"]], [3])
        self.assertEqual(s["cluster_id"], "IzXtKbazRM6ZRcObBw5qXA")

    def test_replication_rows(self):
        rows = mod.parse_replication(fixture("describe-replication.txt"))
        self.assertEqual([r["node_id"] for r in rows], [1, 2, 4, 3])
        self.assertEqual({r["node_id"]: r["status"] for r in rows}, {1: "Leader", 2: "Follower", 4: "Follower", 3: "Observer"})
        self.assertEqual(next(r for r in rows if r["node_id"] == 4)["lag"], 1)

    def test_after_a_controller_swap_stale_observer_rows_remain(self):
        status = mod.parse_status(fixture("describe-status.after-add.txt"))
        self.assertEqual(sorted(v["id"] for v in status["voters"]), [1, 2, 4])
        rows = mod.parse_replication(fixture("describe-replication.after-add.txt"))
        node4 = [r for r in rows if r["node_id"] == 4]
        self.assertGreater(len(node4), 1)
        voters4 = [r for r in node4 if r["status"] in ("Leader", "Follower")]
        self.assertEqual(len(voters4), 1)
        self.assertEqual(voters4[0]["lag"], 0)
        self.assertEqual(voters4[0]["directory_id"], next(v["directory_id"] for v in status["voters"] if v["id"] == 4))

    def test_every_captured_status_file_parses(self):
        for path in sorted(FIXTURES.glob("*status*.txt")):
            with self.subTest(file=path.name):
                s = mod.parse_status(path.read_text(encoding="utf-8"))
                self.assertGreaterEqual(len(s["voters"]), 2)
                self.assertGreater(s["leader"], 0)


if __name__ == "__main__":
    unittest.main()
