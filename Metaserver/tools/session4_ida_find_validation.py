"""
=============================================================================
Session 4: IDA Python — auto-locate validation branch in handler function

Purpose:
  Given a handler callback address (from Session 2/3), automatically:
    1. Locate the protobuf ParseFromArray / decode call
    2. Find all conditional branches AFTER the decode
    3. Classify branches as: validation-failure, success-path, or neutral
    4. Output the patch site for bypassing the validation

Usage in IDA Pro (File → Script file..., or Alt+F7):
  Set HANDLER_EA below, then run this script.

Prerequisite:
  IDA database for ProjectBoundary-Win64-Shipping.exe
  Handler RVA known (from Session 2)
=============================================================================
"""

import idaapi
import idc
import idautils
import ida_name
import ida_funcs
import ida_xref
import ida_hexrays

# ============================================================================
# CONFIG — set this after Session 2
# ============================================================================
HANDLER_EA = 0x0  # ← Replace with handler address from Session 2 (IDA effective address)
# ============================================================================

# Common UE / protobuf function name patterns
DECODE_NAMES = [
    'ParseFromArray', 'ParseFromString', 'MergeFromArray', 'MergeFromString',
    'InternalParse', 'ParseFrom', '_InternalParse', 'parse', 'decode',
    'ParseFromCodedStream', 'MergePartialFromCodedStream',
    'MessageLite',  # google::protobuf::MessageLite
    'protobuf', 'google',
]

# Error-related constants to look for
ERROR_PATTERNS = [
    0, 1, 3, 4, -1,  # common error codes
    'ErrorCode', 'StatusCode', 'ResultCode',
]

def is_decode_call(ea):
    """Heuristic: check if call target's name suggests protobuf decode."""
    target = idc.get_operand_value(ea, 0)
    name = idc.get_name(target, ida_name.GN_VISIBLE).lower()
    if not name:
        return False
    for pat in DECODE_NAMES:
        if pat.lower() in name:
            return True
    # Also check if target is an import thunk
    seg = idc.get_segm_name(target)
    if seg and 'extern' in seg.lower():
        return True
    return False

def find_decode_calls(func_ea):
    """Find all probable protobuf decode calls in the function."""
    results = []
    func = ida_funcs.get_func(func_ea)
    if not func:
        print(f"[!] No function at {func_ea:#x}")
        return results

    for head in idautils.Heads(func.start_ea, func.end_ea):
        if idc.print_insn_mnem(head) == 'call':
            if is_decode_call(head):
                target = idc.get_operand_value(head, 0)
                results.append({
                    'ea': head,
                    'target': target,
                    'target_name': idc.get_name(target, ida_name.GN_VISIBLE),
                })
    return results

def find_branches_after(ea, func_end_ea):
    """Find all conditional branches after a given EA, within the same function."""
    branches = []
    for head in idautils.Heads(ea, func_end_ea):
        mnem = idc.print_insn_mnem(head)
        if mnem.startswith('j') and mnem != 'jmp' and mnem != 'jrcxz':
            target = idc.get_operand_value(head, 0)
            branches.append({
                'ea': head,
                'mnem': mnem,
                'target': target,
                'is_loop': target < ea,  # backward jump = loop
            })
    return branches

def classify_branch(branch_ea, branch_target):
    """Try to classify a branch target as success or failure path."""
    # Check what's at the branch target
    # Failure paths often:
    #   - Set ErrorCode to a non-zero value
    #   - Jump to a block that returns 0 / nullptr
    #   - Log an error
    hints = []

    # Look at first ~10 instructions at target
    count = 0
    for head in idautils.Heads(branch_target, branch_target + 0x200):
        if count > 10:
            break
        count += 1
        mnem = idc.print_insn_mnem(head)

        # Check for immediate moves of small error-ish values
        if mnem == 'mov':
            op1 = idc.print_operand(head, 1)
            try:
                val = idc.get_operand_value(head, 1)
                if val in [1, 2, 3, 4, 0xFFFFFFFF, -1]:
                    hints.append(f"sets_value={val}")
            except:
                pass

        # Check for calls to error/log functions
        if mnem == 'call':
            target = idc.get_operand_value(head, 0)
            tname = idc.get_name(target, ida_name.GN_VISIBLE).lower()
            if any(w in tname for w in ['error', 'fail', 'log', 'warn', 'fatal']):
                hints.append(f"calls_error_func={tname}")

        # Check for return
        if mnem == 'retn' or mnem == 'ret':
            hints.append("returns_quickly")
            break

        # Check for xor eax, eax (return 0/false)
        if mnem == 'xor' and 'eax' in idc.print_operand(head, 0) and 'eax' in idc.print_operand(head, 1):
            hints.append("returns_zero")
            break

    return hints

