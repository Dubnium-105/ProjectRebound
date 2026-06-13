// =============================================================================
// Session 24: BYPASS "must be 2" check at machine code level
//
// sub_9C4840 at +0x1F (0x9C485F):
//   cmp [a4+31Ch], 2
//   jne 0x9C4861  ← return 0 (EARLY FAIL)
//
// We patch: jne → jmp (always skip past the early return 0)
// OR: jne → NOP NOP (fall through to success)
//
// Usage: frida -p <PID> -l tools/session24_patch_field.js
// =============================================================================

const BASE = (() => {
    for (const n of ["ProjectBoundarySteam-Win64-Shipping.exe","ProjectBoundary-Win64-Shipping.exe"])
        { try { return Process.getModuleByName(n).base; } catch(_){} }
    for (const m of Process.enumerateModules())
        { if (m.name.endsWith('.exe') && m.name.toLowerCase().includes('boundary')) return m.base; }
    throw new Error("Module not found");
})();

function hex(n) { return n instanceof NativePointer ? n.toString() : "0x" + n.toString(16); }

// Patch target: the conditional jump at +0x1F in sub_9C4840
// If the check fails, it jumps to return 0
// We change this to ALWAYS jump past the return 0 (unconditional jmp)
const PATCH_OFFSET = 0x9C485F;

const patchAddr = BASE.add(PATCH_OFFSET);
console.log(`[+] Patch target: ${patchAddr}`);

// Read original bytes
try {
    const orig = patchAddr.readByteArray(8);
    console.log(`[+] Original bytes: ${hexdump(orig, {offset:0,length:8,header:false,ansi:false})}`);
} catch (e) {
    console.log(`[!] Cannot read: ${e.message}`);
}

// Use Memory.patchCode to safely modify
try {
    Memory.patchCode(patchAddr, 6, function(code) {
        const writer = new X86Writer(code, { pc: patchAddr });
        // Original: cmp [a4+31Ch], 2; je success_path; ret
        // We replace the conditional jump (jne/je) with unconditional jmp to success
        // For now, just NOP the entire check + jump (6-7 bytes)
        // Write: jmp over the return-0
        // But we need to know the target. Let's just NOP the early return.

        // Alternative: write "xor eax, eax; nop; nop; nop; ret" → effectively return 0 too
        // Even better: just NOP the conditional jump bytes

        writer.putNopPadding(6); // NOP out the conditional jump
        writer.flush();
    });
    console.log(`[+] PATCHED: conditional jump NOP'd out.`);
    console.log(`[+] The check still happens but the result is ignored.`);
    console.log(`[*] Enter armory — validation bypassed!\n`);
} catch (e) {
    console.log(`[!] Patch failed: ${e.message}`);
}
