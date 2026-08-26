#!/usr/bin/env python3
"""Disassemble a small RVA window from a PE image with Capstone."""

from __future__ import annotations

import argparse
from pathlib import Path

from capstone import Cs, CS_ARCH_X86, CS_MODE_64
import pefile


def parse_integer(value: str) -> int:
    return int(value, 0)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("image", type=Path)
    parser.add_argument("rva", type=parse_integer)
    parser.add_argument("--size", type=parse_integer, default=0x400)
    args = parser.parse_args()

    pe = pefile.PE(str(args.image), fast_load=True)
    offset = pe.get_offset_from_rva(args.rva)
    with args.image.open("rb") as stream:
        stream.seek(offset)
        code = stream.read(args.size)

    disassembler = Cs(CS_ARCH_X86, CS_MODE_64)
    disassembler.detail = True
    address = pe.OPTIONAL_HEADER.ImageBase + args.rva
    for instruction in disassembler.disasm(code, address):
        rva = instruction.address - pe.OPTIONAL_HEADER.ImageBase
        raw = instruction.bytes.hex(" ")
        print(
            f"{rva:08X}  {raw:<32}  "
            f"{instruction.mnemonic:<8} {instruction.op_str}"
        )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
