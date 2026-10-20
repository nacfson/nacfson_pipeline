# OpenCode GitHub issue sync Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Opening this repo in OpenCode attaches the GitHub MCP server and refreshes `docs/issues/index.md` without rewriting existing `FIX_` notes.

**Architecture:** Project-local remote MCP gives the agent issue tools. A Python script, started once from a plugin `setup()`, updates the index from `gh issue list`. MCP is not the file writer. OAuth stays outside Git.

**Tech Stack:** OpenCode V2 `mcp.servers`, `opencode mcp add`, Python 3 unittest, `.opencode/plugins/` `Plugin.define`.

**Spec:** `docs/superpowers/specs/2026-10-20-opencode-github-issue-sync-design.md`

## Global Constraints

- Repo is exactly `nacfson/nacfson_pipeline`. No `--global` MCP flag.
- Config shape is `mcp.servers.github` with `"type": "remote"` and `"url": "https://api.githubcopilot.com/mcp/"`. No `enabled`. No `mcp.github`.
- Header `X-MCP-Toolsets` is exactly `context,issues`.
- No PAT, bearer token, or `gho_` string in any committed file. Env fallback, only if OAuth fails, is `Authorization: Bearer {env:GITHUB_PERSONAL_ACCESS_TOKEN}` plus `"oauth": false`.
- Do not run `opencode mcp auth` from a shell step.
- Do not edit `contracts/`, `deploy/`, `clusters/`, `gateway/`, `services/`, `bootstrap/`, `specs/`, `ARCHITECTURE.md`, `DEPENDENCIES.md`, or `docs/platform-architecture.html`.
- Do not rewrite `.opencode/plugins/arch-doc-sync.js`.
- Do not commit unrelated dirty files (`.agents/teamwork/`, `docs/security-and-complexity-audit.md`, the 2026-10-19 translation-egress plan/spec).
- Do not `git push`. Direct push to `main` is rejected.
- Sync must not overwrite an existing `FIX_GithubIssue<number>.md` body, must not drop an index row that this fetch did not return, and must not edit the contract column.

## Review Focus

- A partial `gh` page must not delete issue `#4` from the index. Task 2 asserts a missing issue row stays.
- A state change must not replace the `FIX_` body or the contract cell. Task 2 asserts both stay byte-for-byte.
- `gh` failing must not write an empty index. Task 2 asserts the CLI leaves the tree unchanged on exit 1.
- A committed config that contains `enabled`, `mcp.github`, or a raw token would ship a secret or a deprecated shape. Task 1 asserts the written JSON.
- Plugin `setup()` throwing would block OpenCode open. Task 3 catches a nonzero script exit and does not rethrow.

---

### Task 1: Attach the GitHub MCP server

**Files:**
- Create: the project config file `opencode mcp add` writes (expected `.opencode/opencode.jsonc` or `opencode.jsonc` at the repo root)
- Test: `tests/issues/test_opencode_mcp_config.py`

**Interfaces:**
- Consumes: none
- Produces: a project config whose parsed `mcp.servers.github` is `{"type": "remote", "url": "https://api.githubcopilot.com/mcp/", "headers": {"X-MCP-Toolsets": "context,issues"}}`. OAuth stays at the default. No `oauth` key unless Task 1 Step 4's fallback runs.

- [ ] **Step 1: Write the failing test**

```python
def test_github_mcp_is_project_local_and_issue_scoped():
    text = config_text()
    assert "gho_" not in text and "github_pat_" not in text and "Bearer ghp_" not in text
    data = json.loads(strip_jsonc(text))
    github = data["mcp"]["servers"]["github"]
    assert github["type"] == "remote"
    assert github["url"] == "https://api.githubcopilot.com/mcp/"
    assert github["headers"]["X-MCP-Toolsets"] == "context,issues"
    assert "enabled" not in github
    assert "github" not in data["mcp"] or data["mcp"]["github"] == data["mcp"]["servers"]["github"]
```

`config_text()` reads the first of `.opencode/opencode.jsonc`, `.opencode/opencode.json`, `opencode.jsonc`, `opencode.json` that exists. `strip_jsonc` removes `//` comments before `json.loads`.

- [ ] **Step 2: Run test to verify it fails**

