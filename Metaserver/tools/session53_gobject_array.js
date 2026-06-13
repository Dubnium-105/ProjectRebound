// =============================================================================
// Session 53: Find GUObjectArray in module's .data section
//
// FUObjectArray structure in UE4.27:
//   struct FUObjectArray {
//       FObjectItem* ObjObjects;     // +0x00: pointer to FObjectItem array
//       int32 ObjObjectsNum;          // +0x08: element count
//       int32 ObjObjectsMax;          // +0x0C: capacity
//       // ... more fields
//   };
//
// FObjectItem (24 bytes):
//   UObject* Object;   // +0x00: heap pointer to actual UObject
//   int32 Flags;       // +0x08
//   int32 ClusterIndex;// +0x0C
//   int32 SerialNumber;// +0x10
//   int32 Reserved;    // +0x14 (usually 0)
//
// The GUObjectArray global is in the module's .data section.
// We scan for {heap_ptr, count(50k-500k), max(>=count)} pattern.
// =============================================================================

const BASE = Process.getModuleByName("ProjectBoundarySteam-Win64-Shipping.exe").base;
function hex(n) { return n instanceof NativePointer ? n.toString() : "0x" + n.toString(16); }
function rva(p) { return "0x" + p.sub(BASE).toInt32().toString(16); }

console.log(`[+] BASE = ${BASE}`);

function inHeap(p) { return p.compare(ptr(0x10000000)) > 0 && p.compare(ptr(0x7FFFFFFFFFFF)) < 0; }
function inModule(p) { return p.compare(BASE) > 0 && p.compare(BASE.add(0x35000000)) < 0; }

// ---------------------------------------------------------------------------
// Scan module .data section for FUObjectArray pattern
// ---------------------------------------------------------------------------
function findGObjectArray() {
    console.log(`\n[*] Scanning module .data for GUObjectArray...`);

    const candidates = [];

    // Scan module's data/bss sections (they're marked rw-)
    Process.enumerateRanges('rw-').forEach(function(range) {
        if (candidates.length >= 5) return;
        // Must be in module range
        if (!inModule(range.base)) return;
        if (range.size < 0x100 || range.size > 0x3000000) return;

        // Scan for pattern: {heap_ptr, num(50k-500k), max(>=num)}
        for (let off = 0; off < Math.min(range.size, 0x200000); off += 8) {
            if (candidates.length >= 5) return;
            try {
                const ptr = range.base.add(off).readPointer();
                if (!inHeap(ptr)) continue;
                const num = range.base.add(off + 8).readU32();
                const max = range.base.add(off + 12).readU32();

                // Reasonable GUObjectArray size
                if (num < 10000 || num > 500000) continue;
                if (max < num || max > 1000000) continue;

                // Verify: first few FObjectItem entries should have valid Object pointers
                let valid = 0;
                let total = 0;
                for (let i = 0; i < Math.min(num, 50); i++) {
                    try {
                        const itemAddr = ptr.add(i * 24);
                        const objPtr = itemAddr.readPointer();
                        if (!objPtr.isNull()) {
                            total++;
                            if (inHeap(objPtr)) {
                                // Quick check: does the object have a module vtable?
                                const vtable = objPtr.readPointer();
                                if (inModule(vtable)) {
                                    const nameIdx = objPtr.add(0x18).readU32();
                                    if (nameIdx > 0 && nameIdx < 500000) valid++;
                                }
                            }
                        }
                    } catch(_) {}
                }

                if (valid >= total * 0.5 && valid >= 10) { // At least 50% valid, min 10
                    candidates.push({
                        structAddr: range.base.add(off),
                        arrayPtr: ptr,
                        num,
                        max,
                        valid,
                        total,
                        rva: off,
                    });
                    console.log(`  [CANDIDATE] struct=RVA ${hex(off)}  array=${ptr}  num=${num}  max=${max}  valid=${valid}/${total}`);
                }
            } catch(_) {}
        }
    });

    console.log(`[+] Found ${candidates.length} GUObjectArray candidates`);
    globalThis.gaCandidates = candidates;
    return candidates;
}

