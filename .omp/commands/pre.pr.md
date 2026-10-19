---
description: Run pre-PR domain verification, eval benchmark checks, and review receipt validation via scripts/review-cycle.py.
---

## User Input

```text
$ARGUMENTS
```

## Instructions

1. Follow the workflow in `.agents/skills/pre-pr-verify/SKILL.md` and `REVIEW.md`.
2. Run `python3 scripts/review-cycle.py verify --scope branch --json`, `python3 scripts/review-cycle.py classify-risk --scope branch --json`, and `python3 scripts/review-cycle.py eval --json`.
3. Execute the review prompts in `.agents/review-prompts/` and record the receipt via `python3 scripts/review-cycle.py record-receipt`.
4. Output the markdown summary via `python3 scripts/review-cycle.py summary --markdown`.
