#!/usr/bin/env python3
import argparse
import ipaddress
import re
import shutil
import subprocess
import sys
import tempfile
from dataclasses import dataclass
from pathlib import Path

import yaml

DEFAULT_ROOT = Path(__file__).resolve().parents[1]
DECLARATIONS = Path("infra/kafka/cluster.yaml")
FLUENT_BIT = Path("clusters/gke/observability/fluent-bit-kafka.conf")
ANSIBLE_OUT = Path("infra/oci/ansible/group_vars/all/kafka.generated.yml")
COMPOSE_OUT = Path("tests/kafka/rehearsal/compose.generated.yaml")
COMPOSE_NODES = Path("tests/kafka/rehearsal/nodes")
STRIMZI_OUT = Path("build/strimzi")
STRIMZI_VERSION = "1.3.1"
STRIMZI_SCHEMAS = Path("tests/fixtures/strimzi-schemas") / STRIMZI_VERSION

NODE_NETWORK = ipaddress.ip_network("10.200.0.0/24")
BROKER_BUDGET_BYTES = 20 * 2**30
SUPER_USER_NAME = re.compile(r"^(broker|controller)-\d+$")
DIRECTORY_ID = re.compile(r"^[A-Za-z0-9_][A-Za-z0-9_-]{21}$")
KEY_LOCATION = re.compile(r"^(vm:[a-z0-9-]+|cluster:[a-z0-9-]+/[a-z0-9-]+)$")
FLUENT_BIT_IDENTITY = "fluent-bit-gke"
FLUENT_BIT_TLS_KEYS = (
    "rdkafka.ssl.ca.location",
    "rdkafka.ssl.certificate.location",
    "rdkafka.ssl.key.location",
)
TLS_DIR = "/etc/kafka-node/tls"
DATA_DIR = "/var/lib/kafka/data"
CONFIG_DIR = "/etc/kafka-node"
LOG4J_OPTS = f"-Dlog4j2.configurationFile={CONFIG_DIR}/log4j2.yaml"


class DeclarationError(Exception):
    pass


@dataclass(frozen=True)
class Node:
    name: str
    node_id: int
    roles: tuple[str, ...]
    address: str
    initial_directory_id: str

    @property
    def is_broker(self) -> bool:
        return "broker" in self.roles

    @property
    def is_controller(self) -> bool:
        return "controller" in self.roles

    @property
    def broker_identity(self) -> str:
        return f"broker-{self.broker_index}"

    @property
    def controller_identity(self) -> str:
        return f"controller-{self.node_id}"

    broker_index: int = 0


@dataclass(frozen=True)
class Declarations:
    doc: dict
    nodes: tuple[Node, ...]

    @property
    def brokers(self) -> tuple[Node, ...]:
        return tuple(n for n in self.nodes if n.is_broker)

    @property
    def controllers(self) -> tuple[Node, ...]:
        return tuple(n for n in self.nodes if n.is_controller)

    @property
    def topics(self) -> list[dict]:
        return self.doc["topics"]

    @property
    def identities(self) -> list[dict]:
        return self.doc["identities"]

    @property
    def tls_identities(self) -> list[dict]:
        return self.doc.get("tls_identities", [])

    @property
    def super_users(self) -> list[str]:
        return [i["name"] for i in self.identities if i.get("super_user")]


