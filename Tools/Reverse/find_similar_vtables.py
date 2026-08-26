#!/usr/bin/env python3
"""Rank PE vtable candidates by same-slot similarity to a known vtable."""

from __future__ import annotations

import argparse
from pathlib import Path
import struct

import pefile


def parse_integer(value: str) -> int:
    return int(value, 0)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("image", type=Path)
    parser.add_argument("known_vtable_rva", type=parse_integer)
    parser.add_argument("--bytes", type=parse_integer, default=0x718)
    parser.add_argument("--minimum-matches", type=int, default=48)
    parser.add_argument("--limit", type=int, default=40)
    args = parser.parse_args()

    pe = pefile.PE(str(args.image), fast_load=True)
    image_base = pe.OPTIONAL_HEADER.ImageBase
    image_end = image_base + pe.OPTIONAL_HEADER.SizeOfImage
    code_start = image_base + pe.OPTIONAL_HEADER.BaseOfCode
    code_end = code_start + pe.OPTIONAL_HEADER.SizeOfCode
    known_offset = pe.get_offset_from_rva(args.known_vtable_rva)
    entry_count = args.bytes // 8

    with args.image.open("rb") as stream:
        image = stream.read()
    known = struct.unpack_from(f"<{entry_count}Q", image, known_offset)

    candidates: list[tuple[int, int, int, int, int]] = []
    known_slots: dict[int, list[int]] = {}
    for index, value in enumerate(known):
        known_slots.setdefault(value, []).append(index)
    # A shared no-op thunk appears in many UE vtables and is a poor anchor.
    known_slots = {
        value: indices
        for value, indices in known_slots.items()
        if code_start <= value < code_end and len(indices) <= 4
    }
    for section in pe.sections:
        name = section.Name.rstrip(b"\0").decode("ascii", errors="replace")
        if name not in (".rdata", ".data"):
            continue
        raw_start = section.PointerToRawData
        raw_end = raw_start + section.SizeOfRawData
        inferred_starts: dict[int, int] = {}
        for offset in range(raw_start, raw_end - 7, 8):
            value = struct.unpack_from("<Q", image, offset)[0]
            for known_index in known_slots.get(value, ()):
                start = offset - known_index * 8
                if raw_start <= start <= raw_end - args.bytes:
                    inferred_starts[start] = inferred_starts.get(start, 0) + 1

        for offset, anchor_matches in inferred_starts.items():
            if anchor_matches < args.minimum_matches:
                continue
            values = struct.unpack_from(f"<{entry_count}Q", image, offset)
            matches = sum(left == right for left, right in zip(known, values))
            if matches < args.minimum_matches:
                continue
            rva = section.VirtualAddress + offset - raw_start
            slot_708 = values[0x708 // 8]
            slot_710 = values[0x710 // 8]
            candidates.append((matches, rva, slot_708, slot_710, entry_count))

    candidates.sort(reverse=True)
    for matches, rva, slot_708, slot_710, total in candidates[: args.limit]:
        print(
            f"vtable_rva=0x{rva:X} matches={matches}/{total} "
            f"slot_708_rva=0x{slot_708 - image_base:X} "
            f"slot_710_rva=0x{slot_710 - image_base:X}"
        )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
