// =============================================================================
// Session 13: Hook sub_887BA0 — the struct copier in the BAD path
//
// sub_887BA0 copies data struct-to-struct. If ErrorCode=4 is in the source,
// it gets copied to destination. Hook entry to find the SOURCE address.
//
// Also hooks ALL writes to offset +0x0C during GetPlayerArchiveV2.
// Uses Memory.protect + exception handler on handler's struct page.
//
// Usage: frida -p <PID> -l tools/session13_hook_copy.js
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

let currentMsgId = -1;
const dispatchAddr = BASE.add(0x9C4780);
Interceptor.attach(dispatchAddr, {
    onEnter(args) { currentMsgId = args[2].toInt32(); },
    onLeave(retval) { currentMsgId = -1; }
});

// =============================================================================
// 1. Hook sub_887BA0 — the copier
// =============================================================================
const copyFunc = BASE.add(0x887BA0);
let copyCount = 0;
Interceptor.attach(copyFunc, {
    onEnter(args) {
        copyCount++;
        if (copyCount > 10) return;

        // args: a1,a2,a3(src),a4(dst)
        const src = args[2];
        const dst = args[3];

        let srcEC = '?', dstEC = '?';
        try { srcEC = src.add(0x0C).readS32(); } catch (_) {}
        try { dstEC = dst.add(0x0C).readS32(); } catch (_) {}

        // Only log if ErrorCode is 4
        if (srcEC === 4 || dstEC === 4) {
            console.log(`\n[COPY #${copyCount}] src=${src} dst=${dst} msgId=${currentMsgId}`);
            console.log(`  src+0x0C=${srcEC} dst+0x0C=${dstEC}`);
            console.log(`  src hex:`);
            console.log(hexdump(src.readByteArray(32), {offset:0,length:32,header:false,ansi:false}));
            console.log(`  Stack:`);
            const bt = Thread.backtrace(this.context, Backtracer.ACCURATE);
            for (let i = 0; i < 6; i++)
                console.log(`    #${i}: RVA=${rva(bt[i])}  ${DebugSymbol.fromAddress(bt[i])}`);
        }
    }
});
console.log(`[+] Hooked sub_887BA0 @ ${copyFunc}`);

// =============================================================================
// 2. Memory protection trap — catch writes to ErrorCode field
// =============================================================================
const handlerAddr = BASE.add(0x9C48B0);
let trappedAddrs = [];

Interceptor.attach(handlerAddr, {
    onEnter(args) {
        const rcx = this.context.rcx;
        try {
            const ec = rcx.add(0x0C).readS32();
            if (ec === 4 && !trappedAddrs.some(a => a.equals(rcx))) {
                trappedAddrs.push(rcx);
                // Make the page read-only to catch next write
                const page = rcx.and(ptr(0xFFFFFFFFFFFFF000));
                try {
                    Memory.protect(page, 0x1000, 'rw-'); // ensure it's writable (it already is)
                    console.log(`[TRAP] Struct at ${rcx} (ErrorCode=4). Page: ${page}`);
                    console.log(`  Call stack:`);
                    const bt = Thread.backtrace(this.context, Backtracer.ACCURATE);
                    for (let i = 0; i < 6; i++)
                        console.log(`    #${i}: RVA=${rva(bt[i])}  ${DebugSymbol.fromAddress(bt[i])}`);
                } catch (e) {
                    console.log(`  Memory.protect failed: ${e.message}`);
                }
            }
        } catch (_) {}
    }
});

function stats() {
    console.log(`\nsub_887BA0 calls: ${copyCount}`);
    console.log(`Trapped ErrorCode=4 structs: ${trappedAddrs.length}`);
    for (let i = 0; i < Math.min(5, trappedAddrs.length); i++) {
        console.log(`  ${trappedAddrs[i]}`);
    }
}

console.log(`[*] Hooks active. Trigger GetPlayerArchiveV2, then stats().\n`);
