import argparse
import os
import subprocess
import sys
import time
from dataclasses import dataclass
from pathlib import Path

import yaml

REPO_ROOT = Path(__file__).resolve().parents[2]
DECLARATIONS = REPO_ROOT / "infra" / "kafka" / "cluster.yaml"
REHEARSAL = REPO_ROOT / "tests" / "kafka" / "rehearsal"
COMPOSE_FILE = REHEARSAL / "compose.generated.yaml"
CLIENTS_IN_CONTAINER = "/etc/kafka-node/clients"
KAFKA_BIN = "/opt/kafka/bin"
TOOLS_LOG4J = "KAFKA_LOG4J_OPTS=-Dlog4j2.configurationFile=/opt/kafka/config/tools-log4j2.yaml"


def kafka_exec(name: str, interactive: bool = False) -> list[str]:
    return ["podman", "exec", *(["-i"] if interactive else []), "-e", TOOLS_LOG4J, "-e", "KAFKA_OPTS=", name]


def declarations() -> dict:
    return yaml.safe_load(DECLARATIONS.read_text(encoding="utf-8"))


@dataclass(frozen=True)
class Target:
    name: str
    bootstrap: str
    admin_container: str

    def admin(self, tool: str, *args: str, identity: str = "broker-1", check: bool = True, timeout: float = 120) -> subprocess.CompletedProcess:
        if self.name == "live":
            raise RuntimeError("live admin commands run through the control center, not this harness")
        cmd = [*kafka_exec(running_node_container()), f"{KAFKA_BIN}/{tool}",
               "--bootstrap-server", self.bootstrap,
               "--command-config", f"{CLIENTS_IN_CONTAINER}/{identity}.properties", *args]
        return subprocess.run(cmd, capture_output=True, text=True, check=check, timeout=timeout)


def running_node_container() -> str:
    out = subprocess.run(["podman", "ps", "--filter", "status=running", "--format", "{{.Names}}"], capture_output=True, text=True, check=True)
    running = set(out.stdout.split())
    for node_id in (1, 2, 4, 3):
        if container(node_id) in running:
            return container(node_id)
    raise RuntimeError("no rehearsal node is running")


def target(name: str) -> Target:
    doc = declarations()
    if name == "live":
        return Target("live", os.environ["KAFKA_BOOTSTRAP"], "")
    return Target("rehearsal", ",".join(doc["bootstrap"]["rehearsal"]), "kafka-rehearsal-node-1")


def container(node_id: int) -> str:
    return f"kafka-rehearsal-node-{node_id}"


def compose(*args: str, check: bool = True) -> subprocess.CompletedProcess:
    return subprocess.run(["podman-compose", "-f", str(COMPOSE_FILE), *args], capture_output=True, text=True, check=check, cwd=REHEARSAL)


def stop_node(node_id: int) -> None:
    subprocess.run(["podman", "stop", "--time", "30", container(node_id)], capture_output=True, check=True)


def start_node(node_id: int) -> None:
    subprocess.run(["podman", "start", container(node_id)], capture_output=True, check=True)


def wipe_node(node_id: int) -> None:
    stop_node(node_id)
    subprocess.run(["podman", "rm", container(node_id)], capture_output=True, check=True)
    subprocess.run(["podman", "volume", "rm", f"kafka-rehearsal_node-{node_id}-data"], capture_output=True, check=True)


def format_node(node_id: int) -> subprocess.CompletedProcess:
    args = (REHEARSAL / "nodes" / f"node-{node_id}.format").read_text(encoding="utf-8").split()
    return compose("run", "--rm", "--no-deps", "--entrypoint", f"{KAFKA_BIN}/kafka-storage.sh", f"node-{node_id}", *args)


def registered_brokers(t: Target) -> int:
    out = t.admin("kafka-broker-api-versions.sh", check=False, timeout=60)
    if out.returncode != 0:
        return 0
    return sum(1 for line in out.stdout.splitlines() if "(id: " in line and not line.startswith(" "))


def under_replicated(t: Target) -> str:
    return t.admin("kafka-topics.sh", "--describe", "--under-replicated-partitions").stdout.strip()


def wait_for(predicate, timeout: float, interval: float = 3, what: str = "condition"):
    deadline = time.monotonic() + timeout
    last = None
    while time.monotonic() < deadline:
        last = predicate()
        if last:
            return last
        time.sleep(interval)
    raise TimeoutError(f"{what} not met within {timeout:.0f}s (last: {last!r})")


def wait_brokers(t: Target, count: int = 3, timeout: float = 180) -> None:
    wait_for(lambda: registered_brokers(t) == count, timeout, what=f"{count} registered brokers")


