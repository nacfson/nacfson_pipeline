---
description: Speak a concise ASD-STE100 summary of the current git diff aloud using omp say.
---

## User Input

```text
$ARGUMENTS
```

## Instructions

1. Run `git diff` or inspect unstaged changes (or the target files in `$ARGUMENTS`).
2. Synthesize a 3-sentence summary in strict ASD-STE100:
   - Identify the component changed.
   - State the primary modification.
   - State the expected operational effect.
3. Pipe the clean summary into `omp say`:
   ```bash
   omp say "<3-sentence STE summary>"
   ```
4. Also output the text to the terminal transcript.
