// Session 30: Monitor *(a1+0x18) over time — does it get lazily initialized?
const BASE = (() => {
    for (const n of ["ProjectBoundarySteam-Win64-Shipping.exe","ProjectBoundary-Win64-Shipping.exe"])
        { try { return Process.getModuleByName(n).base; } catch(_){} }
    for (const m of Process.enumerateModules())
        { if (m.name.endsWith('.exe') && m.name.toLowerCase().includes('boundary')) return m.base; }
    throw new Error("Module not found");
})();
function hex(n) { return n instanceof NativePointer ? n.toString() : "0x" + n.toString(16); }

const lookupAddr = BASE.add(0x99E820);
let lastVtable = null;
let sameCount = 0;

Interceptor.attach(lookupAddr, {
    onEnter(args) {
        const a1 = this.context.rcx;
        if (!a1||a1.isNull()) return;
        try {
            const subObj = a1.add(0x18).readPointer();
            if (subObj.isNull()) return;
            const vt = subObj.readPointer();
            const vtHex = hex(vt);

            if (!lastVtable || !vt.equals(lastVtable)) {
                if (lastVtable) console.log(`  (changed after ${sameCount} calls with same value)`);
                console.log(`\n[VTABLE] ${vtHex}  subObj=${hex(subObj)}  a1=${hex(a1)}`);

                // Try to find this address in a module
                let found = false;
                Process.enumerateModules().forEach(m => {
                    if (vt.compare(m.base)>=0 && vt.compare(m.base.add(m.size))<0) {
                        console.log(`  IN MODULE: ${m.name} @ ${m.base}`);
                        found = true;
                    }
                });
                if (!found) console.log(`  NOT IN ANY MODULE`);

                // Try reading from it
                try {
                    const v0 = vt.readPointer();
                    console.log(`  *vt = ${hex(v0)}`);
                    // Check if *vt looks like a pointer to the game module
                    if (v0.compare(BASE)>0 && v0.compare(BASE.add(0x20000000))<0)
                        console.log(`  → points into game module! RVA=0x${v0.sub(BASE).toInt32().toString(16)}`);
                } catch(e) { console.log(`  *vt read err: ${e.message}`); }

                lastVtable = vt;
                sameCount = 0;
            }
            sameCount++;
        } catch(_) {}
    }
});
console.log(`[+] Monitoring *(a1+0x18) vtable. Enter armory.\n`);
