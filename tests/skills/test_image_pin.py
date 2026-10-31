"""Tests for scripts/image-pin.py: request checks, run markers, dedupe, supersede.

The dedupe rules are security-relevant, not just cosmetic. A run's own marker is
the only thing that may suppress another request, and only when that run's
`validate` actually succeeded. See the step-1 plan, Review Focus 1 and 2.
"""

import importlib.util
import json
import subprocess
import sys
import tempfile
import unittest
from datetime import datetime, timedelta, timezone
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
MODULE_PATH = ROOT / "scripts" / "image-pin.py"


def load_module():
    spec = importlib.util.spec_from_file_location("image_pin", MODULE_PATH)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"cannot load {MODULE_PATH}")
    module = importlib.util.module_from_spec(spec)
    # Register before exec: @dataclass resolves annotations through sys.modules.
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


mod = load_module()
SOURCES = mod.load_sources()

SHA_A = "a" * 40
SHA_B = "b" * 40
T0 = datetime(2026, 10, 31, 9, 0, tzinfo=timezone.utc)
T1 = T0 + timedelta(hours=1)


class RequestValidationTests(unittest.TestCase):
    """The allowlist rules, moved unchanged from image-bump-request.py."""

    def test_canonicalizes_a_valid_dispatch(self):
        request = mod.validate_request(
            mod.Request(project="blog", source_repo="", sha=SHA_A, roles=["web"]),
            SOURCES,
        )
        self.assertEqual(request.project, "blog")
        self.assertEqual(request.source_repo, "nacfson/nacfson_blog")
        self.assertEqual(request.roles, ["web"])

    def test_hand_run_without_run_url_passes(self):
        """A manual dispatch has no source run to point at."""
        request = mod.validate_request(
            mod.Request(project="blog", source_repo="", sha=SHA_A, roles=["web"], run_url=None),
            SOURCES,
        )
        self.assertIsNone(request.run_url)

    def test_unknown_project_rejected(self):
        with self.assertRaises(mod.RequestError):
            mod.validate_request(
                mod.Request(project="nope", source_repo="", sha=SHA_A, roles=["web"]),
                SOURCES,
            )

    def test_bad_sha_rejected(self):
        for sha in ("abc", SHA_A + "a", "z" * 40):
            with self.subTest(sha=sha):
                with self.assertRaises(mod.RequestError):
                    mod.validate_request(
                        mod.Request(project="blog", source_repo="", sha=sha, roles=["web"]),
                        SOURCES,
                    )

    def test_unknown_role_rejected(self):
        with self.assertRaises(mod.RequestError):
            mod.validate_request(
                mod.Request(project="blog", source_repo="", sha=SHA_A, roles=["frontend"]),
                SOURCES,
            )

    def test_foreign_source_repo_rejected(self):
        with self.assertRaises(mod.RequestError):
            mod.validate_request(
                mod.Request(
                    project="blog", source_repo="nacfson/other", sha=SHA_A, roles=["web"]
                ),
                SOURCES,
            )

    def test_bad_run_url_rejected(self):
        with self.assertRaises(mod.RequestError):
            mod.validate_request(
                mod.Request(
                    project="blog",
                    source_repo="",
                    sha=SHA_A,
                    roles=["web"],
                    # right shape, wrong repository: must not be accepted
                    run_url="https://github.com/nacfson/blog-web/actions/runs/1",
                ),
                SOURCES,
            )

    def test_run_url_of_the_source_repo_accepted(self):
        request = mod.validate_request(
            mod.Request(
                project="blog",
                source_repo="",
                sha=SHA_A,
                roles=["web"],
                run_url="https://github.com/nacfson/nacfson_blog/actions/runs/123",
            ),
            SOURCES,
        )
        self.assertEqual(
            request.run_url, "https://github.com/nacfson/nacfson_blog/actions/runs/123"
        )


