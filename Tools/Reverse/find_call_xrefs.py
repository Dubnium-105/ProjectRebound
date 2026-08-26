#!/usr/bin/env python3
"""Find direct x64 call/jump references to target RVAs in PE code."""

from __future__ import annotations

import argparse
from pathlib import Path

import pefile
import struct


def parse_integer(value: str) -> int:
    return int(value, 0)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("image", type=Path)
    parser.add_argument("rvas", nargs="+", type=parse_integer)
    args = parser.parse_args()

    pe = pefile.PE(str(args.image), fast_load=True)
    image_base = pe.OPTIONAL_HEADER.ImageBase
    targets = {image_base + rva for rva in args.rvas}
    text_section = next(
        section for section in pe.sections
        if section.Name.rstrip(b"\0") == b".text"
    )
    code = text_section.get_data()
    start = image_base + text_section.VirtualAddress
    for offset in range(0, len(code) - 4):
        opcode = code[offset]
        if opcode not in (0xE8, 0xE9):
            continue
        address = start + offset
        target = address + 5 + struct.unpack_from("<i", code, offset + 1)[0]
        if target not in targets:
            continue
        print(
            f"{address - image_base:08X} -> "
            f"{target - image_base:08X}  "
            f"{'call' if opcode == 0xE8 else 'jmp'}"
        )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
