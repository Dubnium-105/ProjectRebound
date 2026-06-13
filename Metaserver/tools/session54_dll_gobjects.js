// =============================================================================
// Session 54: Use DLL's GObjects to find display character
//
// The DLL has SDK::UObject::GObjects at a known RVA within the DLL.
// From SDK/Basic.hpp: GObjects = 0x05D65FE0 (game module RVA)
//
// The DLL accesses this via: BASE + 0x05D65FE0
// Even if the SDK offset is wrong, we can try to find the REAL address.
//
// But more directly: we can find the DLL's GObjectsAddress global variable,
// which stores the address after InitGObjects() runs.
// =============================================================================

function hex(n) { return n instanceof NativePointer ? n.toString() : "0x" + n.toString(16); }

const GAME_BASE = Process.getModuleByName("ProjectBoundarySteam-Win64-Shipping.exe").base;
let DLL_BASE = null;
try { DLL_BASE = Process.getModuleByName("ProjectReboundMainDLL.dll").base; } catch(_) {}

console.log(`[+] GAME BASE = ${GAME_BASE}`);
console.log(`[+] DLL  BASE = ${DLL_BASE}`);

function inHeap(p) { return p.compare(ptr(0x10000000)) > 0 && p.compare(ptr(0x7FFFFFFFFFFF)) < 0; }

// ---------------------------------------------------------------------------
// Method 1: Try SDK offset directly (GObjects = 0x05D65FE0)
// ---------------------------------------------------------------------------
function trySDKOffset() {
    const sdkGObjects = GAME_BASE.add(0x05D65FE0);
    console.log(`\n[*] Method 1: SDK GObjects @ ${sdkGObjects}`);

    try {
        // Try reading as TUObjectArray (pointer to FObjectItem array)
        const objPtr = sdkGObjects.readPointer();
        const num = sdkGObjects.add(8).readU32();
        const max = sdkGObjects.add(12).readU32();

        console.log(`  GObjects struct: ptr=${objPtr} num=${num} max=${max}`);

        if (!objPtr.isNull() && inHeap(objPtr) && num > 10000 && num < 500000) {
            console.log(`  [!!!] SDK GObjects LOOKS VALID!`);
            console.log(`  [!!!] Array: ${objPtr}  Count: ${num}`);
            return { arrayPtr: objPtr, num, max };
        } else {
            console.log(`  SDK GObjects doesn't look like a valid TUObjectArray`);
            console.log(`  Raw dump:`);
            console.log(`  ${hexdump(sdkGObjects.readByteArray(64), {offset:0,length:64,header:false,ansi:true})}`);
        }
    } catch(e) {
        console.log(`  Read error: ${e.message}`);
    }
    return null;
}

// ---------------------------------------------------------------------------
// Method 2: Search the DLL module for GObjectsAddress global
//
// GObjectsAddress is a void* pointer in the DLL's .data section.
// We can find it by scanning for a pointer that points to a valid
// TUObjectArray-like structure in the game module.
// ---------------------------------------------------------------------------
function searchDLLForGObjects() {
    if (!DLL_BASE) {
        console.log(`[-] DLL not loaded`);
        return null;
    }

    console.log(`\n[*] Method 2: Searching DLL for GObjectsAddress...`);

    // The DLL's GObjectsAddress should contain a pointer to the game module's
    // .data section (where TUObjectArray is). Or possibly a heap pointer.
    try {
        // Look for a known pattern in the DLL code that accesses GObjectsAddress
        // InitGObjects() does: GObjectsAddress = GetImageBase() + 0x05D65FE0
        // This writes pointer: GAME_BASE + 0x05D65FE0 to GObjectsAddress
        const expectedGObjects = GAME_BASE.add(0x05D65FE0);

        // Scan DLL .data for this value
        Process.enumerateRanges('rw-').forEach(function(range) {
            if (range.base.compare(DLL_BASE) < 0 || range.base.compare(DLL_BASE.add(0x100000)) > 0) return;
            if (range.size < 0x100) return;

            for (let off = 0; off < range.size; off += 8) {
                try {
                    const val = range.base.add(off).readPointer();
                    if (val.equals(expectedGObjects)) {
                        console.log(`  Found GObjectsAddress @ DLL+${hex(off)} = ${val}`);
                    }
                } catch(_) {}
            }
        });
    } catch(e) {
        console.log(`  Error: ${e.message}`);
    }
}

