---
description: Run the multi-agent REVIEW.md code review pipeline and record a verified review receipt via scripts/review-cycle.py.
---

## User Input

```text
$ARGUMENTS
```

## Instructions

1. Follow the workflow in `.agents/skills/code-review/SKILL.md` and the canonical rulebook in `REVIEW.md`.
2. Run deterministic verification and risk classification:
   - `python3 scripts/review-cycle.py verify --scope staged --json`
   - `python3 scripts/review-cycle.py classify-risk --scope staged --json`
3. Execute the review passes defined in `.agents/review-prompts/{diff-simplifier,e2e-behavior,bug-reviewer,security-reviewer,contract-reviewer,risk-classifier,finding-verifier}.md`.
4. Persist the verified receipt with `python3 scripts/review-cycle.py record-receipt --scope staged --input -` and verify the gate with `python3 scripts/review-cycle.py check-gate --scope staged`.
