// Session 31: Read vtable[33] for ALL valid vtables
const BASE = (() => {
    for (const n of ["ProjectBoundarySteam-Win64-Shipping.exe","ProjectBoundary-Win64-Shipping.exe"])
        { try { return Process.getModuleByName(n).base; } catch(_){} }
    for (const m of Process.enumerateModules())
        { if (m.name.endsWith('.exe') && m.name.toLowerCase().includes('boundary')) return m.base; }
    throw new Error("Module not found");
})();
function hex(n) { return n instanceof NativePointer ? n.toString() : "0x" + n.toString(16); }
const lookupAddr = BASE.add(0x99E820);
const seen = new Set();
Interceptor.attach(lookupAddr, {
    onEnter(args) {
        const a1 = this.context.rcx; if (!a1||a1.isNull()) return;
        try {
            const subObj = a1.add(0x18).readPointer(); if (subObj.isNull()) return;
            const vt = subObj.readPointer();
            const k = hex(vt);
            // Only process valid vtables inside the game module
            if (vt.compare(BASE)<0 || vt.compare(BASE.add(0x20000000))>0) return;
            if (seen.has(k)) return;
            seen.add(k);
            // Read vtable index 33
            try {
                const h = vt.add(33*8).readPointer();
                const rva = h.sub(BASE).toInt32();
                console.log(`\n[VTABLE ${k}] vtable[33]=${hex(h)} RVA=0x${rva.toString(16)}`);
                // Also scan nearby indices for known handler RVAs
                console.log(`  Nearby indices:`);
                for (let i = 30; i <= 36; i++) {
                    try {
                        const e = vt.add(i*8).readPointer();
                        const er = e.sub(BASE).toInt32();
                        const marker = (er===0x9C4840||er===0x9C4840) ? ' ← sub_9C4840!' :
                                       (er===0x99B4C0) ? ' ← sub_99B4C0!' :
                                       (er===0x9BBB10) ? ' ← sub_9BBB10!' : '';
                        console.log(`    [${i}]: ${hex(e)} RVA=0x${er.toString(16)}${marker}`);
                    } catch(_) {}
                }
            } catch(e) { console.log(`  read err: ${e.message}`); }
        } catch(_) {}
    }
});
console.log(`[+] Hooked. Enter armory.\n`);
