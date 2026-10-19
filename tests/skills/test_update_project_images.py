#!/usr/bin/env python3
"""Unit tests for the update-project-images skill helper."""

from __future__ import annotations

import importlib.util
import json
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

BASE_DIR = Path(__file__).resolve().parents[2]
SCRIPT_PATH = (
    BASE_DIR
    / ".agents"
    / "skills"
    / "update-project-images"
    / "scripts"
    / "update_project_images.py"
)


def load_module():
    spec = importlib.util.spec_from_file_location("update_project_images", SCRIPT_PATH)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"cannot load {SCRIPT_PATH}")
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


mod = load_module()


FRONTEND_OLD = "ghcr.io/nacfson/project-pn-frontend@sha256:" + "84af4c4c" + "0" * 56
BACKEND_OLD = "ghcr.io/nacfson/project-pn-backend@sha256:" + "00e44572" + "0" * 56
NEW_DIGEST = "sha256:" + "ab" * 32
NEW_ARM_DIGEST = "sha256:" + "cd" * 32

DEPLOYMENT = """\
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {name}
spec:
  template:
    spec:
      containers:
        - name: {container}
          image: {image}
          ports:
            - containerPort: {port}
"""


def write_project(root: Path, slug: str, role: str, image: str, port: int) -> Path:
    workload = root / "deploy" / "projects" / slug / "workloads"
    workload.mkdir(parents=True, exist_ok=True)
    path = workload / f"{role}-deployment.yaml"
    path.write_text(
        DEPLOYMENT.format(
            name=f"{slug}-{role}", container=role, image=image, port=port
        ),
        encoding="utf-8",
    )
    (workload / "kustomization.yaml").write_text(
        f"resources:\n  - {role}-deployment.yaml\n", encoding="utf-8"
    )
    return path


def write_html(root: Path) -> Path:
    path = root / "docs" / "platform-architecture.html"
    path.parent.mkdir(parents=True)
    path.write_text(
        f"""\
<html><body>
<g class="node-box" id="node-pn-frontend">
  <text>Pinned @sha256:84af4c4c... · NET_BIND_SERVICE</text>
</g>
      'pn-frontend': {{
        image: '{FRONTEND_OLD[:len(FRONTEND_OLD) - 56]}...',
        full: '{FRONTEND_OLD}',
        desc: 'nested {{ brace }}',
      }},
      'pn-other': {{
        image: '{FRONTEND_OLD[:len(FRONTEND_OLD) - 56]}...',
      }},
<!-- PROJECT_IMAGE_PINS:START -->
<table><tbody></tbody></table>
<!-- PROJECT_IMAGE_PINS:END -->
</body></html>
""",
        encoding="utf-8",
    )
    return path


