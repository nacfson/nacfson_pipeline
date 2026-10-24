"""Unit tests for scripts/open-victorialogs-ui.sh.

Live `--verify` checks talk to a cluster and are not executed here.
"""

from __future__ import annotations

import os
import shutil
import subprocess
import unittest

BASE_DIR = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
SCRIPT = os.path.join(BASE_DIR, "scripts", "open-victorialogs-ui.sh")


class TestOpenVictoriaLogsUi(unittest.TestCase):
    def test_bash_syntax(self):
        result = subprocess.run(
            ["bash", "-n", SCRIPT],
            capture_output=True,
            text=True,
        )
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_help_exits_zero_and_mentions_verify(self):
        result = subprocess.run(
            ["bash", SCRIPT, "--help"],
            capture_output=True,
            text=True,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("--verify", result.stdout)

    def test_unknown_argument_exits_2(self):
        result = subprocess.run(
            ["bash", SCRIPT, "--not-a-flag"],
            capture_output=True,
            text=True,
        )
        self.assertEqual(result.returncode, 2, result.stderr)

    def test_port_without_value_exits_2(self):
        result = subprocess.run(
            ["bash", SCRIPT, "--port"],
            capture_output=True,
            text=True,
        )
        self.assertEqual(result.returncode, 2, result.stderr)

    def test_missing_kubectl_exits_1(self):
        # kubectl lives in /usr/local/bin; /usr/bin:/bin still has bash.
        result = subprocess.run(
            ["bash", SCRIPT],
            capture_output=True,
            text=True,
            env={**os.environ, "PATH": "/usr/bin:/bin"},
        )
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn("kubectl is required", result.stdout + result.stderr)

    def test_shellcheck_clean_when_installed(self):
        shellcheck = shutil.which("shellcheck")
        if shellcheck is None:
            self.skipTest("shellcheck is not installed")
        result = subprocess.run(
            [shellcheck, SCRIPT],
            capture_output=True,
            text=True,
        )
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)


if __name__ == "__main__":
    unittest.main()
