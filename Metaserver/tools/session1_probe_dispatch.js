// =============================================================================
// Session 1: Probe sub_1409C4780 (response dispatch by MessageId)
//
// Goals:
//   1. Confirm the function exists at the expected RVA in the running process
//   2. Infer the function signature (args, calling convention)
//   3. Capture every MessageId that passes through → map to RPC paths
//   4. Record return addresses (who calls sub_9C4780)
//
// Usage: frida -p <PID> -l session1_probe_dispatch.js
// Kill with Ctrl+C after entering armory and changing loadouts a few times.
//
// Prerequisite: proxy.js must be running so logs/msgid_map.json exists.
// =============================================================================

const MODULE_NAME = "ProjectBoundary-Win64-Shipping.exe";
const FUNC_RVA    = 0x9C4780;  // sub_1409C4780 in IDA, subtract image base 0x140000000

// Auto-detect game module
function findGameModule() {
    const mods = Process.enumerateModules();
    for (const name of ["ProjectBoundarySteam-Win64-Shipping.exe","ProjectBoundary-Win64-Shipping.exe","ProjectBoundarySteam.exe","ProjectBoundary.exe"]) {
        const m = Module.findBaseAddress(name); if (m) return { base: m, name };
    }
    for (const m of mods) {
        if (m.name.endsWith('.exe') && (m.name.toLowerCase().includes('boundary')||m.name.toLowerCase().includes('shipping')))
            return { base: m.base, name: m.name };
    }
    return null;
}
const gm = findGameModule();
if (!gm) throw new Error("Game module not found");
const MODULE_NAME = gm.name;
const base = gm.base;

console.log(`[+] Module: ${MODULE_NAME} @ ${base}`);
const targetFunc = base.add(FUNC_RVA);

// ---- Helpers ----
function hex(n) { return n instanceof NativePointer ? n.toString() : "0x" + n.toString(16); }
function safeReadPtr(a,o) { try { return a.add(o).readPointer(); } catch(_) { return null; } }
function safeReadU32(a,o) { try { return a.add(o).readU32(); } catch(_) { return null; } }
function safeReadS32(a,o) { try { return a.add(o).readS32(); } catch(_) { return null; } }

// Verify the function prologue looks plausible
try {
    const prologue = targetFunc.readByteArray(16);
    console.log(`[+] Prologue bytes: ${hexdump(prologue, { offset: 0, length: 16, header: false })}`);
} catch (e) {
    console.error(`[!] Cannot read at ${targetFunc}: ${e.message}`);
    throw e;
}

// ---- Trace state ----

const captured = [];         // { msgId, rpcPath?, stack: string[], retAddr }
const msgIdSet = new Set();
let   hitCount = 0;
const MAX_HITS = 50;         // auto-stop after 50 hits

// ---- Load proxy msgId→RPC map ----

function loadMsgIdMap() {
    try {
        // Adjust path to match your environment
        const data = require('fs').readFileSync('g:/wksp/boundaries/ProjectRebound/Metaserver/logs/msgid_map.json', 'utf8');
        return JSON.parse(data);
    } catch (_) {
        return {}; // proxy may not have written the file yet
    }
}

function lookupRpc(msgId) {
    const map = loadMsgIdMap();
    return map[String(msgId)] || null;
}

// ---- Interceptor ----

