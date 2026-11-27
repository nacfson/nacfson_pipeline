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
OPS_AGENT_TASKS = REPO_ROOT / "infra" / "oci" / "ansible" / "roles" / "ops_agent" / "tasks" / "main.yml"
LOKI_TEMPLATE = REPO_ROOT / "infra" / "oci" / "ansible" / "roles" / "loki" / "templates" / "loki.container.j2"
LOKI_TASKS = REPO_ROOT / "infra" / "oci" / "ansible" / "roles" / "loki" / "tasks" / "main.yml"
LOKI_DEFAULTS = REPO_ROOT / "infra" / "oci" / "ansible" / "roles" / "loki" / "defaults" / "main.yml"
VECTOR_TEMPLATE = REPO_ROOT / "infra" / "oci" / "ansible" / "roles" / "vector" / "templates" / "vector.container.j2"
VECTOR_TASKS = REPO_ROOT / "infra" / "oci" / "ansible" / "roles" / "vector" / "tasks" / "main.yml"
VECTOR_DEFAULTS = REPO_ROOT / "infra" / "oci" / "ansible" / "roles" / "vector" / "defaults" / "main.yml"
ERROR_DETECTOR_TEMPLATE = REPO_ROOT / "infra" / "oci" / "ansible" / "roles" / "error_detector" / "templates" / "error-detector.container.j2"
ERROR_DETECTOR_TASKS = REPO_ROOT / "infra" / "oci" / "ansible" / "roles" / "error_detector" / "tasks" / "main.yml"
ERROR_DETECTOR_DEFAULTS = REPO_ROOT / "infra" / "oci" / "ansible" / "roles" / "error_detector" / "defaults" / "main.yml"
NFTABLES_TEMPLATE = REPO_ROOT / "infra" / "oci" / "ansible" / "roles" / "base" / "templates" / "nftables.conf.j2"
CERTS_VARS = REPO_ROOT / "infra" / "oci" / "ansible" / "roles" / "certs" / "vars" / "main.yml"
SITE_PLAYBOOK = REPO_ROOT / "infra" / "oci" / "ansible" / "site.yml"
RENEW_CERTS_PLAYBOOK = REPO_ROOT / "infra" / "oci" / "ansible" / "renew-certs.yml"
OCI_KAFKA_RUNBOOK = REPO_ROOT / "docs" / "runbooks" / "oci-kafka.md"
KAFKA_TEMPLATE = REPO_ROOT / "infra" / "oci" / "ansible" / "roles" / "kafka" / "templates" / "kafka.container.j2"
KAFKA_GENERATED = REPO_ROOT / "infra" / "oci" / "ansible" / "group_vars" / "all" / "kafka.generated.yml"
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


def section_entries(text: str, section_name: str) -> list[tuple[str, str]]:
    in_section = False
    entries = []
    for raw in text.splitlines():
        line = raw.strip()
        if not line:
            continue
        if line.startswith("[") and line.endswith("]"):
            in_section = line == f"[{section_name}]"
            continue
        if in_section and "=" in line:
            key, val = line.split("=", 1)
            entries.append((key.strip(), val.strip()))
    return entries


def container_entries(text: str) -> list[tuple[str, str]]:
    return section_entries(text, "Container")


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


def render_ops_agent_unit(
    gid: str = "101",
    watched_units: str = "kafka.service,wg-quick@wg0.service,ops-agent.service",
) -> str:
    text = OPS_AGENT_TEMPLATE.read_text(encoding="utf-8")
    replacements = {
        "{{ ops_agent_image }}": "docker.io/fluent/fluent-bit:4.2.8@sha256:6806769c10acf11b41d14083b3101a24433b4ca113a1ab7a6b1a1b7cad86a7e8",
        "{{ kafka_uid }}": "1500",
        "{{ ansible_facts.getent_group['systemd-journal'][1] }}": gid,
        "{{ ops_agent_tls_dir }}": "/etc/ops-agent/tls",
        "{{ inventory_hostname }}": "a1-1",
        "{{ kafka_nodes | dict2items | map(attribute='value') | map(attribute='node_id') | zip(kafka_nodes.keys()) | map('join', '=') | join(',') }}": "1=a1-1,2=a1-2,3=micro-1,4=micro-2",
        "{{ ops_agent_watched_units | join(',') }}": watched_units,
        "{{ ops_agent_memory_max }}": "64M",
    }
    for src, dst in replacements.items():
        text = text.replace(src, dst)
    return text


