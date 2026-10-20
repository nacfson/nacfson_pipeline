import importlib.util
import os
import stat
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "scripts" / "sync-github-issues.py"
REAL_ISSUES = ROOT / "docs" / "issues"
GH_ARGV = (
    "issue list --repo nacfson/nacfson_pipeline --state all "
    "--limit 100 --json number,title,state"
)

# Fixture copied from docs/issues/index.md. INDEX keeps the prose, the table
# header, and the #4 and #57 rows. ORIGINAL is the full index the CLI copies.
ORIGINAL = """\
# Filed GitHub issues

Navigation: [`INDEX.md`](../../INDEX.md) → this file → [`FIX_GithubIssue<number>.md`](FIX_GithubIssue57.md).

## Rule

When a GitHub issue is filed or analyzed:

1. Add `docs/issues/FIX_GithubIssue<number>.md`. Do not add a second `troubleshooting.md` per issue.
2. Add a row to this index. This file is the aggregate. The root [`INDEX.md`](../../INDEX.md) only links here.
3. If the issue changes a platform rule, update the live contract in [`contracts/`](../../contracts/) in the same change. Historical `specs/*/contracts/` notes do not override that live contract.
4. Say what was checked and what was not. A closed GitHub issue is not proof the cluster matches the note.

## Index

| Issue | State | Note | Live contract |
| :--- | :--- | :--- | :--- |
| [#65](https://github.com/nacfson/nacfson_pipeline/issues/65) Static project hub on the apex domain | OPEN | [`FIX_GithubIssue65.md`](FIX_GithubIssue65.md) | [`CONTRACT_TLS_INGRESS.md`](../../contracts/CONTRACT_TLS_INGRESS.md) |
| [#63](https://github.com/nacfson/nacfson_pipeline/issues/63) No declared load balancer in front of Traefik | OPEN | [`FIX_GithubIssue63.md`](FIX_GithubIssue63.md) | [`CONTRACT_TLS_INGRESS.md`](../../contracts/CONTRACT_TLS_INGRESS.md) |
| [#62](https://github.com/nacfson/nacfson_pipeline/issues/62) pn public egress is not declared | CLOSED | [`FIX_GithubIssue62.md`](FIX_GithubIssue62.md) | [`CONTRACT_ISOLATION_BUDGET.md`](../../contracts/CONTRACT_ISOLATION_BUDGET.md) |
| [#58](https://github.com/nacfson/nacfson_pipeline/issues/58) Logout `redirect_uri` is not allowlisted | OPEN | [`FIX_GithubIssue58.md`](FIX_GithubIssue58.md) | Not in the live contract. Sign-in allowlist is [`CONTRACT_AUTH_DOOR.md`](../../contracts/CONTRACT_AUTH_DOOR.md) §3.1 |
| [#57](https://github.com/nacfson/nacfson_pipeline/issues/57) Logout claims revoked when Keycloak deletion fails | OPEN | [`FIX_GithubIssue57.md`](FIX_GithubIssue57.md) | [`CONTRACT_AUTH_DOOR.md`](../../contracts/CONTRACT_AUTH_DOOR.md) §3.4 |
| [#54](https://github.com/nacfson/nacfson_pipeline/issues/54) Keycloak login form still shown for Google SSO | CLOSED | [`FIX_GithubIssue54.md`](FIX_GithubIssue54.md) | [`CONTRACT_AUTH_DOOR.md`](../../contracts/CONTRACT_AUTH_DOOR.md) §4 |
| [#48](https://github.com/nacfson/nacfson_pipeline/issues/48) GHCR image update should follow release tags | CLOSED | [`FIX_GithubIssue48.md`](FIX_GithubIssue48.md) | Not a live contract. Images are digest-pinned. |
| [#18](https://github.com/nacfson/nacfson_pipeline/issues/18) Preflight rejected pn egress to Keycloak | CLOSED | [`FIX_GithubIssue18.md`](FIX_GithubIssue18.md) | [`CONTRACT_AUTH_DOOR.md`](../../contracts/CONTRACT_AUTH_DOOR.md) §2 |
| [#4](https://github.com/nacfson/nacfson_pipeline/issues/4) SSO token and idle lifetimes | CLOSED | [`FIX_GithubIssue4.md`](FIX_GithubIssue4.md) | Not in the live contract. |
"""


def _index_fixture(text):
    kept = []
    for line in text.splitlines(True):
        if line.startswith("| [#") and not (
            line.startswith("| [#57](") or line.startswith("| [#4](")
        ):
            continue
        kept.append(line)
    return "".join(kept)


INDEX = _index_fixture(ORIGINAL)
PROSE = INDEX.split("| Issue |", 1)[0]


def load():
    if not SCRIPT.is_file():
        raise AssertionError("apply_issues not defined")
    spec = importlib.util.spec_from_file_location("sync_github_issues", SCRIPT)
    if spec is None or spec.loader is None:
        raise AssertionError("apply_issues not defined")
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    if not hasattr(mod, "apply_issues"):
        raise AssertionError("apply_issues not defined")
    return mod


def apply_issues(*args, **kwargs):
    return load().apply_issues(*args, **kwargs)


def _files(path):
    if not path.is_dir():
        return {}
    return {item.name: item.read_bytes() for item in path.iterdir() if item.is_file()}


def _restore_real(before):
    after = _files(REAL_ISSUES)
    if after == before:
        return
    for name in set(after) - set(before):
        (REAL_ISSUES / name).unlink()
    for name, data in before.items():
        current = REAL_ISSUES / name
        if not current.exists() or current.read_bytes() != data:
            current.write_bytes(data)
    raise AssertionError("CLI wrote the real docs/issues tree; restored it")


