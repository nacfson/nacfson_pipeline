---
name: comprehension-video
description: Generate a bespoke, 1080p narrated explainer video for high-impact architecture changes, algorithms, or onboarding.
compatibility: Requires Python 3, uv, and FFmpeg (bundled in repo scripts/comprehension/).
---

## User Input
```text
$ARGUMENTS
```

## Purpose
Produce an animated, narrated video walkthrough of a module, refactor, or concept using the zero-root video composition pipeline in `scripts/comprehension/`.

## Procedure

### Step 1: Write Narration Script & Slide Manifest
Draft a concise, 30-to-60 second technical narration script in 80% ASD-STE100.
Structure into 2-5 slides:
- Slide 1: Problem / Motivation (Tag: `PROBLEM` or `BACKGROUND`)
- Slide 2: Structural Change / Implementation (Tag: `ARCHITECTURE` or `IMPLEMENTATION`) with code snippet
- Slide 3: Verification / Outcome (Tag: `VERIFY` or `BENCHMARK`)

Save manifest JSON to `<appDataDir>/brain/<conversation-id>/scratch/video_manifest.json`:
```json
{
  "slides": [
    {
      "tag": "ARCHITECTURE",
      "heading": "Pipeline Concurrency Refactor",
      "bullets": [
        "ACTION: REPLACE unbounded channel with fixed ring buffer.",
        "CAUSE: High ingestion spikes caused OOM failures.",
        "EFFECT: Memory usage remains strictly below 256MB.",
        "VERIFY: Run load test suite with 50k events per second."
      ],
      "code": "def run_worker():\n    ..."
    }
  ]
}
```

### Step 2: Synthesize Voiceover Audio
Synthesize the narration text into an audio file:
```bash
python3 scripts/comprehension/synthesize_speech.py \
  --text "..." \
  --output "<appDataDir>/brain/<conversation-id>/scratch/narration.mp3"
```

### Step 3: Compose 1080p Video
Render the video using the procedural composer:
```bash
uv run --with pillow scripts/comprehension/compose_video.py \
  --manifest "<appDataDir>/brain/<conversation-id>/scratch/video_manifest.json" \
  --audio "<appDataDir>/brain/<conversation-id>/scratch/narration.mp3" \
  --output "<appDataDir>/brain/<conversation-id>/explainer.mp4"
```

### Step 4: Present Video Artifact
Deliver the video artifact with clickable path and command to open:
```markdown
### 🎥 Video Explainer: [Topic Name]
- **Video File:** [explainer.mp4](file:///absolute/path/to/explainer.mp4)
- **Watch in system player:** `xdg-open file:///absolute/path/to/explainer.mp4`
```
