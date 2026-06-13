// =============================================================================
// Session 50: Find display character by scanning heap for UObject instances
//
// Every UObject starts with a vtable pointer pointing into the game module.
// We can scan heap for these pointers to find ALL UObjects, then filter
// for display characters by looking for weapon-like FNames at known offsets.
// =============================================================================

const BASE = Process.getModuleByName("ProjectBoundarySteam-Win64-Shipping.exe").base;
function hex(n) { return n instanceof NativePointer ? n.toString() : "0x" + n.toString(16); }

const MODULE_END = BASE.add(0x35000000); // 850MB module

console.log(`[+] BASE = ${BASE}`);
console.log(`[+] MODULE_END = ${MODULE_END}`);

// ---------------------------------------------------------------------------
// Check if a pointer points into the game module
// ---------------------------------------------------------------------------
function inModule(p) {
    return p.compare(BASE) > 0 && p.compare(MODULE_END) < 0;
}

// ---------------------------------------------------------------------------
// Check if a value looks like a valid FName ComparisonIndex
// ---------------------------------------------------------------------------
function isValidFNameIndex(idx, num) {
    return idx > 0 && idx < 500000 && num < 10000;
}

// ---------------------------------------------------------------------------
// Scan heap for UObjects with weapon/character-like fields
// ---------------------------------------------------------------------------
function findCharacters() {
    console.log(`\n[*] Scanning heap for display character objects...`);
    console.log(`[*] This scans ALL heap memory — may take 30-60 seconds...`);

    const candidates = [];
    let totalObjs = 0;

    Process.enumerateRanges('rw-').forEach(function(range) {
        if (candidates.length >= 10) return;
        if (range.size < 0x10000) return;
        const lo = range.base.and(ptr(0xFFFFFFFF)).toUInt32();
        if (lo < 0x10000000) return; // heap typically above 256MB

        // Sample: check every 8 bytes
        for (let p = range.base; p.compare(range.base.add(range.size)) < 0 && candidates.length < 10; p = p.add(0x8000)) {
            // Read a chunk
            for (let off = 0; off < 0x8000 && candidates.length < 10; off += 8) {
                try {
                    const addr = p.add(off);
                    const vtable = addr.readPointer();

                    // Check if vtable points to module
                    if (!inModule(vtable)) continue;

                    totalObjs++;

                    // Read potential FNames at various offsets
                    // UE4 UObject layout: vtable(8), Flags(4), Index(4), Class(8), Name(8), Outer(8)
                    // Name is at offset 0x18 (FName: CompIdx + Number)
                    const nameIdx = addr.add(0x18).readU32();
                    const nameNum = addr.add(0x1C).readU32();

                    if (!isValidFNameIndex(nameIdx, nameNum)) continue;

                    // Look for character-specific data:
                    // Check offsets 0x390-0x400 for RoleConfig-like FNames
                    // CharacterID would be an FName at some offset
                    let hasCharId = false;
                    let charIdOff = -1;

                    // Scan for FName-like values at character-specific offsets
                    for (let charOff = 0x380; charOff <= 0x3F0; charOff += 8) {
                        const idx = addr.add(charOff).readU32();
                        const num = addr.add(charOff + 4).readU32();
                        if (isValidFNameIndex(idx, num) && idx > 100) {
                            // Found a valid FName — could be CharacterID
                            if (!hasCharId) {
                                hasCharId = true;
                                charIdOff = charOff;
                            }
                        }
                    }

                    // Check for weapon pointers (DisplayFirstWeapon, DisplaySecondWeapon)
                    let weaponPtrs = 0;
                    const weaponPtrOffs = [];
                    for (let wpnOff = 0x398; wpnOff <= 0x3F0; wpnOff += 8) {
                        const wpn = addr.add(wpnOff).readPointer();
                        if (!wpn.isNull() && wpn.compare(ptr(0x10000000)) > 0 && wpn.compare(ptr(0x7FFFFFFFFFFF)) < 0) {
                            // Verify this is likely a UObject (has module vtable)
                            try {
                                const wpnVtable = wpn.readPointer();
                                if (inModule(wpnVtable)) {
                                    weaponPtrs++;
                                    weaponPtrOffs.push({ off: wpnOff, ptr: wpn });
                                }
                            } catch(_) {}
                        }
                    }

                    // A display character should have at least 1-2 weapon pointers
                    // and a valid CharacterID FName
                    if (weaponPtrs >= 1 && hasCharId) {
                        candidates.push({
                            addr,
                            vtable,
                            nameIdx,
                            nameNum,
                            charIdOff,
                            weaponPtrs: weaponPtrOffs,
                        });
                        console.log(`\n  [DISPLAY_CHAR?] @ ${addr}`);
                        console.log(`    vtable = ${vtable}`);
                        console.log(`    Name: idx=${nameIdx} num=${nameNum}`);
                        console.log(`    CharId at +${hex(charIdOff)}`);
                        for (const w of weaponPtrOffs) {
                            console.log(`    WeaponPtr at +${hex(w.off)} → ${w.ptr}`);
                        }
                    }
                } catch(_) {}
            }
        }
    });

    console.log(`\n[+] Scanned ${totalObjs} potential UObjects`);
    console.log(`[+] Found ${candidates.length} display character candidates`);
    return candidates;
}

