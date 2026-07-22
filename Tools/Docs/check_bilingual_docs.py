#!/usr/bin/env python3
"""Validate registered English/Simplified Chinese Markdown pairs."""

from __future__ import annotations

import re
import sys
from pathlib import Path


REPOSITORY_ROOT = Path(__file__).resolve().parents[2]
REGISTRY = REPOSITORY_ROOT / "docs" / "bilingual-docs.txt"
FENCE_PATTERN = re.compile(r"^```([^\r\n`]*)\r?$", re.MULTILINE)
HEADING_PATTERN = re.compile(r"^(#{1,6})\s+", re.MULTILINE)


def chinese_sibling(english: Path) -> Path:
    return english.with_name(f"{english.stem}.zh-CN{english.suffix}")


def registered_paths() -> list[Path]:
    paths: list[Path] = []
    for raw_line in REGISTRY.read_text(encoding="utf-8").splitlines():
        line = raw_line.strip()
        if not line or line.startswith("#"):
            continue
        path = Path(line)
        if path.is_absolute() or ".." in path.parts or path.suffix != ".md":
            raise ValueError(f"invalid registry entry: {line}")
        paths.append(path)
    return paths


def main() -> int:
    errors: list[str] = []
    try:
        entries = registered_paths()
    except ValueError as error:
        print(f"BILINGUAL_DOCS_INVALID: {error}", file=sys.stderr)
        return 1

    if len(entries) != len(set(entries)):
        errors.append("docs/bilingual-docs.txt contains duplicate entries")

    registered_english = set(entries)
    registered_chinese: set[Path] = set()

    for relative_english in entries:
        relative_chinese = chinese_sibling(relative_english)
        registered_chinese.add(relative_chinese)
        english = REPOSITORY_ROOT / relative_english
        chinese = REPOSITORY_ROOT / relative_chinese

        if not english.is_file():
            errors.append(f"missing English document: {relative_english}")
            continue
        if not chinese.is_file():
            errors.append(f"missing Simplified Chinese document: {relative_chinese}")
            continue

        english_text = english.read_text(encoding="utf-8")
        chinese_text = chinese.read_text(encoding="utf-8")
        english_header = "\n".join(english_text.splitlines()[:8])
        chinese_header = "\n".join(chinese_text.splitlines()[:8])
        expected_english_switch = f"English | [简体中文]({relative_chinese.name})"
        expected_chinese_switch = f"[English]({relative_english.name}) | 简体中文"

        if expected_english_switch not in english_header:
            errors.append(
                f"missing or misplaced language switch in {relative_english}"
            )
        if expected_chinese_switch not in chinese_header:
            errors.append(
                f"missing or misplaced language switch in {relative_chinese}"
            )

        english_fences = FENCE_PATTERN.findall(english_text)
        chinese_fences = FENCE_PATTERN.findall(chinese_text)
        if english_fences != chinese_fences:
            errors.append(
                f"fenced-code structure differs: {relative_english} / {relative_chinese}"
            )

        english_headings = [len(level) for level in HEADING_PATTERN.findall(english_text)]
        chinese_headings = [len(level) for level in HEADING_PATTERN.findall(chinese_text)]
        if english_headings != chinese_headings:
            errors.append(
                f"heading structure differs: {relative_english} / {relative_chinese}"
            )

    docs_root = REPOSITORY_ROOT / "docs"
    for chinese in docs_root.rglob("*.zh-CN.md"):
        relative = chinese.relative_to(REPOSITORY_ROOT)
        if relative not in registered_chinese:
            errors.append(f"unregistered Simplified Chinese document: {relative}")

    for readme in docs_root.rglob("README.md"):
        relative = readme.relative_to(REPOSITORY_ROOT)
        if "archive" in relative.parts:
            continue
        if relative not in registered_english:
            errors.append(f"unregistered maintained documentation entry: {relative}")

    if errors:
        print("Bilingual documentation errors:", file=sys.stderr)
        for error in errors:
            print(f"- {error}", file=sys.stderr)
        return 1

    print(f"BILINGUAL_DOCS_OK pairs={len(entries)}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
