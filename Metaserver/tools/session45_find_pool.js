// =============================================================================
// Session 45: Dynamic scan for FName pool using weapon ID strings as anchors
//
// Strategy:
//  1. Search for "PROBE_RU-AKM\x00" (null-terminated ASCII) in heap
//  2. From each hit, walk backwards to find the table start
//  3. Walk forwards to enumerate all strings + their 8-byte metadata
//  4. Check if this is the FName pool (contains "None", "Byte", etc.)
//
// FName pool layout (deduced from session42):
//   [string1\x00][8-byte-metadata][string2\x00][8-byte-metadata]...
//   The 8-byte metadata could be: pointer, FName Number, or hash
// =============================================================================

const BASE = Process.getModuleByName("ProjectBoundarySteam-Win64-Shipping.exe").base;
function hex(n) { return n instanceof NativePointer ? n.toString() : "0x" + n.toString(16); }

console.log(`[+] BASE = ${BASE}`);
console.log(`[*] Scanning for weapon ID strings to locate FName pool...\n`);

// Weapon IDs known to exist in the metaserver data
const ANCHORS = ["PROBE_RU-AKM", "PEACE_GSW-AR", "SNIPER_RU-MOSIN", "MELEE-KNIFE"];

// Known hardcoded UE4 FNames (if we find several of these, it's the FName pool)
const HARDCodedNames = ["None", "Byte", "Bool", "Int", "Float", "Name", "Object",
    "Class", "Enum", "Struct", "Double", "String", "Text", "Interface"];

// ---------------------------------------------------------------------------
// Step 1: Find anchor strings in heap
// ---------------------------------------------------------------------------
function findAnchors() {
    const results = [];

    Process.enumerateRanges('r--').forEach(function(range) {
        if (results.length >= 20) return;
        if (range.size < 0x10000 || range.size > 0x40000000) return;
        const lo = range.base.and(ptr(0xFFFFFFFF)).toUInt32();
        if (lo < 0x10000000) return;

        for (const anchor of ANCHORS) {
            if (results.length >= 20) return;

            // Build pattern: anchor + null terminator
            const bytes = [];
            for (let i = 0; i < anchor.length; i++) bytes.push(anchor.charCodeAt(i).toString(16).padStart(2,'0'));
            bytes.push('00'); // null terminator
            const pattern = bytes.join(' ');

            try {
                Memory.scan(range.base, range.size, pattern, {
                    onMatch(addr) {
                        if (results.length >= 20) return 'stop';
                        // Check if this looks like part of a string table
                        // (8 bytes after null should look like valid metadata)
                        try {
                            const metaOff = addr.add(anchor.length + 1);
                            const metaLo = metaOff.readU32();
                            const metaHi = metaOff.add(4).readU32();
                            const ptr = metaOff.readPointer();

                            results.push({
                                anchor,
                                addr,
                                metaOff,
                                metaLo: hex(metaLo),
                                metaHi: hex(metaHi),
                                ptr: ptr.toString(),
                            });
                        } catch(_) {}
                    },
                    onComplete() {},
                    onError(e) {}
                });
            } catch(e) {}
        }
    });

    console.log(`[+] Found ${results.length} anchor hits`);
    for (const r of results) {
        console.log(`  "${r.anchor}" @ ${r.addr}  meta_lo=${r.metaLo} meta_hi=${r.metaHi} ptr=${r.ptr}`);
    }
    return results;
}

// ---------------------------------------------------------------------------
// Step 2: Walk backwards from an anchor to find table start
// ---------------------------------------------------------------------------
function walkBackwards(anchorAddr, maxDist) {
    maxDist = maxDist || 0x10000;

    // Walk backwards 8 bytes at a time, looking for null-terminated strings
    let pos = anchorAddr;
    let tableStart = null;

    // Read a big chunk backwards
    const searchStart = anchorAddr.sub(maxDist);
    const searchSize = maxDist + 0x1000; // past the anchor
    try {
        const data = searchStart.readByteArray(searchSize);
        const u8 = new Uint8Array(data);

        // Find the anchor in this data
        let anchorOff = -1;
        for (let i = 0; i < maxDist; i++) {
            const testStr = String.fromCharCode.apply(null, u8.slice(i, i + 7));
            if (testStr === "PROBE_RU" || testStr === "PEACE_GS" || testStr === "SNIPER_R") {
                // Found a likely start
                // Walk backwards to find the beginning of the table
                let walkOff = i;
                while (walkOff > 0) {
                    // Find previous null
                    let nullPos = walkOff - 1;
                    while (nullPos > 0 && u8[nullPos] !== 0) nullPos--;
                    if (nullPos <= 0) break;

                    // After null, there should be 8 bytes of metadata
                    const metaStart = nullPos + 1;
                    if (metaStart + 8 > walkOff) break; // not 8 bytes

                    // Then the next string starts at metaStart + 8
                    const strStart = metaStart + 8;
                    if (strStart >= walkOff) break; // overlap

                    // Check if the string before this one is valid
                    let strEnd = strStart;
                    while (strEnd < walkOff && u8[strEnd] !== 0) strEnd++;
                    const strLen = strEnd - strStart;
                    if (strLen <= 0 || strLen > 256) break;

                    walkOff = strStart; // continue walking backwards
                }

                tableStart = searchStart.add(walkOff);
                break;
            }
        }
    } catch(e) {
        console.log(`  Walk error: ${e.message}`);
    }

    return tableStart;
}

