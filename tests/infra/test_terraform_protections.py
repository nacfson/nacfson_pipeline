import importlib.util
import json
import re
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
INFRA = REPO_ROOT / "infra" / "gcp"
RUNBOOK = REPO_ROOT / "docs" / "runbooks" / "gke-terraform.md"

_spec = importlib.util.spec_from_file_location(
    "check_gcp_shared_values", REPO_ROOT / "scripts" / "check-gcp-shared-values.py")
_checker = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(_checker)
strip_hcl_comments = _checker.strip_hcl_comments

RESOURCE_RE = re.compile(r'^resource\s+"([^"]+)"\s+"([^"]+)"\s*\{', re.MULTILINE)


def load_resources(root: Path) -> dict[tuple[str, str], str]:
    resources: dict[tuple[str, str], str] = {}
    for tf in sorted(root.glob("*.tf")):
        text = strip_hcl_comments(tf.read_text(encoding="utf-8"))
        for m in RESOURCE_RE.finditer(text):
            depth, i = 1, m.end()
            while depth and i < len(text):
                depth += {"{": 1, "}": -1}.get(text[i], 0)
                i += 1
            resources[(m.group(1), m.group(2))] = text[m.end() : i - 1]
    return resources


def has_prevent_destroy(body: str) -> bool:
    return re.search(r"lifecycle\s*\{[^}]*prevent_destroy\s*=\s*true", body, re.S) is not None


class TerraformProtectionTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.bootstrap = load_resources(INFRA / "bootstrap")
        cls.project = load_resources(INFRA / "project")
        cls.gke = load_resources(INFRA / "gke")

    def test_cluster_is_protected(self):
        body = self.gke[("google_container_cluster", "this")]
        self.assertRegex(body, r"deletion_protection\s*=\s*true")
        self.assertTrue(has_prevent_destroy(body))

    def test_long_lived_resources_have_prevent_destroy(self):
        for roots, key in [
            (self.bootstrap, ("google_storage_bucket", "tfstate")),
            (self.gke, ("google_kms_key_ring", "vault")),
            (self.gke, ("google_kms_crypto_key", "openbao_unseal")),
            (self.gke, ("google_artifact_registry_repository", "public")),
        ]:
            with self.subTest(resource=key):
                self.assertIn(key, roots)
                self.assertTrue(has_prevent_destroy(roots[key]), f"{key} lacks prevent_destroy")

    def test_ingress_address_can_be_removed(self):
        body = self.gke[("google_compute_address", "ingress")]
        self.assertNotIn("prevent_destroy", body)

    def test_project_services_never_disable_on_destroy(self):
        services = [b for (t, _), b in self.project.items() if t == "google_project_service"]
        self.assertTrue(services)
        for body in services:
            self.assertRegex(body, r"disable_on_destroy\s*=\s*false")

    def test_only_the_web_firewall_rule(self):
        rules = [k for k in self.gke if k[0] == "google_compute_firewall"]
        self.assertEqual(rules, [("google_compute_firewall", "web_ingress")])
        body = self.gke[("google_compute_firewall", "web_ingress")]
        self.assertRegex(body, r'direction\s*=\s*"INGRESS"')
        self.assertRegex(body, r'protocol\s*=\s*"tcp"')
        self.assertRegex(body, r'ports\s*=\s*\["80",\s*"443"\]')
        self.assertIn("google_compute_address.ingress.address", body)
        self.assertIn("google_service_account.gke_nodes.email", body)
        self.assertNotRegex(body, r"\bdeny\b")

    def test_gke_does_not_manage_lb_firewall(self):
        body = self.gke[("google_container_cluster", "this")]
        self.assertRegex(body, r"disable_l4_lb_firewall_reconciliation\s*=\s*true")

    def test_commented_out_protection_does_not_count(self):
        body = strip_hcl_comments("lifecycle {\n  /* prevent_destroy = true */\n  # prevent_destroy = true\n}\n")
        self.assertFalse(has_prevent_destroy(body))

    def test_public_api_without_authorized_networks(self):
        body = self.gke[("google_container_cluster", "this")]
        self.assertNotIn("master_authorized_networks_config", body)



class OciTerraformTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.oci = load_resources(REPO_ROOT / "infra" / "oci" / "terraform")

    def test_reserved_broker_ips_are_protected(self):
        body = self.oci[("oci_core_public_ip", "broker")]
        self.assertRegex(body, r'lifetime\s*=\s*"RESERVED"')
        self.assertTrue(has_prevent_destroy(body))

    def test_only_wireguard_is_open_to_the_internet(self):
        body = self.oci[("oci_core_security_list", "kafka")]
        ingress = re.findall(r"ingress_security_rules\s*\{(.*?)\n  \}", body, re.S)
        self.assertEqual(len(ingress), 4)
        public = [r for r in ingress if '"0.0.0.0/0"' in r]
        self.assertEqual(len(public), 1)
        self.assertRegex(public[0], r'protocol\s*=\s*"17"')
        self.assertIn("local.wireguard_port", public[0])
        internal = [r for r in ingress if "local.subnet_cidr" in r]
        self.assertEqual(len(internal), 2)
        ports = sorted(re.findall(r"min\s*=\s*(\d+)", " ".join(internal)))
        self.assertEqual(ports, ["22", "9092"])
        ssh = next(r for r in internal if re.search(r"min\s*=\s*22\b", r))
        self.assertRegex(ssh, r"max\s*=\s*22\b")
        self.assertNotIn("0.0.0.0/0", ssh)
        loki = [r for r in ingress if '"10.200.0.11/32"' in r]
        self.assertEqual(len(loki), 1)
        self.assertRegex(loki[0], r'protocol\s*=\s*"6"')
        self.assertRegex(loki[0], r"min\s*=\s*3100\b")
        self.assertRegex(loki[0], r"max\s*=\s*3100\b")
        self.assertNotIn("0.0.0.0/0", loki[0])

    def test_budget_guard_is_one_dollar_on_actual_spend(self):
        self.assertRegex(self.oci[("oci_budget_budget", "free_tier_guard")], r"amount\s*=\s*1\b")
        rule = self.oci[("oci_budget_alert_rule", "actual_spend")]
        self.assertRegex(rule, r'type\s*=\s*"ACTUAL"')
        self.assertRegex(rule, r"threshold\s*=\s*100\b")

    def test_runner_policy_is_bastion_sessions_only(self):
        body = self.oci[("oci_identity_policy", "kafka_runner")]
        verbs = re.findall(r"to (\w+) ([\w-]+) in", body)
        self.assertEqual(sorted({v for v, _ in verbs}), ["manage", "read", "use"])
        self.assertEqual([r for v, r in verbs if v == "manage"], ["bastion-session"])
        self.assertEqual([r for v, r in verbs if v == "use"], ["bastion"])
        self.assertIn("target.bastion.ocid = '${oci_bastion_bastion.kafka.id}'", body)
        self.assertNotIn("target.bastion-session.username", body)

    def test_instances_set_ssh_authorized_keys(self):
        variables = (REPO_ROOT / "infra" / "oci" / "terraform" / "variables.tf").read_text(encoding="utf-8")
        self.assertRegex(variables, r'variable "ssh_authorized_keys" \{')
        body = self.oci[("oci_core_instance", "node")]
        self.assertIn(r'ssh_authorized_keys = join("\n", var.ssh_authorized_keys)', body)
        self.assertNotIn("agent_config", body)

    def test_runner_api_key_is_created_only_when_set(self):
        variables = (REPO_ROOT / "infra" / "oci" / "terraform" / "variables.tf").read_text(encoding="utf-8")
        self.assertRegex(variables, r'variable "kafka_runner_api_public_key" \{[^}]*default\s*=\s*null')
        body = self.oci[("oci_identity_api_key", "kafka_runner")]
        self.assertRegex(body, r"count\s*=\s*var\.kafka_runner_api_public_key == null \? 0 : 1")

    def test_runner_user_sets_primary_email(self):
        variables = (REPO_ROOT / "infra" / "oci" / "terraform" / "variables.tf").read_text(encoding="utf-8")
        self.assertRegex(variables, r'variable "kafka_runner_email" \{[^}]*default\s*=\s*"[^"]+"')
        body = self.oci[("oci_identity_user", "kafka_runner")]
        self.assertRegex(body, r"\bemail\s*=\s*var\.kafka_runner_email\b")

    def test_instances_follow_declared_nodes(self):
        locals_text = (REPO_ROOT / "infra" / "oci" / "terraform" / "locals.tf").read_text(encoding="utf-8")
        self.assertIn('file("${path.module}/../../kafka/cluster.yaml")', locals_text)
        body = self.oci[("oci_core_instance", "node")]
        self.assertIn("for_each            = local.nodes", body)
        self.assertIn("private_ip       = each.value.address", body)
        self.assertRegex(locals_text, r"boot_volume_gbs\s*=\s*50")

class RunbookTargetTests(unittest.TestCase):

    def test_removal_targets_exist(self):
        gke = load_resources(INFRA / "gke")
        shared = json.loads((INFRA / "shared-values.json").read_text(encoding="utf-8"))
        pool_names = {p["name"] for p in shared["node_pools"]}

        targets = re.findall(r"-target='([^']+)'", RUNBOOK.read_text(encoding="utf-8"))
        self.assertGreaterEqual(len(targets), 8)
        for target in targets:
            with self.subTest(target=target):
                m = re.fullmatch(r'([a-z0-9_]+)\.([a-z0-9_]+)(?:\["([^"]+)"\])?', target)
                self.assertIsNotNone(m, f"unparseable target {target}")
                rtype, rname, key = m.groups()
                self.assertIn((rtype, rname), gke)
                if key is not None:
                    self.assertIn("for_each", gke[(rtype, rname)])
                    if rtype == "google_container_node_pool":
                        self.assertIn(key, pool_names)

    def test_protected_resources_are_not_targeted(self):
        targets = " ".join(re.findall(r"-target='([^']+)'", RUNBOOK.read_text(encoding="utf-8")))
        for kept in ("google_kms_key_ring", "google_kms_crypto_key", "google_artifact_registry_repository"):
            self.assertNotIn(kept, targets)


if __name__ == "__main__":
    unittest.main()