Interceptor.attach(targetFunc, {
    onEnter(args) {
        hitCount++;
        if (hitCount > MAX_HITS) return; // auto-stop

        // --- Probe each argument register to infer the signature ---
        // Windows x64 calling convention: rcx, rdx, r8, r9, then stack
        const ctx = this.context;

        // Heuristic: MessageId is an int32 in the ResponseWrapper.
        // If args[0] is a struct pointer, MessageId might be at offset 0.
        // If args[1] is the MessageId directly, it'll be a small integer (< 1000).

        const rcxVal = ctx.rcx;
        const rdxVal = ctx.rdx;
        const r8Val  = ctx.r8;
        const r9Val  = ctx.r9;

        // Try reading MessageId from different possible positions
        let msgId = null;
        let sourceHint = "";

        // Hypothesis 1: rdx is MessageId directly (small int)
        if (rdxVal.toInt32() > 0 && rdxVal.toInt32() < 10000) {
            msgId = rdxVal.toInt32();
            sourceHint = "rdx_direct";
        }
        // Hypothesis 2: rcx[+0] is MessageId in a struct
        if (msgId === null) {
            const v = safeReadS32(rcxVal, 0);
            if (v !== null && v > 0 && v < 10000) {
                msgId = v;
                sourceHint = "rcx[0]";
            }
        }
        // Hypothesis 3: rcx[+8] is MessageId
        if (msgId === null) {
            const v = safeReadS32(rcxVal, 8);
            if (v !== null && v > 0 && v < 10000) {
                msgId = v;
                sourceHint = "rcx[8]";
            }
        }
        // Hypothesis 4: r8 is MessageId directly
        if (msgId === null && r8Val.toInt32() > 0 && r8Val.toInt32() < 10000) {
            msgId = r8Val.toInt32();
            sourceHint = "r8_direct";
        }

        if (msgId === null) {
            console.log(`[${hitCount}] WARNING: Could not determine MessageId`);
            console.log(`    rcx=${rcxVal} rdx=${rdxVal} r8=${r8Val} r9=${r9Val}`);
            console.log(`    Try reading rcx as pointer: +0=${safeReadU32(rcxVal,0)} +4=${safeReadU32(rcxVal,4)} +8=${safeReadU32(rcxVal,8)}`);
            msgId = -1;
            sourceHint = "unknown";
        }

        // Deduplicate by MessageId (only log first occurrence per ID)
        const isNew = !msgIdSet.has(msgId);
        if (isNew && msgId > 0) {
            msgIdSet.add(msgId);

            // Capture stack trace
            const stack = Thread.backtrace(this.context, Backtracer.ACCURATE)
                .map(DebugSymbol.fromAddress);

            // Try to resolve RPCPath from proxy log
            const rpcPath = lookupRpc(msgId);

            captured.push({
                msgId,
                sourceHint,
                rpcPath: rpcPath || "?",
                retAddr: hex(this.returnAddress),
                stack: stack.map(String).slice(0, 8), // top 8 frames
                rcx: hex(rcxVal), rdx: hex(rdxVal), r8: hex(r8Val), r9: hex(r9Val),
            });

            const rpcLabel = rpcPath ? ` [${rpcPath}]` : "";
            console.log(`[${hitCount}] MessageId=${msgId}${rpcLabel}  source=${sourceHint}  ret=${hex(this.returnAddress)}`);
            if (!rpcPath) {
                console.log(`    (no RPCPath in proxy map yet — wait for client to send this RPC)`);
            }
        }

        if (hitCount >= MAX_HITS) {
            console.log(`\n[*] Hit limit (${MAX_HITS}) reached. Detaching interceptor...`);
            console.log('[*] Use /dump to print all captured MessageIds');
            console.log('[*] Press Ctrl+C to exit');
        }
    }
});

// ---- REPL commands ----

// In Frida console: dump() — print summary
// In Frida console: findArchive() — look for GetPlayerArchiveV2
global.dump = function() {
    console.log(`\n=== MessageId Summary (${captured.length} unique) ===`);
    for (const c of captured) {
        const known = c.rpcPath !== "?" ? "  ← KNOWN" : "";
        console.log(`  ID=${c.msgId}  → ${c.rpcPath}${known}  ret=${c.retAddr}  via=${c.sourceHint}`);
    }
    console.log(`=== End ===\n`);
};

global.findArchive = function() {
    console.log("\n=== Looking for GetPlayerArchiveV2 and UpdateRoleArchiveV2 ===");
    for (const c of captured) {
        if (c.rpcPath && c.rpcPath.toLowerCase().includes("archive")) {
            console.log(`  ID=${c.msgId} → ${c.rpcPath}  ret=${c.retAddr}`);
        }
    }
    console.log("=== If nothing found, trigger the RPC in-game and wait for proxy to log it ===\n");
};

global.raw = function() {
    console.log(JSON.stringify(captured, null, 2));
};

console.log("[*] Waiting for dispatch calls...");
console.log("[*] Commands: dump(), findArchive(), raw()");
console.log("[*] Enter armory and change equipment to generate RPC traffic\n");