def run_cli(tmp_path, succeed=False):
    issues = tmp_path / "docs" / "issues"
    issues.mkdir(parents=True)
    (issues / "index.md").write_text(ORIGINAL, encoding="utf-8")
    bindir = tmp_path / "bin"
    bindir.mkdir()
    gh = bindir / "gh"
    if succeed:
        gh.write_text(
            "#!/bin/sh\n"
            'printf "%s\\n" "$*" > "${GH_ARGS_FILE:?}"\n'
            "printf '%s\\n' '[{\"number\":99,\"title\":\"New hub bug\",\"state\":\"OPEN\"}]'\n",
            encoding="utf-8",
        )
    else:
        gh.write_text("#!/bin/sh\nexit 1\n", encoding="utf-8")
    gh.chmod(gh.stat().st_mode | stat.S_IEXEC)
    env = os.environ.copy()
    env["PATH"] = str(bindir)
    env["GH_ARGS_FILE"] = str(tmp_path / "gh.args")
    return subprocess.run(
        [sys.executable, str(SCRIPT)],
        cwd=tmp_path,
        env=env,
        capture_output=True,
        text=True,
    )


class TestSyncGithubIssues(unittest.TestCase):
    def test_state_change_keeps_fix_body_and_contract_cell(self):
        result = apply_issues(
            INDEX,
            [{"number": 57, "title": "Logout claims revoked", "state": "CLOSED"}],
            {"FIX_GithubIssue57.md"},
        )
        assert result.index_text.startswith(PROSE)
        assert "| CLOSED |" in result.index_text
        assert "CONTRACT_AUTH_DOOR.md" in result.index_text
        assert result.stubs == {}
        assert (
            "| [#57](https://github.com/nacfson/nacfson_pipeline/issues/57) Logout claims revoked | CLOSED |"
            in result.index_text
        )
        assert "when Keycloak deletion fails" not in result.index_text
        assert "[`FIX_GithubIssue57.md`](FIX_GithubIssue57.md)" in result.index_text

    def test_new_issue_adds_stub_and_not_checked_row(self):
        result = apply_issues(
            INDEX,
            [{"number": 99, "title": "New hub bug", "state": "OPEN"}],
            set(),
        )
        assert result.index_text.startswith(PROSE)
        assert "FIX_GithubIssue99.md" in result.stubs
        assert "Not analyzed." in result.stubs["FIX_GithubIssue99.md"]
        assert "Not checked." in result.index_text
        assert "[#99](https://github.com/nacfson/nacfson_pipeline/issues/99)" in result.index_text
        assert result.stubs["FIX_GithubIssue99.md"] == (
            "# FIX_GithubIssue99\n"
            "\n"
            "- Issue: https://github.com/nacfson/nacfson_pipeline/issues/99\n"
            "- State: OPEN. Created by scripts/sync-github-issues.py. Not analyzed.\n"
            "- Live contract: Not checked.\n"
            "\n"
            "## What is wrong\n"
            "\n"
            "New hub bug\n"
            "\n"
            "## Not checked\n"
            "\n"
            "This file was created on project open. It does not replace a human analysis.\n"
        )
        assert (
            "| [#99](https://github.com/nacfson/nacfson_pipeline/issues/99) New hub bug | OPEN | [`FIX_GithubIssue99.md`](FIX_GithubIssue99.md) | Not checked. |"
            in result.index_text
        )

    def test_missing_issue_row_is_kept(self):
        result = apply_issues(
            INDEX,
            [{"number": 57, "title": "Logout claims revoked", "state": "OPEN"}],
            {"FIX_GithubIssue57.md"},
        )
        assert result.index_text.startswith(PROSE)
        assert "[#4](" in result.index_text
        assert (
            "| [#4](https://github.com/nacfson/nacfson_pipeline/issues/4) SSO token and idle lifetimes | CLOSED | [`FIX_GithubIssue4.md`](FIX_GithubIssue4.md) | Not in the live contract. |"
            in result.index_text
        )

    def test_cli_leaves_tree_unchanged_when_gh_fails(self):
        before = _files(REAL_ISSUES)
        with tempfile.TemporaryDirectory() as tmp:
            tmp_path = Path(tmp)
            try:
                assert run_cli(tmp_path).returncode == 1
                assert (tmp_path / "docs/issues/index.md").read_text() == ORIGINAL
                assert [p.name for p in (tmp_path / "docs/issues").iterdir()] == ["index.md"]
            finally:
                _restore_real(before)

    def test_cli_refreshes_temp_copy_when_gh_succeeds(self):
        before = _files(REAL_ISSUES)
        with tempfile.TemporaryDirectory() as tmp:
            tmp_path = Path(tmp)
            try:
                proc = run_cli(tmp_path, succeed=True)
                assert proc.returncode == 0, proc.stderr
                text = (tmp_path / "docs/issues/index.md").read_text()
                assert text.startswith("# Filed GitHub issues\n")
                assert "[#4](" in text
                assert "[#99](https://github.com/nacfson/nacfson_pipeline/issues/99)" in text
                assert "Not checked." in text
                stub = (tmp_path / "docs/issues/FIX_GithubIssue99.md").read_text()
                assert "Not analyzed." in stub
                assert (tmp_path / "gh.args").read_text().strip() == GH_ARGV
            finally:
                _restore_real(before)


if __name__ == "__main__":
    unittest.main()
