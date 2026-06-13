// =============================================================================
// Session 24: FINAL BYPASS — 1-byte patch
//
// sub_9C4840 at +0x1F (0x9C485F):
//   74 12  =  jz short loc_9C4873  (field==2 → skip return 0)
//
// Change 74 → EB:
//   EB 12  =  jmp short loc_9C4873 (ALWAYS skip return 0)
//
// 1 byte change, no hooks, no crashes.
//
// Usage: frida -p <PID> -l tools/session24_bypass_final.js
// =============================================================================

const BASE = (() => {
    for (const n of ["ProjectBoundarySteam-Win64-Shipping.exe","ProjectBoundary-Win64-Shipping.exe"])
        { try { return Process.getModuleByName(n).base; } catch(_){} }
    for (const m of Process.enumerateModules())
        { if (m.name.endsWith('.exe') && m.name.toLowerCase().includes('boundary')) return m.base; }
    throw new Error("Module not found");
})();

function hex(n) { return n instanceof NativePointer ? n.toString() : "0x" + n.toString(16); }

const PATCH_ADDR = BASE.add(0x9C485F); // jz short → jmp short

// Verify original byte
const orig = PATCH_ADDR.readU8();
console.log(`[+] Patch addr: ${PATCH_ADDR}`);
console.log(`[+] Original opcode: ${hex(orig)} ${orig === 0x74 ? '(jz — confirmed)' : '(UNEXPECTED!)'}`);

if (orig === 0x74) {
    // 0x74 (jz) → 0xEB (jmp)
    try {
        const page = PATCH_ADDR.and(ptr(0xFFFFFFFFFFFFF000));
        Memory.protect(page, 0x1000, 'rwx');
        PATCH_ADDR.writeU8(0xEB);
        const verify = PATCH_ADDR.readU8();
        console.log(`[+] PATCHED: 0x74→0xEB (verify: ${hex(verify)})`);
        console.log(`[+] Effect: ALWAYS skip return-0, always process archive data`);
        console.log(`[*] Enter armory — weapons should display correctly!\n`);
    } catch (e) {
        console.log(`[!] Patch failed: ${e.message}`);
    }
} else {
    console.log(`[!] Unexpected opcode at patch site. Aborting.`);
}
