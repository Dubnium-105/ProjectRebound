// =============================================================================
// Session 39: Fixed FName resolution — correct NativeFunction signature
//
// From sub_19D9570 disassembly:
//   19d9576: mov eax, [rcx]     ← reads ComparisonIndex from *rcx (arg0)
//   19d957a: shr ebx, 10h       ← blockIdx = compIdx >> 16
//   19d9584: movzx ecx, ax      ← entryOffset = compIdx & 0xFFFF
//   ...
//   19d95bb: add rax, [rdx+rbx*8+10h]  ← return pool[blockIdx+2] + 2*offset
//
// INIT path (pool not ready):
//   19d959a: lea rcx, unk_5D29280  ← sets rcx = pool addr (overwrites our arg!)
//   19d95a1: call sub_19D3490      ← safe: rcx correctly set before call
// =============================================================================

const BASE = Process.getModuleByName("ProjectBoundarySteam-Win64-Shipping.exe").base;
function hex(n) { return n instanceof NativePointer ? n.toString() : "0x" + n.toString(16); }

const POOL_READY_RVA = 0x5D29278;
const POOL_BASE_RVA  = 0x5D29280;

const poolReady = BASE.add(POOL_READY_RVA);
const poolBase  = BASE.add(POOL_BASE_RVA);

// Fixed: sub_19D9570 takes ONE pointer arg (rcx = &ComparisonIndex), returns entry*
const resolveEntry = new NativeFunction(BASE.add(0x19D9570), 'pointer', ['pointer']);

console.log(`[+] BASE        = ${BASE}`);
console.log(`[+] poolBase    = ${poolBase}`);
console.log(`[+] poolReady   = ${poolReady} → ${poolReady.readU8()}`);
console.log(`[+] resolveEntry= ${BASE.add(0x19D9570)}`);

// ---------------------------------------------------------------------------
// FName cache & resolution
// ---------------------------------------------------------------------------
const fnameCache = {};

function resolveFName(compIdx) {
    if (compIdx < 1 || compIdx > 500000) return null;
    if (fnameCache[compIdx] !== undefined) return fnameCache[compIdx];

    try {
        // Pass pointer to compIdx as rcx
        const idxBuf = Memory.alloc(4);
        idxBuf.writeU32(compIdx);

        const entry = resolveEntry(idxBuf);
        if (entry.isNull()) { fnameCache[compIdx] = null; return null; }

        // Parse entry: [2-byte header][string data]
        const header = entry.readU16();
        const len = header >> 6;
        const wide = header & 1;

        if (len === 0 || len > 256) { fnameCache[compIdx] = null; return null; }

        let str;
        if (wide) {
            str = entry.add(2).readUtf16String(len);
        } else {
            str = entry.add(2).readCString(len);
        }

        fnameCache[compIdx] = str && str.length > 0 ? str : null;
        return fnameCache[compIdx];
    } catch (e) {
        fnameCache[compIdx] = null;
        return null;
    }
}

// ---------------------------------------------------------------------------
// Direct pool inspection (debug)
// ---------------------------------------------------------------------------
function inspectPool(n) {
    n = n || 16;
    console.log(`\n[*] Pool at ${poolBase}:`);
    let valid = 0;
    for (let i = 0; i < n; i++) {
        const ptr = poolBase.add(i * 8).readPointer();
        if (!ptr.isNull() && ptr.compare(ptr(0x10000000)) > 0) {
            valid++;
            try {
                const h = ptr.readU16();
                const l = h >> 6;
                const w = h & 1;
                const s = l > 0 && l < 256 ? (w ? ptr.add(2).readUtf16String(Math.min(l,30)) : ptr.add(2).readCString(Math.min(l,30))) : '';
                console.log(`  pool[${i}] = ${ptr}  first: len=${l} wide=${w} "${s}"`);
            } catch(e) { console.log(`  pool[${i}] = ${ptr}  (err: ${e.message})`); }
        } else if (!ptr.isNull()) {
            console.log(`  pool[${i}] = ${ptr}  (low addr)`);
        }
    }
    console.log(`  → ${valid}/${n} valid block pointers`);
}

