---
name: code-review
description: Run the multi-agent fresh-context code review pipeline aligned with REVIEW.md and record a verified review receipt via scripts/review-cycle.py.
compatibility: Works in Antigravity (agy) and compatible multi-agent harnesses.
---

## User Input
```text
$ARGUMENTS
```

## Purpose
Execute the full `nacfson_pipeline` multi-agent code review cycle on `staged`, `worktree`, or `branch` changes using fresh-context subagents (`invoke_subagent`) and record a cryptographically bound receipt in `.git/review-cycle/receipt.json`.

## Step-by-Step Workflow

1. **Determine Scope & Run Deterministic Verification**:
   - Default scope is `staged` (or `worktree` / `branch` if specified in `$ARGUMENTS`).
   - Run domain verification and path-based risk classification:
     ```bash
     python3 scripts/review-cycle.py verify --scope staged --json
     python3 scripts/review-cycle.py classify-risk --scope staged --json
     ```

2. **Stage 0 — Diff Simplification & End-to-End Behavior Check**:
   - Dispatch fresh-context subagents via `invoke_subagent` (`TypeName: "research"`, `Model: "inherit"`) using the role prompts in:
     - `.agents/review-prompts/diff-simplifier.md`
     - `.agents/review-prompts/e2e-behavior.md`

3. **Stage 1 — Parallel Specialized Hunters**:
   - Dispatch parallel fresh-context subagents via `invoke_subagent` (`TypeName: "research"`, `Model: "inherit"`) using:
     - `.agents/review-prompts/bug-reviewer.md`
     - `.agents/review-prompts/security-reviewer.md`
     - `.agents/review-prompts/contract-reviewer.md`
     - `.agents/review-prompts/risk-classifier.md`

4. **Stage 2 — Skeptical Finding Verification & Deduplication**:
   - Pass all candidate findings from Stages 0–1 to a fresh verifier subagent via `invoke_subagent` using `.agents/review-prompts/finding-verifier.md`.
   - The verifier enforces `REVIEW.md`: drops style/diagnostic noise, verifies exact `line_start`–`line_end` citations, groups repeated defects by `class_of_bug`, and caps `Nit` findings at `max_nits = 2`.

5. **Stage 3 — Persist Verified Receipt & Check Gate**:
   - Record the verified payload and verify the gate status:
     ```bash
     python3 scripts/review-cycle.py record-receipt --scope staged --input - <<'EOF'
     {
      "agent_harness": "agy",
      "scope": "staged",
      "pipeline": "code-review",
       "classifier2_override": null,
       "verification": {
         "passed": true,
         "commands_run": ["python3 scripts/review-cycle.py verify --scope staged"]
       },
       "findings": []
     }
     EOF
    python3 scripts/review-cycle.py check-gate --scope staged --require-pipeline code-review
     ```
   - The commit hooks reject a receipt whose `pipeline` is not `code-review`. Set that field only after the stages above have run. The hook checks the field; it cannot prove these stages ran if you set the field yourself.
   - Begin the final response with the standard `REVIEW.md` summary header:
     `X Important, Y Security, N Nit, M Pre-existing`
