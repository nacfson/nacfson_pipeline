import argparse
import importlib.util
import os
import pathlib
import tempfile
import threading
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location("debug_signin", ROOT / "scripts" / "ops" / "debug-signin.py")
ds = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(ds)

SID = "25fb6e52-2e25-440e-87de-50e3e991d11c"


def ev(ts, level, type_, client="gateway-client", user="null", error=None, sid=SID, day="2026-11-15"):
    parts = [f'type="{type_}"', 'realmId="r"', f'clientId="{client}"', f'userId="{user}"', f'sessionId="{sid}"',
             'ipAddress="10.24.2.33"']
    if error:
        parts.append(f'error="{error}"')
    return f"{day} {ts} {level}  [org.keycloak.events] (executor-thread-2) " + ", ".join(parts) + "\n"


class ClassifyTest(unittest.TestCase):
    def test_observed_production_line_is_missing_link(self):
        e = ds.parse_event(ev("23:32:51,337", "WARN", "CODE_TO_TOKEN_ERROR", error="user_session_not_found"))
        self.assertEqual(ds.classify(e)[0], "LINK")

    def test_session_not_active_has_user(self):
        e = ds.parse_event(ev("23:32:51,337", "WARN", "CODE_TO_TOKEN_ERROR", user="u1", error="user_session_not_found"))
        self.assertEqual(ds.classify(e)[0], "ACTIVE")

    def test_secret_drift(self):
        e = ds.parse_event(ev("23:32:51,337", "WARN", "CODE_TO_TOKEN_ERROR", error="invalid_client_credentials"))
        self.assertEqual(ds.classify(e)[0], "SECRET")

    def test_non_event_line_ignored(self):
        self.assertIsNone(ds.parse_event("2026-11-15 23:32:51,337 INFO  [io.quarkus] started\n"))

    def test_probe_client_events_ignored(self):
        line = ev("23:32:51,337", "WARN", "CODE_TO_TOKEN_ERROR", client="debug-signin-probe", error="client_not_found", sid="null")
        self.assertIsNone(ds.parse_event(line))
        self.assertEqual(ds.analyze_lines([line])[1], [])
        self.assertNotIn("POST /realms/platform/protocol/openid-connect/token", ds.PROBE_SH)


class AnalyzeTest(unittest.TestCase):
    def test_parallel_logins_flag_race(self):
        lines = [
            ev("23:32:49,100", "DEBUG", "LOGIN", user="u1"),
            ev("23:32:49,180", "DEBUG", "LOGIN", user="u1"),
            ev("23:32:50,000", "DEBUG", "CODE_TO_TOKEN", user="u1"),
            ev("23:32:51,337", "WARN", "CODE_TO_TOKEN_ERROR", error="user_session_not_found"),
        ]
        _, findings = ds.analyze_lines(lines)
        self.assertEqual(len(findings), 1)
        self.assertTrue(any(v.startswith("RACE") for v in findings[0]["verdicts"]))

    def test_single_login_is_unproven(self):
        lines = [
            ev("23:32:49,100", "DEBUG", "LOGIN", user="u1"),
            ev("23:32:51,337", "WARN", "CODE_TO_TOKEN_ERROR", error="user_session_not_found"),
        ]
        _, findings = ds.analyze_lines(lines)
        self.assertTrue(findings[0]["verdicts"][0].startswith("UNPROVEN"))

    def test_old_session_flags_lifespan(self):
        lines = [
            ev("13:32:50,000", "DEBUG", "LOGIN", user="u1"),
            ev("23:32:51,337", "WARN", "CODE_TO_TOKEN_ERROR", error="user_session_not_found"),
        ]
        _, findings = ds.analyze_lines(lines)
        self.assertTrue(any(v.startswith("LIFESPAN") for v in findings[0]["verdicts"]))

    def test_reuse_warning_detected(self):
        lines = [
            ev("23:32:49,100", "DEBUG", "LOGIN", user="u1"),
            f"2026-11-15 23:32:51,300 WARN  [org.keycloak.protocol.oidc.utils.OAuth2CodeParser] Code 'c' already used for userSession '{SID}' and client 'x'.\n",
            ev("23:32:51,337", "WARN", "CODE_TO_TOKEN_ERROR", error="user_session_not_found"),
        ]
        _, findings = ds.analyze_lines(lines)
        self.assertTrue(any(v.startswith("REUSE") for v in findings[0]["verdicts"]))
        self.assertTrue(findings[0]["context"])

    def test_null_session_errors_are_not_grouped(self):
        lines = [
            ev("10:00:00,000", "WARN", "CODE_TO_TOKEN_ERROR", error="invalid_code", sid="null"),
            ev("23:32:51,337", "WARN", "CODE_TO_TOKEN_ERROR", error="expired_code", sid="null"),
            "2026-11-15 23:32:51,300 WARN  [x] Code 'c' already used for userSession 'null-ish' and client 'x'.\n",
        ]
        _, findings = ds.analyze_lines(lines)
        self.assertEqual(len(findings), 2)
        self.assertEqual({f["session"] for f in findings}, {"none"})
        self.assertEqual({f["tag"] for f in findings}, {"CODE", "SLOW"})
        self.assertFalse(any(v.startswith(("LIFESPAN", "REUSE")) for f in findings for v in f["verdicts"]))