def load(root: Path) -> Declarations:
    doc = yaml.safe_load((root / DECLARATIONS).read_text(encoding="utf-8"))
    for key in ("cluster", "nodes", "listeners", "broker_settings", "topics", "identities", "bootstrap"):
        if key not in doc:
            raise DeclarationError(f"{key}: missing from {DECLARATIONS}")
    nodes = []
    broker_index = 0
    for raw in sorted(doc["nodes"], key=lambda n: n["node_id"]):
        roles = tuple(sorted(raw["roles"]))
        if "broker" in roles:
            broker_index += 1
        nodes.append(Node(raw["name"], int(raw["node_id"]), roles, str(raw["address"]), str(raw.get("initial_directory_id", "")), broker_index if "broker" in roles else 0))
    node_names = {n.name for n in nodes}
    kafka_names = set()
    for ident in doc["identities"]:
        kafka_names.add(ident["name"])
        location = ident.get("key_location", "")
        if location != "vm:all" and not KEY_LOCATION.match(location):
            raise DeclarationError(f"identities[{ident['name']}].key_location: {location!r} is not vm:<node>, vm:all, or cluster:<namespace>/<secret>")
    tls_seen = set()
    for ident in doc.get("tls_identities", []):
        name = ident.get("name", "")
        if not name:
            raise DeclarationError("tls_identities: entry missing name")
        if name in kafka_names or name in tls_seen:
            raise DeclarationError(f"tls_identities[{name}]: duplicate identity name")
        tls_seen.add(name)
        vm = ident.get("vm", "")
        if vm not in node_names:
            raise DeclarationError(f"tls_identities[{name}].vm: {vm!r} is not a declared node")
        server_auth = ident.get("server_auth")
        if not isinstance(server_auth, bool):
            raise DeclarationError(f"tls_identities[{name}].server_auth: must be a boolean")
        sans = ident.get("server_sans", [])
        if server_auth and (not isinstance(sans, list) or not sans):
            raise DeclarationError(f"tls_identities[{name}].server_sans: required non-empty list when server_auth is true")
        dest_dir = ident.get("dest_dir", "")
        if not isinstance(dest_dir, str) or not dest_dir.startswith("/"):
            raise DeclarationError(f"tls_identities[{name}].dest_dir: must be an absolute path")
        owner_uid = ident.get("owner_uid")
        if isinstance(owner_uid, bool) or not isinstance(owner_uid, int) or owner_uid < 0:
            raise DeclarationError(f"tls_identities[{name}].owner_uid: must be a non-negative integer")
        reload_unit = ident.get("reload_unit", "")
        if not isinstance(reload_unit, str) or not reload_unit:
            raise DeclarationError(f"tls_identities[{name}].reload_unit: must be a non-empty string")
    return Declarations(doc, tuple(nodes))


def fluent_bit_outputs(text: str) -> list[dict[str, str]]:
    sections: list[tuple[str, dict[str, str]]] = []
    for line in text.splitlines():
        stripped = line.strip()
        if not stripped or stripped.startswith("#"):
            continue
        if stripped.startswith("[") and stripped.endswith("]"):
            sections.append((stripped[1:-1].upper(), {}))
            continue
        if sections:
            key, _, value = stripped.partition(" ")
            sections[-1][1][key.lower()] = value.strip()
    return [body for name, body in sections if name == "OUTPUT" and body.get("name") == "kafka"]


def check_fluent_bit(decl: Declarations, root: Path) -> list[str]:
    errors = []
    outputs = fluent_bit_outputs((root / FLUENT_BIT).read_text(encoding="utf-8"))
    declared = {t["name"] for t in decl.topics}
    produced = set()
    expected_brokers = ",".join(decl.doc["bootstrap"]["gke"])
    for out in outputs:
        match = out.get("match", "?")
        if out.get("brokers") != expected_brokers:
            errors.append(f"bootstrap.gke: {expected_brokers} differs from Brokers {out.get('brokers')} in [OUTPUT] Match {match}")
        for name in (out.get("topics") or "").split(","):
            name = name.strip()
            produced.add(name)
            if name not in declared:
                errors.append(f"Topics: {name} in [OUTPUT] Match {match} is not declared under topics")
        if out.get("rdkafka.security.protocol") != "ssl":
            errors.append(f"rdkafka.security.protocol: [OUTPUT] Match {match} must be ssl")
        for key in FLUENT_BIT_TLS_KEYS:
            if not out.get(key):
                errors.append(f"{key}: missing in [OUTPUT] Match {match}")
        if out.get("rdkafka.compression.codec") != "zstd":
            errors.append(f"rdkafka.compression.codec: [OUTPUT] Match {match} must be zstd")
    for name in sorted(n for n in declared if n.startswith("logs.") and n not in produced):
        errors.append(f"topics[{name}]: declared but no Fluent Bit Topics line produces it")
    return errors


def check_topics(decl: Declarations) -> list[str]:
    errors = []
    for t in decl.topics:
        name = t["name"]
        if t["replication_factor"] < 3:
            errors.append(f"topics[{name}].replication_factor: {t['replication_factor']} is below 3")
        if name.startswith(("logs.", "alerts.")) and t["min_insync_replicas"] < 2:
            errors.append(f"topics[{name}].min_insync_replicas: {t['min_insync_replicas']} is below 2")
    return errors


def grant_set(grants: list[dict]) -> set[tuple[str, str, str]]:
    return {(g["resource"], g.get("name", ""), op) for g in grants for op in g["ops"]}


