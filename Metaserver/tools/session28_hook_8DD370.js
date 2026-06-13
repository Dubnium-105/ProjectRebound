// =============================================================================
// Session 28: Hook sub_8DD370 — the result poster
//
// ALL message handlers call sub_8DD370 to post their results.
// By hooking this, we see which handler is called for GetPlayerArchiveV2.
//
// sub_8DD370(..., result_struct, queue_ptr)
//   a4 = *(a4 + 1768) = the posting target queue
//   a3/rdx = the result struct
//
// Usage: frida -p <PID> -l tools/session28_hook_8DD370.js
// =============================================================================

const BASE = (() => {
    for (const n of ["ProjectBoundarySteam-Win64-Shipping.exe","ProjectBoundary-Win64-Shipping.exe"])
        { try { return Process.getModuleByName(n).base; } catch(_){} }
    for (const m of Process.enumerateModules())
        { if (m.name.endsWith('.exe') && m.name.toLowerCase().includes('boundary')) return m.base; }
    throw new Error("Module not found");
})();

function hex(n) { return n instanceof NativePointer ? n.toString() : "0x" + n.toString(16); }

const sub8DD370 = BASE.add(0x8DD370);
let hit = 0;

Interceptor.attach(sub8DD370, {
    onEnter(args) {
        hit++;
        if (hit > 10) return;

        // args: a1, a2, rdx=a3(result), rcx(???)
        // Actually: (a1, a2, a3, a4) per IDA
        // sub_8DD370(a1, a2, result_struct, queue_ptr)
        const result = args[2]; // r8 = a3 = result struct
        const queuePtr = args[3]; // r9 = a4 = *(handler's a4 + 1768)

        console.log(`\n[sub_8DD370 #${hit}]`);
        console.log(`  result=${result}  queuePtr=${queuePtr}`);
        console.log(`  ret=${hex(this.returnAddress)}`);

        // Dump result struct (first 32 bytes)
        try {
            if (result && !result.isNull()) {
                console.log(`  result hex:`);
                console.log(hexdump(result.readByteArray(32), {offset:0,length:32,header:false,ansi:false}));
            }
        } catch (_) {}

        // Stack trace to find which handler called this
        const bt = Thread.backtrace(this.context, Backtracer.ACCURATE);
        console.log(`  Stack:`);
        for (let i = 0; i < 8; i++) {
            const sym = DebugSymbol.fromAddress(bt[i]);
            const s = sym.toString();
            if (s.includes('ProjectBoundary') || i < 3)
                console.log(`    #${i}: ${bt[i]}  ${s}`);
        }
    }
});

console.log(`[+] Hooked sub_8DD370 @ ${sub8DD370}`);
console.log(`[*] Enter armory. Stack traces will show which handler called.\n`);
