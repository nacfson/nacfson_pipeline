# OpenCode GitHub issue sync

## Intent

Opening this repo in OpenCode should attach GitHub as an MCP server and refresh the filed-issue index. The agent can read issues during the session. The files under `docs/issues/` stay the local aggregate.

## Goals

- Project-local GitHub MCP, not a global server.
- On project open, refresh `docs/issues/index.md` from `nacfson/nacfson_pipeline` issues.
- Create `docs/issues/FIX_GithubIssue<number>.md` only when that file is missing.
- Leave an existing `FIX_` body alone, including its contract column.

## Non-goals

- Do not commit, push, or edit `contracts/`.
- Do not close or comment on GitHub issues.
- Do not overwrite a human analysis with the issue body.
- Do not attach every GitHub toolset. Issues and context only.
- Do not store a token in Git.

## Decisions

- MCP config is OpenCode V2: `mcp.servers.github`. Ignore the GitHub install guide's `mcp.github` and `enabled` fields. Those are deprecated here.
- Remote URL is `https://api.githubcopilot.com/mcp/`.
- Auth is OAuth. The user signs in with `/mcps` if `opencode mcp list` says the server needs authentication. Do not run `opencode mcp auth` from a script.
- If that remote server rejects OAuth on this host, the fallback is `oauth: false` and `Authorization: Bearer {env:GITHUB_PERSONAL_ACCESS_TOKEN}`. Still no token in the file.
- File sync uses `gh`, which is already logged in on this machine. It does not call MCP. MCP connect is async and must not be what writes the index.
- A failed `gh` call leaves every file unchanged.
- An issue missing from one `gh` page is not deleted from the index.
