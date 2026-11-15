import gzip
import hashlib
import io
import json
import os
import shutil
import subprocess
import tarfile
import tempfile
import unittest

REPO = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
SCRIPT = os.path.join(REPO, "scripts", "cutover-bundle.sh")
SCRIPT_LIB = os.path.join(REPO, "scripts", "lib")
STATUS_FIXTURES = os.path.join(REPO, "tests", "fixtures", "openbao", "2.0.0", "status")

LIVE_HCL = """ui = false

storage "raft" {
  path = "/var/lib/openbao/data"
  node_id = "openbao-0"
}

listener "tcp" {
  address = "0.0.0.0:8200"
  cluster_address = "0.0.0.0:8201"
  tls_disable = 1
}

seal "gcpckms" {
  project    = "nacfson-pipeline"
  region     = "asia-northeast3"
  key_ring   = "nacfson-vault"
  crypto_key = "openbao-unseal"
}

api_addr = "http://openbao-service.vault.svc:8200"
cluster_addr = "http://openbao-0.openbao-internal.vault.svc:8201"
"""

KUBECTL_STUB = r"""#!/usr/bin/env bash
set -u
STATE="__STATE__"
printf '%s\n' "$*" >> "$STATE/calls.log"
args=("$@")
while [[ "${args[0]:-}" == --as* ]]; do args=("${args[@]:1}"); done
joined="${args[*]}"
case "$joined" in
  "get configmap openbao-config -n vault -o jsonpath="*)
    cat "$STATE/live.hcl"
    ;;
  "patch configmap openbao-config -n vault --type=merge -p "*)
    printf '%s\n' "${args[-1]}" >> "$STATE/cm-patches.log"
    ;;
  *"bao status -format=json"*)
    bao_state="$(cat "$STATE/bao-state")"
    if [[ "$bao_state" == "unreachable" ]]; then
      echo 'error: unable to upgrade connection: container not found ("openbao")' >&2
      exit 1
    fi
    cat "__FIXTURES__/$bao_state.json"
    case "$bao_state" in
      unsealed-*) exit 0 ;;
      *) exit 2 ;;
    esac
    ;;
  "delete pod -n vault openbao-0"*)
    if [[ -f "$STATE/cm-patches.log" ]]; then
      config="$(tail -n 1 "$STATE/cm-patches.log")"
    else
      config="$(cat "$STATE/live.hcl")"
    fi
    kms="false"
    [[ "$config" == *gcpckms* ]] && kms="true"
    case "$(cat "$STATE/bao-state")" in
      sealed-shamir | unsealed-shamir | sealed-migration)
        if [[ "$kms" == "true" ]]; then echo sealed-migration; else echo sealed-shamir; fi > "$STATE/bao-state"
        ;;
      sealed-auto | unsealed-auto)
        if [[ "$kms" == "true" ]]; then echo unsealed-auto; else echo unreachable; fi > "$STATE/bao-state"
        ;;
    esac
    ;;
  *"bao operator init"*)
    echo sealed-shamir > "$STATE/bao-state"
    echo '{"unseal_keys_b64":["temp-key"],"root_token":"temp-root"}'
    ;;
  *"bao operator unseal -migrate"*)
    echo unsealed-auto > "$STATE/bao-state"
    ;;
  *"bao operator unseal"*)
    echo unsealed-shamir > "$STATE/bao-state"
    ;;
  "exec -i -n vault openbao-0 -- tee /tmp/openbao.snap")
    cat > /dev/null
    ;;
  "get configmap cutover-restore-state -n vault -o jsonpath="*)
    [[ -f "$STATE/marker" ]] && cat "$STATE/marker"
    ;;
  "create configmap cutover-restore-state "*)
    for a in "${args[@]}"; do
      [[ "$a" == --from-literal=bundle-sha256=* ]] && printf 'bundle-sha256: %s\n' "${a#--from-literal=bundle-sha256=}"
    done
    ;;
  "apply -f "*)
    if grep -q '^bundle-sha256: ' "${args[-1]}" 2>/dev/null; then
      sed -n 's/^bundle-sha256: //p' "${args[-1]}" | tr -d '\n' > "$STATE/marker"
    fi
    ;;
esac
exit 0
"""

HELPER_STUB = """#!/usr/bin/env bash
printf '%s %s\\n' "$(basename "$0")" "$*" >> "__STATE__/calls.log"
exit 0
"""


def _write_executable(path, body):
    with open(path, "w", encoding="utf-8") as fh:
        fh.write(body)
    os.chmod(path, 0o755)


def _gz_bytes(text):
    buf = io.BytesIO()
    with gzip.GzipFile(fileobj=buf, mode="wb", mtime=0) as gz:
        gz.write(text.encode())
    return buf.getvalue()


