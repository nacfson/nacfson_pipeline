import json
import shutil
import subprocess
import tempfile
import time
import unittest
from collections import defaultdict
from datetime import datetime, timezone
from pathlib import Path

from tests.kafka import harness

FLUENT_BIT_IMAGE = "docker.io/fluent/fluent-bit:4.2.8@sha256:6806769c10acf11b41d14083b3101a24433b4ca113a1ab7a6b1a1b7cad86a7e8"
FLUENT_BIT_CONTAINER = "kafka-rehearsal-fluent-bit"
NETWORK = "kafka-rehearsal_kafka"
GKE_CONF = harness.REPO_ROOT / "clusters" / "gke" / "observability" / "fluent-bit-kafka.conf"
BASE_CONF = harness.REPO_ROOT / "deploy" / "platform" / "observability" / "fluent-bit"
CLIENTS = harness.REHEARSAL / ".run" / "clients"
LINES_PER_POD = 50_000
INPUT_FIELDS = {"level", "msg", "service.name", "service.version", "seq"}
PLATFORM_FIELDS = {"k8s.namespace.name", "k8s.pod.name", "k8s.pod.uid", "k8s.container.name", "k8s.node.name", "log.iostream", "observed_time", "_time"}


def rehearsal_conf() -> str:
    text = GKE_CONF.read_text(encoding="utf-8")
    gke = ",".join(harness.declarations()["bootstrap"]["gke"])
    rehearsal = ",".join(harness.declarations()["bootstrap"]["rehearsal"])
    assert text.count(f"Brokers {gke}") == 2
    return text.replace(f"Brokers {gke}", f"Brokers {rehearsal}")


def cri_line(seq: int, pod: str) -> str:
    now = datetime.now(timezone.utc)
    stamp = now.strftime("%Y-%m-%dT%H:%M:%S.%f") + "000Z"
    body = {"time": now.strftime("%Y-%m-%dT%H:%M:%S.%fZ"), "level": "info", "msg": f"{pod} line {seq}",
            "service.name": "story1", "service.version": "1.0.0", "seq": seq}
    return f"{stamp} stdout F {json.dumps(body, separators=(',', ':'))}\n"


def end_offset_total(topic: str) -> int:
    t = harness.target("rehearsal")
    out = subprocess.run(
        [*harness.kafka_exec(harness.running_node_container()), f"{harness.KAFKA_BIN}/kafka-get-offsets.sh",
         "--bootstrap-server", t.bootstrap, "--command-config", f"{harness.CLIENTS_IN_CONTAINER}/broker-1.properties", "--topic", topic],
        capture_output=True, text=True, check=True)
    return sum(int(line.rsplit(":", 1)[1]) for line in out.stdout.splitlines() if line.startswith(topic + ":"))


def consume(topic: str, expected: int, timeout_ms: int = 30000) -> list[dict]:
    t = harness.target("rehearsal")
    out = subprocess.run(
        [*harness.kafka_exec(harness.running_node_container()), f"{harness.KAFKA_BIN}/kafka-console-consumer.sh",
         "--bootstrap-server", t.bootstrap, "--command-config", f"{harness.CLIENTS_IN_CONTAINER}/broker-1.properties",
         "--topic", topic, "--from-beginning", "--max-messages", str(expected), "--timeout-ms", str(timeout_ms),
         "--formatter-property", "print.partition=true", "--formatter-property", "print.offset=true"],
        capture_output=True, text=True, check=False, timeout=timeout_ms / 1000 + 120)
    records = []
    for line in out.stdout.splitlines():
        if not line.startswith("Partition:"):
            continue
        partition, offset, value = line.split("\t", 2)
        records.append({"partition": int(partition.split(":")[1]), "offset": int(offset.split(":")[1]), "value": json.loads(value)})
    return records


def leader_of(topic: str, partition: int) -> int:
    out = harness.target("rehearsal").admin("kafka-topics.sh", "--describe", "--topic", topic).stdout
    for line in out.splitlines():
        fields = dict(part.split(": ", 1) for part in line.strip().split("\t") if ": " in part)
        if fields.get("Partition") == str(partition):
            return int(fields["Leader"])
    raise AssertionError(f"no leader line for {topic}-{partition}:\n{out}")


