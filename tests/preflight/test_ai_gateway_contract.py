import copy
import importlib.util
import os
import unittest

import yaml

BASE = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
PREFLIGHT = os.path.join(BASE, "scripts", "platform-preflight.py")
AI_PROXY_YAML = os.path.join(BASE, "deploy", "platform", "vault-proxies", "ai-proxy.yaml")
VAULT_NETPOL_YAML = os.path.join(BASE, "deploy", "platform", "vault", "network-policy.yaml")
PN_BACKEND_YAML = os.path.join(BASE, "deploy", "projects", "pn", "workloads", "backend-deployment.yaml")


def _preflight():
    spec = importlib.util.spec_from_file_location("platform_preflight", PREFLIGHT)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def _load_all(path):
    with open(path, encoding="utf-8") as fh:
        return [doc for doc in yaml.safe_load_all(fh) if doc is not None]


class TestAIGatewayPreflightContract(unittest.TestCase):
    def setUp(self):
        self.module = _preflight()
        self.platform_docs = _load_all(AI_PROXY_YAML) + _load_all(VAULT_NETPOL_YAML)
        self.projects_dict = {"proj-pn": _load_all(PN_BACKEND_YAML)}

    def test_valid_ai_gateway_and_pn_backend_passes(self):
        violations = self.module.check_ai_gateway_contract(self.projects_dict, self.platform_docs)
        self.assertEqual(violations, [])

    def test_project_container_with_api_key_or_direct_gemini_url_rejected(self):
        bad_projects = copy.deepcopy(self.projects_dict)
        containers = bad_projects["proj-pn"][0]["spec"]["template"]["spec"]["containers"]
        containers[0]["env"].append({"name": "GEMINI_API_KEY", "value": "AIzaSyForbidden"})
        v1 = self.module.check_ai_gateway_contract(bad_projects, self.platform_docs)
        self.assertTrue(any("API_KEY" in item["detail"] for item in v1))

        bad_projects2 = copy.deepcopy(self.projects_dict)
        for env_entry in bad_projects2["proj-pn"][0]["spec"]["template"]["spec"]["containers"][0]["env"]:
            if env_entry["name"] == "ENRICH_PRIMARY_BASE_URL":
                env_entry["value"] = "https://generativelanguage.googleapis.com/v1beta/openai"
        v2 = self.module.check_ai_gateway_contract(bad_projects2, self.platform_docs)
        self.assertTrue(any("ai-gateway.vault.svc" in item["detail"] for item in v2))

    def test_service_exposing_admin_port_8081_rejected(self):
        bad_platform = copy.deepcopy(self.platform_docs)
        for doc in bad_platform:
            if doc.get("kind") == "Service" and doc.get("metadata", {}).get("name") == "ai-gateway":
                doc["spec"]["ports"].append({"name": "admin", "port": 8081, "targetPort": 8081, "protocol": "TCP"})
        violations = self.module.check_ai_gateway_contract(self.projects_dict, bad_platform)
        self.assertTrue(any("8081" in item["detail"] or "port 8080 only" in item["detail"] for item in violations))

    def test_missing_startup_or_readiness_or_liveness_probe_rejected(self):
        for probe_field, expected_path in (
            ("startupProbe", "/startupz"),
            ("readinessProbe", "/readyz"),
            ("livenessProbe", "/healthz"),
        ):
            bad_platform = copy.deepcopy(self.platform_docs)
            for doc in bad_platform:
                if doc.get("kind") == "Deployment" and doc.get("metadata", {}).get("name") == "vault-ai-proxy":
                    del doc["spec"]["template"]["spec"]["containers"][0][probe_field]
            violations = self.module.check_ai_gateway_contract(self.projects_dict, bad_platform)
            self.assertTrue(
                any(expected_path in item["detail"] for item in violations),
                f"expected violation for missing {probe_field} ({expected_path}), got {violations}",
            )

    def test_non_loopback_admin_addr_rejected(self):
        bad_platform = copy.deepcopy(self.platform_docs)
        for doc in bad_platform:
            if doc.get("kind") == "Deployment" and doc.get("metadata", {}).get("name") == "vault-ai-proxy":
                for env_entry in doc["spec"]["template"]["spec"]["containers"][0]["env"]:
                    if env_entry["name"] == "ADMIN_ADDR":
                        env_entry["value"] = "0.0.0.0:8081"
        violations = self.module.check_ai_gateway_contract(self.projects_dict, bad_platform)
        self.assertTrue(any("127.0.0.1:8081" in item["detail"] for item in violations))

    def test_overprivileged_serviceaccount_or_openbao_role_rejected(self):
        bad_platform = copy.deepcopy(self.platform_docs)
        for doc in bad_platform:
            if doc.get("kind") == "Deployment" and doc.get("metadata", {}).get("name") == "vault-ai-proxy":
                doc["spec"]["template"]["spec"]["serviceAccountName"] = "openbao-backup"
                for env_entry in doc["spec"]["template"]["spec"]["containers"][0]["env"]:
                    if env_entry["name"] == "OPENBAO_ROLE":
                        env_entry["value"] = "openbao-backup"
        violations = self.module.check_ai_gateway_contract(self.projects_dict, bad_platform)
        self.assertTrue(any("vault-ai-proxy" in item["detail"] for item in violations))

    def test_unrestricted_ai_proxy_networkpolicy_or_wildcard_cilium_egress_rejected(self):
        bad_netpol = copy.deepcopy(self.platform_docs)
        for doc in bad_netpol:
            if doc.get("kind") == "NetworkPolicy" and doc.get("metadata", {}).get("name") == "ai-proxy-policy":
                doc["spec"]["ingress"][0]["from"][0]["podSelector"] = {}
        v1 = self.module.check_ai_gateway_contract(self.projects_dict, bad_netpol)
        self.assertTrue(any("podSelector.matchLabels" in item["detail"] for item in v1))

        bad_cnp = copy.deepcopy(self.platform_docs)
        for doc in bad_cnp:
            if doc.get("kind") == "CiliumNetworkPolicy" and doc.get("metadata", {}).get("name") == "ai-proxy-egress":
                doc["spec"]["egress"][-1]["toFQDNs"] = [{"matchPattern": "*.googleapis.com"}]
        v2 = self.module.check_ai_gateway_contract(self.projects_dict, bad_cnp)
        self.assertTrue(any("exact matchName" in item["detail"] or "generativelanguage.googleapis.com" in item["detail"] for item in v2))

    def test_observability_hostpath_to_vault_pv_or_privileged_rejected(self):
        bad_obs = copy.deepcopy(self.platform_docs) + [
            {
                "apiVersion": "apps/v1",
                "kind": "DaemonSet",
                "metadata": {"name": "fluent-bit", "namespace": "observability"},
                "spec": {
                    "template": {
                        "spec": {
                            "containers": [
                                {
                                    "name": "fluent-bit",
                                    "securityContext": {"privileged": True},
                                    "volumeMounts": [{"name": "varlogpods", "mountPath": "/var/log/pods", "readOnly": False}],
                                }
                            ],
                            "volumes": [{"name": "varlogpods", "hostPath": {"path": "/var/lib/rancher/k3s/storage"}}],
                        }
                    }
                },
            }
        ]
        v = self.module.check_ai_gateway_contract(self.projects_dict, bad_obs)
        self.assertTrue(any("/var/log/pods" in item["detail"] for item in v))
        self.assertTrue(any("privileged: true" in item["detail"] for item in v))
        self.assertTrue(any("readOnly: true" in item["detail"] for item in v))


if __name__ == "__main__":
    unittest.main()
