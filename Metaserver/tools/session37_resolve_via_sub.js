// =============================================================================
// Session 37: Resolve FNames by calling sub_19D9570 (FName entry lookup)
//
// sub_19D9570 takes a ComparisonIndex and returns the raw FNameEntry* pointer.
// Entry format: [2-byte header][string data]
//   header bit 0: wide flag (1=UTF-16, 0=ANSI)
//   header >> 6:  string length in characters
//
// Then scan heap for loadout FName clusters.
// =============================================================================

const BASE = Process.getModuleByName("ProjectBoundarySteam-Win64-Shipping.exe").base;
function hex(n) { return n instanceof NativePointer ? n.toString() : "0x" + n.toString(16); }

const RESOLVE_ENTRY_RVA = 0x19D9570;  // sub_19D9570: FNameEntry* resolve(ComparisonIndex)
const POOL_INIT_RVA    = 0x19D3490;   // sub_19D3490: init the name pool
const POOL_READY_RVA   = 0x5D29278;   // byte_5D29278: pool initialized flag
const POOL_BASE_RVA    = 0x5D29280;   // unk_5D29280: pool base

const resolveEntry = new NativeFunction(BASE.add(RESOLVE_ENTRY_RVA), 'pointer', ['pointer', 'pointer', 'pointer', 'pointer']);
const poolReady = BASE.add(POOL_READY_RVA);

console.log(`[+] resolveEntry @ ${BASE.add(RESOLVE_ENTRY_RVA)}`);
console.log(`[+] poolReady   @ ${poolReady}`);

// ---------------------------------------------------------------------------
// Resolve a single ComparisonIndex to string
// ---------------------------------------------------------------------------
const fnameCache = {};

function resolveFName(compIdx) {
    if (compIdx < 1 || compIdx > 500000) return null;
    if (fnameCache[compIdx] !== undefined) return fnameCache[compIdx];

    try {
        // sub_19D9570(a1,a2,a3, &compIdx) returns entry pointer
        const idxBuf = Memory.alloc(8);
        idxBuf.writeU32(compIdx);
        idxBuf.add(4).writeU32(0);

        const entry = resolveEntry(ptr(0), ptr(0), ptr(0), idxBuf);
        if (entry.isNull()) { fnameCache[compIdx] = null; return null; }

        // Parse entry: [2-byte header][string data]
        const header = entry.readU16();
        const len = header >> 6;
        const wide = header & 1;

        if (len === 0 || len > 256) { fnameCache[compIdx] = null; return null; }

        let str;
        if (wide) {
            // UTF-16 (wide) string data
            str = entry.add(2).readUtf16String(len);
        } else {
            // ANSI string data
            str = entry.add(2).readCString(len);
        }

        fnameCache[compIdx] = str || null;
        return fnameCache[compIdx];
    } catch (e) {
        fnameCache[compIdx] = null;
        return null;
    }
}

// ---------------------------------------------------------------------------
// Test: resolve known indices
// ---------------------------------------------------------------------------
function test() {
    console.log(`[*] Pool ready flag: ${poolReady.readU8()}`);
    console.log(`[*] Testing resolution with first 500 indices...`);
    let resolved = 0;
    for (let i = 1; i < 500; i++) {
        const s = resolveFName(i);
        if (s) {
            console.log(`  idx=${i} → "${s}"`);
            resolved++;
            if (resolved >= 20) break;
        }
    }
    console.log(`[+] Resolved ${resolved}/${Math.min(500, Object.keys(fnameCache).length)} tested`);
}

// ---------------------------------------------------------------------------
// Heap scan for FName clusters
// ---------------------------------------------------------------------------
function scanHeap(minClusterSize, maxResults) {
    minClusterSize = minClusterSize || 6;
    maxResults = maxResults || 30;
    console.log(`\n[*] Scanning heap for FName clusters (>=${minClusterSize} consecutive valid FNames)...`);

    const results = [];
    const seen = new Set();

    Process.enumerateRanges('rw-').forEach(function(range) {
        if (results.length >= maxResults) return;
        if (range.size < 0x10000) return;
        const lo = range.base.and(ptr(0xFFFFFFFF)).toUInt32();
        if (lo < 0x10000000) return;

        for (let p = range.base; p.compare(range.base.add(range.size)) < 0 && results.length < maxResults; p = p.add(0x40000)) {
            try {
                const buf = p.readByteArray(0x1000);
                if (!buf) continue;
                const dv = new DataView(buf);

                for (let off = 0; off < 0x1000 - 64; off += 8) {
                    // Look for sequences of valid FName-like values
                    const cluster = [];
                    for (let slot = 0; slot < 20; slot++) {
                        const idx = dv.getUint32(off + slot * 8, true);
                        const num = dv.getUint32(off + slot * 8 + 4, true);
                        if (idx >= 1 && idx < 300000 && num < 1000) {
                            cluster.push({idx, num});
                        } else {
                            break;
                        }
                    }
                    if (cluster.length >= minClusterSize) {
                        const addr = p.add(off);
                        const key = addr.toString();
                        if (seen.has(key)) continue;
                        seen.add(key);

                        // Resolve FNames in the cluster
                        const names = cluster.slice(0, 12).map(c => {
                            const s = resolveFName(c.idx);
                            return s ? `"${s}"` : `?(${c.idx})`;
                        }).join(', ');

                        console.log(`  [#${results.length+1}] ${addr}: ${cluster.length} FNames [${names}]`);
                        results.push({addr, cluster, names});

                        if (results.length >= maxResults) return;
                        off += cluster.length * 8 + 0x100;
                    }
                }
            } catch (_) {}
        }
    });

    console.log(`[+] Found ${results.length} FName clusters`);
    return results;
}

