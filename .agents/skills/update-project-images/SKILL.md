---
name: update-project-images
description: Use when updating or pinning a hosted project's container images from GHCR package links, GitHub Actions run links, commit links, or image digests, especially when the user writes `proj-<name> frontend image: ...` or `backend image: ...`.
---

# Update Project Images

Turn image links into immutable, validated digest pins and land them through one PR per project.

## Prerequisites

- `origin/main` already contains this skill and the `PROJECT_IMAGE_PINS` block in `docs/platform-architecture.html`. If the block is missing, stop and merge the setup PR first.
- `skopeo`, `gh` (authenticated with `repo` access), `git`, and Python 3 with PyYAML.
- `kubectl kustomize` and `flux` for `render-validate.sh`. If `kubeconform` is absent, the script skips it; report that as skipped and rely on CI.

## Inputs

Accept one or more lines such as:

```text
proj-pn frontend image: <link>
proj-pn backend image: <link>
```

A role is required per image. If a project or role is missing, ask before editing.

Supported links: GHCR package/version links with `?tag=`; GitHub Actions run or job links; GitHub commit links; `ghcr.io/<owner>/<repo>:<tag>`; `ghcr.io/<owner>/<repo>@sha256:<64-hex>`; bare `sha256:<64-hex>`.

Do not guess a tag from a package page without `?tag=`; ask for the version or digest link.

## Workflow

### 1. Isolate

The main working tree may belong to another session. Run this from the repository root, fetch, and create a worktree from `origin/main`:

```bash
git fetch origin main
STAMP=$(date +%Y%m%d-%H%M%S)
git worktree add .worktrees/images-<project>-$STAMP \
  -b chore/images-<project>-$STAMP origin/main
```

Run every later command in that worktree. Never edit the main working tree.

### 2. Dry-run the resolver

```bash
python3 .agents/skills/update-project-images/scripts/update_project_images.py \
  --root . --project <project> \
  --image <role>=<link> [--image <role>=<link> ...] \
  --dry-run --summary-file /tmp/opencode/images-<project>.json
```

Check the resolved role, deployment, tag, digest, `resolution_warnings`, validation `warnings`, `noop` flags, and `html_warnings`. `changed: true` means at least one pin differs; `html_changed: true` in dry-run means the handbook would change. No files are written.

Stop and ask if a role is ambiguous, a repository does not match the container's current image, or a run has no job for that role. Use `--allow-unmatched-job` only after confirming the run published the image.

### 3. Apply

Re-run the same command without `--dry-run` and with the same `--summary-file`. The helper:

- replaces only the matching `image:` line;
- writes `repo@sha256:<64-hex>`;
- regenerates the `PROJECT_IMAGE_PINS` table and updates existing digest references and topology nodes in `docs/platform-architecture.html`.

If every requested image is already pinned (`changed: false`), report the no-op and do not open a PR.

### 4. Understand the image validation

The helper validates without downloading layers:

- digest exists and is an OCI index or manifest list;
- `linux/amd64` and `linux/arm64` are both present and reachable;
- each platform config has `os: linux`, a non-empty rootfs, and an `Entrypoint` or `Cmd`;
- declared `ExposedPorts` match the Deployment's `containerPort`;
- `evidence` records the image user, source URL, revision, entrypoint, and exposed ports;
- secret-like env names are reported with values redacted;
- source/revision labels are checked, and a revision mismatch blocks.

Blocking failures stop the update. Missing config blobs are recorded as skipped and manifest-level validation continues. `--strict` makes every warning block. `--pull-check` is optional, local, and non-blocking.

### 5. Validate the repository

```bash
python3 -m unittest discover tests/preflight
./scripts/render-validate.sh local-k3s
./scripts/render-validate.sh vps-k3s
python3 scripts/check-arch-doc-sync.py --worktree
```

Run the skills tests only when the helper changed:

```bash
python3 -m unittest discover -s tests/skills
```

Check `git status --short` and `git diff --stat`: only the workload files listed in the summary JSON and `docs/platform-architecture.html` may change. If unrelated files changed, stop and investigate.

### 6. Commit and PR

Stage each `path` from the summary JSON explicitly, plus the handbook. Do not stage whole directories.

```bash
# Use the exact `path` values from the summary JSON.
git add deploy/projects/pn/workloads/frontend-deployment.yaml \
        deploy/projects/pn/workloads/backend-deployment.yaml \
        docs/platform-architecture.html
git diff --cached --name-only
git commit -m "chore(<project>): update <roles> image pin(s)"
git push -u origin <branch>
gh pr create --base main --head <branch> \
  --title "chore(<project>): update <roles> image pin(s)" \
  --body-file /tmp/opencode/pr-<project>.md
gh pr checks <pr-url> --watch
```

The PR body must include:

- a table of project, role, old digest, new digest, tag, and source link;
- the image validation results: passed, warned, skipped;
- the positive `evidence` values (user, source, revision, entrypoint, exposed ports);
- the exact repository checks that actually ran.

Mark a check as passed only after observing it pass. Report skipped checks as skipped.

After the PR is green, remove the worktree and report the PR URL:

```bash
git worktree remove .worktrees/images-<project>-$STAMP
```

If checks fail, keep the worktree, fix the failure, and push again.

Stop at a green PR. Do not merge, do not push to `main`, and do not rewrite historical digests in `SPEC.md` or `specs/`.

## Multiple projects

One PR per project. Repeat the worktree, validation, and PR flow for each project; never combine projects in one branch.

## Red flags

- Editing the main worktree because it is already open.
- Writing a tag instead of `@sha256:`.
- Pinning an image whose manifest or digest cannot be read.
- Claiming a config check passed when the blob was unavailable.
- Using `--allow-unmatched-job` without confirming the run published the image.
- Force-pushing, merging, or pushing to `main`.
- Updating platform images under `deploy/platform/`; this skill handles hosted project images only.