// ---------------------------------------------------------------------------
// Step 3: Enumerate the string table from a starting address
// ---------------------------------------------------------------------------
function enumerateTable(startAddr, maxEntries) {
    maxEntries = maxEntries || 200;
    const entries = [];

    let pos = startAddr;
    let prevNull = false;

    for (let i = 0; i < maxEntries; i++) {
        try {
            // Find next null terminator
            let nullOff = 0;
            while (nullOff < 256) {
                const b = pos.add(nullOff).readU8();
                if (b === 0) break;
                nullOff++;
            }
            if (nullOff === 0) {
                // Consecutive nulls — end of table?
                if (prevNull) break;
                prevNull = true;
                pos = pos.add(1);
                continue;
            }
            if (nullOff >= 256) break;
            prevNull = false;

            // Read the string
            const str = pos.readCString(nullOff);
            if (!str || str.length === 0) { pos = pos.add(1); continue; }

            // Read 8 bytes of metadata after null
            const metaAddr = pos.add(nullOff + 1);
            const metaLo = metaAddr.readU32();
            const metaHi = metaAddr.add(4).readU32();

            entries.push({
                str,
                addr: pos,
                len: nullOff,
                metaLo: hex(metaLo),
                metaHi: hex(metaHi),
                metaRaw: [metaLo, metaHi]
            });

            pos = metaAddr.add(8);
        } catch(e) {
            break;
        }
    }

    return entries;
}

// ---------------------------------------------------------------------------
// Step 4: Check if this table is the FName pool
// ---------------------------------------------------------------------------
function checkNamePool(entries) {
    const foundHardcoded = [];
    const foundWeapons = [];

    for (const e of entries) {
        if (HARDCodedNames.includes(e.str)) foundHardcoded.push(e);
        if (ANCHORS.some(a => e.str.includes(a) || e.str === a)) foundWeapons.push(e);
    }

    const score = foundHardcoded.length * 3 + foundWeapons.length;

    return {
        isPool: foundHardcoded.length >= 3,
        score,
        hardcoded: foundHardcoded,
        weapons: foundWeapons
    };
}

