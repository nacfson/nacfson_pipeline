---
description: Generate a bespoke 1080p narrated explainer video for current changes or specified topic.
---

## User Input

```text
$ARGUMENTS
```

## Instructions

1. Identify the topic, recent git commits, or unstaged changes to explain.
2. Formulate a 30-to-45 second spoken narration script and 2-4 slide descriptions.
3. Save the slide manifest to `/tmp/video_manifest.json`:
   ```json
   {
     "slides": [
       {
         "tag": "OVERVIEW",
         "heading": "Title",
         "bullets": ["ACTION: ...", "CAUSE: ...", "EFFECT: ..."],
         "code": "sample code"
       }
     ]
   }
   ```
4. Synthesize voiceover audio:
   ```bash
   python3 scripts/comprehension/synthesize_speech.py \
     --text "<narration_script>" \
     --output /tmp/video_narration.mp3
   ```
5. Compose the video:
   ```bash
   uv run --with pillow scripts/comprehension/compose_video.py \
     --manifest /tmp/video_manifest.json \
     --audio /tmp/video_narration.mp3 \
     --output /tmp/explainer.mp4
   ```
6. Report the video location to the user:
   - File: `/tmp/explainer.mp4`
   - Play command: `xdg-open /tmp/explainer.mp4`