Run: `python3 -m unittest tests.issues.test_opencode_mcp_config -v`
Expected: FAIL because no project MCP config exists.

- [ ] **Step 3: Add the server from the repo root**

```bash
opencode mcp add github --url https://api.githubcopilot.com/mcp/ --header 'X-MCP-Toolsets=context,issues'
```

If the CLI writes `mcp.github` or `"enabled": true`, move the object to `mcp.servers.github` and delete `enabled` before the test can pass. Do not add `--global`.

- [ ] **Step 4: Confirm connection without starting OAuth from the shell**

Run: `opencode mcp list`
Expected: a `github` row. `connected` passes. `needs authentication` also passes this task: tell the user to open OpenCode and run `/mcps`, then select `github`. Do not run `opencode mcp auth`.

If the list says OAuth is unsupported, edit only that server to add `"oauth": false` and `"headers.Authorization": "Bearer {env:GITHUB_PERSONAL_ACCESS_TOKEN}"`, keep `X-MCP-Toolsets`, and extend the test to require that exact env reference and reject any other `Authorization` value.

- [ ] **Step 5: Run test to verify it passes**

Run: `python3 -m unittest tests.issues.test_opencode_mcp_config -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add .opencode/opencode.jsonc opencode.jsonc .opencode/opencode.json opencode.json tests/issues/test_opencode_mcp_config.py
git commit -m "feat(opencode): attach project-local GitHub issue MCP"
```

Add only the config path that exists.

### Task 2: Refresh the issue index without clobbering notes

**Files:**
- Create: `scripts/sync-github-issues.py`
- Test: `tests/issues/test_sync_github_issues.py`
- Modify: `docs/issues/index.md` only when the CLI runs for real

**Interfaces:**
- Consumes: the table in `docs/issues/index.md`. Columns are `Issue | State | Note | Live contract`.
- Produces: `apply_issues(index_text: str, issues: list[dict], existing_fix_names: set[str]) -> ApplyResult`
  - `ApplyResult.index_text: str`
  - `ApplyResult.stubs: dict[str, str]` mapping `FIX_GithubIssue<number>.md` to stub markdown
  - Issue dict keys are `number: int`, `title: str`, `state: str` (`OPEN` or `CLOSED`)

- [ ] **Step 1: Write the failing tests**

```python
def test_state_change_keeps_fix_body_and_contract_cell():
    result = apply_issues(INDEX, [{"number": 57, "title": "Logout claims revoked", "state": "CLOSED"}], {"FIX_GithubIssue57.md"})
    assert "| CLOSED |" in result.index_text
    assert "CONTRACT_AUTH_DOOR.md" in result.index_text
    assert result.stubs == {}

def test_new_issue_adds_stub_and_not_checked_row():
    result = apply_issues(INDEX, [{"number": 99, "title": "New hub bug", "state": "OPEN"}], set())
    assert "FIX_GithubIssue99.md" in result.stubs
    assert "Not analyzed." in result.stubs["FIX_GithubIssue99.md"]
    assert "Not checked." in result.index_text
    assert "[#99](https://github.com/nacfson/nacfson_pipeline/issues/99)" in result.index_text

def test_missing_issue_row_is_kept():
    result = apply_issues(INDEX, [{"number": 57, "title": "Logout claims revoked", "state": "OPEN"}], {"FIX_GithubIssue57.md"})
    assert "[#4](" in result.index_text

def test_cli_leaves_tree_unchanged_when_gh_fails(tmp_path):
    # point the CLI at a copy of docs/issues and a gh stand-in that exits 1
    assert run_cli(tmp_path).returncode == 1
    assert (tmp_path / "docs/issues/index.md").read_text() == ORIGINAL
```

`INDEX` is a fixture copied from the current `docs/issues/index.md` table header plus the `#4` and `#57` rows. The prose above the table must be unchanged in every test.

- [ ] **Step 2: Run tests to verify they fail**

Run: `python3 -m unittest tests.issues.test_sync_github_issues -v`
Expected: FAIL with `apply_issues` not defined.

- [ ] **Step 3: Implement `apply_issues` and the CLI**

CLI: `python3 scripts/sync-github-issues.py`. It runs `gh issue list --repo nacfson/nacfson_pipeline --state all --limit 100 --json number,title,state`. Nonzero `gh` exits 1 and writes nothing.

