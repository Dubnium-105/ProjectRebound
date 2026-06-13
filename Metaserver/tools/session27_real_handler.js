// =============================================================================
// Session 27: Try BASE+vtable to find the real handler
//
// The vtable stored in the object is 0x1659AC (maybe un-relocated RVA).
// Try BASE + 0x1659AC to access vtable[33].
//
// Usage: frida -p <PID> -l tools/session27_real_handler.js
// =============================================================================

const BASE = (() => {
    for (const n of ["ProjectBoundarySteam-Win64-Shipping.exe","ProjectBoundary-Win64-Shipping.exe"])
        { try { return Process.getModuleByName(n).base; } catch(_){} }
    for (const m of Process.enumerateModules())
        { if (m.name.endsWith('.exe') && m.name.toLowerCase().includes('boundary')) return m.base; }
    throw new Error("Module not found");
})();

function hex(n) { return n instanceof NativePointer ? n.toString() : "0x" + n.toString(16); }

// Try to read vtable at BASE + 0x1659AC
const vtableMaybe = BASE.add(0x1659AC);
console.log(`[+] Trying vtable at ${vtableMaybe} (BASE+RVA)`);

try {
    // Read first 16 entries of the vtable (indices 0-15)
    console.log(`[+] Vtable entries (first 16):`);
    for (let i = 0; i < 16; i++) {
        try {
            const entry = vtableMaybe.add(i * 8).readPointer();
            const rva = entry.sub(BASE).toInt32();
            console.log(`  [${i}]: ${hex(entry)}  RVA=0x${rva.toString(16)}`);
        } catch (_) {}
    }

    // Read index 33 (0x108/8)
    const idx33 = vtableMaybe.add(33 * 8).readPointer();
    const rva33 = idx33.sub(BASE).toInt32();
    console.log(`\n[33]: ${hex(idx33)}  RVA=0x${rva33.toString(16)}`);
    if (rva33 === 0x9C4840) console.log(`  → sub_9C4840!`);
    else console.log(`  → Something else. Check IDA.`);
} catch (e) {
    console.log(`[!] Vtable read failed: ${e.message}`);

    // Try reading directly from the raw address 0x1659AC
    console.log(`[*] Trying raw 0x1659AC...`);
    try {
        const rawPage = ptr(0x1659AC).and(ptr(0xFFFFFFFFFFFFF000));
        Memory.protect(rawPage, 0x1000, 'rwx');
        console.log(hexdump(ptr(0x1659AC).readByteArray(128), {offset:0,length:128,header:false,ansi:false}));
    } catch (e2) {
        console.log(`[!] Raw also failed: ${e2.message}`);
    }
}

console.log(`\n[*] Done.`);

