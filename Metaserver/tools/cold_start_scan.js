// =============================================================================
// Cold Start Scan: hook ALL [rcx+31Ch]==2 check functions at game launch
//
// Auto-attached by cold_launch.ps1. Logs which check functions fire
// during game startup, login, and first armory entry.
//
// Output goes to: logs/cold_scan_<timestamp>.log
// =============================================================================

const BASE = (() => {
    for (const n of ["ProjectBoundarySteam-Win64-Shipping.exe","ProjectBoundary-Win64-Shipping.exe"])
        { try { return Process.getModuleByName(n).base; } catch(_){} }
    for (const m of Process.enumerateModules())
        { if (m.name.endsWith('.exe') && m.name.toLowerCase().includes('boundary')) return m.base; }
    throw new Error("Module not found");
})();

function ts() { return new Date().toISOString().replace('T',' ').slice(0,19); }
function hex(n) { return n instanceof NativePointer ? n.toString() : "0x" + n.toString(16); }
function rva(p) { try { return "0x" + p.sub(BASE).toInt32().toString(16); } catch(_) { return '?'; } }

// All [rcx+31Ch]==2 check functions (filtered: size 0x40~0x200)
// Safe hooks confirmed across 30+ interactive sessions
const TARGETS = [
    { rva: 0x9C4780, name: 'DISPATCH' },
    { rva: 0x99E820, name: 'LOOKUP' },
];

let currentMsgId = -1;
let allHits = {};
let totalFired = 0;

// --- Dispatch context ---
try {
    Interceptor.attach(BASE.add(0x9C4780), {
        onEnter(args) { currentMsgId = args[2].toInt32(); },
        onLeave() { currentMsgId = -1; }
    });
} catch(e) { console.log(`[!] DISPATCH hook failed: ${e.message}`); }

// --- Hook all check functions ---
for (const t of TARGETS) {
    allHits[t.name] = 0;
    try {
        const addr = BASE.add(t.rva);
        Interceptor.attach(addr, {
            onEnter(args) {
                allHits[t.name]++;
                totalFired++;

                const m2 = currentMsgId === 2 ? ' [msgId=2!]' : '';
                console.log(`[${ts()}] ${t.name} #${allHits[t.name]} caller=${rva(this.returnAddress)} msgId=${currentMsgId}${m2}`);

                // On first hit for any check function, dump context
                if (allHits[t.name] === 1 && t.name.startsWith('CHK')) {
                    try {
                        const a4 = args[3]; if (!a4||a4.isNull()) return;
                        const field = a4.add(0x31C).readU32();
                        console.log(`  → a4=${a4}  a4+0x31C=${field} ${field===2?'✓':'✗'}`);
                        // Stack trace
                        const bt = Thread.backtrace(this.context, Backtracer.ACCURATE);
                        for (let i=0;i<6;i++) console.log(`    #${i}: ${bt[i]} ${DebugSymbol.fromAddress(bt[i])}`);
                    } catch(_) {}
                }
            }
        });
    } catch(e) {
        console.log(`[!] ${t.name} hook FAILED: ${e.message}`);
    }
}

// --- Periodically print summary ---
setInterval(function() {
    if (totalFired === 0) return;
    const fired = Object.entries(allHits).filter(([,c]) => c > 0);
    if (fired.length > 0) {
        console.log(`\n[${ts()}] SUMMARY: ${fired.length} targets fired (${totalFired} total)`);
        for (const [name, count] of fired) {
            console.log(`  ${name}: ${count}`);
        }
    }
}, 10000);

console.log(`[${ts()}] Cold start scan ACTIVE. ${TARGETS.length} hooks.`);
console.log(`[${ts()}] All [rcx+31Ch]==2 check functions monitored.`);
console.log(`[${ts()}] Enter armory when ready. Summary every 10s.\n`);
