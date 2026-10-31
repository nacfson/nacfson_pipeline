# Image Pin Approval: Discord Notice, GitHub Approval, Pin PR — Design

Date: 2026-10-31 · Size: feature (one new workflow, one new script, small sender jobs in three project repos, removal of an unused path) · Status: decisions approved 2026-10-31; reviewed 2026-10-31; not built

Marking: **(operator)** = decided by the operator. **(my call)** = my proposal, can be changed.

## 1. Intent

Project builds publish new images often: 15 successful `main` publishes in the 7 days before this design (PN 8, health 4, blog 3). Each pin is currently updated by pasting image links into the `update-project-images` skill.

After a build, the operator should get a Discord message with the new images and what changed, decide Approve or Reject from their phone, and on Approve get a pin PR without pasting anything. **(operator)**

## 2. Goals

- A build on `main` leads to one request, per project and commit, with no manual step. **(operator)**
- The request reaches Discord and links to a GitHub approval page. **(operator)**
- The YAML is never changed without the operator's approval. Approve opens a PR. The operator merges it. **(operator)**
- Flux applies the merged pin, with no change to Flux or the cluster. **(operator)**
- The message shows old and new digests and links to the commit and the compare view. **(operator: know what changed)**
- Pin PRs skip normal CI. A strict diff check replaces it. **(operator)**
- Each image is fully validated once, and the same image is never requested twice. **(operator)**

## 3. Non-goals

- Anything running in the cluster for this flow: no pod, watcher, Flux image controller, or new credential. **(operator)**
- Allow and Reject buttons inside Discord, which would need a service to receive the click. **(operator)** This can be added later, and only the approval step would change.
- Storing this flow's secrets in OpenBao. **(operator)**
- Auto-merge.
- A deploy report. After merging, the operator checks the site. **(operator)**
- Required status checks on the `main` ruleset. **(operator)** Any PR can still merge while CI is red, as it can today.
- AI involvement in the decision, including OpenBot. **(my call)** An optional read-only change summary can be added later, and nothing may wait for it.
- Automatic rollback. Reverting is a normal PR or a new request for an older commit.

## 4. Constraints

- `main` rejects direct pushes (`main_PR` ruleset, no bypass actors), so a pin must land through a PR.
- Required reviewers need a public repository on Free, Pro and Team plans. `Project_PN` and `nacfson_health` are private, so the approval gate must live in `nacfson_pipeline`.
- `nacfson_pipeline` is public, so run pages, logs and PR bodies are public. Log only project names, commit SHAs and digests. Print no commit messages from private repositories, and no image configuration from private images — the helper's warnings already print env variable names, labels and ports, and its evidence dict (user, source, revision, entrypoint) must not be copied into a public PR body for `project-pn-*`.
- A plain Discord webhook can send link buttons with `with_components=true`, and can't send interactive buttons.
- Workflow inputs are untrusted. They reach scripts through `env:` only, never through `${{ inputs.* }}` inside a `run:` block.
- Changes to `.github/workflows/` need the code owner's sign-off.

## 5. Architecture

```text
project build succeeds on main
  └─ notify-pipeline job: gh workflow run image-pin.yaml (project, sha, roles, run_url)
        ▼
nacfson_pipeline: .github/workflows/image-pin.yaml
  validate job   allowlist → duplicate check → helper dry-run → full image validation
                   invalid         → red run, no message
                   already pinned  → ends quietly
                   duplicate       → ends quietly
                   otherwise       → Discord message with [Review on GitHub]
        ▼
  pin job        environment: image-pin (waits for the operator's review)
                   Reject → job never runs
                   Approve → newer-request check → apply exact digests
                           → diff check → handbook sync → push branch → open PR
        ▼
  report job     runs always, edits the Discord message with the outcome
        ▼
operator merges the PR → gitops, lint, secret-scan run on main → Flux applies it
```

| Component | Lives in | Holds | Purpose |
|---|---|---|---|
| `notify-pipeline` job | each project's publish workflow | `PIPELINE_RUN_TOKEN` | Starts one request after a successful publish |
| `image-pin.yaml` | `nacfson_pipeline` | `DISCORD_WEBHOOK_URL` | The three jobs above |
| `scripts/image-pin.py` | `nacfson_pipeline` | nothing | Validation order, duplicate and newer checks, diff check, message text |
| `update_project_images.py` | `nacfson_pipeline`, existing, at `.agents/skills/update-project-images/scripts/update_project_images.py` | nothing | Finds the YAML, validates images, writes the pin and handbook |
| `image-pin` environment | `nacfson_pipeline` settings | nothing | Approval gate |

