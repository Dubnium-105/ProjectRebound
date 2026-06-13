// =============================================================================
// Session 41: Trace GetPlayerArchiveV2 handler to find the loadout cache
//
// Strategy: Hook the known dispatch chain and trace where response data
// is actually stored in game memory. The handler receives parsed protobuf
// data and must write it somewhere — that "somewhere" is our cache.
//
// Known chain:
//   sub_9C4780 (DISPATCH) → sub_99E820 (LOOKUP) → vtable[33] handler
//
// We'll hook sub_9C4780 and log its return path to find the caller,
// then trace data flow from the response into the game's structures.
// =============================================================================

const BASE = Process.getModuleByName("ProjectBoundarySteam-Win64-Shipping.exe").base;
function hex(n) { return n instanceof NativePointer ? n.toString() : "0x" + n.toString(16); }

const DISPATCH_RVA = 0x9C4780;
const LOOKUP_RVA   = 0x99E820;

console.log(`[+] BASE = ${BASE}`);

// ---------------------------------------------------------------------------
// Step 1: Hook sub_9C4780 (DISPATCH) — log msgId and trace
// ---------------------------------------------------------------------------
let dispatchCount = 0;

Interceptor.attach(BASE.add(DISPATCH_RVA), {
    onEnter(args) {
        this.msgId = args[1].toInt32(); // rdx = msgId
        this.r8 = args[2];              // r8 = response data?
        this.r9 = args[3];              // r9
        dispatchCount++;

        if (this.msgId === 2) { // GetPlayerArchiveV2
            console.log(`\n[DISPATCH#${dispatchCount}] msgId=2 (GetPlayerArchiveV2)`);
            console.log(`  rcx = ${args[0]}`);
            console.log(`  rdx = ${args[1]} (msgId)`);
            console.log(`  r8  = ${args[2]}`);
            console.log(`  r9  = ${args[3]}`);

            // Dump r8 memory (response data)
            try {
                const r8val = args[2];
                if (!r8val.isNull() && r8val.compare(ptr(0x10000)) > 0) {
                    console.log(`  [r8] = ${hexdump(r8val.readByteArray(128), {offset:0,length:128,header:false,ansi:true})}`);

                    // Check if it's a protobuf-like structure
                    // Look for length-delimited fields
                    const bytes = r8val.readByteArray(128);
                    const u8 = new Uint8Array(bytes);
                    let ascii = '';
                    for (let i = 0; i < 128; i++) {
                        const b = u8[i];
                        if (b >= 32 && b < 127) ascii += String.fromCharCode(b);
                        else if (b === 0) ascii += '.';
                        else ascii += ' ';
                    }
                    console.log(`  ASCII: ${ascii}`);

                    // Try to follow pointer chains from r8
                    for (let off = 0; off < 64; off += 8) {
                        try {
                            const p = r8val.add(off).readPointer();
                            if (!p.isNull() && p.compare(ptr(0x10000000)) > 0 && p.compare(ptr(0x7FFFFFFFFFFF)) < 0) {
                                console.log(`  [+${off}] ptr → ${p}`);
                            }
                        } catch(_) {}
                    }
                }
            } catch(e) {
                console.log(`  r8 read error: ${e.message}`);
            }
        }
    },
    onLeave(retval) {
        if (this.msgId === 2) {
            console.log(`  retval = ${retval}`);
        }
    }
});

// ---------------------------------------------------------------------------
// Step 2: Hook sub_99E820 (LOOKUP) — log what it dispatches to
// ---------------------------------------------------------------------------
Interceptor.attach(BASE.add(LOOKUP_RVA), {
    onEnter(args) {
        this.r8 = args[2];
        this.r9 = args[3];
        this.stack1 = args[4];
    },
    onLeave(retval) {
        // Log occasionally
        if (this.logCount === undefined) this.logCount = 0;
        this.logCount++;
        if (this.logCount <= 10) {
            console.log(`[LOOKUP#${this.logCount}] r8=${this.r8} r9=${this.r9} ret=${retval}`);
        }
    }
});

// ---------------------------------------------------------------------------
// Step 3: Memory write monitor — track writes to potential cache areas
//
// When the GetPlayerArchiveV2 handler executes, it will write loadout data
// somewhere in memory. We can use Memory.accessMonitor or simply scan for
// changes in heap regions after the handler runs.
// ---------------------------------------------------------------------------

