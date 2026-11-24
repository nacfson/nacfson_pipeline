import importlib.util
import json
import os
import subprocess
import sys
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
GUARD_SCRIPT = ROOT / "scripts" / "hooks" / "check-cluster-cli-guard.py"
HOOK_SCRIPT = ROOT / "scripts" / "hooks" / "universal-agent-hook.py"

_spec = importlib.util.spec_from_file_location("check_cluster_cli_guard", GUARD_SCRIPT)
assert _spec and _spec.loader
guard = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(guard)


class ClusterCLIGuardTest(unittest.TestCase):
    def assert_blocked(self, cmd: str, env: dict[str, str] | None = None) -> None:
        ok, reason = guard.check_command(cmd, env if env is not None else {})
        self.assertFalse(ok, f"expected blocked command: {cmd!r}")
        self.assertIn("[CLUSTER-CLI GUARD]", reason)

    def assert_allowed(self, cmd: str, env: dict[str, str] | None = None) -> None:
        ok, reason = guard.check_command(cmd, env if env is not None else {})
        self.assertTrue(ok, f"expected allowed command {cmd!r}, got reason: {reason}")

    def test_blocks_direct_cluster_and_infra_cli_commands(self) -> None:
        for cmd in (
            "kubectl get pods -A",
            "/usr/local/bin/kubectl get secrets",
            "k3s kubectl get pods",
            "oc get pods",
            "flux get kustomizations -A",
            "helm list -A",
            "k9s",
            "kubectx",
            "kubens",
            "stern app",
            "crictl ps",
            "cilium status",
            "hubble observe",
            "argocd app list",
            "velero backup get",
            "ssh ubuntu@local-k3s",
            "scp foo bar:/tmp/",
            "sftp host",
            "gcloud container clusters list",
            "gke-gcloud-auth-plugin",
            "gsutil ls gs://bucket",
            "bq ls",
            "cloud-sql-proxy proj:reg:db",
            "oci os ns get",
            "aws eks list-clusters",
            "eksctl get cluster",
            "az aks list",
            "doctl kubernetes cluster list",
            "linode-cli lke clusters-list",
            "ansible all -m ping",
            "ansible-playbook bootstrap/bootstrap.yml",
            "kcat -L -b localhost:9092",
            "kafkacat -L -b localhost:9092",
            "wg show",
            "wg-quick up wg0",
            "tailscale status",
        ):
            self.assert_blocked(cmd)

    def test_blocks_mutating_terraform_and_tofu_only(self) -> None:
        for cmd in (
            "terraform apply -auto-approve",
            "tofu -chdir=infra/gcp destroy",
            "terraform -chdir=infra/gcp import null_resource.x y",
            "terraform state rm aws_instance.demo",
            "tofu state mv a b",
            "terraform force-unlock abc",
        ):
            self.assert_blocked(cmd)

        for cmd in (
            "terraform -chdir=infra/gcp fmt -check",
            "terraform -chdir=infra/gcp validate",
            "tofu plan",
            "terraform state list",
            "terraform show",
        ):
            self.assert_allowed(cmd)

    def test_blocks_chained_subshell_and_wrapper_evasion(self) -> None:
        for cmd in (
            "echo ok && kubectl get nodes",
            "git status; flux get all",
            "ls | xargs ssh remote-box",
            "echo $(kubectl get pods -A)",
            'echo "$(helm status app)"',
            "echo `gcloud auth list`",
            "sudo env FOO=1 timeout 5 /usr/bin/kubectl get ns",
            "bash -c 'echo ok; kubectl get pods'",
            "sh -ec \"ssh root@vm id\"",
            'env -S "kubectl get pods -A"',
            'env --split-string "kubectl get pods -A"',
            'env -S="flux get all"',
            'env -iS "ssh host"',
            'bash -c -- "kubectl get pods -A"',
            'sh -c -e "kubectl get pods -A"',
            'bash -c -o errexit -- "ssh vm"',
            'bash -O extglob -c "kubectl get pods -A"',
            'bash -Oc extglob "kubectl get pods -A"',
            'bash -oc errexit "kubectl get pods -A"',
            'bash --rcfile /dev/null -c "kubectl get pods -A"',
            'env -S "bash -c" "kubectl get pods -A"',
            'su root -c "kubectl get pods -A"',
            'su -w FOO root -c "kubectl get pods -A"',
            'su --command "kubectl get pods -A"',
            'su --command="kubectl get pods -A"',
            'su -mc "kubectl get pods -A"',
            "sudo --user root kubectl get pods -A",
            "sudo --chdir /tmp kubectl get pods -A",
            'sudo -s "kubectl get pods -A"',
            'sudo -i "kubectl get pods -A"',
            "coproc kubectl get pods -A",
            "coproc BG { kubectl get pods -A; }",
            "(kubectl get pods -A)",
            "((kubectl get pods -A))",
            "{ kubectl get pods -A; }",
            "! kubectl get pods -A",
            "sudo grep . /etc/nacfson-mcp/kubeconfig",
            "sudo rg ^ /etc/nacfson-mcp/kubeconfig",
            "grep . /etc/nacfson-mcp/kubeconfig",
            "grep -nf /etc/nacfson-mcp/kubeconfig /dev/null",
            "grep -f /dev/null /etc/nacfson-mcp/kubeconfig",
            "grep --file=/dev/null /etc/nacfson-mcp/kubeconfig",
            "grep -e token /etc/nacfson-mcp/kubeconfig",
            "rg ^ /etc/nacfson-mcp/kubeconfig",
            "rg -nf /etc/nacfson-mcp/kubeconfig /dev/null",
            "rg -f /dev/null /etc/nacfson-mcp/kubeconfig",
            "rg --ignore-file=/etc/nacfson-mcp/kubeconfig foo docs/",
            "rg --files /etc/nacfson-mcp",
            "git grep -f /dev/null /etc/nacfson-mcp/kubeconfig",
            "git diff --no-index /dev/null /etc/nacfson-mcp/kubeconfig",
            "git diff --no-index -b /etc/nacfson-mcp/kubeconfig /dev/null",
            "git log -p -m /etc/nacfson-mcp/kubeconfig",
            'git -cinclude.path=/etc/nacfson-mcp/kubeconfig commit -m "ok"',
            'gh --hostname=/etc/nacfson-mcp issue create -t hi',
            "2>&1 kubectl get pods -A",
            ">&2 kubectl get pods -A",
            "10>&1 kubectl get pods -A",
            "&>/dev/null kubectl get pods -A",
            'bash <<< "kubectl get pods -A"',
            'sh <<< "cat /etc/nacfson-mcp/kubeconfig"',
            'eval -- "kubectl get pods -A"',
            "eval eval eval eval eval eval eval kubectl get pods",
            "exec -a foo kubectl get pods -A",
            "exec -ca foo kubectl get pods -A",
            "env -iu FOO kubectl get pods -A",
            "env -uS kubectl get pods -A",
            "xargs -rn 1 kubectl get pods -A",
            "timeout -vs KILL 5 kubectl get pods -A",
            "strace -fe write kubectl get pods -A",
            "grep --color . /etc/nacfson-mcp/kubeconfig",
            "git grep --color . /etc/nacfson-mcp/kubeconfig",
            "$'kubectl' get pods -A",
            '$"kubectl" get pods -A',
            "$'kub\\x65ctl' get pods -A",
            "bash -c $'kubectl get pods -A'",
            "bash -c $'echo ok\\nkubectl get pods'",
            "grep -T . /etc/nacfson-mcp/kubeconfig",
            "egrep -T . /etc/nacfson-mcp/kubeconfig",
            "xargs --max-args 1 kubectl get pods",
            "xargs --max-procs 2 kubectl get pods",
            "xargs --delimiter \\n kubectl get pods",
            "xargs --arg-file /dev/null kubectl get pods",
            "xargs -i kubectl get pods {}",
            "xargs --replace kubectl get pods {}",
            "strace --output /tmp/x kubectl get pods",
            "strace -a 40 kubectl get pods",
            "{fd}>/dev/null kubectl get pods -A",
            "source /dev/stdin <<< 'kubectl get pods -A'",
            ". /dev/stdin <<< 'kubectl get pods -A'",
            "cat /etc//nacfson-mcp/kubeconfig",
            "cat /etc/./nacfson-mcp/kubeconfig",
            "cat //etc/nacfson-mcp/kubeconfig",
            "cat /etc/systemd/../nacfson-mcp/kubeconfig",
            "echo x | xargs --max-lines kubectl get pods",
            "echo x | xargs --max-lines 1 kubectl get pods",
            "env - kubectl get pods",
            "env -a foo kubectl get pods",
            "env --argv0 foo kubectl get pods",
            "timeout inf kubectl get pods",
            "timeout infinity kubectl get pods",
            "timeout +5 kubectl get pods",
            "timeout .5 kubectl get pods",
            "timeout 1e2 kubectl get pods",
            "ag -t . /etc/nacfson-mcp/kubeconfig",
            "ack -t . /etc/nacfson-mcp/kubeconfig",
            "cat /etc/nacfson-mcp/kubeconfig",
            "KUBECONFIG=/etc/nacfson-mcp/kubeconfig python3 -c 'print(1)'",
            "echo hi > /etc/nacfson-mcp/kubeconfig",
            "nacfson-mcp --serve-unix /tmp/x.sock --kubeconfig /etc/nacfson-mcp/kubeconfig",
            "watch kubectl get po",
            "watch -n 2 kubectl get po",
            'watch -n2 "kubectl get po"',
            "watch -d kubectl get po",
            'watch -q 5 "echo ok; ssh host"',
            "watch -x kubectl get po",
            "watch -cxn 2 /usr/bin/kubectl get po",
            'watch -x bash -c "terraform apply"',
            r"find . -maxdepth 0 -exec ssh host \;",
            r"find . -name x -exec kubectl get po \;",
            "find . -exec kubectl get po ';'",
            "find . -exec kubectl get po {} +",
            r"find . -execdir helm list \;",
            r"find . -ok flux get all \;",
            r"find . -okdir ssh host \;",
            r"find . -exec echo {} \; -exec kubectl get po \;",
            r"find . -exec echo {} + -exec kubectl get po \;",
            "find . -exec sh -c 'kubectl get po' _ {} +",
            r"find . -exec watch kubectl get po \;",
            r'find . -name "-exec" -exec kubectl get po \;',
            r"find . -exec sh -c + 'kubectl get po' \;",
            "flock /tmp/x kubectl get po",
            "flock -n -w 5 /tmp/x kubectl get po",
            'flock /tmp/x -c "kubectl get po"',
            'flock /tmp/x --command="echo ok && ssh host"',
            'flock -c "kubectl get po" /tmp/x',
            'flock -nc "kubectl get po" /tmp/x',
            "flock -- /tmp/x kubectl get po",
            "flock /etc/nacfson-mcp/kubeconfig grep -n foo docs/",
            'script -qc "kubectl get po" /dev/null',
            'script -c "kubectl get po" /dev/null',
            'script --command "kubectl get po" /dev/null',
            'script --command="echo ok; ssh host" /dev/null',
            'script -t -qc "kubectl get po" /dev/null',
            'script -T /tmp/t -qc "kubectl get po" /dev/null',
            'script -q /dev/null <<< "kubectl get po"',
            "script -q /dev/null kubectl get po",
            'timeout 5 script -qc "kubectl get po" /dev/null',
            "parallel kubectl ::: get",
            "parallel -j 4 kubectl get {} ::: pods",
            "parallel 'kubectl get {}' ::: pods",
            'parallel -j 2 "echo ok; ssh host" ::: 1',
            'parallel ::: "kubectl get pods" "echo ok"',
            "parallel bash -c ::: 'kubectl get pods'",
            "parallel {} get pods ::: kubectl",
            "parallel -q bash -c 'terraform apply' ::: 1",
            'parallel -- "echo ok" "kubectl get pods"',
            "parallel grep -n foo ::: /etc/nacfson-mcp/kubeconfig",
            "sem -j 2 kubectl get pods",
            "sem --id job1 -j 2 'kubectl get pods'",
            "chrt 50 kubectl get po",
            "chrt -f 50 kubectl get po",
            "chrt -T 1000 -P 2000 -D 2000 -d 0 kubectl get po",
            "taskset 0x1 kubectl get po",
            "taskset -c 0,2-3 kubectl get po",
            "taskset --cpu-list 0-7:2 kubectl get po",
            "unbuffer kubectl get po",
            "unbuffer -p kubectl get po",
            "doas kubectl get po",
            "runuser -u ubuntu -- kubectl get po",
            "run0 kubectl get po",
            "dzdo kubectl get po",
            "pbrun kubectl get po",
            "pfexec kubectl get po",
            "setpriv --reuid 1000 kubectl get po",
            "nsenter -t 1 -n kubectl get po",
            "unshare -r -n kubectl get po",
            "chroot /mnt kubectl get po",
            "prlimit --nofile=1024 kubectl get po",
            "setarch x86_64 kubectl get po",
            "ifne kubectl get po",
            "chronic kubectl get po",
            "eatmydata kubectl get po",
            "xargs -a /etc/nacfson-mcp/kubeconfig grep -n foo docs/",
        ):
            self.assert_blocked(cmd)

    def test_allows_offline_scripts_quoted_search_and_escape_hatch(self) -> None:
        for cmd in (
            "git status -sb",
            'git commit -m "docs: document kubectl guard and /etc/nacfson-mcp boundary"',
            'git log -S "/etc/nacfson-mcp" --oneline',
            'grep -rn "kubectl get secrets" docs/',
            'grep -rn "/etc/nacfson-mcp" docs/',
            "rg -n '/etc/nacfson-mcp/kubeconfig' ARCHITECTURE.md",
            "rg -n 'nacfson-mcp --serve-unix' infra/",
            "./scripts/ci/render-validate.sh gke",
            "python3 scripts/ci/platform-preflight.py /tmp/x.yaml",
            "python3 scripts/hooks/check-arch-doc-sync.py --worktree",
            "python3 scripts/hooks/review-cycle.py verify --scope staged",
            "kustomize build deploy/platform/vault",
            "kubeconform -strict /tmp/x.yaml",
            'rg --color auto "/etc/nacfson-mcp" docs/',
            'rg -T py "/etc/nacfson-mcp" docs/',
            'timeout 5 grep -rn "kubectl" docs/',
            "/usr/local/bin/nacfson-mcp --connect /run/nacfson-mcp/mcp.sock",
            "./scripts/mcp/mcp-session.sh status",
            "(cd services/debug-mcp && go test -v ./...)",
            "{ echo ok; }",
            "python3 -m unittest discover tests/hooks",
            'watch -n 2 "git status"',
            "watch -x git status",
            'watch -n 2 "grep -rn \'kubectl\' docs/"',
            'watch -x grep -rn "kubectl" docs/',
            'watch -n 2 "grep -rn \'/etc/nacfson-mcp\' docs/"',
            'find . -name "*.yaml"',
            'find . -name "*kubectl*" -print',
            'find . -name "-exec" -print',
            'find . -type f -exec grep -n "kubectl" {} +',
            r"find . -maxdepth 1 -exec echo {} \;",
            "flock /tmp/x git status",
            'flock /tmp/x -c "git status"',
            'flock -c "git status" /tmp/x',
            "flock 9",
            'script -qc "git status" /dev/null',
            "script -q /dev/null",
            "parallel -j 4 echo ::: a b c",
            "parallel echo ::: kubectl",
            "parallel grep -n kubectl ::: docs/a.md",
            'parallel grep -n "/etc/nacfson-mcp" ::: docs/a.md',
            "sem --wait",
            "sem --id job1 --wait",
            "sem -j 2 git status",
            "chrt -o 0 git status",
            "chrt -p 1234",
            "taskset -c 0 python3 -m unittest discover tests/hooks",
            "taskset -p 700",
            "unbuffer go test ./...",
            "setpriv --no-new-privs git status",
            "prlimit --nofile=1024 python3 -m unittest discover tests/hooks",
            "chronic git status",
            "ifne cat",
        ):
            self.assert_allowed(cmd)

        for inline_bypass in (
            "ALLOW_CLUSTER_CLI=1 kubectl get pods -A",
            "env ALLOW_CLUSTER_CLI=1 kubectl get pods -A",
            "ALLOW_CLUSTER_CLI=1 sudo ./scripts/mcp/refresh-kubeconfig.sh local-k3s",
            "ALLOW_CLUSTER_CLI=1 gcloud container clusters list",
            "echo ALLOW_CLUSTER_CLI=1 && kubectl get pods -A",
        ):
            self.assert_blocked(inline_bypass)

        self.assert_allowed("kubectl get pods -A", {"ALLOW_CLUSTER_CLI": "1"})
        ok_msg, reason_msg = guard.check_command("kubectl get pods -A", {})
        self.assertFalse(ok_msg)
        self.assertIn("nacfson-debug", reason_msg)
        self.assertIn("cluster_overview", reason_msg)
        self.assertNotIn("ALLOW_CLUSTER_CLI", reason_msg)

    def test_cli_entrypoint_and_universal_agent_hook_schemas(self) -> None:
        clean_env = {k: v for k, v in os.environ.items() if k != "ALLOW_CLUSTER_CLI"}

        proc_ok = subprocess.run(
            [sys.executable, str(GUARD_SCRIPT), "--command", "git status"],
            env=clean_env,
            text=True,
            capture_output=True,
            check=False,
        )
        self.assertEqual(proc_ok.returncode, 0)

        proc_deny = subprocess.run(
            [sys.executable, str(GUARD_SCRIPT), "--command", "kubectl get pods"],
            env=clean_env,
            text=True,
            capture_output=True,
            check=False,
        )
        self.assertEqual(proc_deny.returncode, 2)
        self.assertIn("[CLUSTER-CLI GUARD]", proc_deny.stderr)
        self.assertIn("nacfson-debug", proc_deny.stderr)

        ag_proc = subprocess.run(
            [sys.executable, str(HOOK_SCRIPT), "--agent", "antigravity", "--event", "PreToolUse"],
            env=clean_env,
            input=json.dumps({"toolInput": {"CommandLine": "ssh vm01"}}),
            text=True,
            capture_output=True,
            check=False,
        )
        self.assertEqual(ag_proc.returncode, 0)
        ag_out = json.loads(ag_proc.stdout)
        self.assertEqual(ag_out["decision"], "deny")
        self.assertIn("[CLUSTER-CLI GUARD]", ag_out["reason"])

        cl_proc = subprocess.run(
            [sys.executable, str(HOOK_SCRIPT), "--agent", "claude", "--event", "PreToolUse"],
            env=clean_env,
            input=json.dumps({"tool_input": {"command": "kubectl get pods"}}),
            text=True,
            capture_output=True,
            check=False,
        )
        self.assertEqual(cl_proc.returncode, 2)
        cl_out = json.loads(cl_proc.stdout)
        self.assertEqual(cl_out["decision"], "block")
        self.assertIn("[CLUSTER-CLI GUARD]", cl_out["reason"])

        cl_inline_proc = subprocess.run(
            [sys.executable, str(HOOK_SCRIPT), "--agent", "claude", "--event", "PreToolUse"],
            env=clean_env,
            input=json.dumps({"tool_input": {"command": "ALLOW_CLUSTER_CLI=1 kubectl get pods"}}),
            text=True,
            capture_output=True,
            check=False,
        )
        self.assertEqual(cl_inline_proc.returncode, 2)
        cl_inline_out = json.loads(cl_inline_proc.stdout)
        self.assertEqual(cl_inline_out["decision"], "block")
        self.assertIn("[CLUSTER-CLI GUARD]", cl_inline_out["reason"])

        allow_proc = subprocess.run(
            [sys.executable, str(HOOK_SCRIPT), "--agent", "claude", "--event", "PreToolUse"],
            env={**clean_env, "ALLOW_CLUSTER_CLI": "1"},
            input=json.dumps({"tool_input": {"command": "kubectl get pods"}}),
            text=True,
            capture_output=True,
            check=False,
        )
        self.assertEqual(allow_proc.returncode, 0)
        self.assertEqual(json.loads(allow_proc.stdout), {})


if __name__ == "__main__":
    unittest.main()
