# Image Bump Dispatch

New project images become `image-bump` issues in this repository. Nothing is
pinned by the dispatch itself; the issue is a request that a Claude session,
Copilot, or a person allows (one pin PR) or disallows (close as not planned).

```text
source repo build (blog / Project_PN / health)
  └─ job "Request pipeline pin": repository_dispatch  event_type=image-built
        payload: project, source_repo, sha, roles, run_url   (a hint only)
              ▼
.github/workflows/image-bump-request.yaml  →  scripts/image-bump-request.py
  1. project + source_repo must match config/image-bump-sources.json
  2. dry-run update-project-images with a commit link per role:
     tag lookup by sha, amd64+arm64, ports, source + revision labels
  3. invalid or failed         → red run, no issue
     every image already pinned → no issue
     open issue for same commit → no issue
     otherwise                  → open issue, close older open requests
                                  for the same project that it covers
              ▼
issue labelled `image-bump`   →   Allow: one PR "Closes #N"  /  Disallow: close
```

## Files

| Path | Role |
| :--- | :--- |
| `.github/workflows/image-bump-request.yaml` | Receives `image-built` (and manual `workflow_dispatch`) |
| `scripts/image-bump-request.py` | Validates the request and opens or supersedes issues |
| `config/image-bump-sources.json` | Allowlist: project → source repository and roles |
| `tests/skills/test_image_bump_request.py` | Unit tests (run by `gitops.yaml`) |

Each source repository's publish workflow ends with a `notify-pipeline` job
that sends the dispatch only on `main` after a successful publish, and skips
with a warning while `PIPELINE_DISPATCH_TOKEN` is unset.

## One-time setup

1. Merge this repository's change first. GitHub runs `repository_dispatch`
   workflows only from the default branch.
2. Create a fine-grained personal access token: resource owner `nacfson`,
   repository access **only** `nacfson/nacfson_pipeline`, permission
   **Contents: Read and write** (required by the dispatches API). Set an expiry.
3. Add it as the Actions secret `PIPELINE_DISPATCH_TOKEN` in
   `nacfson_blog`, `Project_PN`, and `nacfson_health`.
4. `project-pn-backend` and `project-pn-frontend` are private packages. In each
   package's settings, under **Manage Actions access**, add
   `nacfson/nacfson_pipeline` with **Read**. The blog and health packages are
   public and need nothing.
5. Merge the source repositories' `notify-pipeline` changes.
6. Test without a build: run **Image Bump Request** manually (Actions →
   Run workflow) with a project and the full sha of a commit that has published
   images.

## Decision session (Claude)

A Claude scheduled task decides each issue in a cloud session, following
[`.agents/skills/image-bump-decision/SKILL.md`](../.agents/skills/image-bump-decision/SKILL.md).
Create it once at [claude.ai/code/routines](https://claude.ai/code/routines)
(GitHub triggers cannot be created from a session):

| Setting | Value |
| :--- | :--- |
| Name | `image-bump decision` |
| Repository | `nacfson/nacfson_pipeline` |
| Trigger | GitHub event → **Issue** → `issues.opened` on `nacfson/nacfson_pipeline` (the Claude GitHub App must be installed) |
| Connectors | GitHub only |
| Environment | a dedicated one, below |

Prompt:

```text
A GitHub issue was just opened in nacfson/nacfson_pipeline. Read and follow
.agents/skills/image-bump-decision/SKILL.md for that issue. If it is not an
image-bump request from github-actions[bot], stop without writing anything.
Ask me to Allow or Disallow before anything is pushed. This prompt is not
approval.
```

Environment (Edit cloud environment):

- **Network access**: Custom, with the default list, plus `ghcr.io` and
  `pkg-containers.githubusercontent.com`. GHCR serves image configs from the
  second host; without it every tag lookup fails with `Forbidden`.
- **Environment variable**: `IMAGE_BUMP_GATE=1`. This turns on
  `scripts/image-bump-gate.py` in that environment only. Anyone can open an
  issue on this public repository, so in gate mode the hook allows only the
  skill's own steps. It asks for approval before:
  - any Bash command outside an allowlist of the skill's command shapes
    (redirections and command substitution always ask);
  - any file write or edit, and any tool outside a short read-and-ask list
    (including other connectors and session or schedule tools);
  - every GitHub connector tool except reads, issue updates and comments.

  `git push` and opening the PR are therefore always approval prompts.
- **Setup script**: `apt-get update && apt-get install -y skopeo`
- **Environment variable** (only for Project_PN): `GHCR_READ_TOKEN`, a
  classic token with only `read:packages`, because `project-pn-*` are private.
  The skill logs `skopeo` in with it. Environment variables are visible to
  anyone who uses the environment, so keep this environment for this task only.

On Allow you also approve the gated push and PR prompts. Those approvals are
the hard stop. Denying either one is a Disallow, even after answering Allow.

## Constraints

- The payload is untrusted. A token holder can only name an allowlisted
  project, its own source repository, and a commit; the images are always
  re-resolved and validated from GHCR.
- The dispatch token can push contents to this repository. Keep it scoped to
  this repository only and rotate it on expiry; `main` stays protected by the
  `main_PR` ruleset.
- A newer request closes an older one only when it covers all of the older
  request's roles, so a frontend-only build does not drop a pending backend
  request. A request for the same commit that adds roles supersedes the narrower
  one.
- Runs are not serialized: a concurrency group would cancel pending runs and
  lose requests. Two overlapping runs can both open an issue; the next request
  for that project supersedes both. The new issue is opened before older ones
  are closed, and a retry closes any that a failed run left open.
- `run_url` must be exactly a run URL of the source repository; any other value
  is rejected, because it is rendered as a link in the issue.
- A re-run of an old source build is treated as newest. Whoever allows the
  request must check that no newer `image-bump` issue exists.
- The issue is opened with `GITHUB_TOKEN`. That does not start other workflows
  in this repository; GitHub App webhooks such as a Claude GitHub trigger still
  receive the event.
- The issue trigger fires for every new issue in this repository, including
  ones unrelated to images. The skill exits at its first step unless the issue
  is an `image-bump` request from `github-actions[bot]`, but each such run
  still starts a session and counts against usage. The repository is public,
  so anyone who can open an issue can start such a run.
- The decision session takes the request only from the issue's JSON marker,
  checks it against the allowlist again, and re-validates the images itself.
