// =============================================================================
// Session 38: Direct pool inspection + force initialization
//
// 1. Read pool block pointers directly from memory (no NativeFunction needed)
// 2. If pool is empty, call sub_19D3490 to initialize it
// 3. Then resolve FNames in pure JavaScript using the pool structure
// =============================================================================

const BASE = Process.getModuleByName("ProjectBoundarySteam-Win64-Shipping.exe").base;
function hex(n) { return n instanceof NativePointer ? n.toString() : "0x" + n.toString(16); }

const POOL_READY_RVA = 0x5D29278;   // byte_5D29278
const POOL_BASE_RVA  = 0x5D29280;   // unk_5D29280: pool of block pointers
const INIT_POOL_RVA  = 0x19D3490;   // sub_19D3490: pool initializer

const poolReady = BASE.add(POOL_READY_RVA);
const poolBase  = BASE.add(POOL_BASE_RVA);

console.log(`[+] BASE      = ${BASE}`);
console.log(`[+] poolBase  = ${poolBase}`);
console.log(`[+] poolReady = ${poolReady} → ${poolReady.readU8()}`);

// ---------------------------------------------------------------------------
// Step 1: Inspect the pool directly
// ---------------------------------------------------------------------------
function inspectPool(numBlocks) {
    numBlocks = numBlocks || 16;
    console.log(`\n[*] Inspecting pool at ${poolBase} (${numBlocks} slots)...`);

    let validBlocks = 0;
    for (let i = 0; i < numBlocks; i++) {
        const ptr = poolBase.add(i * 8).readPointer();
        const isNull = ptr.isNull();
        const isHeap = !isNull && ptr.compare(ptr(0x10000000)) > 0 && ptr.compare(ptr(0x7FFFFFFFFFFF)) < 0;

        if (!isNull && isHeap) {
            validBlocks++;
            // Try reading first few entries
            try {
                const hdr0 = ptr.readU16();
                const len0 = hdr0 >> 6;
                const wide0 = hdr0 & 1;

                let preview = '';
                if (len0 > 0 && len0 < 256) {
                    if (wide0) {
                        preview = ptr.add(2).readUtf16String(Math.min(len0, 40));
                    } else {
                        preview = ptr.add(2).readCString(Math.min(len0, 40));
                    }
                }

                console.log(`  pool[${i}] = ${ptr}  (first entry: len=${len0} wide=${wide0} → "${preview}")`);
            } catch (e) {
                console.log(`  pool[${i}] = ${ptr}  (read error: ${e.message})`);
            }
        } else if (!isNull) {
            console.log(`  pool[${i}] = ${ptr}  (NOT heap)`);
        }
    }
    console.log(`[+] Valid blocks: ${validBlocks}/${numBlocks}`);
}

// ---------------------------------------------------------------------------
// Step 2: Try to force-init the pool
// ---------------------------------------------------------------------------
function forceInit() {
    console.log(`\n[*] Forcing pool initialization via sub_19D3490...`);

    // sub_19D3490 expects rcx = &unk_5D29280 (the pool address)
    const initFn = new NativeFunction(BASE.add(INIT_POOL_RVA), 'pointer', ['pointer']);
    const result = initFn(poolBase);

    console.log(`[+] sub_19D3490 returned: ${result}`);
    console.log(`[+] poolReady now: ${poolReady.readU8()}`);

    // Now inspect again
    inspectPool(16);
}

// ---------------------------------------------------------------------------
// Step 3: Pure-JS FName resolution using pool structure
// ---------------------------------------------------------------------------
const fnameCache = {};

function readPoolPtr(index) {
    // pool[index] = 8-byte pointer at poolBase + index*8
    return poolBase.add(index * 8).readPointer();
}

function resolveFName(compIdx) {
    if (compIdx < 1 || compIdx > 500000) return null;
    if (fnameCache[compIdx] !== undefined) return fnameCache[compIdx];

    try {
        const blockIdx = (compIdx >> 16) & 0xFFFF;
        const offset   = compIdx & 0xFFFF;

        // pool[blockIdx + 2] = block pointer
        const blockPtr = readPoolPtr(blockIdx + 2);
        if (blockPtr.isNull()) { fnameCache[compIdx] = null; return null; }

        // entry at blockPtr + 2 * offset
        const entry = blockPtr.add(2 * offset);
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

        fnameCache[compIdx] = str || null;
        return fnameCache[compIdx];
    } catch (e) {
        fnameCache[compIdx] = null;
        return null;
    }
}

// ---------------------------------------------------------------------------
// Step 4: Also try calling the native function (for comparison)
// ---------------------------------------------------------------------------
const resolveNative = new NativeFunction(BASE.add(0x19D9570), 'pointer', ['pointer', 'pointer', 'pointer', 'pointer']);

function resolveNativeFName(compIdx) {
    try {
        const idxBuf = Memory.alloc(8);
        idxBuf.writeU32(compIdx);
        idxBuf.add(4).writeU32(0);
        const entry = resolveNative(ptr(0), ptr(0), ptr(0), idxBuf);
        return entry;
    } catch (e) {
        return ptr(0);
    }
}

// ---------------------------------------------------------------------------
// Test
// ---------------------------------------------------------------------------
function test() {
    inspectPool(16);

    // Try native call
    console.log(`\n[*] Testing NativeFunction resolve for idx=1,2,3,4,5...`);
    for (const idx of [1,2,3,4,5]) {
        const entry = resolveNativeFName(idx);
        console.log(`  native(${idx}) → ${entry}`);
    }

    // Try pure-JS resolve
    console.log(`\n[*] Testing pure-JS resolve for idx=1,2,3,4,5...`);
    for (const idx of [1,2,3,4,5]) {
        const s = resolveFName(idx);
        console.log(`  js(${idx}) → ${s ? '"' + s + '"' : 'null'}`);
    }
}

// ---------------------------------------------------------------------------
// Scan heap for FName clusters (resolve in pure JS)
// ---------------------------------------------------------------------------
function scanHeap(minCluster, maxResults) {
    minCluster = minCluster || 6;
    maxResults = maxResults || 30;
    console.log(`\n[*] Scanning heap for FName clusters (>=${minCluster})...`);

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
                    for (let slot = 0; slot < 20; slot++) {
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

                        const names = cluster.slice(0, 12).map(c => {
                            const s = resolveFName(c.idx);
                            return s ? `"${s}"` : `?(${c.idx})`;
                        }).join(', ');

                        console.log(`  [#${results.length+1}] ${addr}: ${cluster.length} FNames [${names}]`);
                        results.push({addr, cluster: cluster.slice(0, 12)});
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
    console.log(`[stats] Cache: ${total} entries, ${resolved} resolved`);
}

console.log(`\n[READY] Commands:`);
console.log(`  inspectPool(16)       - Read pool block pointers directly`);
console.log(`  forceInit()           - Call sub_19D3490 to init pool`);
console.log(`  test()                - Test resolution both native and pure-JS`);
console.log(`  scanHeap(6, 30)       - Scan heap for FName clusters`);
console.log(`  resolveFName(idx)     - Pure-JS FName resolution`);
console.log(`  stats()               - Cache statistics`);
console.log(`\n`);
