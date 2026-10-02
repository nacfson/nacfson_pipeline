#!/usr/bin/env python3
"""
Procedural Video Composer for the Comprehension Engine.
Renders high-definition (1080p) motion slides and muxes them with narration audio
using Pillow and FFmpeg. Runs completely in user-space without C-compiler libraries.
"""

import argparse
import json
import os
import subprocess
import sys
import tempfile
from pathlib import Path
from typing import List, Dict, Any, Optional

try:
    from PIL import Image, ImageDraw, ImageFont
except ImportError:
    # If not in active env, re-invoke through uv
    print("[INFO] Re-invoking compose_video.py with uv --with pillow...", file=sys.stderr)
    cmd = ["uv", "run", "--with", "pillow", sys.executable, *sys.argv]
    sys.exit(subprocess.call(cmd))


WIDTH = 1920
HEIGHT = 1080

COLOR_BG = (13, 17, 23)           # Dark slate background
COLOR_CARD = (22, 27, 34)         # Card background
COLOR_BORDER = (48, 54, 61)       # Subtle border
COLOR_TEXT = (230, 237, 243)      # Clean white text
COLOR_MUTED = (139, 148, 158)     # Muted gray text
COLOR_ACCENT = (88, 166, 255)     # Blueprint cyan/blue
COLOR_BADGE_BG = (31, 111, 235)   # Badge background
COLOR_CODE_BG = (10, 12, 16)      # Code container background
COLOR_GREEN = (63, 185, 80)       # Success / Action green


def get_font(font_name: str, size: int) -> ImageFont.FreeTypeFont:
    """Find a readable font on the system or fallback to default."""
    candidates = [
        f"/usr/share/fonts/google-noto/{font_name}.ttf",
        f"/usr/share/fonts/dejavu/{font_name}.ttf",
        "/usr/share/fonts/google-noto/NotoSans-Regular.ttf",
        "/usr/share/fonts/google-noto/NotoSans-Bold.ttf",
    ]
    for p in candidates:
        if os.path.exists(p):
            try:
                return ImageFont.truetype(p, size)
            except Exception:
                continue
    return ImageFont.load_default()


def get_audio_duration(audio_path: Path) -> float:
    """Use ffprobe to determine duration of an audio file."""
    cmd = [
        "ffprobe", "-v", "error",
        "-show_entries", "format=duration",
        "-of", "default=noprint_wrappers=1:nokey=1",
        str(audio_path)
    ]
    try:
        out = subprocess.check_output(cmd, text=True).strip()
        return float(out)
    except Exception:
        return 5.0


