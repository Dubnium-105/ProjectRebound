// =============================================================================
// Session 46: Flexible scan for weapon strings in any context
//
// Don't assume null-terminated — search raw substring and analyze each hit.
// =============================================================================

const BASE = Process.getModuleByName("ProjectBoundarySteam-Win64-Shipping.exe").base;
function hex(n) { return n instanceof NativePointer ? n.toString() : "0x" + n.toString(16); }

console.log(`[+] BASE = ${BASE}`);

// Known weapon IDs — these MUST be in game memory somewhere
const WEAPONS = [
    "PROBE_RU-AKM",
    "PEACE_GSW-AR",
    "SNIPER_RU-MOSIN",
    "MELEE-KNIFE",
    "SUBMACHINE_GUN_MP7",
    "PROBE_GSW-AR",
];

// ---------------------------------------------------------------------------
// Scan for raw substring (no null terminator requirement)
// ---------------------------------------------------------------------------
function scanAll() {
    const allHits = {};

    for (const wid of WEAPONS) {
        allHits[wid] = [];
        const bytes = [];
        for (let i = 0; i < wid.length; i++) bytes.push(wid.charCodeAt(i).toString(16).padStart(2,'0'));
        const pattern = bytes.join(' ');

        console.log(`[*] Scanning for "${wid}"...`);

        Process.enumerateRanges('rw-').forEach(function(range) {
            if (allHits[wid].length >= 5) return;
            if (range.size < 0x1000) return;
            const lo = range.base.and(ptr(0xFFFFFFFF)).toUInt32();
            if (lo < 0x10000000) return;

            try {
                Memory.scan(range.base, range.size, pattern, {
                    onMatch(addr) {
                        if (allHits[wid].length >= 5) return 'stop';

                        try {
                            // Read context: 32 bytes before and 64 bytes after
                            const ctx = addr.sub(32).readByteArray(96);
                            const u8 = new Uint8Array(ctx);

                            // Classify the hit
                            let classification = 'unknown';
                            const following = u8.slice[32 + wid.length];

                            // Check what byte follows the string
                            const nextByte = u8[32 + wid.length];
                            if (nextByte === 0x00) classification = 'null-terminated';
                            else if (nextByte === 0x3A) classification = 'colon-separated';
                            else if (nextByte === 0x2E) classification = 'dot-separated';
                            else if (nextByte >= 0x10 && nextByte <= 0x20) classification = 'protobuf-like';
                            else classification = `byte-0x${nextByte.toString(16)}`;

                            // Check preceding bytes for structure
                            const prevByte = u8[31]; // byte before string
                            const prev4 = u8[28]; // 4 bytes before

                            // Check for 8-byte metadata AFTER a null
                            let hasMetaAfterNull = false;
                            if (nextByte === 0x00) {
                                const meta8 = u8.slice(33 + wid.length, 41 + wid.length);
                                // Check if the 8 bytes look like a pointer or valid int
                                const hi32 = new DataView(ctx, 37 + wid.length, 4).getUint32(0, true);
                                if (hi32 < 0x10000) hasMetaAfterNull = true;
                            }

                            allHits[wid].push({
                                addr,
                                classification,
                                prevByte: hex(prevByte),
                                hasMetaAfterNull,
                                ctxStart: addr.sub(32),
                            });

                            console.log(`  [${wid}] @ ${addr}  class=${classification}  prev=${hex(prevByte)}  hasMeta=${hasMetaAfterNull}`);
                            // Show hexdump
                            console.log(`  ${hexdump(ctx, {offset:0,length:96,header:false,ansi:true})}`);
                        } catch(e) {
                            allHits[wid].push({ addr, classification: 'error', error: e.message });
                        }
                    },
                    onComplete() {},
                    onError(e) {}
                });
            } catch(e) {}
        });
    }

    // Summary
    console.log(`\n=== SUMMARY ===`);
    for (const [wid, hits] of Object.entries(allHits)) {
        console.log(`  ${wid}: ${hits.length} hits`);
    }

    return allHits;
}

