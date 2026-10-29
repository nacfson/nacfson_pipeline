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

import yaml

BASE_DIR = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
OBSERVABILITY_DIR = os.path.join(BASE_DIR, "deploy", "platform", "observability")


def render() -> list[dict]:
    """Run `kubectl kustomize deploy/platform/observability` and return documents."""
    result = subprocess.run(
        ["kubectl", "kustomize", OBSERVABILITY_DIR],
        check=True,
        capture_output=True,
        text=True,
    )
    docs = [doc for doc in yaml.safe_load_all(result.stdout) if doc]
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
        self.assertEqual(resources["requests"]["cpu"], "100m")
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
        host_vols = [vol for vol in volumes if "hostPath" in vol]
        self.assertEqual(len(host_vols), 1)
        self.assertEqual(host_vols[0]["hostPath"]["path"], "/var/log/pods")
        host_mounts = [mnt for mnt in mounts if mnt["name"] == host_vols[0]["name"]]
        self.assertEqual(len(host_mounts), 1)
        self.assertIs(host_mounts[0]["readOnly"], True)

        pvc_vols = [vol for vol in volumes if "persistentVolumeClaim" in vol]
        self.assertEqual(len(pvc_vols), 1)
        self.assertEqual(pvc_vols[0]["persistentVolumeClaim"]["claimName"], "fluent-bit-state")

        for vol in volumes:
            if vol in host_vols or vol in pvc_vols:
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

        pvc = _by_kind_name(docs, "PersistentVolumeClaim", "fluent-bit-state")
        self.assertEqual(pvc["spec"]["accessModes"], ["ReadWriteOnce"])
        self.assertEqual(pvc["spec"]["resources"]["requests"]["storage"], "1Gi")

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
        self.assertIn("(?<project>", raw)
        self.assertNotIn("(?P<", raw)
        compiled = re.compile(raw.replace("(?<", "(?P<"))
        match = compiled.match(
            "/var/log/pods/proj-pn_openbao-0.my-set_3f2c-aa-bb/backend/12.log"
        )
        self.assertIsNotNone(match)
        self.assertEqual(match.group("project"), "proj-pn")
        self.assertEqual(match.group("pod"), "openbao-0.my-set")
        self.assertEqual(match.group("pod_uid"), "3f2c-aa-bb")
        self.assertEqual(match.group("container"), "backend")

    def test_output_to_victorialogs_and_bounded_memory(self):
        docs = render()
        conf = _configmap_text(docs, "fluent-bit.conf")
        self.assertIn("Host victorialogs.observability.svc.cluster.local", conf)
        self.assertIn("Port 9428", conf)
        self.assertIn(
            "URI /insert/jsonline?_stream_fields=project,container,pod&_msg_field=message&_time_field=time",
            conf,
        )
        self.assertIn("Format json_lines", conf)
        self.assertIn("json_date_format iso8601", conf)
        self.assertIn("json_date_key time", conf)
        self.assertIn("Mem_Buf_Limit", conf)
        self.assertIn("DB /state/tail.db", conf)
        self.assertRegex(conf, r"Retry_Limit\s+5\b")
        self.assertIn("Log_Level warn", conf)

        ds = _by_kind_name(docs, "DaemonSet", "fluent-bit")
        pod = ds["spec"]["template"]["spec"]
        pvc_vol = next(vol for vol in pod["volumes"] if "persistentVolumeClaim" in vol)
        self.assertEqual(pvc_vol["persistentVolumeClaim"]["claimName"], "fluent-bit-state")
        self.assertNotIn("emptyDir", pvc_vol)
        mount = next(mnt for mnt in pod["containers"][0]["volumeMounts"] if mnt["name"] == pvc_vol["name"])
        self.assertTrue("/state/tail.db".startswith(mount["mountPath"].rstrip("/") + "/") or mount["mountPath"] == "/state")

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
        self.assertEqual(data.get("platformReservations", {}).get("cpuMillicores"), 400)
        self.assertEqual(data.get("platformReservations", {}).get("memoryMib"), 1024)

    def test_record_fields_match_log_schema_contract(self):
        docs = render()
        conf = _configmap_text(docs, "fluent-bit.conf")
        parsers = _configmap_text(docs, "parsers.conf")
        with open(CONTRACT_LOG_SCHEMA, encoding="utf-8") as fh:
            contract = fh.read()
        envelope = contract.split("## 1. Platform envelope", 1)[1].split("## 2.", 1)[0]
        rows = re.findall(r"^\| `([a-z_]+)` \| .* \| ([^|]+) \|$", envelope, re.MULTILINE)
        record_fields = {name for name, _role in rows}
        stream_fields = {name for name, role in rows if role.strip() == "stream"}
        regex = _pod_path_regex(parsers)
        groups = set(re.findall(r"\(\?<([a-z_]+)>", regex))
        record_line = _conf_value(conf, "Record")
        rename = re.search(r"^\s*Rename\s+(\S+)\s+(\S+)\s*$", conf, re.MULTILINE)
        rendered_fields = set(groups)
        rendered_fields.add(record_line.split()[0])
        rendered_fields.add(_conf_value(conf, "json_date_key"))
        if rename:
            rendered_fields.add(rename.group(2))
        uri = _conf_value(conf, "URI")
        stream = re.search(r"_stream_fields=([^&]+)", uri)
        rendered_stream = set(stream.group(1).split(",")) if stream else set()
        parses_message = "Key_Name message" in conf or "Parser message" in conf
        self.assertEqual(
            (record_fields, stream_fields, False),
            (rendered_fields, rendered_stream, parses_message),
            CONTRACT_LOG_SCHEMA,
        )


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
