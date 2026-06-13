// =============================================================================
// Session 43: Trace GetPlayerArchiveV2 handler's memory writes
//
// Strategy: Monitor the thread that processes msgId=2 and capture the memory
// addresses it writes to. The cache should be among them.
//
// Technique: Use Interceptor to hook all functions called during msgId=2
// processing, logging their return values and memory operations.
// =============================================================================

const BASE = Process.getModuleByName("ProjectBoundarySteam-Win64-Shipping.exe").base;
function hex(n) { return n instanceof NativePointer ? n.toString() : "0x" + n.toString(16); }

const DISPATCH_RVA = 0x9C4780;

console.log(`[+] BASE = ${BASE}`);

// ---------------------------------------------------------------------------
// Strategy: Hook sub_9C4780. When msgId=2 arrives, record the time,
// then scan for what CHANGED in the game's heap.
//
// We'll compare before/after snapshots of specific heap regions.
// ---------------------------------------------------------------------------

let dispatchSnapshots = [];
let snapshotEnabled = false;
const SNAPSHOT_SIZE = 0x100000; // 1MB per region
const MAX_REGIONS = 50;

// Snapshot: hash of first N heap regions (fast comparison)
function takeSnapshot() {
    const regions = [];
    Process.enumerateRanges('rw-').forEach(function(range) {
        if (regions.length >= MAX_REGIONS) return;
        if (range.size < 0x10000 || range.size > 0x40000000) return;
        const lo = range.base.and(ptr(0xFFFFFFFF)).toUInt32();
        if (lo < 0x10000000) return;

        try {
            const buf = range.base.readByteArray(Math.min(SNAPSHOT_SIZE, range.size.toInt32()));
            // Simple hash: sum of all bytes (fast but collision-prone)
            let hash = 0;
            const u8 = new Uint8Array(buf);
            for (let i = 0; i < u8.length; i += 64) hash += u8[i];
            regions.push({ base: range.base, size: Math.min(SNAPSHOT_SIZE, range.size.toInt32()), hash });
        } catch(_) {}
    });
    return regions;
}

function compareSnapshots(before, after) {
    const changes = [];
    for (let i = 0; i < before.length; i++) {
        if (before[i].hash !== after[i].hash) {
            changes.push(before[i].base);
        }
    }
    return changes;
}

function diffRegions(regions) {
    console.log(`\n[*] Diffing ${regions.length} changed regions...`);
    for (const base of regions.slice(0, 10)) {
        try {
            const data = base.readByteArray(256);
            console.log(`  ${base}:`);
            console.log(`  ${hexdump(data, {offset:0,length:256,header:false,ansi:true})}`);
        } catch(e) {
            console.log(`  ${base}: read error`);
        }
    }
}

// ---------------------------------------------------------------------------
// Hook dispatch — take snapshot before and after msgId=2
// ---------------------------------------------------------------------------
let beforeSnapshot = null;

Interceptor.attach(BASE.add(DISPATCH_RVA), {
    onEnter(args) {
        const msgId = args[1].toInt32();
        if (msgId === 2) { // GetPlayerArchiveV2
            console.log(`\n[DISPATCH] GetPlayerArchiveV2 — taking snapshot...`);
            beforeSnapshot = takeSnapshot();
            console.log(`[+] Before snapshot: ${beforeSnapshot.length} regions`);
        }
    },
    onLeave(retval) {
        if (beforeSnapshot) {
            console.log(`[DISPATCH] Handler returned — taking after snapshot...`);
            const afterSnapshot = takeSnapshot();
            const changed = compareSnapshots(beforeSnapshot, afterSnapshot);
            console.log(`[+] Changed regions: ${changed.length}`);
            if (changed.length > 0 && changed.length < 50) {
                console.log(`Changed:`);
                for (const c of changed.slice(0, 15)) {
                    console.log(`  ${c}`);
                }
                // Dump first few changed regions
                diffRegions(changed.slice(0, 5));
            } else if (changed.length >= 50) {
                console.log(`  (too many changes — game is actively running)`);
            }
            beforeSnapshot = null;
        }
    }
});

// ---------------------------------------------------------------------------
// Alternative: Hook the protobuf message registration functions
// to understand the data structure
// ---------------------------------------------------------------------------

// sub_9D5460 registers "assets.RoleArchiveDataV2" — hook it
const REG_ROLE_ARCHIVE = 0x9D5460;
try {
    Interceptor.attach(BASE.add(REG_ROLE_ARCHIVE), {
        onEnter(args) {
            console.log(`[REG] RoleArchiveDataV2 registration called`);
            console.log(`  rcx=${args[0]} rdx=${args[1]} r8=${args[2]} r9=${args[3]}`);
        }
    });
    console.log(`[+] Hooked RoleArchiveDataV2 registration @ ${BASE.add(REG_ROLE_ARCHIVE)}`);
} catch(e) {
    console.log(`[-] Failed to hook registration: ${e.message}`);
}

// ---------------------------------------------------------------------------
// Helper: search for specific byte patterns in all heap
// ---------------------------------------------------------------------------
function searchPattern(hexPattern, label) {
    console.log(`\n[*] Searching for ${label}: ${hexPattern}`);
    let count = 0;

    Process.enumerateRanges('rw-').forEach(function(range) {
        if (count >= 10) return;
        if (range.size < 0x1000 || range.size > 0x10000000) return;

        try {
            Memory.scan(range.base, range.size, hexPattern, {
                onMatch(addr) {
                    if (count >= 10) return 'stop';
                    count++;
                    console.log(`  [#${count}] ${addr}`);
                    try {
                        console.log(`    ${hexdump(addr.sub(16).readByteArray(64), {offset:0,length:64,header:false,ansi:true})}`);
                    } catch(_) {}
                },
                onComplete() {},
                onError(e) {}
            });
        } catch(e) {}
    });
    console.log(`[+] Found ${count} matches for ${label}`);
}

// ---------------------------------------------------------------------------
// Search for the protobuf field tag pattern
// RoleArchiveDataV2 fields from the strings:
//   roleID, leftPylon, rightPylon, mobilityModule, meleeWeapon, PrimaryWeapon, secondWeapon
// These are protobuf field names. In binary, protobuf stores field numbers as varints.
// We can search for the raw bytes that would encode these field numbers.
// ---------------------------------------------------------------------------

console.log(`\n[READY] Commands:`);
console.log(`  searchPattern(hexStr, label) - Search for byte pattern in heap`);
console.log(`  takeSnapshot() - Take a memory snapshot`);
console.log(`\n[*] Script is monitoring for msgId=2 dispatch...`);
console.log(`[*] Enter the armory to trigger GetPlayerArchiveV2\n`);
