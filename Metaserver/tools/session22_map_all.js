// =============================================================================
// Session 22: Minimal safe hooks — find what fires for msgId=2
//
// Only hooks functions that have been confirmed safe in previous sessions.
// Reports which ones activate during GetPlayerArchiveV2 processing.
//
// Usage: frida -p <PID> -l tools/session22_map_all.js
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

// Only functions previously confirmed safe to hook
const targets = [
    { rva: 0x99E820, name: 'LOOKUP sub_99E820' },
    { rva: 0x887BA0, name: 'MEMCPY sub_887BA0' },
];

// Track state
let currentMsgId = -1;

// Hook dispatch to detect msgId
const dispatchAddr = BASE.add(0x9C4780);
let dispCount = 0;
Interceptor.attach(dispatchAddr, {
    onEnter(args) {
        currentMsgId = args[2].toInt32();
        dispCount++;
        if (dispCount <= 30) {
            console.log(`[DISPATCH #${dispCount}] msgId=${currentMsgId} caller=${rva(this.returnAddress)}`);
        }
    },
    onLeave(retval) {
        currentMsgId = -1;
    }
});

// Hook targets — log ALL calls (limited)
let hitCounts = {};
const MAX_LOG = 20;

for (const t of targets) {
    hitCounts[t.name] = 0;

    try {
        const addr = BASE.add(t.rva);
        Interceptor.attach(addr, {
            onEnter(args) {
                hitCounts[t.name]++;
                if (hitCounts[t.name] <= MAX_LOG) {
                    console.log(`[${t.name} #${hitCounts[t.name]}] caller=${rva(this.returnAddress)}  msgId=${currentMsgId}`);
                }
            }
        });
    } catch (e) {
        console.log(`[!] ${t.name}: ${e.message}`);
    }
}

function summary() {
    console.log(`\n=== Hit counts ===`);
    for (const t of targets) {
        console.log(`  ${t.name}: ${hitCounts[t.name]}`);
    }
    console.log(`  Dispatch: ${dispCount}`);
}

console.log(`[+] ${targets.length} hooks installed. Enter armory.`);
console.log(`[*] Then: summary()\n`);
