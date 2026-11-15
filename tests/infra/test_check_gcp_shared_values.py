"""Tests for scripts/check-gcp-shared-values.py (Spec 015, contracts/shared-values.md)."""

import json
import re
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
SCRIPT = REPO_ROOT / "scripts" / "check-gcp-shared-values.py"

SOURCES = [
    "infra/gcp/shared-values.json",
    "clusters/gke/capacity.yaml",
    "clusters/gke/layers.yaml",
    "deploy/platform/vault/service-accounts.yaml",
    "deploy/gcp/cloudbuild.yaml",
    "deploy/gcp/gar-cleanup-policy.json",
    "deploy/platform/traefik/service.yaml",
]


def run(repo_root: Path, *extra: str) -> subprocess.CompletedProcess:
    return subprocess.run(
        [sys.executable, str(SCRIPT), "--repo-root", str(repo_root), *extra],
        capture_output=True,
        text=True,
    )


class CheckGcpSharedValuesTests(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.root = Path(self._tmp.name)
        for rel in SOURCES:
            dst = self.root / rel
            dst.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(REPO_ROOT / rel, dst)
        for tf in (REPO_ROOT / "infra" / "gcp").rglob("*.tf"):
            if ".terraform" in tf.parts:
                continue
            dst = self.root / tf.relative_to(REPO_ROOT)
            dst.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(tf, dst)

    def tearDown(self):
        self._tmp.cleanup()

    # --- helpers -------------------------------------------------------------------------

    def edit(self, rel: str, old: str, new: str, count: int = 1):
        path = self.root / rel
        text = path.read_text(encoding="utf-8")
        self.assertIn(old, text, f"fixture text not found in {rel}")
        path.write_text(text.replace(old, new, count), encoding="utf-8")

    def edit_shared(self, mutate):
        path = self.root / "infra/gcp/shared-values.json"
        data = json.loads(path.read_text(encoding="utf-8"))
        mutate(data)
        path.write_text(json.dumps(data, indent=2), encoding="utf-8")

    def assert_fails(self, check_id: int, field: str | None = None):
        res = run(self.root)
        self.assertEqual(res.returncode, 1, res.stdout + res.stderr)
        self.assertRegex(res.stdout, rf"(?m)^CHECK {check_id} FAIL ")
        if field:
            self.assertRegex(res.stdout, rf"(?m)^CHECK {check_id} FAIL {re.escape(field)}:")

    # --- the real repository -------------------------------------------------------------

    def test_real_repository_passes(self):
        res = run(REPO_ROOT)
        self.assertEqual(res.returncode, 0, res.stdout + res.stderr)

    def test_fixture_copy_passes(self):
        res = run(self.root)
        self.assertEqual(res.returncode, 0, res.stdout + res.stderr)

    def test_json_output(self):
        self.edit("clusters/gke/capacity.yaml", "machineType: e2-small", "machineType: e2-micro")
        res = run(self.root, "--json")
        self.assertEqual(res.returncode, 1)
        data = json.loads(res.stdout)
        self.assertFalse(data["ok"])
        self.assertEqual(data["failures"][0]["id"], 1)

    # --- one changed value per check ----------------------------------------------------

    def test_check_1_pool_machine_type(self):
        self.edit("clusters/gke/capacity.yaml", "machineType: e2-small", "machineType: e2-micro")
        self.assert_fails(1)

    def test_check_1_pool_node_count(self):
        self.edit_shared(lambda d: d["node_pools"][0].update(node_count=2))
        self.assert_fails(1)

    def test_check_2_spot_and_taints(self):
        self.edit("clusters/gke/capacity.yaml", '      - "cloud.google.com/gke-spot=true:NoSchedule"\n', "")
        self.assert_fails(2)

    def test_check_3_kms_crypto_key(self):
        self.edit_shared(lambda d: d["kms"].update(crypto_key="x"))
        self.assert_fails(3, "kms.crypto_key")

    def test_check_4_gsa_annotation(self):
        self.edit("clusters/gke/layers.yaml", "openbao-kms@nacfson-pipeline", "openbao-other@nacfson-pipeline")
        self.assert_fails(4)

    def test_check_4_other_workload_identity_users_are_ignored(self):
        # A second Workload Identity user (e.g. cert-manager) may name its own Google account.
        self.edit("clusters/gke/layers.yaml", "  patches:\n", (
            "  patches:\n"
            "    - target:\n"
            "        kind: ServiceAccount\n"
            "        name: cert-manager\n"
            "        namespace: cert-manager\n"
            "      patch: |\n"
            "        apiVersion: v1\n"
            "        kind: ServiceAccount\n"
            "        metadata:\n"
            "          name: cert-manager\n"
            "          namespace: cert-manager\n"
            "          annotations:\n"
            "            iam.gke.io/gcp-service-account: cert-manager-dns@nacfson-pipeline.iam.gserviceaccount.com\n"
        ))
        res = run(self.root)
        self.assertEqual(res.returncode, 0, res.stdout + res.stderr)

    def test_check_4_missing_openbao_annotation(self):
        self.edit("clusters/gke/layers.yaml",
                  "            iam.gke.io/gcp-service-account: openbao-kms@nacfson-pipeline.iam.gserviceaccount.com\n", "")
        self.assert_fails(4)

    def test_check_5_ksa_exists(self):
        self.edit_shared(lambda d: d["kms"].update(ksa="vault/missing"))
        self.assert_fails(5)

    def test_check_6_cloudbuild_repo(self):
        self.edit("deploy/gcp/cloudbuild.yaml", "_GAR_REPO: nacfson-public", "_GAR_REPO: other-repo")
        self.assert_fails(6)

    def test_check_7_cleanup_policy(self):
        self.edit("deploy/gcp/gar-cleanup-policy.json", '"type": "Keep"', '"type": "Archive"')
        self.assert_fails(7)

    def test_check_7_registry_not_derived(self):
        self.edit("infra/gcp/gke/registry.tf", "local.shared.registry.cleanup_source", '"cleanup.json"')
        self.assert_fails(7)

    def test_check_8_ingress_annotation(self):
        self.edit("deploy/platform/traefik/service.yaml",
                  "load-balancer-ip-addresses: gke-ingress-ip", "load-balancer-ip-addresses: other-ip")
        self.assert_fails(8)

    def test_check_9_literal_in_tf(self):
        self.edit("infra/gcp/gke/ingress.tf", "region       = local.shared.region",
                  'region       = "asia-northeast3"')
        self.assert_fails(9)

    def test_check_9_comments_and_backend_are_ignored(self):
        path = self.root / "infra/gcp/gke/ingress.tf"
        path.write_text(path.read_text() + '\n# region "asia-northeast3" in a comment is fine\n')
        self.assertEqual(run(self.root).returncode, 0)

    def test_check_10_overlapping_cidrs(self):
        self.edit_shared(lambda d: d["network"].update(services_cidr="10.24.1.0/24"))
        self.assert_fails(10)

    def test_check_10_public_cidr(self):
        self.edit_shared(lambda d: d["network"].update(nodes_cidr="172.16.0.0/22"))
        self.assert_fails(10)

    def test_check_11_zone_and_pool(self):
        self.edit_shared(lambda d: d.update(zone="us-west1-b"))
        self.assert_fails(11)

    # --- missing source ------------------------------------------------------------------

    def test_missing_source_exits_2(self):
        (self.root / "deploy/platform/traefik/service.yaml").unlink()
        res = run(self.root)
        self.assertEqual(res.returncode, 2, res.stdout + res.stderr)


if __name__ == "__main__":
    unittest.main()
