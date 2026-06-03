// =============================================================================
// Session 8: Bypass ErrorCode=4 at the handler entry
//
// Forces ErrorCode to 0 at handler 0x9C48B0 entry, then checks if armory works.
// If this fixes the display, we have a working bypass while we find root cause.
//
// Usage: frida -p <PID> -l tools/session8_bypass.js
// =============================================================================

const HANDLER_RVA = 0x9C48B0;

const BASE = (() => {
    for (const n of ["ProjectBoundarySteam-Win64-Shipping.exe","ProjectBoundary-Win64-Shipping.exe"])
        { try { return Process.getModuleByName(n).base; } catch(_){} }
    for (const m of Process.enumerateModules())
        { if (m.name.endsWith('.exe') && m.name.toLowerCase().includes('boundary')) return m.base; }
    throw new Error("Module not found");
})();

const handlerFunc = BASE.add(HANDLER_RVA);
console.log(`[+] Handler: ${handlerFunc}`);

let fixedCount = 0;
let totalCount = 0;

Interceptor.attach(handlerFunc, {
    onEnter(args) {
        totalCount++;
        const rcx = this.context.rcx;
        try {
            const ec = rcx.add(0x0C).readS32();
            if (ec === 4) {
                // OVERWRITE ErrorCode to 0
                rcx.add(0x0C).writeS32(0);
                fixedCount++;
                if (fixedCount <= 5) {
                    console.log(`[FIX #${fixedCount}] ErrorCode 4→0 at rcx=${rcx}`);
                }
            }
        } catch (_) {}
    }
});

function stats() {
    console.log(`\nTotal handler calls: ${totalCount}`);
    console.log(`ErrorCode fixed (4→0): ${fixedCount}`);
}

console.log(`[*] Bypass active. Enter armory — check if weapons display correctly.`);
console.log(`[*] stats() to see counts.\n`);
