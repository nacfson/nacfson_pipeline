"""Manifest tests for deploy/platform/observability.

`render()` is the shared helper Task 3 tests import.
"""

from __future__ import annotations

import fnmatch
import json
import os
import re
import subprocess
import unittest

import sys
import yaml

BASE_DIR = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
OBSERVABILITY_DIR = os.path.join(BASE_DIR, "deploy", "platform", "observability")
GKE_OBSERVABILITY_DIR = os.path.join(BASE_DIR, "clusters", "gke", "observability")
if os.path.join(BASE_DIR, "scripts") not in sys.path:
    sys.path.insert(0, os.path.join(BASE_DIR, "scripts"))
from kustomize_render import kustomize_build


def render(kdir: str = OBSERVABILITY_DIR) -> list[dict]:
    """Render an observability kustomize directory and return documents."""
    out = kustomize_build(kdir)
    docs = [doc for doc in yaml.safe_load_all(out) if doc]
    return docs


def _by_kind_name(docs: list[dict], kind: str, name: str) -> dict:
    matches = [
        doc
        for doc in docs
        if doc.get("kind") == kind and doc.get("metadata", {}).get("name") == name
    ]
    if len(matches) != 1:
        raise AssertionError(f"expected one {kind}/{name}, found {len(matches)}")
    return matches[0]


class TestVictoriaLogsStore(unittest.TestCase):
    def test_namespace_is_privileged(self):
        docs = render()
        ns = _by_kind_name(docs, "Namespace", "observability")
        meta = ns["metadata"]
        self.assertEqual(
            meta.get("annotations", {}).get("kustomize.toolkit.fluxcd.io/prune"),
            "disabled",
        )
        labels = meta["labels"]
        for key in (
            "pod-security.kubernetes.io/enforce",
            "pod-security.kubernetes.io/warn",
            "pod-security.kubernetes.io/audit",
        ):
            self.assertEqual(labels[key], "privileged")
        self.assertNotIn("restricted", labels.values())
        self.assertNotIn("baseline", labels.values())

    def test_retention_flags_and_disk_cap_below_pvc(self):
        docs = render()
        sts = _by_kind_name(docs, "StatefulSet", "victorialogs")
        args = sts["spec"]["template"]["spec"]["containers"][0]["args"]
        self.assertIn("-retentionPeriod=30d", args)
        self.assertIn("-retention.maxDiskSpaceUsageBytes=8589934592", args)
        storage = sts["spec"]["volumeClaimTemplates"][0]["spec"]["resources"]["requests"][
            "storage"
        ]
        self.assertEqual(storage, "10Gi")
        self.assertEqual(8589934592, 8 * 1024**3)
        self.assertLess(8589934592, 10 * 1024**3)

    def test_victorialogs_hardening_and_resources(self):
        docs = render()
        sts = _by_kind_name(docs, "StatefulSet", "victorialogs")
        pod = sts["spec"]["template"]["spec"]
        self.assertIs(pod["automountServiceAccountToken"], False)
        container = pod["containers"][0]
        sec = container["securityContext"]
        self.assertIs(sec["allowPrivilegeEscalation"], False)
        self.assertEqual(sec["capabilities"]["drop"], ["ALL"])
        self.assertIs(pod["securityContext"]["runAsNonRoot"], True)
        resources = container["resources"]
        self.assertEqual(resources["requests"]["cpu"], "50m")
        self.assertEqual(resources["requests"]["memory"], "384Mi")
        self.assertEqual(resources["limits"]["cpu"], "500m")
        self.assertEqual(resources["limits"]["memory"], "384Mi")

    def test_service_clusterip_9428_and_nothing_public(self):
        docs = render()
        svc = _by_kind_name(docs, "Service", "victorialogs")
        self.assertEqual(svc["spec"].get("type", "ClusterIP"), "ClusterIP")
        ports = svc["spec"]["ports"]
        self.assertTrue(any(port.get("port") == 9428 for port in ports))
        kinds = {doc.get("kind") for doc in docs}
        self.assertNotIn("Secret", kinds)
        self.assertNotIn("IngressRoute", kinds)
        self.assertNotIn("Ingress", kinds)
        for doc in docs:
            if doc.get("kind") == "Service":
                self.assertEqual(doc["spec"].get("type", "ClusterIP"), "ClusterIP")
                self.assertNotEqual(doc["spec"].get("clusterIP"), "None")

    def test_network_policies(self):
        docs = render()
        policies = [doc for doc in docs if doc.get("kind") == "NetworkPolicy"]
        rendered = yaml.dump(policies)
        self.assertNotIn("0.0.0.0/0", rendered)

        default_deny = [
            policy
            for policy in policies
            if policy["spec"].get("podSelector") == {}
            and policy["spec"].get("policyTypes") == ["Ingress", "Egress"]
            and policy["spec"].get("ingress") == []
        ]
        self.assertEqual(len(default_deny), 1)
        egress = default_deny[0]["spec"]["egress"]
        self.assertEqual(len(egress), 1)
        rule = egress[0]
        self.assertEqual(
            rule["to"],
            [
                {
                    "namespaceSelector": {
                        "matchLabels": {"kubernetes.io/metadata.name": "kube-system"}
                    },
                    "podSelector": {"matchLabels": {"k8s-app": "kube-dns"}},
                }
            ],
        )
        self.assertEqual(
            rule["ports"],
            [
                {"protocol": "UDP", "port": 53},
                {"protocol": "TCP", "port": 53},
            ],
        )

        selected = [
            policy
            for policy in policies
            if policy["spec"].get("podSelector", {}).get("matchLabels", {}).get(
                "app.kubernetes.io/name"
            )
            == "victorialogs"
        ]
        self.assertEqual(len(selected), 1)
        ingress = selected[0]["spec"]["ingress"]
        self.assertEqual(len(ingress), 1)
        sources = ingress[0]["from"]
        # fluent-bit writes the lines; error-detector tails them back out.
        self.assertEqual(len(sources), 2)
        self.assertEqual(
            [s["podSelector"]["matchLabels"]["app.kubernetes.io/name"] for s in sources],
            ["fluent-bit", "error-detector"],
        )
        for source in sources:
            self.assertEqual(
                set(source["podSelector"]["matchLabels"]),
                {"app.kubernetes.io/name"},
                "each ingress source must be selected by name alone, never by a namespace wildcard",
            )
        self.assertEqual(ingress[0]["ports"], [{"protocol": "TCP", "port": 9428}])

    def test_image_is_pinned(self):
        docs = render()
        sts = _by_kind_name(docs, "StatefulSet", "victorialogs")
        image = sts["spec"]["template"]["spec"]["containers"][0]["image"]
        tag = image.rsplit(":", 1)[-1]
        self.assertNotEqual(tag, "latest")
        self.assertRegex(tag, r"\d")

