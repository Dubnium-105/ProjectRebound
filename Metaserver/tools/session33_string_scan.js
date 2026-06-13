// =============================================================================
// Session 33: Scan for loadout cache using STRING weapon IDs
//
// Search memory for metaserver-returned weapon ID strings.
// If we find clusters of weapon IDs, we've found the cache.
//
// Usage: frida -p <PID> -l tools/session33_string_scan.js
// =============================================================================

function hex(n) { return n instanceof NativePointer ? n.toString() : "0x" + n.toString(16); }

// Get these from metaserver log — the loadout data returned
const WEAPON_IDS = [
    "SNIPER_RU-MOSIN",
    "SUBMACHINE_GUN_MP7",
    "MISSILE_GUIDED",
    "EMPYREAN_EXO",
];

console.log(`[*] Searching for weapon ID strings: ${WEAPON_IDS.join(', ')}`);
console.log(`[*] Scan running...`);

let found = {};
let totalHits = 0;

Process.enumerateRanges('rw-').forEach(function(range) {
    if (totalHits > 50) return;
    if (range.size < 0x1000) return;
    const lo = range.base.and(ptr(0xFFFFFFFF)).toUInt32();
    if (lo < 0x10000000) return;

    for (let p = range.base; p.compare(range.base.add(range.size)) < 0 && totalHits < 50; p = p.add(0x10000)) {
        try {
            const buf = p.readByteArray(0x1000);
            if (!buf) continue;
            const dv = new DataView(buf);

            for (const wid of WEAPON_IDS) {
                // Scan for UTF-8 encoded weapon ID
                const bytes = [];
                for (let i = 0; i < wid.length; i++) bytes.push(wid.charCodeAt(i));

                for (let off = 0; off < 0x1000 - wid.length; off++) {
                    let match = true;
                    for (let i = 0; i < bytes.length; i++) {
                        if (dv.getUint8(off + i) !== bytes[i]) { match = false; break; }
                    }
                    if (match) {
                        const addr = p.add(off);
                        const key = addr.toString();
                        if (!found[wid]) found[wid] = [];
                        if (found[wid].length < 5) {
                            found[wid].push(addr);
                            totalHits++;
                            console.log(`  [${wid}] @ ${addr}`);

                            // Dump surrounding 64 bytes for context
                            try {
                                const ctx = addr.sub(32);
                                if (ctx.compare(ptr(0x10000)) > 0) {
                                    const ctxBuf = ctx.readByteArray(64);
                                    // Check for ASCII in context
                                    let ascii = '';
                                    for (let j = 0; j < 64; j++) {
                                        const b = new Uint8Array(ctxBuf)[j];
                                        ascii += (b >= 32 && b < 127) ? String.fromCharCode(b) : '.';
                                    }
                                    console.log(`    ctx: ${ascii}`);
                                }
                            } catch (_) {}
                        }
                        off += wid.length + 16;
                    }
                }
            }
        } catch (_) {}
    }
});

console.log(`\n[+] Found ${totalHits} occurrences`);
for (const [wid, addrs] of Object.entries(found)) {
    console.log(`  ${wid}: ${addrs.length} hits`);
    for (const a of addrs) {
        console.log(`    ${a}`);
    }
}
console.log(`\n[*] Use inspect(addr) to dump 256 bytes around any hit.\n`);
