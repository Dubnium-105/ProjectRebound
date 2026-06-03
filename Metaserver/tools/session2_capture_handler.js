// =============================================================================
// Session 2: Capture GetPlayerArchiveV2 handler callback
//
// Goals:
//   1. Hook sub_1409C4780, filter by GetPlayerArchiveV2 MessageId
//   2. Step INTO the function to find the callback dispatch (call reg / call [reg+off])
//   3. Record: handler address, call stack, register state
//
// Prerequisite: Run Session 1 first to find the GetPlayerArchiveV2 MessageId,
//               then set TARGET_MSG_ID below.
//
// Usage: frida -p <PID> -l session2_capture_handler.js
// =============================================================================

let MODULE_NAME = "ProjectBoundarySteam-Win64-Shipping.exe";
const FUNC_RVA    = 0x9C4780;  // sub_1409C4780

// Auto-detect: try known names, fall back to substring match
if (!Module.findBaseAddress(MODULE_NAME)) {
    const mods = Process.enumerateModules();
    for (const m of mods) {
        if (m.name.endsWith('.exe') && (m.name.toLowerCase().includes('boundary') || m.name.toLowerCase().includes('shipping'))) {
            MODULE_NAME = m.name; break;
        }
    }
}

// ---- SET THIS AFTER SESSION 1 ----
const TARGET_MSG_ID = -1;  // ← Replace with the MessageId for /assets.Assets/GetPlayerArchiveV2
// ===================================

const base = Module.findBaseAddress(MODULE_NAME);
if (!base) {
    console.error(`[!] Module not found: ${MODULE_NAME}`);
    throw new Error("Module not found");
}

const targetFunc = base.add(FUNC_RVA);
console.log(`[+] Module: ${MODULE_NAME} @ ${base}`);

if (TARGET_MSG_ID < 0) {
    console.log("[!] TARGET_MSG_ID not set. Run Session 1 first, find the GetPlayerArchiveV2");
    console.log("[!] MessageId from dump(), then edit this script's TARGET_MSG_ID constant.");
    console.log("[!] For now, logging ALL calls to help identify the right ID...\n");
}

// =============================================================================
// Phase A: Function-level hook — identify which calls are for our target
// =============================================================================

const handlers = [];  // { msgId, handlerAddr, retAddr, stack }

Interceptor.attach(targetFunc, {
    onEnter(args) {
        const ctx = this.context;

        // Determine MessageId (using the heuristic from Session 1)
        let msgId = ctx.rdx.toInt32();
        if (msgId < 1 || msgId > 10000) {
            try { msgId = ctx.rcx.add(0).readS32(); } catch (_) { msgId = -1; }
        }
        if (msgId < 1 || msgId > 10000) {
            try { msgId = ctx.rcx.add(8).readS32(); } catch (_) { msgId = -1; }
        }

        this.msgId = msgId;
        this.shouldTrace = (TARGET_MSG_ID > 0 && msgId === TARGET_MSG_ID);

        if (TARGET_MSG_ID < 0) {
            // Discovery mode: log everything
            console.log(`[DISPATCH] msgId=${msgId}  ret=${this.returnAddress}`);
        } else if (this.shouldTrace) {
            console.log(`\n[!!!] TARGET HIT! msgId=${msgId}  ret=${this.returnAddress}`);
            console.log(`  rcx=${ctx.rcx} rdx=${ctx.rdx} r8=${ctx.r8} r9=${ctx.r9}`);
        }
    },

    onLeave(retval) {
        if (!this.shouldTrace) return;

        console.log(`  sub_9C4780 returned: ${retval}`);
        console.log(`  Thread backtrace:`);
        const bt = Thread.backtrace(this.context, Backtracer.ACCURATE);
        for (const addr of bt) {
            const sym = DebugSymbol.fromAddress(addr);
            // Only show frames within our module
            if (sym.moduleName && sym.moduleName.includes("ProjectBoundary")) {
                console.log(`    ${addr}  ${sym}`);
            }
        }
    }
});