// ---------------------------------------------------------------------------
// Walk GUObjectArray and find display character by checking for weapons
// ---------------------------------------------------------------------------
function walkObjects(candidate) {
    if (!candidate) {
        if (!globalThis.gaCandidates || globalThis.gaCandidates.length === 0) {
            console.log(`[-] Run findGObjectArray() first`);
            return;
        }
        candidate = globalThis.gaCandidates[0];
    }

    console.log(`\n[*] Walking ${candidate.num} objects from array @ ${candidate.arrayPtr}...`);

    const results = [];
    let checked = 0;

    for (let i = 0; i < candidate.num && results.length < 20; i++) {
        const itemAddr = candidate.arrayPtr.add(i * 24);
        try {
            const objPtr = itemAddr.readPointer();
            if (objPtr.isNull()) continue;
            if (!inHeap(objPtr)) continue;

            checked++;
            const vtable = objPtr.readPointer();
            if (!inModule(vtable)) continue;

            const nameIdx = objPtr.add(0x18).readU32();
            const classPtr = objPtr.add(0x10).readPointer();
            let classNameIdx = 0;
            try { classNameIdx = classPtr.add(0x18).readU32(); } catch(_) {}

            // Check for weapon pointers in 0x380-0x410 range
            let weapons = [];
            for (let off = 0x380; off <= 0x410; off += 8) {
                try {
                    const wpn = objPtr.add(off).readPointer();
                    if (inHeap(wpn)) {
                        const wpnVtable = wpn.readPointer();
                        if (inModule(wpnVtable)) {
                            const wpnName = wpn.add(0x18).readU32();
                            if (wpnName > 0 && wpnName < 500000) {
                                weapons.push({ off, addr: wpn, nameIdx: wpnName });
                            }
                        }
                    }
                } catch(_) {}
            }

            // Check for FNames in role config area
            let fnames = [];
            for (let off = 0x380; off <= 0x400; off += 8) {
                const idx = objPtr.add(off).readU32();
                const num = objPtr.add(off + 4).readU32();
                if (idx > 10 && idx < 500000 && num < 1000) {
                    fnames.push({ off, idx, num });
                }
            }

            if (weapons.length >= 2 || (weapons.length >= 1 && fnames.length >= 3)) {
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
                console.log(`  [#${i}] @ ${objPtr} nameIdx=${nameIdx} classIdx=${classNameIdx} weapons=${weapons.length} fnames=${fnames.length}`);
                for (const w of weapons) console.log(`    wpn@+${hex(w.off)}: ${w.addr} (nameIdx=${w.nameIdx})`);
            }
        } catch(_) {}
    }

    console.log(`[+] Checked ${checked} valid objects, found ${results.length} candidates`);
    globalThis.objResults = results;
    return results;
}

// ---------------------------------------------------------------------------
// Dump a specific object
// ---------------------------------------------------------------------------
function dumpObj(addr, size) {
    size = size || 0x500;
    console.log(`\n[*] Dump @ ${addr}:`);
    try {
        console.log(`  vtable: ${addr.readPointer()} (RVA ${rva(addr.readPointer())})`);
        console.log(`  Flags: ${hex(addr.add(0x8).readU32())}`);
        console.log(`  Class: ${addr.add(0x10).readPointer()}`);
        console.log(`  Name idx: ${addr.add(0x18).readU32()}`);
        console.log(`  Outer: ${addr.add(0x20).readPointer()}`);

        console.log(`\n  Header (0x0-0x80):`);
        console.log(`  ${hexdump(addr.readByteArray(0x80), {offset:0,length:0x80,header:false,ansi:true})}`);
        console.log(`\n  RoleConfig area (0x380-0x440):`);
        console.log(`  ${hexdump(addr.add(0x380).readByteArray(0xC0), {offset:0x380,length:0xC0,header:false,ansi:true})}`);
    } catch(e) {
        console.log(`  Error: ${e.message}`);
    }
}

// ---------------------------------------------------------------------------
// Auto pipeline
// ---------------------------------------------------------------------------
function auto() {
    const cands = findGObjectArray();
    if (cands.length === 0) return;
    return walkObjects(cands[0]);
}

console.log(`\n[READY] Commands:`);
console.log(`  auto()              - Find GUObjectArray → walk objects → find characters`);
console.log(`  findGObjectArray()  - Just find the array`);
console.log(`  walkObjects(cand)   - Walk a specific candidate`);
console.log(`  dumpObj(addr)       - Dump an object's memory`);
console.log(`\n`);
