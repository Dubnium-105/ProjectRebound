// =============================================================================
// Session 7: Check what's at a4+0x31C — the critical validation field
//
// sub_9BF020 checks: if (*(a4+0x31C) != 2) return 0 (fail)
// We need to know: what is a4, and what is the field value?
//
// Usage: frida -p <PID> -l tools/session7_check_field.js
// =============================================================================

const VALIDATOR_RVA = 0x9BF020;

const BASE = (() => {
    for (const n of ["ProjectBoundarySteam-Win64-Shipping.exe","ProjectBoundary-Win64-Shipping.exe"])
        { try { return Process.getModuleByName(n).base; } catch(_){} }
    for (const m of Process.enumerateModules())
        { if (m.name.endsWith('.exe') && m.name.toLowerCase().includes('boundary')) return m.base; }
    throw new Error("Module not found");
})();

function hex(n) { return n instanceof NativePointer ? n.toString() : "0x" + n.toString(16); }

const validator = BASE.add(VALIDATOR_RVA);
console.log(`[+] sub_9BF020: ${validator}`);

let hit = 0;
const MAX_LOG = 10;

Interceptor.attach(validator, {
    onEnter(args) {
        if (hit++ >= MAX_LOG) return;

        // args: rcx=args[0], rdx=args[1], r8=args[2], r9=args[3]
        const a1 = args[0], a2 = args[1], a3 = args[2], a4 = args[3], a5 = args[4], a6 = args[5];

        let fieldVal = '?';
        try { fieldVal = a4.add(0x31C).readS32().toString(); } catch (_) {}

        const status = fieldVal === '2' ? '✓ PASS' : '✗ FAIL (returns 0)';

        console.log(`\n[#${hit}] a4=${a4}  a4+0x31C=${fieldVal}  ${status}`);

        // Try to identify what a4 is — read vtable pointer
        try {
            const vt = a4.readPointer();
            const sym = DebugSymbol.fromAddress(vt);
            console.log(`  a4 vtable: ${vt}  [${sym}]`);
        } catch (_) {}

        // Dump first 64 bytes of a4
        try {
            console.log(`  a4 hex:`);
            console.log(hexdump(a4.readByteArray(64), { offset: 0, length: 64, header: false, ansi: false }));
        } catch (_) {}

        // Also dump a4 around +0x31C
        try {
            console.log(`  a4+0x310 to +0x330:`);
            console.log(hexdump(a4.add(0x310).readByteArray(32), { offset: 0x310, length: 32, header: true, ansi: false }));
        } catch (_) {}
    },
    onLeave(retval) {
        if (hit <= MAX_LOG) {
            console.log(`  → returns: ${retval} (${retval.toInt32() ? 'PASS' : 'FAIL'})`);
        }
    }
});

console.log(`\n[*] Hook active. Trigger GetPlayerArchiveV2.\n`);
