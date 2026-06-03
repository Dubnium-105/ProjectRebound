// =============================================================================
// Session 3: Stalker instruction-level trace of GetPlayerArchiveV2 handler
//
// Goals:
//   Once we know the handler callback address (from Session 2), trace EVERY
//   instruction executed inside it when processing a GetPlayerArchiveV2 response.
//   This captures:
//     - All function calls (subroutines called by the handler)
//     - All conditional branch directions (which path was taken for our valid data)
//     - Register state at each call site (for understanding arguments)
//
// Usage: frida -p <PID> -l session3_stalk_trace.js
//        Then call trace() from Frida console, or it auto-triggers on handler hit.
//
// Prerequisite: Session 2 must have identified the handler callback address.
//               Set HANDLER_RVA below.
// =============================================================================

let MODULE_NAME = "ProjectBoundarySteam-Win64-Shipping.exe";

// Auto-detect
if (!Module.findBaseAddress(MODULE_NAME)) {
    const mods = Process.enumerateModules();
    for (const m of mods) {
        if (m.name.endsWith('.exe') && (m.name.toLowerCase().includes('boundary') || m.name.toLowerCase().includes('shipping'))) {
            MODULE_NAME = m.name; break;
        }
    }
}

// ---- SET THIS AFTER SESSION 2 ----
const HANDLER_RVA = 0x0;  // ← Replace with the handler function RVA from Session 2
// ===================================

const base = Module.findBaseAddress(MODULE_NAME);
if (!base) {
    console.error(`[!] Module not found: ${MODULE_NAME}`);
    throw new Error("Module not found");
}

console.log(`[+] Module: ${MODULE_NAME} @ ${base}`);

if (HANDLER_RVA === 0) {
    console.log("[!] HANDLER_RVA not set.");
    console.log("[!] Run Session 2 first, find the handler callback address,");
    console.log("[!] then compute RVA = handlerAddr - base, and set HANDLER_RVA.");
    console.log("[!] For now, the script is loaded but inactive.\n");
}

const handlerAddr = HANDLER_RVA > 0 ? base.add(HANDLER_RVA) : null;

// =============================================================================
// Stalker config
// =============================================================================

const TRACE_DURATION_MS = 10000;  // trace for 10 seconds after handler hit
const MAX_CALLS = 500;            // stop after 500 traced calls

let traceActive = false;
let callLog = [];
let branchLog = [];
let callCount = 0;
let startTime = 0;

// ---- Stalker transform callback ----
function stalkTransform(iterator) {
    let instruction;
    while ((instruction = iterator.next()) !== null) {
        const addr = instruction.address;

        // Only transform instructions in our handler's code range
        // (functions can span up to ~0x2000 bytes, but we're conservative)
        if (!handlerAddr || addr.compare(handlerAddr) < 0 ||
            addr.compare(handlerAddr.add(0x4000)) > 0) {
            iterator.keep();
            continue;
        }

        // --- Intercept CALL instructions ---
        if (instruction.mnemonic === 'call') {
            const opStr = instruction.operands[0];
            iterator.putCallout((ctx) => {
                if (!traceActive || callCount >= MAX_CALLS) return;

                // Resolve call target
                let target = null;
                try {
                    // For direct calls, we can compute the target
                    // For indirect calls, we need register value
                    if (opStr.startsWith('0x')) {
                        target = ptr(opStr);
                    } else if (opStr === 'rax') target = ctx.rax;
                    else if (opStr === 'rcx') target = ctx.rcx;
                    else if (opStr === 'rdx') target = ctx.rdx;
                    else if (opStr === 'r8')  target = ctx.r8;
                    else if (opStr === 'r9')  target = ctx.r9;
                    else if (opStr.startsWith('[')) {
                        // Memory indirect — approximate
                        target = ptr(0); // placeholder
                    }
                } catch (_) {}

                const targetSym = target ? DebugSymbol.fromAddress(target) : null;
                const entry = {
                    n: callCount++,
                    from: addr,
                    to: target,
                    toSym: targetSym ? targetSym.toString() : null,
                    rcx: ctx.rcx, rdx: ctx.rdx, r8: ctx.r8,
                    retAddr: null, // filled in onLeave-style can't work in Stalker callout
                };
                callLog.push(entry);
            });
        }

        // --- Intercept conditional branches ---
        if (instruction.mnemonic.startsWith('j') &&
            instruction.mnemonic !== 'jmp' &&
            instruction.mnemonic !== 'jrcxz' &&
            !instruction.mnemonic.startsWith('jecxz')) {

            const jmpTarget = instruction.operands[0];
            iterator.putCallout((ctx) => {
                if (!traceActive) return;
                if (branchLog.length >= MAX_CALLS * 2) return;

                // Determine if branch was taken by comparing next instruction
                // This is approximate — actual branch direction resolution
                // would require post-processing with instruction size
                branchLog.push({
                    addr,
                    mnemonic: instruction.mnemonic,
                    target: jmpTarget,
                    // Fallback: we can't easily detect taken/not-taken in Stalker
                    // but we record the branch instruction itself for IDA analysis
                });
            });
        }

        iterator.keep();
    }
}

