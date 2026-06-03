// =============================================================================
// Session 7b: Wide hook — find which validation functions actually fire
//
// Hooks four points in the validation chain for GetPlayerArchiveV2 (msgId=2):
//   A: sub_9C4780 (dispatch) — confirmed works
//   B: sub_9B9F3B (caller, contains call to sub_9BF020)
//   C: sub_9BF020 (validator #1)
//   D: sub_9C5F10 (validator #2, called inside sub_9BF020)
//
// Usage: frida -p <PID> -l tools/session7b_wide_hook.js
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

// Points to hook
const points = [
    { rva: 0x9C4780, name: 'A:dispatch(sub_9C4780)', logArg: true },
    { rva: 0x9BF020, name: 'C:validator1(sub_9BF020)', logArg: true },
    { rva: 0x9C5F10, name: 'D:validator2(sub_9C5F10)', logArg: true },
];

let hitCounts = {};
let detailCount = 0;
const MAX_DETAIL = 3;

for (const pt of points) {
    const addr = BASE.add(pt.rva);
    hitCounts[pt.name] = 0;

    try {
        Interceptor.attach(addr, {
            onEnter(args) {
                hitCounts[pt.name]++;
                if (detailCount >= MAX_DETAIL) return;
                detailCount++;

                console.log(`\n[${pt.name}] HIT #${hitCounts[pt.name]}`);
                console.log(`  addr=${addr}  ret=${this.returnAddress}  callerRVA=${rva(this.returnAddress)}`);

                // Dump first 3 args
                for (let i = 0; i < 4; i++) {
                    const val = args[i];
                    if (val && !val.isNull()) {
                        console.log(`  arg[${i}]=${val}`);
                        // Try to find ErrorCode in first arg
                        if (i === 0 || i === 3) {
                            try {
                                const ec = val.add(0x0C).readS32();
                                if (ec === 0 || ec === 4) {
                                    console.log(`    → ErrorCode at +0x0C = ${ec === 4 ? 'FAIL(4)' : 'OK(0)'}`);
                                }
                            } catch (_) {}
                        }
                    }
                }
            }
        });
        console.log(`[+] Hooked ${pt.name} @ ${addr}`);
    } catch (e) {
        console.log(`[!] Failed to hook ${pt.name}: ${e.message}`);
    }
}

// Also hook sub_9C4780 to only log when msgId=2
const dispatchAddr = BASE.add(0x9C4780);
Interceptor.attach(dispatchAddr, {
    onEnter(args) {
        const msgId = args[2].toInt32();
        if (msgId !== 2) return;
        hitCounts['dispatch_msgId2'] = (hitCounts['dispatch_msgId2'] || 0) + 1;
        if (hitCounts['dispatch_msgId2'] <= MAX_DETAIL) {
            console.log(`\n[DISPATCH msgId=2] #${hitCounts['dispatch_msgId2']}`);
        }
    }
});

// Summary command
function summary() {
    console.log('\n=== Hook Hit Summary ===');
    for (const [name, count] of Object.entries(hitCounts)) {
        console.log(`  ${name}: ${count} hits`);
    }
}

console.log(`\n[*] 4 hooks active. Trigger GetPlayerArchiveV2.`);
console.log(`[*] Then: summary()\n`);
