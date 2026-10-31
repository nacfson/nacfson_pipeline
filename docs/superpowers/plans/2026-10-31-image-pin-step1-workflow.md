# Step 1: Image Pin Workflow and Approval Gate — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A project build on `main` reaches a GitHub Environment approval gate in `nacfson_pipeline`. The operator approves from a phone. On approval, a PR opens pinning exact `@sha256` digests, with no manual step and no YAML change before that approval.

**Architecture:** One new workflow `.github/workflows/image-pin.yaml` with three jobs. `validate` checks the request, skips duplicates, validates the images once, and posts a Discord notice. `pin` sits behind `environment: image-pin`; only after the operator approves does it apply the digests, run the admission gate, and open a PR via `GITHUB_TOKEN`. `report` edits the Discord message with the outcome. All decision logic lives in one new script `scripts/image-pin.py`; image resolution and YAML rewriting stay in the existing helper, which gains `--skip-validation`. **No sender job, no Discord channel, no `PIPELINE_RUN_TOKEN` in this step** — it is driven by hand for `blog`.

**Tech Stack:** GitHub Actions (`workflow_dispatch`, environments, job outputs, artifacts/run markers), Python 3.12 `unittest`, `gh` CLI 2.x, `skopeo` (preinstalled on `ubuntu-latest`), the existing `update_project_images.py` helper, Kustomize/Flux untouched.

**Spec:** `docs/superpowers/specs/2026-10-31-image-pin-approval-design.md` §4–§9, §11–§13, §16–§19.

## Global Constraints

- **Nothing merges on its own.** `pin` only ever opens a PR. The operator merges. `main` rejects direct pushes.
- **No workflow inputs in `run:` blocks.** Untrusted values reach scripts through `env:` only, never `${{ inputs.* }}` inline. This is a spec constraint (§4) and a shell-injection guard.
- **Pin PRs must start no CI.** PRs opened with `GITHUB_TOKEN` do this natively. Do not add `pull_request` triggers to `image-pin.yaml`, and do not add a token so CI can run.
- **Only these files may change in a pin PR:** the `image:` line under `deploy/projects/<project>/workloads/` (exactly one removed, one added, same repo, same indentation) and digest tokens in `docs/platform-architecture.html`.
- **`platform-preflight` runs on every pin.** `render-validate.sh` does **not** invoke it — it renders, runs kubeconform and the invariants, and prints the candidate path. Call `platform-preflight.py` separately, per environment.
- **Public repo, private images.** `nacfson_pipeline` is public. Log project names, commit SHAs and digests. Never print or publish `evidence` (user, source, revision, entrypoint, ports) for `project-pn-*`.
- **Request identity never comes from `run-name`.** Dedupe and supersede decisions read a marker a run writes about itself, only when that run's `validate` concluded `success`.
- **Architecture handbook sync (Rule 8).** This step adds no watched path: `.github/workflows/` and `docs/superpowers/` are not in `ARCH_WATCH_PREFIXES` (`scripts/check-arch-doc-sync.py:29-42`). Pin PRs touch `deploy/projects/` **and** the handbook together, as they must.
- **Do not touch `gitops.yaml`, `lint.yaml`, `secret-scan.yaml`, or `review-gate.yaml` in this step.** That is step 4.
- Run the `code-review` skill before every commit, and let it set `"pipeline": "code-review"` in the receipt.

## Review Focus