// ---------------------------------------------------------------------------
// Main pipeline
// ---------------------------------------------------------------------------
function findPool() {
    const anchors = findAnchors();
    if (anchors.length === 0) {
        console.log(`[-] No anchors found. Game might not be fully loaded.`);
        return null;
    }

    // For each anchor, try to walk backwards and enumerate the table
    const candidates = [];
    for (const a of anchors) {
        console.log(`\n[*] Analyzing table around "${a.anchor}" @ ${a.addr}...`);

        const tableStart = walkBackwards(a.addr, 0x8000);
        if (!tableStart) {
            console.log(`  Could not find table start`);
            continue;
        }

        console.log(`  Table start: ${tableStart}`);
        const entries = enumerateTable(tableStart, 200);
        console.log(`  Enumerated ${entries.length} entries`);

        // Show first/last entries
        for (let i = 0; i < Math.min(entries.length, 10); i++) {
            console.log(`  [${i}] "${entries[i].str}" meta=${entries[i].metaLo},${entries[i].metaHi}`);
        }

        // Check if it's the FName pool
        const result = checkNamePool(entries);
        console.log(`  Score: ${result.score}  Hardcoded: ${result.hardcoded.map(e => e.str).join(', ')}  Weapons: ${result.weapons.length}`);

        if (result.isPool) {
            console.log(`\n[!!!] THIS LOOKS LIKE THE FName POOL!`);
            console.log(`[!!!] Table at: ${tableStart}`);
            console.log(`[!!!] ${entries.length} entries, ${result.hardcoded.length} hardcoded names found`);
        }

        candidates.push({ anchor: a, tableStart, entries, result });
    }

    // Sort by score
    candidates.sort((a, b) => b.result.score - a.result.score);
    const best = candidates[0];

    if (best) {
        console.log(`\n=== BEST CANDIDATE ===`);
        console.log(`Table: ${best.tableStart}  Score: ${best.result.score}`);
        console.log(`Hardcoded: ${best.result.hardcoded.map(e => `"${e.str}"`).join(', ')}`);
        console.log(`Weapons: ${best.result.weapons.map(e => `"${e.str}"`).join(', ')}`);

        // If score >= 3 (at least 1 hardcoded OR multiple weapons), it's promising
        if (best.result.score >= 3) {
            console.log(`\n[***] High-confidence FName pool found!`);
            console.log(`[*] Store this address: best.tableStart`);
            globalThis.poolAddr = best.tableStart;
            globalThis.poolEntries = best.entries;

            // Build an index map: entry position → string
            const idxMap = {};
            best.entries.forEach((e, i) => { idxMap[i] = e.str; });
            globalThis.idxMap = idxMap;

            console.log(`[*] Use resolveEntry(n) to look up entry by index`);
            console.log(`[*] First 10 entries: ${best.entries.slice(0,10).map((e,i)=>`[${i}]="${e.str}"`).join(', ')}`);
        }
    }

    return candidates;
}

// ---------------------------------------------------------------------------
// Helper: resolve an entry index to string
// ---------------------------------------------------------------------------
function resolveEntry(n) {
    if (!globalThis.idxMap) { console.log("Run findPool() first"); return null; }
    return globalThis.idxMap[n] || null;
}

// ---------------------------------------------------------------------------
// Direct search for "None" in a string table format
// ---------------------------------------------------------------------------
function searchNone() {
    console.log(`\n[*] Searching for "None" null-terminated in potential FName pool...`);
    const pattern = "4e 6f 6e 65 00"; // "None\x00"
    const candidates = [];

    Process.enumerateRanges('r--').forEach(function(range) {
        if (candidates.length >= 20) return;
        if (range.size < 0x10000 || range.size > 0x40000000) return;
        const lo = range.base.and(ptr(0xFFFFFFFF)).toUInt32();
        if (lo < 0x10000000) return;

        try {
            Memory.scan(range.base, range.size, pattern, {
                onMatch(addr) {
                    if (candidates.length >= 20) return 'stop';
                    try {
                        // Check if next 8 bytes look like valid metadata, then "Byte" or another hardcoded name
                        const metaLo = addr.add(5).readU32();
                        const metaHi = addr.add(9).readU32();
                        const nextStart = addr.add(13);
                        let nextStr = '';
                        try { nextStr = nextStart.readCString(20); } catch(_) {}

                        // FName pool has "Byte" right after "None" metadata
                        if (nextStr === "Byte" || nextStr === "Bool") {
                            candidates.push({ addr, metaLo: hex(metaLo), metaHi: hex(metaHi), nextStr });
                            console.log(`  [POOL CANDIDATE] "None" @ ${addr} meta=${hex(metaLo)},${hex(metaHi)} next="${nextStr}"`);
                        }
                    } catch(_) {}
                },
                onComplete() {},
                onError(e) {}
            });
        } catch(e) {}
    });

    if (candidates.length > 0) {
        console.log(`\n[!!!] Found ${candidates.length} FName pool candidates!`);
        console.log(`[*] Best candidate: ${candidates[0].addr}`);
        console.log(`[*] Run enumerateTable(addr, 500) to explore`);
        globalThis.poolCandidates = candidates;
    } else {
        console.log(`[-] No FName pool candidates found (expected "Byte"/"Bool" after "None")`);
    }

    return candidates;
}

console.log(`\n[READY] Commands:`);
console.log(`  findPool()        - Full pipeline: find weapon strings → locate table → analyze`);
console.log(`  searchNone()      - Search for "None"+"Byte"/"Bool" pattern (FName pool signature)`);
console.log(`  enumerateTable(addr, n) - Enumerate a string table at address`);
console.log(`  resolveEntry(n)   - Look up string by index in found pool`);
console.log(`\n`);
