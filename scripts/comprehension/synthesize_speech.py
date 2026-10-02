#!/usr/bin/env python3
"""
Speech Synthesis Script for the Comprehension Engine.
Supports:
1. Microsoft Neural TTS via edge-tts (uvx edge-tts) - Zero configuration, high quality.
2. ElevenLabs API if ELEVENLABS_API_KEY is present in environment.
3. Fallback to omp say if available.
"""

import argparse
import os
import shutil
import subprocess
import sys
from pathlib import Path


def synthesize_with_edge_tts(text: str, output_path: Path, voice: str = "en-US-ChristopherNeural") -> bool:
    """Synthesize speech using uvx edge-tts."""
    try:
        cmd = [
            "uvx", "edge-tts",
            "-t", text,
            "-v", voice,
            "--write-media", str(output_path)
        ]
        result = subprocess.run(cmd, capture_output=True, text=True, check=True)
        return output_path.exists() and output_path.stat().st_size > 0
    except (subprocess.CalledProcessError, FileNotFoundError) as e:
        print(f"[WARN] edge-tts synthesis failed: {e}", file=sys.stderr)
        return False


def synthesize_with_elevenlabs(text: str, output_path: Path, api_key: str) -> bool:
    """Synthesize speech using ElevenLabs API via curl/python."""
    import urllib.request
    import json

    voice_id = os.environ.get("ELEVENLABS_VOICE_ID", "21m00Tcm4TlvDq8ikWAM")  # Default voice: Rachel
    url = f"https://api.elevenlabs.io/v1/text-to-speech/{voice_id}"
    headers = {
        "Accept": "audio/mpeg",
        "Content-Type": "application/json",
        "xi-api-key": api_key,
    }
    data = json.dumps({
        "text": text,
        "model_id": "eleven_monolingual_v1",
        "voice_settings": {"stability": 0.5, "similarity_boost": 0.75}
    }).encode("utf-8")

    req = urllib.request.Request(url, data=data, headers=headers, method="POST")
    try:
        with urllib.request.urlopen(req) as resp:
            output_path.write_bytes(resp.read())
        return True
    except Exception as e:
        print(f"[WARN] ElevenLabs synthesis failed: {e}", file=sys.stderr)
        return False


def synthesize_with_omp(text: str, output_path: Path) -> bool:
    """Synthesize speech using omp say --out."""
    omp_path = shutil.which("omp") or str(Path.home() / ".bun/bin/omp")
    if not os.path.exists(omp_path):
        return False
    try:
        cmd = [omp_path, "say", text, "--out", str(output_path)]
        subprocess.run(cmd, capture_output=True, text=True, check=True, timeout=15)
        return output_path.exists() and output_path.stat().st_size > 0
    except Exception as e:
        print(f"[WARN] omp say synthesis failed: {e}", file=sys.stderr)
        return False


def main():
    parser = argparse.ArgumentParser(description="Comprehension Engine Speech Synthesizer")
    parser.add_argument("--text", "-t", help="Raw text to speak")
    parser.add_argument("--file", "-f", type=Path, help="Input file containing text to speak")
    parser.add_argument("--output", "-o", type=Path, required=True, help="Output audio file path (mp3/wav)")
    parser.add_argument("--voice", "-v", default="en-US-ChristopherNeural", help="Voice model identifier")
    args = parser.parse_args()

    if args.file:
        text = args.file.read_text(encoding="utf-8").strip()
    elif args.text:
        text = args.text.strip()
    else:
        text = sys.stdin.read().strip()

    if not text:
        print("Error: No text provided.", file=sys.stderr)
        sys.exit(1)

    args.output.parent.mkdir(parents=True, exist_ok=True)

    # 1. Check ElevenLabs API key
    eleven_key = os.environ.get("ELEVENLABS_API_KEY")
    if eleven_key:
        print(f"[TTS] Using ElevenLabs API...")
        if synthesize_with_elevenlabs(text, args.output, eleven_key):
            print(f"[TTS] Successfully generated {args.output}")
            sys.exit(0)

    # 2. Try edge-tts via uvx (zero setup, high quality)
    print(f"[TTS] Synthesizing speech with edge-tts ({args.voice})...")
    if synthesize_with_edge_tts(text, args.output, args.voice):
        print(f"[TTS] Successfully generated {args.output}")
        sys.exit(0)

    # 3. Fallback to omp say
    print("[TTS] Falling back to omp say...")
    if synthesize_with_omp(text, args.output):
        print(f"[TTS] Successfully generated {args.output}")
        sys.exit(0)

    print("Error: All speech synthesis backends failed.", file=sys.stderr)
    sys.exit(1)


if __name__ == "__main__":
    main()
