#!/usr/bin/env python3
"""Unit tests for scripts/image-bump-request.py (dispatch -> image-bump issue)."""

from __future__ import annotations

import importlib.util
import json
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

BASE_DIR = Path(__file__).resolve().parents[2]
SCRIPT_PATH = BASE_DIR / "scripts" / "image-bump-request.py"


def load_module():
    spec = importlib.util.spec_from_file_location("image_bump_request", SCRIPT_PATH)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"cannot load {SCRIPT_PATH}")
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


mod = load_module()

SHA = "a" * 40
OTHER_SHA = "b" * 40
SOURCES = mod.load_sources()
OLD_DIGEST = "sha256:" + "11" * 32
NEW_DIGEST = "sha256:" + "22" * 32


def dispatch(**payload):
    base = {
        "project": "pn",
        "source_repo": "nacfson/Project_PN",
        "sha": SHA,
        "roles": ["backend", "frontend"],
        "run_url": "https://github.com/nacfson/Project_PN/actions/runs/42",
    }
    base.update(payload)
    return {"client_payload": base}


def valid(**payload):
    return mod.validate_request(
        mod.parse_request(dispatch(**payload), "repository_dispatch"), SOURCES
    )


def image(role, noop=False, **extra):
    data = {
        "role": role,
        "deployment": f"pn-{role}",
        "container": role,
        "path": f"deploy/projects/pn/workloads/{role}-deployment.yaml",
        "old_image": f"ghcr.io/nacfson/project-pn-{role}@{OLD_DIGEST}",
        "new_image": f"ghcr.io/nacfson/project-pn-{role}@{OLD_DIGEST if noop else NEW_DIGEST}",
        "tag": SHA[:7],
        "digest": OLD_DIGEST if noop else NEW_DIGEST,
        "source": "",
        "noop": noop,
        "platforms": ["linux/amd64", "linux/arm64"],
        "checks": {"platform:linux/amd64": True, "platform:linux/arm64": True},
        "evidence": {
            "linux/amd64": {
                "user": "10001",
                "revision": SHA,
                "entrypoint": ["/app"],
                "exposed_ports": ["8080/tcp"],
            }
        },
        "warnings": [],
        "resolution_warnings": [],
        "skipped": [],
    }
    data.update(extra)
    return data


def summary(*images):
    return {"project": "pn", "changed": any(not i["noop"] for i in images), "images": list(images)}


class ValidateRequestTests(unittest.TestCase):
    def test_canonicalizes_a_valid_dispatch(self):
        request = valid(source_repo="NACFSON/project_pn", roles="frontend, backend")
        self.assertEqual(request.source_repo, "nacfson/Project_PN")
        self.assertEqual(request.roles, ["backend", "frontend"])
        self.assertEqual(
            request.commit_url(), f"https://github.com/nacfson/Project_PN/commit/{SHA}"
        )

    def test_empty_roles_mean_every_project_role(self):
        self.assertEqual(valid(project="blog", source_repo="nacfson/nacfson_blog",
                               roles=[], run_url="").roles, ["web"])

    def test_workflow_dispatch_takes_source_repo_from_allowlist(self):
        event = {"inputs": {"project": "health", "sha": SHA, "roles": ""}}
        request = mod.validate_request(mod.parse_request(event, "workflow_dispatch"), SOURCES)
        self.assertEqual(request.source_repo, "nacfson/nacfson_health")
        self.assertEqual(request.roles, ["backend", "frontend"])

    def test_rejects_bad_requests(self):
        cases = {
            "unknown project": dict(project="evil"),
            "foreign source repo": dict(source_repo="attacker/Project_PN"),
            "short sha": dict(sha=SHA[:7]),
            "unknown role": dict(roles=["backend", "db"]),
            "foreign run url": dict(run_url="https://github.com/attacker/x/actions/runs/1"),
            "markdown in run url": dict(
                run_url="https://github.com/nacfson/Project_PN/actions/runs/1) **x** [y](https://evil"
            ),
        }
        for name, payload in cases.items():
            with self.subTest(name), self.assertRaises(mod.RequestError):
                valid(**payload)

    def test_rejects_other_events(self):
        with self.assertRaises(mod.RequestError):
            mod.parse_request({}, "push")


class PlanTests(unittest.TestCase):
    def issue(self, number, sha=SHA, roles=("backend", "frontend"), project="pn"):
        request = mod.Request(project, "nacfson/Project_PN", sha, list(roles))
        return {"number": number, "body": mod.marker(request) + "\nbody"}

    def test_same_commit_is_a_duplicate(self):
        self.assertEqual(mod.plan(valid(), [self.issue(7)]), (7, []))

    def test_same_commit_narrower_request_is_a_duplicate(self):
        self.assertEqual(mod.plan(valid(roles=["frontend"]), [self.issue(7)]), (7, []))

    def test_same_commit_wider_request_supersedes(self):
        issues = [self.issue(5, roles=["backend"])]
        self.assertEqual(mod.plan(valid(), issues), (None, [5]))

    def test_duplicate_still_reports_stale_requests(self):
        issues = [self.issue(12), self.issue(8, OTHER_SHA)]
        self.assertEqual(mod.plan(valid(), issues), (12, [8]))

    def test_accepts_run_attempt_url(self):
        url = "https://github.com/nacfson/Project_PN/actions/runs/42/attempts/2"
        self.assertEqual(valid(run_url=url).run_url, url)

    def test_newer_build_supersedes_covered_requests(self):
        issues = [self.issue(9, OTHER_SHA), self.issue(8, OTHER_SHA, roles=["backend"])]
        self.assertEqual(mod.plan(valid(), issues), (None, [8, 9]))

    def test_partial_build_keeps_wider_request_open(self):
        issues = [self.issue(5, OTHER_SHA)]
        self.assertEqual(mod.plan(valid(roles=["frontend"]), issues), (None, []))

    def test_ignores_other_projects_and_unmarked_issues(self):
        issues = [self.issue(3, OTHER_SHA, project="health"), {"number": 4, "body": "hi"}]
        self.assertEqual(mod.plan(valid(), issues), (None, []))