// =============================================================================
// Trigger mechanism
// =============================================================================

let stalkerId = null;
let stopTimer = null;

function startTrace() {
    if (traceActive) return;
    if (!handlerAddr) {
        console.log("[!] HANDLER_RVA not set, cannot trace");
        return;
    }

    traceActive = true;
    callLog = [];
    branchLog = [];
    callCount = 0;
    startTime = Date.now();

    stalkerId = Stalker.follow(Process.getCurrentThreadId(), {
        transform: stalkTransform,
    });

    console.log(`[+] Stalker started on thread ${Process.getCurrentThreadId()}`);
    console.log(`[+] Tracing handler at ${handlerAddr}`);

    // Auto-stop
    stopTimer = setTimeout(stopTrace, TRACE_DURATION_MS);
}

function stopTrace() {
    if (!traceActive) return;

    if (stalkerId) Stalker.unfollow(stalkerId);
    if (stopTimer) clearTimeout(stopTimer);

    traceActive = false;
    const elapsed = Date.now() - startTime;

    console.log(`\n=== Trace Complete ===`);
    console.log(`  Duration: ${elapsed}ms`);
    console.log(`  Calls traced: ${callLog.length}`);
    console.log(`  Branches recorded: ${branchLog.length}`);

    // Summary: deduplicate called functions
    const uniqueTargets = new Map();
    for (const c of callLog) {
        if (!c.to || c.to.isNull()) continue;
        const key = c.to.toString();
        if (!uniqueTargets.has(key)) {
            uniqueTargets.set(key, { addr: c.to, sym: c.toSym, count: 0, firstAt: c.n });
        }
        uniqueTargets.get(key).count++;
    }

    console.log(`\n  Unique functions called (${uniqueTargets.size}):`);
    // Sort by first occurrence
    const sorted = Array.from(uniqueTargets.values()).sort((a, b) => a.firstAt - b.firstAt);
    for (const u of sorted.slice(0, 30)) {
        const symName = u.sym ? u.sym.split(' ').pop() : '?';
        console.log(`    #${u.firstAt}: ${u.addr}  [${symName}]  x${u.count}`);
    }

    console.log(`\n[*] Use analyze() to print full call log`);
    console.log(`[*] Copy the function addresses above to IDA for validation analysis`);
}

// Manual trigger — call this from Frida console after entering armory
global.trace = function() {
    console.log("[*] Manual trace trigger");
    startTrace();
};

// Hook handler entry for auto-trigger
if (handlerAddr) {
    Interceptor.attach(handlerAddr, {
        onEnter(args) {
            console.log(`\n[!!!] Handler ${handlerAddr} called!`);
            console.log(`  rcx=${this.context.rcx} rdx=${this.context.rdx} r8=${this.context.r8}`);
            console.log(`  Stack: ${Thread.backtrace(this.context, Backtracer.ACCURATE).slice(0, 5).map(DebugSymbol.fromAddress).join(' | ')}`);

            if (!traceActive) {
                console.log("[*] Auto-starting Stalker trace...");
                startTrace();
            }
        }
    });
    console.log(`[+] Auto-trigger hook set on handler ${handlerAddr}`);
}

// ---- REPL commands ----

global.analyze = function() {
    console.log(`\n=== Full Call Log (${callLog.length} entries) ===`);
    for (const c of callLog) {
        const sym = c.toSym || '?';
        console.log(`  [${c.n}] ${c.from} CALL ${c.to}  ${sym}`);
        console.log(`       rcx=${c.rcx} rdx=${c.rdx} r8=${c.r8}`);
    }
};

global.branches = function() {
    console.log(`\n=== Branch Log (${branchLog.length} entries) ===`);
    for (const b of branchLog) {
        console.log(`  ${b.addr}  ${b.mnemonic} → ${b.target}`);
    }
};

global.status = function() {
    console.log(`Trace active: ${traceActive}`);
    console.log(`Calls so far: ${callLog.length}`);
    console.log(`Branches so far: ${branchLog.length}`);
    if (handlerAddr) console.log(`Handler: ${handlerAddr}`);
};

console.log("[*] Commands: trace(), analyze(), branches(), status()");
console.log("[*] Auto-trigger on handler entry is " + (handlerAddr ? "ENABLED" : "DISABLED (set HANDLER_RVA)"));
