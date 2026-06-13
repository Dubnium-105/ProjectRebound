// =============================================================================
// Session 51: Find REAL UObjects by verifying Class pointer at offset 0x10
//
// Real UObject layout: vtable(8), Flags(4), Index(4), ClassPtr(8), Name(8), Outer(8)
// The Class pointer at offset 0x10 must point to a UClass object.
// A UClass object itself has a valid layout with a ClassName FName.
// =============================================================================

const BASE = Process.getModuleByName("ProjectBoundarySteam-Win64-Shipping.exe").base;
function hex(n) { return n instanceof NativePointer ? n.toString() : "0x" + n.toString(16); }
function rva(p) { return "0x" + p.sub(BASE).toInt32().toString(16); }

console.log(`[+] BASE = ${BASE}`);

function inHeap(p) { return p.compare(ptr(0x10000000)) > 0 && p.compare(ptr(0x7FFFFFFFFFFF)) < 0; }
function inModule(p) { return p.compare(BASE) > 0 && p.compare(BASE.add(0x35000000)) < 0; }
function validFName(idx, num) { return idx > 0 && idx < 500000 && num < 10000; }

// Cache: already verified UObject addresses to avoid re-checking
const verifiedUObjects = new Set();

function isUObject(addr) {
    if (verifiedUObjects.has(addr.toString())) return true;
    try {
        // Check 1: First 8 bytes should look like a vtable (point to module .rdata or .text)
        const first8 = addr.readPointer();
        if (!inModule(first8)) return false;

        // Check 2: Offset 0x10 should point to a Class object
        const classPtr = addr.add(0x10).readPointer();
        if (!inHeap(classPtr)) return false;

        // Check 3: The Class object should itself be a valid UObject
        const classVtable = classPtr.readPointer();
        if (!inModule(classVtable)) return false;
        const classNameIdx = classPtr.add(0x18).readU32();
        const classNameNum = classPtr.add(0x1C).readU32();
        if (!validFName(classNameIdx, classNameNum)) return false;

        // Check 4: Object's own Name should be valid
        const objNameIdx = addr.add(0x18).readU32();
        const objNameNum = addr.add(0x1C).readU32();
        if (!validFName(objNameIdx, objNameNum)) return false;

        verifiedUObjects.add(addr.toString());
        return true;
    } catch(_) {
        return false;
    }
}

// ---------------------------------------------------------------------------
// Scan all heap objects and find display characters
// ---------------------------------------------------------------------------
function findRealCharacters() {
    console.log(`\n[*] Scanning heap for REAL UObjects (verified Class pointer)...`);

    const chars = [];
    let totalObjs = 0;
    let realObjs = 0;

    Process.enumerateRanges('rw-').forEach(function(range) {
        if (chars.length >= 10) return;
        if (range.size < 0x100) return;
        if (!inHeap(range.base)) return;

        // Read chunks, looking for UObjects
        for (let off = 0; off < Math.min(range.size, 0x1000000); off += 0x4000) {
            if (chars.length >= 10) return;
            try {
                const chunkBase = range.base.add(off);
                // Sample every 8 bytes in the chunk
                for (let subOff = 0; subOff < 0x4000 && chars.length < 10; subOff += 8) {
                    const addr = chunkBase.add(subOff);
                    totalObjs++;
                    if (totalObjs > 200000) return; // safety limit

                    if (!isUObject(addr)) continue;
                    realObjs++;

                    // This is a real UObject. Check if it's a display character.
                    // Display characters have weapon pointers at offsets 0x390-0x400
                    let weaponCount = 0;
                    const weapons = [];

                    for (let wpnOff = 0x390; wpnOff <= 0x410; wpnOff += 8) {
                        const wpn = addr.add(wpnOff).readPointer();
                        if (inHeap(wpn)) {
                            try {
                                // Check if it's a weapon (has ItemId FName)
                                const itemIdx = wpn.add(0x18).readU32();
                                if (validFName(itemIdx, 0)) {
                                    const wpnVtable = wpn.readPointer();
                                    if (inModule(wpnVtable)) {
                                        weaponCount++;
                                        weapons.push({ off: wpnOff, addr: wpn, nameIdx: itemIdx });
                                    }
                                }
                            } catch(_) {}
                        }
                    }

                    // Check for RoleConfig-like FName at offsets 0x380-0x400
                    let hasRoleConfig = false;
                    let roleConfigOff = -1;
                    for (let rcOff = 0x380; rcOff <= 0x3F8; rcOff += 8) {
                        const idx = addr.add(rcOff).readU32();
                        const num = addr.add(rcOff + 4).readU32();
                        if (validFName(idx, num) && idx > 50) {
                            hasRoleConfig = true;
                            roleConfigOff = rcOff;
                            break;
                        }
                    }

                    const nameIdx = addr.add(0x18).readU32();
                    const classNameIdx = addr.add(0x10).readPointer().add(0x18).readU32();

                    if (weaponCount >= 2 || (weaponCount >= 1 && hasRoleConfig)) {
                        chars.push({
                            addr,
                            nameIdx,
                            classNameIdx,
                            classPtr: addr.add(0x10).readPointer(),
                            weaponCount,
                            weapons,
                            roleConfigOff,
                        });
                        console.log(`\n  [CHAR#${chars.length}] @ ${addr}`);
                        console.log(`    Name: idx=${nameIdx}  ClassName: idx=${classNameIdx}`);
                        console.log(`    Weapons (${weaponCount}):`);
                        for (const w of weapons) {
                            console.log(`      +${hex(w.off)} → ${w.addr} (nameIdx=${w.nameIdx})`);
                        }
                        if (hasRoleConfig) console.log(`    RoleConfig at +${hex(roleConfigOff)}`);
                    }
                }
            } catch(_) {}
        }
    });

    console.log(`\n[+] Scanned ${totalObjs} addresses, found ${realObjs} real UObjects`);
    console.log(`[+] Display characters: ${chars.length}`);
    return chars;
}

// ---------------------------------------------------------------------------
// Dump a UObject in detail
// ---------------------------------------------------------------------------
function dumpObj(addr, size) {
    size = size || 0x500;
    console.log(`\n[*] Dump of UObject @ ${addr}:`);
    try {
        // Verify it's a UObject
        const vtable = addr.readPointer();
        const classPtr = addr.add(0x10).readPointer();
        const classNameIdx = classPtr.add(0x18).readU32();
        const objNameIdx = addr.add(0x18).readU32();

        console.log(`  vtable    = ${vtable} (RVA ${rva(vtable)})`);
        console.log(`  Class     = ${classPtr} (className idx=${classNameIdx})`);
        console.log(`  Name      = idx=${objNameIdx}`);
        console.log(`  Outer     = ${addr.add(0x20).readPointer()}`);

        // Hex dump header
        console.log(`\n  Header (0x0-0x80):`);
        console.log(`  ${hexdump(addr.readByteArray(0x80), {offset:0,length:0x80,header:false,ansi:true})}`);

        // Hex dump role config area
        console.log(`\n  RoleConfig/Weapon area (0x380-0x440):`);
        console.log(`  ${hexdump(addr.add(0x380).readByteArray(0xC0), {offset:0x380,length:0xC0,header:false,ansi:true})}`);

        // List all valid FNames in the object
        console.log(`\n  All FName fields in object:`);
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
console.log(`  findRealCharacters()  - Find display characters among real UObjects`);
console.log(`  dumpObj(addr)          - Detailed dump of any UObject`);
console.log(`  isUObject(addr)        - Verify if an address is a real UObject`);
console.log(`\n`);
