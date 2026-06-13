// =============================================================================
// Session 49: Find APBDisplayCharacter via GUObjectArray scan
//
// UE4 tracks all UObjects in a global array (GUObjectArray/FUObjectArray).
// Each entry (FObjectItem) is 24 bytes: {Object*, Flags, ClusterIdx, SerialNum}.
// The array itself is stored in the game module's .data section.
//
// Strategy:
//  1. Scan .data section for FUObjectArray-like structure
//  2. Walk all UObjects to find APBDisplayCharacter or PBShowRoomManager
//  3. Read RoleConfig and DisplayWeapon pointers directly
// =============================================================================

const BASE = Process.getModuleByName("ProjectBoundarySteam-Win64-Shipping.exe").base;
function hex(n) { return n instanceof NativePointer ? n.toString() : "0x" + n.toString(16); }

console.log(`[+] BASE = ${BASE}`);

// ---------------------------------------------------------------------------
// Step 1: Find GUObjectArray in module .data section
//
// Pattern: a pointer to heap (array of FObjectItems), followed by Num/Max int32s.
// The array should contain thousands of valid UObject pointers.
// ---------------------------------------------------------------------------
function findGUObjectArray() {
    console.log(`\n[*] Searching for GUObjectArray in module data...`);

    // Scan module's writable data sections
    const candidates = [];

    Process.enumerateRanges('rw-').forEach(function(range) {
        if (candidates.length >= 10) return;
        // Only scan the game module's data sections
        if (range.base.compare(BASE) < 0 || range.base.compare(BASE.add(0x35000000)) > 0) return;
        if (range.size < 0x1000 || range.size > 0x1000000) return;

        // Scan for structures that look like {heap_ptr, int32_count, int32_max}
        for (let off = 0; off < Math.min(range.size, 0x100000); off += 8) {
            if (candidates.length >= 10) return;

            try {
                const ptr = range.base.add(off).readPointer();
                if (ptr.isNull()) continue;
                // Must point to heap (above 0x10000000)
                if (ptr.compare(ptr(0x10000000)) < 0) continue;
                if (ptr.compare(ptr(0x7FFFFFFFFFFF)) > 0) continue;

                const numElem = range.base.add(off + 8).readU32();
                const maxElem = range.base.add(off + 12).readU32();

                // Reasonable FUObjectArray sizes: 10000-1000000 objects
                if (numElem < 5000 || numElem > 2000000) continue;
                if (maxElem < numElem || maxElem > 5000000) continue;

                // Verify: read first few entries to check if they're valid UObjects
                let validObjs = 0;
                let totalChecked = 0;
                for (let i = 0; i < Math.min(numElem, 20); i++) {
                    const itemAddr = ptr.add(i * 24);
                    try {
                        const objPtr = itemAddr.readPointer();
                        if (!objPtr.isNull() && objPtr.compare(ptr(0x10000000)) > 0) {
                            // Check if it has a valid vtable (pointing to module)
                            const vtable = objPtr.readPointer();
                            if (vtable.compare(BASE) > 0 && vtable.compare(BASE.add(0x35000000)) < 0) {
                                validObjs++;
                            }
                        }
                        totalChecked++;
                    } catch(_) {}
                }

                if (validObjs >= Math.min(totalChecked, 10) * 0.8) { // 80% valid
                    candidates.push({
                        arrayAddr: ptr,
                        structAddr: range.base.add(off),
                        numElem,
                        maxElem,
                        validObjs,
                        totalChecked,
                    });
                    console.log(`  [CANDIDATE] struct @ ${range.base.add(off)}  array=${ptr}  num=${numElem}  valid=${validObjs}/${totalChecked}`);
                    if (candidates.length >= 10) return;
                }
            } catch(_) {}
        }
    });

    console.log(`[+] Found ${candidates.length} GUObjectArray candidates`);
    return candidates;
}

