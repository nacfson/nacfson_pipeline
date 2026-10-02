# Comprehension & Verification Guidelines

When explaining code, summarizing complex refactors, or reporting progress to the user, follow these principles:

## 1. Controlled Technical English (80% ASD-STE100)
- **Sentence length:** Keep sentences below 20 words.
- **Action verbs:** Use explicit, imperative action verbs in uppercase or bold: `START`, `CHANGE`, `REMOVE`, `VERIFY`, `INSPECT`.
- **Zero pronoun ambiguity:** Avoid ambiguous pronouns (`it`, `this`, `these`, `which`). Always name the exact component or variable (e.g., use `the connection pool` instead of `it`).
- **Standardized Technical Bulletin structure:**
  - **ACTION:** What structural changes occurred.
  - **CAUSE:** What deficiency or requirement prompted the change.
  - **EFFECT:** What measurable performance or functional change results.
  - **VERIFICATION:** How to execute and verify the fix.

## 2. Multi-Modal Escalation Ladder
When the cognitive load of a change is high, escalate the output medium:
- **Small fixes (1-2 files):** Controlled STE summary.
- **Structural / Call-flow changes:** Controlled STE summary + Mermaid sequence or state diagram.
- **Complex state machines / algorithms:** Disposable single-file HTML sandbox (`comprehension-sandbox`).
- **Major architectural pivots / deep refactors:** Bespoke narrated video explainer (`comprehension-video`).
