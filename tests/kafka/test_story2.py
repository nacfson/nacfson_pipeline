import json
import os
import subprocess
import threading
import time
import unittest

from tests.kafka import harness
from tests.kafka.test_story1 import assert_exactly_once, ship

OUTAGE = int(os.environ.get("KAFKA_TEST_OUTAGE_SECONDS", "600"))
ACK_GAP_LIMIT = 30
RECOVERY_LIMIT = 15 * 60
PRODUCER_NODE = 4
BROKERS = (1, 2, 3)


class StreamProducer:
    def __init__(self, topic: str, rate: int = 20):
        t = harness.target("rehearsal")
        self.topic = topic
        self.successes: list[tuple[int, int]] = []
        self.errors: list[dict] = []
        settings = "enable.idempotence=true\nretries=2147483647\ndelivery.timeout.ms=120000\nmax.in.flight.requests.per.connection=5\n"
        subprocess.run(["podman", "exec", "-i", harness.container(PRODUCER_NODE), "sh", "-c",
                        f"cat {harness.CLIENTS_IN_CONTAINER}/broker-1.properties - > /tmp/stream-producer.properties"],
                       input=settings, text=True, check=True)
        self.proc = subprocess.Popen(
            [*harness.kafka_exec(harness.container(PRODUCER_NODE)), f"{harness.KAFKA_BIN}/kafka-verifiable-producer.sh",
             "--bootstrap-server", t.bootstrap, "--command-config", "/tmp/stream-producer.properties",
             "--topic", topic, "--throughput", str(rate), "--acks", "-1"],
            stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True)
        self.reader = threading.Thread(target=self._read, daemon=True)
        self.reader.start()

    def _read(self):
        for line in self.proc.stdout:
            if not line.startswith("{"):
                continue
            event = json.loads(line)
            if event["name"] == "producer_send_success":
                self.successes.append((event["timestamp"], int(event["value"])))
            elif event["name"] == "producer_send_error":
                self.errors.append(event)

    def stop(self):
        subprocess.run(["podman", "exec", harness.container(PRODUCER_NODE), "pkill", "-f", "VerifiableProducer"], capture_output=True, check=False)
        self.proc.wait(timeout=60)
        self.reader.join(timeout=30)

    def max_ack_gap(self, start_ms: int, end_ms: int) -> float:
        times = sorted(ts for ts, _ in self.successes if start_ms <= ts <= end_ms)
        if len(times) < 2:
            return float("inf")
        return max(b - a for a, b in zip([start_ms] + times, times + [end_ms])) / 1000


def create_topic(name: str) -> None:
    harness.target("rehearsal").admin("kafka-topics.sh", "--create", "--if-not-exists", "--topic", name,
                                      "--partitions", "3", "--replication-factor", "3", "--config", "min.insync.replicas=2")


def delete_topic(name: str) -> None:
    harness.target("rehearsal").admin("kafka-topics.sh", "--delete", "--topic", name, check=False)


def stored_values(topic: str, expected: int) -> set[int]:
    t = harness.target("rehearsal")
    out = subprocess.run(
        [*harness.kafka_exec(harness.running_node_container()), f"{harness.KAFKA_BIN}/kafka-console-consumer.sh",
         "--bootstrap-server", t.bootstrap, "--command-config", f"{harness.CLIENTS_IN_CONTAINER}/broker-1.properties",
         "--topic", topic, "--from-beginning", "--max-messages", str(expected), "--timeout-ms", "30000"],
        capture_output=True, text=True, check=False, timeout=180)
    return {int(line) for line in out.stdout.splitlines() if line.strip().isdigit()}


def now_ms() -> int:
    return int(time.time() * 1000)


class OneBrokerDown(unittest.TestCase):
    def test_each_broker_down_in_turn(self):
        t = harness.target("rehearsal")
        for node in BROKERS:
            with self.subTest(broker=node):
                topic = f"rehearsal.s2-{node}-{int(time.time())}"
                create_topic(topic)
                producer = StreamProducer(topic)
                try:
                    time.sleep(10)
                    down_at = now_ms()
                    harness.stop_node(node)
                    time.sleep(OUTAGE)
                    harness.start_node(node)
                    up_at = now_ms()
                    restarted = time.monotonic()
                    harness.wait_brokers(t, timeout=180)
                    harness.wait_fully_replicated(t, timeout=RECOVERY_LIMIT)
                    recovered_after = time.monotonic() - restarted
                    time.sleep(5)
                finally:
                    producer.stop()
                acked = {v for _, v in producer.successes}
                stored = stored_values(topic, len(acked) + len(producer.errors) + 1000)
                self.assertGreater(len(acked), OUTAGE * 10)
                self.assertLessEqual(producer.max_ack_gap(down_at, up_at), ACK_GAP_LIMIT)
                self.assertEqual(acked - stored, set(), "acknowledged records missing")
                self.assertLessEqual(recovered_after, RECOVERY_LIMIT)
                delete_topic(topic)


class TwoBrokersDown(unittest.TestCase):
    def test_writes_refused_then_fluent_bit_delivers_everything(self):
        t = harness.target("rehearsal")
        topic = f"rehearsal.s2-two-{int(time.time())}"
        create_topic(topic)
        producer = StreamProducer(topic, rate=5)
        try:
            time.sleep(5)
            harness.stop_node(2)
            harness.stop_node(3)
            time.sleep(60)
        finally:
            producer.stop()
        reasons = " ".join(json.dumps(e) for e in producer.errors)
        self.assertTrue(producer.errors, "writes must be refused with two brokers down")
        self.assertRegex(reasons, "NotEnoughReplicas|TimeoutException")
        harness.start_node(2)
        harness.start_node(3)
        harness.wait_brokers(t)
        harness.wait_fully_replicated(t)
        delete_topic(topic)

        def two_down(before):
            harness.stop_node(2)
            harness.stop_node(3)
            time.sleep(90)
            harness.start_node(3)

        try:
            pods, records = ship("tb", 20_000, disrupt=two_down)
        finally:
            harness.start_node(2)
            harness.wait_brokers(t)
            harness.wait_fully_replicated(t)
        assert_exactly_once(self, pods, records, 20_000)


class RollingRestart(unittest.TestCase):
    def test_restarting_one_broker_at_a_time_never_stops_ingestion(self):
        t = harness.target("rehearsal")
        topic = f"rehearsal.s2-roll-{int(time.time())}"
        create_topic(topic)
        producer = StreamProducer(topic)
        start = now_ms()
        try:
            for node in BROKERS:
                harness.stop_node(node)
                harness.start_node(node)
                harness.wait_brokers(t)
                harness.wait_fully_replicated(t)
            end = now_ms()
            time.sleep(5)
        finally:
            producer.stop()
        acked = {v for _, v in producer.successes}
        self.assertEqual(producer.errors, [])
        self.assertLessEqual(producer.max_ack_gap(start, end), ACK_GAP_LIMIT)
        self.assertEqual(acked - stored_values(topic, len(acked) + 1000), set())
        delete_topic(topic)


if __name__ == "__main__":
    unittest.main()
