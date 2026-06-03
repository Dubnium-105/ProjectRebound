// =============================================================================
// Session 5: Pinpoint the exact call inside sub_99E820 that sets ErrorCode=4
//
// Hooks every CALL inside sub_99E820, checks ErrorCode BEFORE and AFTER.
// The call where ErrorCode changes 0→4 is our validation function.
//
// Usage: frida -p <PID> -l tools/session5_pinpoint.js
// =============================================================================

const LOOKUP_RVA = 0x99E820;

const BASE = (() => {
    for (const n of ["ProjectBoundarySteam-Win64-Shipping.exe","ProjectBoundary-Win64-Shipping.exe"])
        { try { return Process.getModuleByName(n).base; } catch(_){} }
    for (const m of Process.enumerateModules())
        { if (m.name.endsWith('.exe') && m.name.toLowerCase().includes('boundary')) return m.base; }
    throw new Error("Module not found");
})();

function hex(n) { return n instanceof NativePointer ? n.toString() : "0x" + n.toString(16); }

const lookupFunc = BASE.add(LOOKUP_RVA);
console.log(`[+] Lookup func: ${lookupFunc}`);

// =============================================================================
// Track ErrorCode at a known struct address across sub_99E820's execution
// =============================================================================

let trackedStruct = null;  // NativePointer to the struct containing ErrorCode
let errorCodeBefore = -1;
let insideLookup = false;
let callIndex = 0;
let hitCount = 0;
const MAX_HITS = 3;

// =============================================================================
// Entry hook — capture the struct address when ErrorCode is still 0
// =============================================================================

Interceptor.attach(lookupFunc, {
    onEnter(args) {
        // Find the struct with ErrorCode=0 from the args
        for (const reg of [this.context.rcx, this.context.rdx, this.context.r8, this.context.r9]) {
            if (!reg || reg.isNull()) continue;
            try {
                const ec = reg.add(0x0C).readS32();
                if (ec === 0) {
                    trackedStruct = reg;
                    errorCodeBefore = 0;
                    insideLookup = true;
                    callIndex = 0;
                    hitCount++;
                    if (hitCount <= MAX_HITS) {
                        console.log(`\n[HIT #${hitCount}] sub_99E820 ENTRY: struct=${trackedStruct} ErrorCode=OK(0)`);
                    }
                    break;
                }
            } catch (_) {}
        }
    },
    onLeave(retval) {
        if (!insideLookup || !trackedStruct) return;
        insideLookup = false;

        try {
            const ecAfter = trackedStruct.add(0x0C).readS32();
            if (hitCount <= MAX_HITS) {
                const label = ecAfter === 4 ? 'FAIL(4) *** CHANGED! ***' :
                              ecAfter === 0 ? 'OK(0) (no change)' :
                              `?(${ecAfter})`;
                console.log(`  sub_99E820 EXIT: ErrorCode=${label}`);
                console.log(`  Total internal calls: ${callIndex}`);
            }
        } catch (_) {}
        trackedStruct = null;
    }
});

// =============================================================================
// Scan and hook every CALL inside sub_99E820
// =============================================================================

console.log(`[*] Scanning sub_99E820 for call instructions...`);

let addr = lookupFunc;
let hookedCount = 0;

while (addr.compare(lookupFunc.add(0x1000)) < 0) {
    try {
        const insn = Instruction.parse(addr);
        if (insn.mnemonic === 'call') {
            const off = addr.sub(lookupFunc).toInt32();
            let isIndirect = false, ripDisp = null, target = null, targetStr = '?';

            try {
                const ops = insn.operands;
                if (ops.length > 0 && ops[0].type === 'imm') {
                    target = ptr(ops[0].value);
                    targetStr = target.toString();
                } else if (ops.length > 0 && ops[0].type === 'reg') {
                    isIndirect = true; targetStr = `reg:${ops[0].value}`;
                } else if (ops.length > 0 && ops[0].type === 'mem') {
                    isIndirect = true;
                    const mv = ops[0].value;
                    if (mv.base === 'rip') { ripDisp = mv.disp; targetStr = `[rip+${mv.disp}]`; }
                    else targetStr = `mem:${JSON.stringify(mv)}`;
                } else { isIndirect = true; }
            } catch (_) {}

            const cOff = off, cIsIndirect = isIndirect;
            const cTarget = target, cRipDisp = ripDisp;
            const cInsnSize = insn.size, cAddr = addr;

            try {
                Interceptor.attach(addr, {
                    onEnter(_args) {
                        if (!insideLookup || !trackedStruct) return;
                        callIndex++;

                        let ecBefore = -1;
                        try { ecBefore = trackedStruct.add(0x0C).readS32(); } catch (_) {}

                        // Resolve target for logging
                        let actualTarget = cTarget;
                        if (cIsIndirect) {
                            if (cRipDisp !== null && cRipDisp !== undefined) {
                                try { actualTarget = cAddr.add(cInsnSize).add(cRipDisp).readPointer(); } catch (_) {}
                            }
                            if (!actualTarget) {
                                try { actualTarget = this.context.rax; } catch (_) {}
                            }
                        }
                        const rva = (actualTarget && actualTarget instanceof NativePointer) ?
                            actualTarget.sub(BASE).toInt32() : -1;

                        this._off = cOff;
                        this._target = actualTarget;
                        this._rva = rva;
                        this._ecBefore = ecBefore;
                        this._callNum = callIndex;
                    },
                    onLeave(_retval) {
                        if (!insideLookup || !trackedStruct) return;

                        let ecAfter = -1;
                        try { ecAfter = trackedStruct.add(0x0C).readS32(); } catch (_) {}

                        const changed = (this._ecBefore === 0 && ecAfter === 4);

                        if (changed || hitCount <= MAX_HITS) {
                            let symStr = '?';
                            const t = this._target;
                            if (t && t instanceof NativePointer) {
                                try { const s = DebugSymbol.fromAddress(t); if (s) symStr = s.toString(); } catch (_) {}
                            }
                            const rvaStr = this._rva > 0 ? hex(this._rva) : '?';
                            const tgtStr = t ? t.toString() : '?';
                            const marker = changed ? ' ←←← ERRORCODE 0→4 HERE!' : '';
                            console.log(`  [#${this._callNum}] +${hex(this._off)} EC:${this._ecBefore}→${ecAfter} → ${tgtStr}  RVA=${rvaStr}  [${symStr}]${marker}`);
                        }
                    }
                });
                hookedCount++;
            } catch (_) {}
        }
        addr = insn.next;
    } catch (_) { break; }
}

console.log(`[+] Hooked ${hookedCount} call sites inside sub_99E820`);
console.log(`[*] Enter armory. Watch for 'ERRORCODE 0→4 HERE!' marker.\n`);