CONTRACT_LOG_SCHEMA = os.path.join(BASE_DIR, "contracts", "CONTRACT_LOG_SCHEMA.md")
PREFLIGHT = os.path.join(BASE_DIR, "scripts", "platform-preflight.py")
CAPACITY = os.path.join(BASE_DIR, "tests", "preflight", "fixtures", "valid_capacity.yaml")


def _configmap_text(docs: list[dict], key: str) -> str:
    cm = _by_kind_name(docs, "ConfigMap", "fluent-bit-config")
    text = cm.get("data", {}).get(key, "")
    if not text:
        raise AssertionError(f"fluent-bit-config missing {key}")
    return text


def _conf_value(text: str, key: str) -> str:
    match = re.search(rf"^\s*{re.escape(key)}\s+(.+)$", text, re.MULTILINE)
    if not match:
        raise AssertionError(f"missing {key}")
    return match.group(1).strip()


def _pod_path_regex(parsers: str) -> str:
    blocks = re.split(r"(?=^\s*\[PARSER\])", parsers, flags=re.MULTILINE)
    for block in blocks:
        if re.search(r"^\s*Name\s+pod_path\s*$", block, re.MULTILINE):
            return _conf_value(block, "Regex")
    raise AssertionError("pod_path parser missing")


class TestFluentBitCollector(unittest.TestCase):
    def test_only_one_readonly_hostpath_and_hardening(self):
        docs = render()
        ds = _by_kind_name(docs, "DaemonSet", "fluent-bit")
        pod = ds["spec"]["template"]["spec"]
        volumes = pod["volumes"]
        mounts = pod["containers"][0]["volumeMounts"]
        host_vols = {vol["name"]: vol["hostPath"] for vol in volumes if "hostPath" in vol}
        self.assertEqual(len(host_vols), 2)
        self.assertEqual(host_vols["varlogpods"], {"path": "/var/log/pods", "type": "Directory"})
        self.assertEqual(
            host_vols["state"],
            {"path": "/var/lib/fluent-bit-state", "type": "DirectoryOrCreate"},
        )
        mount_map = {mnt["name"]: mnt for mnt in mounts}
        self.assertIs(mount_map["varlogpods"]["readOnly"], True)
        self.assertEqual(mount_map["varlogpods"]["mountPath"], "/var/log/pods")
        self.assertIs(mount_map["state"]["readOnly"], False)
        self.assertEqual(mount_map["state"]["mountPath"], "/state")

        pvc_vols = [vol for vol in volumes if "persistentVolumeClaim" in vol]
        self.assertEqual(len(pvc_vols), 0)
        pvc_docs = [
            doc
            for doc in docs
            if doc.get("kind") == "PersistentVolumeClaim"
            and doc.get("metadata", {}).get("name") == "fluent-bit-state"
        ]
        self.assertEqual(len(pvc_docs), 0)

        tolerations = pod.get("tolerations") or []
        self.assertIn(
            {
                "key": "cloud.google.com/gke-spot",
                "operator": "Equal",
                "value": "true",
                "effect": "NoSchedule",
            },
            tolerations,
        )

        for vol in volumes:
            if "hostPath" in vol:
                continue
            self.assertTrue("emptyDir" in vol or "configMap" in vol, vol)

        self.assertEqual(pod["serviceAccountName"], "fluent-bit")
        self.assertIs(pod["automountServiceAccountToken"], False)
        sa = _by_kind_name(docs, "ServiceAccount", "fluent-bit")
        self.assertIs(sa["automountServiceAccountToken"], False)

        container = pod["containers"][0]
        sc = container["securityContext"]
        self.assertEqual(sc["runAsUser"], 0)
        self.assertIs(sc["allowPrivilegeEscalation"], False)
        self.assertIs(sc["readOnlyRootFilesystem"], True)
        self.assertEqual(sc["capabilities"]["drop"], ["ALL"])
        self.assertNotIn("add", sc["capabilities"])

        node_env = [env for env in container["env"] if env["name"] == "NODE_NAME"]
        self.assertEqual(len(node_env), 1)
        self.assertEqual(node_env[0]["valueFrom"]["fieldRef"]["fieldPath"], "spec.nodeName")

        resources = container["resources"]
        self.assertEqual(resources["requests"], {"cpu": "50m", "memory": "64Mi"})
        self.assertEqual(resources["limits"], {"cpu": "50m", "memory": "64Mi"})

        rendered = yaml.dump(ds) + yaml.dump(sa)
        self.assertNotIn("secretKeyRef", rendered)
        self.assertNotRegex(rendered, r"(?i)\bsecret\b")

    def test_tail_glob_matches_only_project_namespaces(self):
        docs = render()
        path = _conf_value(_configmap_text(docs, "fluent-bit.conf"), "Path")
        self.assertTrue(
            fnmatch.fnmatch("/var/log/pods/proj-pn_backend-7d9f_3f2c-aa/backend/0.log", path)
        )
        self.assertTrue(
            fnmatch.fnmatch("/var/log/pods/proj-health_web-1_uid-1/web/0.log", path)
        )
        for prefix in (
            "identity_",
            "vault_",
            "platform_",
            "kube-system_",
            "flux-system_",
            "cert-manager_",
            "governance_",
            "observability_",
        ):
            self.assertFalse(fnmatch.fnmatch(f"/var/log/pods/{prefix}x_uid/c/0.log", path), prefix)
            self.assertFalse(fnmatch.fnmatch(f"{prefix}x_uid/c/0.log", path), prefix)
        self.assertFalse(fnmatch.fnmatch("/var/log/pods/projx_x_uid/c/0.log", path))
        self.assertFalse(fnmatch.fnmatch("projx_x_uid/c/0.log", path))

    def test_path_regex_extracts_fields(self):
        docs = render()
        raw = _pod_path_regex(_configmap_text(docs, "parsers.conf"))
        self.assertIn("(?<ns>", raw)
        self.assertNotIn("(?P<", raw)
        compiled = re.compile(raw.replace("(?<", "(?P<"))
        match = compiled.match(
            "/var/log/pods/proj-pn_openbao-0.my-set_3f2c-aa-bb/backend/12.log"
        )
        self.assertIsNotNone(match)
        self.assertEqual(match.group("ns"), "proj-pn")
        self.assertEqual(match.group("pod"), "openbao-0.my-set")
        self.assertEqual(match.group("uid"), "3f2c-aa-bb")
        self.assertEqual(match.group("container"), "backend")

    def test_output_to_victorialogs_and_bounded_memory(self):
        docs = render()
        conf = _configmap_text(docs, "fluent-bit.conf")
        self.assertIn("Host victorialogs.observability.svc.cluster.local", conf)
        self.assertIn("Port 9428", conf)
        expected_uri = "URI /insert/jsonline?_stream_fields=k8s.namespace.name,k8s.pod.name,k8s.container.name&_msg_field=msg&_time_field=_time"
        self.assertIn(expected_uri, conf)
        self.assertIn("Format json_lines", conf)
        self.assertIn("json_date_format iso8601", conf)
        self.assertIn("json_date_key _time", conf)
        self.assertIn("Mem_Buf_Limit 5MB", conf)
        self.assertIn("DB /state/tail.db", conf)
        self.assertIn("storage.path /state/chunks", conf)
        self.assertIn("storage.type filesystem", conf)
        self.assertRegex(conf, r"Retry_Limit\s+5\b")
        self.assertIn("Log_Level warn", conf)

        # Output blocks check: exactly two http outputs for main (ProjectID 0, 192M) and quarantine (ProjectID 1, 64M)
        output_blocks = re.findall(r"\[OUTPUT\][\s\S]*?(?=\n\s*\[|$)", conf)
        self.assertEqual(len(output_blocks), 2, "expected exactly two http outputs (main and quarantine)")
        main_out = [b for b in output_blocks if re.search(r"Match\s+proj\.\*", b) and "ProjectID 0" in b]
        quarantine_out = [b for b in output_blocks if re.search(r"Match\s+quarantine\.\*", b) and "ProjectID 1" in b]
        self.assertEqual(len(main_out), 1, "expected one main http output with Match proj.* and ProjectID 0")
        self.assertEqual(len(quarantine_out), 1, "expected one quarantine http output with Match quarantine.* and ProjectID 1")
        self.assertIn("storage.total_limit_size 192M", main_out[0])
        self.assertIn("storage.total_limit_size 64M", quarantine_out[0])
        for b in (main_out[0], quarantine_out[0]):
            self.assertIn("AccountID 0", b)
            self.assertIn(expected_uri, b)

        # Filter order invariants (T018, T051, collector-pipeline.md)
        filter_blocks = re.findall(r"\[FILTER\][\s\S]*?(?=\n\s*\[|$)", conf)
        # 1st filter: multiline
        self.assertIn("Name multiline", filter_blocks[0])
        self.assertIn("multiline.parser go, python, java", filter_blocks[0])
        self.assertIn("buffer on", filter_blocks[0])
        self.assertIn("emitter_storage.type filesystem", filter_blocks[0])

        # Filters before rewrite_tag must match proj.*
        for fb in filter_blocks[:-1]:
            if "Name modify" in fb and "Remove _route" in fb:
                continue
            self.assertIn("Match proj.*", fb, f"filter must match proj.*: {fb}")

        # Final modify filter matches * to strip _route from both main and quarantine
        last_filter = filter_blocks[-1]
        self.assertIn("Name modify", last_filter)
        self.assertIn("Match *", last_filter)
        self.assertIn("Remove _route", last_filter)

        # rewrite_tag filter
        rewrite_blocks = [b for b in filter_blocks if "Name rewrite_tag" in b]
        self.assertEqual(len(rewrite_blocks), 1)
        self.assertIn("Rule $_route ^quarantine$ quarantine.$TAG false", rewrite_blocks[0])
        self.assertIn("Emitter_Storage.type filesystem", rewrite_blocks[0])

        # Lua filter comes after JSON parser filter
        json_idx = next(i for i, b in enumerate(filter_blocks) if "Parser json" in b)
        lua_idx = next(i for i, b in enumerate(filter_blocks) if "Name lua" in b)
        self.assertLess(json_idx, lua_idx, "Lua filter must come after JSON parser filter")
        self.assertIn("Preserve_Key On", filter_blocks[json_idx])

        # No grep or throttle filter
        for fb in filter_blocks:
            self.assertNotIn("Name grep", fb)
            self.assertNotIn("Name throttle", fb)

        ds = _by_kind_name(docs, "DaemonSet", "fluent-bit")
        pod = ds["spec"]["template"]["spec"]
        state_vol = next(vol for vol in pod["volumes"] if vol["name"] == "state")
        self.assertEqual(
            state_vol["hostPath"],
            {"path": "/var/lib/fluent-bit-state", "type": "DirectoryOrCreate"},
        )
        mount = next(mnt for mnt in pod["containers"][0]["volumeMounts"] if mnt["name"] == "state")
        self.assertEqual(mount["mountPath"], "/state")
        self.assertIs(mount["readOnly"], False)

    def test_fluent_bit_network_policy(self):
        docs = render()
        policies = [doc for doc in docs if doc.get("kind") == "NetworkPolicy"]
        selected = [
            policy
            for policy in policies
            if policy["spec"].get("podSelector", {}).get("matchLabels", {}).get("app.kubernetes.io/name")
            == "fluent-bit"
        ]
        self.assertEqual(len(selected), 1)
        rendered = yaml.dump(selected[0])
        self.assertNotIn("0.0.0.0/0", rendered)
        egress = selected[0]["spec"]["egress"]
        self.assertEqual(len(egress), 2)
        dns = next(rule for rule in egress if any(port.get("port") == 53 for port in rule.get("ports", [])))
        self.assertEqual(
            dns["to"],
            [
                {
                    "namespaceSelector": {
                        "matchLabels": {"kubernetes.io/metadata.name": "kube-system"}
                    },
                    "podSelector": {"matchLabels": {"k8s-app": "kube-dns"}},
                }
            ],
        )
        self.assertEqual(
            dns["ports"],
            [{"protocol": "UDP", "port": 53}, {"protocol": "TCP", "port": 53}],
        )
        store = next(rule for rule in egress if rule is not dns)
        self.assertEqual(
            store["to"],
            [{"podSelector": {"matchLabels": {"app.kubernetes.io/name": "victorialogs"}}}],
        )
        self.assertEqual(store["ports"], [{"protocol": "TCP", "port": 9428}])

    def test_platform_reservation_delta(self):
        import tempfile

        docs = render()
        text = yaml.dump_all(docs)
        with tempfile.NamedTemporaryFile("w", suffix=".yaml", delete=False) as tmp:
            tmp.write(text)
            path = tmp.name
        try:
            res = subprocess.run(
                [
                    "python3",
                    PREFLIGHT,
                    "--environment",
                    "test-env",
                    "--candidate",
                    path,
                    "--capacity",
                    CAPACITY,
                    "--json",
                ],
                capture_output=True,
                text=True,
            )
        finally:
            os.remove(path)
        data = json.loads(res.stdout) if res.stdout.strip() else {}
        self.assertEqual(data.get("validationStatus"), "PASSED", data.get("rejectionReason"))
        # fluent-bit + victorialogs + error-detector (50m/64Mi).
        self.assertEqual(data.get("platformReservations", {}).get("cpuMillicores"), 350)
        self.assertEqual(data.get("platformReservations", {}).get("memoryMib"), 1024)

    def test_record_fields_match_log_schema_contract(self):
        docs = render()
        conf = _configmap_text(docs, "fluent-bit.conf")
        lua = _configmap_text(docs, "log_contract.lua")
        with open(CONTRACT_LOG_SCHEMA, encoding="utf-8") as fh:
            contract = fh.read()
        envelope = contract.split("## 1. Platform envelope", 1)[1].split("## 2.", 1)[0]
        rows = re.findall(r"^\|\s*`([a-z0-9._]+)`\s*\|\s*([^|]+)\s*\|\s*([^|]+)\s*\|$", envelope, re.MULTILINE)
        record_fields = {name for name, _src, _role in rows}
        stream_fields = {name for name, _src, role in rows if role.strip() == "stream"}

        # Platform fields must match contract §1 table
        expected_platform_fields = {
            "k8s.namespace.name",
            "k8s.pod.name",
            "k8s.container.name",
            "k8s.node.name",
            "k8s.pod.uid",
            "log.iostream",
            "observed_time",
        }
        self.assertEqual(record_fields, expected_platform_fields)

        # Stream fields in output URI
        uri = _conf_value(conf, "URI")
        stream_match = re.search(r"_stream_fields=([^&]+)", uri)
        rendered_stream = set(stream_match.group(1).split(",")) if stream_match else set()
        self.assertEqual(stream_fields, rendered_stream)

        # Check that log_contract.lua sets every platform field
        for field in record_fields:
            self.assertIn(f'record["{field}"]', lua, f"log_contract.lua must set {field}")

        # Message and time field flags in URI
        self.assertIn("_msg_field=msg", uri)
        self.assertIn("_time_field=_time", uri)

    def test_reserved_keys_stripped_before_platform_fields(self):
        docs = render()
        lua = _configmap_text(docs, "log_contract.lua")
        strip_pos = lua.find("for _, key in ipairs(RESERVED)")
        assign_pos = lua.find('record["k8s.')
        self.assertGreater(strip_pos, 0, "loop stripping RESERVED must exist in log_contract.lua")
        self.assertGreater(assign_pos, 0, "platform assignment to record['k8s.*'] must exist")
        self.assertLess(
            strip_pos,
            assign_pos,
            "strip RESERVED must occur before assigning platform fields (FR-012)",
        )