1. **A forged or failed run suppresses a real request.** A decoy `workflow_dispatch` naming `pn/backend,frontend` and a real SHA fails validation but sits in the runs list for 30 days. If dedupe trusts it, the operator never sees a message and never knows why. → markers are written by the run about itself, and read only from a run whose `validate` succeeded; `TestDecoyRunDoesNotSuppress` in Task 2.
2. **The operator approves the newer message, and both pins land.** Approval moves a run from `waiting` to `in_progress`, so a "is a newer *waiting* run?" test answers no and the older run proceeds. → "live" means `queued`, `waiting` **or** `in_progress`; `TestApprovedNewerRequestStillReplaces` in Task 2, and the two-PR collision is accepted and documented (§13) rather than promised away.
3. **`--skip-validation` silently pins the wrong repository.** A bare `sha256:<hex>` is reinterpreted as the container's *current* repo, so a mistyped argument still "succeeds" against the wrong image. → pass `<repo>@<digest>` and assert the applied repo equals what `validate` recorded; `TestDigestPathRejectsRepoMismatch` and `TestSkipValidationStillConfirmsExistence` in Task 9.
4. **The admission gate runs but does not include preflight.** `render-validate.sh` passes while `platform-preflight` was never called. → assert both commands run, per environment; `TestGateRunsPreflightForBothEnvironments` in Task 8.
5. **A hand-run cannot be made, so the phone test never happens.** `run_url` required, or a config file that does not exist yet, blocks the first manual dispatch. → `run_url` optional (as `image-bump-request.py:118` already treats it), and the config rename lands in Task 1; `TestHandRunWithoutRunUrlPasses` in Task 2 and Task 12.

## Interfaces

One script, one flag, one config, one workflow. Everything else is reused.

```text
scripts/image-pin.py                  new   request checks, dedupe, marker I/O, diff check, message
config/image-pin-sources.json         new   renamed from image-bump-sources.json, `label` dropped
tests/skills/test_image_pin.py        new   unit tests for the above
.github/workflows/image-pin.yaml      new   validate / pin / report
update_project_images.py              edit  add --skip-validation (3 call sites)
```

### `scripts/image-pin.py` — public surface

- `Request(project, source_repo, sha, roles, run_url=None)` and `validate_request(request, sources) -> Request` — moved verbatim from `image-bump-request.py` (L48-58 model, L89-125 validator). Raises `RequestError` on a bad project, repo, SHA, role, or `run_url` format. `run_url` stays **optional**.
- `list_live_requests(repo, *, gh=run_gh) -> list[Marker]` — reads `GET /repos/{repo}/actions/runs` and returns the markers of runs that concluded `success` and are `queued`/`waiting`/`in_progress`.
- `Marker(project, sha, roles, run_id, status, created_at)`; `write_marker(..., path)` and `read_marker(path) -> Marker | None` (JSON, same shape as the old issue marker).
- `is_duplicate(request, live) -> bool` — an earlier live marker has the same project and sha, and `request.roles ⊆ marker.roles`.
- `is_replaced(request, live, *, now) -> bool` — a **newer** live marker has the same project and `marker.roles ⊇ request.roles`. Compare `created_at`, not run ID.
- `diff_check(changed: dict[str, str], summary: dict, approved: dict[str, str]) -> list[str]` — returns violations; empty means pass. Implements spec §9.
- `render_message(request, summary) -> dict` — the Discord payload, including the run URL and `allowed_mentions: {"parse": []}`.
- `render_pr_body(request, summary, checks, run_url, *, public_evidence: bool) -> str`.
- CLI: `check-request`, `dedupe`, `newer-check`, `diff-check`, `render-message`, `render-pr-body`, `write-marker`. Each takes `--json` and exits `0`/`2` like the old script, so the workflow stays declarative.

### Helper change (`update_project_images.py`)

- `run_update(..., skip_validation: bool = False)` at L979; the `validate_image` call at L1018-1026 is skipped when set, leaving `validation = None`.
- `main()` gains `--skip-validation` beside `--summary-file` (L1121).
- Digest existence is still confirmed: `resolve_digest_ref` runs on the digest path regardless (L327-341), and the repo-equality guard at L475-481 stays on. `--skip-validation` suppresses only the multi-arch/label/port inspection.

---

## Task 1: Config rename (must land first)

`§7.1` step 1 reads this file, so it has to exist before any manual dispatch.

**Files:**
- Rename: `config/image-bump-sources.json` → `config/image-pin-sources.json`, dropping the top-level `label` key
- Modify: `scripts/image-bump-request.py` (`SOURCES_PATH`, L38), `tests/skills/test_image_bump_request.py` (path references)