def _make_bundle(bundle_dir):
    os.makedirs(bundle_dir, mode=0o700)
    os.chmod(bundle_dir, 0o700)
    files = {
        "openbao.snap": b"snapshot",
        "keycloak.dump.gz": _gz_bytes("keycloak"),
        "proj_pn.dump.gz": _gz_bytes("pn"),
        "proj_health.dump.gz": _gz_bytes("health"),
        "edge-tls-secrets.yaml": b"apiVersion: v1\nkind: Secret\nmetadata:\n  name: auth-tls\n  namespace: platform\n",
    }
    tar_buf = io.BytesIO()
    with tarfile.open(fileobj=tar_buf, mode="w") as tar:
        info = tarfile.TarInfo("logs.txt")
        data = b"log"
        info.size = len(data)
        tar.addfile(info, io.BytesIO(data))
    gz_buf = io.BytesIO()
    with gzip.GzipFile(fileobj=gz_buf, mode="wb", mtime=0) as gz:
        gz.write(tar_buf.getvalue())
    files["victoria-logs-data.tar.gz"] = gz_buf.getvalue()
    lines = []
    for name, data in files.items():
        with open(os.path.join(bundle_dir, name), "wb") as fh:
            fh.write(data)
        lines.append(f"{hashlib.sha256(data).hexdigest()}  {name}")
    with open(os.path.join(bundle_dir, "manifest.sha256"), "w", encoding="utf-8") as fh:
        fh.write("\n".join(lines) + "\n")


