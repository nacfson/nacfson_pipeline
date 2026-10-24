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

    def test_repeated_errors_ignore_leading_timestamp(self):
        sample = "\n".join(
            [
                "[2026/10/24 12:00:00.111111111] [error] [output:http:http.0] retry failed",
                "[2026/10/24 12:00:01.222222222] [error] [output:http:http.0] retry failed",
                "[2026/10/24 12:00:02.333333333] [error] [output:http:http.0] once",
                "[2026/10/24 12:00:03.444444444] [info] collector healthy",
            ]
        )
        result = subprocess.run(
            [
                "bash",
                "-c",
                'source "$1"; printf "%s\\n" "$2" | repeated_fluentbit_errors',
                "bash",
                SCRIPT,
                sample,
            ],
            capture_output=True,
            text=True,
            env={**os.environ, "OPEN_VICTORIALOGS_LIB": "1"},
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(
            result.stdout,
            "[error] [output:http:http.0] retry failed\n",
        )
        self.assertNotIn("once", result.stdout)

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
