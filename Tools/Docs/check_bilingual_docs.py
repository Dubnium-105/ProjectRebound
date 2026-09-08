#!/usr/bin/env python3
"""Validate every maintained English/Simplified Chinese Markdown pair."""

from __future__ import annotations

import re
import subprocess
import sys
from pathlib import Path
from urllib.parse import unquote


REPOSITORY_ROOT = Path(__file__).resolve().parents[2]
FENCE_PATTERN = re.compile(r"^```([^\r\n`]*)\r?$", re.MULTILINE)
FENCED_CODE_PATTERN = re.compile(r"^```.*?^```\s*$", re.MULTILINE | re.DOTALL)
HEADING_PATTERN = re.compile(r"^(#{1,6})\s+", re.MULTILINE)
LINK_PATTERN = re.compile(r"(?<!!)\[[^\]]+\]\((?P<target>[^)]+)\)")
CHINESE_ONLY_MARKER = "<!-- bilingual-doc: chinese-only -->"
GENERATED_PATH_PARTS = {
    ".git",
    ".agents",
    ".tmp",
    "artifacts",
    "build",
    "_deps",
    "node_modules",
    "dist",
    "target",
}
# These directories are immutable audit snapshots, not maintained product
# documentation. Their source bytes and language coverage are part of the
# historical evidence record and must not be rewritten to satisfy this check.
AUDIT_SNAPSHOT_ROOTS = (
    Path("docs/implementation/strict-roster-20260907"),
    Path("docs/testing/hardware-test-20260908"),
)


def chinese_sibling(english: Path) -> Path:
    return english.with_name(f"{english.stem}.zh-CN{english.suffix}")


def english_sibling(chinese: Path) -> Path:
    return chinese.with_name(chinese.name.removesuffix(".zh-CN.md") + ".md")


def is_chinese_only_handoff(chinese: Path) -> bool:
    """Allow explicitly marked Chinese-only handoff artifacts outside the archive."""
    if not chinese.name.endswith(".zh-CN.md"):
        return False
    header = "\n".join(chinese.read_text(encoding="utf-8").splitlines()[:8])
    return CHINESE_ONLY_MARKER in header


def is_excluded_document(relative: Path) -> bool:
    if any(part in GENERATED_PATH_PARTS for part in relative.parts):
        return True
    if any(
        relative == root or root in relative.parents for root in AUDIT_SNAPSHOT_ROOTS
    ):
        return True
    return relative.parts[:2] == ("docs", "archive")


def maintained_markdown_docs() -> list[Path]:
    """Return tracked or non-ignored Markdown under active maintenance.

    Git's index is the source of truth for maintained files. Including
    non-ignored working-tree files lets the check catch a new document before
    it is staged, while `--exclude-standard` keeps local package/build output
    out of the scan.
    """
    result = subprocess.run(
        ["git", "ls-files", "--cached", "--others", "--exclude-standard"],
        cwd=REPOSITORY_ROOT,
        check=True,
        capture_output=True,
        text=True,
    )
    documents: list[Path] = []
    for line in result.stdout.splitlines():
        relative = Path(line.strip())
        if relative.suffix.lower() != ".md" or is_excluded_document(relative):
            continue
        documents.append(REPOSITORY_ROOT / relative)
    return sorted(documents)


def local_target(markdown: Path, raw_target: str) -> Path | None:
    target = raw_target.strip().strip("<>").split("#", 1)[0]
    if not target or target.startswith(("http://", "https://", "mailto:", "#")):
        return None
    return (markdown.parent / unquote(target)).resolve()


def check_language_preserving_links(markdown: Path, text: str, errors: list[str]) -> None:
    without_code = FENCED_CODE_PATTERN.sub("", text)
    is_chinese = markdown.name.endswith(".zh-CN.md")
    own_translation = english_sibling(markdown) if is_chinese else chinese_sibling(markdown)
    for match in LINK_PATTERN.finditer(without_code):
        resolved = local_target(markdown, match.group("target"))
        if resolved is None or resolved == own_translation.resolve():
            continue
        if is_chinese:
            if resolved.suffix == ".md" and not resolved.name.endswith(".zh-CN.md"):
                localized = chinese_sibling(resolved)
                if localized.is_file():
                    errors.append(
                        f"Chinese document links to English despite available translation: "
                        f"{markdown.relative_to(REPOSITORY_ROOT)} -> {resolved.relative_to(REPOSITORY_ROOT)}"
                    )
        elif resolved.name.endswith(".zh-CN.md"):
            errors.append(
                f"English document links to Chinese translation: "
                f"{markdown.relative_to(REPOSITORY_ROOT)} -> {resolved.relative_to(REPOSITORY_ROOT)}"
            )


def main() -> int:
    errors: list[str] = []
    documents = maintained_markdown_docs()
    english_documents = [
        path for path in documents if not path.name.endswith(".zh-CN.md")
    ]
    expected_chinese = {chinese_sibling(path) for path in english_documents}

    for english in english_documents:
        chinese = chinese_sibling(english)
        relative_english = english.relative_to(REPOSITORY_ROOT)
        relative_chinese = chinese.relative_to(REPOSITORY_ROOT)
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
            errors.append(f"missing or misplaced language switch in {relative_english}")
        if expected_chinese_switch not in chinese_header:
            errors.append(f"missing or misplaced language switch in {relative_chinese}")

        if FENCE_PATTERN.findall(english_text) != FENCE_PATTERN.findall(chinese_text):
            errors.append(f"fenced-code structure differs: {relative_english} / {relative_chinese}")
        english_headings = [len(level) for level in HEADING_PATTERN.findall(english_text)]
        chinese_headings = [len(level) for level in HEADING_PATTERN.findall(chinese_text)]
        if english_headings != chinese_headings:
            errors.append(f"heading structure differs: {relative_english} / {relative_chinese}")

        check_language_preserving_links(english, english_text, errors)
        check_language_preserving_links(chinese, chinese_text, errors)

    localized_documents = {
        path for path in documents if path.name.endswith(".zh-CN.md")
    }
    for chinese in sorted(localized_documents - expected_chinese):
        if is_chinese_only_handoff(chinese):
            continue
        errors.append(
            f"orphan Simplified Chinese document: {chinese.relative_to(REPOSITORY_ROOT)}"
        )

    if errors:
        print("Bilingual documentation errors:", file=sys.stderr)
        for error in errors:
            print(f"- {error}", file=sys.stderr)
        return 1

    print(f"BILINGUAL_DOCS_OK pairs={len(english_documents)}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
