import base64
import ipaddress
import json
import shlex
import shutil
import struct
import subprocess
import tempfile
import unittest
from pathlib import Path

import yaml

REPO_ROOT = Path(__file__).resolve().parents[2]
OPS_AGENT_TEMPLATE = REPO_ROOT / "infra" / "oci" / "ansible" / "roles" / "ops_agent" / "templates" / "ops-agent.container.j2"
KAFKA_TEMPLATE = REPO_ROOT / "infra" / "oci" / "ansible" / "roles" / "kafka" / "templates" / "kafka.container.j2"
KAFKA_GENERATED = REPO_ROOT / "infra" / "oci" / "ansible" / "group_vars" / "all" / "kafka.generated.yml"
KAFKA_VARS = REPO_ROOT / "infra" / "oci" / "ansible" / "roles" / "kafka" / "vars" / "main.yml"
OCI_INVENTORY = REPO_ROOT / "infra" / "oci" / "ansible" / "inventory" / "oci.yml"
WIREGUARD_SLOTS = REPO_ROOT / "infra" / "oci" / "ansible" / "group_vars" / "brokers" / "wireguard_slots.yml"
SSH_AUTHORIZED_KEYS = REPO_ROOT / "infra" / "oci" / "ansible" / "group_vars" / "all" / "ssh_authorized_keys.yml"
WG_PEERS_JSON = REPO_ROOT / "clusters" / "gke" / "oci-link" / "wg-peers.json"
CLUSTER_YAML = REPO_ROOT / "infra" / "kafka" / "cluster.yaml"

PODMAN_4_9_3_CONTAINER_KEYS = frozenset({
    "AddCapability",
    "AddDevice",
    "Annotation",
    "AutoUpdate",
    "ContainerName",
    "ContainersConfModule",
    "DNS",
    "DNSOption",
    "DNSSearch",
    "DropCapability",
    "Environment",
    "EnvironmentFile",
    "EnvironmentHost",
    "Exec",
    "ExposeHostPort",
    "GIDMap",
    "GlobalArgs",
    "Group",
    "HealthCmd",
    "HealthInterval",
    "HealthOnFailure",
    "HealthRetries",
    "HealthStartPeriod",
    "HealthStartupCmd",
    "HealthStartupInterval",
    "HealthStartupRetries",
    "HealthStartupSuccess",
    "HealthStartupTimeout",
    "HealthTimeout",
    "HostName",
    "IP",
    "IP6",
    "Image",
    "Label",
    "LogDriver",
    "Mask",
    "Mount",
    "Network",
    "NoNewPrivileges",
    "Notify",
    "PidsLimit",
    "PodmanArgs",
    "PublishPort",
    "Pull",
    "ReadOnly",
    "ReadOnlyTmpfs",
    "RemapGid",
    "RemapUid",
    "RemapUidSize",
    "RemapUsers",
    "Rootfs",
    "RunInit",
    "SeccompProfile",
    "Secret",
    "SecurityLabelDisable",
    "SecurityLabelFileType",
    "SecurityLabelLevel",
    "SecurityLabelNested",
    "SecurityLabelType",
    "ShmSize",
    "SubGIDMap",
    "SubUIDMap",
    "Sysctl",
    "Timezone",
    "Tmpfs",
    "UIDMap",
    "Ulimit",
    "Unmask",
    "User",
    "UserNS",
    "VolatileTmp",
    "Volume",
    "WorkingDir",
})


def container_entries(text: str) -> list[tuple[str, str]]:
    in_container = False
    entries = []
    for raw in text.splitlines():
        line = raw.strip()
        if not line:
            continue
        if line.startswith("[") and line.endswith("]"):
            in_container = line == "[Container]"
            continue
        if in_container and "=" in line:
            key, val = line.split("=", 1)
            entries.append((key.strip(), val.strip()))
    return entries


def parse_quadlet_env(entries: list[tuple[str, str]]) -> tuple[dict[str, str], list[str]]:
    env_map: dict[str, str] = {}
    stray: list[str] = []
    for key, val in entries:
        if key != "Environment":
            continue
        for token in shlex.split(val):
            if "=" in token:
                k, v = token.split("=", 1)
                env_map[k] = v
            else:
                stray.append(token)
    return env_map, stray


def render_ops_agent_unit(gid: str = "101") -> str:
    text = OPS_AGENT_TEMPLATE.read_text(encoding="utf-8")
    replacements = {
        "{{ ops_agent_image }}": "docker.io/fluent/fluent-bit:4.2.8@sha256:6806769c10acf11b41d14083b3101a24433b4ca113a1ab7a6b1a1b7cad86a7e8",
        "{{ kafka_uid }}": "1500",
        "{{ ansible_facts.getent_group['systemd-journal'][1] }}": gid,
        "{{ ops_agent_tls_dir }}": "/etc/ops-agent/tls",
        "{{ inventory_hostname }}": "a1-1",
        "{{ kafka_nodes | dict2items | map(attribute='value') | map(attribute='node_id') | zip(kafka_nodes.keys()) | map('join', '=') | join(',') }}": "1=a1-1,2=a1-2,3=micro-1,4=micro-2",
        "{{ ops_agent_memory_max }}": "64M",
    }
    for src, dst in replacements.items():
        text = text.replace(src, dst)
    return text


