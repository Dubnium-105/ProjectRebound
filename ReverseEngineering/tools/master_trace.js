// =============================================================================
// Master Trace Script — MessageId discovery + handler identification
//
// Usage:
//   frida -p <PID> -l tools/master_trace.js
//   > start()       — begin tracing
//   > dump()        — show all captured MessageIds
//   > findArchive() — filter archive-related RPCs
//   > export()      — save results to logs/frida_export.json
//
// The dispatch hook is active immediately on load.
// Call start() to also load proxy mappings.
// =============================================================================

const DISPATCH_RVA = 0x9C4780;  // sub_1409C4780 in IDA

// =============================================================================
// Module auto-detection
// =============================================================================

function findGameModule() {
    const mods = Process.enumerateModules();
    const known = [
        "ProjectBoundarySteam-Win64-Shipping.exe",
        "ProjectBoundary-Win64-Shipping.exe",
        "ProjectBoundarySteam.exe",
        "ProjectBoundary.exe",
    ];
    for (const name of known) {
        try { const m = Process.getModuleByName(name); return { base: m.base, name }; } catch (_) {}
    }
    for (const m of mods) {
        const n = m.name.toLowerCase();
        if (n.endsWith('.exe') && (n.includes('boundary') || n.includes('shipping')))
            return { base: m.base, name: m.name };
    }
    let best = null;
    for (const m of mods) {
        if (!m.name.endsWith('.exe')) continue;
        if (!best || m.size > best.size) best = m;
    }
    return best ? { base: best.base, name: best.name } : null;
}

const gm = findGameModule();
if (!gm) {
    console.error("[!] Cannot find game module. .exe modules:");
    Process.enumerateModules().filter(m => m.name.endsWith('.exe'))
        .forEach(m => console.error(`  ${m.name} @ ${m.base}`));
    throw new Error("Game module not found");
}

const MODULE_NAME = gm.name;
const BASE = gm.base;
console.log(`[+] Module: ${MODULE_NAME}`);
console.log(`[+] Base:   ${BASE}`);

const dispatchFunc = BASE.add(DISPATCH_RVA);
console.log(`[+] Target: ${dispatchFunc}  (sub_${hexStr(DISPATCH_RVA)})`);

// =============================================================================
// Helpers
// =============================================================================

function hexStr(n) {
    if (n instanceof NativePointer) return n.toString();
    return "0x" + n.toString(16).toUpperCase();
}

function safeS32(addr, off) {
    try { return addr.add(off).readS32(); } catch (_) { return null; }
}

function readTextFile(path) {
    try { const f = new File(path, 'r'); const c = f.read(); f.close(); return c; }
    catch (_) { return null; }
}

function writeTextFile(path, content) {
    try { const f = new File(path, 'w'); f.write(content); f.close(); return true; }
    catch (_) { return false; }
}

// =============================================================================
// Proxy MessageId→RPCPath map
// =============================================================================

const MSGID_MAP_PATH = 'g:/wksp/boundaries/ProjectRebound/Metaserver/logs/msgid_map.json';
const msgIdToRpc = {};

function loadProxyMap() {
    const raw = readTextFile(MSGID_MAP_PATH);
    if (!raw) return 0;
    try {
        const obj = JSON.parse(raw);
        let n = 0;
        for (const [k, v] of Object.entries(obj)) { msgIdToRpc[parseInt(k)] = v; n++; }
        return n;
    } catch (_) { return 0; }
}

function rpcLabel(msgId) { return msgIdToRpc[msgId] || '?'; }

// =============================================================================
// Dispatch hook — raw probe mode
// =============================================================================

const hits = {};
const hitList = [];
let callCount = 0;
const MAX_RAW_LOG = 5;  // only log first 5 calls raw for verification

