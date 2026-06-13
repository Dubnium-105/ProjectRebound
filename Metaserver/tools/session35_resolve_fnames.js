// =============================================================================
// Session 35: Resolve FName indices using the real GName pool at 0x5D29280
//             Then scan memory for loadout cache clusters
// =============================================================================

const BASE = Process.getModuleByName("ProjectBoundarySteam-Win64-Shipping.exe").base;
function hex(n) { return n instanceof NativePointer ? n.toString() : "0x" + n.toString(16); }

const NAMEPOOL_RVA = 0x5D29280;
const poolAddr = BASE.add(NAMEPOOL_RVA);
console.log(`[+] NamePool @ ${poolAddr}`);

// Resolve FName ComparisonIndex to string
function resolveFName(compIdx) {
    if (compIdx < 1 || compIdx > 200000) return null;
    try {
        const blockIdx = (compIdx >> 16) & 0xFFFF;
        const offset   = compIdx & 0xFFFF;

        // Read block pointer: pool[blockIdx + 2]
        const blockPtr = poolAddr.add((blockIdx + 2) * 8).readPointer();
        if (blockPtr.isNull()) return null;

        // Entry at: blockPtr + 2 * offset
        const entryAddr = blockPtr.add(2 * offset);
        // Read 2-byte header: length in top 6 bits (>> 10? No, >> 6)
        const header = entryAddr.readU16();
        const len = header >> 6;  // length in characters
        if (len === 0 || len > 128) return null;

        const strAddr = entryAddr.add(2);
        return strAddr.readCString(len);
    } catch (_) { return null; }
}

// Test resolution
const testIndices = [2, 4, 5, 16, 49, 255, 1];
console.log(`\n[*] Testing FName resolution:`);
for (const idx of testIndices) {
    const s = resolveFName(idx);
    if (s) console.log(`  idx=${idx} → "${s}"`);
}

// Quick scan for loadout data
function scanCache() {
    console.log(`\n[*] Scanning heap for loadout FName clusters...`);
    let found = [];

    Process.enumerateRanges('rw-').forEach(function(range) {
        if (found.length >= 10) return;
        if (range.size < 0x10000) return;
        const lo = range.base.and(ptr(0xFFFFFFFF)).toUInt32();
        if (lo < 0x10000000) return;

        for (let p = range.base; p.compare(range.base.add(range.size)) < 0 && found.length < 10; p = p.add(0x40000)) {
            try {
                const buf = p.readByteArray(0x1000);
                if (!buf) continue;
                const dv = new DataView(buf);

                for (let off = 0; off < 0x1000 - 64; off += 8) {
                    let fnames = [];
                    for (let slot = 0; slot < 15; slot++) {
                        const idx = dv.getUint32(off + slot * 8, true);
                        const num = dv.getUint32(off + slot * 8 + 4, true);
                        if (idx > 0 && idx < 200000 && num < 1000) {
                            fnames.push(idx);
                        } else break;
                    }
                    if (fnames.length >= 6) {
                        const addr = p.add(off);
                        let names = fnames.slice(0, 10).map(i => resolveFName(i) || `?(${i})`).join(', ');
                        console.log(`  [#${found.length+1}] ${addr}: ${fnames.length} FNames [${names}]`);
                        found.push({addr, fnames});
                        if (found.length >= 10) return;
                        off += fnames.length * 8 + 0x100;
                    }
                }
            } catch(_) {}
        }
    });
    console.log(`[+] Found ${found.length} candidates\n`);
    return found;
}

console.log(`\n[*] Commands: resolveFName(idx), scanCache()`);
console.log(`[*] Enter armory, then scanCache()\n`);