**Interfaces:**
- Produces: `config/image-pin-sources.json` with `{"schema_version": "1.0", "projects": {"blog": {"source_repo": "nacfson/nacfson_blog", "roles": ["web"]}, "health": {...}, "pn": {...}}}` — identical project data, no `label`.

- [ ] **Step 1: Copy and edit the config** — `git mv config/image-bump-sources.json config/image-pin-sources.json`, then delete the `"label": "image-bump"` line. Leave `schema_version` and all three projects untouched.
- [ ] **Step 2: Repoint the old script** so the suite stays green until step 4 deletes it — change `SOURCES_PATH` at `scripts/image-bump-request.py:42`, and any literal path in `tests/skills/test_image_bump_request.py`.
- [ ] **Step 3: Run the affected tests** — `python3 -m unittest tests.skills.test_image_bump_request` → PASS.
- [ ] **Step 4: Verify the file parses and matches the old project data**

```bash
python3 -c "
import json
new = json.load(open('config/image-pin-sources.json'))
old = json.load(open('/dev/stdin'))
assert 'label' not in new
assert new['projects'] == old['projects']
print('projects:', sorted(new['projects']))
"
```

- [ ] **Step 5: Commit**

```bash
git add config scripts/image-bump-request.py tests/skills
git commit -m "chore(image-pin): rename sources config for the pin flow"
```

## Task 2: Request checks, markers, dedupe and supersede

**Files:**
- Create: `scripts/image-pin.py` (request model, `Marker`, dedupe/supersede, CLI skeleton)
- Create: `tests/skills/test_image_pin.py`

**Interfaces:**
- Consumes: `config/image-pin-sources.json` (Task 1).
- Produces: `Request`, `validate_request`, `Marker`, `read_marker`, `write_marker`, `is_duplicate`, `is_replaced`, and the `check-request` / `dedupe` / `newer-check` / `write-marker` CLI verbs used by Tasks 8 and 9.

- [ ] **Step 1: Write the failing tests**

```python
def test_hand_run_without_run_url_passes(self):
    req = Request(project="blog", source_repo="", sha="a"*40, roles=["web"], run_url=None)
    self.assertEqual(validate_request(req, SOURCES).roles, ["web"])

def test_bad_run_url_rejected(self):
    with self.assertRaises(RequestError):
        validate_request(Request("blog", "", "a"*40, ["web"],
                                 run_url="https://github.com/nacfson/blog-web/actions/runs/1"), SOURCES)

def test_unknown_role_rejected(self):
    with self.assertRaises(RequestError):
        validate_request(Request("blog", "", "a"*40, ["frontend"]), SOURCES)

def test_decoy_run_does_not_suppress(self):
    """A failed run's marker must never count as live."""
    live = [Marker("blog", "a"*40, ["web"], 1, "completed", "2026-10-31T09:00:00Z")]
    self.assertFalse(is_duplicate(Request("blog", "", "a"*40, ["web"]), live))

def test_queued_run_is_live(self):
    live = [Marker("blog", "a"*40, ["web"], 2, "queued", "2026-10-31T09:00:00Z")]
    self.assertTrue(is_duplicate(Request("blog", "", "a"*40, ["web"]), live))

def test_partial_role_coverage_is_not_a_duplicate(self):
    live = [Marker("pn", "a"*40, ["backend"], 3, "waiting", "2026-10-31T09:00:00Z")]
    self.assertFalse(is_duplicate(Request("pn", "", "a"*40, ["backend", "frontend"]), live))

def test_approved_newer_request_still_replaces(self):
    """in_progress must count as live; approval moves a run out of waiting."""
    live = [Marker("pn", "b"*40, ["backend", "frontend"], 5, "in_progress", "2026-10-31T10:00:00Z")]
    self.assertTrue(is_replaced(Request("pn", "", "a"*40, ["backend"], run_url=None), live, now=NOW))

def test_older_request_does_not_replace(self):
    live = [Marker("pn", "b"*40, ["backend"], 4, "waiting", "2026-10-31T08:00:00Z")]
    self.assertFalse(is_replaced(Request("pn", "", "a"*40, ["backend"]), live, now=NOW))
```

