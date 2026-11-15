import os
import shutil
import subprocess
import tempfile
import unittest

REPO = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
SCRIPT = os.path.join(REPO, "scripts", "vault-reconcile.sh")

PRETTY_STATUS = """{
  "type": "shamir",
  "initialized": %s,
  "sealed": %s,
  "t": 1
}
"""

KUBECTL_STUB = r"""#!/usr/bin/env bash
printf '%s\n' "$*" >> "__STATE__/calls.log"
case "$*" in
  *"bao status -format=json"*)
    if [[ "$*" != *"VAULT_ADDR=http://127.0.0.1:8200"* ]]; then
      echo 'Error: http: server gave HTTP response to HTTPS client' >&2
      exit 1
    fi
    cat "__STATE__/status.json"
    ;;
esac
exit 0
"""


class VaultReconcileStatusTest(unittest.TestCase):
    def _run(self, initialized, sealed, *args, mock=False):
        tmp = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, tmp)
        with open(os.path.join(tmp, "status.json"), "w", encoding="utf-8") as fh:
            fh.write(PRETTY_STATUS % (initialized, sealed))
        bin_dir = os.path.join(tmp, "bin")
        os.makedirs(bin_dir)
        stub = os.path.join(bin_dir, "kubectl")
        with open(stub, "w", encoding="utf-8") as fh:
            fh.write(KUBECTL_STUB.replace("__STATE__", tmp))
        os.chmod(stub, 0o755)
        env = {**os.environ, "PATH": f"{bin_dir}:{os.environ['PATH']}"}
        env.pop("VAULT_MOCK_MODE", None)
        if mock:
            env["VAULT_MOCK_MODE"] = "true"
        result = subprocess.run(["bash", SCRIPT, *args], capture_output=True, text=True, env=env, timeout=30)
        calls_path = os.path.join(tmp, "calls.log")
        calls = open(calls_path, encoding="utf-8").read() if os.path.exists(calls_path) else ""
        return result, calls

    def test_unsealed_pretty_json_passes_health_check(self):
        result, _ = self._run("true", "false")
        self.assertIn("PASS: Vault instance is unsealed and healthy.", result.stdout)
        self.assertEqual(result.returncode, 2, result.stdout + result.stderr)

    def test_confirm_lifts_gate_with_explicit_address(self):
        result, calls = self._run("true", "false", "--confirm")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("VAULT_ADDR=http://127.0.0.1:8200 VAULT_TOKEN=", calls)

    def test_sealed_vault_is_rejected(self):
        result, _ = self._run("true", "true")
        self.assertEqual(result.returncode, 1)
        self.assertIn("SEALED", result.stdout)

    def test_uninitialized_vault_is_rejected(self):
        result, _ = self._run("false", "true")
        self.assertEqual(result.returncode, 1)
        self.assertIn("not initialized", result.stdout)

    def test_mock_mode_is_opt_in_only(self):
        result, calls = self._run("true", "false", mock=True)
        self.assertIn("Simulated", result.stdout)
        self.assertNotIn("bao status", calls)
        result, calls = self._run("true", "false")
        self.assertNotIn("Simulated", result.stdout)
        self.assertIn("bao status", calls)


if __name__ == "__main__":
    unittest.main()