// ---------------------------------------------------------------------------
// Step 2: Search the GUObjectArray for objects by vtable or by name
// ---------------------------------------------------------------------------
function searchObjects(candidates) {
    if (!candidates || candidates.length === 0) {
        console.log(`[-] No GUObjectArray candidates. Run findGUObjectArray() first.`);
        return;
    }

    const best = candidates[0];
    console.log(`\n[*] Searching ${best.numElem} objects for display characters...`);

    const results = {
        displayChars: [],
        showroomMgrs: [],
        weapons: [],
        all: []
    };

    // Walk all objects (sampling every Nth for large arrays)
    const step = best.numElem > 100000 ? 1 : 1; // Check all for moderate sizes
    let checked = 0;

    for (let i = 0; i < best.numElem && checked < 50000; i += step) {
        const itemAddr = best.arrayAddr.add(i * 24);
        try {
            const objPtr = itemAddr.readPointer();
            if (objPtr.isNull()) { checked++; continue; }

            const vtable = objPtr.readPointer();
            const inModule = vtable.compare(BASE) > 0 && vtable.compare(BASE.add(0x35000000)) < 0;
            if (!inModule) { checked++; continue; }

            checked++;

            // Try to read the object's name (FName at offset depending on UObject layout)
            // In UE4.27, UObject has: vtable(8) + ObjectFlags(4) + InternalIndex(4) + Class(8) + Name(8) + Outer(8)
            // Name is at offset 0x18 (24 bytes into the object)
            const nameCompIdx = objPtr.add(0x18).readU32();
            const nameNumber = objPtr.add(0x1C).readU32();
            const classPtr = objPtr.add(0x10).readPointer();

            results.all.push({
                addr: objPtr,
                vtable,
                nameCompIdx,
                nameNumber,
                classPtr,
            });

            // Look for objects with specific characteristics
            // Display characters should have weapon pointers
            // Check if offset 0x398-0x3A8 has valid FName-like data (CharacterID?)
            const charIdIdx = objPtr.add(0x3A0).readU32();
            const charIdNum = objPtr.add(0x3A4).readU32();

            if (charIdIdx > 0 && charIdIdx < 500000 && charIdNum < 1000) {
                // This might be a display character — check for weapon pointers
                const firstWpn = objPtr.add(0x3B0).readPointer();
                const secondWpn = objPtr.add(0x3B8).readPointer();
                const roleConfigPtr = objPtr.add(0x398); // approximate

                if (!firstWpn.isNull() && firstWpn.compare(ptr(0x10000000)) > 0) {
                    results.displayChars.push({
                        addr: objPtr,
                        charIdIdx,
                        charIdNum,
                        firstWpn,
                        secondWpn,
                    });
                    console.log(`  [DISPLAY_CHAR] @ ${objPtr}  charIdIdx=${charIdIdx}  firstWpn=${firstWpn}  secondWpn=${secondWpn}`);
                    if (results.displayChars.length >= 5) break;
                }
            }
        } catch(_) {}
    }

    console.log(`[+] Checked ${checked} objects`);
    console.log(`    Display chars: ${results.displayChars.length}`);
    console.log(`    Total valid objects: ${results.all.length}`);

    return results;
}

// ---------------------------------------------------------------------------
// Step 3: Dump RoleConfig and weapon data from a display character
// ---------------------------------------------------------------------------
function dumpDisplayChar(charAddr) {
    console.log(`\n[*] Dumping display character @ ${charAddr}:`);

    try {
        // Read 0x100 bytes of the object header
        console.log(`  Object header (0x0-0x100):`);
        console.log(`  ${hexdump(charAddr.readByteArray(0x100), {offset:0,length:0x100,header:false,ansi:true})}`);

        // Read RoleConfig area (around 0x398-0x400)
        console.log(`  RoleConfig area (0x390-0x430):`);
        console.log(`  ${hexdump(charAddr.add(0x390).readByteArray(0xA0), {offset:0x390,length:0xA0,header:false,ansi:true})}`);

        // Try to read Name at offset 0x18
        const nameIdx = charAddr.add(0x18).readU32();
        const nameNum = charAddr.add(0x1C).readU32();
        console.log(`  Object Name: CompIdx=${nameIdx} Number=${nameNum}`);
        console.log(`  Object Class Ptr: ${charAddr.add(0x10).readPointer()}`);
        console.log(`  Object Outer Ptr: ${charAddr.add(0x20).readPointer()}`);

        // Try reading potential weapon pointers
        for (let off = 0x398; off <= 0x3E0; off += 8) {
            try {
                const p = charAddr.add(off).readPointer();
                if (!p.isNull() && p.compare(ptr(0x10000000)) > 0 && p.compare(ptr(0x7FFFFFFFFFFF)) < 0) {
                    console.log(`  [+${hex(off)}] ptr → ${p}`);
                }
            } catch(_) {}
        }

    } catch(e) {
        console.log(`  Error: ${e.message}`);
    }
}

// ---------------------------------------------------------------------------
// Main pipeline
// ---------------------------------------------------------------------------
function auto() {
    const candidates = findGUObjectArray();
    if (candidates.length === 0) return;

    const results = searchObjects(candidates);
    globalThis.results = results;
    globalThis.candidates = candidates;

    if (results && results.displayChars.length > 0) {
        console.log(`\n[!!!] Found display characters! Dump with: dumpDisplayChar(ptr)`);
        console.log(`  First: dumpDisplayChar(ptr("${results.displayChars[0].addr}"))`);
    }
}

console.log(`\n[READY] Commands:`);
console.log(`  auto()          - Full pipeline: find GUObjectArray → find display chars`);
console.log(`  findGUObjectArray() - Step 1: locate the global object array`);
console.log(`  searchObjects(candidates) - Step 2: find display characters`);
console.log(`  dumpDisplayChar(addr) - Step 3: dump display char memory`);
console.log(`\n`);
