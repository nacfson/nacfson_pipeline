import subprocess
import tempfile
import time
import unittest
from pathlib import Path

from tests.kafka import harness
from tests.kafka.test_story1 import produce


def utc_since() -> str:
    return time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(time.time() - 2))


def broker_logs(since: str) -> str:
    return "".join(harness.logs_since(n, since) for n in (1, 2, 3))


class Security(unittest.TestCase):
    def test_client_without_a_ca_signed_certificate_is_refused(self):
        since = utc_since()
        node = harness.running_node_container()
        with tempfile.TemporaryDirectory() as tmp:
            tmp = Path(tmp)
            subprocess.run(["openssl", "req", "-x509", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:P-256", "-nodes", "-days", "1",
                            "-keyout", str(tmp / "k.pem"), "-out", str(tmp / "c.pem"), "-subj", "/CN=fluent-bit-gke"],
                           capture_output=True, check=True)
            (tmp / "rogue.pem").write_text((tmp / "k.pem").read_text() + (tmp / "c.pem").read_text())
            (tmp / "rogue.properties").write_text(
                "security.protocol=SSL\nssl.keystore.type=PEM\nssl.keystore.location=/tmp/rogue.pem\n"
                "ssl.truststore.type=PEM\nssl.truststore.location=/etc/kafka-node/clients/ca.crt\n")
            for name in ("rogue.pem", "rogue.properties"):
                subprocess.run(["podman", "cp", str(tmp / name), f"{node}:/tmp/{name}"], check=True, capture_output=True)
        out = subprocess.run([*harness.kafka_exec(node), f"{harness.KAFKA_BIN}/kafka-topics.sh", "--bootstrap-server",
                              harness.target("rehearsal").bootstrap, "--command-config", "/tmp/rogue.properties", "--list"],
                             capture_output=True, text=True, check=False, timeout=120)
        self.assertNotEqual(out.returncode, 0)
        self.assertRegex(out.stdout + out.stderr, "SslAuthenticationException|SSL handshake failed")
        time.sleep(3)
        self.assertRegex(broker_logs(since), "Failed authentication with .*SSL handshake failed")

    def test_producer_cannot_read_or_write_outside_its_grants(self):
        since = utc_since()
        node = harness.running_node_container()
        read = subprocess.run([*harness.kafka_exec(node), f"{harness.KAFKA_BIN}/kafka-console-consumer.sh", "--bootstrap-server",
                               harness.target("rehearsal").bootstrap, "--command-config", f"{harness.CLIENTS_IN_CONTAINER}/fluent-bit-gke.properties",
                               "--topic", "logs.proj.valid", "--from-beginning", "--timeout-ms", "15000"],
                              capture_output=True, text=True, check=False, timeout=120)
        self.assertRegex(read.stdout + read.stderr, "AuthorizationException|not authorized")
        wrote = produce("fluent-bit-gke", "ops.platform.events", '{"schema":1}')
        self.assertIn("TOPIC_AUTHORIZATION_FAILED", wrote.stdout + wrote.stderr)
        time.sleep(3)
        logs = broker_logs(since)
        self.assertRegex(logs, r"Principal = User:fluent-bit-gke is Denied operation = (READ|DESCRIBE)")
        self.assertRegex(logs, r"Principal = User:fluent-bit-gke is Denied operation = (DESCRIBE|WRITE) from host = \S+ on resource = Topic:LITERAL:ops\.platform\.events")


if __name__ == "__main__":
    unittest.main()
