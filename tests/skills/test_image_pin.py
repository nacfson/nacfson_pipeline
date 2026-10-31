"""Tests for scripts/image-pin.py: request checks, run markers, dedupe, supersede.

The dedupe rules are security-relevant, not just cosmetic. A run's own marker is
the only thing that may suppress another request, and only when that run's
`validate` actually succeeded. See the step-1 plan, Review Focus 1 and 2.
"""

import importlib.util
import json
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


class LiveStateTests(unittest.TestCase):
    """The set of statuses that count as live, defined once."""

    def test_live_states_are_the_three_running_ones(self):
        self.assertEqual(mod.LIVE_STATES, frozenset({"queued", "waiting", "in_progress"}))

    def test_success_is_not_live_but_is_a_concluded_marker(self):
        self.assertNotIn("success", mod.LIVE_STATES)


if __name__ == "__main__":
    unittest.main()