def draw_slide(slide: Dict[str, Any], slide_index: int, total_slides: int, output_path: Path):
    """Render a single 1080p slide to PNG."""
    img = Image.new("RGB", (WIDTH, HEIGHT), COLOR_BG)
    draw = ImageDraw.Draw(img)

    # 1. Subtle grid lines / background styling
    for y in range(0, HEIGHT, 80):
        draw.line([(0, y), (WIDTH, y)], fill=(18, 22, 29), width=1)
    for x in range(0, WIDTH, 80):
        draw.line([(x, 0), (x, HEIGHT)], fill=(18, 22, 29), width=1)

    # 2. Header bar
    font_badge = get_font("NotoSans-Bold", 20)
    font_header = get_font("NotoSans-Bold", 46)
    font_body = get_font("NotoSans-Regular", 30)
    font_code = get_font("NotoSansMono-Regular", 24)
    font_footer = get_font("NotoSans-Regular", 22)

    # Tag Badge
    tag = slide.get("tag", "EXPLAINER").upper()
    badge_w = len(tag) * 14 + 28
    draw.rounded_rectangle([100, 75, 100 + badge_w, 115], radius=6, fill=COLOR_BADGE_BG)
    draw.text((114, 82), tag, font=font_badge, fill=(255, 255, 255))

    # Slide Counter
    counter_text = f"STEP {slide_index + 1} OF {total_slides}"
    draw.text((WIDTH - 260, 84), counter_text, font=font_badge, fill=COLOR_MUTED)

    # Heading
    heading = slide.get("heading", "System Overview")
    draw.text((100, 140), heading, font=font_header, fill=COLOR_TEXT)

    # Separator
    draw.line([(100, 220), (WIDTH - 100, 220)], fill=COLOR_BORDER, width=2)

    # Layout: check if slide has code or diagram
    has_code = "code" in slide and bool(slide["code"])
    has_diagram = "diagram" in slide and bool(slide["diagram"]) and os.path.exists(slide["diagram"])

    content_w = WIDTH - 200
    left_w = content_w if not (has_code or has_diagram) else int(content_w * 0.52)

    # Render Bullets
    bullets = slide.get("bullets", [])
    curr_y = 260
    for bullet in bullets:
        # Check for STE Action prefix
        prefix_color = COLOR_GREEN if any(bullet.startswith(k) for k in ["ACTION:", "VERIFY:", "CHANGE:", "CAUSE:", "EFFECT:"]) else COLOR_ACCENT
        draw.ellipse([100, curr_y + 12, 112, curr_y + 24], fill=prefix_color)

        # Word wrap bullet text
        words = bullet.split()
        lines = []
        cur_line = []
        for word in words:
            test_line = " ".join(cur_line + [word])
            bbox = draw.textbbox((0, 0), test_line, font=font_body)
            if bbox[2] - bbox[0] < left_w - 40:
                cur_line.append(word)
            else:
                lines.append(" ".join(cur_line))
                cur_line = [word]
        if cur_line:
            lines.append(" ".join(cur_line))

        for line in lines:
            draw.text((130, curr_y), line, font=font_body, fill=COLOR_TEXT)
            curr_y += 42
        curr_y += 24

    # Render Side Panel (Code or Diagram)
    if has_code:
        panel_x = 100 + left_w + 40
        panel_y = 260
        panel_w = WIDTH - panel_x - 100
        panel_h = 680

        draw.rounded_rectangle([panel_x, panel_y, panel_x + panel_w, panel_y + panel_h], radius=12, fill=COLOR_CODE_BG, outline=COLOR_BORDER, width=2)
        
        # Header bar for code box
        draw.line([(panel_x, panel_y + 45), (panel_x + panel_w, panel_y + 45)], fill=COLOR_BORDER, width=1)
        draw.ellipse([panel_x + 20, panel_y + 18, panel_x + 32, panel_y + 30], fill=(255, 95, 86))
        draw.ellipse([panel_x + 40, panel_y + 18, panel_x + 52, panel_y + 30], fill=(255, 189, 46))
        draw.ellipse([panel_x + 60, panel_y + 18, panel_x + 72, panel_y + 30], fill=(39, 201, 63))
        draw.text((panel_x + 90, panel_y + 12), slide.get("code_title", "Implementation Delta"), font=font_badge, fill=COLOR_MUTED)

        code_lines = slide["code"].strip().split("\n")
        code_y = panel_y + 65
        for i, code_line in enumerate(code_lines[:20]):
            draw.text((panel_x + 25, code_y), f"{i+1:02d}", font=font_code, fill=COLOR_MUTED)
            draw.text((panel_x + 70, code_y), code_line, font=font_code, fill=COLOR_TEXT)
            code_y += 30

    elif has_diagram:
        panel_x = 100 + left_w + 40
        panel_y = 260
        panel_w = WIDTH - panel_x - 100
        panel_h = 680
        draw.rounded_rectangle([panel_x, panel_y, panel_x + panel_w, panel_y + panel_h], radius=12, fill=COLOR_CARD, outline=COLOR_BORDER, width=2)
        try:
            diag_img = Image.open(slide["diagram"]).convert("RGBA")
            diag_img.thumbnail((panel_w - 40, panel_h - 40))
            offset_x = panel_x + (panel_w - diag_img.width) // 2
            offset_y = panel_y + (panel_h - diag_img.height) // 2
            img.paste(diag_img, (offset_x, offset_y), diag_img)
        except Exception as e:
            draw.text((panel_x + 40, panel_y + 40), f"Error loading diagram: {e}", font=font_body, fill=COLOR_MUTED)

    # Footer
    footer_text = "Antigravity & Omp Comprehension Engine • High-Velocity Architecture Verification"
    draw.text((100, HEIGHT - 70), footer_text, font=font_footer, fill=COLOR_MUTED)

    img.save(output_path, "PNG")


