#!/usr/bin/env python3
"""Find RIP-relative references to one or more RVAs in a PE .text section."""

from __future__ import annotations

import argparse
from pathlib import Path

from capstone import Cs, CS_ARCH_X86, CS_MODE_64
from capstone.x86 import X86_OP_MEM, X86_REG_RIP
import pefile


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
    text = next(
        section for section in pe.sections
        if section.Name.rstrip(b"\0") == b".text"
    )
    code = text.get_data()
    start = image_base + text.VirtualAddress
    disassembler = Cs(CS_ARCH_X86, CS_MODE_64)
    disassembler.detail = True
    for instruction in disassembler.disasm(code, start):
        for operand in instruction.operands:
            if operand.type != X86_OP_MEM or operand.mem.base != X86_REG_RIP:
                continue
            target = instruction.address + instruction.size + operand.mem.disp
            if target not in targets:
                continue
            rva = instruction.address - image_base
            target_rva = target - image_base
            print(
                f"{rva:08X} -> {target_rva:08X}  "
                f"{instruction.mnemonic} {instruction.op_str}"
            )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
