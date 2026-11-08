import copy
import importlib.util
import os
import unittest

import yaml

BASE = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
PREFLIGHT = os.path.join(BASE, "scripts", "platform-preflight.py")
FIXTURES = os.path.join(BASE, "tests", "preflight", "fixtures", "log_contract")


def _preflight():
    spec = importlib.util.spec_from_file_location("platform_preflight", PREFLIGHT)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def _load_yaml(filename):
    path = os.path.join(FIXTURES, filename)
    with open(path, encoding="utf-8") as fh:
        return [doc for doc in yaml.safe_load_all(fh) if doc is not None]


class TestLogContractPreflight(unittest.TestCase):
    def setUp(self):
        self.module = _preflight()
        # Mock minimal platform docs containing fluent-bit-config with legacy-containers.txt
        self.platform_docs = [
            {
                "apiVersion": "v1",
                "kind": "ConfigMap",
                "metadata": {"name": "fluent-bit-config", "namespace": "observability"},
                "data": {
                    "legacy-containers.txt": "\n".join(
                        [
                            "proj-pn/backend",
                            "proj-pn/frontend",
                            "proj-health/backend",
                            "proj-health/frontend",
                            "proj-blog/blog-web",
                        ]
                    )
                },
            }
        ]

    def test_missing_annotation(self):
        docs = _load_yaml("missing-annotation.yaml")
        v = self.module.check_log_contract(docs, self.platform_docs, repo_root=BASE)
        rules = [item["rule"] for item in v]
        self.assertIn("log-declaration-missing", rules)

    def test_bad_annotation_value(self):
        docs = _load_yaml("bad-annotation-value.yaml")
        v = self.module.check_log_contract(docs, self.platform_docs, repo_root=BASE)
        rules = [item["rule"] for item in v]
        self.assertIn("log-declaration-missing", rules)

    def test_log_file_env(self):
        docs = _load_yaml("log-file-env.yaml")
        v = self.module.check_log_contract(docs, self.platform_docs, repo_root=BASE)
        rules = [item["rule"] for item in v]
        self.assertIn("log-file-output", rules)

    def test_log_format_text(self):
        docs = _load_yaml("log-format-text.yaml")
        v = self.module.check_log_contract(docs, self.platform_docs, repo_root=BASE)
        rules = [item["rule"] for item in v]
        self.assertIn("log-file-output", rules)

    def test_undeclared_log_volume(self):
        docs = _load_yaml("undeclared-log-volume.yaml")
        v = self.module.check_log_contract(docs, self.platform_docs, repo_root=BASE)
        rules = [item["rule"] for item in v]
        self.assertIn("log-volume-undeclared", rules)

    def test_declared_data_path_ok(self):
        docs = _load_yaml("declared-data-path-ok.yaml")
        # Ensure migration row is satisfied by matching a known project/container or mock contract
        # In declared-data-path-ok.yaml, namespace is proj-test, container is app.
        # We temporarily pass platform docs and verify no data-path violation
        v = self.module.check_log_contract(docs, self.platform_docs, repo_root=BASE)
        rules = [item["rule"] for item in v]
        self.assertNotIn("log-volume-undeclared", rules)
        self.assertNotIn("log-data-reason-missing", rules)

    def test_data_reason_missing(self):
        docs = _load_yaml("data-reason-missing.yaml")
        v = self.module.check_log_contract(docs, self.platform_docs, repo_root=BASE)
        rules = [item["rule"] for item in v]
        self.assertIn("log-data-reason-missing", rules)

    def test_version_label_missing(self):
        docs = _load_yaml("version-label-missing.yaml")
        v = self.module.check_log_contract(docs, self.platform_docs, repo_root=BASE)
        rules = [item["rule"] for item in v]
        self.assertIn("log-version-label", rules)

    def test_version_label_malformed(self):
        docs = _load_yaml("version-label-malformed.yaml")
        v = self.module.check_log_contract(docs, self.platform_docs, repo_root=BASE)
        rules = [item["rule"] for item in v]
        self.assertIn("log-version-label", rules)

    def test_v0_plain(self):
        docs = _load_yaml("v0-plain.yaml")
        v = self.module.check_log_contract(docs, self.platform_docs, repo_root=BASE)
        rules = [item["rule"] for item in v]
        self.assertIn("log-version-label", rules)

    def test_v0_commit_in_vps_ok(self):
        docs = _load_yaml("v0-commit-in-vps-ok.yaml")
        v = self.module.check_log_contract(docs, self.platform_docs, environment="vps-k3s", repo_root=BASE)
        rules = [item["rule"] for item in v]
        self.assertNotIn("log-version-label", rules)

    def test_legacy_list_drift(self):
        docs = _load_yaml("legacy-list-drift.yaml")
        v = self.module.check_log_contract(docs, self.platform_docs, repo_root=BASE)
        rules = [item["rule"] for item in v]
        self.assertIn("log-legacy-list-drift", rules)

    def test_done_regressed(self):
        docs = _load_yaml("done-regressed.yaml")
        # In CONTRACT_LOG_SCHEMA.md §6, mock a done container
        mock_contract = "\n## 6. Migration from version 2\n\n| Project | Container | Current format | Status |\n|---|---|---|---|\n| `proj-test` | `done-app` | json | done |\n"
        import tempfile

        with tempfile.TemporaryDirectory() as tmp:
            c_dir = os.path.join(tmp, "contracts")
            os.makedirs(c_dir)
            with open(os.path.join(c_dir, "CONTRACT_LOG_SCHEMA.md"), "w", encoding="utf-8") as fh:
                fh.write(mock_contract)
            v = self.module.check_log_contract(docs, self.platform_docs, repo_root=tmp)
            rules = [item["rule"] for item in v]
            self.assertIn("log-migration-regressed", rules)

    def test_new_container_legacy(self):
        docs = _load_yaml("new-container-legacy.yaml")
        v = self.module.check_log_contract(docs, self.platform_docs, repo_root=BASE)
        rules = [item["rule"] for item in v]
        self.assertIn("log-migration-row-missing", rules)


if __name__ == "__main__":
    unittest.main()
