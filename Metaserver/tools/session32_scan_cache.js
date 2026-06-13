// =============================================================================
// Session 32: Scan for loadout cache in memory
//
// 1. Find GName pool via SDK offset 0x05D29C80
// 2. Build FName→string resolver
// 3. Scan all writable memory for clustered FName patterns (loadout data)
// 4. Dump candidate cache locations with human-readable role/weapon names
//
// Usage: frida -p <PID> -l tools/session32_scan_cache.js
// =============================================================================

const BASE = (() => {
    for (const n of ["ProjectBoundarySteam-Win64-Shipping.exe","ProjectBoundary-Win64-Shipping.exe"])
        { try { return Process.getModuleByName(n).base; } catch(_){} }
    for (const m of Process.enumerateModules())
        { if (m.name.endsWith('.exe') && m.name.toLowerCase().includes('boundary')) return m.base; }
    throw new Error("Module not found");
})();

function hex(n) { return n instanceof NativePointer ? n.toString() : "0x" + n.toString(16); }

const GNAMES_RVA = 0x05D29C80;

// =============================================================================
// Phase 1: Locate GName pool and build a string resolver
// =============================================================================

let namePool = null;

function initNamePool() {
    try {
        const gnamesAddr = BASE.add(GNAMES_RVA);
        console.log(`[+] GNames global @ ${gnamesAddr}`);

        // Read first 8 bytes — may be a pointer or inline data
        const val = gnamesAddr.readPointer();
        console.log(`[+] GNames value: ${val}`);

        // Try different UE4 name pool formats
        // Format 1: Direct pointer to FNamePool
        // Format 2: GNamesAddr itself is the start of the pool
        // Format 3: Pointer to pointer

        // Try reading as direct pointer first
        if (!val.isNull() && val.compare(ptr(0x10000)) > 0) {
            namePool = val;
            console.log(`[+] NamePool at ${namePool}`);
            return true;
        }

        // Maybe it's the pool itself (inline)
        namePool = gnamesAddr;
        console.log(`[+] Trying GNames as inline pool @ ${namePool}`);
        return true;
    } catch (e) {
        console.log(`[!] NamePool init failed: ${e.message}`);
        return false;
    }
}

// Try to read FName string for a given ComparisonIndex
// UE4 FName format: each entry has a string stored with size prefix
function tryReadFNameString(compIdx) {
    if (!namePool || compIdx < 1 || compIdx > 100000) return null;
    try {
        // In UE4.27 FNamePool:
        // - Each block has 0x4000 entries
        // - Entry size varies (strings are variable-length)
        // Simple approach: try reading from known offset patterns

        // Most common: CompIdx * 2 + base gives the entry offset within a block
        // But this varies by engine version. Let's try common patterns.

        // Pattern 1: direct entry at namePool + compIdx * some_stride
        // Pattern 2: block-based

        // For now, try reading a string at a few potential offsets
        for (const stride of [2, 4, 8, 16]) {
            try {
                const entryAddr = namePool.add(compIdx * stride);
                // UE strings: first 1-2 bytes are length, then ASCII string
                const len = entryAddr.readU8();
                if (len > 0 && len < 64) {
                    const str = entryAddr.add(1).readCString(len);
                    if (str && /^[A-Za-z0-9_\-]+$/.test(str) && str.length > 1) {
                        return str;
                    }
                }
            } catch (_) {}
        }
        return null;
    } catch (_) { return null; }
}

// =============================================================================
// Phase 2: Scan memory for loadout data patterns
// =============================================================================

function scanForLoadoutCache() {
    console.log(`\n[*] Scanning heap for loadout data clusters...`);
    console.log(`[*] Pattern: 6-10 consecutive valid FName indices per role`);

    let candidates = [];
    let pagesChecked = 0;

    Process.enumerateRanges('rw-').forEach(function(range) {
        if (candidates.length >= 10) return;
        if (range.size < 0x1000) return;

        const lo = range.base.and(ptr(0xFFFFFFFF)).toUInt32();
        if (lo < 0x10000000) return; // skip low memory

        // Sample: every 64th page
        for (let p = range.base; p.compare(range.base.add(range.size)) < 0 && candidates.length < 10; p = p.add(0x40000)) {
            try {
                const buf = p.readByteArray(0x1000);
                if (!buf) continue;
                pagesChecked++;

                const dv = new DataView(buf);

                // Scan for consecutive valid FName indices (uint32, 1-50000)
                for (let off = 0; off < 0x1000 - 80; off += 8) {
                    let fnamesInRow = 0;
                    let fnIndices = [];

                    for (let slot = 0; slot < 20; slot++) {
                        const idx = dv.getUint32(off + slot * 8, true);
                        const num = dv.getUint32(off + slot * 8 + 4, true);
                        if (idx > 0 && idx < 50000 && num < 1000) {
                            fnamesInRow++;
                            fnIndices.push(idx);
                        } else {
                            break;
                        }
                    }

                    // If we found 6+ consecutive valid FNames, this might be role data
                    if (fnamesInRow >= 6) {
                        const baseAddr = p.add(off);
                        // Resolve strings if name pool is available
                        let names = [];
                        if (namePool) {
                            for (const idx of fnIndices.slice(0, 10)) {
                                const s = tryReadFNameString(idx);
                                names.push(s || `?(${idx})`);
                            }
                        }

                        candidates.push({
                            addr: baseAddr,
                            count: fnamesInRow,
                            indices: fnIndices.slice(0, 10),
                            names: names,
                        });

                        if (candidates.length <= 10) {
                            const nameStr = names.length > 0 ? ` [${names.join(', ')}]` : '';
                            console.log(`  [#${candidates.length}] ${baseAddr}: ${fnamesInRow} FNames${nameStr}`);
                            if (names.length === 0) {
                                console.log(`    indices: [${fnIndices.slice(0, 10).join(', ')}]`);
                            }
                        }

                        // Skip past this region
                        off += fnamesInRow * 8 + 0x100;
                    }
                }
            } catch (_) {}
        }
    });

    console.log(`\n[+] Scanned ${pagesChecked} pages, found ${candidates.length} candidates`);
    return candidates;
}

// =============================================================================
// Phase 3: Deep-inspect a candidate
// =============================================================================

function inspect(idx) {
    const found = scanForLoadoutCache();
    if (idx >= found.length) { console.log("Invalid index"); return; }
    const c = found[idx];
    console.log(`\n=== Inspecting ${c.addr} (${c.count} FNames) ===`);
    // Dump 256 bytes
    console.log(hexdump(c.addr.readByteArray(256), {offset:0,length:256,header:false,ansi:false}));
    // Resolve all FNames
    if (namePool) {
        for (let i = 0; i < Math.min(30, c.count || 10); i++) {
            const idx = c.addr.add(i * 8).readU32();
            const s = tryReadFNameString(idx);
            console.log(`  [${i}] idx=${idx} → ${s || '?'}`);
        }
    }
}

// --- Init ---
initNamePool();
console.log(`\n[*] Commands: scanForLoadoutCache(), inspect(N)`);
console.log(`[*] Enter armory first so loadout data is in memory, then scan.\n`);
