// =============================================================================
// Session 44: Analyze the string table found at 0x1c9685bf480
//
// Strings are null-terminated ASCII with 8 bytes of metadata between them.
// This could be the FName pool, an asset table, or the loadout cache.
// =============================================================================

const BASE = Process.getModuleByName("ProjectBoundarySteam-Win64-Shipping.exe").base;
function hex(n) { return n instanceof NativePointer ? n.toString() : "0x" + n.toString(16); }

// Addresses from session42 results — string tables with weapon IDs
const TARGETS = [
    { addr: ptr("0x1c9685bf480"), label: "StringTable_36" },
    { addr: ptr("0x1c9685bf6ed"), label: "StringTable_37" },
    { addr: ptr("0x1c95e49359a"), label: "StringTable_33" },
    { addr: ptr("0x1c9736ed578"), label: "StringTable_38" },
    { addr: ptr("0x1c94d2900bc"), label: "Protobuf_N2" },
    { addr: ptr("0x1c94d2c4724"), label: "ProtoArray_N4" },
];

console.log(`[+] BASE = ${BASE}`);

// ---------------------------------------------------------------------------
// Analyze a string table: read null-terminated strings with 8-byte interleaved data
// ---------------------------------------------------------------------------
function analyzeTable(addr, label, maxStrings) {
    maxStrings = maxStrings || 50;
    console.log(`\n=== ${label} @ ${addr} ===`);

    // Read a big chunk
    const data = addr.readByteArray(0x1000);
    const u8 = new Uint8Array(data);
    const strings = [];

    let pos = 0;
    while (pos < u8.length - 8 && strings.length < maxStrings) {
        // Find null terminator
        let end = pos;
        while (end < u8.length && u8[end] !== 0) end++;
        if (end >= u8.length) break;

        const len = end - pos;
        if (len > 0 && len < 256) {
            const str = String.fromCharCode.apply(null, u8.slice(pos, end));
            // Read 8 bytes after null
            const metaOff = end + 1;
            if (metaOff + 8 <= u8.length) {
                const meta = u8.slice(metaOff, metaOff + 8);
                const ptrVal = new DataView(data, metaOff, 8).getBigUint64(0, true);
                const lo32 = new DataView(data, metaOff, 4).getUint32(0, true);
                const hi32 = new DataView(data, metaOff + 4, 4).getUint32(0, true);

                strings.push({ str, len, metaOff: addr.add(metaOff), meta: Array.from(meta), ptrVal, lo32, hi32 });
            }
            pos = metaOff + 8;
        } else {
            pos = end + 1;
        }
    }

    console.log(`  Found ${strings.length} strings`);
    for (let i = 0; i < Math.min(strings.length, 30); i++) {
        const s = strings[i];
        const ptrStr = s.ptrVal ? ptr(s.ptrVal) : ptr(0);
        const inModule = !ptrStr.isNull() && ptrStr.compare(BASE) > 0 && ptrStr.compare(BASE.add(0x30000000)) < 0;
        const inHeap = !ptrStr.isNull() && ptrStr.compare(ptr(0x10000000)) > 0 && ptrStr.compare(ptr(0x7FFFFFFFFFFF)) < 0;
        const loc = inModule ? '(MODULE)' : (inHeap ? '(HEAP)' : '');
        console.log(`  [${i}] "${s.str}" meta_lo=${hex(s.lo32)} meta_hi=${hex(s.hi32)} ptr=${ptrStr} ${loc}`);
    }

    return strings;
}

// ---------------------------------------------------------------------------
// Search backwards and forwards from a hit to find the table boundaries
// ---------------------------------------------------------------------------
function findTableBounds(addr, label) {
    console.log(`\n[*] Finding table bounds around ${addr}...`);

    // Scan backwards to find start
    let start = addr;
    for (let off = -8; off > -0x10000; off -= 8) {
        try {
            const check = addr.add(off);
            const b = check.readU8();
            // If we see a null followed by consistent-looking data, it might be the start
            if (b === 0) {
                const prevBytes = check.sub(16).readByteArray(16);
                const prevU8 = new Uint8Array(prevBytes);
                const hasNull = prevU8.indexOf(0);
                if (hasNull >= 0 && hasNull < 14) {
                    start = check.sub(14 - hasNull);
                    if (start.compare(addr.sub(0x5000)) < 0) break;
                }
            }
        } catch(_) { break; }
    }
    console.log(`  Estimated start: ${start}`);

    // Scan forwards to find end
    let end = addr;
    let nullCount = 0;
    for (let off = 0; off < 0x10000; off += 8) {
        try {
            const check = addr.add(off);
            const b = check.readU8();
            if (b === 0) nullCount++;
            else nullCount = 0;
            if (nullCount > 5) { end = check; break; }
        } catch(_) { break; }
    }
    console.log(`  Estimated end:   ${end} (size: ${end.sub(start)} bytes)`);

    return { start, end, size: end.sub(start).toInt32() };
}