// ---------------------------------------------------------------------------
// Test resolution
// ---------------------------------------------------------------------------
function test() {
    console.log(`\n[*] Pool ready: ${poolReady.readU8()}`);
    inspectPool(16);

    console.log(`\n[*] Testing FName resolution (indices 1-200)...`);
    let resolved = 0;
    for (let i = 1; i <= 200; i++) {
        const s = resolveFName(i);
        if (s) {
            console.log(`  idx=${i} → "${s}"`);
            resolved++;
            if (resolved >= 25) break;
        }
    }
    console.log(`[+] Resolved ${resolved} names`);

    // Try some weapon-related indices
    console.log(`\n[*] Trying higher indices (common weapon range)...`);
    const testIndices = [500, 1000, 2000, 3000, 5000, 8000, 10000, 15000, 20000, 30000];
    for (const idx of testIndices) {
        const s = resolveFName(idx);
        if (s) console.log(`  idx=${idx} → "${s}"`);
    }
}

// ---------------------------------------------------------------------------
// Brute-force search for weapon strings in FName pool
// ---------------------------------------------------------------------------
const WEAPON_KEYWORDS = ["WEAPON", "RIFLE", "PISTOL", "SNIPER", "AKM", "MOSIN", "PEACE", "PROBE",
    "GSW", "SUBMACHINE", "MISSILE", "GRENADE", "EXO", "SCOPE", "MUZZLE", "MAGAZINE", "GRIP", "STOCK"];

function searchWeaponNames(maxIndex) {
    maxIndex = maxIndex || 50000;
    console.log(`\n[*] Searching indices 1-${maxIndex} for weapon-related FNames...`);
    const found = [];

    for (let i = 1; i <= maxIndex; i++) {
        const s = resolveFName(i);
        if (!s) continue;

        const upper = s.toUpperCase();
        for (const kw of WEAPON_KEYWORDS) {
            if (upper.includes(kw)) {
                console.log(`  FName[${i}] = "${s}"`);
                found.push({idx: i, name: s});
                break;
            }
        }
    }

    console.log(`[+] Found ${found.length} weapon-related FNames`);
    return found;
}

// ---------------------------------------------------------------------------
// Heap scan for FName clusters (with resolution)
// ---------------------------------------------------------------------------
function scanHeap(minCluster, maxResults) {
    minCluster = minCluster || 6;
    maxResults = maxResults || 30;
    console.log(`\n[*] Heap scan for FName clusters (>=${minCluster})...`);

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

                for (let off = 0; off < 0x1000 - 64 && results.length < maxResults; off += 8) {
                    const cluster = [];
                    for (let slot = 0; slot < 25; slot++) {
                        const idx = dv.getUint32(off + slot * 8, true);
                        const num = dv.getUint32(off + slot * 8 + 4, true);
                        if (idx >= 1 && idx < 300000 && num < 1000) {
                            cluster.push({idx, num});
                        } else break;
                    }
                    if (cluster.length >= minCluster) {
                        const addr = p.add(off);
                        const key = addr.toString();
                        if (seen.has(key)) continue;
                        seen.add(key);

                        // Resolve names
                        const names = cluster.slice(0, 12).map(c => {
                            const s = resolveFName(c.idx);
                            return s ? `"${s}"` : `?(${c.idx})`;
                        }).join(', ');

                        console.log(`  [#${results.length+1}] ${addr}: ${cluster.length} FNames [${names}]`);
                        results.push({addr, cluster: cluster.slice(0, 12), names});

                        if (results.length >= maxResults) return;
                        off += cluster.length * 8 + 0x100;
                    }
                }
            } catch (_) {}
        }
    });
    console.log(`[+] Found ${results.length} clusters`);
    return results;
}

function stats() {
    const total = Object.keys(fnameCache).length;
    const resolved = Object.values(fnameCache).filter(v => v !== null && v !== undefined).length;
    console.log(`[stats] ${total} cached, ${resolved} resolved`);
}

// Auto-test on load
console.log(`\n[READY] Running initial test...`);
setTimeout(() => {
    test();
}, 2000);

console.log(`\nCommands: test() searchWeaponNames(N) scanHeap(6,30) resolveFName(idx) stats() inspectPool(N)\n`);
