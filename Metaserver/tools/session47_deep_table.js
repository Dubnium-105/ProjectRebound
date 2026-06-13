// =============================================================================
// Session 47: Deep analysis of the stable string table at hit #3
//
// From session46 hit #3:
//   "PROBE_RU-AKM" @ 0x1f9625efd96
//   null-terminated strings with 8-byte interleaved metadata
//   Low 32 bits of metadata are STABLE across sessions
//
// This table also contains: "hFrame", "Priority", "ReferenceTemplates", "RootGuid"
// These sound like UProperty/reflection metadata names.
// =============================================================================

const BASE = Process.getModuleByName("ProjectBoundarySteam-Win64-Shipping.exe").base;
function hex(n) { return n instanceof NativePointer ? n.toString() : "0x" + n.toString(16); }

// Scan for any null-terminated "PROBE_RU-AKM" with valid 8-byte metadata
function findTable() {
    console.log(`[*] Scanning for "PROBE_RU-AKM\\x00" with 8-byte metadata...`);
    const pattern = "50 52 4f 42 45 5f 52 55 2d 41 4b 4d 00";
    const candidates = [];

    Process.enumerateRanges('rw-').forEach(function(range) {
        if (candidates.length >= 5) return;
        if (range.size < 0x100 || range.size > 0x40000000) return;
        const lo = range.base.and(ptr(0xFFFFFFFF)).toUInt32();
        if (lo < 0x10000000) return;

        try {
            Memory.scan(range.base, range.size, pattern, {
                onMatch(addr) {
                    if (candidates.length >= 5) return 'stop';
                    try {
                        // Read 8 bytes after null
                        const metaAddr = addr.add(13); // "PROBE_RU-AKM" = 12 chars + 1 null = 13
                        const metaLo = metaAddr.readU32();
                        const metaHi = metaAddr.add(4).readU32();

                        // Valid metadata: hi32 should be small (<0x10000), lo32 should be non-zero
                        if (metaHi < 0x10000 && metaLo > 0x1000) {
                            candidates.push({
                                addr,
                                metaAddr,
                                metaLo: hex(metaLo),
                                metaHi: hex(metaHi),
                                meta64: ptr(metaAddr.readU64()),
                            });
                            console.log(`  [CANDIDATE] @ ${addr}  metaLo=${hex(metaLo)} metaHi=${hex(metaHi)}`);
                        }
                    } catch(_) {}
                },
                onComplete() {},
                onError(e) {}
            });
        } catch(e) {}
    });

    console.log(`[+] Found ${candidates.length} candidates`);
    return candidates;
}

// ---------------------------------------------------------------------------
// Enumerate entire string table from a known entry
// ---------------------------------------------------------------------------
function enumerateTable(anchorAddr, maxEntries) {
    maxEntries = maxEntries || 300;
    console.log(`\n[*] Enumerating table from anchor @ ${anchorAddr}...`);

    // First walk backwards to find table start
    let pos = anchorAddr;
    try {
        // Walk backwards looking for boundaries
        // A table boundary is where we see non-string data or a clear header
        for (let step = 0; step < 1000; step++) {
            const checkAddr = anchorAddr.sub(step * 16);
            try {
                // Try reading a string backwards
                // Look for null byte, then check if string before it is valid
                const check = checkAddr.readU8();
            } catch(e) { break; }
        }
    } catch(e) {}

    // Just enumerate forward
    const entries = [];
    pos = anchorAddr;

    for (let i = 0; i < maxEntries; i++) {
        try {
            // Read until null
            let len = 0;
            while (len < 256) {
                const b = pos.add(len).readU8();
                if (b === 0x00) break;
                len++;
            }
            if (len === 0) {
                // Empty string — maybe end of table or gap
                pos = pos.add(1);
                continue;
            }
            if (len >= 256) break;

            const str = pos.readCString(len);
            if (!str || str.length === 0) { pos = pos.add(1); continue; }

            // Read 8 bytes after null
            const metaAddr = pos.add(len + 1);
            const metaLo = metaAddr.readU32();
            const metaHi = metaAddr.add(4).readU32();

            // Check if metadata looks valid
            const validMeta = metaHi < 0x100000 && metaLo > 0;

            entries.push({
                index: i,
                str,
                addr: pos,
                len,
                metaLo,
                metaHi,
                metaLoHex: hex(metaLo),
                metaHiHex: hex(metaHi),
                validMeta
            });

            pos = metaAddr.add(8);

            // Stop if we see too many consecutive invalid entries
            if (!validMeta && entries.length > 10) {
                let consecutiveInvalid = 0;
                for (let j = entries.length - 1; j >= 0 && entries[j].validMeta === false; j--) {
                    consecutiveInvalid++;
                }
                if (consecutiveInvalid > 5) {
                    // Remove invalid tail entries
                    while (entries.length > 0 && !entries[entries.length - 1].validMeta) {
                        entries.pop();
                    }
                    break;
                }
            }
        } catch(e) {
            break;
        }
    }

    console.log(`  Enumerated ${entries.length} entries`);
    return entries;
}

