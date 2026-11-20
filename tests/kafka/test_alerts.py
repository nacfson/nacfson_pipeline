import json
import os
import shutil
import subprocess
import tempfile
import time
import unittest
from collections import Counter
from pathlib import Path

from tests.kafka import harness

FLUENT_BIT_IMAGE = "docker.io/fluent/fluent-bit:4.2.8@sha256:6806769c10acf11b41d14083b3101a24433b4ca113a1ab7a6b1a1b7cad86a7e8"
ALPINE_IMAGE = "docker.io/library/alpine:3.24.3"
KAFKA_NETWORK = "kafka-rehearsal_kafka"
NOTIFY_NETWORK = "kafka-rehearsal-notify-net"
RELAY = "kafka-rehearsal-relay"
NOTIFY = "kafka-rehearsal-notify"
ANSIBLE_ROOT = harness.REPO_ROOT / "infra" / "oci" / "ansible"
VMS = {1: "a1-1", 2: "a1-2", 3: "micro-1", 4: "micro-2"}
OPS_NODES = "1=a1-1,2=a1-2,3=micro-1,4=micro-2"
HEARTBEAT_SECONDS = 15


def sh(*cmd: str, check: bool = True, **kw) -> subprocess.CompletedProcess:
    return subprocess.run(list(cmd), capture_output=True, text=True, check=check, **kw)


def build(out: Path) -> None:
    sh("go", "build", "-o", str(out / "ops-relay"), "./cmd/ops-relay", cwd=harness.REPO_ROOT / "services" / "error-pipeline",
       env={**os.environ, "CGO_ENABLED": "0"})
    sh("go", "build", "-o", str(out / "fakenotify"), "main.go", cwd=harness.REPO_ROOT / "tests" / "kafka" / "fakenotify",
       env={**os.environ, "CGO_ENABLED": "0", "GO111MODULE": "off"})


def render_agent_conf(dest: Path) -> None:
    dest.mkdir(parents=True, exist_ok=True)
    extra = {
        "ops_agent_input": "stdin",
        "ops_agent_heartbeat_seconds": HEARTBEAT_SECONDS,
        "ops_agent_brokers": ",".join(harness.declarations()["bootstrap"]["rehearsal"]),
        "ops_agent_tls_dir": "/etc/ops-agent/tls",
        "ops_agent_identity": "__IDENTITY__",
    }
    sh(os.environ.get("ANSIBLE_BIN", "ansible"), "localhost", "-c", "local", "-m", "ansible.builtin.template",
       "-a", f"src={ANSIBLE_ROOT / 'roles/ops_agent/templates/fluent-bit.conf.j2'} dest={dest / 'fluent-bit.conf.tmpl'}",
       "-e", json.dumps(extra), env={**os.environ, "ANSIBLE_LOCALHOST_WARNING": "false", "ANSIBLE_INVENTORY_UNPARSED_WARNING": "false"})
    shutil.copy(ANSIBLE_ROOT / "roles/ops_agent/files/ops_events.lua", dest / "ops_events.lua")
    shutil.copy(ANSIBLE_ROOT / "roles/ops_agent/files/parsers.conf", dest / "parsers.conf")


class Rehearsal:
    def __init__(self):
        self.tmp = Path(tempfile.mkdtemp(prefix="alerts-"))
        self.agents: dict[int, subprocess.Popen] = {}

    def start(self) -> None:
        build(self.tmp)
        sh("podman", "network", "create", NOTIFY_NETWORK, check=False)
        sh("podman", "rm", "-f", RELAY, NOTIFY, check=False)
        sh("podman", "run", "-d", "--name", NOTIFY, "--network", NOTIFY_NETWORK, "-v", f"{self.tmp / 'fakenotify'}:/fakenotify:ro,Z",
           ALPINE_IMAGE, "/fakenotify")
        tls = self.tmp / "relay-tls"
        tls.mkdir()
        clients = harness.REHEARSAL / ".run" / "clients"
        for src, dst in (("ca.crt", "ca.crt"), ("ops-relay.crt", "tls.crt"), ("ops-relay.key", "tls.key")):
            sh("podman", "unshare", "cp", str(clients / src), str(tls / dst))
        sh("podman", "run", "-d", "--name", RELAY, "--network", KAFKA_NETWORK, "--network", NOTIFY_NETWORK,
           "-v", f"{self.tmp / 'ops-relay'}:/ops-relay:ro,Z", "-v", f"{tls}:/tls:ro,Z",
           "-e", "KAFKA_BROKERS=" + ",".join(harness.declarations()["bootstrap"]["rehearsal"]),
           "-e", "KAFKA_TLS_DIR=/tls", "-e", f"NOTIFY_URL=http://{NOTIFY}:8080", "-e", "STATUS_SOCKET=/tmp/status.sock",
           "-e", "READY_FILE=/tmp/ready", ALPINE_IMAGE, "/ops-relay", "serve")
        render_agent_conf(self.tmp / "agent")
        for node in VMS:
            self.start_agent(node)
        harness.wait_for(lambda: sh("podman", "exec", RELAY, "test", "-f", "/tmp/ready", check=False).returncode == 0, 240, what="relay ready")

    def start_agent(self, node: int) -> None:
        vm = VMS[node]
        conf = self.tmp / f"agent-{vm}"
        conf.mkdir(exist_ok=True)
        text = (self.tmp / "agent" / "fluent-bit.conf.tmpl").read_text(encoding="utf-8").replace("__IDENTITY__", f"ops-agent-{vm}")
        (conf / "fluent-bit.conf").write_text(text, encoding="utf-8")
        for name in ("ops_events.lua", "parsers.conf"):
            shutil.copy(self.tmp / "agent" / name, conf / name)
        name = f"kafka-rehearsal-agent-{node}"
        sh("podman", "rm", "-f", name, check=False)
        pipeline = (
            f"while true; do podman logs -f --since 1s {harness.container(node)} 2>&1; sleep 1; done | "
            f"podman run -i --rm --name {name} --network {KAFKA_NETWORK} -e OPS_VM={vm} -e OPS_NODES={OPS_NODES} "
            f"-v {conf}:/fluent-bit/etc:ro,Z -v {harness.REHEARSAL / '.run' / 'clients'}:/etc/ops-agent/tls:ro,Z {FLUENT_BIT_IMAGE}"
        )
        self.agents[node] = subprocess.Popen(["sh", "-c", pipeline], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, start_new_session=True)

    def stop_agent(self, node: int) -> None:
        sh("podman", "stop", "--time", "5", f"kafka-rehearsal-agent-{node}", check=False)
        proc = self.agents.pop(node, None)
        if proc:
            os.killpg(proc.pid, 15)
            proc.wait(timeout=30)

    def messages(self) -> list[str]:
        out = sh("podman", "logs", NOTIFY).stdout
        return [json.loads(line)["title"] for line in out.splitlines() if line.startswith("{")]

    def status(self) -> str:
        return sh("podman", "exec", RELAY, "/ops-relay", "status", check=False).stdout

    def stop(self) -> None:
        for node in list(self.agents):
            self.stop_agent(node)
        logs = sh("podman", "logs", RELAY, check=False)
        (harness.REHEARSAL / ".run" / "relay.log").write_text(logs.stdout + logs.stderr, encoding="utf-8")
        sh("podman", "rm", "-f", RELAY, NOTIFY, check=False)
        sh("podman", "unshare", "rm", "-rf", str(self.tmp), check=False)


