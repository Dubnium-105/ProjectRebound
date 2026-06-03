// =============================================================================
// Session 15: Group sub_99E820 callers by return address + ErrorCode
//
// Finds ALL callers of sub_99E820 and groups them by RVA.
// For each caller, shows how many times ErrorCode=0 vs ErrorCode=4.
// This tells us which caller is the source of ErrorCode=4 structs.
//
// Usage: frida -p <PID> -l tools/session15_group_calls.js
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

const lookupFunc = BASE.add(0x99E820);
console.log(`[+] sub_99E820: ${lookupFunc}`);

// { callerRVA → { ec0: N, ec4: N, ecOther: N, sampleStruct: addr, sampleStack: [...] } }
const callers = {};

let totalCalls = 0;
const MAX_SAMPLES = 50;

Interceptor.attach(lookupFunc, {
    onEnter(args) {
        totalCalls++;
        const retRva = rva(this.returnAddress);

        if (!callers[retRva]) {
            callers[retRva] = { ec0: 0, ec4: 0, ecOther: 0, firstSeen: totalCalls, hasStack: false };
        }
        const c = callers[retRva];

        // Find ErrorCode in args
        let ec = '?';
        for (const reg of [this.context.rcx, this.context.rdx, this.context.r8, this.context.r9]) {
            if (!reg || reg.isNull()) continue;
            try {
                const v = reg.add(0x0C).readS32();
                if (v === 0 || v === 4) { ec = v; break; }
            } catch (_) {}
        }

        if (ec === 0) c.ec0++;
        else if (ec === 4) c.ec4++;
        else c.ecOther++;

        // Capture stack trace for first ErrorCode=4 per caller
        if (ec === 4 && !c.hasStack && totalCalls < 500) {
            c.hasStack = true;
            c.sampleStack = [];
            const bt = Thread.backtrace(this.context, Backtracer.ACCURATE);
            for (let i = 0; i < 8; i++) {
                c.sampleStack.push({ rva: rva(bt[i]), sym: DebugSymbol.fromAddress(bt[i]).toString() });
            }
        }
    }
});

function analyze() {
    console.log(`\n=== sub_99E820 Caller Analysis (${totalCalls} total calls) ===`);

    // Sort by ErrorCode=4 count descending
    const sorted = Object.entries(callers).sort((a, b) => b[1].ec4 - a[1].ec4);

    for (const [callerRva, data] of sorted) {
        const total = data.ec0 + data.ec4 + data.ecOther;
        if (total === 0) continue;

        const markers = [];
        if (data.ec4 > 0) markers.push(`EC4 x${data.ec4}`);
        if (data.ec0 > 0) markers.push(`EC0 x${data.ec0}`);

        console.log(`\n  Caller RVA=${callerRva}  (total=${total}  #${data.firstSeen})`);
        console.log(`    ${markers.join('  ')}`);

        if (data.sampleStack) {
            console.log(`    Sample stack (from EC4 call):`);
            for (const frame of data.sampleStack.slice(0, 6)) {
                console.log(`      RVA=${frame.rva}  ${frame.sym}`);
            }
        }
    }

    // Summary
    const withEC4 = sorted.filter(([,d]) => d.ec4 > 0);
    console.log(`\n=== ${withEC4.length} callers pass ErrorCode=4 structs ===`);
    for (const [rva, data] of withEC4) {
        console.log(`  RVA=${rva}  EC4 x${data.ec4}`);
        console.log(`    → In IDA: G → ${rva}, find the call sub_99E820 instruction`);
    }
}

console.log(`[*] Hook active. Trigger GetPlayerArchiveV2 (enter armory).`);
console.log(`[*] Then: analyze()\n`);