// ---------------------------------------------------------------------------
// Targeted scan: look for specific weapon-related FNames
// ---------------------------------------------------------------------------
const WEAPON_KEYWORDS = ["WEAPON", "weapon", "RIFLE", "PISTOL", "SNIPER", "AKM", "MOSIN", "PEACE", "PROBE", "GSW"];

function findWeaponClusters() {
    console.log(`\n[*] Scanning for weapon-related FName clusters...`);

    const poolBase = BASE.add(POOL_BASE_RVA);
    const results = [];

    Process.enumerateRanges('rw-').forEach(function(range) {
        if (results.length >= 20) return;
        if (range.size < 0x10000) return;
        const lo = range.base.and(ptr(0xFFFFFFFF)).toUInt32();
        if (lo < 0x10000000) return;

        for (let p = range.base; p.compare(range.base.add(range.size)) < 0 && results.length < 20; p = p.add(0x80000)) {
            try {
                const buf = p.readByteArray(0x1000);
                if (!buf) continue;
                const dv = new DataView(buf);

                for (let off = 0; off < 0x1000 - 64; off += 8) {
                    // Check if any FName in the potential cluster matches weapon keywords
                    const cluster = [];
                    let hasWeapon = false;
                    for (let slot = 0; slot < 30; slot++) {
                        const idx = dv.getUint32(off + slot * 8, true);
                        const num = dv.getUint32(off + slot * 8 + 4, true);
                        if (idx >= 1 && idx < 300000 && num < 1000) {
                            cluster.push({idx, num});
                            // Resolve each to check for weapon keywords
                            if (!hasWeapon) {
                                const s = resolveFName(idx);
                                if (s && WEAPON_KEYWORDS.some(kw => s.toUpperCase().includes(kw))) {
                                    hasWeapon = true;
                                }
                            }
                        } else {
                            break;
                        }
                    }

                    if (hasWeapon && cluster.length >= 3) {
                        const addr = p.add(off);
                        const names = cluster.slice(0, 15).map(c => {
                            const s = resolveFName(c.idx);
                            return s ? `"${s}"` : `?(${c.idx})`;
                        }).join(', ');

                        console.log(`  [WPN#${results.length+1}] ${addr}: ${cluster.length} FNames [${names}]`);
                        results.push({addr, cluster, names});

                        if (results.length >= 20) return;
                        off += 0x100;
                    }
                }
            } catch (_) {}
        }
    });

    console.log(`[+] Found ${results.length} weapon-related clusters`);
    return results;
}

// ---------------------------------------------------------------------------
// Loadout signature scan: search for known weapon ID substrings in resolved FNames
// ---------------------------------------------------------------------------
const WEAPON_IDS = ["PEACE_GSW-AR", "PROBE_RU-AKM", "SNIPER_RU-MOSIN", "SUBMACHINE_GUN_MP7"];

function findWeaponIdFNames() {
    console.log(`\n[*] Searching for known weapon IDs in FName pool...`);
    const found = {};
    for (let i = 1; i < 300000; i++) {
        const s = resolveFName(i);
        if (!s) continue;
        for (const wid of WEAPON_IDS) {
            if (s === wid || s.includes(wid)) {
                if (!found[wid]) found[wid] = [];
                found[wid].push(i);
                console.log(`  FName[${i}] = "${s}"  ← matches "${wid}"`);
            }
        }
    }
    console.log(`[+] Weapon ID FNames found: ${JSON.stringify(found)}`);
    return found;
}

// ---------------------------------------------------------------------------
// Dump cache stats
// ---------------------------------------------------------------------------
function stats() {
    const total = Object.keys(fnameCache).length;
    const resolved = Object.values(fnameCache).filter(v => v !== null && v !== undefined).length;
    console.log(`[stats] Cache: ${total} entries, ${resolved} resolved`);
}

console.log(`\n[READY] Commands:`);
console.log(`  test()              - Test FName resolution on first 500 indices`);
console.log(`  scanHeap(6, 30)     - Scan heap for FName clusters`);
console.log(`  findWeaponClusters()- Scan for weapon-related FName clusters`);
console.log(`  findWeaponIdFNames()- Brute-force search for weapon IDs in FName pool`);
console.log(`  resolveFName(idx)   - Resolve a single FName index`);
console.log(`  stats()             - Show cache statistics`);
console.log(`\n`);