// ---------------------------------------------------------------------------
// Deep-analyze a specific hit: follow pointers, read surrounding struct
// ---------------------------------------------------------------------------
function analyzeHit(addr) {
    console.log(`\n[*] Deep analysis of ${addr}:`);
    try {
        // Read 512 bytes around the hit
        const buf = addr.sub(128).readByteArray(512);
        const u8 = new Uint8Array(buf);

        // Find all null-terminated strings in this region
        console.log(`  Strings in region:`);
        let pos = 0;
        let strCount = 0;
        while (pos < u8.length - 4 && strCount < 20) {
            // Find start of a readable string
            if (u8[pos] >= 0x20 && u8[pos] < 0x7F) {
                let end = pos;
                while (end < u8.length && u8[end] >= 0x20 && u8[end] < 0x7F) end++;
                const len = end - pos;
                if (len >= 3 && len < 128) {
                    const str = String.fromCharCode.apply(null, u8.slice(pos, end));
                    // Show what follows
                    let after = '';
                    for (let i = end; i < Math.min(end + 16, u8.length); i++) {
                        after += u8[i].toString(16).padStart(2,'0') + ' ';
                    }
                    console.log(`    [${strCount}] @+${pos} "${str}"  after: ${after}`);
                    strCount++;
                }
                pos = end + 1;
            } else {
                pos++;
            }
        }

        // Look for pointer-like values (8-byte aligned, in heap range)
        console.log(`\n  Potential pointers in region:`);
        for (let off = 0; off < 512 - 8; off += 8) {
            const ptrAddr = addr.sub(128).add(off);
            try {
                const p = ptrAddr.readPointer();
                if (!p.isNull() && p.compare(ptr(0x10000000)) > 0 && p.compare(ptr(0x7FFFFFFFFFFF)) < 0) {
                    // Check if it points to readable memory
                    try {
                        const peek = p.readU8();
                        console.log(`    +${off} → ${p} (readable, first byte=${hex(peek)})`);
                    } catch(_) {
                        console.log(`    +${off} → ${p} (not readable)`);
                    }
                }
            } catch(_) {}
        }
    } catch(e) {
        console.log(`  Error: ${e.message}`);
    }
}

// ---------------------------------------------------------------------------
// Search for string in read-only memory (module sections)
// ---------------------------------------------------------------------------
function scanModule() {
    console.log(`\n[*] Scanning module sections for weapon strings...`);
    const modEnd = BASE.add(0x35000000); // ~850MB module

    for (const wid of WEAPONS) {
        const bytes = [];
        for (let i = 0; i < wid.length; i++) bytes.push(wid.charCodeAt(i).toString(16).padStart(2,'0'));
        const pattern = bytes.join(' ');

        let count = 0;
        try {
            Memory.scan(BASE, modEnd.sub(BASE).toInt32(), pattern, {
                onMatch(addr) {
                    if (count >= 3) return 'stop';
                    count++;
                    const rva = addr.sub(BASE).toInt32();
                    console.log(`  [${wid}] @ ${addr} (RVA ${hex(rva)})`);
                    try {
                        console.log(`  ${hexdump(addr.sub(16).readByteArray(64), {offset:0,length:64,header:false,ansi:true})}`);
                    } catch(_) {}
                },
                onComplete() {},
                onError(e) {}
            });
        } catch(e) {}
        if (count === 0) console.log(`  [${wid}] not found in module`);
    }
}

// ---------------------------------------------------------------------------
// Find large contiguous string tables
// ---------------------------------------------------------------------------
function findStringTables() {
    console.log(`\n[*] Searching for large string tables...`);

    // Look for regions with many consecutive ASCII strings separated by short gaps
    Process.enumerateRanges('rw-').forEach(function(range) {
        if (range.size < 0x100000 || range.size > 0x40000000) return;
        const lo = range.base.and(ptr(0xFFFFFFFF)).toUInt32();
        if (lo < 0x10000000) return;

        // Sample at regular intervals
        for (let p = range.base; p.compare(range.base.add(range.size)) < 0; p = p.add(0x100000)) {
            try {
                const buf = p.readByteArray(0x1000);
                const u8 = new Uint8Array(buf);

                let strCount = 0;
                let pos = 0;
                while (pos < u8.length - 4) {
                    if (u8[pos] >= 0x20 && u8[pos] < 0x7F) {
                        let end = pos;
                        while (end < u8.length && u8[end] >= 0x20 && u8[end] < 0x7F) end++;
                        if (end - pos >= 4 && end - pos < 128) {
                            strCount++;
                        }
                        pos = end + 1;
                    } else {
                        pos++;
                    }
                }

                if (strCount > 100) {
                    console.log(`  High-density string region: ${p} (${strCount} strings in first 4KB)`);
                    // Show first 128 bytes
                    try {
                        console.log(`  ${hexdump(buf.slice(0, 128), {offset:0,length:128,header:false,ansi:true})}`);
                    } catch(_) {}
                    return; // Just report the first one
                }
            } catch(_) {}
        }
    });
}

console.log(`\n[READY] Commands:`);
console.log(`  scanAll()         - Scan heap for all weapon substrings in any format`);
console.log(`  scanModule()      - Scan game module sections for weapon strings`);
console.log(`  analyzeHit(addr)  - Deep-analyze a specific hit address`);
console.log(`  findStringTables()- Find regions with high density of ASCII strings`);
console.log(`\n`);