def check_identities(decl: Declarations) -> list[str]:
    errors = []
    producer_topics = [t["name"] for t in decl.topics if t["name"].startswith("logs.")]
    allowed = {("topic", t, op) for t in producer_topics for op in ("Write", "Describe")}
    allowed.add(("cluster", "", "IdempotentWrite"))
    for ident in decl.identities:
        name = ident["name"]
        if ident.get("super_user") and not SUPER_USER_NAME.match(name):
            errors.append(f"identities[{name}].super_user: only broker-* and controller-* may be super users")
        if name == FLUENT_BIT_IDENTITY:
            extra = sorted(grant_set(ident.get("grants", [])) - allowed)
            for resource, target, op in extra:
                errors.append(f"identities[{name}].grants: {op} on {resource} {target} is not allowed for the producer".rstrip())
    return errors


def check_nodes(decl: Declarations) -> list[str]:
    errors = []
    if len(decl.brokers) != 3:
        errors.append(f"nodes.roles: {len(decl.brokers)} nodes have broker, need exactly 3")
    if len(decl.controllers) != 3:
        errors.append(f"nodes.roles: {len(decl.controllers)} nodes have controller, need exactly 3")
    for field, values in (("node_id", [n.node_id for n in decl.nodes]), ("address", [n.address for n in decl.nodes]), ("name", [n.name for n in decl.nodes])):
        dupes = sorted({str(v) for v in values if values.count(v) > 1})
        if dupes:
            errors.append(f"nodes.{field}: repeated {', '.join(dupes)}")
    for n in decl.nodes:
        if ipaddress.ip_address(n.address) not in NODE_NETWORK:
            errors.append(f"nodes[{n.name}].address: {n.address} is outside {NODE_NETWORK}")
        if n.is_controller and not DIRECTORY_ID.match(n.initial_directory_id):
            errors.append(f"nodes[{n.name}].initial_directory_id: controllers need a 22-character base64url ID that does not start with '-'")
    return errors


def check_budget(decl: Declarations) -> list[str]:
    settings = decl.doc["broker_settings"]
    errors = []
    for key in ("offsets.topic.num.partitions", "offsets.topic.segment.bytes"):
        val = settings.get(key)
        if isinstance(val, bool) or not isinstance(val, int) or val <= 0:
            errors.append(f"broker_settings.{key}: must be pinned to a positive integer")
    if errors:
        return errors
    offsets_bytes = settings["offsets.topic.num.partitions"] * settings["offsets.topic.segment.bytes"] * 2
    total = sum(t["retention_bytes"] * t["partitions"] + t["segment_bytes"] * t["partitions"] for t in decl.topics) + offsets_bytes
    if total > BROKER_BUDGET_BYTES:
        return [f"topics.retention_bytes: retention plus one segment per partition and __consumer_offsets is {total} bytes per broker, above the {BROKER_BUDGET_BYTES} budget"]
    return []


def check_strimzi(decl: Declarations, root: Path) -> list[str]:
    kubeconform = shutil.which("kubeconform")
    if kubeconform is None:
        return ["strimzi: kubeconform is not installed"]
    schemas = root / STRIMZI_SCHEMAS
    with tempfile.TemporaryDirectory() as tmp:
        out = Path(tmp)
        write_files(out, strimzi_files(decl))
        files = sorted(str(p) for p in out.glob("*.yaml"))
        result = subprocess.run(
            [kubeconform, "-strict", "-summary=false", "-output", "text",
             "-schema-location", str(schemas) + "/{{ .Group }}/{{ .ResourceKind }}_{{ .ResourceAPIVersion }}.json", *files],
            capture_output=True, text=True, check=False)
    if result.returncode != 0:
        lines = [l for l in (result.stdout + result.stderr).splitlines() if l.strip()]
        return [f"strimzi: {l}" for l in lines] or ["strimzi: kubeconform failed"]
    return []


def check(root: Path) -> int:
    try:
        decl = load(root)
    except DeclarationError as exc:
        print(exc, file=sys.stderr)
        return 1
    errors = (check_fluent_bit(decl, root) + check_topics(decl) + check_identities(decl)
              + check_nodes(decl) + check_budget(decl) + check_strimzi(decl, root))
    for e in errors:
        print(e, file=sys.stderr)
    if errors:
        return 1
    print(f"kafka declarations: {len(decl.topics)} topics, {len(decl.identities)} identities, {len(decl.nodes)} nodes OK")
    return 0