// ---------------------------------------------------------------------------
// Method 3: Walk the game module's .data section looking for TUObjectArray
//
// TUObjectArray in UE4.27 is a fixed-size array, sometimes embedded in a struct.
// The key signature: a large heap pointer followed by count+capacity ints.
// But we already tried this in session53 and it didn't work.
//
// Let's try a looser search: just look for ANY large array of FObjectItem-like
// structures in the heap.
// ---------------------------------------------------------------------------
function findObjectArrayInHeap() {
    console.log(`\n[*] Method 3: Searching heap for FObjectItem arrays...`);

    // FObjectItem pattern: 24 bytes, first 8 are heap pointer
    // An array of these would look like: [ptr, flags, cluster, serial, reserved] repeated
    // We're looking for a region with DENSE valid heap pointers

    const candidates = [];
    Process.enumerateRanges('rw-').forEach(function(range) {
        if (candidates.length >= 5) return;
        if (range.size < 0x100000) return; // At least 1MB
        if (!inHeap(range.base)) return;

        // Sample the range looking for consecutive valid heap pointers
        const stride = 24; // FObjectItem size
        for (let p = range.base; p.compare(range.base.add(range.size)) < 0 && candidates.length < 5; p = p.add(0x100000)) {
            try {
                let consecutive = 0;
                let maxConsecutive = 0;
                let maxStart = p;

                for (let off = 0; off < 0x100000; off += stride) {
                    const itemAddr = p.add(off);
                    const objPtr = itemAddr.readPointer();
                    if (!objPtr.isNull() && inHeap(objPtr)) {
                        consecutive++;
                        if (consecutive > maxConsecutive) {
                            maxConsecutive = consecutive;
                            maxStart = itemAddr.sub((consecutive - 1) * stride);
                        }
                    } else {
                        consecutive = 0;
                    }
                }

                if (maxConsecutive >= 100) {
                    const metaOff = -8; // maybe the array header is just before
                    const header = maxStart.add(metaOff);
                    console.log(`  [CANDIDATE] ${maxConsecutive} consecutive FObjectItems @ ${maxStart}`);
                    console.log(`    Header @ ${header}:`);
                    try {
                        console.log(`    ${hexdump(header.readByteArray(64), {offset:0,length:64,header:false,ansi:true})}`);
                    } catch(_) {}
                    candidates.push({ start: maxStart, count: maxConsecutive });
                }
            } catch(_) {}
        }
    });

    console.log(`[+] Found ${candidates.length} candidates`);
    return candidates;
}

// ---------------------------------------------------------------------------
// Method 4: Search the DLL's loaded code for GObjects usage
//
// The DLL code at APIInternal.cpp accesses GObjects->Num() and GObjects->GetByIndex().
// These are inline functions that access TUObjectArray.
// ---------------------------------------------------------------------------
function traceDLLGObjects() {
    if (!DLL_BASE) { console.log(`[-] DLL not loaded`); return; }

    console.log(`\n[*] Method 4: Tracing DLL's GObjects access...`);
    console.log(`[*] Searching DLL code for GObjects-related instructions...`);

    // The inline code for GObjects() is at Basic.hpp:275:
    // return reinterpret_cast<class TUObjectArray*>(GObjectsAddress);
    // This dereferences GObjectsAddress pointer.
    //
    // At call sites, this would be inlined. Let's find where the DLL calls
    // GObjects->Num() or GObjects->GetByIndex() by looking for patterns.

    // We could also just hook GetLastOfType and capture its return value
    // when it returns non-null. This would give us the display character pointer.
    try {
        // Find GetLastOfType in DLL exports or by scanning
        const dllExports = DLL_BASE ? Process.getModuleByName("ProjectReboundMainDLL.dll").enumerateExports() : [];
        for (const exp of dllExports) {
            console.log(`  DLL Export: ${exp.name} @ ${exp.address}`);
        }
    } catch(e) {}
}

// ---------------------------------------------------------------------------
// Method 5: Brute-force: scan the ENTIRE game module for a valid object array
// ---------------------------------------------------------------------------
function bruteForceGObjectArray() {
    console.log(`\n[*] Method 5: Brute-force scan of game module .data for TUObjectArray...`);

    let best = null;
    Process.enumerateRanges('rw-').forEach(function(range) {
        if (best) return;
        if (range.base.compare(GAME_BASE) < 0) return;
        if (range.base.compare(GAME_BASE.add(0x35000000)) > 0) return;
        if (range.size < 0x1000 || range.size > 0x2000000) return;

        for (let off = 0; off < Math.min(range.size, 0x100000); off += 8) {
            if (best) return;
            try {
                const addr = range.base.add(off);
                const ptr = addr.readPointer();
                if (!inHeap(ptr)) continue;

                const num = addr.add(8).readU32();
                const max = addr.add(12).readU32();
                // Relaxed constraints:
                if (num < 5000 || num > 2000000) continue;
                if (max < num || max > 5000000) continue;

                // Quick verify: check first 10 entries
                let valid = 0;
                for (let i = 0; i < 10; i++) {
                    const obj = ptr.add(i * 24).readPointer();
                    if (inHeap(obj)) {
                        const vt = obj.readPointer();
                        // Check if vt is ANY valid address (not just module)
                        if (!vt.isNull() && vt.compare(ptr(0x1000)) > 0) valid++;
                    }
                }
                if (valid >= 5) {
                    best = { addr, ptr, num, max, valid };
                    console.log(`  [BEST] @ RVA ${hex(off)}: ptr=${ptr} num=${num} max=${max} valid=${valid}/10`);
                }
            } catch(_) {}
        }
    });

    if (best) {
        console.log(`\n[!!!] Found TUObjectArray @ ${best.addr}`);
        console.log(`  Array: ${best.ptr}  Count: ${best.num}`);
        globalThis.gaArray = best;
        return best;
    }
    console.log(`[-] Not found`);
    return null;
}

console.log(`\n[READY] Commands:`);
console.log(`  trySDKOffset()        - Try SDK GObjects offset directly`);
console.log(`  bruteForceGObjectArray() - Brute-force scan for TUObjectArray`);
console.log(`  findObjectArrayInHeap()  - Search heap for FObjectItem arrays`);
console.log(`  traceDLLGObjects()      - Look for DLL exports`);
console.log(`\n`);
