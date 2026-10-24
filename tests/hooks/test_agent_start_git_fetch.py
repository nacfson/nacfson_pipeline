import importlib.util
import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
FETCH_SCRIPT = ROOT / "scripts" / "agent-start-git-fetch.py"
HOOK_SCRIPT = ROOT / "scripts" / "universal-agent-hook.py"

_spec = importlib.util.spec_from_file_location("agent_start_git_fetch", FETCH_SCRIPT)
assert _spec and _spec.loader
fetch_module = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(fetch_module)


def git(cwd, *args, check=True):
    return subprocess.run(
        ["git", *args],
        cwd=cwd,
        text=True,
        capture_output=True,
        check=check,
    )


class AgentStartGitFetchTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        base = Path(self.tmp.name)
        self.base = base
        self.remote = base / "remote.git"
        self.work = base / "work"
        self.other = base / "other"

        git(base, "init", "--bare", "-b", "main", str(self.remote))
        git(base, "clone", str(self.remote), str(self.work))
        for repo in (self.work,):
            git(repo, "config", "user.email", "test@example.com")
            git(repo, "config", "user.name", "Test User")
        (self.work / "a.txt").write_text("one\n", encoding="utf-8")
        git(self.work, "add", "a.txt")
        git(self.work, "commit", "-m", "one")
        git(self.work, "push", "-u", "origin", "main")

        git(base, "clone", str(self.remote), str(self.other))
        git(self.other, "config", "user.email", "test@example.com")
        git(self.other, "config", "user.name", "Test User")

    def publish_commit(self, filename, content):
        (self.other / filename).write_text(content, encoding="utf-8")
        git(self.other, "add", filename)
        git(self.other, "commit", "-m", f"add {filename}")
        git(self.other, "push", "origin", "main")

    def fetch(self, **kwargs):
        kwargs.setdefault("env", dict(os.environ))
        return fetch_module.fetch(self.work, **kwargs)

    def test_fetch_updates_remote_tracking_ref_without_touching_worktree(self):
        head_before = git(self.work, "rev-parse", "HEAD").stdout.strip()
        self.publish_commit("b.txt", "two\n")

        result = self.fetch(cooldown=0)

        self.assertEqual(result["status"], "fetched")
        self.assertEqual(result["behind"], 1)
        self.assertEqual(result["ahead"], 0)
        self.assertIn("behind 1", result["message"])
        self.assertEqual(git(self.work, "rev-parse", "HEAD").stdout.strip(), head_before)
        self.assertFalse((self.work / "b.txt").exists())
        self.assertEqual(
            git(self.work, "rev-parse", "origin/main").stdout.strip(),
            git(self.other, "rev-parse", "HEAD").stdout.strip(),
        )

    def test_second_fetch_within_cooldown_is_skipped(self):
        self.assertEqual(self.fetch(cooldown=60)["status"], "fetched")
        self.publish_commit("c.txt", "three\n")

        result = self.fetch(cooldown=60)

        self.assertEqual(result["status"], "skipped")
        self.assertIn("cooldown", result["reason"])
        self.assertNotEqual(
            git(self.work, "rev-parse", "origin/main").stdout.strip(),
            git(self.other, "rev-parse", "HEAD").stdout.strip(),
        )

    def test_force_bypasses_cooldown(self):
        self.assertEqual(self.fetch(cooldown=60)["status"], "fetched")
        self.publish_commit("d.txt", "four\n")

        result = self.fetch(cooldown=60, force=True)

        self.assertEqual(result["status"], "fetched")
        self.assertEqual(result["behind"], 1)

    def test_stale_remote_ref_is_kept_unless_prune_is_enabled(self):
        stale_sha = git(self.work, "rev-parse", "HEAD").stdout.strip()
        git(self.work, "update-ref", "refs/remotes/origin/stale-branch", stale_sha)

        self.assertEqual(self.fetch(cooldown=0)["status"], "fetched")
        self.assertTrue(
            git(
                self.work,
                "show-ref",
                "--verify",
                "--quiet",
                "refs/remotes/origin/stale-branch",
                check=False,
            ).returncode
            == 0
        )

        env = dict(os.environ, AGENT_START_FETCH_PRUNE="1")
        result = fetch_module.fetch(self.work, cooldown=0, env=env)

        self.assertEqual(result["status"], "fetched")
        self.assertNotEqual(
            git(
                self.work,
                "show-ref",
                "--verify",
                "--quiet",
                "refs/remotes/origin/stale-branch",
                check=False,
            ).returncode,
            0,
        )

    def test_disable_env_skips_fetch(self):
        env = dict(os.environ, AGENT_START_FETCH_DISABLE="1")
        result = fetch_module.fetch(self.work, env=env)

        self.assertEqual(result["status"], "skipped")
        self.assertIn("AGENT_START_FETCH_DISABLE", result["reason"])

    def test_outside_git_repository_is_skipped(self):
        with tempfile.TemporaryDirectory() as outside:
            result = fetch_module.fetch(outside, env=dict(os.environ))

        self.assertEqual(result["status"], "skipped")
        self.assertIn("git work tree", result["reason"])

    def test_repository_without_remote_is_skipped(self):
        local = self.base / "local-only"
        git(self.base, "init", "-b", "main", str(local))

        result = fetch_module.fetch(local, env=dict(os.environ))

        self.assertEqual(result["status"], "skipped")
        self.assertIn("no git remote", result["reason"])

    def test_unreachable_remote_fails_open(self):
        git(self.work, "remote", "set-url", "origin", str(self.base / "missing.git"))

        result = self.fetch(cooldown=0)

        self.assertEqual(result["status"], "error")
        self.assertIn("Working tree unchanged", result["message"])

    def test_cli_always_exits_zero(self):
        proc = subprocess.run(
            [sys.executable, str(FETCH_SCRIPT), "--format", "json", "--cwd", str(self.base / "not-a-repo")],
            text=True,
            capture_output=True,
            env=dict(os.environ),
            check=False,
        )

        self.assertEqual(proc.returncode, 0)
        self.assertEqual(json.loads(proc.stdout)["status"], "skipped")


class UniversalHookSessionStartTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        base = Path(self.tmp.name)
        self.remote = base / "remote.git"
        self.work = base / "work"
        git(base, "init", "--bare", "-b", "main", str(self.remote))
        git(base, "clone", str(self.remote), str(self.work))
        git(self.work, "config", "user.email", "test@example.com")
        git(self.work, "config", "user.name", "Test User")
        (self.work / "a.txt").write_text("one\n", encoding="utf-8")
        git(self.work, "add", "a.txt")
        git(self.work, "commit", "-m", "one")
        git(self.work, "push", "-u", "origin", "main")

    def run_hook(self, agent, payload):
        env = dict(os.environ, AGENT_START_FETCH_COOLDOWN="0")
        env.pop("AGENT_START_FETCH_DISABLE", None)
        return subprocess.run(
            [sys.executable, str(HOOK_SCRIPT), "--agent", agent, "--event", "SessionStart"],
            input=json.dumps(payload),
            text=True,
            capture_output=True,
            cwd=self.work,
            env=env,
            check=False,
        )

    def test_claude_receives_additional_context(self):
        proc = self.run_hook("claude", {"hook_event_name": "SessionStart", "source": "startup"})

        self.assertEqual(proc.returncode, 0)
        data = json.loads(proc.stdout.strip())
        self.assertEqual(data["hookSpecificOutput"]["hookEventName"], "SessionStart")
        self.assertIn("[git fetch]", data["hookSpecificOutput"]["additionalContext"])

    def test_cursor_receives_additional_context(self):
        proc = self.run_hook("cursor", {"hook_event_name": "sessionStart"})

        self.assertEqual(proc.returncode, 0)
        data = json.loads(proc.stdout.strip())
        self.assertIn("[git fetch]", data["additional_context"])

    def test_antigravity_receives_plain_status(self):
        proc = self.run_hook("antigravity", {"hookEventName": "SessionStart"})

        self.assertEqual(proc.returncode, 0)
        self.assertIn("[git fetch]", proc.stdout)

    def test_auto_event_detection_maps_session_start(self):
        proc = subprocess.run(
            [sys.executable, str(HOOK_SCRIPT), "--agent", "claude", "--event", "auto"],
            input=json.dumps({"hook_event_name": "SessionStart"}),
            text=True,
            capture_output=True,
            cwd=self.work,
            env=dict(os.environ, AGENT_START_FETCH_DISABLE="1"),
            check=False,
        )

        self.assertEqual(proc.returncode, 0)
        self.assertEqual(proc.stdout.strip(), "")


if __name__ == "__main__":
    unittest.main()
