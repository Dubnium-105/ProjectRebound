// =============================================================================
// Session 23: Find cache manager via sub_99E820 call chain
//
// sub_99E820 receives a4 = some struct. Walk a4 backwards to find the
// manager object. Also try to find global pointers in the binary.
//
// Usage: frida -p <PID> -l tools/session23_find_manager.js
// =============================================================================

const BASE = (() => {
    for (const n of ["ProjectBoundarySteam-Win64-Shipping.exe","ProjectBoundary-Win64-Shipping.exe"])
        { try { return Process.getModuleByName(n).base; } catch(_){} }
    for (const m of Process.enumerateModules())
        { if (m.name.endsWith('.exe') && m.name.toLowerCase().includes('boundary')) return m.base; }
    throw new Error("Module not found");
})();

function hex(n) { return n instanceof NativePointer ? n.toString() : "0x" + n.toString(16); }
function rva(p) { return p instanceof NativePointer ? hex(p.sub(BASE).toInt32()) : '?'; }

const lookupAddr = BASE.add(0x99E820);
let hit = 0;

Interceptor.attach(lookupAddr, {
    onEnter(args) {
        hit++;
        if (hit > 3) return;

        const a4 = args[3]; // 4th arg = struct with handler info
        console.log(`\n[sub_99E820 #${hit}] a4=${a4}`);

        // Dump a4 — this struct might contain or point to the cache manager
        try {
            console.log(`  a4 hex (96 bytes):`);
            console.log(hexdump(a4.readByteArray(96), {offset:0,length:96,header:false,ansi:false}));
        } catch (_) {}

        // Follow pointers in a4
        console.log(`  Pointers in a4:`);
        for (let off = 0; off < 0x60; off += 8) {
            try {
                const p = a4.add(off).readPointer();
                if (!p.isNull() && p.compare(BASE) > 0 && p.compare(ptr(0x100000000000)) < 0) {
                    const r = rva(p);
                    // Try to read what it points to
                    try {
                        const vt = p.readPointer();
                        const vr = rva(vt);
                        console.log(`    +${hex(off)}: ${p}  → ${vt} (RVA=${vr})`);
                    } catch (_) {
                        console.log(`    +${hex(off)}: ${p}`);
                    }
                }
            } catch (_) {}
        }

        // Try to find an object above a4 in memory that looks like a manager
        // Walk backwards and search for vtable + FName pattern
        console.log(`  Searching near a4 for manager objects...`);
        for (let delta = -0x200; delta < 0x200; delta += 0x10) {
            try {
                const candidate = a4.add(delta);
                const vt = candidate.readPointer();
                if (vt.isNull()) continue;
                const r = rva(vt);
                // Check if vtable is in game module
                if (r !== '?' && r.startsWith('0x')) {
                    // Read potential object name FName at +0x18
                    const fnIdx = candidate.add(0x18).readU32();
                    if (fnIdx > 0 && fnIdx < 50000) {
                        console.log(`    [${hex(delta)}] obj=${candidate}  vtRVA=${r}  FNameIdx=${fnIdx}`);
                    }
                }
            } catch (_) {}
        }
    }
});

// Also: try to find the cache by searching for specific FName patterns
// Known from session20: CID=16 (RoleConfig charID), CID=2
// These are FName ComparisonIndices. Search heap for these patterns.

function findCache() {
    console.log("[*] Scanning for archive cache pattern...");
    console.log("[*] Looking for structs containing FName(2), FName(5), FName(16) nearby...");

    // The cache entry structure is 120 bytes per role
    // Each entry has a RoleID FName at some offset
    // From sub_A49E10: entries are at v7+64+120*index

    // Instead of scanning, try to follow from known DisplayCharacters
    // DisplayCharacter RoleConfig has the cache data
    // The cache itself might be in CustomizeManager

    Process.enumerateRanges('rw-').forEach(function(range) {
        if (range.size < 0x10000) return;
        // Just sample a few pages
    });

    console.log("[!] Full heap scan skipped (too slow). Use IDA approach.");
}

console.log(`[*] Ready. Enter armory to trigger sub_99E820.`);
console.log(`[*] a4 contents and nearby objects will be dumped.\n`);