// Record a baseline of heap regions, then check for changes after dispatch
let lastWrites = [];

function snapshotHeap() {
    lastWrites = [];
    console.log(`[*] Taking heap snapshot...`);
    // We just record the count, not the data (too slow)
    console.log(`[+] Snapshot done`);
}

// ---------------------------------------------------------------------------
// Step 4: Search for weapon ID strings from the metaserver response
//         in the game's heap after dispatch
// ---------------------------------------------------------------------------
const WEAPON_IDS = ["PEACE_GSW-AR", "PROBE_RU-AKM", "SNIPER_RU-MOSIN", "MISSILE_GUIDED"];

function searchWeaponStrings() {
    console.log(`\n[*] Searching heap for weapon IDs...`);
    let results = {};

    Process.enumerateRanges('rw-').forEach(function(range) {
        if (range.size < 0x1000) return;
        const lo = range.base.and(ptr(0xFFFFFFFF)).toUInt32();
        if (lo < 0x10000000) return;

        for (const wid of WEAPON_IDS) {
            if (results[wid] && results[wid].length >= 3) continue;
            if (!results[wid]) results[wid] = [];

            // Create pattern bytes
            const bytes = [];
            for (let i = 0; i < wid.length; i++) bytes.push(wid.charCodeAt(i).toString(16).padStart(2,'0'));
            const pattern = bytes.join(' ');

            try {
                Memory.scan(range.base, range.size, pattern, {
                    onMatch(addr) {
                        if (results[wid].length >= 3) return;
                        results[wid].push(addr);
                        console.log(`  [${wid}] @ ${addr}`);
                        try {
                            console.log(`    ctx: ${hexdump(addr.sub(16).readByteArray(64), {offset:0,length:64,header:false,ansi:true})}`);
                        } catch(_) {}
                    },
                    onComplete() {},
                    onError(e) { if (!e.message.includes('not')) console.log(`  err: ${e}`); }
                });
            } catch(e) {}
        }
    });

    for (const [wid, addrs] of Object.entries(results)) {
        console.log(`  ${wid}: ${addrs.length} hits`);
    }
    return results;
}

// ---------------------------------------------------------------------------
// Step 5: Also try searching for weapon IDs as FName ComparisonIndex values
//
// Convert "PEACE_GSW-AR" → FName index → search for that uint32 in heap
// But we need working FName resolution first...
//
// Instead: search for the PROTOBUF binary pattern of the metaserver response
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Step 6: Direct approach — log heap addresses written during msgId=2
//
// Use Stalker on the dispatch handler for msgId=2 to trace all memory writes
// ---------------------------------------------------------------------------
let stalkerActive = false;
let stalkerWrites = [];

function startStalker() {
    if (stalkerActive) { console.log(`[-] Stalker already active`); return; }
    stalkerActive = true;
    stalkerWrites = [];

    const dispatchAddr = BASE.add(DISPATCH_RVA);

    // We'll stalk only the dispatch function's code range
    // Get function size from the module
    Stalker.follow(Process.getCurrentThreadId(), {
        transform(iterator) {
            let insn;
            while ((insn = iterator.next()) !== null) {
                // Instrument memory write instructions
                if (insn.mnemonic && (insn.mnemonic.startsWith('mov') || insn.mnemonic.startsWith('stos') ||
                    insn.mnemonic.startsWith('fxsave') || insn.mnemonic.startsWith('push'))) {
                    // Check if it writes to memory (not register-only)
                    const ops = insn.operands;
                    if (ops.length >= 1 && ops[0].type === 'mem') {
                        iterator.putCallout((ctx) => {
                            const addr = ctx.rcx; // or whichever register
                            // This is complex to do generically with Stalker
                        });
                    }
                }
                iterator.keep();
            }
        }
    });
    console.log(`[+] Stalker started on thread ${Process.getCurrentThreadId()}`);
}

console.log(`\n[READY] Commands:`);
console.log(`  searchWeaponStrings() - Scan heap for weapon ID strings`);
console.log(`  Enter armory and watch for [DISPATCH] log output`);
console.log(`  The dispatch log will show r8/r9 data for GetPlayerArchiveV2\n`);