Also assert the marker round-trips through `write_marker`/`read_marker`, and that `read_marker` returns `None` for a run whose `validate` conclusion was not `success`.

- [ ] **Step 2: Run them to verify they fail** — `python3 -m unittest tests.skills.test_image_pin` → FAIL (`ModuleNotFoundError`).
- [ ] **Step 3: Implement.** Move `Request`/`validate_request`/`run_gh` verbatim from `scripts/image-bump-request.py:45-125,277-281`, pointing `SOURCES_PATH` at `image-pin-sources.json`. Add `Marker` (dataclass + `to_json`/`from_json`), `live_states = {"queued", "waiting", "in_progress"}`, and `list_live_requests` reading `gh api "repos/{repo}/actions/runs?per_page=100"` and keeping only runs whose `status` is in `live_states` and whose marker file says `conclusion == "success"`. Compare `created_at` in `is_replaced`.
- [ ] **Step 4: Run the tests** → PASS.
- [ ] **Step 5: Commit**

```bash
git add scripts/image-pin.py tests/skills/test_image_pin.py
git commit -m "feat(image-pin): request validation, run markers, dedupe and supersede"
```

## Task 3: Diff check (§9)

This is what makes skipping CI safe, so it gets written before anything can open a PR.

**Files:**
- Modify: `scripts/image-pin.py` (`diff_check`, `diff-check` verb)
- Modify: `tests/skills/test_image_pin.py`

**Interfaces:**
- Consumes: the helper's `summary` dict — `{"changed": bool, "html_changed": bool, "images": [{"role", "path", "old_image", "new_image", "digest", "noop", ...}]}`.
- Produces: `diff_check(changed: dict[str, str], summary: dict, approved: dict[str, str]) -> list[str]`, where `changed` maps path → unified diff text and `approved` maps role → `repo@sha256:<64-hex>`.

- [ ] **Step 1: Write the failing tests** — accept and reject cases:

```python
# accept: one image line swapped for the approved digest, handbook digest tokens mapped
# accept: a two-role request where one role is noop (only one YAML path present)
# accept: handbook unchanged (html_changed False)
# reject: tag instead of digest      ("image: ghcr.io/x/y:latest")
# reject: different repository       (ghcr.io/other/y@sha256:...)
# reject: extra added line in the YAML
# reject: an extra file outside the approved set
# reject: a path under another project (deploy/projects/pn/...)
# reject: a digest that is not in `approved`
# reject: a handbook hunk that changes a non-digest token
```

Assert the expected-set rule from spec §9: the file set must equal the paths of roles **actually written** (`not image["noop"]`) plus the handbook **if** `html_changed`, not every requested role.

- [ ] **Step 2: Run to verify they fail** → FAIL.
- [ ] **Step 3: Implement** `diff_check`: project-scope prefix check; per-YAML-file "exactly one removed and one added line, both `image:` lines, same repo, same indentation, new digest approved"; handbook hunks tokenised over `sha256:[0-9a-f]{64}` and short `sha256:[0-9a-f]{8}` and every difference mapped old→new; and the expected-file-set equality using the written-roles rule.
- [ ] **Step 4: Run the tests** → PASS.
- [ ] **Step 5: Commit**

```bash
git add scripts/image-pin.py tests/skills/test_image_pin.py
git commit -m "feat(image-pin): strict digest-only diff check"
```

## Task 4: Message and PR-body rendering

**Files:**
- Modify: `scripts/image-pin.py` (`render_message`, `render_pr_body`)
- Modify: `tests/skills/test_image_pin.py`

**Interfaces:**
- Produces: `render_message(request, summary) -> dict` with `content` plus a link button or markdown link to `https://github.com/nacfson/nacfson_pipeline/actions/runs/<id>`, and `allowed_mentions: {"parse": []}`. `render_pr_body(..., *, public_evidence: bool) -> str`.