// =============================================================================
// Phase B: Instruction-level tracing inside sub_9C4780
// We intercept at specific offsets inside the function to catch the callback
// dispatch. These offsets are determined by IDA static analysis.
//
// In IDA, sub_1409C4780 likely:
//   1. Takes a lock (EnterCriticalSection or similar)
//   2. Looks up hash table by MessageId (call sub_14099E820)
//   3. Calls the handler through a function pointer (call [reg+offset])
//   4. Releases the lock
//
// We intercept AFTER the hash table lookup, at the call [reg+X] instruction.
// =============================================================================

// Offsets to probe — these are relative to sub_9C4780 entry.
// We scan the first 0x400 bytes for 'call reg' or 'call [reg+X]' instructions.
// This runs once when the script loads.

function scanCallInstructions() {
    console.log("[*] Scanning sub_9C4780 for call instructions...");
    const funcStart = targetFunc;
    const callSites = [];

    for (let offset = 0; offset < 0x400; offset++) {
        const addr = funcStart.add(offset);
        try {
            const bytes = addr.readByteArray(2);
            // call [reg+disp] typically starts with 0xFF
            if (bytes[0] === 0xFF) {
                const modrm = bytes[1];
                const reg = (modrm >> 3) & 0x7;
                if (reg === 2) { // call [reg+disp]
                    callSites.push({ addr, offset, bytes: Array.from(bytes) });
                }
            }
            // call reg (0xFF 0xD0-0xD7)
            if (bytes[0] === 0xFF && (bytes[1] & 0xF8) === 0xD0) {
                callSites.push({ addr, offset, bytes: Array.from(bytes), type: 'call_reg' });
            }
            // E8 xx xx xx xx (direct call)
            if (bytes[0] === 0xE8) {
                const disp = addr.add(1).readS32();
                const target = addr.add(5).add(disp);
                callSites.push({ addr, offset, bytes: Array.from(bytes), type: 'direct', target });
            }
        } catch (_) {
            break; // unreadable memory, end of function
        }
    }

    console.log(`[+] Found ${callSites.length} call instructions in sub_9C4780:`);
    for (const cs of callSites) {
        let desc = `  +${hex(cs.offset)}: `;
        if (cs.type === 'direct') {
            const sym = DebugSymbol.fromAddress(cs.target);
            desc += `CALL ${cs.target} ${sym}`;
        } else if (cs.type === 'call_reg') {
            desc += `CALL reg (indirect handler?)`;
        } else {
            desc += `CALL [reg+disp] (vtable/function pointer?)`;
        }
        console.log(desc);
    }

    return callSites;
}

const callSites = scanCallInstructions();

// Hook ALL indirect/direct calls within sub_9C4780 that look like handler dispatch
for (const cs of callSites) {
    if (cs.type === 'call_reg' || (cs.type === 'direct' && !cs.target.equals(targetFunc))) {
        Interceptor.attach(cs.addr, {
            onEnter(args) {
                // Only log when our target MessageId is being processed
                // (we can't filter easily here because msgId context is only in the outer hook)
                // Instead, record all indirect calls with their targets
                const ctx = this.context;
                // Find the actual call target from the instruction
                const rcx = ctx.rcx, rdx = ctx.rdx, r8 = ctx.r8;
                console.log(`  [CALL @ +${hex(cs.offset)}] rcx=${rcx} rdx=${rdx} r8=${r8}`);
                // Log return address (who handles the callback)
                this.retAddr = this.returnAddress;
            },
            onLeave(retval) {
                // Called after callback returns
            }
        });
    }
}

console.log(`\n[*] Interceptors active. ${callSites.length} call sites probed.`);
console.log(`[*] Trigger GetPlayerArchiveV2 by entering the armory.`);
console.log(`[*] Press Ctrl+C to stop.\n`);
