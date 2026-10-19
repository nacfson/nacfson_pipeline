---
name: pre-pr-verify
description: Run end-to-end pre-PR verification, risk classification, multi-agent review, and receipt gate validation via scripts/review-cycle.py.
compatibility: Works in Antigravity (agy) and compatible multi-agent harnesses.
---

## User Input
```text
$ARGUMENTS
```

## Purpose
Validate a feature branch (`--scope branch` or `--scope staged`) before opening or updating a pull request, ensuring all domain tests, architecture handbook sync checks, and fresh-context subagent reviews pass with a valid receipt.

## Step-by-Step Workflow

1. **Run Domain Verification & Risk Classification**:
   ```bash
   python3 scripts/review-cycle.py verify --scope branch --json
   python3 scripts/review-cycle.py classify-risk --scope branch --json
   python3 scripts/review-cycle.py eval --json
   ```

2. **Dispatch Fresh-Context Review Subagents (`invoke_subagent`)**:
   - Invoke parallel subagents via `invoke_subagent` (`TypeName: "research"`, `Model: "inherit"`) using the prompts in `.agents/review-prompts/`:
     - `diff-simplifier.md` & `e2e-behavior.md`
     - `bug-reviewer.md`, `security-reviewer.md`, `contract-reviewer.md`, & `risk-classifier.md`
   - Deduplicate and verify candidate findings via `invoke_subagent` with `.agents/review-prompts/finding-verifier.md`.

3. **Record Receipt & Print Markdown PR Summary**:
   ```bash
   python3 scripts/review-cycle.py record-receipt --scope staged --input <verified-payload.json>
   python3 scripts/review-cycle.py check-gate --scope staged
   python3 scripts/review-cycle.py summary --markdown
   ```