- [ ] **Step 1: Write the failing tests**

```python
def test_allowed_mentions_empty(self):
    self.assertEqual(render_message(REQ, SUMMARY)["allowed_mentions"], {"parse": []})

def test_review_link_is_the_pin_run_not_the_source_run(self):
    msg = json.dumps(render_message(REQ, SUMMARY, pin_run_id=42))
    self.assertIn("nacfson/nacfson_pipeline/actions/runs/42", msg)

def test_private_evidence_redacted_for_pn(self):
    body = render_pr_body(PN_REQ, PN_SUMMARY, CHECKS, RUN_URL, public_evidence=False)
    for leaked in ("/app/server", "exposed", "entrypoint"):
        self.assertNotIn(leaked.lower(), body.lower())

def test_outcome_titles(self):
    for outcome, title in {"pr_open": "PR open", "rejected": "Rejected",
                           "replaced": "Replaced by a newer build",
                           "already_pinned": "Already pinned",
                           "failed": "Failed", "expired": "Expired or cancelled"}.items():
        self.assertEqual(outcome_title(outcome), title)
```

- [ ] **Step 2: Run to verify they fail** → FAIL.
- [ ] **Step 3: Implement** both renderers, plus `outcome_title(outcome)` covering every row of spec §7.3.
- [ ] **Step 4: Run the tests** → PASS.
- [ ] **Step 5: Commit**

```bash
git add scripts/image-pin.py tests/skills/test_image_pin.py
git commit -m "feat(image-pin): Discord message and PR body rendering"
```

## Task 5: `--skip-validation` in the helper

**Files:**
- Modify: `.agents/skills/update-project-images/scripts/update_project_images.py` (`--skip-validation` flag; `run_update(..., skip_validation=False)`; guard the `validate_image` call at L1018-1026)
- Modify: `tests/skills/test_update_project_images.py`

**Interfaces:**
- Produces: `--skip-validation`, default **off**. When set, `validate_image` is not called and `summary["images"][i]["checks"|"platforms"|"evidence"]` are empty; `resolve_digest_ref` still runs.

- [ ] **Step 1: Write the failing tests** — with a digest argument and `--skip-validation`: `validate_image` is never called (patch it and assert not called), the manifest read still happens (assert `resolve_digest_ref` called), and the flag defaults to off.
- [ ] **Step 2: Run to verify they fail** → FAIL (`unrecognized arguments: --skip-validation`).
- [ ] **Step 3: Implement:** add the flag at `main()` beside `--summary-file` (L1121), thread it through `run_update` (L979), and wrap the `validate_image` call so `validation` stays `None`.

```python
validation = None if (noop or skip_validation) else validate_image(...)
```

- [ ] **Step 4: Run the whole suite** — `python3 -m unittest discover -s tests/skills` → PASS (existing helper tests unchanged).
- [ ] **Step 5: Commit**

```bash
git add .agents/skills/update-project-images tests/skills/test_update_project_images.py
git commit -m "feat(image-pin): add --skip-validation to the image helper"
```

## Task 6: `validate` job

**Files:**
- Create: `.github/workflows/image-pin.yaml` (workflow header + `validate`)

**Interfaces:**
- Consumes: `check-request`, `dedupe`, `render-message`, `write-marker` from Task 2; the helper from Task 5.
- Produces job outputs: `changed` (`true`/`false`), `roles`, and `digests` (JSON map role → `repo@sha256:...`), consumed by Tasks 7 and 8.

