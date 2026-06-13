// =============================================================================
// Session 52: Dense heap scan for UObjects
//
// Instead of sparse sampling, scan EVERY 8 bytes through the heap.
// Lower verification bar: just check vtable → module, Class → heap.
// =============================================================================

const BASE = Process.getModuleByName("ProjectBoundarySteam-Win64-Shipping.exe").base;
function hex(n) { return n instanceof NativePointer ? n.toString() : "0x" + n.toString(16); }
function rva(p) { return "0x" + p.sub(BASE).toInt32().toString(16); }

console.log(`[+] BASE = ${BASE}`);

function inHeap(p) { return p.compare(ptr(0x10000000)) > 0 && p.compare(ptr(0x7FFFFFFFFFFF)) < 0; }
function inModule(p) { return p.compare(BASE) > 0 && p.compare(BASE.add(0x35000000)) < 0; }
function validFName(idx, num) { return idx > 0 && idx < 500000 && num < 10000; }

// ---------------------------------------------------------------------------
// Quick UObject check (fewer verifications, faster)
// ---------------------------------------------------------------------------
function quickCheck(addr) {
    try {
        const vtable = addr.readPointer();
        if (!inModule(vtable)) return false;

        const classPtr = addr.add(0x10).readPointer();
        if (!inHeap(classPtr)) return false;

        // Quick Class verification: just read Class vtable
        const classVtable = classPtr.readPointer();
        if (!inModule(classVtable)) return false;

        // Quick Name check
        const nameIdx = addr.add(0x18).readU32();
        if (nameIdx < 1 || nameIdx > 500000) return false;

        return true;
    } catch(_) { return false; }
}

// ---------------------------------------------------------------------------
// Find display characters by scanning dense heap regions
// ---------------------------------------------------------------------------
function findChars() {
    console.log(`\n[*] Dense scan for display characters...`);
    console.log(`[*] This checks EVERY 8 bytes in key heap regions...`);

    const chars = [];
    let checks = 0;
    let uobjs = 0;

    // Focus on heap regions 0x10000000-0x7FFFFFFFFFFF
    // But scan much more densely
    Process.enumerateRanges('rw-').forEach(function(range) {
        if (chars.length >= 5) return;
        if (!inHeap(range.base)) return;
        if (range.size < 0x10000) return;
        // Skip huge ranges (mapped files, textures)
        if (range.size > 0x20000000) return;

        // Scan every 8 bytes
        const end = range.base.add(range.size);
        for (let p = range.base; p.compare(end) < 0 && chars.length < 5; p = p.add(8)) {
            checks++;
            // Progress indicator every 10M checks
            if (checks % 10000000 === 0) {
                console.log(`  ... ${checks/1000000}M checks, ${uobjs} UObjects, ${chars.length} chars`);
            }
            if (checks > 100000000) return; // hard safety limit

            if (!quickCheck(p)) continue;
            uobjs++;

            // Get object info
            const nameIdx = p.add(0x18).readU32();
            const classPtr = p.add(0x10).readPointer();
            const classNameIdx = classPtr.add(0x18).readU32();

            // Look for weapon pointers in the 0x380-0x410 range
            let weapons = [];
            for (let off = 0x380; off <= 0x410; off += 8) {
                try {
                    const wpn = p.add(off).readPointer();
                    if (inHeap(wpn)) {
                        const wpnVtable = wpn.readPointer();
                        if (inModule(wpnVtable)) {
                            const wpnName = wpn.add(0x18).readU32();
                            weapons.push({ off, addr: wpn, nameIdx: wpnName });
                        }
                    }
                } catch(_) {}
            }

            // Look for nearby FNames that could be CharacterID
            let fnames = [];
            for (let off = 0x380; off <= 0x400; off += 8) {
                const idx = p.add(off).readU32();
                const num = p.add(off + 4).readU32();
                if (validFName(idx, num) && idx > 10) {
                    fnames.push({ off, idx, num });
                }
            }

            if (weapons.length >= 1 && fnames.length >= 2) {
                chars.push({ addr: p, nameIdx, classNameIdx, classPtr, weapons, fnames });
                console.log(`\n  [CHAR#${chars.length}] @ ${p}`);
                console.log(`    Name idx=${nameIdx}  ClassName idx=${classNameIdx}`);
                console.log(`    Weapons (${weapons.length}):`);
                for (const w of weapons) console.log(`      +${hex(w.off)} → ${w.addr} (nameIdx=${w.nameIdx})`);
                console.log(`    FNames:`);
                for (const f of fnames.slice(0, 10)) console.log(`      +${hex(f.off)}: idx=${f.idx} num=${f.num}`);
            }
        }
    });

    console.log(`\n[+] Scanned ${checks} addresses, found ${uobjs} UObjects`);
    console.log(`[+] Display characters: ${chars.length}`);

    globalThis.chars = chars;
    return chars;
}

// ---------------------------------------------------------------------------
// Dump object memory
// ---------------------------------------------------------------------------
function dump(addr, size) {
    size = size || 0x500;
    console.log(`\n[*] Dump of @ ${addr}:`);
    try {
        // Verify basic UObject structure
        const vtable = addr.readPointer();
        const flags = addr.add(0x8).readU32();
        const index = addr.add(0xC).readU32();
        const classPtr = addr.add(0x10).readPointer();
        const classNameIdx = classPtr.add(0x18).readU32();
        const objNameIdx = addr.add(0x18).readU32();
        const outer = addr.add(0x20).readPointer();

        console.log(`  vtable: ${vtable} (RVA ${rva(vtable)})`);
        console.log(`  Flags: ${hex(flags)}  InternalIndex: ${index}`);
        console.log(`  Class: ${classPtr} (nameIdx=${classNameIdx})`);
        console.log(`  Name: idx=${objNameIdx}  Outer: ${outer}`);

        // Hexdump sections
        console.log(`\n  Header:`);
        console.log(`  ${hexdump(addr.readByteArray(0x80), {offset:0,length:0x80,header:false,ansi:true})}`);

        console.log(`\n  Weapon area (0x380-0x430):`);
        console.log(`  ${hexdump(addr.add(0x380).readByteArray(0xB0), {offset:0x380,length:0xB0,header:false,ansi:true})}`);

        // All FNames
        console.log(`\n  All FName fields:`);
        const data = addr.readByteArray(size);
        for (let off = 0; off < size - 8; off += 4) {
            const idx = new DataView(data, off, 4).getUint32(0, true);
            const num = new DataView(data, off + 4, 4).getUint32(0, true);
            if (validFName(idx, num) && idx > 10) {
                console.log(`    +${hex(off)}: idx=${idx} num=${num}`);
            }
        }
    } catch(e) {
        console.log(`  Error: ${e.message}`);
    }
}

console.log(`\n[READY] Commands:`);
console.log(`  findChars()  - Dense scan for display characters`);
console.log(`  dump(addr)   - Detailed dump of any object`);
console.log(`\n`);
