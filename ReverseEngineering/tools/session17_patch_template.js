// =============================================================================
// Session 17: Patch the ErrorCode template at xmmword_41CB330
//
// sub_A49E10 uses _mm_load_si128(xmmword_41CB330) to init entries.
// If this template contains ErrorCode=4, we patch it to 0 at runtime.
//
// Usage: frida -p <PID> -l tools/session17_patch_template.js
// =============================================================================

const BASE = (() => {
    for (const n of ["ProjectBoundarySteam-Win64-Shipping.exe","ProjectBoundary-Win64-Shipping.exe"])
        { try { return Process.getModuleByName(n).base; } catch(_){} }
    for (const m of Process.enumerateModules())
        { if (m.name.endsWith('.exe') && m.name.toLowerCase().includes('boundary')) return m.base; }
    throw new Error("Module not found");
})();

const TEMPLATE_RVA = 0x41CB330;
const templateAddr = BASE.add(TEMPLATE_RVA);

console.log(`[+] Template @ ${templateAddr}`);

// Read template
try {
    console.log(`[+] Original 32 bytes:`);
    console.log(hexdump(templateAddr.readByteArray(32), {offset:0,length:32,header:false,ansi:false}));
} catch (e) {
    console.log(`[!] Read failed: ${e.message}`);
}

// Search for and patch ErrorCode=4 → 0
// The struct has ErrorCode at some offset, looking for 04 00 00 00
let patched = 0;
try {
    const bytes = templateAddr.readByteArray(64);
    const arr = new Uint8Array(bytes);
    for (let i = 0; i < arr.length - 3; i++) {
        if (arr[i] === 4 && arr[i+1] === 0 && arr[i+2] === 0 && arr[i+3] === 0) {
            console.log(`[+] Found ErrorCode=4 @ +${i.toString(16)} → patching to 0`);
            try {
                templateAddr.add(i).writeU32(0);
                patched++;
            } catch (e) {
                console.log(`[!] Write failed: ${e.message}`);
                // Try with Memory.protect
                const page = templateAddr.and(ptr(0xFFFFFFFFFFFFF000));
                Memory.protect(page, 0x1000, 'rwx');
                templateAddr.add(i).writeU32(0);
                patched++;
            }
        }
    }
    console.log(`[+] Patched ${patched} occurrences`);
    console.log(`[+] Patched bytes:`);
    console.log(hexdump(templateAddr.readByteArray(32), {offset:0,length:32,header:false,ansi:false}));
} catch (e) {
    console.log(`[!] Error: ${e.message}`);
}

console.log(`\n[*] Template patched. Re-enter armory to test.`);
console.log(`[*] If weapons display correctly, ROOT CAUSE CONFIRMED.\n`);