- [ ] **Step 1: Write the workflow.** `on: workflow_dispatch` with inputs `project`, `sha`, `roles`, `run_url` (all `required: false` except `project` and `sha`); `run-name: image-pin ${{ inputs.project }} ${{ inputs.roles }} ${{ inputs.sha }}` for readability only; **no `concurrency` group** (it would cancel waiting requests). Permissions `contents: read`, `actions: read`, `packages: read`, `pull-requests: read`. Every input reaches the shell through `env:`.
- [ ] **Step 2: Implement the steps in order** — check-request → dedupe (write marker and exit 0 quietly when duplicate) → GHCR login + helper dry-run with one commit link per role → stop when every role has `noop: true` → export `digests` → post to Discord, **warning and skipping when `DISCORD_WEBHOOK_URL` is unset** → write the marker with `conclusion: success`.
- [ ] **Step 3: Lint the workflow** — `python3 -c "import yaml,sys; yaml.safe_load(open('.github/workflows/image-pin.yaml'))"` → no error; `actionlint` if present.
- [ ] **Step 4: Confirm the no-secret path** — read the file and confirm no `run:` block interpolates `${{ inputs.* }}` directly.
- [ ] **Step 5: Commit**

```bash
git add .github/workflows/image-pin.yaml
git commit -m "feat(image-pin): validate job with dedupe and Discord notice"
```

## Task 7: `report` job

**Files:**
- Modify: `.github/workflows/image-pin.yaml`

- [ ] **Step 1: Write the job** — `needs: [validate, pin]`, `if: always()`, only when `validate` posted a message; permissions `actions: read`; secret `DISCORD_WEBHOOK_URL`. Read `GET /actions/runs/{id}/approvals` to tell a rejection from a failure, map to `outcome_title` (Task 4), and `PATCH` the message by its stored ID.
- [ ] **Step 2: Verify the outcome mapping covers every §7.3 row** — read the job and check each of the six titles is reachable, including `expired` when the run was cancelled at GitHub's 30-day limit.
- [ ] **Step 3: Confirm the job cannot fail the run** — a Discord `PATCH` failure must warn, not error.
- [ ] **Step 4: Commit**

```bash
git add .github/workflows/image-pin.yaml
git commit -m "feat(image-pin): report job edits the Discord message with the outcome"
```

## Task 8: `pin` job and the admission gate

The operator's approval unlocks this job. Everything here is what makes a CI-less PR safe.

**Files:**
- Modify: `.github/workflows/image-pin.yaml`

**Interfaces:**
- Consumes: `newer-check`, `diff-check`, `render-pr-body` from Tasks 2-4; `--skip-validation` from Task 5.
- Produces: a branch `chore/images-<project>-<sha7>` and an open PR, or one of the outcomes `replaced`, `pr_exists`, `already_pinned`, `failed`.

- [ ] **Step 1: Write the job** — `environment: image-pin`; permissions `contents: write`, `pull-requests: write`, `actions: read`, `packages: read`; no secret of its own. Steps in spec order:
  1. Check out `main` (not the requesting commit), `fetch-depth: 0` (the diff needs a base).
  2. `newer-check` → outcome `replaced`, stop.
  3. Existing open PR for this branch → `pr_exists`.
  4. Apply digests with `--skip-validation`, then **assert each applied `old_repo` equals the repo `validate` recorded**.
  5. `main` already has these digests → `already_pinned`.
  6. Admission gate, in this order:
     - `python3 scripts/image-pin.py diff-check`
     - `check-arch-doc-sync.py` in a **diff mode that works on a fresh checkout** — see Step 2
     - for each `env` in `local-k3s`, `vps-k3s`:
       `CAND=$(./scripts/render-validate.sh $env | tail -n1)` then
       `python3 scripts/platform-preflight.py --environment $env --candidate "$CAND" --capacity clusters/$env/capacity.yaml`
  7. Push and open the PR with `GITHUB_TOKEN`, commit message `chore(proj-<project>): update <roles> image pin(s)`.