def find_validation_site(func_ea):
    """Main analysis: locate the validation that rejects our response."""
    func = ida_funcs.get_func(func_ea)
    if not func:
        print(f"[!] No function at {func_ea:#x}")
        return

    print(f"\n{'='*60}")
    print(f"Analyzing handler: {ida_name.get_name(func_ea)} at {func_ea:#x}")
    print(f"Function range: {func.start_ea:#x} - {func.end_ea:#x}")
    print(f"{'='*60}\n")

    # Step 1: Find decode calls
    print("[1] Searching for protobuf decode calls...")
    decodes = find_decode_calls(func_ea)
    if not decodes:
        print("[!] No decode calls found by name heuristic.")
        print("[*] Listing ALL calls in function for manual inspection:")
        for head in idautils.Heads(func.start_ea, func.end_ea):
            if idc.print_insn_mnem(head) == 'call':
                target = idc.get_operand_value(head, 0)
                name = idc.get_name(target, ida_name.GN_VISIBLE)
                print(f"      {head:#x}: call {target:#x} ({name})")
        return

    print(f"[+] Found {len(decodes)} potential decode call(s):")
    for d in decodes:
        print(f"      {d['ea']:#x}: call {d['target_name']}")

    # Step 2: For each decode, find branches after it
    last_decode = decodes[-1]['ea']  # Usually the last decode is the inner message decode
    next_after_decode = idc.next_head(last_decode)

    print(f"\n[2] Branches after last decode ({last_decode:#x}):")
    branches = find_branches_after(next_after_decode, func.end_ea)

    if not branches:
        print("[!] No conditional branches found after decode.")
        return

    print(f"[+] Found {len(branches)} conditional branches:")
    for b in branches:
        direction = "<- BACKWARD (loop)" if b['is_loop'] else "-> FORWARD"
        hints = classify_branch(b['ea'], b['target'])
        hint_str = " | ".join(hints) if hints else "no obvious hints"

        # Highlight the target block
        block_start = b['target']
        # Try to find the block end
        block_end = block_start
        for h in idautils.Heads(block_start, block_start + 0x100):
            mnem = idc.print_insn_mnem(h)
            if mnem in ['jmp', 'retn', 'ret'] or mnem.startswith('j'):
                block_end = h
                break

        print(f"\n  {b['ea']:#x}:  {b['mnem']} {direction}  → {b['target']:#x}")
        print(f"      Target block: {b['target']:#x} - {block_end:#x}")
        print(f"      Hints: {hint_str}")

        # If target sets value=4 (UnknowError), flag it
        if 'sets_value=4' in hint_str:
            print(f"      *** FLAGGED: Sets ErrorCode=4 (UnknowError) — LIKELY VALIDATION FAILURE PATH ***")

    # Step 3: Print patch suggestions
    print(f"\n[3] Patch suggestions:")
    for b in branches:
        hints = classify_branch(b['ea'], b['target'])
        if 'sets_value=4' in str(hints) or 'calls_error_func' in str(hints) or 'returns_zero' in str(hints):
            print(f"\n  Likely validation failure at {b['ea']:#x}: {b['mnem']} → {b['target']:#x}")
            print(f"  Patch: NOP this branch  OR  invert the condition (change {b['mnem']} to opposite)")

    # Step 4: Try Hex-Rays decompilation if available
    print(f"\n[4] Decompiler view:")
    try:
        cfunc = ida_hexrays.decompile(func_ea)
        if cfunc:
            print("[+] Decompiled pseudocode available. Look for:")
            print("    - Comparisons after the protobuf ParseFrom* call")
            print("    - if (result != OK || data.size() < N) style checks")
            print("    - Assignments to ErrorCode / StatusCode fields")
            print(f"\n    Function: {cfunc}")
    except Exception as e:
        print(f"[!] Decompiler not available or failed: {e}")

    print(f"\n{'='*60}")
    print("Analysis complete.")
    print(f"{'='*60}\n")

# ============================================================================
# Entry point
# ============================================================================

if __name__ == "__main__":
    ea = HANDLER_EA
    if ea == 0x0:
        # Try to get from cursor position
        ea = idc.get_screen_ea()
        if ea == idc.BADADDR:
            print("[!] Set HANDLER_EA or place cursor on the handler function and re-run.")
        else:
            func = ida_funcs.get_func(ea)
            if func:
                ea = func.start_ea
            print(f"[*] Using cursor position: {ea:#x}")

    if ea and ea != idc.BADADDR:
        find_validation_site(ea)
    else:
        print("[!] No valid address to analyze.")