def get_best_video_encoder() -> str:
    """Detect the best available H.264 / video encoder in FFmpeg."""
    try:
        out = subprocess.check_output(["ffmpeg", "-encoders"], stderr=subprocess.DEVNULL, text=True)
        for enc in ["libopenh264", "libx264", "h264_nvenc", "h264_vaapi", "mpeg4"]:
            if enc in out:
                return enc
    except Exception:
        pass
    return "libopenh264"


def compose_video(manifest_path: Path, output_mp4: Path, audio_path: Optional[Path] = None):
    """Compose full MP4 video from manifest and optional audio."""
    with open(manifest_path, "r", encoding="utf-8") as f:
        data = json.load(f)

    slides = data.get("slides", [])
    if not slides:
        raise ValueError("Manifest contains no slides.")

    if not audio_path and "audio" in data and data["audio"]:
        audio_path = Path(data["audio"])

    total_duration = get_audio_duration(audio_path) if audio_path and audio_path.exists() else float(data.get("duration", len(slides) * 5.0))
    slide_duration = total_duration / len(slides)

    v_encoder = get_best_video_encoder()
    print(f"[VIDEO] Using video encoder: {v_encoder}")

    with tempfile.TemporaryDirectory() as tmpdir:
        tmp_dir = Path(tmpdir)
        slide_images = []

        for i, slide in enumerate(slides):
            img_path = tmp_dir / f"slide_{i:03d}.png"
            draw_slide(slide, i, len(slides), img_path)
            slide_images.append(img_path)

        # Create FFmpeg concat input list
        concat_file = tmp_dir / "input.txt"
        with open(concat_file, "w", encoding="utf-8") as f:
            for img_p in slide_images:
                f.write(f"file '{img_p.resolve()}'\n")
                f.write(f"duration {slide_duration:.2f}\n")
            # Repeat last file for ffmpeg concat requirement
            f.write(f"file '{slide_images[-1].resolve()}'\n")

        output_mp4.parent.mkdir(parents=True, exist_ok=True)

        ffmpeg_cmd = [
            "ffmpeg", "-y",
            "-f", "concat",
            "-safe", "0",
            "-i", str(concat_file),
        ]

        if audio_path and audio_path.exists():
            ffmpeg_cmd.extend(["-i", str(audio_path)])
            ffmpeg_cmd.extend([
                "-c:v", v_encoder,
                "-pix_fmt", "yuv420p",
                "-c:a", "aac",
                "-shortest",
                str(output_mp4)
            ])
        else:
            ffmpeg_cmd.extend([
                "-c:v", v_encoder,
                "-pix_fmt", "yuv420p",
                str(output_mp4)
            ])

        print(f"[VIDEO] Encoding 1080p MP4 with FFmpeg ({len(slides)} slides, {total_duration:.1f}s)...")
        try:
            res = subprocess.run(ffmpeg_cmd, check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        except subprocess.CalledProcessError as e:
            print(f"[ERROR] FFmpeg failed with code {e.returncode}:\n{e.stderr}", file=sys.stderr)
            raise
        print(f"[VIDEO] Successfully generated {output_mp4} ({output_mp4.stat().st_size // 1024} KB)")


def main():
    parser = argparse.ArgumentParser(description="Comprehension Engine Video Composer")
    parser.add_argument("--manifest", "-m", type=Path, required=True, help="Path to slide manifest JSON")
    parser.add_argument("--audio", "-a", type=Path, help="Narration audio file path (optional)")
    parser.add_argument("--output", "-o", type=Path, required=True, help="Output MP4 file path")
    args = parser.parse_args()

    compose_video(args.manifest, args.output, args.audio)


if __name__ == "__main__":
    main()