class MarkerTests(unittest.TestCase):
    """A marker is what a run writes about itself; it round-trips through a file."""

    def test_missing_conclusion_is_not_success(self):
        """Regression: a marker without a conclusion must not suppress anything.

        A run that dies before writing its marker, or any refactor that drops
        the key, produced a marker that counted as successful and hid real
        requests for 30 days.
        """
        data = {
            "project": "blog",
            "sha": SHA_A,
            "roles": ["web"],
            "run_id": 1,
            "status": "queued",
            "created_at": "2026-10-31T09:00:00Z",
        }
        marker = mod.Marker.from_dict(data)
        self.assertNotEqual(marker.conclusion, "success")
        self.assertFalse(mod.is_duplicate(mod.Request("blog", "", SHA_A, ["web"]), [marker]))

    def test_marker_round_trips(self):
        marker = mod.Marker(
            project="pn",
            sha=SHA_A,
            roles=["backend", "frontend"],
            run_id=42,
            status="success",
            created_at=T0,
        )
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "marker.json"
            mod.write_marker(marker, path)
            self.assertEqual(mod.read_marker(path), marker)

    def test_missing_marker_reads_as_none(self):
        with tempfile.TemporaryDirectory() as tmp:
            self.assertIsNone(mod.read_marker(Path(tmp) / "absent.json"))

    def test_corrupt_marker_reads_as_none(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "marker.json"
            path.write_text("{not json", encoding="utf-8")
            self.assertIsNone(mod.read_marker(path))

    def test_marker_json_is_stable_for_comparison(self):
        marker = mod.Marker("blog", SHA_A, ["web"], 7, "success", T0)
        payload = json.loads(marker.to_json())
        self.assertEqual(payload["project"], "blog")
        self.assertEqual(payload["sha"], SHA_A)
        self.assertEqual(payload["roles"], ["web"])
        self.assertEqual(payload["run_id"], 7)
        self.assertEqual(payload["status"], "success")
        self.assertIn("2026-10-31", payload["created_at"])


class DuplicateTests(unittest.TestCase):
    """Is a request already covered by a live run?"""

    def test_decoy_failed_run_does_not_suppress(self):
        """Review Focus 1: a failed run's marker must never count as live.

        A token holder can dispatch any project/sha/roles. If a failed run's
        marker suppressed the next legitimate request, the operator would never
        see a message and never learn why. The run is still listed by the API,
        so the guard is the conclusion, not the status.
        """
        live = [
            mod.Marker("blog", SHA_A, ["web"], 1, "queued", T0, conclusion="failure"),
        ]
        self.assertFalse(mod.is_duplicate(mod.Request("blog", "", SHA_A, ["web"]), live))

    def test_queued_run_is_live(self):
        """A run is `queued` until validate finishes, and still counts."""
        live = [mod.Marker("blog", SHA_A, ["web"], 2, "queued", T0, conclusion="success")]
        self.assertTrue(mod.is_duplicate(mod.Request("blog", "", SHA_A, ["web"]), live))

    def test_waiting_run_is_live(self):
        live = [mod.Marker("blog", SHA_A, ["web"], 3, "waiting", T0, conclusion="success")]
        self.assertTrue(mod.is_duplicate(mod.Request("blog", "", SHA_A, ["web"]), live))

    def test_in_progress_run_is_live(self):
        """An approved run leaves `waiting`; it is still covering these roles."""
        live = [mod.Marker("blog", SHA_A, ["web"], 4, "in_progress", T0, conclusion="success")]
        self.assertTrue(mod.is_duplicate(mod.Request("blog", "", SHA_A, ["web"]), live))

    def test_completed_run_is_not_live(self):
        live = [mod.Marker("blog", SHA_A, ["web"], 5, "success", T0, conclusion="success")]
        self.assertFalse(mod.is_duplicate(mod.Request("blog", "", SHA_A, ["web"]), live))

    def test_different_commit_is_not_a_duplicate(self):
        live = [mod.Marker("blog", SHA_B, ["web"], 6, "waiting", T0, conclusion="success")]
        self.assertFalse(mod.is_duplicate(mod.Request("blog", "", SHA_A, ["web"]), live))

    def test_partial_role_coverage_is_not_a_duplicate(self):
        """A backend-only request must not swallow a backend+frontend one."""
        live = [mod.Marker("pn", SHA_A, ["backend"], 7, "waiting", T0, conclusion="success")]
        self.assertFalse(
            mod.is_duplicate(mod.Request("pn", "", SHA_A, ["backend", "frontend"]), live)
        )

    def test_wider_live_run_covers_a_narrower_request(self):
        live = [mod.Marker("pn", SHA_A, ["backend", "frontend"], 8, "waiting", T0, conclusion="success")]
        self.assertTrue(mod.is_duplicate(mod.Request("pn", "", SHA_A, ["backend"]), live))

    def test_different_project_is_not_a_duplicate(self):
        live = [mod.Marker("pn", SHA_A, ["backend"], 9, "waiting", T0, conclusion="success")]
        self.assertFalse(mod.is_duplicate(mod.Request("blog", "", SHA_A, ["backend"]), live))


class ReplacedTests(unittest.TestCase):
    """Has a newer request superseded this one?"""

    def test_approved_newer_request_still_replaces(self):
        """Review Focus 2: approval moves a run to in_progress, off `waiting`.

        If `in_progress` were excluded, approving the newer message first would
        let the older approved run proceed and open a second PR.
        """
        live = [
            mod.Marker("pn", SHA_B, ["backend", "frontend"], 20, "in_progress", T1, conclusion="success"),
        ]
        self.assertTrue(mod.is_replaced(mod.Request("pn", "", SHA_A, ["backend"]), live, requested_at=T0))

    def test_waiting_newer_request_replaces(self):
        live = [mod.Marker("pn", SHA_B, ["backend"], 21, "waiting", T1, conclusion="success")]
        self.assertTrue(mod.is_replaced(mod.Request("pn", "", SHA_A, ["backend"]), live, requested_at=T0))

    def test_queued_newer_request_replaces(self):
        live = [mod.Marker("pn", SHA_B, ["backend"], 22, "queued", T1, conclusion="success")]
        self.assertTrue(mod.is_replaced(mod.Request("pn", "", SHA_A, ["backend"]), live, requested_at=T0))

    def test_older_request_does_not_replace(self):
        live = [mod.Marker("pn", SHA_B, ["backend"], 23, "waiting", T0, conclusion="success")]
        self.assertFalse(mod.is_replaced(mod.Request("pn", "", SHA_A, ["backend"]), live, requested_at=T0))

    def test_newer_request_without_our_roles_does_not_replace(self):
        """A frontend-only build must not cancel a waiting backend request."""
        live = [mod.Marker("pn", SHA_B, ["frontend"], 24, "waiting", T1, conclusion="success")]
        self.assertFalse(mod.is_replaced(mod.Request("pn", "", SHA_A, ["backend"]), live, requested_at=T0))

    def test_newer_failed_request_does_not_replace(self):
        live = [
            mod.Marker("pn", SHA_B, ["backend"], 25, "waiting", T1, conclusion="failure"),
        ]
        self.assertFalse(mod.is_replaced(mod.Request("pn", "", SHA_A, ["backend"]), live, requested_at=T0))

    def test_other_project_does_not_replace(self):
        live = [mod.Marker("health", SHA_B, ["backend"], 26, "waiting", T1, conclusion="success")]
        self.assertFalse(mod.is_replaced(mod.Request("pn", "", SHA_A, ["backend"]), live, requested_at=T0))

    def test_compares_against_request_time_not_now(self):
        """Regression: comparing markers to "now" inverted the answer.

        An old request (created T0) and a newer marker (created T1) must mean
        "replaced", regardless of when the comparison runs. Using wall-clock
        now made the marker look older than the request.
        """
        live = [mod.Marker("pn", SHA_B, ["backend"], 27, "waiting", T1, conclusion="success")]
        self.assertTrue(
            mod.is_replaced(
                mod.Request("pn", "", SHA_A, ["backend"]),
                live,
                requested_at=T0,
            )
        )

    def test_marker_older_than_request_does_not_replace(self):
        """The mirror case: a marker predating the request must not replace it."""
        live = [mod.Marker("pn", SHA_B, ["backend"], 28, "waiting", T0, conclusion="success")]
        self.assertFalse(mod.is_replaced(mod.Request("pn", "", SHA_A, ["backend"]), live, requested_at=T1))


class SelfExclusionTests(unittest.TestCase):
    """A run must never treat its own marker as a reason to stop."""

    def test_run_does_not_replace_itself(self):
        """Regression: marker write time is minutes after run creation.

        Without excluding the evaluating run, an older request compared its own
        marker (written after it started) to its own creation time and declared
        itself `replaced`, silently doing nothing.
        """
        own = mod.Marker("pn", SHA_A, ["backend"], 42, "waiting", T1)
        self.assertFalse(
            mod.is_replaced(
                mod.Request("pn", "", SHA_A, ["backend"]),
                [own],
                requested_at=T0,
                own_run_id=42,
            )
        )

    def test_run_does_not_see_itself_as_duplicate(self):
        own = mod.Marker("pn", SHA_A, ["backend"], 43, "queued", T0)
        self.assertFalse(
            mod.is_duplicate(
                mod.Request("pn", "", SHA_A, ["backend"]), [own], own_run_id=43
            )
        )

    def test_other_runs_still_count_when_own_run_id_is_given(self):
        other = mod.Marker("pn", SHA_A, ["backend"], 44, "waiting", T0, conclusion="success")
        self.assertTrue(
            mod.is_duplicate(
                mod.Request("pn", "", SHA_A, ["backend"]), [other], own_run_id=99
            )
        )


class CanonicalMarkerTests(unittest.TestCase):
    """The CLI records the same identity that check-request computes."""

    def test_write_marker_canonicalizes_project_and_default_roles(self):
        """Regression: write-marker bypassed validation.

        It stored the raw `--project` and an empty role set, so a marker never
        matched the canonical request identity and dedupe silently matched
        nothing for the default-roles case.
        """
        import io
        from contextlib import redirect_stdout

        with tempfile.TemporaryDirectory() as tmp:
            out = Path(tmp) / "marker.json"
            with redirect_stdout(io.StringIO()):
                code = mod.main(
                    [
                        "write-marker",
                        "--project", "PN",
                        "--sha", SHA_A,
                        "--roles", "",
                        "--run-id", "77",
                        "--out", str(out),
                    ]
                )
            self.assertEqual(code, 0)
            marker = mod.read_marker(out)
            self.assertIsNotNone(marker)
            self.assertEqual(marker.project, "pn")
            self.assertEqual(marker.roles, ["backend", "frontend"])

    def test_write_marker_rejects_an_unknown_project(self):
        import io
        from contextlib import redirect_stderr, redirect_stdout

        with tempfile.TemporaryDirectory() as tmp:
            out = Path(tmp) / "marker.json"
            with redirect_stdout(io.StringIO()), redirect_stderr(io.StringIO()):
                code = mod.main(
                    [
                        "write-marker",
                        "--project", "nope",
                        "--sha", SHA_A,
                        "--run-id", "1",
                        "--out", str(out),
                    ]
                )
            self.assertEqual(code, 2)
            self.assertFalse(out.exists(), "a rejected request must not leave a marker")


class DiffCheckTests(unittest.TestCase):
    """§9: the changed set must be exactly the approved digests and nothing else.

    This is what makes skipping CI's PR workflows safe, so every reject case here
    is a way a pin PR could smuggle in a change the operator never approved.
    """

    OLD = "sha256:" + "1" * 64
    NEW = "sha256:" + "2" * 64
    REPO = "ghcr.io/nacfson/blog-web"
    YAML_PATH = "deploy/projects/blog/workloads/deployment.yaml"
    HTML_PATH = "docs/platform-architecture.html"

    def yaml_diff(self, old=None, new=None, repo=None, indent="          "):
        old = old or self.OLD
        new = new or self.NEW
        repo = repo or self.REPO
        return (
            f"--- a/{self.YAML_PATH}\n"
            f"+++ b/{self.YAML_PATH}\n"
            "@@ -34,7 +34,7 @@ spec:\n"
            "           type: RuntimeDefault\n"
            "       containers:\n"
            "         - name: blog-web\n"
            f"-{indent}image: {repo}@{old}\n"
            f"+{indent}image: {repo}@{new}\n"
            "           imagePullPolicy: IfNotPresent\n"
        )

    def html_diff(self, old=None, new=None, row=None):
        old = old or self.OLD
        new = new or self.NEW
        row = row if row is not None else (
            '<tr class="border-b"><td>proj-blog</td><td>blog-web</td>'
            '<td>ghcr.io/nacfson/blog-web</td><td>'
        )
        return (
            f"--- a/{self.HTML_PATH}\n"
            f"+++ b/{self.HTML_PATH}\n"
            "@@ -952,7 +952,7 @@\n"
            "             <tbody>\n"
            f"-              {row}{old}</td></tr>\n"
            f"+              {row}{new}</td></tr>\n"
            "             </tbody>\n"
        )

    def summary(self, *, noop=False, html_changed=True):
        return {
            "project": "blog",
            "changed": not noop,
            "html_changed": html_changed,
            "images": [
                {
                    "role": "web",
                    "path": self.YAML_PATH,
                    "old_image": f"{self.REPO}@{self.OLD}",
                    "new_image": f"{self.REPO}@{self.NEW}",
                    "digest": self.NEW,
                    "noop": noop,
                }
            ],
        }

    def approved(self):
        return {"web": f"{self.REPO}@{self.NEW}"}

    def check(self, changed, summary=None, approved=None):
        return mod.diff_check(
            changed,
            summary if summary is not None else self.summary(),
            approved if approved is not None else self.approved(),
        )

    # --- accept -------------------------------------------------------------

    def test_accepts_a_real_pin_diff(self):
        """The shape of commit a52a27f: one image line, one handbook digest."""
        violations = self.check(
            {self.YAML_PATH: self.yaml_diff(), self.HTML_PATH: self.html_diff()}
        )
        self.assertEqual(violations, [])

    def test_accepts_when_the_handbook_did_not_change(self):
        """html_changed False is legitimate: no row or short token matched."""
        violations = self.check(
            {self.YAML_PATH: self.yaml_diff()},
            summary=self.summary(html_changed=False),
        )
        self.assertEqual(violations, [])

    def test_accepts_a_two_role_request_where_one_role_is_noop(self):
        """Only the changed role's path may appear; the noop role writes nothing."""
        summary = self.summary()
        summary["images"].append(
            {
                "role": "extra",
                "path": "deploy/projects/blog/workloads/other.yaml",
                "old_image": f"{self.REPO}@{self.OLD}",
                "new_image": f"{self.REPO}@{self.OLD}",
                "digest": self.OLD,
                "noop": True,
            }
        )
        violations = self.check(
            {self.YAML_PATH: self.yaml_diff(), self.HTML_PATH: self.html_diff()},
            summary=summary,
        )
        self.assertEqual(violations, [])

    def test_the_noop_role_path_is_not_required_in_the_diff(self):
        """The converse: reporting the noop role's path is an error."""
        summary = self.summary()
        summary["images"].append(
            {
                "role": "extra",
                "path": "deploy/projects/blog/workloads/other.yaml",
                "old_image": f"{self.REPO}@{self.OLD}",
                "new_image": f"{self.REPO}@{self.OLD}",
                "digest": self.OLD,
                "noop": True,
            }
        )
        violations = self.check(
            {
                self.YAML_PATH: self.yaml_diff(),
                self.HTML_PATH: self.html_diff(),
                "deploy/projects/blog/workloads/other.yaml": self.yaml_diff(),
            },
            summary=summary,
        )
        self.assertTrue(violations, "a noop role must not produce a file change")

    # --- reject -------------------------------------------------------------

    def test_rejects_a_tag_instead_of_a_digest(self):
        bad = self.yaml_diff(new="").replace(
            f"+          image: {self.REPO}@\n",
            f"+          image: {self.REPO}:latest\n",
        )
        violations = self.check({self.YAML_PATH: bad})
        self.assertTrue(violations, "a floating tag must never be pinnable")

    def test_rejects_a_changed_repository(self):
        violations = self.check(
            {self.YAML_PATH: self.yaml_diff(repo="ghcr.io/other/blog-web")}
        )
        self.assertTrue(violations)

    def test_rejects_an_unapproved_digest(self):
        violations = self.check(
            {self.YAML_PATH: self.yaml_diff(new="sha256:" + "9" * 64)}
        )
        self.assertTrue(violations)

    def test_rejects_an_extra_added_yaml_line(self):
        bad = self.yaml_diff().replace(
            "           imagePullPolicy: IfNotPresent\n",
            "           imagePullPolicy: IfNotPresent\n+          env:\n",
        )
        violations = self.check({self.YAML_PATH: bad})
        self.assertTrue(violations)

    def test_rejects_changed_indentation(self):
        violations = self.check({self.YAML_PATH: self.yaml_diff(indent="            ")})
        self.assertTrue(violations)

    def test_rejects_an_extra_file(self):
        violations = self.check(
            {
                self.YAML_PATH: self.yaml_diff(),
                "deploy/projects/blog/workloads/extra.yaml": self.yaml_diff(),
            }
        )
        self.assertTrue(violations)

    def test_rejects_a_path_in_another_project(self):
        other = self.YAML_PATH.replace("/blog/", "/pn/")
        bad = self.yaml_diff().replace(self.YAML_PATH, other)
        violations = self.check({other: bad})
        self.assertTrue(violations)

    def test_rejects_an_unrelated_handbook_change(self):
        html = self.html_diff().replace(
            "             </tbody>\n",
            "             </tbody>\n+            <p>an unrelated sentence</p>\n",
        )
        violations = self.check({self.YAML_PATH: self.yaml_diff(), self.HTML_PATH: html})
        self.assertTrue(violations)

    def test_rejects_a_handbook_digest_that_is_not_approved(self):
        html = self.html_diff(new="sha256:" + "9" * 64)
        violations = self.check({self.YAML_PATH: self.yaml_diff(), self.HTML_PATH: html})
        self.assertTrue(violations)

    def test_rejects_a_missing_handbook_change_when_the_helper_rewrote_it(self):
        """html_changed True but the file is absent from the diff."""
        violations = self.check({self.YAML_PATH: self.yaml_diff()})
        self.assertTrue(violations)

    def test_rejects_a_missing_yaml_change(self):
        violations = self.check({})
        self.assertTrue(violations)

    def test_rejects_two_replaced_lines_in_one_file(self):
        doubled = self.yaml_diff() + self.yaml_diff().split("@@", 1)[1]
        violations = self.check({self.YAML_PATH: doubled})
        self.assertTrue(violations)

    # --- the parser's own trust boundary ------------------------------------

    def test_rejects_a_real_diff_whose_content_starts_with_plus_plus(self):
        """Regression: the `+++` header skip also swallowed `++` content.

        git renders a content line beginning `+` as `++...`, so a naive
        startswith("+++") skip dropped it. An injected line like
        `++hostNetwork: true` then rode along with a clean-looking pin.
        """
        injected = self.yaml_diff() + "+++hostNetwork: true\n"
        violations = self.check({self.YAML_PATH: injected})
        self.assertTrue(violations, "a content line beginning '++' must not be skipped")

    def test_rejects_a_real_diff_whose_content_starts_with_minus_minus(self):
        """The mirror case: removing a `--` line renders as `----`."""
        removed = self.yaml_diff() + "----removed-separator\n"
        violations = self.check({self.YAML_PATH: removed})
        self.assertTrue(violations)

    def test_rejects_a_blank_added_line(self):
        injected = self.yaml_diff() + "+\n"
        violations = self.check({self.YAML_PATH: injected})
        self.assertTrue(violations, "a blank-line insertion is still a change")

    def test_rejects_a_binary_diff(self):
        binary = (
            f"--- a/{self.YAML_PATH}\n+++ b/{self.YAML_PATH}\n"
            f"Binary files a/{self.YAML_PATH} and b/{self.YAML_PATH} differ\n"
        )
        violations = self.check({self.YAML_PATH: binary})
        self.assertTrue(violations)

    def test_rejects_a_multi_hunk_diff_that_hides_a_second_change(self):
        second = "@@ -50,3 +50,3 @@\n context\n-          runAsNonRoot: true\n+          runAsNonRoot: false\n"
        violations = self.check({self.YAML_PATH: self.yaml_diff() + second})
        self.assertTrue(violations)

    def test_rejects_an_empty_handbook_diff_when_html_changed(self):
        """Regression: html_changed True with no handbook hunk validated as clean."""
        violations = self.check({self.YAML_PATH: self.yaml_diff(), self.HTML_PATH: ""})
        self.assertTrue(violations)

    def test_rejects_a_header_only_handbook_diff(self):
        violations = self.check(
            {
                self.YAML_PATH: self.yaml_diff(),
                self.HTML_PATH: f"--- a/{self.HTML_PATH}\n+++ b/{self.HTML_PATH}\n",
            }
        )
        self.assertTrue(violations)

    def test_rejects_digest_rows_swapped_between_repositories(self):
        """Regression: any approved digest was accepted anywhere in the handbook."""
        other_repo = "ghcr.io/nacfson/project-pn-frontend"
        other_new = "sha256:" + "4" * 64
        # A row for blog-web rewritten with the digest approved for pn-frontend.
        row = '<tr><td>proj-blog</td><td>blog-web</td><td>ghcr.io/nacfson/blog-web</td><td>'
        violations = self.check(
            {self.YAML_PATH: self.yaml_diff(), self.HTML_PATH: self.html_diff(new=other_new, row=row)},
            approved={"web": f"{self.REPO}@{self.NEW}", "frontend": f"{other_repo}@{other_new}"},
        )
        self.assertTrue(violations, "a digest approved for another repo must be rejected")

    def test_accepts_a_short_badge_rewrite(self):
        """Regression: the short-token test compared bare hex to a prefixed string.

        It was always False, so every pn pin touching a `Pinned @sha256:` badge
        failed its own gate and no PR could ever open.
        """
        short_old = "sha256:" + self.OLD.split(":")[1][:8]
        short_new = "sha256:" + self.NEW.split(":")[1][:8]
        html = (
            f"--- a/{self.HTML_PATH}\n+++ b/{self.HTML_PATH}\n@@ -1,2 +1,2 @@\n"
            f"-  <text>Pinned @{short_old}... </text>\n"
            f"+  <text>Pinned @{short_new}... </text>\n"
            " c\n"
        )
        violations = self.check({self.YAML_PATH: self.yaml_diff(), self.HTML_PATH: html})
        self.assertEqual(violations, [])

    def test_rejects_a_short_badge_for_an_unapproved_digest(self):
        short_new = "sha256:99999999"
        html = (
            f"--- a/{self.HTML_PATH}\n+++ b/{self.HTML_PATH}\n@@ -1,2 +1,2 @@\n"
            f"-  <text>Pinned @sha256:{self.OLD.split(':')[1][:8]}... </text>\n"
            f"+  <text>Pinned @{short_new}... </text>\n"
            " c\n"
        )
        violations = self.check({self.YAML_PATH: self.yaml_diff(), self.HTML_PATH: html})
        self.assertTrue(violations)

    def test_rejects_a_traversal_path(self):
        """Regression: startswith() accepted `.../workloads/../../pn/...`."""
        evil = "deploy/projects/blog/workloads/../../pn/workloads/x.yaml"
        violations = self.check({evil: self.yaml_diff().replace(self.YAML_PATH, evil)})
        self.assertTrue(violations)


class RealDiffTests(unittest.TestCase):
    """Run the checker against git's own output, not hand-written fixtures.

    Hand-written fixtures can agree with a bug in the parser. Commit a52a27f is a
    real pin commit, so its diff is the ground truth for what must be accepted.
    """

    PIN_COMMIT = "a52a27f"
    YAML = "deploy/projects/blog/workloads/deployment.yaml"
    HTML = "docs/platform-architecture.html"
    NEW = "sha256:ac28e84ff5716443b9b68568f3f43321dbf2980736e9562f8efeb98934a9aacf"
    OLD = "sha256:38375007f07ff7c0ab6dcbc5341f1d5a95bcc6e650eee9b901352d79319bdddf"

    def _show(self, rev: str, path: str) -> str:
        return subprocess.run(
            ["git", "show", f"{self.PIN_COMMIT}{rev}:{path}"],
            cwd=ROOT, capture_output=True, text=True, check=True,
        ).stdout

    def _udiff(self, path: str) -> str:
        with tempfile.TemporaryDirectory() as tmp:
            before, after = Path(tmp) / "a", Path(tmp) / "b"
            before.write_text(self._show("^", path), encoding="utf-8")
            after.write_text(self._show("", path), encoding="utf-8")
            return subprocess.run(
                ["diff", "-u", "--label", f"a/{path}", "--label", f"b/{path}",
                 str(before), str(after)],
                capture_output=True, text=True, check=False,
            ).stdout

    def test_accepts_a_real_historical_pin_commit(self):
        try:
            self._show("", self.YAML)
        except subprocess.CalledProcessError:
            self.skipTest(f"commit {self.PIN_COMMIT} is not available in this clone")

        summary = {
            "project": "blog",
            "changed": True,
            "html_changed": True,
            "images": [
                {
                    "role": "web",
                    "path": self.YAML,
                    "old_image": f"ghcr.io/nacfson/blog-web@{self.OLD}",
                    "new_image": f"ghcr.io/nacfson/blog-web@{self.NEW}",
                    "digest": self.NEW,
                    "noop": False,
                }
            ],
        }
        approved = {"web": f"ghcr.io/nacfson/blog-web@{self.NEW}"}
        changed = {self.YAML: self._udiff(self.YAML), self.HTML: self._udiff(self.HTML)}
        self.assertEqual(mod.diff_check(changed, summary, approved), [])

    def test_rejects_the_same_real_diff_with_a_wrong_approved_digest(self):
        try:
            self._show("", self.YAML)
        except subprocess.CalledProcessError:
            self.skipTest(f"commit {self.PIN_COMMIT} is not available in this clone")

        summary = {
            "project": "blog",
            "changed": True,
            "html_changed": True,
            "images": [
                {
                    "role": "web",
                    "path": self.YAML,
                    "old_image": f"ghcr.io/nacfson/blog-web@{self.OLD}",
                    "new_image": f"ghcr.io/nacfson/blog-web@{self.NEW}",
                    "digest": self.NEW,
                    "noop": False,
                }
            ],
        }
        approved = {"web": f"ghcr.io/nacfson/blog-web@{'0' * 64}"}
        changed = {self.YAML: self._udiff(self.YAML), self.HTML: self._udiff(self.HTML)}
        self.assertTrue(mod.diff_check(changed, summary, approved))


class RenderTests(unittest.TestCase):
    """§7.3 message titles and the PR body, including private-image redaction."""

    RUN_ID = 41194135849

    def req(self, project="blog", images_public=True):
        return mod.Request(
            project=project,
            source_repo=f"nacfson/nacfson_{project}",
            sha=SHA_A,
            roles=["web"],
            run_url=f"https://github.com/nacfson/nacfson_{project}/actions/runs/7",
            images_public=images_public,
        )

    def summary(self, project="blog"):
        return {
            "project": project,
            "changed": True,
            "html_changed": True,
            "images": [
                {
                    "role": "web",
                    "deployment": "blog-web",
                    "container": "blog-web",
                    "path": f"deploy/projects/{project}/workloads/deployment.yaml",
                    "old_image": f"ghcr.io/nacfson/blog-web@sha256:{'1'*64}",
                    "new_image": f"ghcr.io/nacfson/blog-web@sha256:{'2'*64}",
                    "digest": f"sha256:{'2'*64}",
                    "tag": "sha-abc1234",
                    "source": "https://github.com/nacfson/nacfson_blog/commit/" + SHA_A,
                    "noop": False,
                    "platforms": ["linux/amd64", "linux/arm64"],
                    "checks": {"platform:linux/amd64": True, "platform:linux/arm64": True},
                    "evidence": {
                        "linux/amd64": {
                            "user": "10001",
                            "source": "https://github.com/nacfson/nacfson_blog",
                            "revision": SHA_A,
                            "entrypoint": ["/app/server"],
                            "cmd": ["--serve"],
                            "exposed_ports": ["8080/tcp"],
                        }
                    },
                    "warnings": [],
                }
            ],
        }

    # --- message ------------------------------------------------------------

    def test_allowed_mentions_is_empty(self):
        message = mod.render_message(self.req(), self.summary(), pin_run_id=self.RUN_ID)
        self.assertEqual(message["allowed_mentions"], {"parse": []})

    def test_review_link_is_the_pin_run_not_the_source_run(self):
        message = mod.render_message(self.req(), self.summary(), pin_run_id=self.RUN_ID)
        self.assertEqual(
            message["review_url"],
            f"https://github.com/nacfson/nacfson_pipeline/actions/runs/{self.RUN_ID}",
        )
        # The source build run may appear as context, but must not be the button.
        self.assertNotIn(f"actions/runs/{self.RUN_ID}", self.summary()["images"][0]["source"])

    def test_outcome_titles_cover_every_row_of_the_spec_table(self):
        expected = {
            "pr_open": "PR open",
            "rejected": "Rejected",
            "replaced": "Replaced by a newer build",
            "already_pinned": "Already pinned",
            "failed": "Failed",
            "expired": "Expired or cancelled",
        }
        for outcome, title in expected.items():
            with self.subTest(outcome=outcome):
                self.assertEqual(mod.outcome_title(outcome), title)

    def test_unknown_outcome_does_not_invent_a_title(self):
        with self.assertRaises(KeyError):
            mod.outcome_title("something-new")

    # --- pr body ------------------------------------------------------------

    def test_pr_body_has_the_digest_table(self):
        body = mod.render_pr_body(self.req(), self.summary(), {}, self.RUN_ID)
        self.assertIn("sha256:" + "2" * 64, body)
        self.assertIn("blog-web", body)

    def test_pr_body_says_normal_ci_did_not_run(self):
        body = mod.render_pr_body(self.req(), self.summary(), {}, self.RUN_ID)
        self.assertIn("did not run", body.lower())

    def test_private_evidence_is_never_rendered(self):
        """Regression guard: evidence is image configuration from a private image.

        The spec forbids publishing user, entrypoint, ports or the source label
        for project-pn-* and health, because nacfson_pipeline is public.
        """
        summary = self.summary(project="pn")
        body = mod.render_pr_body(self.req("pn", images_public=False), summary, {}, self.RUN_ID)
        for leaked in ("/app/server", "--serve", "8080/tcp", "10001", "exposed_ports", "entrypoint"):
            with self.subTest(leaked=leaked):
                self.assertNotIn(leaked, body)

    def test_public_evidence_may_be_rendered(self):
        body = mod.render_pr_body(self.req("blog", images_public=True), self.summary(), {}, self.RUN_ID)
        self.assertIn("linux/amd64", body)

    def test_evidence_defaults_to_hidden_when_visibility_is_unset(self):
        """An unknown project must render the least information, not the most."""
        request = mod.Request("mystery", "nacfson/x", SHA_A, ["web"])
        self.assertFalse(request.images_public)

    def test_pr_body_records_the_checks_that_ran(self):
        body = mod.render_pr_body(
            self.req(),
            self.summary(),
            {"render-validate.local-k3s": "PASS", "platform-preflight.vps-k3s": "PASS"},
            self.RUN_ID,
        )
        self.assertIn("render-validate.local-k3s", body)
        self.assertIn("platform-preflight.vps-k3s", body)


    def test_private_warnings_are_omitted(self):
        """Regression: warnings bypassed the redaction gate.

        The helper builds warnings from the image's own config — user, exposed
        ports, secret-like env names, source label — which is exactly what the
        public/private rule forbids. The body previously said configuration was
        omitted directly above the leaked configuration.
        """
        summary = self.summary(project="pn")
        summary["images"][0]["warnings"] = [
            "linux/amd64 image user is 10001; the pod securityContext still enforces non-root",
            "linux/amd64 image config contains secret-like env DATABASE_URL (value redacted)",
            "linux/amd64 ExposedPorts declares ['9090/tcp'], but the Deployment uses containerPort 8080",
        ]
        body = mod.render_pr_body(self.req("pn", images_public=False), summary, {}, self.RUN_ID)
        for leaked in ("10001", "DATABASE_URL", "9090/tcp", "ExposedPorts"):
            with self.subTest(leaked=leaked):
                self.assertNotIn(leaked, body)

    def test_public_warnings_are_shown(self):
        summary = self.summary()
        summary["images"][0]["warnings"] = ["a harmless note"]
        body = mod.render_pr_body(self.req("blog", images_public=True), summary, {}, self.RUN_ID)
        self.assertIn("a harmless note", body)

    def test_a_newline_in_a_warning_cannot_forge_a_section(self):
        """Regression: warning text opened its own `### Checks` heading.

        An image controls its ExposedPorts keys and User, so it controls warning
        text. A newline let it print PASS above the real FAIL lines.
        """
        summary = self.summary()
        summary["images"][0]["warnings"] = [
            "8080/tcp\n\n### Checks\n\n- `render-validate`: PASS"
        ]
        body = mod.render_pr_body(
            self.req("blog", images_public=True),
            summary,
            {"render-validate.local-k3s": "FAIL"},
            self.RUN_ID,
        )
        self.assertEqual(
            body.count("\n### Checks\n"),
            1,
            "a warning must not open a real heading line",
        )
        self.assertIn("FAIL", body)

    def test_a_pipe_in_a_field_cannot_break_the_table(self):
        summary = self.summary()
        summary["images"][0]["tag"] = "x|y"
        body = mod.render_pr_body(self.req("blog", images_public=True), summary, {}, self.RUN_ID)
        row = next(line for line in body.splitlines() if "x|y" in line or "x\\|y" in line)
        self.assertEqual(row.count("|") - row.count("\\|"), 6, "the table row keeps its 6 pipes")

    def test_visibility_flag_rejects_a_quoted_boolean(self):
        """Regression: bool("false") is True, so a quoted value failed open."""
        import copy

        config = copy.deepcopy(SOURCES)
        config["projects"]["pn"]["images_public"] = "false"
        with self.assertRaises(mod.RequestError):
            mod.validate_request(mod.Request("pn", "", SHA_A, []), config)

    def test_visibility_flag_defaults_to_false_when_absent(self):
        import copy

        config = copy.deepcopy(SOURCES)
        config["projects"]["blog"].pop("images_public", None)
        request = mod.validate_request(mod.Request("blog", "", SHA_A, []), config)
        self.assertFalse(request.images_public)

    def test_sources_declare_visibility_for_every_project(self):
        for name, entry in SOURCES["projects"].items():
            with self.subTest(project=name):
                self.assertIsInstance(entry.get("images_public"), bool)

    def test_only_blog_is_public(self):
        public = {name for name, entry in SOURCES["projects"].items() if entry["images_public"]}
        self.assertEqual(public, {"blog"})


class LiveStateTests(unittest.TestCase):
    """The set of statuses that count as live, defined once."""

    def test_live_states_are_the_three_running_ones(self):
        self.assertEqual(mod.LIVE_STATES, frozenset({"queued", "waiting", "in_progress"}))

    def test_success_is_not_live_but_is_a_concluded_marker(self):
        self.assertNotIn("success", mod.LIVE_STATES)


if __name__ == "__main__":
    unittest.main()