class TestGkeObservabilityOverlay(unittest.TestCase):
    def test_gke_deletes_victorialogs_pauses_detector_and_ships_to_oci_kafka(self):
        docs = render(GKE_OBSERVABILITY_DIR)
        kind_names = {(d.get("kind"), d.get("metadata", {}).get("name")) for d in docs}
        self.assertNotIn(("StatefulSet", "victorialogs"), kind_names)
        self.assertNotIn(("Service", "victorialogs"), kind_names)
        self.assertNotIn(("NetworkPolicy", "victorialogs-ingress"), kind_names)
        self.assertIn(("PersistentVolumeClaim", "error-detector-state"), kind_names)

        detector = _by_kind_name(docs, "Deployment", "error-detector")
        self.assertEqual(detector["spec"].get("replicas"), 0)

        fb_netpol = _by_kind_name(docs, "NetworkPolicy", "fluent-bit-egress")
        egress = fb_netpol["spec"]["egress"]
        self.assertEqual(len(egress), 2)
        kafka_rule = next(r for r in egress if any(p.get("port") == 9092 for p in r.get("ports", [])))
        cidrs = {peer["ipBlock"]["cidr"] for peer in kafka_rule["to"]}
        self.assertEqual(cidrs, {"10.200.0.11/32", "10.200.0.12/32", "10.200.0.13/32"})

        conf = _configmap_text(docs, "fluent-bit.conf")
        output_blocks = re.findall(r"\[OUTPUT\][\s\S]*?(?=\n\s*\[|$)", conf)
        self.assertEqual(len(output_blocks), 2)
        for block in output_blocks:
            self.assertIn("Name kafka", block)
            self.assertIn("Brokers 10.200.0.11:9092,10.200.0.12:9092,10.200.0.13:9092", block)
            self.assertIn("rdkafka.request.required.acks -1", block)
            self.assertIn("rdkafka.enable.idempotence true", block)
            self.assertIn("Retry_Limit False", block)
        valid_out = [b for b in output_blocks if "Topics logs.proj.valid" in b]
        quar_out = [b for b in output_blocks if "Topics logs.proj.quarantine" in b]
        self.assertEqual(len(valid_out), 1)
        self.assertEqual(len(quar_out), 1)
        self.assertIn("storage.total_limit_size 192M", valid_out[0])
        self.assertIn("storage.total_limit_size 64M", quar_out[0])


