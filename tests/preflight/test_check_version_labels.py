import importlib.util
import json
import os
import sys
import unittest
from unittest.mock import patch, MagicMock

BASE = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
SCRIPT_PATH = os.path.join(BASE, "scripts", "check-version-labels.py")

spec = importlib.util.spec_from_file_location("check_version_labels", SCRIPT_PATH)
cvl = importlib.util.module_from_spec(spec)
spec.loader.exec_module(cvl)


class TestCheckVersionLabels(unittest.TestCase):

    def setUp(self):
        self.sample_doc = {
            "apiVersion": "apps/v1",
            "kind": "Deployment",
            "metadata": {
                "name": "pn-backend",
                "namespace": "proj-pn",
            },
            "spec": {
                "template": {
                    "metadata": {
                        "labels": {
                            "app.kubernetes.io/name": "pn-backend",
                            "app.kubernetes.io/version": "v1.4.2",
                        }
                    },
                    "spec": {
                        "containers": [
                            {
                                "name": "backend",
                                "image": "ghcr.io/nacfson/project-pn-backend@sha256:22f444",
                            }
                        ]
                    },
                }
            },
        }

    @patch("subprocess.run")
    def test_matching_label_passes(self, mock_run):
        mock_run.return_value = MagicMock(
            returncode=0,
            stdout=json.dumps({
                "Labels": {
                    "org.opencontainers.image.version": "v1.4.2",
                }
            }),
            stderr="",
        )

        violations = cvl.check_version_labels([self.sample_doc])
        self.assertEqual(len(violations), 0)

    @patch("subprocess.run")
    def test_mismatched_label_fails_and_names_both_versions(self, mock_run):
        mock_run.return_value = MagicMock(
            returncode=0,
            stdout=json.dumps({
                "Labels": {
                    "org.opencontainers.image.version": "v1.5.0",
                }
            }),
            stderr="",
        )

        violations = cvl.check_version_labels([self.sample_doc])
        self.assertEqual(len(violations), 1)
        v = violations[0]
        self.assertEqual(v["rule"], cvl.VIOLATION_MISMATCH)
        self.assertEqual(v["object"], "proj-pn/backend")
        self.assertIn("v1.4.2", v["detail"])
        self.assertIn("v1.5.0", v["detail"])

    @patch("subprocess.run")
    def test_unreadable_image_fails(self, mock_run):
        mock_run.return_value = MagicMock(
            returncode=1,
            stdout="",
            stderr="manifest unknown: manifest unknown",
        )

        violations = cvl.check_version_labels([self.sample_doc])
        self.assertEqual(len(violations), 1)
        v = violations[0]
        self.assertEqual(v["rule"], cvl.VIOLATION_UNREADABLE)
        self.assertEqual(v["object"], "proj-pn/backend")
        self.assertIn("manifest unknown", v["detail"])

    @patch("subprocess.run")
    def test_missing_version_label_in_image(self, mock_run):
        mock_run.return_value = MagicMock(
            returncode=0,
            stdout=json.dumps({
                "Labels": {
                    "org.opencontainers.image.revision": "abc1234",
                }
            }),
            stderr="",
        )

        violations = cvl.check_version_labels([self.sample_doc])
        self.assertEqual(len(violations), 1)
        v = violations[0]
        self.assertEqual(v["rule"], cvl.VIOLATION_MISMATCH)
        self.assertIn("missing", v["detail"])

    @patch("subprocess.run")
    def test_cli_main_success(self, mock_run):
        mock_run.return_value = MagicMock(
            returncode=0,
            stdout=json.dumps({
                "Labels": {
                    "org.opencontainers.image.version": "v0.0.0-feb5afc",
                }
            }),
            stderr="",
        )
        pn_dir = os.path.join(BASE, "deploy", "projects", "pn")
        with patch.object(
            sys,
            "argv",
            ["check-version-labels.py", "--workloads-dir", pn_dir],
        ):
            code = cvl.main()
            self.assertEqual(code, 0)

    @patch("subprocess.run")
    def test_cli_main_failure(self, mock_run):
        mock_run.return_value = MagicMock(
            returncode=0,
            stdout=json.dumps({
                "Labels": {
                    "org.opencontainers.image.version": "v9.9.9",
                }
            }),
            stderr="",
        )
        pn_dir = os.path.join(BASE, "deploy", "projects", "pn")
        with patch.object(
            sys,
            "argv",
            ["check-version-labels.py", "--workloads-dir", pn_dir],
        ):
            code = cvl.main()
            self.assertEqual(code, 1)


if __name__ == "__main__":
    unittest.main()
