---
name: comprehension-sandbox
description: Generate a self-contained, interactive single-file HTML/JS sandbox application to visually explore and test complex code, state machines, or data structures.
compatibility: Works in Antigravity CLI and standard browsers.
---

## User Input
```text
$ARGUMENTS
```

## Purpose
When dealing with state machines, algorithms, regex/parsers, or high-dimensional configuration logic:
Generate a disposable, interactive single-page HTML application (`sandbox_<component>.html`) directly into the conversation artifacts directory (`<appDataDir>/brain/<conversation-id>/`) or scratch directory.

## Capabilities & Architecture
1. **Zero build step:** Must be a single `.html` file that bundles HTML, CSS, and JavaScript.
2. **Modern UI:** Use Tailwind CSS via CDN (`https://cdn.tailwindcss.com`) and dark mode by default (`bg-slate-900 text-slate-100`).
3. **Interactive Controls:**
   - Sliders, toggle switches, or text input boxes to adjust parameters dynamically.
   - Live visualizer (Canvas, SVG, or DOM elements) reflecting state changes in real time.
   - Event log panel displaying transitions, mutations, and metrics.
4. **Execution & Delivery:**
   - Write the file to `<appDataDir>/brain/<conversation-id>/sandbox_<name>.html`.
   - Provide a direct file link: `[Open Interactive Sandbox](file:///absolute/path/to/sandbox.html)`.
   - Optionally open it in the default browser using:
     ```bash
     xdg-open file:///absolute/path/to/sandbox.html
     ```
