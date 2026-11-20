import filecmp
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

import yaml

REPO_ROOT = Path(__file__).resolve().parents[2]
SCRIPT = REPO_ROOT / "scripts" / "kafka-declarations.py"
DECLARATIONS = Path("infra/kafka/cluster.yaml")
FLUENT_BIT = Path("clusters/gke/observability/fluent-bit-kafka.conf")
SCHEMAS = Path("tests/fixtures/strimzi-schemas")
RENDER_OUTPUTS = {
    "ansible": [Path("infra/oci/ansible/group_vars/all/kafka.generated.yml")],
    "compose": [Path("tests/kafka/rehearsal/compose.generated.yaml"), Path("tests/kafka/rehearsal/nodes")],
    "strimzi": [Path("build/strimzi")],
}


def run(root: Path, *args: str) -> subprocess.CompletedProcess:
    return subprocess.run(
        [sys.executable, str(SCRIPT), *args, "--root", str(root)],
        capture_output=True,
        text=True,
        check=False,
    )


def topic(doc: dict, name: str) -> dict:
    return next(t for t in doc["topics"] if t["name"] == name)


def identity(doc: dict, name: str) -> dict:
    return next(i for i in doc["identities"] if i["name"] == name)


def node(doc: dict, name: str) -> dict:
    return next(n for n in doc["nodes"] if n["name"] == name)


class KafkaDeclarationsTests(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.root = Path(self._tmp.name)
        for rel in (DECLARATIONS, FLUENT_BIT):
            (self.root / rel).parent.mkdir(parents=True, exist_ok=True)
            shutil.copy(REPO_ROOT / rel, self.root / rel)
        shutil.copytree(REPO_ROOT / SCHEMAS, self.root / SCHEMAS)

    def tearDown(self):
        self._tmp.cleanup()

    def mutate_declarations(self, change):
        path = self.root / DECLARATIONS
        doc = yaml.safe_load(path.read_text(encoding="utf-8"))
        change(doc)
        path.write_text(yaml.safe_dump(doc, sort_keys=False), encoding="utf-8")

    def mutate_fluent_bit(self, old: str, new: str):
        path = self.root / FLUENT_BIT
        text = path.read_text(encoding="utf-8")
        self.assertIn(old, text)
        path.write_text(text.replace(old, new), encoding="utf-8")

    def assert_check_fails(self, field: str):
        result = run(self.root, "check")
        self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn(field, result.stderr)

    def test_committed_files_pass(self):
        result = run(REPO_ROOT, "check")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_rule1_broker_list_must_match_bootstrap(self):
        self.mutate_declarations(lambda d: d["bootstrap"].update(
            gke=["10.200.0.11:9092", "10.200.0.12:9092", "10.200.0.14:9092"]))
        self.assert_check_fails("bootstrap.gke")

    def test_rule2_fluent_bit_topic_must_be_declared(self):
        self.mutate_declarations(lambda d: topic(d, "logs.proj.quarantine").update(name="logs.proj.other"))
        self.assert_check_fails("logs.proj.quarantine")

    def test_rule3_logs_topics_keep_min_insync_two(self):
        self.mutate_declarations(lambda d: topic(d, "logs.proj.valid").update(min_insync_replicas=1))
        self.assert_check_fails("min_insync_replicas")

    def test_rule4_fluent_bit_must_compress_with_zstd(self):
        self.mutate_fluent_bit("rdkafka.compression.codec zstd", "rdkafka.compression.codec lz4")
        self.assert_check_fails("rdkafka.compression.codec")

    def test_rule5_producer_has_only_write_grants(self):
        self.mutate_declarations(lambda d: identity(d, "fluent-bit-gke")["grants"].append(
            {"resource": "topic", "name": "logs.proj.valid", "ops": ["Read"]}))
        self.assert_check_fails("fluent-bit-gke")

    def test_rule5_only_brokers_and_controllers_are_super_users(self):
        self.mutate_declarations(lambda d: identity(d, "ops-relay").update(super_user=True))
        self.assert_check_fails("ops-relay")

    def test_rule6_exactly_three_controllers(self):
        self.mutate_declarations(lambda d: node(d, "micro-1").update(roles=["broker", "controller"]))
        self.assert_check_fails("roles")

    def test_rule7_per_broker_budget(self):
        self.mutate_declarations(lambda d: topic(d, "logs.proj.quarantine").update(retention_bytes=4294967296))
        self.assert_check_fails("retention_bytes")

    def test_rule8_strimzi_render_must_validate(self):
        self.mutate_declarations(lambda d: topic(d, "ops.platform.metrics").update(partitions=0))
        self.assert_check_fails("partitions")

    def test_renders_are_byte_identical(self):
        for target, outputs in RENDER_OUTPUTS.items():
            with self.subTest(target=target):
                copies = []
                for _ in range(2):
                    result = run(self.root, "render", "--target", target)
                    self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                    snapshot = Path(tempfile.mkdtemp(dir=self.root))
                    for rel in outputs:
                        src = self.root / rel
                        dst = snapshot / rel
                        dst.parent.mkdir(parents=True, exist_ok=True)
                        if src.is_dir():
                            shutil.copytree(src, dst)
                            shutil.rmtree(src)
                        else:
                            shutil.copy(src, dst)
                            src.unlink()
                    copies.append(snapshot)
                cmp = filecmp.dircmp(copies[0], copies[1])
                self.assertEqual(differences(cmp), [])

    def test_committed_renders_are_current(self):
        for target in ("ansible", "compose"):
            with self.subTest(target=target):
                result = run(self.root, "render", "--target", target)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                for rel in RENDER_OUTPUTS[target]:
                    fresh, committed = self.root / rel, REPO_ROOT / rel
                    if fresh.is_dir():
                        self.assertEqual(differences(filecmp.dircmp(fresh, committed)), [], rel)
                    else:
                        self.assertEqual(fresh.read_bytes(), committed.read_bytes(), rel)


def differences(cmp: filecmp.dircmp) -> list[str]:
    found = cmp.left_only + cmp.right_only + cmp.funny_files
    found += [f for f in cmp.common_files if not filecmp.cmp(Path(cmp.left) / f, Path(cmp.right) / f, shallow=False)]
    for sub in cmp.subdirs.values():
        found += differences(sub)
    return found


if __name__ == "__main__":
    unittest.main()