class RenderTests(unittest.TestCase):
    def test_marker_round_trips(self):
        request = valid()
        data = mod.parse_marker(mod.render_body(request, summary(image("backend")), None))
        self.assertEqual(data["sha"], SHA)
        self.assertEqual(data["roles"], ["backend", "frontend"])

    def test_title_lists_only_changed_roles(self):
        title = mod.render_title(valid(), summary(image("backend"), image("frontend", noop=True)))
        self.assertEqual(title, f"image-bump: proj-pn backend → Project_PN@{SHA[:7]}")

    def test_body_neutralizes_image_metadata(self):
        evil = image("backend", evidence={"linux/amd64": {"user": "`root` | @nacfson\nx"}})
        body = mod.render_body(valid(), summary(evil), "https://example/run")
        self.assertIn("`'root' / @nacfson x`", body)
        self.assertIn(f"--image backend=https://github.com/nacfson/Project_PN/commit/{SHA}", body)
        self.assertIn("_Opened by [image-bump-request](https://example/run)._", body)


class FakeHelper:
    class UpdateError(Exception):
        pass

    def __init__(self, result=None, error=None):
        self.result, self.error, self.calls = result, error, []

    def run_update(self, **kwargs):
        self.calls.append(kwargs)
        if self.error:
            raise self.UpdateError(self.error)
        return self.result


class ResolveTests(unittest.TestCase):
    def test_dry_runs_the_helper_with_commit_links(self):
        helper = FakeHelper(result=summary(image("backend")))
        mod.resolve(BASE_DIR, valid(roles=["backend"]), helper)
        call = helper.calls[0]
        self.assertTrue(call["dry_run"])
        self.assertEqual(
            call["image_args"], [f"backend=https://github.com/nacfson/Project_PN/commit/{SHA}"]
        )

    def test_validation_failure_becomes_request_error(self):
        with self.assertRaisesRegex(mod.RequestError, "revision mismatch"):
            mod.resolve(BASE_DIR, valid(), FakeHelper(error="revision mismatch"))


class MainTests(unittest.TestCase):
    def run_main(self, helper, gh_outputs):
        calls = []

        def fake_gh(args):
            calls.append(args)
            return gh_outputs.get(args[0] + " " + args[1], "")

        with tempfile.TemporaryDirectory() as tmp:
            event_file = Path(tmp) / "event.json"
            event_file.write_text(json.dumps(dispatch()), encoding="utf-8")
            with mock.patch.object(mod, "load_helper", return_value=helper), \
                    mock.patch.object(mod, "run_gh", side_effect=fake_gh):
                code = mod.main([
                    "--event-file", str(event_file), "--event-name", "repository_dispatch",
                    "--repo", "nacfson/nacfson_pipeline",
                ])
        return code, calls

    def test_already_pinned_opens_nothing(self):
        code, calls = self.run_main(FakeHelper(result=summary(image("backend", noop=True))), {})
        self.assertEqual((code, calls), (0, []))

    def test_validation_failure_exits_2_without_gh(self):
        code, calls = self.run_main(FakeHelper(error="no linux/arm64"), {})
        self.assertEqual((code, calls), (2, []))

    def test_opens_issue_and_closes_superseded(self):
        old = mod.Request("pn", "nacfson/Project_PN", OTHER_SHA, ["backend", "frontend"])
        outputs = {
            "issue list": json.dumps([{"number": 3, "body": mod.marker(old)}]),
            "issue create": "https://github.com/nacfson/nacfson_pipeline/issues/12\n",
        }
        code, calls = self.run_main(FakeHelper(result=summary(image("backend"))), outputs)
        self.assertEqual(code, 0)
        self.assertEqual([c[:2] for c in calls], [
            ["issue", "list"], ["label", "create"], ["issue", "create"], ["issue", "close"],
        ])
        self.assertEqual(calls[-1][2], "3")
        self.assertIn("Superseded by #12.", calls[-1])

    def test_retry_closes_requests_a_failed_run_left_open(self):
        new = mod.Request("pn", "nacfson/Project_PN", SHA, ["backend", "frontend"])
        old = mod.Request("pn", "nacfson/Project_PN", OTHER_SHA, ["backend", "frontend"])
        outputs = {"issue list": json.dumps([
            {"number": 12, "body": mod.marker(new)}, {"number": 3, "body": mod.marker(old)},
        ])}
        code, calls = self.run_main(FakeHelper(result=summary(image("backend"))), outputs)
        self.assertEqual(code, 0)
        self.assertEqual([c[:2] for c in calls], [["issue", "list"], ["issue", "close"]])
        self.assertIn("Superseded by #12.", calls[-1])


if __name__ == "__main__":
    unittest.main()
