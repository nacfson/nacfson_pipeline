import os
import shutil
import subprocess
import tempfile
import unittest

REPO = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
SCRIPT = os.path.join(REPO, "scripts", "vault", "vault-configure-k8s-auth.sh")
FIXTURE = os.path.join(
    REPO, "tests", "fixtures", "openbao", "2.0.0", "status", "gke-unsealed-auto-gcpckms.json"
)

KUBECTL_STUB = r"""#!/usr/bin/env bash
set -u
STATE="__STATE__"
printf '%s\n' "$*" >> "$STATE/calls.log"
case "$*" in
  *"bao status -format=json"*)
    cat "__FIXTURE__"
    exit 0
    ;;
  *"bao token lookup"*)
    exit 0
    ;;
  *"get configmap kube-root-ca.crt"*)
    printf 'FAKE_CA_CERT\n'
    exit 0
    ;;
  *"create token openbao-token-reviewer"*)
    printf 'FAKE_REVIEWER_JWT\n'
    exit 0
    ;;
  *"tee /tmp/"*)
    cat > /dev/null
    exit 0
    ;;
  *" -- env")
    [[ -n "${STUB_K8S_HOST:-}" ]] && printf 'KUBERNETES_SERVICE_HOST=%s\n' "${STUB_K8S_HOST}"
    [[ -n "${STUB_K8S_PORT:-}" ]] && printf 'KUBERNETES_SERVICE_PORT=%s\n' "${STUB_K8S_PORT}"
    exit 0
    ;;
esac
exit 0
"""


class VaultConfigureK8sAuthTest(unittest.TestCase):
    def _run(self, k8s_host="10.28.0.1", k8s_port="443", use_ssh_sim=False):
        tmp = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, tmp)
        bin_dir = os.path.join(tmp, "bin")
        os.makedirs(bin_dir)
        stub = os.path.join(bin_dir, "kubectl")
        body = KUBECTL_STUB.replace("__STATE__", tmp).replace("__FIXTURE__", FIXTURE)
        with open(stub, "w", encoding="utf-8") as fh:
            fh.write(body)
        os.chmod(stub, 0o755)

        env = {
            **os.environ,
            "PATH": f"{bin_dir}:{os.environ['PATH']}",
            "VAULT_TOKEN": "s.root-test",
            "POLICIES_DIR": os.path.join(tmp, "empty-policies"),
            "ALLOW_SSH_FALLBACK": "false",
            "STUB_K8S_HOST": k8s_host,
            "STUB_K8S_PORT": k8s_port,
        }
        if use_ssh_sim:
            ssh_sim = os.path.join(bin_dir, "ssh-sim")
            with open(ssh_sim, "w", encoding="utf-8") as fh:
                fh.write("#!/usr/bin/env bash\neval \"$*\"\n")
            os.chmod(ssh_sim, 0o755)
            env.pop("KUBERNETES_SERVICE_HOST", None)
            env.pop("KUBERNETES_SERVICE_PORT", None)
            env["KUBE_EXEC"] = f"{ssh_sim} kubectl"
        result = subprocess.run(
            ["bash", SCRIPT],
            cwd=REPO,
            capture_output=True,
            text=True,
            env=env,
            timeout=30,
        )
        calls_path = os.path.join(tmp, "calls.log")
        if os.path.exists(calls_path):
            with open(calls_path, encoding="utf-8") as fh:
                calls = fh.read()
        else:
            calls = ""
        return result, calls

    def test_binds_kubernetes_host_from_in_pod_service_ip(self):
        result, calls = self._run(k8s_host="10.28.0.1", k8s_port="443")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("kubernetes_host=https://10.28.0.1:443", calls)
        self.assertNotIn("kubernetes.default.svc", calls)
        self.assertIn("rm -f /tmp/ca.crt /tmp/token_reviewer.jwt", calls)

    def test_brackets_ipv6_service_host(self):
        result, calls = self._run(k8s_host="fd00::1", k8s_port="6443")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("kubernetes_host=https://[fd00::1]:6443", calls)

    def test_fails_closed_when_service_host_missing(self):
        result, calls = self._run(k8s_host="", k8s_port="443")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("KUBERNETES_SERVICE_HOST", result.stderr)
        self.assertNotIn("auth/kubernetes/config", calls)

    def test_ssh_style_kube_exec_does_not_expand_pod_env_on_remote_shell(self):
        result, calls = self._run(k8s_host="10.28.0.1", k8s_port="443", use_ssh_sim=True)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("kubernetes_host=https://10.28.0.1:443", calls)


if __name__ == "__main__":
    unittest.main()
