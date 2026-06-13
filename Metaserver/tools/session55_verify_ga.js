// =============================================================================
// Session 55: Verify and walk the found GUObjectArray candidates
//
// From session54: candidate 1 @ 0x12b05da5060 (43,687 items)
//                 candidate 5 @ 0x12b174d0000 (20,486 items)
//
// Verify they're real GUObjectArrays and find display characters.
// =============================================================================

const BASE = Process.getModuleByName("ProjectBoundarySteam-Win64-Shipping.exe").base;
function hex(n) { return n instanceof NativePointer ? n.toString() : "0x" + n.toString(16); }
function rva(p) { return "0x" + p.sub(BASE).toInt32().toString(16); }
function inHeap(p) { return p.compare(ptr(0x10000000)) > 0 && p.compare(ptr(0x7FFFFFFFFFFF)) < 0; }
function inModule(p) { return p.compare(BASE) > 0 && p.compare(BASE.add(0x35000000)) < 0; }
function validFName(idx, num) { return idx > 0 && idx < 500000 && num < 10000; }

const CANDIDATES = [
    { start: ptr("0x12b05da5060"), count: 43687, label: "C1" },
    { start: ptr("0x12b05ea5000"), count: 43691, label: "C2" },
    { start: ptr("0x12b174d0000"), count: 20486, label: "C5" },
];

// ---------------------------------------------------------------------------
// Verify: read FObjectItems and check Object validity
// ---------------------------------------------------------------------------
function verifyCandidate(cand) {
    console.log(`\n[*] Verifying ${cand.label} @ ${cand.start}...`);

    let validUObjects = 0;
    let validItems = 0;
    let nullItems = 0;
    let total = 0;

    for (let i = 0; i < Math.min(cand.count, 100); i++) {
        const itemAddr = cand.start.add(i * 24);
        total++;
        try {
            const objPtr = itemAddr.readPointer();
            if (objPtr.isNull()) { nullItems++; continue; }
            if (!inHeap(objPtr)) continue;

            validItems++;
            const vtable = objPtr.readPointer();
            if (!inModule(vtable)) continue;

            const flags = itemAddr.add(8).readU32();
            const nameIdx = objPtr.add(0x18).readU32();
            if (!validFName(nameIdx, 0)) continue;

            validUObjects++;
            if (i < 5) {
                const classPtr = objPtr.add(0x10).readPointer();
                let classNameIdx = 0;
                try { classNameIdx = classPtr.add(0x18).readU32(); } catch(_) {}

                console.log(`  [${i}] obj=${objPtr} nameIdx=${nameIdx} classIdx=${classNameIdx} flags=${hex(flags)}`);
            }
        } catch(_) {}
    }

    console.log(`  Results: ${validUObjects}/${validItems} valid UObjects, ${nullItems} null, ${total-validItems-nullItems} invalid`);
    return validUObjects > total * 0.3; // At least 30% valid
}