def render_loki_unit() -> str:
    defaults = yaml.safe_load(LOKI_DEFAULTS.read_text(encoding="utf-8"))
    text = LOKI_TEMPLATE.read_text(encoding="utf-8")
    replacements = {
        "{{ loki_image }}": defaults["loki_image"],
        "{{ loki_uid }}": str(defaults["loki_uid"]),
        "{{ loki_tls_dir }}": defaults["loki_tls_dir"],
        "{{ loki_data_dir }}": defaults["loki_data_dir"],
        "{{ loki_memory_max }}": defaults["loki_memory_max"],
    }
    for src, dst in replacements.items():
        text = text.replace(src, dst)
    return text


def render_vector_unit() -> str:
    defaults = yaml.safe_load(VECTOR_DEFAULTS.read_text(encoding="utf-8"))
    text = VECTOR_TEMPLATE.read_text(encoding="utf-8")
    replacements = {
        "{{ vector_image }}": defaults["vector_image"],
        "{{ vector_uid }}": str(defaults["vector_uid"]),
        "{{ vector_tls_dir }}": defaults["vector_tls_dir"],
        "{{ vector_memory_max }}": defaults["vector_memory_max"],
    }
    for src, dst in replacements.items():
        text = text.replace(src, dst)
    return text


def render_error_detector_unit() -> str:
    defaults = yaml.safe_load(ERROR_DETECTOR_DEFAULTS.read_text(encoding="utf-8"))
    text = ERROR_DETECTOR_TEMPLATE.read_text(encoding="utf-8")
    replacements = {
        "{{ error_detector_image }}": defaults["error_detector_image"],
        "{{ error_detector_uid }}": str(defaults["error_detector_uid"]),
        "{{ error_detector_tls_dir }}": defaults["error_detector_tls_dir"],
        "{{ error_detector_listen_addr }}": defaults["error_detector_listen_addr"],
        "{{ error_detector_brokers }}": "10.200.0.11:9092,10.200.0.12:9092,10.200.0.13:9092",
        "{{ error_detector_group }}": defaults["error_detector_group"],
        "{{ error_detector_outbox_topic }}": defaults["error_detector_outbox_topic"],
        "{{ error_detector_dedup_ttl }}": defaults["error_detector_dedup_ttl"],
        "{{ error_detector_quarantine_throttle }}": defaults["error_detector_quarantine_throttle"],
        "{{ error_detector_memory_max }}": defaults["error_detector_memory_max"],
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
                "OPS_UNITS": "kafka.service,wg-quick@wg0.service,ops-agent.service",
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

    def test_observability_quadlets_podman_4_9_3_keys_and_limits(self):
        specs = [
            (LOKI_TEMPLATE, render_loki_unit(), "1800M"),
            (VECTOR_TEMPLATE, render_vector_unit(), "256M"),
            (ERROR_DETECTOR_TEMPLATE, render_error_detector_unit(), "96M"),
        ]
        for tmpl_path, rendered, expected_mem in specs:
            with self.subTest(template=tmpl_path.name):
                raw_entries = container_entries(tmpl_path.read_text(encoding="utf-8"))
                keys = [k for k, _ in raw_entries]
                self.assertNotIn("GroupAdd", keys)
                self.assertTrue(set(keys).issubset(PODMAN_4_9_3_CONTAINER_KEYS), set(keys) - PODMAN_4_9_3_CONTAINER_KEYS)
                self.assertNotIn("{{", rendered)
                entries = container_entries(rendered)
                self.assertIn(("ReadOnly", "true"), entries)
                env_map, stray = parse_quadlet_env(entries)
                self.assertEqual(stray, [])
                svc_entries = dict(section_entries(rendered, "Service"))
                self.assertEqual(svc_entries.get("MemoryMax"), expected_mem)
                self.assertEqual(svc_entries.get("OOMScoreAdjust"), "600")
                self.assertEqual(svc_entries.get("CPUWeight"), "100")

        det_env, _ = parse_quadlet_env(container_entries(render_error_detector_unit()))
        self.assertEqual(det_env["SOURCE_MODE"], "kafka")
        self.assertEqual(det_env["NOTIFY_MODE"], "kafka-outbox")
        self.assertEqual(det_env["STATE_ENABLED"], "false")
        self.assertEqual(det_env["DEDUP_TTL"], "10m")
        self.assertEqual(det_env["QUARANTINE_THROTTLE"], "1h")
        self.assertNotIn("GRAFANA_BASE_URL", det_env)

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
        loki_rendered = render_loki_unit()
        vector_rendered = render_vector_unit()
        det_rendered = render_error_detector_unit()
        with tempfile.TemporaryDirectory() as tmp:
            src = Path(tmp) / "src"
            out = Path(tmp) / "out"
            src.mkdir()
            out.mkdir()
            (src / "ops-agent.container").write_text(ops_rendered, encoding="utf-8")
            (src / "kafka.container").write_text(kafka_rendered, encoding="utf-8")
            (src / "loki.container").write_text(loki_rendered, encoding="utf-8")
            (src / "vector.container").write_text(vector_rendered, encoding="utf-8")
            (src / "error-detector.container").write_text(det_rendered, encoding="utf-8")
            res = subprocess.run(
                [str(quadlet), "-dryrun", str(out)],
                env={"QUADLET_UNIT_DIRS": str(src)},
                capture_output=True,
                text=True,
                check=False,
            )
        self.assertEqual(res.returncode, 0, res.stderr)
        exec_starts = [line for line in res.stdout.splitlines() if line.startswith("ExecStart=")]
        normalized = "\n".join(exec_starts).replace("\\x20", " ")
        self.assertIn("--group-add=101", normalized)
        self.assertIn('--env "KAFKA_HEAP_OPTS=-Xms1g -Xmx1g"', normalized)


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

    def test_ops_agent_prepulls_image_before_quadlet_and_start(self):
        for tasks_path, image_var, template_src, unit_name in (
            (OPS_AGENT_TASKS, "{{ ops_agent_image }}", "ops-agent.container.j2", "ops-agent"),
            (LOKI_TASKS, "{{ loki_image }}", "loki.container.j2", "loki"),
            (VECTOR_TASKS, "{{ vector_image }}", "vector.container.j2", "vector"),
            (ERROR_DETECTOR_TASKS, "{{ error_detector_image }}", "error-detector.container.j2", "error-detector"),
        ):
            with self.subTest(unit=unit_name):
                tasks = yaml.safe_load(tasks_path.read_text(encoding="utf-8"))
                exists_idx = None
                pull_idx = None
                write_idx = None
                start_idx = None
                for idx, task in enumerate(tasks):
                    cmd = task.get("ansible.builtin.command")
                    if isinstance(cmd, dict):
                        argv = cmd.get("argv")
                        if argv == ["podman", "image", "exists", image_var]:
                            exists_idx = idx
                            self.assertIs(task.get("changed_when"), False)
                            self.assertIs(task.get("failed_when"), False)
                        elif argv == ["podman", "pull", image_var]:
                            pull_idx = idx
                            self.assertIs(task.get("changed_when"), True)
                            self.assertIn("rc != 0", str(task.get("when", "")))
                    tmpl = task.get("ansible.builtin.template")
                    if isinstance(tmpl, dict) and tmpl.get("src") == template_src:
                        write_idx = idx
                    svc = task.get("ansible.builtin.systemd_service")
                    if isinstance(svc, dict) and svc.get("name") == unit_name:
                        start_idx = idx
                self.assertIsNotNone(exists_idx)
                self.assertIsNotNone(pull_idx)
                self.assertIsNotNone(write_idx)
                self.assertIsNotNone(start_idx)
                self.assertLess(exists_idx, pull_idx)
                self.assertLess(pull_idx, write_idx)
                self.assertLess(write_idx, start_idx)

    def test_headless_oci_observability_boundaries(self):
        nft = NFTABLES_TEMPLATE.read_text(encoding="utf-8")
        self.assertIn('iifname "wg0" ip saddr 10.201.0.0/28 ip daddr {{ ansible_host }} tcp dport {{ kafka_client_port }} accept', nft)
        self.assertNotIn("3000", nft)
        self.assertNotIn("3100", nft)

        certs_vars = yaml.safe_load(CERTS_VARS.read_text(encoding="utf-8"))
        self.assertEqual(certs_vars["certs_enabled_tls_identities"], ["loki-a1-2"])
        self.assertNotIn("grafana-a1-1", certs_vars["certs_enabled_tls_identities"])

        site = yaml.safe_load(SITE_PLAYBOOK.read_text(encoding="utf-8"))
        obs_play = next(p for p in site if p.get("hosts") == "a1-2")
        obs_roles = [r["role"] if isinstance(r, dict) else r for r in obs_play["roles"]]
        self.assertEqual(obs_roles, ["loki", "vector", "error_detector"])
        self.assertNotIn("grafana", obs_roles)
        obs_idx = site.index(obs_play)
        commit_plays = [
            idx for idx, p in enumerate(site)
            if "/etc/nacfson/applied-commit" in yaml.safe_dump(p)
        ]
        self.assertEqual(len(commit_plays), 1)
        self.assertGreater(commit_plays[0], obs_idx)

        renew = yaml.safe_load(RENEW_CERTS_PLAYBOOK.read_text(encoding="utf-8"))
        renew_dump = yaml.safe_dump(renew)
        self.assertNotIn("grafana", renew_dump)
        self.assertIn("renew_kafka", renew_dump)
        self.assertNotIn("results[0]", renew_dump)
        self.assertFalse((REPO_ROOT / "infra" / "oci" / "ansible" / "roles" / "grafana").exists())
        self.assertFalse((REPO_ROOT / "infra" / "oci" / "ansible" / "host_vars" / "a1-1" / "wireguard_operators.yml").exists())

        loki_uid = yaml.safe_load(LOKI_DEFAULTS.read_text(encoding="utf-8"))["loki_uid"]
        vector_uid = yaml.safe_load(VECTOR_DEFAULTS.read_text(encoding="utf-8"))["vector_uid"]
        det_uid = yaml.safe_load(ERROR_DETECTOR_DEFAULTS.read_text(encoding="utf-8"))["error_detector_uid"]
        self.assertEqual(len({1500, loki_uid, vector_uid, det_uid}), 4)

        loki_yaml_tmpl = (REPO_ROOT / "infra" / "oci" / "ansible" / "roles" / "loki" / "templates" / "loki.yaml.j2").read_text(encoding="utf-8")
        self.assertIn("grpc_tls_config:", loki_yaml_tmpl)
        self.assertIn("max_chunk_age: 48h", loki_yaml_tmpl)

        for pb_name in ("replace-controller.yml", "rebuild-node.yml"):
            pb_text = (REPO_ROOT / "infra" / "oci" / "ansible" / pb_name).read_text(encoding="utf-8")
            self.assertNotIn("/etc/nacfson/applied-commit", pb_text)

    def test_runbook_kafka_podman_exec_clears_kafka_opts(self):
        text = OCI_KAFKA_RUNBOOK.read_text(encoding="utf-8")
        in_fence = False
        exec_lines = []
        for raw_line in text.splitlines():
            stripped = raw_line.strip()
            if stripped.startswith("```"):
                in_fence = not in_fence
                continue
            if in_fence and "podman exec " in stripped and " kafka" in stripped:
                exec_lines.append(stripped)
        self.assertEqual(len(exec_lines), 3)
        for line in exec_lines:
            self.assertIn("--env=KAFKA_OPTS=", line)


if __name__ == "__main__":
    unittest.main()
