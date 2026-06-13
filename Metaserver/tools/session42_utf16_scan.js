// =============================================================================
// Session 42: Brute-force UTF-16LE scan for weapon IDs
//
// UE4 FString on Windows = UTF-16LE. The metaserver weapon IDs like
// "PEACE_GSW-AR" must exist somewhere in memory — in FString form,
// in loaded DataTable assets, network buffers, or config data.
//
// Also scan for UTF-8 (narrow string) and FName header format.
// =============================================================================

const BASE = Process.getModuleByName("ProjectBoundarySteam-Win64-Shipping.exe").base;
function hex(n) { return n instanceof NativePointer ? n.toString() : "0x" + n.toString(16); }

// Weapon IDs from metaserver loadout responses
const WEAPON_IDS = [
    "PEACE_GSW-AR",
    "PROBE_RU-AKM",
    "SNIPER_RU-MOSIN",
    "SUBMACHINE_GUN_MP7",
    "MISSILE_GUIDED",
    "EMPYREAN_EXO",
];

// Additional IDs that might be in cache
const EXTRA_IDS = [
    "ASSAULTRIFLE",
    "PISTOL",
    "MELEE",
    "GRENADE",
    "SCOPE",
    "MUZZLE",
    "MAGAZINE",
];

console.log(`[+] BASE = ${BASE}`);
console.log(`[*] Scanning for weapon IDs in UTF-16LE, UTF-8, and FName formats...\n`);

// ---------------------------------------------------------------------------
// Helper: create UTF-16LE bytes from ASCII string
// ---------------------------------------------------------------------------
function toUtf16LE(str) {
    const bytes = [];
    for (let i = 0; i < str.length; i++) {
        const c = str.charCodeAt(i);
        bytes.push(c & 0xFF, (c >> 8) & 0xFF);
    }
    return bytes;
}

// ---------------------------------------------------------------------------
// Scan all readable memory for each weapon ID in UTF-16LE format
// ---------------------------------------------------------------------------
let totalHits = 0;
const foundLocations = [];

Process.enumerateRanges('r--').forEach(function(range) {
    if (totalHits > 50) return;
    if (range.size < 0x100) return;

    // Skip very large ranges (mapped files)
    if (range.size > 0x40000000) return; // > 1GB

    const lo = range.base.and(ptr(0xFFFFFFFF)).toUInt32();
    // Include all memory, including module ranges
    if (lo < 0x10000) return; // skip null page

    for (const wid of WEAPON_IDS) {
        if (totalHits > 50) return;
        const utf16bytes = toUtf16LE(wid);
        const pattern = utf16bytes.map(b => b.toString(16).padStart(2, '0')).join(' ');

        try {
            Memory.scan(range.base, range.size, pattern, {
                onMatch(addr) {
                    totalHits++;
                    const isModule = addr.compare(BASE) > 0 && addr.compare(BASE.add(0x30000000)) < 0;
                    const loc = {
                        addr,
                        id: wid,
                        format: 'UTF-16LE',
                        inModule: isModule,
                    };
                    foundLocations.push(loc);

                    console.log(`[#${totalHits}] "${wid}" @ ${addr} ${isModule ? '(MODULE)' : '(HEAP)'}`);

                    // Show context
                    try {
                        const ctxStart = addr.sub(Math.min(32, addr.toInt32()));
                        console.log(`  ${hexdump(ctxStart.readByteArray(80), {offset:0,length:80,header:false,ansi:true})}`);
                    } catch(e) {}

                    if (totalHits > 50) return 'stop';
                },
                onComplete() {},
                onError(e) {}
            });
        } catch(e) {}
    }
});

console.log(`\n[+] Total UTF-16LE hits: ${totalHits}`);

// ---------------------------------------------------------------------------
// Also scan for narrow (ASCII/UTF-8) strings
// ---------------------------------------------------------------------------
console.log(`\n[*] Scanning for narrow (ASCII) weapon IDs...`);

let narrowHits = 0;
Process.enumerateRanges('r--').forEach(function(range) {
    if (narrowHits > 30) return;
    if (range.size < 0x100 || range.size > 0x40000000) return;
    const lo = range.base.and(ptr(0xFFFFFFFF)).toUInt32();
    if (lo < 0x10000) return;

    for (const wid of WEAPON_IDS) {
        if (narrowHits > 30) return;
        const narrowBytes = [];
        for (let i = 0; i < wid.length; i++) narrowBytes.push(wid.charCodeAt(i));
        const pattern = narrowBytes.map(b => b.toString(16).padStart(2, '0')).join(' ');

        try {
            Memory.scan(range.base, range.size, pattern, {
                onMatch(addr) {
                    narrowHits++;
                    const isModule = addr.compare(BASE) > 0 && addr.compare(BASE.add(0x30000000)) < 0;
                    // Only log if NOT already found as UTF-16
                    const alreadyFound = foundLocations.some(l => l.id === wid && l.addr.equals(addr));
                    if (!alreadyFound) {
                        console.log(`[N#${narrowHits}] "${wid}" @ ${addr} ${isModule ? '(MODULE)' : '(HEAP)'}`);
                        try {
                            console.log(`  ${hexdump(addr.sub(16).readByteArray(64), {offset:0,length:64,header:false,ansi:true})}`);
                        } catch(e) {}
                    }
                    if (narrowHits > 30) return 'stop';
                },
                onComplete() {},
                onError(e) {}
            });
        } catch(e) {}
    }
});

console.log(`\n[+] Narrow hits: ${narrowHits}`);

// ---------------------------------------------------------------------------
// Summary
// ---------------------------------------------------------------------------
console.log(`\n=== SUMMARY ===`);
console.log(`UTF-16LE locations: ${foundLocations.length}`);
// Group by address range
const groups = {};
for (const loc of foundLocations) {
    // Group by 1MB alignment
    const group = loc.addr.and(ptr(0xFFFFFFFFFFF00000)).toString();
    if (!groups[group]) groups[group] = [];
    groups[group].push(loc);
}
console.log(`\nLocations grouped by 1MB region:`);
for (const [region, locs] of Object.entries(groups)) {
    const ids = [...new Set(locs.map(l => l.id))];
    console.log(`  ${region}: ${locs.length} hits, IDs: ${ids.join(', ')}`);
    for (const l of locs.slice(0, 3)) {
        console.log(`    ${l.id} @ ${l.addr} ${l.inModule ? '(MODULE)' : '(HEAP)'}`);
    }
}

console.log(`\n[*] Use inspect(addr) to dump memory around any hit\n`);
