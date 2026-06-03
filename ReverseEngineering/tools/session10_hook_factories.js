// =============================================================================
// Session 10: Hook sub_16BD8F0 — log ALL calls during GetPlayerArchiveV2
//
// Only logs when msgId=2 is being processed (during GetPlayerArchiveV2).
// Captures: caller RVA, allocation size, stack trace.
//
// Usage: frida -p <PID> -l tools/session10_hook_factories.js
// =============================================================================

const BASE = (() => {
    for (const n of ["ProjectBoundarySteam-Win64-Shipping.exe","ProjectBoundary-Win64-Shipping.exe"])
        { try { return Process.getModuleByName(n).base; } catch(_){} }
    for (const m of Process.enumerateModules())
        { if (m.name.endsWith('.exe') && m.name.toLowerCase().includes('boundary')) return m.base; }
    throw new Error("Module not found");
})();

function hex(n) { return n instanceof NativePointer ? n.toString() : "0x" + n.toString(16); }
function rva(ptr) { return ptr instanceof NativePointer ? hex(ptr.sub(BASE).toInt32()) : '?'; }

// Track current MessageId from dispatch
let currentMsgId = -1;

const dispatchAddr = BASE.add(0x9C4780);
Interceptor.attach(dispatchAddr, {
    onEnter(args) { currentMsgId = args[2].toInt32(); },
    onLeave(retval) { currentMsgId = -1; }
});

// Hook allocator
const allocAddr = BASE.add(0x16BD8F0);
let hitCount = 0;
const MAX_LOG = 30;
const callersSeen = {};

Interceptor.attach(allocAddr, {
    onEnter(args) {
        if (currentMsgId !== 2) return;  // only during GetPlayerArchiveV2
        hitCount++;
        if (hitCount > MAX_LOG) return;

        // args[3] (r9) is the 4th argument = allocation size
        let size = '?';
        try { size = args[3].toInt32(); } catch (_) {}

        const caller = rva(this.returnAddress);
        const key = `${caller}_${size}`;
        callersSeen[key] = (callersSeen[key] || 0) + 1;

        console.log(`[#${hitCount}] callerRVA=${caller} size=${size}`);
    },
    onLeave(retval) {
        if (currentMsgId !== 2 || hitCount > MAX_LOG) return;
        if (retval && !retval.isNull()) {
            console.log(`  → ${retval}`);
        }
    }
});

function summary() {
    console.log(`\n=== Allocator calls during GetPlayerArchiveV2 ===`);
    console.log(`Total: ${hitCount}`);
    for (const [key, count] of Object.entries(callersSeen)) {
        console.log(`  ${key}  x${count}`);
    }
}

console.log(`[+] Allocator hooked @ ${allocAddr}`);
console.log(`[*] Trigger GetPlayerArchiveV2, then summary()\n`);
