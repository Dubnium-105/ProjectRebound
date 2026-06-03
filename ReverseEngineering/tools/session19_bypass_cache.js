// =============================================================================
// Session 19: Bypass "field must be 2" check in GetPlayerArchiveV2 path
//
// sub_A49E10 checks: if (*(v7+72) == *(v7+116)) goto fail (empty cache)
// We hook entry, read both values. If equal, set them different.
// Also: try to find and set the "must be 2" field if it exists.
//
// Usage: frida -p <PID> -l tools/session19_bypass_cache.js
// =============================================================================

const BASE = (() => {
    for (const n of ["ProjectBoundarySteam-Win64-Shipping.exe","ProjectBoundary-Win64-Shipping.exe"])
        { try { return Process.getModuleByName(n).base; } catch(_){} }
    for (const m of Process.enumerateModules())
        { if (m.name.endsWith('.exe') && m.name.toLowerCase().includes('boundary')) return m.base; }
    throw new Error("Module not found");
})();

function hex(n) { return n instanceof NativePointer ? n.toString() : "0x" + n.toString(16); }

// sub_A49E10: a4(r9) = v7 (the response context / cache manager)
const processorAddr = BASE.add(0xA49E10);
let hitCount = 0;
const fixes = [];

Interceptor.attach(processorAddr, {
    onEnter(args) {
        hitCount++;
        const v7 = args[3]; // a4 = r9 = v7

        if (hitCount <= 5) {
            console.log(`\n[sub_A49E10 #${hitCount}] v7(a4)=${v7}`);

            try {
                const v72 = v7.add(0x48).readU32();  // v7+72 = +0x48
                const v116 = v7.add(0x74).readU32(); // v7+116 = +0x74
                const equals = v72 === v116;
                console.log(`  +0x48(readPtr)=${v72}  +0x74(writePtr)=${v116}  equal=${equals} ${equals ? '← CACHE EMPTY!' : '← OK'}`);

                // Also dump surrounding fields
                console.log(`  v7 hex (first 0x80):`);
                console.log(hexdump(v7.readByteArray(0x80), {offset:0,length:0x80,header:false,ansi:false}));
            } catch (e) {
                console.log(`  read error: ${e.message}`);
            }

            // Try to find ANY field that looks like a "must be 2" flag
            try {
                console.log(`  Scanning v7 for fields matching 2...`);
                for (let off = 0; off < 0x200; off += 4) {
                    try {
                        const v = v7.add(off).readU32();
                        // Look for fields with value 2, 3, or similar state values
                        if (v === 1 || v === 2 || v === 3 || v === 7) {
                            console.log(`    +${hex(off)}: ${v}`);
                        }
                    } catch (_) {}
                }
            } catch (_) {}
        }

        // --- ATTEMPT FIX: break the equality ---
        try {
            const v72 = v7.add(0x48).readU32();
            const v116 = v7.add(0x74).readU32();

            if (v72 === v116) {
                // Make writePtr ≠ readPtr to pretend cache has data
                v7.add(0x74).writeU32(v116 + 1);
                const fixInfo = { hit: hitCount, v7: v7.toString(), oldRead: v72, oldWrite: v116 };
                fixes.push(fixInfo);
                if (fixes.length <= 3) {
                    console.log(`[FIX #${fixes.length}] Cache empty → forced writePtr+1`);
                    console.log(`  readPtr=${v72} writePtr: ${v116} → ${v116+1}`);
                }
            }
        } catch (e) {
            console.log(`[!] Fix failed: ${e.message}`);
        }
    },
    onLeave(retval) {
        // Check result
        if (hitCount <= 3) {
            console.log(`  → sub_A49E10 returned: ${retval}`);
        }
    }
});

function stats() {
    console.log(`\nsub_A49E10 hits: ${hitCount}`);
    console.log(`Cache bypasses attempted: ${fixes.length}`);
    for (const f of fixes.slice(0, 10)) {
        console.log(`  #${f.hit}: v7=${f.v7} writePtr ${f.oldWrite}→${f.oldWrite+1}`);
    }
}

console.log(`[+] Hooked sub_A49E10 @ ${processorAddr}`);
console.log(`[*] Enter armory — cache will be bypassed.`);
console.log(`[*] stats() to see results.\n`);
