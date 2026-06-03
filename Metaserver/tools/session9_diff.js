// =============================================================================
// Session 9: Diff the GOOD vs BAD struct paths through sub_99E820
//
// Captures both ErrorCode=0 and ErrorCode=4 calls to sub_99E820.
// For each: records MessageId (from context), struct addr, ret addr.
// This tells us WHY some structs fail and others pass.
//
// Usage: frida -p <PID> -l tools/session9_diff.js
// =============================================================================

const LOOKUP_RVA   = 0x99E820;
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

const lookupFunc   = BASE.add(LOOKUP_RVA);
const dispatchFunc = BASE.add(DISPATCH_RVA);

// Track which MessageId is currently being dispatched
let currentMsgId = -1;

Interceptor.attach(dispatchFunc, {
    onEnter(args) {
        currentMsgId = args[2].toInt32(); // r8
    },
    onLeave(retval) {
        currentMsgId = -1;
    }
});

// Track sub_99E820 calls with ErrorCode
let goodCount = 0, badCount = 0, maxLog = 8;

Interceptor.attach(lookupFunc, {
    onEnter(args) {
        // Find struct with ErrorCode
        for (const reg of [this.context.rcx, this.context.rdx, this.context.r8, this.context.r9]) {
            if (!reg || reg.isNull()) continue;
            try {
                const ec = reg.add(0x0C).readS32();
                if (ec === 0) {
                    goodCount++;
                    if (goodCount <= maxLog) {
                        console.log(`[GOOD #${goodCount}] msgId=${currentMsgId} struct=${reg} ret=${rva(this.returnAddress)}`);
                        // Dump first 32 bytes
                        console.log(hexdump(reg.readByteArray(32), { offset: 0, length: 32, header: false, ansi: false }));
                    }
                    break;
                } else if (ec === 4) {
                    badCount++;
                    if (badCount <= maxLog) {
                        console.log(`[BAD  #${badCount}] msgId=${currentMsgId} struct=${reg} ret=${rva(this.returnAddress)}`);
                        console.log(hexdump(reg.readByteArray(32), { offset: 0, length: 32, header: false, ansi: false }));
                    }
                    break;
                }
            } catch (_) {}
        }
    }
});

function stats() {
    console.log(`\n=== sub_99E820 Stats ===`);
    console.log(`GOOD (ErrorCode=0): ${goodCount}`);
    console.log(`BAD  (ErrorCode=4): ${badCount}`);
}

console.log(`[*] Hook active. Trigger GetPlayerArchiveV2 (enter armory).`);
console.log(`[*] stats() to see summary.\n`);