class TestLayerWiring(unittest.TestCase):
    def test_observability_layer(self):
        for env in ("local-k3s", "vps-k3s"):
            path = os.path.join(BASE_DIR, "clusters", env, "layers.yaml")
            with open(path, encoding="utf-8") as handle:
                docs = [doc for doc in yaml.safe_load_all(handle) if doc]
            matches = [
                doc
                for doc in docs
                if doc.get("kind") == "Kustomization"
                and doc.get("metadata", {}).get("name") == "observability"
                and doc.get("metadata", {}).get("namespace") == "flux-system"
            ]
            self.assertEqual(len(matches), 1, env)
            spec = matches[0]["spec"]
            self.assertEqual(spec["path"], "./deploy/platform/observability")
            self.assertIs(spec["wait"], True)
            self.assertIs(spec["prune"], True)
            self.assertEqual(
                [item["name"] for item in spec["dependsOn"]],
                ["cni", "governance"],
            )
            checks = {
                (item["kind"], item["name"], item["namespace"])
                for item in spec["healthChecks"]
            }
            self.assertIn(("DaemonSet", "fluent-bit", "observability"), checks)
            self.assertIn(("StatefulSet", "victorialogs", "observability"), checks)

        gke_path = os.path.join(BASE_DIR, "clusters", "gke", "layers.yaml")
        with open(gke_path, encoding="utf-8") as handle:
            gke_docs = [doc for doc in yaml.safe_load_all(handle) if doc]
        gke_obs = next(
            doc
            for doc in gke_docs
            if doc.get("kind") == "Kustomization"
            and doc.get("metadata", {}).get("name") == "observability"
        )
        gke_spec = gke_obs["spec"]
        self.assertEqual(gke_spec["path"], "./clusters/gke/observability")
        self.assertEqual([item["name"] for item in gke_spec["dependsOn"]], ["governance"])
        gke_checks = {
            (item["kind"], item["name"], item["namespace"])
            for item in gke_spec["healthChecks"]
        }
        self.assertEqual(gke_checks, {("DaemonSet", "fluent-bit", "observability")})

    def test_manual_change_policy_lists_observability(self):
        path = os.path.join(
            BASE_DIR, "deploy", "platform", "governance", "manual-change-policy.yaml"
        )
        with open(path, encoding="utf-8") as handle:
            docs = [doc for doc in yaml.safe_load_all(handle) if doc]
        bindings = [
            doc
            for doc in docs
            if doc.get("kind") == "ValidatingAdmissionPolicyBinding"
            and doc.get("metadata", {}).get("name") == "gitops-managed-namespaces"
        ]
        self.assertEqual(len(bindings), 1)
        values = []
        for expr in bindings[0]["spec"]["matchResources"]["namespaceSelector"][
            "matchExpressions"
        ]:
            if expr.get("key") == "kubernetes.io/metadata.name":
                values.extend(expr.get("values", []))
        self.assertIn("observability", values)