class TempRepo(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        self.frontend = write_project(
            self.root, "pn", "frontend", FRONTEND_OLD, 80
        )
        self.backend = write_project(self.root, "pn", "backend", BACKEND_OLD, 8080)
        write_project(
            self.root,
            "health",
            "frontend",
            "ghcr.io/nacfson/health-frontend@sha256:" + "11" * 32,
            80,
        )
        write_project(
            self.root,
            "health",
            "backend",
            "ghcr.io/nacfson/health-backend@sha256:" + "22" * 32,
            8080,
        )
        self.html = write_html(self.root)

    def tearDown(self):
        self.tmp.cleanup()


class TestLinkParsing(unittest.TestCase):
    def test_actions_run_url(self):
        link = mod.parse_link(
            "https://github.com/nacfson/Project_PN/actions/runs/39259800181"
        )
        self.assertEqual(link.kind, "actions_run")
        self.assertEqual(link.owner, "nacfson")
        self.assertEqual(link.repo, "Project_PN")
        self.assertEqual(link.run_id, "39259800181")

    def test_actions_job_url_uses_run_id(self):
        link = mod.parse_link(
            "https://github.com/nacfson/Project_PN/actions/runs/39259800181/job/118084959441"
        )
        self.assertEqual(link.kind, "actions_run")
        self.assertEqual(link.run_id, "39259800181")

    def test_ghcr_package_url(self):
        link = mod.parse_link(
            "https://github.com/nacfson/Project_PN/pkgs/container/project-pn-frontend/123456?tag=9b1c473"
        )
        self.assertEqual(link.kind, "image")
        self.assertEqual(link.image, "ghcr.io/nacfson/project-pn-frontend:9b1c473")

    def test_user_package_url(self):
        link = mod.parse_link(
            "https://github.com/users/nacfson/packages/container/project-pn-backend?tag=9b1c473"
        )
        self.assertEqual(link.kind, "image")
        self.assertEqual(link.image, "ghcr.io/nacfson/project-pn-backend:9b1c473")

    def test_direct_image_ref(self):
        link = mod.parse_link("ghcr.io/nacfson/project-pn-frontend:9b1c473")
        self.assertEqual(link.kind, "image")
        self.assertEqual(link.image, "ghcr.io/nacfson/project-pn-frontend:9b1c473")

    def test_direct_digest_ref(self):
        link = mod.parse_link(f"ghcr.io/nacfson/project-pn-frontend@{NEW_DIGEST}")
        self.assertEqual(link.kind, "image")
        self.assertEqual(link.image, f"ghcr.io/nacfson/project-pn-frontend@{NEW_DIGEST}")

    def test_bare_digest(self):
        link = mod.parse_link(NEW_DIGEST)
        self.assertEqual(link.kind, "digest")
        self.assertEqual(link.digest, NEW_DIGEST)

    def test_package_url_without_tag_is_rejected(self):
        with self.assertRaises(mod.UpdateError):
            mod.parse_link(
                "https://github.com/nacfson/Project_PN/pkgs/container/project-pn-frontend"
            )


class TestTargetDiscovery(TempRepo):
    def test_normalizes_proj_prefix(self):
        self.assertEqual(mod.normalize_project("proj-pn"), "pn")
        self.assertEqual(mod.normalize_project("pn"), "pn")

    def test_finds_frontend_container(self):
        target = mod.discover_target(self.root, "proj-pn", "frontend")
        self.assertEqual(target.project, "pn")
        self.assertEqual(target.deployment, "pn-frontend")
        self.assertEqual(target.container, "frontend")
        self.assertEqual(target.old_image, FRONTEND_OLD)
        self.assertEqual(target.container_port, 80)

    def test_repo_mismatch_blocks(self):
        target = mod.discover_target(self.root, "pn", "frontend")
        link = mod.parse_link(
            f"ghcr.io/nacfson/project-pn-backend@{NEW_DIGEST}"
        )
        with self.assertRaises(mod.UpdateError):
            mod.resolve_link(target, link)

    def test_invalid_container_port_raises_update_error(self):
        workload = self.root / "deploy" / "projects" / "bad" / "workloads"
        workload.mkdir(parents=True)
        (workload / "frontend-deployment.yaml").write_text(
            DEPLOYMENT.format(
                name="bad-frontend",
                container="frontend",
                image="ghcr.io/nacfson/bad-frontend@sha256:" + "55" * 32,
                port='"eighty"',
            ),
            encoding="utf-8",
        )
        with self.assertRaises(mod.UpdateError):
            mod.discover_target(self.root, "bad", "frontend")

    def test_ambiguous_role_raises(self):
        workload = self.root / "deploy" / "projects" / "pn" / "workloads"
        (workload / "other-frontend.yaml").write_text(
            DEPLOYMENT.format(
                name="pn-other-frontend",
                container="frontend",
                image="ghcr.io/nacfson/other@sha256:" + "33" * 32,
                port=80,
            ),
            encoding="utf-8",
        )
        with self.assertRaises(mod.UpdateError):
            mod.discover_target(self.root, "pn", "frontend")


class TestImageReplacement(TempRepo):
    def test_replaces_only_the_image_value(self):
        new_image = f"ghcr.io/nacfson/project-pn-frontend@{NEW_DIGEST}"
        mod.replace_image(self.frontend, FRONTEND_OLD, new_image)
        text = self.frontend.read_text(encoding="utf-8")
        self.assertIn(f"image: {new_image}", text)
        self.assertNotIn(FRONTEND_OLD, text)
        self.assertIn("containerPort: 80", text)

    def test_missing_old_image_raises(self):
        with self.assertRaises(mod.UpdateError):
            mod.replace_image(
                self.frontend, "ghcr.io/nacfson/missing@sha256:" + "44" * 32, "x"
            )


class TestHtmlUpdate(TempRepo):
    def test_table_and_references_are_synchronized(self):
        target = mod.discover_target(self.root, "pn", "frontend")
        update = mod.ImageUpdate(
            target=target,
            source="https://github.com/nacfson/Project_PN/actions/runs/1",
            tag="9b1c473",
            digest=NEW_DIGEST,
            new_image=f"ghcr.io/nacfson/project-pn-frontend@{NEW_DIGEST}",
            validation=mod.ValidationReport(
                platforms=["linux/amd64", "linux/arm64"]
            ),
        )
        changed = mod.update_html(self.root, [update])
        html = self.html.read_text(encoding="utf-8")
        self.assertTrue(changed)
        self.assertIn(NEW_DIGEST, html)
        self.assertNotIn(FRONTEND_OLD, html)
        self.assertIn("Pinned @sha256:abababab...", html)
        self.assertIn("proj-health", html)
        self.assertIn("project-pn-backend", html)
        self.assertIn("health-frontend", html)
        # A different workload sharing the old digest must not be rewritten.
        other_block = html.split("'pn-other':", 1)[1].split("},", 1)[0]
        self.assertIn("84af4c4c...", other_block)


class TestImageValidation(unittest.TestCase):
    def manifest(self, arches):
        return json.dumps(
            {
                "schemaVersion": 2,
                "mediaType": "application/vnd.oci.image.index.v1+json",
                "manifests": [
                    {
                        "digest": "sha256:" + ("aa" if arch == "amd64" else "bb") * 32,
                        "platform": {"os": "linux", "architecture": arch},
                    }
                    for arch in arches
                ],
            }
        )

    def config(self, arch, env=None, ports=None):
        return json.dumps(
            {
                "architecture": arch,
                "os": "linux",
                "rootfs": {"diff_ids": ["sha256:" + "cc" * 32]},
                "config": {
                    "Entrypoint": ["/app"],
                    "Env": env or ["PORT=8080"],
                    "ExposedPorts": ports or {},
                },
            }
        )

    def run_command(self, cmd, check=False):
        joined = " ".join(cmd)
        if "--raw" in cmd:
            if "@sha256:aa" in joined or "@sha256:bb" in joined:
                return mock.Mock(returncode=0, stdout=self.manifest(["amd64", "arm64"]), stderr="")
            return mock.Mock(returncode=0, stdout=self.manifest(["amd64", "arm64"]), stderr="")
        if "--config" in cmd:
            arch = "arm64" if "@sha256:bb" in joined else "amd64"
            return mock.Mock(returncode=0, stdout=self.config(arch), stderr="")
        raise AssertionError(f"unexpected command: {joined}")

    def test_accepts_multiarch(self):
        with mock.patch.object(mod, "run_command", side_effect=self.run_command):
            report = mod.validate_image(
                "ghcr.io/nacfson/project-pn-frontend",
                NEW_DIGEST,
                container_port=80,
                expected_revision=None,
                strict=False,
                pull_check=False,
            )
        self.assertIn("linux/amd64", report.platforms)
        self.assertIn("linux/arm64", report.platforms)
        self.assertEqual(
            report.evidence["linux/amd64"]["entrypoint"], ["/app"]
        )
        self.assertEqual(
            report.evidence["linux/amd64"]["source"], "(missing)"
        )

    def test_rejects_single_arch(self):
        def single_arch(cmd, check=False):
            return mock.Mock(
                returncode=0, stdout=self.manifest(["amd64"]), stderr=""
            )

        with mock.patch.object(mod, "run_command", side_effect=single_arch):
            with self.assertRaises(mod.UpdateError):
                mod.validate_image(
                    "ghcr.io/nacfson/project-pn-frontend",
                    NEW_DIGEST,
                    container_port=80,
                    expected_revision=None,
                    strict=False,
                    pull_check=False,
                )

    def test_config_blob_unavailable_is_skipped(self):
        def raw_only(cmd, check=False):
            if "--raw" in cmd:
                return mock.Mock(
                    returncode=0, stdout=self.manifest(["amd64", "arm64"]), stderr=""
                )
            return mock.Mock(returncode=1, stdout="", stderr="denied")

        with mock.patch.object(mod, "run_command", side_effect=raw_only):
            report = mod.validate_image(
                "ghcr.io/nacfson/project-pn-frontend",
                NEW_DIGEST,
                container_port=80,
                expected_revision=None,
                strict=False,
                pull_check=False,
            )
        self.assertTrue(report.skipped)
        self.assertIn("manifest validation only", "\n".join(report.warnings))

    def test_strict_mode_rejects_unavailable_config(self):
        def raw_only(cmd, check=False):
            if "--raw" in cmd:
                return mock.Mock(
                    returncode=0, stdout=self.manifest(["amd64", "arm64"]), stderr=""
                )
            return mock.Mock(returncode=1, stdout="", stderr="denied")

        with mock.patch.object(mod, "run_command", side_effect=raw_only):
            with self.assertRaises(mod.UpdateError):
                mod.validate_image(
                    "ghcr.io/nacfson/project-pn-frontend",
                    NEW_DIGEST,
                    container_port=80,
                    expected_revision=None,
                    strict=True,
                    pull_check=False,
                )

    def test_source_repo_mismatch_blocks(self):
        def mismatched_source(cmd, check=False):
            if "--raw" in cmd:
                return mock.Mock(
                    returncode=0, stdout=self.manifest(["amd64", "arm64"]), stderr=""
                )
            arch = "arm64" if "@sha256:bb" in " ".join(cmd) else "amd64"
            config = json.loads(self.config(arch))
            config["config"]["Labels"] = {
                "org.opencontainers.image.source": "https://github.com/other/repo"
            }
            return mock.Mock(returncode=0, stdout=json.dumps(config), stderr="")

        with mock.patch.object(mod, "run_command", side_effect=mismatched_source):
            with self.assertRaises(mod.UpdateError):
                mod.validate_image(
                    "ghcr.io/nacfson/project-pn-frontend",
                    NEW_DIGEST,
                    container_port=80,
                    expected_revision=None,
                    strict=False,
                    pull_check=False,
                    expected_source_repo="nacfson/Project_PN",
                )

    def test_revision_label_mismatch_blocks(self):
        def mismatched(cmd, check=False):
            if "--raw" in cmd:
                return mock.Mock(
                    returncode=0, stdout=self.manifest(["amd64", "arm64"]), stderr=""
                )
            arch = "arm64" if "@sha256:bb" in " ".join(cmd) else "amd64"
            config = json.loads(self.config(arch))
            config["config"]["Labels"] = {
                "org.opencontainers.image.revision": "deadbeef"
            }
            return mock.Mock(returncode=0, stdout=json.dumps(config), stderr="")

        with mock.patch.object(mod, "run_command", side_effect=mismatched):
            with self.assertRaises(mod.UpdateError):
                mod.validate_image(
                    "ghcr.io/nacfson/project-pn-frontend",
                    NEW_DIGEST,
                    container_port=80,
                    expected_revision="9b1c473635e0fe45d67d2fbb0cfd887c96e9d308",
                    strict=False,
                    pull_check=False,
                )

    def test_warns_on_secret_env_and_port_mismatch(self):
        def config_with_secret(cmd, check=False):
            arch = "arm64" if "@sha256:bb" in " ".join(cmd) else "amd64"
            if "--raw" in cmd:
                return mock.Mock(
                    returncode=0, stdout=self.manifest(["amd64", "arm64"]), stderr=""
                )
            return mock.Mock(
                returncode=0,
                stdout=self.config(
                    arch,
                    env=["DB_PASSWORD=hunter2"],
                    ports={"9090/tcp": {}},
                ),
                stderr="",
            )

        with mock.patch.object(mod, "run_command", side_effect=config_with_secret):
            report = mod.validate_image(
                "ghcr.io/nacfson/project-pn-backend",
                NEW_DIGEST,
                container_port=8080,
                expected_revision=None,
                strict=False,
                pull_check=False,
            )
        joined = "\n".join(report.warnings)
        self.assertIn("DB_PASSWORD", joined)
        self.assertNotIn("hunter2", joined)
        self.assertIn("ExposedPorts", joined)


class TestActionsRunResolution(TempRepo):
    def test_uses_short_sha_tag_when_present(self):
        target = mod.discover_target(self.root, "pn", "frontend")
        link = mod.parse_link(
            "https://github.com/nacfson/Project_PN/actions/runs/39259800181"
        )
        calls = mock.Mock()
        calls.returncode = 0
        calls.stdout = json.dumps(
            {
                "headSha": "9b1c473635e0fe45d67d2fbb0cfd887c96e9d308",
                "headBranch": "main",
                "conclusion": "success",
                "workflowName": "Publish Project PN images",
                "jobs": [
                    {"name": "Build & Publish Frontend", "conclusion": "success"}
                ],
            }
        )
        with mock.patch.object(mod, "run_command", return_value=calls):
            with mock.patch.object(
                mod, "resolve_tag_digest", return_value=NEW_DIGEST
            ) as resolver:
                resolved = mod.resolve_link(target, link)
        resolver.assert_called_once_with(target.old_repo, "9b1c473")
        self.assertEqual(resolved.digest, NEW_DIGEST)
        self.assertEqual(resolved.tag, "9b1c473")
        self.assertEqual(
            resolved.expected_revision, "9b1c473635e0fe45d67d2fbb0cfd887c96e9d308"
        )

    def _run_with_jobs(self, jobs):
        result = mock.Mock()
        result.returncode = 0
        result.stdout = json.dumps(
            {
                "headSha": "9b1c473635e0fe45d67d2fbb0cfd887c96e9d308",
                "headBranch": "main",
                "conclusion": "success",
                "workflowName": "Publish images",
                "jobs": jobs,
            }
        )
        return result

    def test_unmatched_job_blocks_by_default(self):
        target = mod.discover_target(self.root, "pn", "frontend")
        link = mod.parse_link(
            "https://github.com/nacfson/Project_PN/actions/runs/39259800181"
        )
        with mock.patch.object(
            mod,
            "run_command",
            return_value=self._run_with_jobs(
                [{"name": "publish", "conclusion": "success"}]
            ),
        ):
            with self.assertRaises(mod.UpdateError):
                mod.resolve_link(target, link)

    def test_unmatched_job_can_be_allowed(self):
        target = mod.discover_target(self.root, "pn", "frontend")
        link = mod.parse_link(
            "https://github.com/nacfson/Project_PN/actions/runs/39259800181"
        )
        with mock.patch.object(
            mod,
            "run_command",
            return_value=self._run_with_jobs(
                [{"name": "publish", "conclusion": "success"}]
            ),
        ), mock.patch.object(mod, "resolve_tag_digest", return_value=NEW_DIGEST):
            resolved = mod.resolve_link(target, link, allow_unmatched_job=True)
        self.assertTrue(resolved.warnings)


class TestUpdateFlow(TempRepo):
    def test_dry_run_does_not_write(self):
        before = self.frontend.read_text(encoding="utf-8")
        with mock.patch.object(
            mod, "resolve_digest_ref", return_value=NEW_DIGEST
        ), mock.patch.object(
            mod,
            "validate_image",
            return_value=mod.ValidationReport(platforms=["linux/amd64", "linux/arm64"]),
        ):
            summary = mod.run_update(
                root=self.root,
                project="pn",
                image_args=[
                    f"frontend=ghcr.io/nacfson/project-pn-frontend@{NEW_DIGEST}"
                ],
                dry_run=True,
                strict=False,
                pull_check=False,
            )
        self.assertTrue(summary["dry_run"])
        self.assertTrue(summary["changed"])
        self.assertTrue(summary["html_changed"])
        self.assertEqual(self.frontend.read_text(encoding="utf-8"), before)
        self.assertFalse(summary["images"][0]["noop"])

    def test_alias_roles_do_not_double_write(self):
        before = self.frontend.read_text(encoding="utf-8")
        with mock.patch.object(
            mod, "resolve_digest_ref", return_value=NEW_DIGEST
        ), mock.patch.object(
            mod,
            "validate_image",
            return_value=mod.ValidationReport(platforms=["linux/amd64", "linux/arm64"]),
        ):
            with self.assertRaises(mod.UpdateError):
                mod.run_update(
                    root=self.root,
                    project="pn",
                    image_args=[
                        f"frontend=ghcr.io/nacfson/project-pn-frontend@{NEW_DIGEST}",
                        f"pn-frontend=ghcr.io/nacfson/project-pn-frontend@{NEW_DIGEST}",
                    ],
                    dry_run=False,
                    strict=False,
                    pull_check=False,
                )
        self.assertEqual(self.frontend.read_text(encoding="utf-8"), before)

    def test_tag_pinned_current_image_is_upgraded(self):
        tagged_dir = self.root / "deploy" / "projects" / "tagged" / "workloads"
        tagged_dir.mkdir(parents=True)
        tagged = tagged_dir / "frontend-deployment.yaml"
        tagged.write_text(
            DEPLOYMENT.format(
                name="tagged-frontend",
                container="frontend",
                image="ghcr.io/nacfson/tagged-frontend:oldtag",
                port=80,
            ),
            encoding="utf-8",
        )
        old_digest = "sha256:" + "ee" * 32
        html_before = self.html.read_text(encoding="utf-8")
        self.html.write_text(
            html_before.replace(
                "<!-- PROJECT_IMAGE_PINS:START -->",
                '<g class="node-box" id="node-tagged-frontend">\n'
                "  <text>Pinned ghcr.io/nacfson/tagged-frontend:oldtag</text>\n"
                "</g>\n"
                "      'tagged-frontend': {\n"
                "        image: 'ghcr.io/nacfson/tagged-frontend:oldtag',\n"
                "      },\n"
                "<!-- PROJECT_IMAGE_PINS:START -->",
            ),
            encoding="utf-8",
        )

        def resolve(ref):
            if ref.endswith(":oldtag"):
                return old_digest
            return NEW_DIGEST

        with mock.patch.object(
            mod, "resolve_digest_ref", side_effect=resolve
        ), mock.patch.object(
            mod,
            "validate_image",
            return_value=mod.ValidationReport(platforms=["linux/amd64", "linux/arm64"]),
        ):
            summary = mod.run_update(
                root=self.root,
                project="tagged",
                image_args=[
                    f"frontend=ghcr.io/nacfson/tagged-frontend@{NEW_DIGEST}"
                ],
                dry_run=False,
                strict=False,
                pull_check=False,
            )
        self.assertFalse(summary["images"][0]["noop"])
        self.assertIn(
            f"ghcr.io/nacfson/tagged-frontend@{NEW_DIGEST}",
            tagged.read_text(encoding="utf-8"),
        )
        html = self.html.read_text(encoding="utf-8")
        self.assertNotIn("tagged-frontend:oldtag", html)
        node_block = html.split('id="node-tagged-frontend"', 1)[1].split("</g>", 1)[0]
        self.assertIn(
            f"ghcr.io/nacfson/tagged-frontend@{NEW_DIGEST}", node_block
        )

    def test_resolution_warnings_reach_the_summary(self):
        target = mod.discover_target(self.root, "pn", "frontend")
        update = mod.ImageUpdate(
            target=target,
            source="https://github.com/nacfson/Project_PN/actions/runs/1",
            tag=None,
            digest=NEW_DIGEST,
            new_image=f"{target.old_repo}@{NEW_DIGEST}",
            resolution_warnings=["no job mentioned frontend"],
        )
        rows = mod._summary_images([update], self.root)
        self.assertEqual(
            rows[0]["resolution_warnings"], ["no job mentioned frontend"]
        )

    def test_noop_when_digest_matches(self):
        with mock.patch.object(
            mod, "resolve_digest_ref", return_value=FRONTEND_OLD.split("@", 1)[1]
        ):
            summary = mod.run_update(
                root=self.root,
                project="pn",
                image_args=[f"frontend={FRONTEND_OLD}"],
                dry_run=False,
                strict=False,
                pull_check=False,
            )
        self.assertTrue(summary["images"][0]["noop"])
        self.assertFalse(summary["changed"])
        self.assertFalse(summary["html_changed"])


if __name__ == "__main__":
    unittest.main()