def ship(name: str, lines_per_pod: int, disrupt=None) -> tuple[tuple[str, str], list[dict]]:
    run = str(int(time.time()))
    pods = (f"{name}a-{run}", f"{name}b-{run}")
    tmp = Path(tempfile.mkdtemp(prefix=f"{name}-"))
    try:
        conf = tmp / "conf"
        shutil.copytree(BASE_CONF, conf)
        (conf / "fluent-bit.conf").write_text(rehearsal_conf(), encoding="utf-8")
        tls = tmp / "tls"
        tls.mkdir()
        for src, dst in (("ca.crt", "ca.crt"), ("fluent-bit-gke.crt", "tls.crt"), ("fluent-bit-gke.key", "tls.key")):
            subprocess.run(["podman", "unshare", "cp", str(CLIENTS / src), str(tls / dst)], check=True)
        pods_dir = tmp / "pods"
        for pod in pods:
            path = pods_dir / f"proj-story1_{pod}_uid-{pod}" / "app"
            path.mkdir(parents=True)
            with open(path / "0.log", "w", encoding="utf-8") as fh:
                for seq in range(1, lines_per_pod + 1):
                    fh.write(cri_line(seq, pod))
        (tmp / "state").mkdir()
        subprocess.run(["podman", "rm", "-f", FLUENT_BIT_CONTAINER], capture_output=True, check=False)
        before = end_offset_total("logs.proj.valid")
        expected = len(pods) * lines_per_pod
        subprocess.run(
            ["podman", "run", "-d", "--name", FLUENT_BIT_CONTAINER, "--network", NETWORK, "-e", "NODE_NAME=rehearsal-node",
             "-v", f"{conf}:/fluent-bit/etc:ro,Z", "-v", f"{tls}:/etc/kafka-tls:ro,Z", "-v", f"{pods_dir}:/var/log/pods:ro,Z",
             "-v", f"{tmp / 'state'}:/state:Z", FLUENT_BIT_IMAGE],
            capture_output=True, text=True, check=True)
        if disrupt:
            disrupt(before)
        harness.wait_for(lambda: end_offset_total("logs.proj.valid") >= before + expected, 600, interval=2, what=f"{expected} new records")
        time.sleep(5)
        total = end_offset_total("logs.proj.valid")
        records = [r for r in consume("logs.proj.valid", total, timeout_ms=30000) if r["value"].get("k8s.pod.name") in pods]
        return pods, records
    finally:
        logs = subprocess.run(["podman", "logs", FLUENT_BIT_CONTAINER], capture_output=True, text=True, check=False)
        (harness.REHEARSAL / ".run" / f"{name}-fluent-bit.log").write_text(logs.stdout + logs.stderr, encoding="utf-8")
        subprocess.run(["podman", "rm", "-f", FLUENT_BIT_CONTAINER], capture_output=True, check=False)
        subprocess.run(["podman", "unshare", "rm", "-rf", str(tmp)], check=False)


def assert_exactly_once(case: unittest.TestCase, pods, records, lines_per_pod: int) -> None:
    seen = defaultdict(list)
    for r in records:
        seen[r["value"]["k8s.pod.name"]].append(r["value"]["seq"])
    for pod in pods:
        with case.subTest(pod=pod):
            case.assertEqual(sorted(seen[pod]), list(range(1, lines_per_pod + 1)))