## 6. Project sender

Each publish workflow gains one final job, `notify-pipeline`. **(my call: the job only runs on `main`)**

- Runs only when `github.ref == 'refs/heads/main'`, the event isn't `pull_request`, and at least one publish job succeeded.
- `continue-on-error: true`. A failed or expired token warns and can never fail a release. **(my call)**
- With no `PIPELINE_RUN_TOKEN` set, it prints a warning and exits successfully.
- Sends only the roles that actually published in this run.

```bash
gh workflow run image-pin.yaml -R nacfson/nacfson_pipeline --ref main \
  -f project=<slug> -f sha="$GITHUB_SHA" -f roles=<comma-list> -f run_url="<this run>"
```

| Repo | Project | Roles | Project-specific notes |
|---|---|---|---|
| `nacfson_blog` | `blog` | `web` | Also publishes from `master`; the sender must skip it |
| `Project_PN` | `pn` | `backend`, `frontend` | The `target` input can publish one role, so roles come from job results |
| `nacfson_health` | `health` | `backend`, `frontend` | The publish matrix covers both roles |

## 7. `image-pin.yaml`

Triggered only by `workflow_dispatch`, so the operator can also start a request by hand. `run-name` carries the project, roles and commit for readability. There is no `concurrency` group, because one would cancel waiting requests.

**Request identity is not derived from `run-name`.** Duplicate and newer-request decisions (§7.1 step 2, §7.2 step 2) must be made from state the receiver validated itself, never from a name the requester authored. The old `image-bump-request.py` got this right with a machine-readable marker in an issue body (`MARKER_RE`, L40, and `plan()` at L176-197); moving to run-name string parsing would lose it. The replacement: each `validate` run writes a small marker (project, sha, roles, conclusion) as a **job artifact or a comment on its own run**, and later runs compare against that marker only when the referenced run's `validate` concluded `success`. That makes a red or forged run unable to suppress anything.

Two timing facts shape the rules below, and the original design got both wrong:
- A run is `queued` from dispatch until its `validate` finishes and `pin` reaches the environment gate; only then is it `waiting`. Two dispatches seconds apart both see no `waiting` run, so a pure "waiting" filter cannot prevent the duplicate §13 promises to prevent.
- A run leaves `waiting` the moment it is **approved** and enters `in_progress`. So "is a newer *waiting* run covering my roles?" answers "no" for a newer request the operator already approved — the older run then proceeds, which is exactly the outcome §13 says cannot happen.

### 7.1 `validate` job

Permissions: `contents: read`, `actions: read`, `packages: read`, `pull-requests: read`. Secret: `DISCORD_WEBHOOK_URL`.

1. **Check the request.** `project`, `roles` and `run_url` must match `config/image-pin-sources.json`, and `sha` must be 40 hex characters. These rules move unchanged from `scripts/image-bump-request.py`. Note that there `run_url` is **optional** — it is only format-checked when present (`if run_url and not re.fullmatch(...)`, L118) and pinned to `https://github.com/<source_repo>/actions/runs/<digits>(/attempts/<digits>)?`. A hand-run has no such URL, so `run_url` must stay optional here too, and the message must simply omit the build link. Requiring it would make the step-1 hand test impossible without a fabricated value.
2. **Skip duplicates.** Stop quietly if an earlier request for this project, commit and these roles is still live — meaning its marker exists, its `validate` concluded `success`, and its run is `queued`, `waiting` or `in_progress` — or if an open PR exists from `chore/images-<project>-<sha7>`. Because this runs before `pin` is queued, this run cannot yet see itself; the overlap case is handled by the marker write being idempotent for one commit, not by the status filter.
3. **Validate the images.** Log in to GHCR with `GITHUB_TOKEN`, then dry-run the helper with one commit link per role. This is the one full validation: tag lookup by commit, `linux/amd64` and `linux/arm64`, rootfs and entrypoint, exposed ports, source and revision labels. **Toolchain:** every one of those checks shells out to `skopeo`, which the helper calls directly (no fallback); it is preinstalled on `ubuntu-latest` (1.13.3 on 24.04), but pinning it explicitly is safer than relying on the runner image. The job also needs PyYAML (`scripts/requirements-preflight.txt`) and the helper at `.agents/skills/update-project-images/scripts/update_project_images.py`, whose path the spec must state because §7.2 step 4 edits its `argparse`.
4. **Stop if nothing changes.** If every role's image summary has `noop: true`, end quietly. The helper has no per-role `changed` field; `changed` is a whole-run boolean and `noop` is the per-role one.
5. **Record the result.** Job outputs carry each role's exact `repo@sha256:<64-hex>` and old digest.
6. **Post to Discord** (§8). A Discord failure warns and doesn't fail the run, because the approval page still works. When `DISCORD_WEBHOOK_URL` is unset, this step warns and skips — the same fail-open rule as §6's missing `PIPELINE_RUN_TOKEN` — so step 1 can run before step 2 creates the channel.