// ---------------------------------------------------------------------------
// Check if this is the FName pool by looking for known FName patterns
// ---------------------------------------------------------------------------
function checkIfNamePool(strings) {
    // FName pool entries have specific patterns:
    // - "None" is always index 0 or 1
    // - "Byte", "Bool", "Int", "Float" are low indices
    // - Engine types like "Class", "Object", "Function" etc.
    const knownHardcoded = ["None", "Byte", "Bool", "Int", "Float", "Name", "Object",
        "Class", "Enum", "Struct", "Vector", "Rotator", "Color", "LinearColor"];

    const found = [];
    for (const s of strings) {
        if (knownHardcoded.includes(s.str)) {
            found.push(s);
        }
    }

    if (found.length >= 3) {
        console.log(`\n[!!!] This looks like an FName pool! Found hardcoded names: ${found.map(s => s.str).join(', ')}`);
        return true;
    }
    return false;
}

// ---------------------------------------------------------------------------
// Main analysis
// ---------------------------------------------------------------------------
function analyzeAll() {
    const allStrings = [];

    for (const t of TARGETS) {
        try {
            const strings = analyzeTable(t.addr, t.label, 60);
            allStrings.push({ target: t, strings });
            checkIfNamePool(strings);
        } catch(e) {
            console.log(`  Error reading ${t.label}: ${e.message}`);
        }
    }

    // Find table bounds for the most promising target
    try {
        findTableBounds(TARGETS[0].addr, TARGETS[0].label);
    } catch(e) {}

    return allStrings;
}

// ---------------------------------------------------------------------------
// Also: search for specific known strings to find the table
// ---------------------------------------------------------------------------
function searchForString(str) {
    console.log(`\n[*] Searching for "${str}"...`);
    const bytes = [];
    for (let i = 0; i < str.length; i++) bytes.push(str.charCodeAt(i));
    bytes.push(0); // null terminator
    const pattern = bytes.map(b => b.toString(16).padStart(2, '0')).join(' ');

    let count = 0;
    Process.enumerateRanges('r--').forEach(function(range) {
        if (count >= 10) return;
        if (range.size < 0x100 || range.size > 0x40000000) return;
        try {
            Memory.scan(range.base, range.size, pattern, {
                onMatch(addr) {
                    count++;
                    console.log(`  [#${count}] @ ${addr}`);
                    // Show context
                    try {
                        console.log(`  ${hexdump(addr.sub(8).readByteArray(48), {offset:0,length:48,header:false,ansi:true})}`);
                    } catch(_) {}
                    if (count >= 10) return 'stop';
                },
                onComplete() {},
                onError(e) {}
            });
        } catch(e) {}
    });
    console.log(`[+] Found ${count} matches`);
}

// ---------------------------------------------------------------------------
// Dump a specific address region
// ---------------------------------------------------------------------------
function dump(addr, size) {
    size = size || 256;
    try {
        console.log(hexdump(addr.readByteArray(size), {offset:0,length:size,header:false,ansi:true}));
    } catch(e) {
        console.log(`Error: ${e.message}`);
    }
}

console.log(`\n[READY] Commands:`);
console.log(`  analyzeAll()           - Analyze all found string tables`);
console.log(`  searchForString("str") - Search for a specific null-terminated string`);
console.log(`  dump(addr, size)       - Hex dump a memory region`);
console.log(`  analyzeTable(addr, label, max) - Analyze a specific address`);
console.log(`  findTableBounds(addr)  - Find boundaries of a string table`);
console.log(`\n`);
