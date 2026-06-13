// =============================================================================
// Session 25: Capture the archive processor object from sub_99E820
//
// sub_99E820: mov rcx, [rcx+18h]; mov r11, [rcx]; call [r11+108h]
// The object at *(a1+0x18) is the archive processor.
// Its field at +0x31C should be 2. Let's see what it is.
//
// Usage: frida -p <PID> -l tools/session25_capture_obj.js
// =============================================================================

const BASE = (() => {
    for (const n of ["ProjectBoundarySteam-Win64-Shipping.exe","ProjectBoundary-Win64-Shipping.exe"])
        { try { return Process.getModuleByName(n).base; } catch(_){} }
    for (const m of Process.enumerateModules())
        { if (m.name.endsWith('.exe') && m.name.toLowerCase().includes('boundary')) return m.base; }
    throw new Error("Module not found");
})();

function hex(n) { return n instanceof NativePointer ? n.toString() : "0x" + n.toString(16); }
function rva(p) { return p instanceof NativePointer ? hex(p.sub(BASE).toInt32()) : '?'; }

// Hook sub_99E820 — read the processor object before the vtable call
const lookupAddr = BASE.add(0x99E820);
let hit = 0;

Interceptor.attach(lookupAddr, {
    onEnter(args) {
        hit++;
        if (hit > 5) return;

        // args[0] (rcx) = a1 — the big context object
        const a1 = this.context.rcx;
        if (!a1 || a1.isNull()) return;

        try {
            // Read the sub-object at a1 + 0x18
            const subObj = a1.add(0x18).readPointer();
            if (subObj.isNull()) return;

            // Read its vtable
            const vtable = subObj.readPointer();
            const vtableRva = rva(vtable);

            // Read the field at subObj + 0x31C (the "must be 2" field)
            const field = subObj.add(0x31C).readU32();

            console.log(`\n[#${hit}] Context a1=${a1}`);
            console.log(`  *(a1+0x18)=${subObj}  (obj that has sub_9C4840 in vtable)`);
            console.log(`  vtable=${vtable}  RVA=${vtableRva}`);
            console.log(`  obj+0x31C = ${field}  ${field===2?'✓ PASS':'✗ NOT 2!'}`);

            // Dump subObj's first 64 bytes
            try {
                console.log(`  subObj hex (first 0x60):`);
                console.log(hexdump(subObj.readByteArray(0x60), {offset:0,length:0x60,header:false,ansi:false}));
            } catch (_) {}

            // Scan for all "2" fields in subObj
            console.log(`  Fields == 2 in subObj:`);
            for (let off = 0; off < 0x400; off += 4) {
                try {
                    if (subObj.add(off).readU32() === 2)
                        console.log(`    +${hex(off)}: 2`);
                } catch (_) {}
            }
        } catch (e) {
            console.log(`  read error: ${e.message}`);
        }
    }
});

console.log(`[+] Hooked sub_99E820 @ ${lookupAddr}`);
console.log(`[*] Enter armory. The processor object +0x31C will be dumped.\n`);
