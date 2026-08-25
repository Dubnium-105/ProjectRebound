#!/usr/bin/env python3
"""Find x64 instructions referencing an exact memory displacement in PE code."""

from __future__ import annotations

import argparse
from pathlib import Path
import struct

from capstone import Cs, CS_ARCH_X86, CS_MODE_64
from capstone.x86 import X86_OP_MEM
import pefile


def parse_integer(value: str) -> int:
    return int(value, 0)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("image", type=Path)
    parser.add_argument("displacement", type=parse_integer)
    args = parser.parse_args()

    pe = pefile.PE(str(args.image), fast_load=True)
    image_base = pe.OPTIONAL_HEADER.ImageBase
    disassembler = Cs(CS_ARCH_X86, CS_MODE_64)
    disassembler.detail = True
    needle = struct.pack("<I", args.displacement & 0xffffffff)
    matches: dict[int, object] = {}
    with args.image.open("rb") as stream:
        for section in pe.sections:
            name = section.Name.rstrip(b"\0")
            if name != b".text":
                continue
            stream.seek(section.PointerToRawData)
            code = stream.read(section.SizeOfRawData)
            search_from = 0
            while True:
                displacement_offset = code.find(needle, search_from)
                if displacement_offset < 0:
                    break
                search_from = displacement_offset + 1
                for instruction_start in range(
                    max(0, displacement_offset - 11), displacement_offset + 1
                ):
                    address = (
                        image_base + section.VirtualAddress + instruction_start
                    )
                    instructions = list(disassembler.disasm(
                        code[instruction_start:instruction_start + 15], address, 1
                    ))
                    if not instructions:
                        continue
                    instruction = instructions[0]
                    instruction_end = instruction_start + instruction.size
                    if not (
                        instruction_start <= displacement_offset and
                        displacement_offset + 4 <= instruction_end
                    ):
                        continue
                    if any(
                        operand.type == X86_OP_MEM and
                        operand.mem.disp == args.displacement
                        for operand in instruction.operands
                    ):
                        matches[instruction.address] = instruction

    for address in sorted(matches):
        instruction = matches[address]
        rva = instruction.address - image_base
        print(
            f"{rva:08X}  {instruction.bytes.hex(' '):<32}  "
            f"{instruction.mnemonic:<8} {instruction.op_str}"
        )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