def render_kafka_unit(
    node_id: int,
    roles: str,
    heap: str,
    memory_max: str,
    log4j_opts: str,
    image: str,
) -> str:
    text = KAFKA_TEMPLATE.read_text(encoding="utf-8")
    replacements = {
        "{{ kafka_nodes[inventory_hostname].node_id }}": str(node_id),
        "{{ kafka_nodes[inventory_hostname].roles | join(', ') }}": roles,
        "{{ kafka_image }}": image,
        "{{ kafka_uid }}": "1500",
        "{{ kafka_config_file }}": "/etc/kafka/server.properties",
        "{{ kafka_container_config }}": "/etc/kafka-node/server.properties",
        "{{ kafka_tls_dir }}": "/etc/kafka/tls",
        "{{ kafka_container_tls_dir }}": "/etc/kafka-node/tls",
        "{{ kafka_data_dir }}": "/var/lib/kafka/data",
        "{{ jmx_exporter_dir }}": "/opt/jmx-exporter",
        "{{ kafka_heap }}": heap,
        "{{ kafka_log4j_opts }}": log4j_opts,
        "{{ kafka_memory_max }}": memory_max,
    }
    for src, dst in replacements.items():
        text = text.replace(src, dst)
    return text


def find_quadlet_binary() -> Path | None:
    for candidate in ("/usr/libexec/podman/quadlet", "/usr/lib/systemd/system-generators/podman-system-generator"):
        path = Path(candidate)
        if path.exists():
            return path
    resolved = shutil.which("podman-system-generator")
    return Path(resolved) if resolved else None


class OciQuadletTests(unittest.TestCase):
    def test_ops_agent_quadlet_podman_4_9_3_keys_and_group_add(self):
        text = OPS_AGENT_TEMPLATE.read_text(encoding="utf-8")
        entries = container_entries(text)
        keys = [k for k, _ in entries]
        self.assertNotIn("GroupAdd", keys)
        self.assertTrue(set(keys).issubset(PODMAN_4_9_3_CONTAINER_KEYS), set(keys) - PODMAN_4_9_3_CONTAINER_KEYS)
        self.assertIn(
            ("PodmanArgs", "--group-add={{ ansible_facts.getent_group['systemd-journal'][1] }}"),
            entries,
        )
        rendered = render_ops_agent_unit()
        self.assertNotIn("{{", rendered)
        env_map, stray = parse_quadlet_env(container_entries(rendered))
        self.assertEqual(stray, [])
        self.assertEqual(
            env_map,
            {
                "OPS_VM": "a1-1",
                "OPS_NODES": "1=a1-1,2=a1-2,3=micro-1,4=micro-2",
                "OPS_REPLICATION": "3",
            },
        )

    def test_kafka_quadlet_podman_4_9_3_keys_and_quoted_heap_opts(self):
        text = KAFKA_TEMPLATE.read_text(encoding="utf-8")
        entries = container_entries(text)
        keys = [k for k, _ in entries]
        self.assertTrue(set(keys).issubset(PODMAN_4_9_3_CONTAINER_KEYS), set(keys) - PODMAN_4_9_3_CONTAINER_KEYS)
        self.assertIn(('Environment', '"KAFKA_HEAP_OPTS=-Xms{{ kafka_heap }} -Xmx{{ kafka_heap }}"'), entries)
        self.assertNotIn(('Environment', 'KAFKA_HEAP_OPTS=-Xms{{ kafka_heap }} -Xmx{{ kafka_heap }}'), entries)

        generated = yaml.safe_load(KAFKA_GENERATED.read_text(encoding="utf-8"))
        inventory = yaml.safe_load(OCI_INVENTORY.read_text(encoding="utf-8"))
        hosts = {}
        for group_name in ("a1", "micros"):
            hosts.update(inventory["all"]["children"][group_name]["hosts"])

        for host_name, node in generated["kafka_nodes"].items():
            with self.subTest(host=host_name):
                host_vars = hosts[host_name]
                heap = host_vars["kafka_heap"]
                rendered = render_kafka_unit(
                    node_id=node["node_id"],
                    roles=", ".join(node["roles"]),
                    heap=heap,
                    memory_max=host_vars["kafka_memory_max"],
                    log4j_opts=generated["kafka_log4j_opts"],
                    image=generated["kafka_image"],
                )
                self.assertNotIn("{{", rendered)
                env_map, stray = parse_quadlet_env(container_entries(rendered))
                self.assertEqual(stray, [])
                self.assertEqual(env_map["KAFKA_HEAP_OPTS"], f"-Xms{heap} -Xmx{heap}")
                self.assertEqual(env_map["KAFKA_LOG4J_OPTS"], generated["kafka_log4j_opts"])
                self.assertEqual(env_map["LOG_DIR"], "/tmp")
                self.assertEqual(
                    env_map["KAFKA_OPTS"],
                    "-javaagent:/opt/jmx-exporter/jmx_prometheus_javaagent.jar=127.0.0.1:7071:/etc/kafka-node/jmx-exporter.yaml",
                )

    def test_quadlet_generator_dryrun_when_available(self):
        quadlet = find_quadlet_binary()
        if quadlet is None:
            self.skipTest("podman quadlet generator binary is not installed")
        generated = yaml.safe_load(KAFKA_GENERATED.read_text(encoding="utf-8"))
        ops_rendered = render_ops_agent_unit("101")
        kafka_rendered = render_kafka_unit(
            node_id=1,
            roles="broker, controller",
            heap="1g",
            memory_max="1600M",
            log4j_opts=generated["kafka_log4j_opts"],
            image=generated["kafka_image"],
        )
        with tempfile.TemporaryDirectory() as tmp:
            src = Path(tmp) / "src"
            out = Path(tmp) / "out"
            src.mkdir()
            out.mkdir()
            (src / "ops-agent.container").write_text(ops_rendered, encoding="utf-8")
            (src / "kafka.container").write_text(kafka_rendered, encoding="utf-8")
            res = subprocess.run(
                [str(quadlet), "-dryrun", str(out)],
                env={"QUADLET_UNIT_DIRS": str(src)},
                capture_output=True,
                text=True,
                check=False,
            )
        self.assertEqual(res.returncode, 0, res.stderr)
        self.assertIn("--group-add=101", res.stdout)
        self.assertIn('--env "KAFKA_HEAP_OPTS=-Xms1g -Xmx1g"', res.stdout)