Interceptor.attach(dispatchFunc, {
    onEnter(args) {
        callCount++;
        const ctx = this.context;

        if (callCount <= MAX_RAW_LOG) {
            console.log(`[CALL #${callCount}] rcx=${ctx.rcx} rdx=${ctx.rdx} r8=${ctx.r8} r9=${ctx.r9} ret=${this.returnAddress}`);
        }

        // From raw data: r8 = MessageId (small int, 1-200 range)
        // rcx = this pointer, rdx = some heap pointer, r9 = 0
        let msgId = null, source = '';

        const r8i = ctx.r8.toInt32();
        if (r8i > 0 && r8i < 500) { msgId = r8i; source = 'r8'; }

        // Fallback: rdx if it happens to be small
        if (msgId === null) {
            const rd32 = ctx.rdx.toInt32();
            if (rd32 > 0 && rd32 < 500) { msgId = rd32; source = 'rdx'; }
        }

        if (msgId !== null && msgId > 0) {
            this.msgId = msgId;
            if (!hits[msgId]) {
                loadProxyMap();
                const rpc = rpcLabel(msgId);
                hits[msgId] = {
                    retAddr: hexStr(this.returnAddress),
                    count: 0, source, rpc,
                };
                hitList.push(msgId);
                console.log(`  >>> msgId=${msgId} ${rpc !== '?' ? '→ ' + rpc : '(unknown)'}  src=${source}`);
            }
            hits[msgId].count++;
        }
    }
});

// =============================================================================
// REPL commands
// =============================================================================

let started = false;

function start() {
    if (started) { console.log("[*] Already started."); return; }
    started = true;
    const n = loadProxyMap();
    console.log(`\n=== Master Trace Active ===`);
    console.log(`[+] Proxy mapping: ${n} entries loaded`);
    console.log(`[+] Dispatch hook: ACTIVE (${hitList.length} msgIds seen so far)`);
    console.log(`[*] Trigger RPCs in-game → dump() to view\n`);
};

function dump() {
    loadProxyMap();
    console.log(`\n=== MessageId Summary (${hitList.length} unique, ${Object.keys(hits).length} total) ===`);
    for (const id of hitList) {
        const h = hits[id];
        const rpc = rpcLabel(id);
        const label = rpc !== '?' ? rpc : h.rpc;
        const marker = label !== '?' ? ' ✓' : ' ?';
        console.log(`  msgId=${id}${marker} → ${label !== '?' ? label : 'UNKNOWN'}`);
        console.log(`        ret=${h.retAddr}  count=${h.count}  src=${h.source}`);
    }
};

function findArchive() {
    loadProxyMap();
    console.log("\n=== Archive-related ===");
    // From proxy map
    for (const [id, rpc] of Object.entries(msgIdToRpc)) {
        if (rpc.toLowerCase().includes('archive') || rpc.toLowerCase().includes('player')) {
            console.log(`  ID=${id} → ${rpc}`);
        }
    }
    // From hits
    for (const id of hitList) {
        const rpc = rpcLabel(id);
        if (rpc.toLowerCase().includes('archive')) {
            console.log(`  [HIT] ID=${id} → ${rpc}  ret=${hits[id].retAddr}`);
        }
    }
    console.log("(If empty, trigger GetPlayerArchiveV2 by entering armory)\n");
};

function exportData() {
    loadProxyMap();
    const data = {
        module: MODULE_NAME,
        base: BASE.toString(),
        dispatchRVA: hexStr(DISPATCH_RVA),
        hits: hitList.map(id => ({
            msgId: id,
            ...hits[id],
        })),
        proxyMap: msgIdToRpc,
    };
    const json = JSON.stringify(data, null, 2);
    const ok = writeTextFile(
        'g:/wksp/boundaries/ProjectRebound/Metaserver/logs/frida_export.json', json);
    if (ok) console.log(`[+] Exported ${json.length} bytes to logs/frida_export.json`);
    else console.log(`[!] Write failed. Data:\n${json}`);
};

function status() {
    loadProxyMap();
    console.log(`Module:  ${MODULE_NAME} @ ${BASE}`);
    console.log(`Target:  ${dispatchFunc}`);
    console.log(`Started: ${started}`);
    console.log(`Hits:    ${hitList.length} unique msgIds`);
    console.log(`Proxy:   ${Object.keys(msgIdToRpc).length} mapped RPCs`);
};

// Auto-load proxy map on script load
loadProxyMap();
console.log(`[+] Proxy mapping: ${Object.keys(msgIdToRpc).length} RPCs known`);
console.log(`[*] Dispatch hook active — waiting for RPC traffic...`);
console.log(`[*] Commands: start(), dump(), findArchive(), exportData(), status()`);
console.log(`[*] Enter armory to trigger GetPlayerArchiveV2, then dump()\n`);
