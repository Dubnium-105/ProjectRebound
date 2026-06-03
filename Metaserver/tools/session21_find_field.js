// =============================================================================
// Session 21: Find the "must be 2" field in GetPlayerArchiveV2 path
//
// Hooks sub_9BF020 (validation) and sub_9C4780 (dispatch, msgId=2).
// When sub_9BF020 fires, logs a4+796 value and all state fields.
// When msgId=2 fires, logs the response context for analysis.
//
// Usage: frida -p <PID> -l tools/session21_find_field.js
// =============================================================================

const BASE = (() => {
    for (const n of ["ProjectBoundarySteam-Win64-Shipping.exe","ProjectBoundary-Win64-Shipping.exe"])
        { try { return Process.getModuleByName(n).base; } catch(_){} }
    for (const m of Process.enumerateModules())
        { if (m.name.endsWith('.exe') && m.name.toLowerCase().includes('boundary')) return m.base; }
    throw new Error("Module not found");
})();

function hex(n) { return n instanceof NativePointer ? n.toString() : "0x" + n.toString(16); }

// ---- Hook 1: sub_9BF020 — the "must be 2" check ----
const validatorAddr = BASE.add(0x9BF020);
let validHits = 0;

try {
    Interceptor.attach(validatorAddr, {
        onEnter(args) {
            validHits++;
            if (validHits > 5) return;
            const a4 = args[3]; // 4th arg
            console.log(`\n[sub_9BF020 #${validHits}]`);
            console.log(`  a4=${a4}  ret=${hex(this.returnAddress.sub(BASE).toInt32())}`);

            // Read the "must be 2" field at a4+796 (0x31C)
            try {
                const mustBe2 = a4.add(0x31C).readU32();
                console.log(`  a4+0x31C(mustBe2) = ${mustBe2} ${mustBe2 === 2 ? '✓' : '✗ FAILING!'}`);

                // Also scan for ALL fields with value 2 nearby
                console.log(`  Fields equal to 2 near a4:`);
                for (let off = 0; off < 0x400; off += 4) {
                    try {
                        const v = a4.add(off).readU32();
                        if (v === 2) console.log(`    +${hex(off)}: 2`);
                    } catch (_) {}
                }
            } catch (e) {
                console.log(`  read error: ${e.message}`);
            }

            // Dump a4
            try {
                console.log(`  a4 hex (first 0x60):`);
                console.log(hexdump(a4.readByteArray(0x60), {offset:0,length:0x60,header:false,ansi:false}));
            } catch (_) {}
        },
        onLeave(retval) {
            if (validHits <= 5) {
                console.log(`  → returns: ${retval} ${retval.toInt32() === 0 ? '(FAIL)' : '(PASS)'}`);
            }
        }
    });
    console.log(`[+] Hooked sub_9BF020 @ ${validatorAddr}`);
} catch(e) {
    console.log(`[!] sub_9BF020 hook failed: ${e.message}`);
}

// ---- Hook 2: sub_9C4780 for msgId=2 ----
const dispatchAddr = BASE.add(0x9C4780);
let dispHits = 0;

Interceptor.attach(dispatchAddr, {
    onEnter(args) {
        const msgId = args[2].toInt32();
        if (msgId !== 2) return;
        dispHits++;
        if (dispHits > 3) return;

        console.log(`\n[DISPATCH msgId=2 #${dispHits}]`);

        // Dump rcx (response context)
        const rcx = this.context.rcx;
        const rdx = this.context.rdx;
        console.log(`  rcx=${rcx}  rdx=${rdx}`);
        try {
            console.log(`  rcx hex:`);
            console.log(hexdump(rcx.readByteArray(0x60), {offset:0,length:0x60,header:false,ansi:false}));
        } catch (_) {}
        try {
            console.log(`  rdx hex:`);
            console.log(hexdump(rdx.readByteArray(0x40), {offset:0,length:0x40,header:false,ansi:false}));
        } catch (_) {}

        // Scan rcx for pointers we can follow
        console.log(`  Pointers in rcx:`);
        for (let off = 0; off < 0x40; off += 8) {
            try {
                const ptr = rcx.add(off).readPointer();
                if (!ptr.isNull() && ptr.compare(BASE) > 0) {
                    const rva = ptr.sub(BASE).toInt32();
                    console.log(`    +${hex(off)}: ${ptr}  RVA=${hex(rva)}`);
                }
            } catch (_) {}
        }
    }
});

function stats() {
    console.log(`\nsub_9BF020 hits: ${validHits}`);
    console.log(`Dispatch msgId=2 hits: ${dispHits}`);
}

console.log(`[*] Hooks active. Enter armory, then stats()`);
console.log(`[*] Look for sub_9BF020 output — that'"'"'s the validation target\n`);
