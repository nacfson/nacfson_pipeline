"""Unit tests for scripts/open-observe.sh.

The live SSH and port-forward path is not executed here.
"""

from __future__ import annotations

import os
import subprocess
import tempfile
import unittest

BASE_DIR = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
SCRIPT = os.path.join(BASE_DIR, "scripts", "open-observe.sh")


def _path_without_kubectl(tmp: str) -> str:
    """Return a PATH that keeps system tools but never resolves kubectl.

    CI runners ship kubectl in /usr/bin, so trimming PATH to /usr/bin:/bin
    does not hide it. Symlink every system tool except kubectl instead.
    """
    for src_dir in ("/usr/bin", "/bin"):
        if not os.path.isdir(src_dir):
            continue
        for name in os.listdir(src_dir):
            dst = os.path.join(tmp, name)
            if name == "kubectl" or os.path.lexists(dst):
                continue
            os.symlink(os.path.join(src_dir, name), dst)
    return tmp


class TestOpenObserve(unittest.TestCase):
    def test_bash_syntax(self):
        result = subprocess.run(["bash", "-n", SCRIPT], capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_help_names_both_uis_and_the_ssh_host(self):
        result = subprocess.run(["bash", SCRIPT, "--help"], capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("oracleCloud", result.stdout)
        self.assertIn("12000", result.stdout)
        self.assertIn("9428", result.stdout)
        self.assertIn("16443", result.stdout)

    def test_unknown_argument_exits_2(self):
        result = subprocess.run(
            ["bash", SCRIPT, "--not-a-flag"],
            capture_output=True,
            text=True,
        )
        self.assertEqual(result.returncode, 2, result.stderr)

    def test_missing_kubectl_exits_1(self):
        with tempfile.TemporaryDirectory() as tmp:
            result = subprocess.run(
                ["bash", SCRIPT],
                capture_output=True,
                text=True,
                env={**os.environ, "PATH": _path_without_kubectl(tmp)},
            )
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn("kubectl is required", result.stdout + result.stderr)

    def test_logsql_example_query(self):
        with open(SCRIPT, encoding="utf-8") as fh:
            content = fh.read()
        self.assertIn('{k8s.namespace.name="proj-pn"}', content)
        self.assertNotIn('{project="proj-pn"}', content)
