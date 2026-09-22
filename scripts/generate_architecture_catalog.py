#!/usr/bin/env python3
"""Generate and verify the OMP architecture-section catalog.

The catalog is deterministic metadata for candidate retrieval. An agent or a
structured evaluator selects entries from it, then reads the authoritative
source ranges; the catalog is never a replacement for source material.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import re
import sys
from dataclasses import dataclass
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SOURCE = ROOT / "ARCHITECTURE.md"
CATALOG = ROOT / ".omp" / "architecture-catalog.json"
HEADING = re.compile(r"^(#{2,4})\s+(.+?)\s*$")
WHITESPACE = re.compile(r"\s+")
MAX_DESCRIPTION_LENGTH = 320


class CatalogError(Exception):
    pass


@dataclass(frozen=True)
class Section:
    level: int
    line: int
    heading: str
    parent_id: str | None

    @property
    def id(self) -> str:
        return f"ARCH-{self.line:04d}"


def read_sections(lines: list[str]) -> list[Section]:
    sections: list[Section] = []
    parents: dict[int, Section] = {}
    for line_number, line in enumerate(lines, start=1):
        match = HEADING.match(line)
        if not match:
            continue
        level = len(match.group(1))
        while parents and max(parents) >= level:
            del parents[max(parents)]
        parent = parents.get(level - 1)
        section = Section(
            level=level,
            line=line_number,
            heading=match.group(2),
            parent_id=parent.id if parent else None,
        )
        sections.append(section)
        parents[level] = section
    if not sections:
        raise CatalogError(f"no level-2 through level-4 headings found in {SOURCE.relative_to(ROOT)}")
    return sections


def end_line(section: Section, sections: list[Section], line_count: int) -> int:
    for candidate in sections:
        if candidate.line > section.line and candidate.level <= section.level:
            return candidate.line - 1
    return line_count


def description(section: Section, lines: list[str], sections: list[Section]) -> str:
    last_line = end_line(section, sections, len(lines))
    prose: list[str] = []
    bullets: list[str] = []
    in_fence = False
    for line in lines[section.line:last_line]:
        stripped = line.strip()
        if stripped.startswith("```"):
            in_fence = not in_fence
            continue
        if in_fence:
            continue
        if not stripped:
            if bullets:
                break
            # Keep going when a colon lead-in is waiting for its bullet list.
            if prose and not prose[-1].endswith(":"):
                break
            continue
        if stripped.startswith("#"):
            # Nested headings belong to child sections, not this description.
            break
        if stripped.startswith("|"):
            continue
        bullet = None
        if stripped.startswith(("- ", "* ")):
            bullet = stripped[2:].strip()
        elif re.match(r"^\d+\.\s+", stripped):
            bullet = re.sub(r"^\d+\.\s+", "", stripped).strip()
        if bullet is not None:
            if prose and prose[-1].endswith(":") and len(bullets) < 6:
                bullets.append(bullet.rstrip(";."))
            continue
        if bullets:
            break
        prose.append(stripped)
    if not prose and not bullets:
        return section.heading
    parts = list(prose)
    if bullets:
        parts.append("; ".join(bullets))
    text = WHITESPACE.sub(" ", " ".join(parts))
    return text[:MAX_DESCRIPTION_LENGTH].rstrip()


def tags(section: Section) -> list[str]:
    terms = re.findall(r"[a-z0-9]+", section.heading.lower())
    return sorted(set(terms))


def build_catalog() -> dict[str, object]:
    source_text = SOURCE.read_text(encoding="utf-8")
    lines = source_text.splitlines()
    sections = read_sections(lines)
    source_path = SOURCE.relative_to(ROOT).as_posix()
    return {
        "version": 1,
        "purpose": "Candidate retrieval metadata. Read the source range before relying on an entry.",
        "sources": [
            {
                "path": source_path,
                "sha256": hashlib.sha256(source_text.encode("utf-8")).hexdigest(),
            }
        ],
        "entries": [
            {
                "id": section.id,
                "path": source_path,
                "lineStart": section.line,
                "lineEnd": end_line(section, sections, len(lines)),
                "heading": section.heading,
                "level": section.level,
                "parentId": section.parent_id,
                "description": description(section, lines, sections),
                "authority": "architecture",
                "tags": tags(section),
            }
            for section in sections
        ],
    }


def render(catalog: dict[str, object]) -> str:
    return json.dumps(catalog, indent=2, sort_keys=True) + "\n"


def main() -> int:
    parser = argparse.ArgumentParser(description="Generate the OMP architecture-section catalog")
    parser.add_argument("--check", action="store_true", help="fail if the committed catalog is stale")
    args = parser.parse_args()

    try:
        expected = render(build_catalog())
    except (CatalogError, OSError) as exc:
        print(f"CATALOG_FAILED {exc}", file=sys.stderr)
        return 1

    if args.check:
        try:
            actual = CATALOG.read_text(encoding="utf-8")
        except OSError:
            actual = ""
        if actual != expected:
            print("CATALOG_STALE .omp/architecture-catalog.json", file=sys.stderr)
            return 1
        print("CATALOG_OK architecture-catalog")
        return 0

    CATALOG.write_text(expected, encoding="utf-8")
    print("CATALOG_WRITTEN .omp/architecture-catalog.json")
    return 0


if __name__ == "__main__":
    sys.exit(main())
