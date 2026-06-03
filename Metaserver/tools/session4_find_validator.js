// =============================================================================
// Session 4: Find who sets ErrorCode=4 — binary search on the call chain
//
// Strategy: Check the struct at rcx+0x0C (ErrorCode) at three points:
//   A) Entry of sub_9C4780 (dispatch)
//   B) Entry of sub_99E820 (hash lookup, called from sub_9C4780+0xFD)
//   C) Entry of handler at 0x9C48B0
//
// Whichever point sees ErrorCode become 4 tells us which function
// or its callees did the validation.
//
// Usage: frida -p <PID> -l tools/session4_find_validator.js
// =============================================================================

const DISPATCH_RVA = 0x9C4780;
const LOOKUP_RVA   = 0x99E820;
const HANDLER_RVA  = 0x9C48B0;

// --- Module ---
const BASE = (() => {
    for (const n of ["ProjectBoundarySteam-Win64-Shipping.exe","ProjectBoundary-Win64-Shipping.exe"])
        { try { return Process.getModuleByName(n).base; } catch(_){} }
    for (const m of Process.enumerateModules())
        { if (m.name.endsWith('.exe') && m.name.toLowerCase().includes('boundary')) return m.base; }
    throw new Error("Module not found");
})();

function hex(n) { return n instanceof NativePointer ? n.toString() : "0x" + n.toString(16); }

const dispatchFunc = BASE.add(DISPATCH_RVA);
const lookupFunc   = BASE.add(LOOKUP_RVA);
const handlerFunc  = BASE.add(HANDLER_RVA);

console.log(`[+] Dispatch: ${dispatchFunc}`);
console.log(`[+] Lookup:   ${lookupFunc}`);
console.log(`[+] Handler:  ${handlerFunc}`);

// =============================================================================
// Helper: read ErrorCode from struct at rcx+0x0C
// =============================================================================

function readErrorCode(ptr) {
    try { return ptr.add(0x0C).readS32(); } catch (_) { return -999; }
}

function errLabel(code) {
    if (code === 0) return "OK(0)";
    if (code === 4) return "FAIL(4)";
    return `?(${code})`;
}

// =============================================================================
// Log counters
// =============================================================================

let hitCount = 0;
const MAX_LOG = 8; // only log first N hits per checkpoint

// =============================================================================
// Checkpoint A: sub_9C4780 entry (dispatch function)
// =============================================================================

Interceptor.attach(dispatchFunc, {
    onEnter(args) {
        // r8 = MessageId (from Session 1)
        const msgId = args[2].toInt32();
        if (msgId !== 2) return; // only GetPlayerArchiveV2

        hitCount++;
        if (hitCount > MAX_LOG) return;

        // struct might be in rcx or rdx — probe both
        for (const [label, reg] of [['rcx', this.context.rcx], ['rdx', this.context.rdx]]) {
            const ec = readErrorCode(reg);
            if (ec !== -999) {
                console.log(`[A:DISPATCH  #${hitCount}] ${label}@+0x0C = ${errLabel(ec)}  msgId=${msgId}`);
            }
        }
    }
});

// =============================================================================
// Checkpoint B: sub_99E820 entry (hash table lookup)
// =============================================================================

Interceptor.attach(lookupFunc, {
    onEnter(args) {
        // sub_99E820(r1, r2, ...) — probe first 3 regs for struct pointer
        for (const reg of [this.context.rcx, this.context.rdx, this.context.r8]) {
            const ec = readErrorCode(reg);
            if (ec !== -999 && (ec === 0 || ec === 4)) {
                console.log(`[B:LOOKUP   #${hitCount}] ErrorCode = ${errLabel(ec)}  reg=${reg}`);
                break; // found it
            }
        }
    },
    onLeave(retval) {
        // Check if the returned value (or any arg) has ErrorCode changed
        for (const reg of [retval, this.context.rcx, this.context.rdx, this.context.r8]) {
            if (!reg || reg.isNull()) continue;
            const ec = readErrorCode(reg);
            if (ec === 4) {
                console.log(`[B:LOOKUP-RET] ErrorCode = ${errLabel(ec)}  at retval/reg=${reg}`);
                break;
            }
        }
    }
});

// =============================================================================
// Checkpoint C: Handler at 0x9C48B0 entry (GetPlayerArchiveV2 handler)
// =============================================================================

Interceptor.attach(handlerFunc, {
    onEnter(args) {
        for (const reg of [this.context.rcx, this.context.rdx, this.context.r8, this.context.r9]) {
            if (!reg || reg.isNull()) continue;
            const ec = readErrorCode(reg);
            if (ec === 0 || ec === 4) {
                console.log(`[C:HANDLER  #${hitCount}] ErrorCode = ${errLabel(ec)}  reg=${reg}`);
                break;
            }
        }
    }
});

console.log(`\n[*] 3 checkpoints active: A(dispatch) → B(lookup) → C(handler)`);
console.log(`[*] Enter armory to trigger GetPlayerArchiveV2`);
console.log(`[*] Look for where ErrorCode changes from 0 to 4\n`);