// ---------------------------------------------------------------------------
// Deep analysis of a candidate: dump memory and trace all fields
// ---------------------------------------------------------------------------
function analyzeChar(addr) {
    console.log(`\n[*] Deep analysis of character @ ${addr}:`);
    console.log(`[*] Dumping 0x500 bytes of object memory...`);

    try {
        const data = addr.readByteArray(0x500);
        const u8 = new Uint8Array(data);

        // Find all FNames in the object
        console.log(`\n  FName fields (CompIdx > 0, < 500000):`);
        for (let off = 0; off < 0x500 - 8; off += 4) {
            const idx = new DataView(data, off, 4).getUint32(0, true);
            const num = new DataView(data, off + 4, 4).getUint32(0, true);
            if (isValidFNameIndex(idx, num) && idx > 10) {
                console.log(`    +${hex(off)}: idx=${idx} num=${num}`);
            }
        }

        // Find all pointers
        console.log(`\n  Pointer fields (pointing to heap):`);
        for (let off = 0; off < 0x500 - 8; off += 8) {
            const ptr = new DataView(data, off, 8).getBigUint64(0, true);
            if (ptr > 0x10000000n && ptr < 0x7FFFFFFFFFFFn) {
                const p = ptr(Number(ptr));
                try {
                    const peek = p.readPointer();
                    if (inModule(peek)) {
                        console.log(`    +${hex(off)} → ${p} (points to UObject, vtable=${peek})`);
                    } else {
                        console.log(`    +${hex(off)} → ${p} (data pointer)`);
                    }
                } catch(_) {
                    console.log(`    +${hex(off)} → ${p}`);
                }
            }
        }

        // Dump hex around key areas
        console.log(`\n  Object header (0x0-0x80):`);
        console.log(`  ${hexdump(addr.readByteArray(0x80), {offset:0,length:0x80,header:false,ansi:true})}`);

        console.log(`\n  RoleConfig area (0x380-0x440):`);
        console.log(`  ${hexdump(addr.add(0x380).readByteArray(0xC0), {offset:0x380,length:0xC0,header:false,ansi:true})}`);

    } catch(e) {
        console.log(`  Error: ${e.message}`);
    }
}

// ---------------------------------------------------------------------------
// Also: dump a weapon object to understand its structure
// ---------------------------------------------------------------------------
function analyzeWeapon(addr) {
    console.log(`\n[*] Deep analysis of weapon @ ${addr}:`);
    try {
        const data = addr.readByteArray(0x300);
        console.log(`  Object header (0x0-0x100):`);
        console.log(`  ${hexdump(addr.readByteArray(0x100), {offset:0,length:0x100,header:false,ansi:true})}`);

        console.log(`\n  Weapon-specific area (0x100-0x300):`);
        console.log(`  ${hexdump(addr.add(0x100).readByteArray(0x200), {offset:0x100,length:0x200,header:false,ansi:true})}`);

        // Look for ItemId (FName at some offset)
        for (let off = 0; off < 0x300 - 8; off += 4) {
            const idx = new DataView(data, off, 4).getUint32(0, true);
            const num = new DataView(data, off + 4, 4).getUint32(0, true);
            if (isValidFNameIndex(idx, num) && idx > 100) {
                console.log(`  FName at +${hex(off)}: idx=${idx} num=${num}`);
            }
        }
    } catch(e) {
        console.log(`  Error: ${e.message}`);
    }
}

console.log(`\n[READY] Commands:`);
console.log(`  findCharacters()  - Scan heap for display character objects`);
console.log(`  analyzeChar(addr)  - Deep-dump a character object`);
console.log(`  analyzeWeapon(addr) - Deep-dump a weapon object`);
console.log(`\n`);