- [ ] **Step 2: Fix the CI diff mode for the handbook check.** `--worktree` reads `git status --porcelain`, which is empty on a fresh checkout, so it would pass unconditionally. Give `check-arch-doc-sync.py` a mode that takes a base ref (for example `--staged`-equivalent over `git diff --name-only origin/main...HEAD`), and use that here. **Do not** change the existing `--worktree` behaviour — local hooks depend on it.
- [ ] **Step 3: Confirm tool provisioning.** The runner needs `flux` and `kubeconform` for `render-validate.sh`, plus `skopeo` (preinstalled) and PyYAML. Install them in the job with the same pinned versions `gitops.yaml` uses (`FLUX_VERSION`, `KUBECONFORM_VERSION`). Do **not** refactor `gitops.yaml` in this step — duplicate the ~10 lines and note it for step 4.
- [ ] **Step 4: Add the gate-ordering tests** to `tests/skills/test_image_pin.py`:

```python
def test_gate_runs_preflight_for_both_environments(self):
    text = (ROOT / ".github/workflows/image-pin.yaml").read_text()
    self.assertIn("render-validate.sh", text)
    self.assertIn("platform-preflight.py", text)          # render-validate does NOT run it
    doc = yaml.safe_load(text)
    pin = doc["jobs"]["pin"]
    self.assertEqual(sorted(pin["strategy"]["matrix"]["env"]), ["local-k3s", "vps-k3s"])

def test_no_inputs_interpolated_into_run_blocks(self):
    """A line-based grep misses `run: |` continuation lines, so parse the YAML."""
    doc = yaml.safe_load((ROOT / ".github/workflows/image-pin.yaml").read_text())
    for job_name, job in doc["jobs"].items():
        for step in job.get("steps", []):
            body = step.get("run") or ""
            self.assertNotIn("${{ inputs.", body,
                             f"{job_name}/{step.get('name')} interpolates inputs into a run block")
            self.assertNotIn("${{ github.event.inputs.", body)
```

- [ ] **Step 5: Commit**

```bash
git add .github/workflows/image-pin.yaml scripts/check-arch-doc-sync.py tests/skills/test_image_pin.py
git commit -m "feat(image-pin): pin job behind the approval gate with the full admission gate"
```

## Task 9: Digest-path safety tests

**Files:**
- Modify: `tests/skills/test_image_pin.py`, `tests/skills/test_update_project_images.py`

- [ ] **Step 1: Write the tests**

```python
def test_bare_digest_is_reinterpreted_as_current_repo(self):
    """Documents the trap: sha256:<hex> ignores any repo you meant to pass."""
    self.assertEqual(resolve_link(TARGET, parse_link("sha256:" + "a"*64)).repo, TARGET.old_repo)

def test_repo_at_digest_mismatch_is_rejected(self):
    with self.assertRaises(UpdateError):
        resolve_link(TARGET, parse_link("ghcr.io/other/repo@sha256:" + "a"*64))

def test_skip_validation_still_confirms_existence(self):   # resolve_digest_ref is called
def test_skip_validation_is_off_by_default(self)
```

- [ ] **Step 2: Run to verify they fail (or pass for the intended reason)** — the first two should pass against current behaviour; that is the point, they pin it. Any that fail reveal a wrong assumption in this plan — fix the plan before the code.
- [ ] **Step 3: Commit**

```bash
git add tests/skills
git commit -m "test(image-pin): pin digest-path repo handling and skip-validation scope"
```

## Task 10: Docs and index

**Files:**
- Create: `docs/image-pin.md` (operator guide: how to dispatch by hand, where to approve, what each outcome means)
- Modify: `INDEX.md` (rows for `scripts/image-pin.py`, `docs/image-pin.md`, `tests/skills/test_image_pin.py`, `.github/workflows/image-pin.yaml`)
- Modify: `AGENTS.md` §3 routing line for image pins

- [ ] **Step 1: Write the operator guide** — lead with the phone flow: dispatch by hand, wait for the pending deployment, approve from the GitHub app's Notifications tab (not a run page opened in-app, which may show only "Cancel workflow"), then merge the PR. Include the six outcomes and what to do for each.
- [ ] **Step 2: Add the INDEX rows** — `INDEX.md` catalogues scripts and test suites individually, so a single repointed row is not enough.
- [ ] **Step 3: Verify nothing points at the old flow** — `grep -rn "image-bump" INDEX.md AGENTS.md docs/image-pin.md` → only historical `docs/superpowers/` records may remain (step 4 removes the rest).
- [ ] **Step 4: Commit**