class IndentedDumper(yaml.SafeDumper):
    def increase_indent(self, flow=False, indentless=False):
        return super().increase_indent(flow, False)


def dump(data) -> str:
    return yaml.dump(data, Dumper=IndentedDumper, sort_keys=False, default_flow_style=False, width=1000, explicit_start=True)


def image_ref(decl: Declarations) -> str:
    return f"{decl.doc['cluster']['image']}@{decl.doc['cluster']['image_digest']}"


def initial_controllers(decl: Declarations, host_of) -> str:
    port = decl.doc["listeners"]["controller"]["port"]
    return ",".join(f"{n.node_id}@{host_of(n)}:{port}:{n.initial_directory_id}" for n in decl.controllers)


def format_args(decl: Declarations, node: Node, host_of, bootstrap: bool) -> list[str]:
    args = ["format", "--cluster-id", decl.doc["cluster"]["id"], "--config", f"{CONFIG_DIR}/server.properties"]
    if node.is_controller and bootstrap:
        return args + ["--initial-controllers", initial_controllers(decl, host_of)]
    return args + ["--no-initial-controllers"]


def node_properties(decl: Declarations, node: Node, bind_host: str, client_host: str, controller_host, controller_bootstrap: list[str]) -> dict[str, str]:
    listeners, advertised = [], []
    if node.is_broker:
        listeners.append(f"CLIENT://{bind_host}:{decl.doc['listeners']['client']['port']}")
        advertised.append(f"CLIENT://{client_host}:{decl.doc['listeners']['client']['port']}")
    if node.is_controller:
        listeners.append(f"CONTROLLER://{bind_host}:{decl.doc['listeners']['controller']['port']}")
        advertised.append(f"CONTROLLER://{controller_host}:{decl.doc['listeners']['controller']['port']}")
    primary = node.broker_identity if node.is_broker else node.controller_identity
    props = {
        "node.id": str(node.node_id),
        "process.roles": ",".join(r for r in ("broker", "controller") if r in node.roles),
        "listeners": ",".join(listeners),
        "advertised.listeners": ",".join(advertised),
        "listener.security.protocol.map": "CLIENT:SSL,CONTROLLER:SSL",
        "controller.listener.names": "CONTROLLER",
        "controller.quorum.bootstrap.servers": ",".join(controller_bootstrap),
        "log.dirs": DATA_DIR,
        "ssl.client.auth": "required",
        "ssl.keystore.type": "PEM",
        "ssl.keystore.location": f"{TLS_DIR}/{primary}.pem",
        "ssl.truststore.type": "PEM",
        "ssl.truststore.location": f"{TLS_DIR}/ca.crt",
        "ssl.principal.mapping.rules": "RULE:^CN=([^,]+)$/$1/,DEFAULT",
        "authorizer.class.name": "org.apache.kafka.metadata.authorizer.StandardAuthorizer",
        "super.users": ";".join(f"User:{u}" for u in decl.super_users),
    }
    if node.is_broker:
        props["inter.broker.listener.name"] = "CLIENT"
    if node.is_broker and node.is_controller:
        props["listener.name.controller.ssl.keystore.location"] = f"{TLS_DIR}/{node.controller_identity}.pem"
    for key, value in decl.doc["broker_settings"].items():
        props[key] = str(value).lower() if isinstance(value, bool) else str(value)
    return props


def properties_text(props: dict[str, str]) -> str:
    return "".join(f"{k}={v}\n" for k, v in props.items())


def node_identities(decl: Declarations, node: Node) -> list[str]:
    names = [i["name"] for i in decl.identities if i["key_location"] in (f"vm:{node.name}", "vm:all")]
    return sorted(names)


def normalize_tls_identity(ident: dict) -> dict:
    return {
        "name": ident["name"],
        "vm": ident["vm"],
        "server_auth": bool(ident["server_auth"]),
        "server_sans": list(ident.get("server_sans", [])),
        "dest_dir": ident["dest_dir"],
        "owner_uid": int(ident["owner_uid"]),
        "reload_unit": ident["reload_unit"],
    }


def node_tls_identities(decl: Declarations, node: Node) -> list[dict]:
    items = [normalize_tls_identity(i) for i in decl.tls_identities if i["vm"] == node.name]
    return sorted(items, key=lambda item: item["name"])


