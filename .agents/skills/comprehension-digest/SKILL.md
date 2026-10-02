---
name: comprehension-digest
description: Generate an unambiguous, high-clarity technical bulletin using 80% ASD-STE100 and Mermaid architecture/delta diagrams.
compatibility: Works in any codebase within Antigravity CLI.
---

## User Input
```text
$ARGUMENTS
```

## Purpose
When the user asks for a digest, explanation, or code review of a file, module, or git diff:
Translate complex logic into an aerospace-grade **Technical Bulletin** formatted according to 80% ASD-STE100 principles, paired with a visual Mermaid diagram.

## Guidelines
1. **Sentence Limit:** Max 20 words per sentence.
2. **Grammar:** Use active voice. No ambiguous pronouns (`it`, `this`, `they`). Specify exact symbols and components.
3. **Imperative Verbs:** Highlight primary actions using `ACTION:`, `CAUSE:`, `EFFECT:`, and `VERIFY:`.
4. **Visual Diagram:** Include a Mermaid diagram illustrating the flow, state transition, or architectural delta.

## Output Format
Create a Markdown artifact or respond directly with:

```markdown
### TECHNICAL BULLETIN: [Component / Change Name]

#### 1. Procedural Summary
- **ACTION:** [Specific structural change made to the code].
- **CAUSE:** [Exact problem, bug, or requirement that required this change].
- **EFFECT:** [Concrete operational or performance consequence].
- **VERIFY:** [Exact commands to test and validate the behavior].

#### 2. Visual Architecture Schema
```mermaid
[Mermaid Diagram here]
```

#### 3. Critical Oversight Notes
[Any caveats, edge cases, or rollback instructions in direct, unambiguous language.]
```