`apply_issues` keeps every line before the `| Issue |` table. It updates `State` and the title text for numbers present in `issues`. It keeps the `Live contract` cell. It appends a row only for a number that has no row. A stub is returned only when `FIX_GithubIssue<number>.md` is not in `existing_fix_names`. Stub text is:

```markdown
# FIX_GithubIssue{number}

- Issue: https://github.com/nacfson/nacfson_pipeline/issues/{number}
- State: {STATE}. Created by scripts/sync-github-issues.py. Not analyzed.
- Live contract: Not checked.

## What is wrong

{title}

## Not checked

This file was created on project open. It does not replace a human analysis.
```

Write the index with a same-directory temp file and `os.replace`. Write each stub the same way.

- [ ] **Step 4: Run tests to verify they pass**

Run: `python3 -m unittest tests.issues.test_sync_github_issues -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add scripts/sync-github-issues.py tests/issues/test_sync_github_issues.py
git commit -m "feat(issues): refresh the issue index without clobbering FIX notes"
```

### Task 3: Run the sync when OpenCode loads the project

**Files:**
- Create: `.opencode/plugins/github-issue-sync/index.ts`
- Test: `tests/issues/test_github_issue_sync_plugin.py`

**Interfaces:**
- Consumes: `python3 scripts/sync-github-issues.py` from Task 2. Exit 0 means the index was refreshed or was already current. Exit 1 means `gh` failed and files were left unchanged.
- Produces: plugin id `nacfson-github-issue-sync`. `setup(ctx)` spawns that script with `cwd` = `ctx.location.project.canonical` and does not throw if the script exits nonzero.

- [ ] **Step 1: Write the failing test**

```python
def test_plugin_spawns_sync_and_swallows_failure():
    text = (ROOT / ".opencode/plugins/github-issue-sync/index.ts").read_text()
    assert 'id: "nacfson-github-issue-sync"' in text
    assert "scripts/sync-github-issues.py" in text
    assert "ctx.location.project.canonical" in text
    assert "catch" in text
```

This pins the load hook. It does not start OpenCode.

- [ ] **Step 2: Run test to verify it fails**

Run: `python3 -m unittest tests.issues.test_github_issue_sync_plugin -v`
Expected: FAIL because the plugin file is missing.

- [ ] **Step 3: Implement the plugin**

```ts
import { Plugin } from "@opencode/plugin"
import { spawn } from "node:child_process"

export default Plugin.define({
  id: "nacfson-github-issue-sync",
  setup(ctx) {
    const cwd = ctx.location.project.canonical
    const child = spawn("python3", ["scripts/sync-github-issues.py"], { cwd, stdio: "ignore" })
    child.on("error", () => {})
    child.on("exit", () => {})
  },
})
```

Do not `await` the child. Do not throw. If `Plugin.define` does not load on OpenCode v2.1.0, stop and report that. Do not copy the `server:` export from `arch-doc-sync.js`.

- [ ] **Step 4: Run test to verify it passes**

Run: `python3 -m unittest tests.issues.test_github_issue_sync_plugin -v`
Expected: PASS

- [ ] **Step 5: Add the rule sentence and commit**

In `docs/issues/index.md`, under Rule, add: `Opening this repo in OpenCode runs scripts/sync-github-issues.py once. That refreshes this index and creates a missing FIX_ file. It does not rewrite an existing FIX_ body and it does not edit contracts/.`

```bash
git add .opencode/plugins/github-issue-sync/index.ts tests/issues/test_github_issue_sync_plugin.py docs/issues/index.md
git commit -m "feat(opencode): sync GitHub issues into docs/issues on project open"
```

---

## Self-review

- Spec coverage: MCP attach is Task 1. On-open refresh, stub-only create, and no clobber are Tasks 2 and 3. No token in Git is Task 1. No contract edits is a global constraint. OAuth sign-in is a user step, not a shell step.
- The plugin test does not execute OpenCode. A missed `setup()` throw is only pinned by the source asserting `catch` or empty `error` handlers. That is the check this repo can run without a TUI.
- `apply_issues` is the only sync function later tasks rely on. The plugin calls the CLI, not the function.