// ---------------------------------------------------------------------------
// Walk GUObjectArray and find display characters
// ---------------------------------------------------------------------------
function findDisplayChars(cand) {
    console.log(`\n[*] Walking ${cand.label} (${cand.count} objects) for display characters...`);

    const results = [];
    let checked = 0;
    let validObjs = 0;
    let step = cand.count > 50000 ? 1 : 1;

    for (let i = 0; i < cand.count && results.length < 10; i += step) {
        checked++;
        const itemAddr = cand.start.add(i * 24);
        try {
            const objPtr = itemAddr.readPointer();
            if (objPtr.isNull()) continue;
            if (!inHeap(objPtr)) continue;

            // Quick UObject check
            const vtable = objPtr.readPointer();
            if (!inModule(vtable)) continue;
            const nameIdx = objPtr.add(0x18).readU32();
            if (!validFName(nameIdx, 0)) continue;

            validObjs++;

            // Check for weapon pointers
            let weapons = [];
            for (let off = 0x380; off <= 0x410; off += 8) {
                try {
                    const wpn = objPtr.add(off).readPointer();
                    if (inHeap(wpn)) {
                        const wpnVtable = wpn.readPointer();
                        if (inModule(wpnVtable)) {
                            const wpnNameIdx = wpn.add(0x18).readU32();
                            if (validFName(wpnNameIdx, 0)) {
                                weapons.push({ off, addr: wpn, nameIdx: wpnNameIdx });
                            }
                        }
                    }
                } catch(_) {}
            }

            // Look for FNames in role config area
            let fnames = [];
            for (let off = 0x380; off <= 0x400; off += 8) {
                const idx = objPtr.add(off).readU32();
                const num = objPtr.add(off + 4).readU32();
                if (validFName(idx, num) && idx > 10) {
                    fnames.push({ off, idx, num });
                }
            }

            // A display character should have multiple weapons and FNames
            if (weapons.length >= 2 || (weapons.length >= 1 && fnames.length >= 4)) {
                const classPtr = objPtr.add(0x10).readPointer();
                let classNameIdx = 0;
                try { classNameIdx = classPtr.add(0x18).readU32(); } catch(_) {}

                results.push({
                    objIndex: i,
                    addr: objPtr,
                    vtable,
                    nameIdx,
                    classNameIdx,
                    classPtr,
                    weapons,
                    fnames,
                });
                console.log(`\n  [CHAR#${results.length}] obj #${i} @ ${objPtr}`);
                console.log(`    Name idx=${nameIdx}  ClassName idx=${classNameIdx}`);
                console.log(`    Weapons (${weapons.length}):`);
                for (const w of weapons) console.log(`      +${hex(w.off)} → ${w.addr} (nameIdx=${w.nameIdx})`);
                console.log(`    FNames:`);
                for (const f of fnames.slice(0, 8)) console.log(`      +${hex(f.off)}: idx=${f.idx} num=${f.num}`);
            }
        } catch(_) {}
    }

    console.log(`\n[+] Checked ${checked} entries, ${validObjs} valid UObjects`);
    console.log(`[+] Found ${results.length} display character candidates`);
    globalThis.dcResults = results;
    return results;
}

// ---------------------------------------------------------------------------
// Deep dump of a display character
// ---------------------------------------------------------------------------
function dumpChar(addr) {
    console.log(`\n[*] Deep dump of display char @ ${addr}:`);
    try {
        const size = 0x600;
        const data = addr.readByteArray(size);

        console.log(`  Object header:`);
        console.log(`  ${hexdump(addr.readByteArray(0x80), {offset:0,length:0x80,header:false,ansi:true})}`);

        console.log(`\n  Mid section (0x200-0x380):`);
        console.log(`  ${hexdump(addr.add(0x200).readByteArray(0x180), {offset:0x200,length:0x180,header:false,ansi:true})}`);

        console.log(`\n  Weapon/RoleConfig area (0x380-0x500):`);
        console.log(`  ${hexdump(addr.add(0x380).readByteArray(0x180), {offset:0x380,length:0x180,header:false,ansi:true})}`);

        // All FNames
        console.log(`\n  All FName fields:`);
        for (let off = 0; off < size - 8; off += 4) {
            const idx = new DataView(data, off, 4).getUint32(0, true);
            const num = new DataView(data, off + 4, 4).getUint32(0, true);
            if (validFName(idx, num) && idx > 10) {
                console.log(`    +${hex(off)}: idx=${idx} num=${num}`);
            }
        }

        // All heap pointers
        console.log(`\n  Heap pointers:`);
        for (let off = 0; off < size - 8; off += 8) {
            const p = addr.add(off).readPointer();
            if (inHeap(p)) {
                try {
                    const v = p.readPointer();
                    const marker = inModule(v) ? ' [UObject]' : '';
                    console.log(`    +${hex(off)} → ${p}${marker}`);
                } catch(_) {
                    console.log(`    +${hex(off)} → ${p}`);
                }
            }
        }
    } catch(e) {
        console.log(`  Error: ${e.message}`);
    }
}

// ---------------------------------------------------------------------------
// Auto pipeline
// ---------------------------------------------------------------------------
function auto() {
    for (const cand of CANDIDATES) {
        const valid = verifyCandidate(cand);
        console.log(`  ${cand.label}: ${valid ? 'VALID' : 'INVALID'}`);
        if (valid) {
            findDisplayChars(cand);
            return;
        }
    }
    console.log(`[-] No valid GUObjectArray found`);
}

console.log(`\n[READY] Commands:`);
console.log(`  auto()                - Verify all candidates & find display chars`);
console.log(`  verifyCandidate(CANDIDATES[0]) - Verify specific candidate`);
console.log(`  findDisplayChars(CANDIDATES[0]) - Walk specific candidate`);
console.log(`  dumpChar(addr)        - Deep dump of display character`);
console.log(`\n`);