def wait_fully_replicated(t: Target, timeout: float = 900) -> None:
    wait_for(lambda: under_replicated(t) == "", timeout, interval=5, what="no under-replicated partitions")


LOG_FIXTURES = REPO_ROOT / "tests" / "fixtures" / "kafka-logs" / "4.3.2"


def utc_now() -> str:
    return time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())


def logs_since(node_id: int, since: str) -> str:
    out = subprocess.run(["podman", "logs", "--since", since, container(node_id)], capture_output=True, text=True, check=True)
    return out.stdout + out.stderr


def controller_leader(t: Target) -> int:
    out = t.admin("kafka-metadata-quorum.sh", "describe", "--status").stdout
    return int(next(line.split()[1] for line in out.splitlines() if line.startswith("LeaderId:")))


def save_fixture(name: str, text: str) -> None:
    LOG_FIXTURES.mkdir(parents=True, exist_ok=True)
    (LOG_FIXTURES / name).write_text(text, encoding="utf-8")
    print(f"captured {name}: {len(text.splitlines())} lines")


def capture_logs(t: Target) -> None:
    leader = controller_leader(t)
    since = utc_now()
    subprocess.run(["podman", "pause", container(3)], capture_output=True, check=True)
    time.sleep(25)
    subprocess.run(["podman", "unpause", container(3)], capture_output=True, check=True)
    wait_brokers(t)
    wait_fully_replicated(t)
    time.sleep(5)
    save_fixture(f"broker-fence-unfence.controller-{leader}.log", logs_since(leader, since))
    for node in (1, 2):
        save_fixture(f"isr-shrink-expand.broker-{node}.log", logs_since(node, since))

    leader = controller_leader(t)
    since = utc_now()
    stop_node(leader)
    time.sleep(20)
    new_leader = controller_leader(t)
    start_node(leader)
    wait_brokers(t)
    wait_fully_replicated(t)
    time.sleep(5)
    save_fixture(f"controller-leader-change.controller-{new_leader}.log", logs_since(new_leader, since))

    leader = controller_leader(t)
    since = utc_now()
    stop_node(3)
    time.sleep(15)
    save_fixture("broker-stop.broker-3.log", logs_since(3, since))
    start_node(3)
    wait_brokers(t)
    wait_fully_replicated(t)
    time.sleep(5)
    save_fixture(f"broker-stop.controller-{leader}.log", logs_since(leader, since))


def topic_commands(doc: dict) -> list[list[str]]:
    cmds = []
    for topic in doc["topics"]:
        configs = {
            "min.insync.replicas": topic["min_insync_replicas"],
            "retention.ms": topic["retention_ms"],
            "retention.bytes": topic["retention_bytes"],
            "segment.bytes": topic["segment_bytes"],
            "max.message.bytes": topic["max_message_bytes"],
            "message.timestamp.type": topic["timestamp_type"],
        }
        cmd = ["kafka-topics.sh", "--create", "--if-not-exists", "--topic", topic["name"],
               "--partitions", str(topic["partitions"]), "--replication-factor", str(topic["replication_factor"])]
        for key, value in configs.items():
            cmd += ["--config", f"{key}={value}"]
        cmds.append(cmd)
    return cmds


def acl_commands(doc: dict) -> list[list[str]]:
    cmds = []
    for ident in doc["identities"]:
        for grant in ident.get("grants", []):
            cmd = ["kafka-acls.sh", "--add", "--allow-principal", f"User:{ident['name']}"]
            for op in grant["ops"]:
                cmd += ["--operation", op]
            if grant["resource"] == "cluster":
                cmd.append("--cluster")
            else:
                cmd += [f"--{grant['resource']}", grant["name"]]
            cmds.append(cmd)
    return cmds


def apply_declarations(t: Target) -> None:
    doc = declarations()
    for cmd in topic_commands(doc) + acl_commands(doc):
        t.admin(cmd[0], *cmd[1:])


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(prog="harness")
    parser.add_argument("command", choices=["format", "wait", "declare", "capture-logs"])
    parser.add_argument("--target", choices=["rehearsal", "live"], default="rehearsal")
    args = parser.parse_args(argv)
    t = target(args.target)
    if args.command == "format":
        for node in declarations()["nodes"]:
            result = format_node(node["node_id"])
            sys.stdout.write(result.stdout)
    elif args.command == "capture-logs":
        capture_logs(t)
    elif args.command == "wait":
        wait_brokers(t)
        print("3 brokers registered")
    else:
        apply_declarations(t)
        print("topics and ACLs applied")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