def subjects_since(messages: list[str], start: int) -> Counter:
    counted = Counter()
    for m in messages[start:]:
        parts = m.split()
        level, subject = parts[1], parts[2]
        counted[(level, subject)] += 1
    return counted


class Alerts(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.r = Rehearsal()
        cls.r.start()
        time.sleep(200)

    @classmethod
    def tearDownClass(cls):
        cls.r.stop()

    def setUp(self):
        t = harness.target("rehearsal")
        harness.wait_brokers(t)
        harness.wait_fully_replicated(t)
        time.sleep(10)
        self.start = len(self.r.messages())

    def wait_messages(self, want: Counter, timeout: float) -> Counter:
        def enough():
            got = subjects_since(self.r.messages(), self.start)
            return got if all(got[k] >= v for k, v in want.items()) else None
        try:
            harness.wait_for(enough, timeout, interval=5, what=f"messages {dict(want)}")
        finally:
            time.sleep(20)
        return subjects_since(self.r.messages(), self.start)

    def test_a_stopped_broker_gives_one_down_and_one_ok(self):
        harness.stop_node(3)
        status_seen = harness.wait_for(lambda: "brokers 2/3" in self.r.status(), 60, interval=5, what="status shows 2/3")
        self.assertTrue(status_seen)
        self.wait_messages(Counter({("DOWN", "vm/micro-1"): 1}), 120)
        time.sleep(120)
        harness.start_node(3)
        got = self.wait_messages(Counter({("OK", "vm/micro-1"): 1}), 300)
        self.assertEqual(got[("DOWN", "vm/micro-1")], 1, got)
        self.assertEqual(got[("OK", "vm/micro-1")], 1, got)

    def test_b_broker_back_within_two_minutes_gives_one_down_and_one_ok(self):
        harness.stop_node(3)
        time.sleep(60)
        harness.start_node(3)
        got = self.wait_messages(Counter({("DOWN", "vm/micro-1"): 1, ("OK", "vm/micro-1"): 1}), 300)
        self.assertEqual(got[("DOWN", "vm/micro-1")], 1, got)
        self.assertEqual(got[("OK", "vm/micro-1")], 1, got)

    def test_c_silent_agent_is_labelled_broker_healthy(self):
        self.r.stop_agent(4)
        try:
            self.wait_messages(Counter({("DOWN", "vm/micro-2"): 1}), 300)
        finally:
            self.r.start_agent(4)
        got = self.wait_messages(Counter({("OK", "vm/micro-2"): 1}), 120)
        downs = [m for m in self.r.messages()[self.start:] if "DOWN" in m and "vm/micro-2" in m]
        self.assertEqual(len(downs), 1, got)
        self.assertIn("agent silent, broker healthy", downs[0])

    def test_d_cut_network_gives_one_unreachable_and_no_silent_vms(self):
        sh("podman", "network", "disconnect", KAFKA_NETWORK, RELAY)
        try:
            self.wait_messages(Counter({("DOWN", "cluster/unreachable"): 1}), 180)
            time.sleep(200)
        finally:
            sh("podman", "network", "connect", KAFKA_NETWORK, RELAY)
        got = self.wait_messages(Counter({("OK", "cluster/unreachable"): 1}), 180)
        time.sleep(200)
        got = subjects_since(self.r.messages(), self.start)
        self.assertEqual(got[("DOWN", "cluster/unreachable")], 1, got)
        self.assertFalse([k for k in got if k[1].startswith("vm/")], got)


if __name__ == "__main__":
    unittest.main()
