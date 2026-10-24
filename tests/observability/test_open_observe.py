"""Unit tests for scripts/open-observe.sh.

The live SSH and port-forward path is not executed here.
"""

from __future__ import annotations

import os
import subprocess
import unittest

BASE_DIR = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
SCRIPT = os.path.join(BASE_DIR, "scripts", "open-observe.sh")


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
        result = subprocess.run(
            ["bash", SCRIPT],
            capture_output=True,
            text=True,
            env={**os.environ, "PATH": "/usr/bin:/bin"},
        )
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn("kubectl is required", result.stdout + result.stderr)
