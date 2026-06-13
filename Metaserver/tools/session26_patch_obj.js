// =============================================================================
// Session 26: DIRECT PATCH — set obj+0x31C = 2 on the archive processor
//
// The processor object at *(ctx+0x18) has +0x31C = 0 (should be 2).
// Patch to 2 and see if weapons display correctly.
//
// Usage: frida -p <PID> -l tools/session26_patch_obj.js
// =============================================================================

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
let patched = false;

Interceptor.attach(lookupAddr, {
    onEnter(args) {
        hit++;
        if (hit > 30 || patched) return;

        try {
            const subObj = this.context.rcx.add(0x18).readPointer();
            if (subObj.isNull()) return;

            const oldVal = subObj.add(0x31C).readU32();

            if (oldVal !== 2) {
                // PATCH: set to 2
                const page = subObj.and(ptr(0xFFFFFFFFFFFFF000));
                Memory.protect(page, 0x1000, 'rwx');
                subObj.add(0x31C).writeU32(2);
                patched = true;

                const verify = subObj.add(0x31C).readU32();
                console.log(`\n[!!!] PATCHED obj+0x31C: ${oldVal} → ${verify}`);
                console.log(`  obj = ${subObj}`);
                console.log(`  This bypasses the "must be 2" check in sub_9C4840`);
                console.log(`  Now re-enter armory to test!`);
                console.log(`  (Patch is persistent — the object lives for the session)`);
            }
        } catch (e) {
            if (hit <= 3) console.log(`[#${hit}] err: ${e.message}`);
        }
    }
});

function check() {
    console.log(`[*] Checked ${hit} calls, patched: ${patched}`);
}

console.log(`[+] Hooked sub_99E820. Trigger GetPlayerArchiveV2.`);
console.log(`[*] The field will be patched from 0→2. check() for status.\n`);