class Story1(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.pods, cls.records = ship("s1", LINES_PER_POD)

    def test_every_line_exactly_once(self):
        assert_exactly_once(self, self.pods, self.records, LINES_PER_POD)

    def test_in_order_per_pod_within_each_partition(self):
        by_key = defaultdict(list)
        for r in sorted(self.records, key=lambda r: (r["partition"], r["offset"])):
            by_key[(r["value"]["k8s.pod.name"], r["partition"])].append(r["value"]["seq"])
        for key, seqs in by_key.items():
            with self.subTest(pod=key[0], partition=key[1]):
                self.assertEqual(seqs, sorted(seqs))

    def test_fields_match_input(self):
        for r in self.records[:1000]:
            self.assertEqual(set(r["value"]) - PLATFORM_FIELDS, INPUT_FIELDS)
            self.assertEqual(r["value"]["service.name"], "story1")
            self.assertEqual(r["value"]["k8s.namespace.name"], "proj-story1")
            self.assertEqual(r["value"]["msg"], f"{r['value']['k8s.pod.name']} line {r['value']['seq']}")

    def test_partitions_have_three_in_sync_replicas(self):
        t = harness.target("rehearsal")
        for topic in ("logs.proj.valid", "logs.proj.quarantine"):
            out = t.admin("kafka-topics.sh", "--describe", "--topic", topic).stdout
            rows = [line for line in out.splitlines() if "Partition:" in line]
            self.assertEqual(len(rows), 3, out)
            for row in rows:
                fields = dict(part.split(": ", 1) for part in row.strip().split("\t") if ": " in part)
                self.assertEqual(len(fields["Replicas"].split(",")), 3, row)
                self.assertEqual(len(fields["Isr"].split(",")), 3, row)



class Retries(unittest.TestCase):
    def test_paused_leader_forces_retries_without_duplicates(self):
        paused = []
        observed = {}

        def pause_leader(before):
            harness.wait_for(lambda: end_offset_total("logs.proj.valid") > before + 1000, 120, interval=1, what="first records")
            node = leader_of("logs.proj.valid", 0)
            subprocess.run(["podman", "pause", harness.container(node)], check=True, capture_output=True)
            paused.append(node)
            observed["sent_at_pause"] = end_offset_total("logs.proj.valid") - before
            time.sleep(45)
            observed["leader_during_pause"] = leader_of("logs.proj.valid", 0)
            subprocess.run(["podman", "unpause", harness.container(node)], check=True, capture_output=True)

        try:
            pods, records = ship("rt", 40_000, disrupt=pause_leader)
        finally:
            for node in paused:
                subprocess.run(["podman", "unpause", harness.container(node)], capture_output=True, check=False)
        self.assertEqual(len(paused), 1)
        self.assertLess(observed["sent_at_pause"], 80_000, "the pause must start before every record is stored")
        self.assertNotEqual(observed["leader_during_pause"], paused[0], "partition 0 must fail over away from the paused broker")
        assert_exactly_once(self, pods, records, 40_000)
        harness.wait_fully_replicated(harness.target("rehearsal"), timeout=300)


def produce(identity: str, topic: str, value: str) -> subprocess.CompletedProcess:
    t = harness.target("rehearsal")
    return subprocess.run(
        [*harness.kafka_exec(harness.running_node_container(), interactive=True), f"{harness.KAFKA_BIN}/kafka-console-producer.sh",
         "--bootstrap-server", t.bootstrap, "--command-config", f"{harness.CLIENTS_IN_CONTAINER}/{identity}.properties",
         "--topic", topic, "--producer-property", "acks=all", "--producer-property", "max.request.size=4194304",
         "--producer-property", "delivery.timeout.ms=20000", "--producer-property", "request.timeout.ms=10000"],
        input=value + "\n", capture_output=True, text=True, check=False, timeout=120)


class Refusals(unittest.TestCase):
    def test_undeclared_topic_is_refused_and_not_created(self):
        result = produce("fluent-bit-gke", "logs.proj.undeclared", '{"msg":"x"}')
        self.assertIn("TOPIC_AUTHORIZATION_FAILED", result.stdout + result.stderr)
        topics = harness.target("rehearsal").admin("kafka-topics.sh", "--list").stdout.split()
        self.assertNotIn("logs.proj.undeclared", topics)

    def test_undeclared_topic_is_not_created_even_for_a_super_user(self):
        produce("broker-1", "logs.proj.undeclared", '{"msg":"x"}')
        topics = harness.target("rehearsal").admin("kafka-topics.sh", "--list").stdout.split()
        self.assertNotIn("logs.proj.undeclared", topics)

    def test_record_above_max_message_bytes_is_refused(self):
        limit = next(t["max_message_bytes"] for t in harness.declarations()["topics"] if t["name"] == "logs.proj.valid")
        before = end_offset_total("logs.proj.valid")
        result = produce("fluent-bit-gke", "logs.proj.valid", "x" * (limit + 1024))
        self.assertIn("RecordTooLargeException", result.stdout + result.stderr)
        self.assertEqual(end_offset_total("logs.proj.valid"), before)


if __name__ == "__main__":
    unittest.main()