class CutoverRestoreOrderTest(unittest.TestCase):
    def _run_restore(self, target, extra_args=(), bao_state="uninitialized", marker=None):
        tmp = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, tmp)
        repo = os.path.join(tmp, "repo")
        scripts = os.path.join(repo, "scripts")
        os.makedirs(scripts)
        shutil.copy(SCRIPT, os.path.join(scripts, "cutover-bundle.sh"))
        shutil.copytree(SCRIPT_LIB, os.path.join(scripts, "lib"))
        state = os.path.join(tmp, "state")
        os.makedirs(state)
        with open(os.path.join(state, "live.hcl"), "w", encoding="utf-8") as fh:
            fh.write(LIVE_HCL)
        with open(os.path.join(state, "bao-state"), "w", encoding="utf-8") as fh:
            fh.write(bao_state)
        if marker is not None:
            with open(os.path.join(state, "marker"), "w", encoding="utf-8") as fh:
                fh.write(marker)
        for helper in ("db-migrate-dump.sh", "vault-configure-k8s-auth.sh", "vault-reconcile.sh"):
            _write_executable(os.path.join(scripts, helper), HELPER_STUB.replace("__STATE__", state))
        bin_dir = os.path.join(tmp, "bin")
        os.makedirs(bin_dir)
        _write_executable(os.path.join(bin_dir, "kubectl"), KUBECTL_STUB.replace("__STATE__", state).replace("__FIXTURES__", STATUS_FIXTURES))
        bundle = os.path.join(tmp, "bundle")
        _make_bundle(bundle)
        env = {
            **os.environ,
            "PATH": f"{bin_dir}:{os.environ['PATH']}",
            "VAULT_TOKEN": "old-root",
            "UNSEAL_KEY": "old-unseal",
            "CUTOVER_POLL_SECONDS": "0",
        }
        result = subprocess.run(
            ["bash", os.path.join(scripts, "cutover-bundle.sh"), "restore", "--bundle-dir", bundle,
             "--target", target, "--yes", *extra_args],
            capture_output=True, text=True, env=env, timeout=60,
        )
        calls_path = os.path.join(state, "calls.log")
        calls = open(calls_path, encoding="utf-8").read().splitlines() if os.path.exists(calls_path) else []
        patches_path = os.path.join(state, "cm-patches.log")
        patches = open(patches_path, encoding="utf-8").read().splitlines() if os.path.exists(patches_path) else []
        marker_path = os.path.join(state, "marker")
        self.marker = open(marker_path, encoding="utf-8").read() if os.path.exists(marker_path) else None
        with open(os.path.join(bundle, "manifest.sha256"), "rb") as fh:
            self.bundle_sha = hashlib.sha256(fh.read()).hexdigest()
        return result, calls, patches

    @staticmethod
    def _index(calls, needle):
        for i, call in enumerate(calls):
            if needle in call:
                return i
        raise AssertionError(f"call containing {needle!r} not found in:\n" + "\n".join(calls))

    def test_gke_restore_order_lets_postgres_start_before_database_load(self):
        result, calls, _ = self._run_restore("gke")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        idx = lambda needle: self._index(calls, needle)
        suspend_vault = idx('patch kustomization vault -n flux-system --type=merge -p {"spec":{"suspend":true}}')
        suspend_identity = idx('patch kustomization identity -n flux-system --type=merge -p {"spec":{"suspend":true}}')
        snapshot = idx("raft snapshot restore")
        migrate = idx("bao operator unseal -migrate")
        resume_vault = idx('patch kustomization vault -n flux-system --type=merge -p {"spec":{"suspend":false}}')
        wait_database = idx("wait kustomization/database -n flux-system --for=condition=Ready")
        first_db_load = idx("db-migrate-dump.sh --keycloak")
        resume_identity = idx('patch kustomization identity -n flux-system --type=merge -p {"spec":{"suspend":false}}')
        wait_identity = idx("wait kustomization/identity -n flux-system --for=condition=Ready")
        self.assertLess(suspend_vault, snapshot)
        self.assertLess(suspend_identity, resume_vault)
        self.assertLess(snapshot, migrate)
        self.assertLess(migrate, resume_vault)
        self.assertLess(resume_vault, wait_database)
        self.assertLess(wait_database, first_db_load)
        self.assertLess(first_db_load, resume_identity)
        self.assertLess(resume_identity, wait_identity)

    def test_gke_seal_configs_keep_the_live_storage_path(self):
        result, _, patches = self._run_restore("gke")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(len(patches), 2, patches)
        shamir_hcl = json.loads(patches[0])["data"]["openbao.hcl"]
        kms_hcl = json.loads(patches[1])["data"]["openbao.hcl"]
        self.assertNotIn('seal "gcpckms"', shamir_hcl)
        self.assertIn('path = "/var/lib/openbao/data"', shamir_hcl)
        self.assertEqual(kms_hcl, LIVE_HCL)
        self.assertNotIn("/openbao/data\"", kms_hcl.replace("/var/lib/openbao/data", ""))

    def test_gke_rejects_no_migrate_kms(self):
        result, calls, _ = self._run_restore("gke", ["--no-migrate-kms"])
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("--no-migrate-kms", result.stderr)
        self.assertFalse(any("suspend" in c for c in calls), calls)

    def test_gke_migration_is_detected_from_the_recovery_seal(self):
        result, _, _ = self._run_restore("gke")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("PASS: openbao-0 runs on unattended Cloud KMS auto-unseal", result.stdout)
        self.assertEqual(self.marker, self.bundle_sha)

    def test_gke_rerun_after_migration_resumes_without_touching_openbao(self):
        result, calls, patches = self._run_restore("gke", bao_state="unsealed-auto")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("[4/7] Skipped", result.stdout)
        self.assertEqual(patches, [])
        self.assertFalse(any("raft snapshot restore" in c or "unseal" in c or "delete pod" in c for c in calls), calls)
        self.assertLess(self._index(calls, "vault-configure-k8s-auth.sh"), self._index(calls, "db-migrate-dump.sh --keycloak"))
        self.assertEqual(self.marker, self.bundle_sha)

    def test_gke_rerun_with_the_same_bundle_marker_resumes(self):
        self._run_restore("gke")
        same = self.bundle_sha
        result, calls, _ = self._run_restore("gke", bao_state="unsealed-auto", marker=same)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertFalse(any("raft snapshot restore" in c for c in calls), calls)

    def test_gke_refuses_to_resume_onto_data_from_another_bundle(self):
        result, calls, patches = self._run_restore("gke", bao_state="unsealed-auto", marker="0" * 64)
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertIn("another bundle", result.stderr)
        self.assertEqual(patches, [])
        self.assertFalse(any("suspend" in c or "apply -f" in c for c in calls), calls)

    def _assert_read_only(self, calls):
        for call in calls:
            self.assertTrue(
                call.startswith("get configmap openbao-config") or "bao status -format=json" in call,
                f"mutating call before the status pre-check passed: {call!r}\n" + "\n".join(calls),
            )

    def test_gke_aborts_before_any_mutation_when_status_is_unreachable(self):
        result, calls, patches = self._run_restore("gke", bao_state="unreachable")
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertIn("cannot read the openbao-0 status", result.stderr)
        self.assertNotIn("Traceback", result.stderr)
        self.assertEqual(patches, [])
        self._assert_read_only(calls)

    def test_gke_refuses_the_shamir_switch_for_sealed_kms_data(self):
        result, calls, patches = self._run_restore("gke", bao_state="sealed-auto")
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertIn("openbao-0 is 'sealed-auto'", result.stderr)
        self.assertEqual(patches, [])
        self._assert_read_only(calls)

    def test_gke_takes_the_shamir_switch_from_every_shamir_state(self):
        for state in ("uninitialized", "sealed-shamir", "unsealed-shamir", "sealed-migration"):
            with self.subTest(state=state):
                result, _, patches = self._run_restore("gke", bao_state=state)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertEqual(len(patches), 2, patches)

    def test_local_k3s_stops_before_restoring_when_status_stays_unreachable(self):
        result, calls, _ = self._run_restore("local-k3s", bao_state="unreachable")
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertIn("'unreachable' before the snapshot restore", result.stderr)
        self.assertFalse(any("operator init" in c or "snapshot restore" in c for c in calls), calls)

    def test_local_k3s_restore_does_not_touch_seal_config(self):
        result, calls, patches = self._run_restore("local-k3s")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(patches, [])
        self.assertFalse(any("unseal -migrate" in c for c in calls), calls)
        self.assertLess(self._index(calls, "wait kustomization/database -n flux-system --for=condition=Ready"),
                        self._index(calls, "db-migrate-dump.sh --keycloak"))


if __name__ == "__main__":
    unittest.main()