class OciWireGuardAndSshExchangeTests(unittest.TestCase):
    def test_wireguard_slots_populated_and_valid(self):
        doc = yaml.safe_load(WIREGUARD_SLOTS.read_text(encoding="utf-8"))
        self.assertEqual(doc["wireguard_port"], 51820)
        slots = doc["wireguard_slots"]
        self.assertEqual([s["slot"] for s in slots], [0, 1, 2, 3])
        seen_keys = set()
        for entry in slots:
            pub = entry["public_key"]
            self.assertIsInstance(pub, str)
            raw = base64.b64decode(pub, validate=True)
            self.assertEqual(len(raw), 32)
            self.assertEqual(base64.b64encode(raw).decode("ascii"), pub)
            seen_keys.add(pub)
        self.assertEqual(len(seen_keys), 4)

    def test_ssh_authorized_keys_populated_and_valid(self):
        doc = yaml.safe_load(SSH_AUTHORIZED_KEYS.read_text(encoding="utf-8"))
        keys = doc["ubuntu_ssh_authorized_keys"]
        self.assertIsInstance(keys, list)
        self.assertGreaterEqual(len(keys), 1)
        valid_types = ("ssh-rsa", "ssh-ed25519", "ecdsa-sha2-nistp256")
        for key in keys:
            self.assertIsInstance(key, str)
            self.assertNotIn("\n", key)
            self.assertNotIn("\r", key)
            parts = key.split()
            self.assertGreaterEqual(len(parts), 2)
            key_type, key_b64 = parts[0], parts[1]
            self.assertIn(key_type, valid_types)
            blob = base64.b64decode(key_b64, validate=True)
            self.assertGreater(len(blob), 4)
            type_len = struct.unpack(">I", blob[:4])[0]
            self.assertEqual(blob[4 : 4 + type_len].decode("ascii"), key_type)

    def test_wg_peers_json_valid_and_matches_declared_brokers(self):
        peers = json.loads(WG_PEERS_JSON.read_text(encoding="utf-8"))
        cluster = yaml.safe_load(CLUSTER_YAML.read_text(encoding="utf-8"))
        declared_brokers = sorted(
            [f"{n['address']}/32" for n in cluster["nodes"] if "broker" in n["roles"]]
        )
        self.assertEqual(len(peers), 3)
        self.assertEqual([p["name"] for p in peers], ["broker-1", "broker-2", "broker-3"])
        vcn = ipaddress.ip_network("10.200.0.0/24")
        seen_keys = set()
        seen_endpoints = set()
        addresses = []
        for peer in peers:
            raw_key = base64.b64decode(peer["publicKey"], validate=True)
            self.assertEqual(len(raw_key), 32)
            self.assertEqual(base64.b64encode(raw_key).decode("ascii"), peer["publicKey"])
            seen_keys.add(peer["publicKey"])

            host, port_str = peer["endpoint"].rsplit(":", 1)
            addr = ipaddress.ip_address(host)
            self.assertEqual(addr.version, 4)
            self.assertEqual(str(addr), host)
            self.assertEqual(int(port_str), 51820)
            seen_endpoints.add(peer["endpoint"])

            net = ipaddress.ip_network(peer["address"], strict=True)
            self.assertEqual(net.prefixlen, 32)
            self.assertTrue(net.subnet_of(vcn))
            addresses.append(peer["address"])

        self.assertEqual(len(seen_keys), 3)
        self.assertEqual(len(seen_endpoints), 3)
        self.assertEqual(sorted(addresses), declared_brokers)


if __name__ == "__main__":
    unittest.main()