// ---------------------------------------------------------------------------
// Classify the table based on its content
// ---------------------------------------------------------------------------
function classifyTable(entries) {
    const strings = entries.map(e => e.str);
    const allText = strings.join(' ');

    const hardcodedFNames = ["None", "Byte", "Bool", "Int", "Float", "Name", "Object", "Class", "Enum", "Struct"];
    const propertyNames = ["Priority", "RootGuid", "ReferenceTemplates", "hFrame"];
    const weaponPatterns = ["GSW-", "RU-", "AKM", "MOSIN", "SVD", "MELEE-", "PROBE_", "PEACE_", "SNIPER_"];

    const hasHardcoded = hardcodedFNames.filter(n => strings.includes(n));
    const hasProperties = propertyNames.filter(n => strings.includes(n));
    const hasWeapons = weaponPatterns.filter(p => strings.some(s => s.includes(p)));

    console.log(`\n[*] Classification:`);
    console.log(`  FName hardcoded: ${hasHardcoded.join(', ') || 'none'}`);
    console.log(`  UProperty names: ${hasProperties.join(', ') || 'none'}`);
    console.log(`  Weapon patterns: ${hasWeapons.join(', ') || 'none'}`);

    // Try to determine the metadata format
    // Collect all metaLo and metaHi values
    const metaLos = entries.filter(e => e.validMeta).map(e => e.metaLo);
    const metaHis = entries.filter(e => e.validMeta).map(e => e.metaHi);

    if (metaLos.length > 10) {
        const minLo = Math.min(...metaLos);
        const maxLo = Math.max(...metaLos);
        console.log(`\n[*] Metadata analysis (${metaLos.length} entries):`);
        console.log(`  metaLo range: ${hex(minLo)} - ${hex(maxLo)}`);
        console.log(`  metaHi values: ${[...new Set(metaHis)].map(hex).join(', ')}`);

        // If metaLo values are sequential or close together, they might be indices
        const sorted = [...metaLos].sort((a, b) => a - b);
        let sequential = 0;
        for (let i = 1; i < sorted.length; i++) {
            if (sorted[i] - sorted[i-1] < 100) sequential++;
        }
        console.log(`  Sequential gaps: ${sequential}/${sorted.length - 1}`);
    }

    return { hasHardcoded, hasProperties, hasWeapons };
}

// ---------------------------------------------------------------------------
// Search for specific weapon IDs in the string table
// and show their metadata
// ---------------------------------------------------------------------------
function lookupWeapon(weaponId) {
    console.log(`\n[*] Looking up "${weaponId}" in table...`);
    const pattern = [];
    for (let i = 0; i < weaponId.length; i++) pattern.push(weaponId.charCodeAt(i).toString(16).padStart(2,'0'));
    pattern.push('00');
    const hexPattern = pattern.join(' ');

    let found = 0;
    Process.enumerateRanges('rw-').forEach(function(range) {
        if (found >= 10) return;
        if (range.size < 0x100) return;
        try {
            Memory.scan(range.base, range.size, hexPattern, {
                onMatch(addr) {
                    if (found >= 10) return 'stop';
                    found++;
                    const metaLo = addr.add(weaponId.length + 1).readU32();
                    const metaHi = addr.add(weaponId.length + 5).readU32();
                    console.log(`  [#${found}] @ ${addr}  metaLo=${hex(metaLo)} metaHi=${hex(metaHi)}`);
                },
                onComplete() {},
                onError(e) {}
            });
        } catch(e) {}
    });
    console.log(`[+] Found ${found} matches`);
}

// ---------------------------------------------------------------------------
// Auto: find table, enumerate, classify
// ---------------------------------------------------------------------------
function auto() {
    const candidates = findTable();
    if (candidates.length === 0) return;

    const best = candidates[0];
    const entries = enumerateTable(best.addr, 300);

    // Show first and last 15 entries
    console.log(`\n[*] First 15 entries:`);
    for (let i = 0; i < Math.min(15, entries.length); i++) {
        const e = entries[i];
        console.log(`  [${i}] "${e.str}" metaLo=${e.metaLoHex} metaHi=${e.metaHiHex}`);
    }
    if (entries.length > 30) {
        console.log(`  ... (${entries.length - 30} more) ...`);
        console.log(`[*] Last 15 entries:`);
        for (let i = entries.length - 15; i < entries.length; i++) {
            const e = entries[i];
            console.log(`  [${i}] "${e.str}" metaLo=${e.metaLoHex} metaHi=${e.metaHiHex}`);
        }
    }

    classifyTable(entries);

    // Store for later use
    globalThis.tableAddr = best.addr;
    globalThis.tableEntries = entries;
    globalThis.tableMap = {};
    entries.forEach(e => { globalThis.tableMap[e.str] = e; });

    console.log(`\n[*] Table stored: tableAddr, tableEntries, tableMap`);

    return entries;
}

console.log(`\n[READY] Commands:`);
console.log(`  auto()              - Find table, enumerate, classify automatically`);
console.log(`  findTable()         - Find "PROBE_RU-AKM\\x00" with valid metadata`);
console.log(`  enumerateTable(addr) - Enumerate the string table at address`);
console.log(`  lookupWeapon("id")  - Search for a weapon ID and show its metadata`);
console.log(`\n`);
