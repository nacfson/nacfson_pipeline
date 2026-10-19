---
description: Break down the current diff into dependency-ordered review slices with risk and verification annotations.
---

## User Input

```text
$ARGUMENTS
```

## Instructions

1. Follow `.agents/skills/explain-slice/SKILL.md` and `REVIEW.md`.
2. Run `python3 scripts/review-cycle.py classify-risk --scope worktree --json`.
3. Group changed files into logical slices (Contracts/Policy → Runtime/Automation → Cluster/Project Manifests → Tests/Handbook) and list exact verification commands per slice.