def ansible_files(decl: Declarations) -> dict[Path, str]:
    client_port = decl.doc["listeners"]["client"]["port"]
    controller_port = decl.doc["listeners"]["controller"]["port"]
    controller_bootstrap = [f"{n.address}:{controller_port}" for n in decl.controllers]
    data = {
        "kafka_cluster_id": decl.doc["cluster"]["id"],
        "kafka_version": decl.doc["cluster"]["kafka_version"],
        "kafka_image": image_ref(decl),
        "kafka_client_port": client_port,
        "kafka_log4j_opts": LOG4J_OPTS,
        "kafka_controller_port": controller_port,
        "kafka_bootstrap_servers": ",".join(f"{n.address}:{client_port}" for n in decl.brokers),
        "kafka_controller_bootstrap_servers": ",".join(controller_bootstrap),
        "kafka_controllers": [{"node_id": n.node_id, "name": n.name, "address": n.address} for n in decl.controllers],
        "kafka_nodes": {
            n.name: {
                "node_id": n.node_id,
                "roles": list(n.roles),
                "address": n.address,
                "identities": node_identities(decl, n),
                "tls_identities": node_tls_identities(decl, n),
                "server_properties": node_properties(decl, n, n.address, n.address, n.address, controller_bootstrap),
                "format_args_bootstrap": format_args(decl, n, lambda c: c.address, bootstrap=True),
                "format_args_join": format_args(decl, n, lambda c: c.address, bootstrap=False),
            }
            for n in decl.nodes
        },
        "kafka_topics": decl.topics,
        "kafka_identities": [
            {"name": i["name"], "key_location": i["key_location"], "super_user": bool(i.get("super_user")), "grants": i.get("grants", [])}
            for i in decl.identities
        ],
        "kafka_tls_identities": sorted(
            [normalize_tls_identity(i) for i in decl.tls_identities],
            key=lambda item: item["name"],
        ),
    }
    return {ANSIBLE_OUT: dump(data)}


def compose_files(decl: Declarations) -> dict[Path, str]:
    controller_port = decl.doc["listeners"]["controller"]["port"]
    controller_bootstrap = [f"node-{n.node_id}:{controller_port}" for n in decl.controllers]
    services = {}
    files: dict[Path, str] = {}
    volumes = {}
    for n in decl.nodes:
        svc = f"node-{n.node_id}"
        aliases = [svc] + ([n.broker_identity] if n.is_broker else [])
        props = node_properties(decl, n, "0.0.0.0", n.broker_identity if n.is_broker else svc, svc, controller_bootstrap)
        files[COMPOSE_NODES / f"{svc}.properties"] = properties_text(props)
        files[COMPOSE_NODES / f"{svc}.format"] = "\n".join(format_args(decl, n, lambda c: f"node-{c.node_id}", bootstrap=True)) + "\n"
        files[COMPOSE_NODES / f"{svc}.join.format"] = "\n".join(format_args(decl, n, lambda c: f"node-{c.node_id}", bootstrap=False)) + "\n"
        volumes[f"{svc}-data"] = {}
        services[svc] = {
            "image": image_ref(decl),
            "container_name": f"kafka-rehearsal-{svc}",
            "hostname": svc,
            "entrypoint": ["/opt/kafka/bin/kafka-server-start.sh"],
            "command": [f"{CONFIG_DIR}/server.properties"],
            "networks": {"kafka": {"aliases": aliases}},
            "volumes": [
                f"./nodes/{svc}.properties:{CONFIG_DIR}/server.properties:ro,Z",
                f"./.run/tls/{svc}:{TLS_DIR}:ro,Z",
                f"./.run/clients:{CONFIG_DIR}/clients:ro,Z",
                f"../../../infra/kafka/log4j2.yaml:{CONFIG_DIR}/log4j2.yaml:ro,Z",
                f"{svc}-data:{DATA_DIR}",
            ],
            "environment": {"KAFKA_HEAP_OPTS": "-Xms256m -Xmx256m", "KAFKA_LOG4J_OPTS": LOG4J_OPTS},
        }
    compose = {"name": "kafka-rehearsal", "services": services, "networks": {"kafka": {}}, "volumes": volumes}
    files[COMPOSE_OUT] = dump(compose)
    return files


