// =============================================================================
// Session 3: Hook GetPlayerArchiveV2 handler (RVA 0x9C48B0)
//
// First hit: full detail (args, stack, all internal calls)
// Subsequent hits: silent (count only)
// Commands: detail() to re-enable, summary() to print stats
//
// Usage: frida -p <PID> -l tools/session3_handler_probe.js
// =============================================================================

const HANDLER_RVA = 0x9C48B0;

// --- Module ---
const BASE = (() => {
    for (const n of ["ProjectBoundarySteam-Win64-Shipping.exe","ProjectBoundary-Win64-Shipping.exe"])
        { try { return Process.getModuleByName(n).base; } catch(_){} }
    for (const m of Process.enumerateModules())
        { if (m.name.endsWith('.exe') && m.name.toLowerCase().includes('boundary')) return m.base; }
    throw new Error("Module not found");
})();

const handlerFunc = BASE.add(HANDLER_RVA);
console.log(`[+] Handler: ${handlerFunc}  (RVA ${hex(HANDLER_RVA)})`);

function hex(n) {
    if (n instanceof NativePointer) return n.toString();
    return "0x" + n.toString(16).toUpperCase();
}

// =============================================================================
// Output control
// =============================================================================

let detailMode = true;   // true = print everything for next hit, then auto-off
let hitCount = 0;
let callSummary = {};    // off → { target, sym, count }
let callOrder = [];      // order of first appearance

// =============================================================================
// Phase 1: Entry hook
// =============================================================================

Interceptor.attach(handlerFunc, {
    onEnter(args) {
        hitCount++;
        const ctx = this.context;
        this.isDetailed = detailMode;

        if (detailMode) {
            console.log(`\n=== HANDLER HIT #${hitCount} (DETAIL MODE) ===`);
            console.log(`  rcx=${ctx.rcx}  rdx=${ctx.rdx}  r8=${ctx.r8}  r9=${ctx.r9}`);
            console.log(`  ret=${this.returnAddress}`);

            // Try string args
            for (const [label, reg] of [['rcx', ctx.rcx], ['rdx', ctx.rdx]]) {
                try {
                    // Direct C string
                    const s = reg.readCString();
                    if (s && s.length > 0 && s.length < 256 && /^[\x20-\x7E]+$/.test(s))
                        console.log(`  ${label} string: "${s}"`);
                } catch (_) {}
                try {
                    // Pointer to struct with string at +0
                    const p = reg.readPointer();
                    const s = p.readCString();
                    if (s && s.length > 0 && s.length < 256 && /^[\x20-\x7E]+$/.test(s))
                        console.log(`  ${label}->[0] string: "${s}"`);
                } catch (_) {}
                // Also try at +0x10 (common string offset)
                for (const off of [0x10, 0x18, 0x20]) {
                    try {
                        const p = reg.add(off).readPointer();
                        const s = p.readCString();
                        if (s && s.length > 0 && s.length < 200 && /^[\x20-\x7E]+$/.test(s))
                            console.log(`  ${label}+${hex(off)} string: "${s}"`);
                    } catch (_) {}
                }
            }

            // Hex dump first 64 bytes of rcx
            try {
                const bytes = ctx.rcx.readByteArray(64);
                console.log(`  rcx[0:64]: ${hexdump(bytes, { offset: 0, length: 64, header: false, ansi: true })}`);
            } catch (_) {}

            // Stack
            console.log(`  Stack:`);
            const bt = Thread.backtrace(ctx, Backtracer.ACCURATE);
            for (let i = 0; i < Math.min(6, bt.length); i++) {
                const sym = DebugSymbol.fromAddress(bt[i]);
                console.log(`    #${i}: ${bt[i]}  ${sym}`);
            }

            detailMode = false;
            console.log(`  [*] Detail mode OFF — use detail() to re-enable for next hit`);
        } else {
            // Silent counting
        }
    },
    onLeave(retval) {
        if (this.isDetailed) {
            console.log(`  ← returns: ${retval}`);
        }
    }
});

// =============================================================================
// Phase 2: Internal call hooks (accumulate into summary)
// =============================================================================

console.log(`[*] Scanning handler for internal calls...`);

let addr = handlerFunc;
let scanCount = 0, hookedCount = 0;

while (addr.compare(handlerFunc.add(0x2000)) < 0) {
    try {
        const insn = Instruction.parse(addr);
        if (insn.mnemonic === 'call') {
            scanCount++;
            const off = addr.sub(handlerFunc).toInt32();
            let isIndirect = false, ripDisp = null, target = null;
            let targetStr = '?';

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

            const cOff = off, cIndirect = isIndirect;
            const cTarget = target, cRipDisp = ripDisp;
            const cInsnSize = insn.size, cAddr = addr;

            try {
                Interceptor.attach(addr, {
                    onEnter(_args) {
                        if (hitCount === 0) return;

                        let actualTarget = cTarget;
                        if (cIndirect) {
                            if (cRipDisp !== null && cRipDisp !== undefined) {
                                try { actualTarget = cAddr.add(cInsnSize).add(cRipDisp).readPointer(); } catch (_) {}
                            }
                            if (!actualTarget) {
                                try { actualTarget = this.context.rax; } catch (_) {}
                            }
                        }

                        let symStr = '?';
                        if (actualTarget && actualTarget instanceof NativePointer) {
                            try { const s = DebugSymbol.fromAddress(actualTarget); if (s) symStr = s.toString(); } catch (_) {}
                        }

                        const key = `${cOff}`;
                        if (!callSummary[key]) {
                            callSummary[key] = {
                                off: cOff,
                                target: actualTarget ? actualTarget.toString() : targetStr,
                                sym: symStr,
                                indirect: cIndirect,
                                count: 0,
                            };
                            callOrder.push(key);
                        }
                        callSummary[key].count++;

                        if (this.isDetailed) {
                            console.log(`    [+${hex(cOff)}] → ${callSummary[key].target}  [${symStr}]${cIndirect ? ' ★' : ''}`);
                        }
                    }
                });
                hookedCount++;
            } catch (_) {}
        }
        addr = insn.next;
    } catch (_) { break; }
}

console.log(`[+] Scan: ${scanCount} calls, hooked ${hookedCount}`);

// =============================================================================
// REPL commands
// =============================================================================

function detail() {
    detailMode = true;
    console.log(`[+] Detail mode ON for next handler hit (hitCount=${hitCount})`);
}

function summary() {
    console.log(`\n=== Handler Summary ===`);
    console.log(`Total hits: ${hitCount}`);
    console.log(`Internal calls observed: ${callOrder.length}`);
    for (const key of callOrder) {
        const cs = callSummary[key];
        const marker = cs.indirect ? ' ★' : '';
        console.log(`  +${hex(cs.off)} → ${cs.target}  [${cs.sym}]  x${cs.count}${marker}`);
    }
    console.log(`\nUse detail() to re-enable verbose mode for next hit.`);
}

function reset() {
    hitCount = 0;
    callSummary = {};
    callOrder = [];
    detailMode = true;
    console.log(`[+] Reset. Detail mode ON.`);
}

console.log(`\n=== Ready ===`);
console.log(`[*] Enter armory to trigger GetPlayerArchiveV2 handler`);
console.log(`[*] First hit: full detail. Then: silent counting.`);
console.log(`[*] Commands: detail(), summary(), reset()\n`);