class SessionsParseTest(unittest.TestCase):
    def test_parses_marked_kcadm_output(self):
        out = ('@@USERS\n[ {\n  "id" : "u1",\n  "email" : "a@b"\n} ]\n'
               '@@SESSIONS u1\n[ {\n  "id" : "s1",\n  "start" : 1,\n  "lastAccess" : 2,\n  "clients" : { "x" : "gateway-client" }\n} ]\n')
        users, sessions = ds.parse_sessions_output(out)
        self.assertEqual(users[0]["id"], "u1")
        self.assertEqual(sessions["u1"][0]["clients"]["x"], "gateway-client")


class KubeQuotingTest(unittest.TestCase):
    def test_ssh_wrapper_quotes_and_break_glass_only_on_writes(self):
        captured = []
        orig_run = ds.subprocess.run

        class P:
            returncode, stdout, stderr = 0, "", ""

        def fake_run(argv, **kw):
            captured.append(argv)
            return P()

        ds.subprocess.run = fake_run
        os.environ["KUBECTL"] = "ssh host sudo k3s kubectl"
        ds.STATE["break_glass"] = "nacfson"
        try:
            ds.kube(["exec", "--", "bash", "-c", "echo a && echo b"])
            ds.kube(["patch", "x"], write=True)
        finally:
            ds.subprocess.run = orig_run
            del os.environ["KUBECTL"]
            ds.STATE["break_glass"] = None
        self.assertIn("'echo a && echo b'", captured[0])
        self.assertFalse(any("--as" in x for x in captured[0]))
        self.assertIn("--as-group=platform:break-glass", captured[1])


class RaceDriverTest(unittest.TestCase):
    def setUp(self):
        self.orig = ds.fetch
        self.lock = threading.Lock()
        self.calls = []
        self.start_location = "https://auth.test/realms/platform/protocol/openid-connect/auth?x=1"
        self.cookie_sent_to = []

        def fake(opener, url, headers=None, timeout=20):
            with self.lock:
                self.calls.append(url)
                n = len(self.calls)
            if "/oauth/start" in url:
                return 302, self.start_location, ""
            if "/openid-connect/auth" in url:
                self.cookie_sent_to.append(url)
                return 302, f"https://auth.test/oauth/callback?code=c{n}&state=s", ""
            if url.endswith("code=c2&state=s"):
                return 503, "", '{"error":"unsigned_in","message":"Sign-in service unreachable"}'
            return 302, "https://pn.test/?ticket=t", ""

        ds.fetch = fake
        fd, self.cookie_file = tempfile.mkstemp()
        with os.fdopen(fd, "w") as f:
            f.write("KEYCLOAK_IDENTITY=abc; KEYCLOAK_SESSION=def")

    def tearDown(self):
        ds.fetch = self.orig
        ds.RESULTS.clear()
        os.unlink(self.cookie_file)

    def run_race(self, mode, workers=2, cookie_file=None):
        a = argparse.Namespace(auth_host="auth.test", return_url="https://pn.test/", sso_cookie_file=cookie_file or self.cookie_file,
                               workers=workers, rounds=1, mode=mode, shared_auth_session=False, pause=0)
        return ds.cmd_race(a)

    def test_sequential_reports_reproduction(self):
        self.assertEqual(self.run_race("sequential"), 3)
        self.assertTrue(any(r[2].startswith("Race REPRODUCED") for r in ds.RESULTS))

    def test_parallel_runs_all_workers_through_barrier(self):
        rc = self.run_race("parallel", workers=4)
        self.assertIn(rc, (0, 3))
        self.assertEqual(sum(1 for r in ds.RESULTS if r[2].startswith("round 1 worker")), 4)
        self.assertEqual(len(self.cookie_sent_to), 4)

    def test_cookie_withheld_from_foreign_host(self):
        self.start_location = "http://evil.test/realms/platform/protocol/openid-connect/auth?x=1"
        self.run_race("parallel", workers=2)
        self.assertEqual(self.cookie_sent_to, [])
        self.assertTrue(any("cookie withheld" in r[3] for r in ds.RESULTS))

    def test_rejects_cookie_file_without_identity(self):
        fd, path = tempfile.mkstemp()
        try:
            with os.fdopen(fd, "w") as f:
                f.write("FOO=bar")
            self.assertEqual(self.run_race("parallel", cookie_file=path), 2)
        finally:
            os.unlink(path)

    def test_rejects_zero_workers(self):
        self.assertEqual(self.run_race("parallel", workers=0), 2)


if __name__ == "__main__":
    unittest.main()
