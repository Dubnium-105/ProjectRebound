// =============================================================================
// Session 2: Find handler callback by hooking call sites inside sub_9C4780
//
// Scans the dispatch function for all CALL instructions, hooks them.
// When msgId=2 (GetPlayerArchiveV2) or msgId=49 (UpdateRoleArchiveV2) passes
// through, prints every call target reached inside the function.
//
// Usage: frida -p <PID> -l tools/session2_scan_calls.js
// =============================================================================

const DISPATCH_RVA = 0x9C4780;
const TARGETS = new Set([2, 49]);

// --- Module ---
const BASE = (() => {
    for (const n of ["ProjectBoundarySteam-Win64-Shipping.exe","ProjectBoundary-Win64-Shipping.exe"])
        { try { return Process.getModuleByName(n).base; } catch(_){} }
    for (const m of Process.enumerateModules())
        { if (m.name.endsWith('.exe') && m.name.toLowerCase().includes('boundary')) return m.base; }
    throw new Error("Module not found");
})();

const dispatchFunc = BASE.add(DISPATCH_RVA);
console.log(`[+] Dispatch: ${dispatchFunc}`);

function hex(n) {
    if (n instanceof NativePointer) return n.toString();
    return "0x" + n.toString(16).toUpperCase();
}

// =============================================================================
// Scan the function for all call instructions
// =============================================================================

const callHooks = [];

function scanAndHook() {
    console.log("[*] Scanning sub_9C4780 for call instructions...");
    let addr = dispatchFunc;
    let count = 0;

    while (addr.compare(dispatchFunc.add(0x600)) < 0) {
        try {
            const insn = Instruction.parse(addr);
            const mnem = insn.mnemonic;
            const off = addr.sub(dispatchFunc).toInt32();

            if (mnem === 'call') {
                count++;
                // Resolve target for direct calls
                let target = null, targetStr = '?', isIndirect = false;
                let ripOffset = null;
                try {
                    const ops = insn.operands;
                    if (ops.length > 0 && ops[0].type === 'imm') {
                        // Use ptr() to preserve full 64-bit address (JS Number loses precision)
                        target = ptr(ops[0].value);
                        targetStr = target.toString();
                    } else if (ops.length > 0 && ops[0].type === 'reg') {
                        isIndirect = true;
                        targetStr = `reg:${ops[0].value}`;
                    } else if (ops.length > 0 && ops[0].type === 'mem') {
                        isIndirect = true;
                        const mv = ops[0].value;
                        if (mv.base === 'rip' && mv.disp !== undefined) {
                            // RIP-relative: target pointer at addr + insn_size + disp
                            ripOffset = mv.disp;
                            targetStr = `[rip${mv.disp >= 0 ? '+' : ''}${mv.disp}]`;
                        } else {
                            targetStr = `mem:${JSON.stringify(mv)}`;
                        }
                    } else {
                        isIndirect = true;
                        targetStr = `?(${insn.toString()})`;
                    }
                } catch (_) {}

                const thisOff = off;
                const thisIsIndirect = isIndirect;
                const thisTarget = target;
                const thisRipDisp = ripOffset;
                const thisInsnSize = insn.size;
                const thisAddr = addr;

                // Hook this call instruction
                try {
                    Interceptor.attach(addr, {
                        onEnter(args) {
                            if (currentMsgId < 0 || !TARGETS.has(currentMsgId)) return;

                            let actualTarget = null;

                            if (thisIsIndirect) {
                                // Try RIP-relative first
                                if (thisRipDisp !== null && thisRipDisp !== undefined) {
                                    try {
                                        const ptrAddr = thisAddr.add(thisInsnSize).add(thisRipDisp);
                                        actualTarget = ptrAddr.readPointer();
                                    } catch (_) {}
                                }
                                // Fall back to register resolution
                                if (!actualTarget) {
                                    try {
                                        const ts = targetStr;
                                        if (ts.includes('rax')) actualTarget = this.context.rax;
                                        else if (ts.includes('rcx')) actualTarget = this.context.rcx;
                                        else if (ts.includes('rdx')) actualTarget = this.context.rdx;
                                        else if (ts.includes('r8'))  actualTarget = this.context.r8;
                                        else if (ts.includes('r9'))  actualTarget = this.context.r9;
                                        else if (ts.includes('rbx')) actualTarget = this.context.rbx;
                                        else if (ts.includes('rsi')) actualTarget = this.context.rsi;
                                        else if (ts.includes('rdi')) actualTarget = this.context.rdi;
                                    } catch (_) {}
                                }
                            } else {
                                actualTarget = thisTarget;
                            }

                            let symStr = '?';
                            if (actualTarget && actualTarget instanceof NativePointer) {
                                try { const s = DebugSymbol.fromAddress(actualTarget); if (s) symStr = s.toString(); } catch (_) {}
                            }

                            const entry = {
                                off: thisOff,
                                target: actualTarget ? actualTarget.toString() : targetStr,
                                sym: symStr,
                                indirect: thisIsIndirect,
                            };

                            if (!callsThisInvocation) callsThisInvocation = [];
                            callsThisInvocation.push(entry);
                        }
                    });
                    callHooks.push({ off, addr, isIndirect, targetStr });
                } catch (e) {
                    // Some addresses can't be hooked (middle of another instruction, etc.)
                }
            }

            addr = insn.next;
        } catch (_) {
            break; // unreadable memory, end of function
        }
    }

    console.log(`[+] Scanned ${count} call instructions, hooked ${callHooks.length}`);
    for (const h of callHooks) {
        const marker = h.isIndirect ? ' ← INDIRECT (likely handler)' : '';
        console.log(`    +${hex(h.off)}: ${h.targetStr}${marker}`);
    }
}

// =============================================================================
// Dispatch hook — context for call hooks
// =============================================================================

let currentMsgId = -1;
let callsThisInvocation = null;
let hitNum = 0;

Interceptor.attach(dispatchFunc, {
    onEnter(args) {
        const msgId = args[2].toInt32(); // r8
        if (TARGETS.has(msgId)) {
            currentMsgId = msgId;
            hitNum++;
        }
    },
    onLeave(retval) {
        if (currentMsgId < 0) return;
        const msgId = currentMsgId;
        currentMsgId = -1;

        const calls = callsThisInvocation || [];
        callsThisInvocation = null;

        const rpc = msgId === 2 ? 'GetPlayerArchiveV2' : msgId === 49 ? 'UpdateRoleArchiveV2' : '?';
        console.log(`\n=== ${rpc} (msgId=${msgId}): ${calls.length} calls ===`);

        for (const c of calls) {
            console.log(`  +${hex(c.off)} → ${c.target}  [${c.sym}]${c.indirect ? ' ★' : ''}`);
        }

        // Print the LAST indirect call — this is most likely the handler callback
        const indirectCalls = calls.filter(c => c.indirect);
        if (indirectCalls.length > 0) {
            const last = indirectCalls[indirectCalls.length - 1];
            console.log(`\n*** LAST INDIRECT CALL (likely handler): +${hex(last.off)} → ${last.target} [${last.sym}] ***`);
        }
    }
});

scanAndHook();
console.log(`\n[*] Ready. Trigger GetPlayerArchiveV2 (enter armory) or UpdateRoleArchiveV2 (equip).`);
