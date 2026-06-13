// Session 29: Diagnose vtable address from sub_99E820
const BASE = (() => {
    for (const n of ["ProjectBoundarySteam-Win64-Shipping.exe","ProjectBoundary-Win64-Shipping.exe"])
        { try { return Process.getModuleByName(n).base; } catch(_){} }
    for (const m of Process.enumerateModules())
        { if (m.name.endsWith('.exe') && m.name.toLowerCase().includes('boundary')) return m.base; }
    throw new Error("Module not found");
})();
function hex(n) { return n instanceof NativePointer ? n.toString() : "0x" + n.toString(16); }

const lookupAddr = BASE.add(0x99E820);
let hit = 0;
Interceptor.attach(lookupAddr, {
    onEnter(args) {
        hit++; if (hit > 5) return;
        const a1 = this.context.rcx;
        if (!a1||a1.isNull()) return;
        try {
            const subObj = a1.add(0x18).readPointer();
            if (subObj.isNull()) { hit--; return; }
            const rawVt = subObj.readPointer();
            console.log(`\n[#${hit}] subObj=${hex(subObj)}  raw_vtable=${hex(rawVt)}`);
            // Try: is rawVt within any loaded module?
            let foundIn = 'NONE';
            Process.enumerateModules().forEach(m => {
                if (rawVt.compare(m.base)>=0 && rawVt.compare(m.base.add(m.size))<0)
                    foundIn = m.name;
            });
            console.log(`  vtable module: ${foundIn}`);
            // Try multiple base+offset combos
            console.log(`  Try BASE+rawVt: ${hex(BASE.add(rawVt.toUInt32()))}`);
            try { console.log(`    → [0]: ${hex(BASE.add(rawVt.toUInt32()).readPointer())}`); } catch(e){console.log(`    → ERR: ${e.message}`);}
            // Try reading as-is
            try {
                Memory.protect(rawVt, 0x1000, 'rwx');
                console.log(`  Memory.protect OK. First 8 ptrs:`);
                for(let i=0;i<8;i++) console.log(`    [${i}]: ${hex(rawVt.add(i*8).readPointer())}`);
                // Index 33
                const h = rawVt.add(33*8).readPointer();
                console.log(`  [33]: ${hex(h)}  RVA=0x${h.sub(BASE).toInt32().toString(16)}`);
            } catch(e){ console.log(`  read err: ${e.message}`); }
        } catch(e) { console.log(`err: ${e.message}`); }
    }
});
console.log(`[+] Hooked. Enter armory.\n`);
