// =============================================================================
// Session 20: Find DisplayCharacter RoleConfig for Cheat Engine
//
// Usage: frida -p <PID> -l tools/session20_find_chars.js
//        Enter armory → auto-scan → note addresses → Cheat Engine
// =============================================================================

const BASE = (() => {
    for (const n of ["ProjectBoundarySteam-Win64-Shipping.exe","ProjectBoundary-Win64-Shipping.exe"])
        { try { return Process.getModuleByName(n).base; } catch(_){} }
    for (const m of Process.enumerateModules())
        { if (m.name.endsWith('.exe') && m.name.toLowerCase().includes('boundary')) return m.base; }
    throw new Error("Module not found");
})();

function hex(n) { return n instanceof NativePointer ? n.toString() : "0x" + n.toString(16); }

const BASE_LO = BASE.and(ptr(0xFFFFFFFF)).toUInt32();
const BASE_HI = BASE.shr(32).and(ptr(0xFFFFFFFF)).toUInt32();

function isValidVTable(vtLo, vtHi) {
    // VTable must be within game module: BASE to BASE+0x20000000
    if (vtHi !== BASE_HI) {
        // Accept any high word that maps to the same general 64-bit region
        if (vtHi < BASE_HI || vtHi > BASE_HI + 0x2) return false;
    }
    if (vtLo < BASE_LO) return false;
    if (vtLo > BASE_LO + 0x20000000) return false;
    return true;
}

const handlerAddr = BASE.add(0x9C48B0);
let foundChars = [];
let scanned = false;

function doScan() {
    if (scanned) { console.log("[*] Already scanned. Run reset() first."); return; }
    scanned = true;

    console.log("[*] Scanning heap pages for DisplayCharacter objects...");
    let pagesChecked = 0;
    let totalFound = 0;

    Process.enumerateRanges('rw-').forEach(function(range) {
        if (totalFound >= 10) return;

        // Skip small ranges and non-heap regions
        if (range.size < 0x100000) return; // < 1MB
        const lo = range.base.and(ptr(0xFFFFFFFF)).toUInt32();
        if (lo < 0x10000000) return; // low memory (stack, etc.)

        // Scan every 0x40 pages (64 * 4KB = 256KB stride)
        const rangeEnd = range.base.add(range.size);
        for (let p = range.base; p.compare(rangeEnd.sub(0x1000)) < 0 && totalFound < 10; p = p.add(0x40000)) {
            try {
                const buf = p.readByteArray(0x1000);
                if (!buf) continue;
                pagesChecked++;

                const dv = new DataView(buf);
                for (let off = 0; off < 0x1000 - 0x3A0 - 0x40; off += 0x10) {
                    // Read potential vtable (64-bit) at off
                    const vtLo = dv.getUint32(off, true);
                    const vtHi = dv.getUint32(off + 4, true);
                    if (vtLo === 0 && vtHi === 0) continue;
                    if (!isValidVTable(vtLo, vtHi)) continue;

                    // Check CharID FName at off + 0x3A0
                    const rc = off + 0x3A0;
                    if (rc + 8 > 0x1000) continue;
                    const cid = dv.getUint32(rc, true);
                    if (cid < 1 || cid > 50000) continue;
                    if (dv.getUint32(rc + 4, true) > 1000) continue;

                    // Check WeaponID at rc + 0x30 + 0x28 = rc + 0x58
                    const wo = rc + 0x58;
                    if (wo + 4 > 0x1000) continue;
                    const wid = dv.getUint32(wo, true);
                    if (wid < 1 || wid > 50000) continue;

                    // Found!
                    const charAddr = p.add(off);
                    const roleAddr = charAddr.add(0x3A0);
                    if (!foundChars.find(fc => fc.addr.equals(charAddr))) {
                        foundChars.push({ addr: charAddr, roleAddr, cid, wid });
                        totalFound++;
                        console.log(`[#${totalFound}] DisplayChar @ ${charAddr}  RoleConfig @ ${roleAddr}  CID=${cid} WID=${wid}`);
                    }

                    // Skip past this object
                    off += 0x400;
                }
            } catch (_) {}
        }
    });

    console.log(`\nScanned ${pagesChecked} pages, found ${foundChars.length} DisplayCharacters`);

    if (foundChars.length > 0) {
        console.log(`\n=== Copy these addresses to Cheat Engine ===`);
        for (let i = 0; i < foundChars.length; i++) {
            const dc = foundChars[i];
            console.log(`  ${dc.roleAddr}  ← RoleConfig[${i}] (CE: Add Address Manually)`);
        }
        console.log(`\nCheat Engine steps:`);
        console.log(`  1. Attach to game`);
        console.log(`  2. Add Address Manually → paste one address above`);
        console.log(`  3. Right-click → Find out what writes to this address`);
        console.log(`  4. Re-enter armory → the overwriter appears!`);
    } else {
        console.log(`\n[!] No chars found. Try again after entering armory.`);
        scanned = false;
    }
}

// Auto-trigger
let hits = 0;
Interceptor.attach(handlerAddr, {
    onEnter(args) { hits++; if (hits === 50 && !scanned) { console.log(`[*] Auto-scanning...`); doScan(); } }
});

function reset() { scanned = false; foundChars = []; hits = 0; console.log("[+] Reset."); }

console.log(`[*] Enter armory — auto-scans after handler warms up.`);
console.log(`[*] Manual: doScan()  Reset: reset()\n`);
