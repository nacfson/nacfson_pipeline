---
description: Run a focused security and trust-boundary review aligned with REVIEW.md and record a receipt via scripts/review-cycle.py.
---

## User Input

```text
$ARGUMENTS
```

## Instructions

1. Follow the workflow in `.agents/skills/security-review/SKILL.md` and `REVIEW.md`.
2. Run `python3 scripts/review-cycle.py verify --scope staged --json` and `python3 scripts/review-cycle.py classify-risk --scope staged --json`.
3. Audit the diff using `.agents/review-prompts/security-reviewer.md`, `.agents/review-prompts/contract-reviewer.md`, `.agents/review-prompts/risk-classifier.md`, and `.agents/review-prompts/finding-verifier.md`.
4. Record the verified receipt via `python3 scripts/review-cycle.py record-receipt --scope staged --input -` and verify `python3 scripts/review-cycle.py check-gate --scope staged`.