def strimzi_acls(ident: dict) -> list[dict]:
    acls = []
    for g in ident.get("grants", []):
        if g["resource"] == "cluster":
            resource = {"type": "cluster"}
        elif g["name"] == "*":
            resource = {"type": g["resource"], "name": "*", "patternType": "literal"}
        else:
            resource = {"type": g["resource"], "name": g["name"], "patternType": "literal"}
        acls.append({"resource": resource, "operations": list(g["ops"])})
    return acls


def strimzi_files(decl: Declarations) -> dict[Path, str]:
    cluster = "logs"
    labels = {"strimzi.io/cluster": cluster}
    client_port = decl.doc["listeners"]["client"]["port"]
    kafka = {
        "apiVersion": "kafka.strimzi.io/v1",
        "kind": "Kafka",
        "metadata": {"name": cluster},
        "spec": {
            "kafka": {
                "version": decl.doc["cluster"]["kafka_version"],
                "listeners": [{"name": "tls", "port": client_port, "type": "internal", "tls": True, "authentication": {"type": "tls"}}],
                "authorization": {"type": "simple", "superUsers": list(decl.super_users)},
                "config": {k: v for k, v in decl.doc["broker_settings"].items()},
            },
        },
    }
    files = {Path("kafka.yaml"): dump(kafka)}
    pools: dict[tuple[str, ...], list[Node]] = {}
    for n in decl.nodes:
        pools.setdefault(n.roles, []).append(n)
    for roles, members in sorted(pools.items()):
        name = "-".join(roles)
        pool = {
            "apiVersion": "kafka.strimzi.io/v1",
            "kind": "KafkaNodePool",
            "metadata": {"name": name, "labels": labels, "annotations": {"strimzi.io/next-node-ids": "[" + ",".join(str(m.node_id) for m in members) + "]"}},
            "spec": {"replicas": len(members), "roles": list(roles), "storage": {"type": "persistent-claim", "size": "20Gi", "deleteClaim": False}},
        }
        files[Path(f"nodepool-{name}.yaml")] = dump(pool)
    for t in decl.topics:
        topic = {
            "apiVersion": "kafka.strimzi.io/v1",
            "kind": "KafkaTopic",
            "metadata": {"name": t["name"].replace(".", "-"), "labels": labels},
            "spec": {
                "topicName": t["name"],
                "partitions": t["partitions"],
                "replicas": t["replication_factor"],
                "config": {
                    "min.insync.replicas": t["min_insync_replicas"],
                    "retention.ms": t["retention_ms"],
                    "retention.bytes": t["retention_bytes"],
                    "segment.bytes": t["segment_bytes"],
                    "max.message.bytes": t["max_message_bytes"],
                    "message.timestamp.type": t["timestamp_type"],
                },
            },
        }
        files[Path(f"topic-{t['name']}.yaml")] = dump(topic)
    for ident in decl.identities:
        if ident.get("super_user"):
            continue
        user = {
            "apiVersion": "kafka.strimzi.io/v1",
            "kind": "KafkaUser",
            "metadata": {"name": ident["name"], "labels": labels},
            "spec": {"authentication": {"type": "tls"}, "authorization": {"type": "simple", "acls": strimzi_acls(ident)}},
        }
        files[Path(f"user-{ident['name']}.yaml")] = dump(user)
    return files


def write_files(base: Path, files: dict[Path, str]) -> None:
    for rel, text in sorted(files.items()):
        path = base / rel
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text, encoding="utf-8")


def render(root: Path, target: str) -> int:
    decl = load(root)
    if target == "ansible":
        write_files(root, ansible_files(decl))
    elif target == "compose":
        shutil.rmtree(root / COMPOSE_NODES, ignore_errors=True)
        write_files(root, compose_files(decl))
    else:
        shutil.rmtree(root / STRIMZI_OUT, ignore_errors=True)
        write_files(root / STRIMZI_OUT, strimzi_files(decl))
    return 0


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(prog="kafka-declarations")
    sub = parser.add_subparsers(dest="command", required=True)
    for name in ("check", "render"):
        p = sub.add_parser(name)
        p.add_argument("--root", type=Path, default=DEFAULT_ROOT)
        if name == "render":
            p.add_argument("--target", choices=("ansible", "compose", "strimzi"), required=True)
    args = parser.parse_args(argv)
    if args.command == "check":
        return check(args.root)
    return render(args.root, args.target)


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
