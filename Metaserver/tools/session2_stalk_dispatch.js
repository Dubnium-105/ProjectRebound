// =============================================================================
// Session 2: Stalker-trace sub_9C4780 for GetPlayerArchiveV2 / UpdateRoleArchiveV2
//
// Captures every call instruction inside sub_9C4780 when msgId=2 or msgId=49.
// Usage: frida -p <PID> -l tools/session2_stalk_dispatch.js
//        Trigger the RPC in-game. Results auto-print.
// =============================================================================

const DISPATCH_RVA = 0x9C4780;
const TARGETS = new Set([2, 49]); // GetPlayerArchiveV2=2, UpdateRoleArchiveV2=49

// --- Module detection ---
const BASE = (() => {
    const names = ["ProjectBoundarySteam-Win64-Shipping.exe","ProjectBoundary-Win64-Shipping.exe"];
    for (const n of names) { try { return Process.getModuleByName(n).base; } catch(_){} }
    for (const m of Process.enumerateModules()) {
        if (m.name.endsWith('.exe') && m.name.toLowerCase().includes('boundary')) return m.base;
    }
    throw new Error("Module not found");
})();

const dispatchFunc = BASE.add(DISPATCH_RVA);
const funcEnd = dispatchFunc.add(0x600);
console.log(`[+] Dispatch: ${dispatchFunc} ~ ${funcEnd}`);
console.log(`[+] Targets: msgId=2 (GetPlayerArchiveV2), msgId=49 (UpdateRoleArchiveV2)`);

function hex(n) {
    if (n instanceof NativePointer) return n.toString();
    return "0x" + n.toString(16).toUpperCase();
}

// =============================================================================
// Global state: currentMsgId is set by onEnter, read by Stalker callouts
// =============================================================================

let currentMsgId = -1;
let currentHitNum = 0;
const capturedTraces = {}; // msgId → [{from, to, sym}]

// =============================================================================
// Stalker — always active, filters by currentMsgId
// =============================================================================

Stalker.follow(Process.getCurrentThreadId(), {
    transform(iterator) {
        let instruction;
        while ((instruction = iterator.next()) !== null) {
            const addr = instruction.address;

            if (addr.compare(dispatchFunc) >= 0 && addr.compare(funcEnd) < 0) {
                if (instruction.mnemonic === 'call') {
                    iterator.putCallout((ctx) => {
                        if (currentMsgId < 0 || !TARGETS.has(currentMsgId)) return;

                        let target = null;
                        const op = instruction.operands[0];
                        try {
                            if (op === 'rax') target = ctx.rax;
                            else if (op === 'rcx') target = ctx.rcx;
                            else if (op === 'rdx') target = ctx.rdx;
                            else if (op === 'r8')  target = ctx.r8;
                            else if (op === 'r9')  target = ctx.r9;
                            else if (op === 'rbx') target = ctx.rbx;
                            else if (op === 'rsi') target = ctx.rsi;
                            else if (op === 'rdi') target = ctx.rdi;
                            else if (op === 'rbp') target = ctx.rbp;
                            else if (op.startsWith('0x')) target = ptr(op);
                        } catch (_) {}

                        const sym = target ? DebugSymbol.fromAddress(target) : null;
                        if (!capturedTraces[currentMsgId]) capturedTraces[currentMsgId] = [];
                        if (capturedTraces[currentMsgId].length < 500) {
                            capturedTraces[currentMsgId].push({
                                from: addr.sub(dispatchFunc).toInt32(),
                                to: target ? target.toString() : '?',
                                sym: sym ? sym.toString() : null,
                            });
                        }
                    });
                }
            }
            iterator.keep();
        }
    }
});

// =============================================================================
// Dispatch hook — sets currentMsgId during execution
// =============================================================================

Interceptor.attach(dispatchFunc, {
    onEnter(args) {
        const msgId = args[2].toInt32(); // r8 = MessageId
        if (TARGETS.has(msgId)) {
            currentMsgId = msgId;
            currentHitNum++;
            console.log(`\n[HIT #${currentHitNum}] msgId=${msgId} — tracing...`);
        }
    },
    onLeave(retval) {
        if (currentMsgId > 0 && TARGETS.has(currentMsgId)) {
            const msgId = currentMsgId;
            currentMsgId = -1;
            printTrace(msgId, capturedTraces[msgId] || []);
            delete capturedTraces[msgId]; // clear for next hit
        }
    }
});

// =============================================================================
// Print results
// =============================================================================

function printTrace(msgId, calls) {
    const rpcName = msgId === 2 ? 'GetPlayerArchiveV2' : msgId === 49 ? 'UpdateRoleArchiveV2' : '?';

    console.log(`\n=== ${rpcName} (msgId=${msgId}): ${calls.length} calls ===`);

    // Deduplicate
    const seen = new Set();
    const uniq = [];
    for (const c of calls) {
        const key = `${c.from}/${c.to}`;
        if (seen.has(key)) continue;
        seen.add(key);
        uniq.push(c);
    }

    console.log(`Unique: ${uniq.length}`);
    for (const c of uniq) {
        const symName = c.sym || '?';
        console.log(`  +${hex(c.from)}  →  ${c.to}  [${symName}]`);
    }

    // Highlight potential handler callbacks
    // These are typically indirect calls (call rax/rcx/etc) near the end of the function
    const indirectCalls = uniq.filter(c => c.sym && (
        c.sym.includes('0x7ff') && !c.sym.toLowerCase().includes('guard') &&
        !c.sym.toLowerCase().includes('lock') && !c.sym.toLowerCase().includes('critical')
    ));
    if (indirectCalls.length > 0) {
        console.log(`\n--- Likely handler/sink calls ---`);
        for (const c of indirectCalls) {
            console.log(`  +${hex(c.from)} → ${c.to}  [${c.sym}]`);
        }
    }

    // For IDA: print offset and target address
    console.log(`\n--- IDA cross-reference ---`);
    console.log(`  Offset list: [${uniq.map(c => hex(c.from)).join(', ')}]`);
    console.log(`  First call: +${hex(uniq[0]?.from || 0)}`);
    console.log(`  Last call:  +${hex(uniq[uniq.length-1]?.from || 0)}`);
}

console.log(`\n[*] Ready. Trigger GetPlayerArchiveV2 (enter armory) or UpdateRoleArchiveV2 (equip item).`);
console.log(`[*] Trace prints automatically on each hit.\n`);
