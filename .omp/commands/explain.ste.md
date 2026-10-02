---
description: Summarize current git diff or target file in strict 80% ASD-STE100 technical English.
---

## User Input

```text
$ARGUMENTS
```

## Instructions

1. If arguments are provided, inspect the target file or symbol. If no arguments are provided, run `git diff` or inspect unstaged changes.
2. Structure the summary strictly using 80% ASD-STE100:
   - **Sentence constraint:** Maximum 20 words per sentence.
   - **Active voice:** No passive constructions.
   - **No pronouns:** Explicitly name classes, functions, or files instead of "it", "this", or "which".
   - **Core fields:**
     - `ACTION:` Explicit code or structural change.
     - `CAUSE:` Root reason or bug requirement.
     - `EFFECT:` Measurable behavioral or architectural impact.
     - `VERIFY:` Direct commands to test.
