// =============================================================================
// Session 14: Trace source of ErrorCode=4 in sub_887BA0 + broader search
//
// 1. Hook sub_887BA0 → when ErrorCode=4 is copied, dump source & stack
// 2. Hook sub_9B9F3B entry → log its caller (where a3 comes from)
// 3. Also: search IDA xref patterns in running code
//
// Usage: frida -p <PID> -l tools/session14_trace_source.js
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

// =============================================================================
// 1. Hook sub_887BA0 — dump SOURCE struct when ErrorCode=4
// =============================================================================
const copyFunc = BASE.add(0x887BA0);
let copyHit = 0;

Interceptor.attach(copyFunc, {
    onEnter(args) {
        const src = args[2]; // a3 = source struct
        const dst = args[3]; // a4 = destination
        if (!src || src.isNull()) return;

        let ec = -1;
        try { ec = src.add(0x0C).readS32(); } catch (_) {}

        // Also check at +0x34 (a3+40+0x0C in sub_9B9F3B's frame)
        const srcAlt = args[0]; // a1 — the big struct
        let ecAlt = -1;
        try { ecAlt = srcAlt.add(0x34).readS32(); } catch (_) {}

        if (ec === 4 || ecAlt === 4) {
            copyHit++;
            if (copyHit > 5) return;

            console.log(`\n=== COPY #${copyHit} (ErrorCode=4 found) ===`);
            console.log(`  src(a3)=${src}  ec@+0C=${ec}`);
            console.log(`  bigStruct(a1)=${srcAlt}  ec@+34=${ecAlt}`);
            console.log(`  dst(a4)=${dst}`);

            // Dump source (64 bytes)
            try {
                console.log(`  src hex:`);
                console.log(hexdump(src.readByteArray(64), {offset:0,length:64,header:false,ansi:false}));
            } catch (_) {}

            // Dump big struct around +0x28 (where source sub-struct starts)
            try {
                console.log(`  bigStruct+0x28:`);
                console.log(hexdump(srcAlt.add(0x28).readByteArray(48), {offset:0,length:48,header:false,ansi:false}));
            } catch (_) {}

            // Stack trace
            console.log(`  Stack:`);
            const bt = Thread.backtrace(this.context, Backtracer.ACCURATE);
            for (let i = 0; i < 10; i++) {
                const r = rva(bt[i]);
                const sym = DebugSymbol.fromAddress(bt[i]);
                // Only show game module frames
                const symStr = sym.toString();
                if (symStr.includes('ProjectBoundary') || symStr.includes('ntdll') || symStr.includes('kernel'))
                    console.log(`    #${i}: RVA=${r}  ${symStr}`);
            }
        }
    }
});
console.log(`[+] Hooked sub_887BA0 @ ${copyFunc}`);

// =============================================================================
// 2. Hook sub_9B9F3B entry — find who calls it, trace a3
// =============================================================================
const callerFunc = BASE.add(0x9B9F3B);
let callerHit = 0;
Interceptor.attach(callerFunc, {
    onEnter(args) {
        callerHit++;
        if (callerHit > 3) return;

        console.log(`\n[sub_9B9F3B #${callerHit}] ENTRY`);
        console.log(`  a1(rcx)=${args[0]}  a2(rdx)=${args[1]}  a3(r8)=${args[2]}  a4(r9)=${args[3]}`);
        console.log(`  ret=${rva(this.returnAddress)}`);

        // Check a3 for ErrorCode
        const a3 = args[2];
        if (a3 && !a3.isNull()) {
            try { console.log(`  a3+0x0C=${a3.add(0x0C).readS32()}`); } catch (_) {}
            try { console.log(`  a3+0x28+0x0C=${a3.add(0x28+0x0C).readS32()}`); } catch (_) {}
        }

        // Stack to find caller of sub_9B9F3B
        const bt = Thread.backtrace(this.context, Backtracer.ACCURATE);
        for (let i = 0; i < 6; i++) {
            const r = rva(bt[i]);
            const sym = DebugSymbol.fromAddress(bt[i]);
            if (sym.toString().includes('ProjectBoundary'))
                console.log(`  #${i}: RVA=${r}  ${sym}`);
        }
    }
});
console.log(`[+] Hooked sub_9B9F3B @ ${callerFunc}`);

function stats() {
    console.log(`\nsub_887BA0 hits: ${copyHit}`);
    console.log(`sub_9B9F3B hits: ${callerHit}`);
}

console.log(`[*] Ready. Trigger GetPlayerArchiveV2 (enter armory).\n`);
