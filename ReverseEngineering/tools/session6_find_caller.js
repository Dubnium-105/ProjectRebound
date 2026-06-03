// =============================================================================
// Session 6: Find sub_9C4780's caller — where ErrorCode is set
//
// Strategy:
//   1. Hook sub_9C4780, capture return address (=caller) for msgId=2
//   2. Read the caller function's code to understand who sets ErrorCode
//   3. Also dump struct contents at sub_9C4780 entry (full hex)
//
// Usage: frida -p <PID> -l tools/session6_find_caller.js
// =============================================================================

const DISPATCH_RVA = 0x9C4780;

const BASE = (() => {
    for (const n of ["ProjectBoundarySteam-Win64-Shipping.exe","ProjectBoundary-Win64-Shipping.exe"])
        { try { return Process.getModuleByName(n).base; } catch(_){} }
    for (const m of Process.enumerateModules())
        { if (m.name.endsWith('.exe') && m.name.toLowerCase().includes('boundary')) return m.base; }
    throw new Error("Module not found");
})();

function hex(n) { return n instanceof NativePointer ? n.toString() : "0x" + n.toString(16); }
function rva(ptr) { return ptr instanceof NativePointer ? hex(ptr.sub(BASE).toInt32()) : '?'; }

const dispatchFunc = BASE.add(DISPATCH_RVA);
console.log(`[+] Dispatch: ${dispatchFunc}  BASE=${BASE}`);

// =============================================================================
// Hook sub_9C4780 entry for msgId=2
// =============================================================================

let seenCallers = {};
let hitCount = 0;
const MAX_LOG = 5;

Interceptor.attach(dispatchFunc, {
    onEnter(args) {
        const msgId = args[2].toInt32(); // r8
        if (msgId !== 2 && msgId !== 49) return; // GetPlayerArchiveV2=2, UpdateRoleArchiveV2=49

        hitCount++;
        const retAddr = this.returnAddress;
        const retStr = retAddr.toString();
        const callerRVA = rva(retAddr);

        if (!seenCallers[retStr]) {
            seenCallers[retStr] = { count: 0, rva: callerRVA };
        }
        seenCallers[retStr].count++;

        if (hitCount <= MAX_LOG) {
            console.log(`\n[DISPATCH #${hitCount}] msgId=${msgId} callerRVA=${callerRVA}`);

            // Dump struct contents at all 4 regs
            for (const [label, reg] of [['rcx', this.context.rcx], ['rdx', this.context.rdx],
                                         ['r8', this.context.r8], ['r9', this.context.r9]]) {
                try {
                    // Check ErrorCode at multiple offsets
                    let ecInfo = [];
                    for (const off of [0x0C, 0x10, 0x14, 0x18]) {
                        try { ecInfo.push(`+${hex(off)}=${reg.add(off).readS32()}`); } catch (_) {}
                    }
                    console.log(`  ${label}=${reg}  EC:[${ecInfo.join(', ')}]`);
                } catch (_) {}
            }

            // Hex dump of rcx and rdx (first 48 bytes)
            for (const reg of [this.context.rcx, this.context.rdx]) {
                try {
                    console.log(`  ${reg} hex:`);
                    console.log(hexdump(reg.readByteArray(48), { offset: 0, length: 48, header: false, ansi: false }));
                } catch (_) {}
            }
        }
    }
});

// =============================================================================
// REPL
// =============================================================================

function callers() {
    console.log(`\n=== Callers of sub_9C4780 (${hitCount} total hits) ===`);
    for (const [addr, info] of Object.entries(seenCallers)) {
        console.log(`  RVA=${info.rva}  ret=${addr}  x${info.count}`);
    }
    console.log(`\n[*] In IDA: G → each RVA to see the caller function`);
}

console.log(`\n[*] Enter armory (msgId=2) or change equip (msgId=49)`);
console.log(`[*] Then: callers() to list all caller RVAs\n`);