class TestErrorDetector(unittest.TestCase):
    """Contracts for Deployment/error-detector (WS1 Task 13).

    The detector is the only workload that both reads the whole project log
    stream and can post to Discord, so its blast radius is what these tests
    bound: no host access, no API token, guaranteed-QoS so it cannot be evicted
    mid-flush, and egress that reaches exactly three places.
    """

    @classmethod
    def setUpClass(cls):
        cls.docs = render()
        cls.deployment = _by_kind_name(cls.docs, "Deployment", "error-detector")
        cls.pod_spec = cls.deployment["spec"]["template"]["spec"]
        cls.containers = cls.pod_spec["containers"]
        cls.detector = next(c for c in cls.containers if c["name"] == "error-detector")

    def test_detector_has_no_hostpath_and_no_sa_token(self):
        self.assertIs(
            self.pod_spec.get("automountServiceAccountToken"),
            False,
            "the detector needs no API access; a mounted token is a credential to steal",
        )
        volumes = self.pod_spec.get("volumes") or []
        for volume in volumes:
            self.assertNotIn("hostPath", volume, f"volume {volume.get('name')} uses hostPath")

    def test_detector_guaranteed_qos(self):
        res = self.detector["resources"]
        self.assertEqual(res["requests"]["cpu"], res["limits"]["cpu"], "CPU requests must equal limits")
        self.assertEqual(
            res["requests"]["memory"], res["limits"]["memory"], "memory requests must equal limits"
        )
        self.assertEqual(res["requests"]["cpu"], "50m")
        self.assertEqual(res["requests"]["memory"], "64Mi")

    def test_detector_image_pinned_by_digest(self):
        image = self.detector["image"]
        self.assertRegex(
            image,
            r"@sha256:[0-9a-f]{64}$",
            "the detector image must be pinned by digest, never a tag",
        )

    def test_detector_egress_only_vlogs_notify_investigator_dns(self):
        netpol = _by_kind_name(self.docs, "NetworkPolicy", "error-detector-egress")
        peers = [
            peer
            for rule in netpol["spec"].get("egress") or []
            for peer in rule.get("to") or []
        ]

        # DNS is the one peer that may be selected by namespace label alone.
        dns = [
            p
            for p in peers
            if p.get("namespaceSelector", {})
            .get("matchLabels", {})
            .get("kubernetes.io/metadata.name")
            == "kube-system"
        ]
        self.assertEqual(len(dns), 1, "the detector must reach kube-dns to resolve service names")

        names = set()
        for peer in peers:
            if peer in dns:
                continue
            ns = (peer.get("namespaceSelector") or {}).get("matchLabels", {}).get(
                "kubernetes.io/metadata.name"
            )
            pod = (peer.get("podSelector") or {}).get("matchLabels", {}).get(
                "app.kubernetes.io/name"
            )
            # An empty namespaceSelector means the pod's own namespace, which is
            # observability; anything else must name its namespace explicitly.
            names.add(f"{ns or 'observability'}/{pod}")

        self.assertEqual(
            names,
            {
                "observability/victorialogs",
                "vault/vault-notify-proxy",
                "observability/error-investigator",
            },
            f"unexpected egress peers: {sorted(names)}",
        )
        for peer in peers:
            self.assertNotIn("ipBlock", peer, "the detector must not reach any CIDR")

    def test_victorialogs_ingress_allows_detector(self):
        netpol = _by_kind_name(self.docs, "NetworkPolicy", "victorialogs-ingress")
        names = {
            peer.get("podSelector", {}).get("matchLabels", {}).get("app.kubernetes.io/name")
            for rule in netpol["spec"]["ingress"]
            for peer in rule.get("from") or []
        }
        self.assertIn(
            "error-detector",
            names,
            "the detector tails victorialogs; without this it cannot read a single line",
        )

    def test_layer_health_checks_include_detector(self):
        for env in ("local-k3s", "vps-k3s"):
            path = os.path.join(BASE_DIR, "clusters", env, "layers.yaml")
            with open(path, encoding="utf-8") as handle:
                docs = [doc for doc in yaml.safe_load_all(handle) if doc]
            layer = next(
                doc
                for doc in docs
                if doc.get("kind") == "Kustomization"
                and doc.get("metadata", {}).get("name") == "observability"
            )
            checks = {
                (item["kind"], item["name"], item["namespace"])
                for item in layer["spec"]["healthChecks"]
            }
            self.assertIn(
                ("Deployment", "error-detector", "observability"),
                checks,
                f"{env}: the detector must gate the layer health check",
            )

    def test_investigator_url_empty_until_ws3(self):
        env = {
            item["name"]: item.get("value")
            for item in self.detector.get("env") or []
        }
        self.assertEqual(
            env.get("INVESTIGATOR_URL", ""),
            "",
            "workstream 3 has not landed; the handoff must stay disabled by config",
        )


if __name__ == "__main__":
    unittest.main()