```bash
git add docs/image-pin.md INDEX.md AGENTS.md
git commit -m "docs(image-pin): operator guide and index entries"
```

## Task 11: Repository settings (operator, in the GitHub UI)

Not code. Nothing here is needed to *build* the workflow, but the phone test cannot run without it.

- [ ] **Step 1: Create the environment.** Settings → Environments → new `image-pin`. Required reviewer: the operator. **Prevent self-review: off** (the token belongs to the operator, so they are the initiator and could not otherwise approve). Deployment branches: `main` only.
- [ ] **Step 2: Enable Actions-created PRs.** Settings → Actions → General → Workflow permissions → enable **"Allow GitHub Actions to create and approve pull requests"**. Default permissions stay read.
- [ ] **Step 3: Confirm no secret is needed yet.** `DISCORD_WEBHOOK_URL` is **not** set in step 1; `validate` must warn and skip. This is deliberate — it is what lets the phone test happen before step 2 builds Discord.

## Task 12: Live acceptance — the phone test

The whole point of step 1. Run it for `blog`, by hand.

- [ ] **Step 1: Find a real blog publish.** Pick a recent successful `main` build in `nacfson/nacfson_blog` and note its commit SHA.
- [ ] **Step 2: Dispatch by hand with no `run_url`**

```bash
gh workflow run image-pin.yaml -R nacfson/nacfson_pipeline --ref main \
  -f project=blog -f sha=<40-hex> -f roles=web
```

Expected: the `validate` job validates `blog-web`, reports `changed: true`, warns about the missing Discord secret, and `pin` goes to **Pending** and waits.

- [ ] **Step 3: Approve from the phone.** Open the GitHub app → **Notifications** tab → the deployment review request → approve. Record how long this took and how many taps. **This is the measurement that decides the whole design.**
- [ ] **Step 4: Confirm the PR.** Expected: one PR on `chore/images-blog-<sha7>`, description with the digest table, no CI checks started, and the `pin` run log showing render-validate and platform-preflight for both environments.
- [ ] **Step 5: Confirm the gate really ran.** In the run log, find `Platform Preflight Status: PASSED` **twice** (both environments) and a kubeconform summary. If preflight is missing, the gate did not run — stop and fix Task 8.
- [ ] **Step 6: Reject path.** Dispatch the same commit again and **reject** at the gate. Expected: no branch, no PR, and no second message was created by the duplicate check.
- [ ] **Step 7: Merge the PR** (operator). Expected: `gitops`, `lint`, `secret-scan` run on `main`, and Flux applies the new digest. Confirm with `kubectl -n proj-blog get deploy blog-web -o jsonpath='{.spec.template.spec.containers[0].image}'`.
- [ ] **Step 8: Record the verdict.** If phone approval is comfortable, continue to step 2 (Discord) and then connecting the projects. If it is awkward, the fallback is Discord buttons with a serverless endpoint, and only the approval step changes.

---

## Out of scope for this plan

Deferred by design, each with its own step:

- **Discord channel, webhook, real message, `report` polish** — step 2. This plan ships `report` and the message code, but with no webhook configured it only warns.
- **Sender jobs in the three project repos, `PIPELINE_RUN_TOKEN`** — step 3. Step 1 is dispatched by hand.
- **Deleting `image-bump-*`, `docs/image-bump-dispatch.md`, and `review-gate.yaml`; the `code-review` exemption in `AGENTS.md` §5** — step 4.
- **The constitution amendment for GitHub-held secrets** — step 4. Step 1 stores no secret.
- **Pinning `ai-proxy.yaml` / `registry-proxy.yaml` off `:latest`** — before step 3, when a token first exists. Not needed for step 1.
- **Extracting the shared CI setup step from `gitops.yaml`** — step 4, deliberately not done here.
