"""Unit tests for VictoriaLogs archiving in scripts/cutover-bundle.sh."""

from __future__ import annotations

import os
import re
import stat
import subprocess
import tempfile
import unittest

BASE_DIR = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
SCRIPT = os.path.join(BASE_DIR, "scripts", "cutover-bundle.sh")


def _extract_functions(script_path: str, names: list[str]) -> str:
    with open(script_path, encoding="utf-8") as fh:
        content = fh.read()
    extracted: list[str] = []
    for name in names:
        pattern = re.compile(
            rf"^{re.escape(name)}\(\)\s*\{{.*?^\}}",
            re.MULTILINE | re.DOTALL,
        )
        match = pattern.search(content)
        if not match:
            raise AssertionError(f"Function {name}() not found in {script_path}")
        extracted.append(match.group(0))
    return "\n\n".join(extracted)


def _write_executable(path: str, body: str) -> None:
    with open(path, "w", encoding="utf-8") as fh:
        fh.write(body)
    os.chmod(path, os.stat(path).st_mode | stat.S_IXUSR | stat.S_IXGRP | stat.S_IXOTH)


class TestCutoverBundleVictoriaLogs(unittest.TestCase):
    def test_bash_syntax(self) -> None:
        result = subprocess.run(
            ["bash", "-n", SCRIPT],
            capture_output=True,
            text=True,
        )
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_archive_victorialogs_via_ssh_and_symlink(self) -> None:
        funcs = _extract_functions(SCRIPT, ["run_source_kubectl", "archive_victorialogs"])

        with tempfile.TemporaryDirectory() as tmpdir:
            bin_dir = os.path.join(tmpdir, "bin")
            pv_dir = os.path.join(tmpdir, "pv-data")
            bundle_dir = os.path.join(tmpdir, "bundle")
            backing_dir = os.path.join(tmpdir, "backing")
            os.makedirs(bin_dir)
            os.makedirs(pv_dir)
            os.makedirs(bundle_dir)
            os.makedirs(backing_dir)

            with open(os.path.join(pv_dir, "segment.bin"), "wb") as fh:
                fh.write(b"victorialogs-test-payload\n")

            backing_file = os.path.join(backing_dir, "victoria-logs-data.tar.gz")
            open(backing_file, "wb").close()
            bundle_archive = os.path.join(bundle_dir, "victoria-logs-data.tar.gz")
            os.symlink(backing_file, bundle_archive)

            _write_executable(
                os.path.join(bin_dir, "ssh"),
                '#!/usr/bin/env bash\nshift\nexec bash -c "$*"\n',
            )
            _write_executable(
                os.path.join(bin_dir, "sudo"),
                '#!/usr/bin/env bash\nexec "$@"\n',
            )
            _write_executable(
                os.path.join(bin_dir, "k3s"),
                f"""#!/usr/bin/env bash
if [[ "$*" == *"get pvc"* ]]; then
  echo "pvc-123"
elif [[ "$*" == *"get pv pvc-123"* ]]; then
  echo "{pv_dir}"
else
  echo "unexpected k3s args: $*" >&2
  exit 1
fi
""",
            )

            env = {
                **os.environ,
                "PATH": f"{bin_dir}:{os.environ['PATH']}",
                "SSH_HOST": "oracleCloud",
                "ABS_BUNDLE_DIR": bundle_dir,
            }
            result = subprocess.run(
                ["bash", "-euo", "pipefail", "-c", f"{funcs}\narchive_victorialogs"],
                capture_output=True,
                text=True,
                env=env,
            )
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertIn("✓ VictoriaLogs archive:", result.stdout)
            self.assertTrue(os.path.islink(bundle_archive), "symlink must be preserved")

            tar_list = subprocess.run(
                ["tar", "-tzf", bundle_archive],
                capture_output=True,
                text=True,
            )
            self.assertEqual(tar_list.returncode, 0, tar_list.stderr)
            self.assertIn("./segment.bin", tar_list.stdout.splitlines())

    def test_archive_victorialogs_without_ssh_gives_actionable_error(self) -> None:
        funcs = _extract_functions(SCRIPT, ["run_source_kubectl", "archive_victorialogs"])

        with tempfile.TemporaryDirectory() as tmpdir:
            bin_dir = os.path.join(tmpdir, "bin")
            bundle_dir = os.path.join(tmpdir, "bundle")
            os.makedirs(bin_dir)
            os.makedirs(bundle_dir)

            _write_executable(
                os.path.join(bin_dir, "kubectl"),
                '#!/usr/bin/env bash\necho \'exec: "tar": executable file not found in $PATH\' >&2\nexit 126\n',
            )

            env = {
                **os.environ,
                "PATH": f"{bin_dir}:{os.environ['PATH']}",
                "SSH_HOST": "",
                "ABS_BUNDLE_DIR": bundle_dir,
            }
            result = subprocess.run(
                ["bash", "-euo", "pipefail", "-c", f"{funcs}\narchive_victorialogs"],
                capture_output=True,
                text=True,
                env=env,
            )
            self.assertEqual(result.returncode, 1)
            self.assertIn("Re-run export with --ssh <k3s-host>", result.stderr)


if __name__ == "__main__":
    unittest.main()
