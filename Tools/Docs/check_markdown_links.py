#!/usr/bin/env python3
"""Validate repository-local links in Markdown files."""

from __future__ import annotations

import re
import sys
from pathlib import Path
from urllib.parse import unquote


REPOSITORY_ROOT = Path(__file__).resolve().parents[2]
SCAN_ROOTS = (
    REPOSITORY_ROOT / "README.md",
    REPOSITORY_ROOT / "README.zh-CN.md",
    REPOSITORY_ROOT / "docs",
    REPOSITORY_ROOT / "Backend",
    REPOSITORY_ROOT / "Desktop",
    REPOSITORY_ROOT / "Tools",
)
LINK_PATTERN = re.compile(r"(?<!!)\[[^\]]+\]\((?P<target>[^)]+)\)")
FENCED_CODE_PATTERN = re.compile(r"^```.*?^```\s*$", re.MULTILINE | re.DOTALL)
EXTERNAL_PREFIXES = ("http://", "https://", "mailto:", "#")


def markdown_files() -> list[Path]:
    files: list[Path] = []
    for root in SCAN_ROOTS:
        if root.is_file():
            files.append(root)
        elif root.is_dir():
            files.extend(root.rglob("*.md"))
    return sorted(set(files))


def local_target(raw_target: str) -> str | None:
    target = raw_target.strip().strip("<>")
    if target.startswith(EXTERNAL_PREFIXES):
        return None
    target = target.split("#", 1)[0]
    if not target:
        return None
    return unquote(target)


def main() -> int:
    broken: list[str] = []
    for markdown in markdown_files():
        content = markdown.read_text(encoding="utf-8")
        content = FENCED_CODE_PATTERN.sub("", content)
        for match in LINK_PATTERN.finditer(content):
            target = local_target(match.group("target"))
            if target is None:
                continue
            resolved = (markdown.parent / target).resolve()
            if not resolved.exists():
                source = markdown.relative_to(REPOSITORY_ROOT)
                broken.append(f"{source}: {target}")

    if broken:
        print("Broken local Markdown links:", file=sys.stderr)
        for item in broken:
            print(f"- {item}", file=sys.stderr)
        return 1

    print("MARKDOWN_LINKS_OK")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