### 7.2 `pin` job

`environment: image-pin`. Permissions: `contents: write`, `pull-requests: write`, `actions: read`, `packages: read`. It holds no secret of its own, and because it runs only after approval, it can't write anything before then.

1. **Check out current `main`**, not the commit from when the request was made.
2. **Stop if replaced.** If a newer request for this project covers all these roles and is still live, set outcome `replaced` and stop without opening a PR. "Live" is `queued`, `waiting`, **or `in_progress`** — the last one matters because an approved newer run has already left `waiting`. Compare by commit timestamp, not run ID.
3. **Reuse an existing PR.** If an open PR already exists from this branch, set outcome `pr_exists`.
4. **Apply the approved digests.** Run the helper with `--image <role>=<repo>@sha256:<digest>` and a new `--skip-validation` flag. The digest path still confirms the digest exists in GHCR, so an image deleted while the request waited stops the run. **(my call: this existence check isn't a second validation)** Two caveats the implementer must handle: the `<repo>@sha256:` form is not free-text — it is repo-equality checked against the container's current repo and rejected on mismatch, so it can only ever re-pin the same repository and intentionally cannot express a repo move (§9 catches that as a violation rather than performing it). And a bare `sha256:<hex>` would be silently reinterpreted as the *current* repo, so pass the full `<repo>@<digest>` form and assert the applied `old_repo` matches what `validate` recorded.
5. **Stop if already pinned.** If `main` already has these digests, set outcome `already_pinned`.
6. **Run the short checks:** the diff check (§9), then `check-arch-doc-sync.py --worktree`. Optionally also `platform-preflight`, if we accept the extra runtime: Principle I says a revision reaches a cluster "only after a revision has passed that environment's required checks, including `platform-preflight`", and a pin PR becomes the deployment source on merge. A digest-only change cannot move a budget, so the check would pass, and its absence is disclosed rather than silently assumed. **(operator decision still open)**
7. **Commit, push and open the PR** with `GITHUB_TOKEN`, as `github-actions[bot]`, on `chore/images-<project>-<sha7>`. The commit message is `chore(proj-<project>): update <roles> image pin(s)`.

The PR description holds:
- a table of role, old digest, new digest, tag and source link
- the validation results `validate` recorded — passed to `pin` as job outputs, because `pin` itself runs with `--skip-validation` and therefore has no evidence of its own. For `project-pn-*`, publish only the pass/fail summary and the checks that ran, never the raw `evidence` dict (user, source, revision, entrypoint), per §4
- the short checks that ran
- a statement that normal CI didn't run on this PR, and a link to the run

PRs opened with `GITHUB_TOKEN` start no workflows, and that is how pin PRs skip CI. No path filter or CI change is needed. **(operator)**

### 7.3 `report` job

`needs: [validate, pin]`, `if: always()` and only when `validate` posted a message. Permissions: `actions: read`. Secret: `DISCORD_WEBHOOK_URL`.

It edits the Discord message. Because a rejected job and a failed job can look alike, it reads `GET /actions/runs/{id}/approvals` to tell them apart.

| Situation | Message title | Details |
|---|---|---|
| PR opened, or already open | PR open | PR link |
| Reject | Rejected | Nothing changed |
| Newer request exists | Replaced by a newer build | Use the newest message |
| `main` already has the digests | Already pinned | Nothing to do |
| Pin job failed | Failed | Failing step and run link |
| Cancelled or past GitHub's 30-day limit | Expired or cancelled | A new build or manual run asks again |

## 8. Discord message

Posted with `?wait=true&with_components=true`, keeping the returned message ID as a job output for the edit in §7.3.

```text
New images ready: proj-pn
Backend    22f44415 → 7c1e90ab
Frontend   5838896b → b04d21f6
Checks     amd64 + arm64, ports, labels (plus any warnings)
Changes    Commit · Compare old…new
[Review on GitHub ↗]
```

- **The button's URL is the run page:** `https://github.com/nacfson/nacfson_pipeline/actions/runs/<run_id>`. This is the one place the approval can be given, and the spec must build it explicitly — the only URL otherwise present is `run_url`, which is the *source project's* build run and cannot approve anything. **(my call)**
- **Phone approval is the biggest unknown, and it has two paths.** Pending deployments are approvable from the GitHub mobile app's **Notifications** tab and its push notifications (the app has supported deployment review since 2021). What the app does *not* do reliably is show approve/reject on a workflow-run page opened from a link — the run page in the app can show only "Cancel workflow". So the Discord link alone is not a dependable phone path. Plan for the operator to approve from the notification, and keep the link as the fallback that opens the run page in a phone browser, where "Review deployments" is present. **(operator: this is the step-1 test)**
- `allowed_mentions: {"parse": []}`, so no message can ping anyone. **(my call)**
- `#image-bumps` is a new private channel, set to notify on all messages. Phone alerts come from this channel setting, so no user ID is stored. **(my call)**
- The compare link uses the old and new revision labels. Without an old revision, only the commit link is shown.
- Commit titles aren't shown, so that no private-repo text passes through this public workflow. **(my call)**

## 9. Diff check

This is what makes skipping CI safe. Any failure stops the run and no PR opens. **(operator)**

- **Files:** the changed set must equal exactly the `path` values of roles the helper actually wrote, plus `docs/platform-architecture.html`. The helper only writes roles whose image changed (`html_updates` filter), so a multi-role request where one role is already pinned yields fewer paths than roles. The checker must derive the expected set from the run's own writes, not from every requested role — otherwise a legitimately partial change fails. The handbook may legitimately be unchanged too (when no pins-block row and no short `Pinned @` token matches), so it is allowed but not required.
- **Project scope:** every YAML path must be under `deploy/projects/<project>/workloads/`.
- **YAML:** each file has exactly one removed and one added line. Both must match `image: <repo>@sha256:<64-hex>`, with the same repository and indentation, and the new digest must be the one approved, on the same container the role resolved to at validation time.
- **Handbook:** compare `main` with the new version token by token over `sha256:<hex>` values. Every difference must map an old digest to its approved new digest, in full or short form, and nothing else may differ.
- **Known limitation — the handbook's own prose.** `docs/platform-architecture.html:942` still attributes the pins-table regeneration to the `update-project-images` skill. Once §7.2 does that work, the sentence is wrong, and this rule forbids a pin PR from fixing it (a prose edit would differ from a digest mapping). It must be corrected by hand, in step 4, outside this flow. Until then the handbook carries a stale attribution — accepted, because letting the workflow rewrite prose would let it change anything.
- **Known limitation — short-form tokens.** The helper rewrites short `Pinned @sha256:<8-hex>` tokens, not only full digests. If such a token was hand-edited away from the YAML's full digest, the helper's rewrite will not correspond to an old→new mapping of the token it replaced, and this check fails on the helper's own edit. Only two such tokens exist today, so this is latent; the failure is loud, and the fix is to correct the token by hand.
- **Cross-project rows:** the helper rebuilds the whole pins table from every `deploy/projects/*` directory, so a pin PR can rewrite rows for projects other than the requested one. When those rows were already current nothing changes and the check passes. If a second project's pin landed between validation and approval, this run rewrites that row back and the check fails loudly. That is intentional, and the fix is to rebase and re-request. **(my call)**

If the handbook's pin table is already stale, regenerating it fails this check. That is intentional: the run fails loudly and the handbook gets fixed by hand. **(my call)**

## 10. Finding the right YAML

Most of this exists already in the helper, so no per-file registration is needed. **(operator: automatic)**

| Filter | Decides | Stops when |
|---|---|---|
| Allowlist | Which project and source repo | Project, repo or role isn't allowed |
| `discover_target` | Reads only `deploy/projects/<project>/workloads/` and scores each container: exact container name, then deployment name, then a `-<role>` suffix on the deployment or repository | No match, or a tie at the top score |
| `replace_image` | The `image:` line holding the current digest | Not found exactly once |
| Diff check (§9, new) | Nothing else changed | Anything else differs |

Adding a project needs its `deploy/projects/<slug>/workloads/` folder, a line in `config/image-pin-sources.json`, a sender job, and a container or deployment name that contains its role. It does **not** need the container renamed to the role: `blog` has role `web` and container `blog-web`, and it resolves at score 80 through the deployment-suffix rule. All five allowlisted roles resolve today.

## 11. Secrets and settings

| Item | Where | Scope | If leaked |
|---|---|---|---|
| `PIPELINE_RUN_TOKEN` | Actions secret in the 3 project repos | Fine-grained, `nacfson_pipeline` only, `Actions: write`, with an expiry | **Wider than this flow.** `Actions: write` can dispatch *any* workflow in this repository that has `workflow_dispatch`, on *any existing branch*. `.github/workflows/build-images.yaml` has `workflow_dispatch` and `packages: write`, and tags every build `:latest,sha-<sha>`; `deploy/platform/vault-proxies/ai-proxy.yaml` and `registry-proxy.yaml` reference `:latest`. So a holder can republish a built image as `:latest` from any pushed branch. It can't change code on `main`, but it can change what `:latest` resolves to. Fix before step 3: pin those two manifests to digests and drop `:latest` (§20) |
| `DISCORD_WEBHOOK_URL` | Actions secret in `nacfson_pipeline` | One channel | Fake messages in that channel. Approval still happens on GitHub |
| `GITHUB_TOKEN` | Issued per run | Per-job permissions in §7 | Short-lived, and `main` stays protected |

**Constitution.** Rule 2 in `AGENTS.md` covers Git and pods, but Principle V in `.specify/memory/constitution.md` is the binding text and is broader: it says backend credentials — naming "external API credentials" — "MUST be stored, managed and executed exclusively within a self-hosted internal cluster Vault", and its one carve-out closes with "This exception covers no other credential." This design therefore **needs a constitution amendment**, not a docs note. The amendment is narrow: GitHub Actions secrets held outside the cluster, for automation that cannot reach Vault, are outside Principle V's scope, and the OpenBao-only habit still governs every credential a workload uses. It is a MINOR bump (§ "Amendment Process"), with the rationale that GitHub's runners cannot reach OpenBao and that reaching it would need a public route into `vault` or a watcher pod. **(operator)** Step 4 lands the amendment; step 1 may proceed while it is drafted, because step 1 stores only the webhook that step 2 adds.

**Environment `image-pin`:**
- Required reviewer: the operator.
- Prevent self-review: **off**. `PIPELINE_RUN_TOKEN` belongs to the operator, so they count as the initiator and couldn't otherwise approve.
- Deployment branches: `main` only.

**Repository setting:** "Allow GitHub Actions to create and approve pull requests" must be turned on. It's currently off. Default workflow permissions stay `read`.

**Registry access:** `project-pn-backend` and `project-pn-frontend` are private, so each package must grant `nacfson_pipeline` read access under **Manage Actions access**. `blog-web`, `health-backend` and `health-frontend` are public.

## 12. Security model

- **Who can approve:** only the required reviewer, verified by GitHub. A fake Discord message can't approve anything, and its link can only lead to a GitHub page.
- **What a token holder can request:** the sender's identity isn't proven, because `workflow_dispatch` inputs are self-declared. Any holder of `PIPELINE_RUN_TOKEN` can name any allowlisted project. They can still only name a commit whose image exists under that project's repository, with matching source and revision labels. Within *this* flow the worst outcome is a request for an older real image, which still needs approval and a merge.
- **What a token holder can do outside this flow:** more than the sentence above suggests. `Actions: write` dispatches any workflow with `workflow_dispatch` on any existing branch, so it also reaches `build-images.yaml` and can republish `:latest` tags (§11). Treat the token as "can disturb CI and `:latest` images in this repository", not "can only ask for older images".
- **Requests can be crowded out, not silently forged:** duplicate and newer-request checks (§7.1 step 2, §7.2 step 2) compare against a marker each `validate` run writes about itself, and only when that run concluded `success` — never against requester-authored `run-name` text. A failed or decoy run therefore cannot suppress a legitimate request. Residual gap: if two requests for the same commit are dispatched within seconds, neither has reached `waiting` yet, so both can pass §7.1 step 2 (see §13). They still gain no write access — nothing merges without the operator — and the manual `update-project-images` skill remains the fallback.
- **What a pin PR can change:** only image digests and their handbook entries (§9).
- **Final check:** the operator merges, and normal CI doesn't run first. The PR shows the old and new digests, and `gitops`, `lint` and `secret-scan` run on `main` after merge without blocking Flux.

## 13. Edge cases

| Case | Behaviour |
|---|---|
| Newer build while waiting | Two messages exist. Approving the older one stops at §7.2 step 2 if the newer one covers its roles — and this holds even after the newer one is approved, because `in_progress` counts as live |
| Frontend-only build while a backend request waits | Both stay valid, and each opens its own PR. A later one must apply cleanly to current `main` |
| Same commit dispatched twice within seconds | Both may pass §7.1 step 2, because neither has reached `waiting` yet. Dedupe relies on the per-commit marker being idempotent; if two PRs do open, the second is closed by hand |
| Re-run of an old build | Treated as a new request. The PR shows the older digest, and the operator decides |
| Two approvals landing together | Each uses its own branch, and both regenerate the pins table for every project, so the second usually conflicts on `docs/platform-architecture.html`. The operator closes it and re-requests |
| Image deleted while waiting | §7.2 step 4 fails, and the message says Failed |
| Discord down at request time | Warning only. The run still waits on the Actions page |
| Token expired | The sender warns and the build still succeeds. No request appears until rotation |
| Nobody answers | The run is cancelled at GitHub's 30-day limit, and production stays on the current pin |
| Workflow itself broken | The manual `update-project-images` skill remains as the fallback |

## 14. CI changes

No required checks, no `ci.yaml`, and no `ci-required`. **(operator)** `gitops`, `lint` and `secret-scan` keep their triggers.

`review-gate.yaml` is deleted. **(operator)** It is the only CI that runs three checks, so they move into the `preflight-tests` job in `gitops.yaml`: **(my call)**

| Step | Decision | Reason |
|---|---|---|
| `check-arch-doc-sync.py --worktree` | Move, **but change the mode** | Rule 8 requires it, and without CI only local hooks enforce it. As written this step would be a no-op in CI: `--worktree` runs `git status --porcelain`, which is always clean on a fresh checkout, and `check_git_co_update` returns no errors when that list is empty. CI must use a diff range against the base (`--staged`-equivalent against `origin/main`), not `--worktree` |
| `tests/review_cycle` | Move | `review-cycle.py` stays for the `code-review` skill |
| `review-cycle.py eval` | Move | Confirms the same tool still flags bad changes |
| `classify-risk`, `circuit-breaker --json` | Remove | Report-only output |
| Nightly 03:00 UTC schedule | Remove | This is the one real loss: no re-run when no PR is open |

- That job's checkout needs `fetch-depth: 0`, as `review-gate.yaml` used. `gitops.yaml`'s `preflight-tests` checkout has no `with:` block today, so it is depth 1.
- `tests/review_cycle/test_ci_and_evals.py` currently requires `review-gate.yaml` and its schedule, and asserts `check-arch-doc-sync.py --worktree` in it (L31-46). It must assert the moved steps in `gitops.yaml` instead, and assert the *new* mode, so the no-op can't come back. Deleting the workflow is not enough on its own: the `merge_group` loop at L31-33 iterates `("lint.yaml", "gitops.yaml", "review-gate.yaml")` and `read_text` raises `FileNotFoundError` on the deleted file, and L37 reads it by name again. Drop `review-gate.yaml` from that loop and remove the L36-44 schedule/steps assertions, or `python3 -m unittest discover tests/review_cycle` stays red on every PR, inside the same job.
- `lint.yaml` and `gitops.yaml` keep their `merge_group` triggers.

## 15. Review rule

`AGENTS.md` §5 gains this exemption: **(operator)**

> Commits made by `image-pin.yaml` are exempt from the `code-review` skill, because a workflow can't run it. The exemption covers only commits that passed that workflow's diff check after the owner's approval. Every other commit follows the normal rule, including changes to `image-pin.yaml` and `scripts/image-pin.py`.

Prose alone would not hold: `.githooks/pre-commit` runs `review-cycle.py check-gate --scope staged --require-pipeline code-review` for every commit, and `review-cycle.py` accepts no actor or path predicate. The exemption only means something if the gate learns to recognise these commits, so step 4 also adds a narrow, positive test to that gate: a commit may pass without a receipt **only** when its author is `github-actions[bot]`, every changed path is an `image:` line under `deploy/projects/*/workloads/` or the pins block of `docs/platform-architecture.html`, and it records the Actions run ID it came from. Anything else — including any commit touching `image-pin.yaml` or `scripts/image-pin.py` — still needs a receipt. **(my call)**

## 16. Removed

Removed entirely, in step 4. **(operator)** None of this has ever run: no `image-bump` label, issue or dispatch run exists.

| Path | Action |
|---|---|
| `.agents/skills/image-bump-decision/` | Delete |
| `scripts/image-bump-gate.py`, `tests/hooks/test_image_bump_gate.py` | Delete |
| PreToolUse `image-bump-gate.py` entry in `.claude/settings.json` | Delete |
| `scripts/image-bump-request.py`, `tests/skills/test_image_bump_request.py` | Delete, keeping the validation rules in `scripts/image-pin.py` |
| `docs/image-bump-dispatch.md` | Replace with `docs/image-pin.md` |
| `config/image-bump-sources.json` | Rename to `config/image-pin-sources.json`, dropping `label`. **Done in step 1, not here** — §7.1 step 1 reads it **(my call)** |
| `AGENTS.md` routing line, `INDEX.md` entry | Point to the new flow, and add `INDEX.md` rows for everything this design creates: `scripts/image-pin.py`, `tests/skills/test_image_pin.py`, `docs/image-pin.md`, and `.github/workflows/image-pin.yaml`. `INDEX.md` catalogues scripts and test suites individually (L106-123), so a single repointed row is not enough |
| `docs/platform-architecture.html` | Update the pins-block prose at L942: it still says the `update-project-images` skill regenerates the table, which stops being true once §7.2 does it. §9 forbids any non-digest change in a pin PR, so this must be fixed by hand in step 4, not by the workflow |
| `.github/workflows/review-gate.yaml` | Delete (§14) |

## 17. Testing

**Unit tests**, in `tests/skills/test_image_pin.py`, run by `gitops.yaml`:
- Request checks: wrong repo, bad SHA, unknown role, bad `run_url`, and an **absent** `run_url` (a hand-run must pass).
- Duplicate and newer checks, from fixture run lists, including partial role coverage, a rejected run that must not suppress a new request, a newer run already approved (`in_progress`) that must still report `replaced`, and two dispatches seconds apart that are both `queued`.
- Diff check accepts a real pin diff, and rejects: a tag instead of a digest, a changed repository, an extra YAML line, an extra file, a path in another project, an unapproved digest, and an unrelated handbook change.
- Diff check accepts a **partial** change: a two-role request where one role is already pinned writes one YAML path; and accepts an unchanged handbook.
- Message rendering: `allowed_mentions` is empty, each outcome in §7.3 maps to the right text, and the review button URL is the `nacfson_pipeline` run URL.
- `--skip-validation`: skips `validate_image`, still confirms the digest exists, and is off by default.
- Unset `DISCORD_WEBHOOK_URL`: `validate` warns, still reaches the approval gate, and does not fail.

**Live acceptance**, run by hand for `blog` before any sender is connected:

| Test | Expected |
|---|---|
| Approve from a phone | PR opens, no normal CI runs, description is correct |
| Approve from the phone's GitHub app Notifications tab | Approve/reject is reachable without the Discord link |
| Reject | No branch or PR, message says Rejected |
| Same commit again | No second message |
| Commit with no image | Red run, no message |
| Older request approved while a newer one waits | No PR, message says Replaced |
| Merge the PR | `gitops`, `lint`, `secret-scan` run on `main`, and Flux applies the pin |

## 18. Build order

Each step lands through its own PR.

1. **Workflow and approval:** `image-pin.yaml`, `scripts/image-pin.py`, the diff check, the helper flag, the environment and the repository setting. Includes `config/image-pin-sources.json` — the rename in §16 must happen here, not in step 4, because §7.1 step 1 reads that file. Run it by hand for `blog`, and try approving from a phone.
2. **Discord:** the channel, the webhook, the message and the `report` job.
3. **Projects:** `blog`, then `Project_PN` with its package access, then `nacfson_health`. Each repo is a separate PR.
4. **Cleanup:** §15, §16 and §14 together, then `docs/image-pin.md`.

**Decision point after step 1:** if phone approval is too awkward, add Discord buttons with a serverless endpoint. Only the approval step changes. Note what step 1 can and cannot tell us: with no Discord yet, it tests *approving from a phone* (via the GitHub app's Notifications tab and the run page in a phone browser), not the Discord-to-phone hop. That hop is only tested once step 2 lands. If the goal is to de-risk the full phone path, consider swapping steps 1 and 2, or accept that step 2 carries the remaining unknown.

**Operator setup** (needs the operator's accounts):
- Create `#image-bumps` and its webhook.
- Create `PIPELINE_RUN_TOKEN` and add it to the 3 project repos.
- Grant package access for `project-pn-*`.

**Prerequisites found in review, not in the original design:**
- A constitution amendment for GitHub-held automation secrets (§11).
- Pin `deploy/platform/vault-proxies/ai-proxy.yaml` and `registry-proxy.yaml` to digests, so the token's `Actions: write` can't repoint `:latest` (§11, §20).
- Give `check-arch-doc-sync.py` a real diff mode for CI; `--worktree` sees an empty diff on a fresh checkout (§14).

## 19. Not yet verified

Documented behaviour, not yet run on this repository:

- Approving a pending deployment **from a phone**. Partly checked: GitHub mobile supports deployment review from its Notifications tab and push notifications (changelog, 2021), but opening a workflow-run page from a *link* in the app may show no approve button, so the in-app path depends on notifications rather than the Discord link. Not verified on this repository.
- The link button rendering through a plain webhook.
- `GITHUB_TOKEN` opening a PR, and that PR starting no workflows.
- `GITHUB_TOKEN` reading PN's private packages after the access grant.
- Reading another run's marker and status from inside a run. The runs API exposes `status=waiting` for runs held at an environment gate and `in_progress` once approved, and `GET /actions/runs/{id}/approvals` returns `pending`/`approved`/`rejected`, so both §7.3's reject-vs-fail distinction and §7.2 step 2's "still live" test are possible. `run-name` is deliberately not used for these decisions, because it is requester-authored.
- The `report` job running after a rejection and after the 30-day cancellation. If it doesn't run after cancellation, that message stays unchanged. **(accepted risk)**

## 20. Rejected

- **Discord buttons handled by an in-cluster pod.** New pod and credentials, hand-written WebSocket code under the stdlib-only rule, and an approval path that stops when the cluster does.
- **Discord buttons handled by a serverless endpoint.** Adds a platform for one less tap. This is the fallback if phone approval disappoints.
- **Emoji reaction polling.** Needs the same always-running service.
- **Approval through OpenBot or a Claude session.** An AI can't provide a hard stop, and OpenBot's README describes agents running with `danger-full-access` and `approvalPolicy: never`. PR #98 tried to restrict an AI session for this job and kept finding bypasses.
- **Flux image controllers.** Two more controllers, Kubernetes Secrets that conflict with rule 2, and the cluster watching for images, which isn't needed.
- **Secrets in OpenBao.** Needs a public route into `vault` or a watcher pod, which adds more risk than two low-impact GitHub secrets.
- **Scoping the token to a single workflow.** Fine-grained PATs grant `Actions: write` per repository, not per workflow, so `PIPELINE_RUN_TOKEN` cannot be narrowed to `image-pin.yaml` alone. The alternative considered and deferred: pin the two `:latest` vault-proxy manifests to digests so a dispatcher cannot change what runs. **(prerequisite for step 3)**
- **A dedicated token so CI runs on pin PRs.** A third secret to run checks that a digest-only diff can't fail. **(operator)**
- **Required status checks with a shared `ci-required`.** Works, but adds `ci.yaml`, edits all three workflows, and is the riskiest untested part. It can be added later. **(operator)**
- **`repository_dispatch`.** Needs `Contents: write`, which lets a token holder push unprotected branches here. `workflow_dispatch` needs only `Actions: write`.

## 21. Out-of-scope observations

- While checking this design, `vault-notify-proxy` still had an all-zero placeholder digest. PR #106 has since pinned a real one. This flow doesn't depend on it.
