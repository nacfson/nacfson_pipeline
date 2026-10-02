---
description: Generate a self-contained single-file HTML interactive sandbox and preview with browser.
---

## User Input

```text
$ARGUMENTS
```

## Instructions

1. Identify the state machine, algorithm, or component specified in `$ARGUMENTS` (or from recent session edits).
2. Generate a single self-contained HTML file `/tmp/sandbox.html` with:
   - Modern Tailwind styling (via CDN `https://cdn.tailwindcss.com`).
   - Interactive UI controls (sliders, input fields, test trigger buttons).
   - Live visualizer or canvas rendering state transitions dynamically.
   - Event log container.
3. Open the generated file using the `browser` tool:
   - Navigate to `file:///tmp/sandbox.html`.
   - Take a screenshot to confirm rendering.
   - Let the user know the interactive URL is available for direct inspection.
