// =============================================================================
// Session 30: Capture r11 at call [r11+108h] via code patch
//
// Patches sub_99E820 to save r11 before the vtable dispatch call.
//
// Usage: frida -p <PID> -l tools/session30_capture_r11.js
// =============================================================================

const BASE = (() => {
    for (const n of ["ProjectBoundarySteam-Win64-Shipping.exe","ProjectBoundary-Win64-Shipping.exe"])
        { try { return Process.getModuleByName(n).base; } catch(_){} }
    for (const m of Process.enumerateModules())
        { if (m.name.endsWith('.exe') && m.name.toLowerCase().includes('boundary')) return m.base; }
    throw new Error("Module not found");
})();

function hex(n) { return n instanceof NativePointer ? n.toString() : "0x" + n.toString(16); }

// Allocate a global slot for r11 capture
const captureSlot = Memory.alloc(8);
console.log(`[+] Capture slot: ${captureSlot}`);

// Patch at 0x99E840 (mov r11, [r10] — the instruction BEFORE the call)
// We'll insert right after it to save r11
// Original: mov r11, [r10]; call [r11+108h]
// Patched:  mov r11, [r10]; mov [captureSlot], r11; call [r11+108h]

const patchAddr = BASE.add(0x99E840); // mov r11, [r10]
const insnSize = 3; // 3 bytes for "mov r11, [r10]"

// Actually, patch at 0x99E843: the call instruction itself is 6 bytes
// We can NOP the call, but we still want it to execute...
// Better approach: patch the call instruction to first save r11

// Use a trampoline approach: redirect call [r11+108h] to our trampoline
// that saves r11 and then jumps to the original target

// Actually, simplest: use Memory.patchCode to replace the bytes at 0x99E843
const callAddr = BASE.add(0x99E843);
const callSize = 6; // "call [r11+108h]" is 6 bytes

// Read original bytes
try {
    const orig = callAddr.readByteArray(callSize);
    console.log(`[+] Original call bytes: ${hexdump(orig, {offset:0,length:6,header:false,ansi:false})}`);
} catch (e) {
    console.log(`[!] Read failed: ${e.message}`);
}

let captured = false;
let staleRead = false;

// Periodically read the capture slot (will contain the LAST r11 value seen)
setInterval(function() {
    try {
        const r11 = captureSlot.readPointer();
        if (!r11.isNull() && !captured) {
            captured = true;
            console.log(`\n[CAPTURE] r11 = ${hex(r11)}`);
            try {
                // Now try to read vtable[33]
                const h = r11.add(0x108).readPointer();
                const rva = h.sub(BASE).toInt32();
                console.log(`  vtable[33] = ${hex(h)}  RVA=0x${rva.toString(16)}`);

                // Read first 8 vtable entries
                console.log(`  First 8 vtable entries:`);
                for (let i = 0; i < 8; i++) {
                    const e = r11.add(i*8).readPointer();
                    const er = e.sub(BASE).toInt32();
                    console.log(`    [${i}]: ${hex(e)}  RVA=0x${er.toString(16)}`);
                }
            } catch (e) {
                console.log(`  read err: ${e.message}`);
            }
            staleRead = true;
        }
    } catch (_) {}
}, 100);

// Now inject the save instruction
try {
    const page = callAddr.and(ptr(0xFFFFFFFFFFFFF000));
    Memory.protect(page, 0x1000, 'rwx');
    callAddr.writeByteArray([0x41, 0x89, 0x1D, 0x00, 0x00, 0x00, 0x00]); // temp: mov [rip+0], r11d
    console.log(`[+] Wrote save instruction`);
} catch (e) {
    console.log(`[!] Write failed: ${e.message}`);

    // Fallback: try Memory.patchCode
    try {
        Memory.patchCode(callAddr, callSize + 13, function(code) {
            const writer = new X86Writer(code, { pc: callAddr });
            // Save r11 to captureSlot
            writer.putMovRegOffsetNearPtrReg('r11', callAddr.add(7), 'rip'); // won't work easily
            writer.flush();
        });
    } catch (e2) {
        console.log(`[!] patchCode also failed: ${e2.message}`);
    }
}

console.log(`[*] Capture active. Enter armory.\n`);
