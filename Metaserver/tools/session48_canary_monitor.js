// =============================================================================
// Session 48: Monitor all msgIds + scan for canary markers
//
// 1. Hook sub_9C4780 to log ALL msgIds processed
// 2. Hook sub_99E820 to see what handler addresses are dispatched to
// 3. Provide scanCanary() to search for injected ZZCANARY markers
// 4. Provide dumpContext() to analyze any memory address
// =============================================================================

const BASE = Process.getModuleByName("ProjectBoundarySteam-Win64-Shipping.exe").base;
function hex(n) { return n instanceof NativePointer ? n.toString() : "0x" + n.toString(16); }

const DISPATCH_RVA = 0x9C4780;
const LOOKUP_RVA   = 0x99E820;

console.log(`[+] BASE = ${BASE}`);

// ---------------------------------------------------------------------------
// Part 1: Monitor sub_9C4780 (DISPATCH) — log ALL msgIds
// ---------------------------------------------------------------------------
let dispatchLog = [];
const MAX_LOG = 100;

Interceptor.attach(BASE.add(DISPATCH_RVA), {
    onEnter(args) {
        const msgId = args[1].toInt32();
        const r8 = args[2];
        const r9 = args[3];
        const retAddr = this.returnAddress;
        const rva = retAddr.sub(BASE).toInt32();

        dispatchLog.push({
            msgId,
            time: Date.now(),
            r8: r8.toString(),
            r9: r9.toString(),
            callerRVA: hex(rva),
        });
        if (dispatchLog.length > MAX_LOG) dispatchLog.shift();

        // Log all msgIds with brief info
        const marker = msgId === 2 ? ' ← GetPlayerArchiveV2!' : '';
        console.log(`[DISPATCH] msgId=${msgId} r8=${r8} r9=${r9} caller=${hex(rva)}${marker}`);
    }
});

// ---------------------------------------------------------------------------
// Part 2: Monitor sub_99E820 (LOOKUP) — log handler addresses
// ---------------------------------------------------------------------------
Interceptor.attach(BASE.add(LOOKUP_RVA), {
    onEnter(args) {
        this.r8 = args[2];
        this.r9 = args[3];
    },
    onLeave(retval) {
        // Only log occasionally
        if (this.logCount === undefined) this.logCount = 0;
        this.logCount++;
        if (this.logCount <= 20) {
            const handlerRVA = retval.sub(BASE).toInt32();
            console.log(`[LOOKUP#${this.logCount}] handler_addr=${retval} handlerRVA=${hex(handlerRVA)}`);
        }
    }
});

console.log(`[+] Hooks active: DISPATCH @ ${BASE.add(DISPATCH_RVA)}, LOOKUP @ ${BASE.add(LOOKUP_RVA)}`);

// ---------------------------------------------------------------------------
// Part 3: Canary scanner
// ---------------------------------------------------------------------------
const CANARIES = ["ZZCANARYX001", "ZZCANARYX002", "ZZCANARYX003"];

function scanCanary() {
    console.log(`\n[*] Scanning for canary markers...`);

    for (const canary of CANARIES) {
        const bytes = [];
        for (let i = 0; i < canary.length; i++) bytes.push(canary.charCodeAt(i).toString(16).padStart(2,'0'));
        const pattern = bytes.join(' ');
        let count = 0;

        // Scan heap (rw-)
        Process.enumerateRanges('rw-').forEach(function(range) {
            if (count >= 5) return;
            if (range.size < 0x1000) return;
            const lo = range.base.and(ptr(0xFFFFFFFF)).toUInt32();
            if (lo < 0x10000000) return;

            try {
                Memory.scan(range.base, range.size, pattern, {
                    onMatch(addr) {
                        if (count >= 5) return 'stop';
                        count++;
                        console.log(`\n  [CANARY:${canary}] @ ${addr}`);
                        try {
                            // Dump 256 bytes around the hit
                            const ctx = addr.sub(64);
                            console.log(`  ${hexdump(ctx.readByteArray(256), {offset:0,length:256,header:false,ansi:true})}`);

                            // Check if this is in protobuf data
                            // Look for protobuf varint patterns nearby
                            const buf = new Uint8Array(ctx.readByteArray(256));
                            let protoLike = 0;
                            for (let i = 0; i < buf.length - 4; i++) {
                                if (buf[i] === 0x0a || buf[i] === 0x12 || buf[i] === 0x1a) {
                                    if (buf[i+1] >= 0x01 && buf[i+1] <= 0x40) protoLike++;
                                }
                            }
                            if (protoLike > 3) {
                                console.log(`  → This looks like PROTOBUF data (${protoLike} field markers)`);
                            }

                            // Check if it's followed by null or another string
                            const nextByte = new Uint8Array([buf[64 + canary.length]]);
                            const nextChar = String.fromCharCode(nextByte[0]);
                            console.log(`  → Next byte after canary: 0x${nextByte[0].toString(16)} ('${nextChar}')`);

                        } catch(e) {
                            console.log(`  read error: ${e.message}`);
                        }
                    },
                    onComplete() {},
                    onError(e) {}
                });
            } catch(e) {}
        });

        if (count === 0) {
            console.log(`  [${canary}] NOT FOUND in heap (may not have been sent yet)`);
        } else {
            console.log(`  [${canary}] Found ${count} occurrences`);
        }
    }
}

// ---------------------------------------------------------------------------
// Part 4: Also scan module for canary (protobuf descriptors or cached data)
// ---------------------------------------------------------------------------
function scanModule() {
    console.log(`\n[*] Scanning module sections for canary markers...`);
    for (const canary of CANARIES) {
        const bytes = [];
        for (let i = 0; i < canary.length; i++) bytes.push(canary.charCodeAt(i).toString(16).padStart(2,'0'));
        const pattern = bytes.join(' ');
        let count = 0;
        try {
            Memory.scan(BASE, 0x30000000, pattern, {
                onMatch(addr) {
                    if (count >= 3) return 'stop';
                    count++;
                    console.log(`  [${canary}] MODULE @ ${addr}`);
                },
                onComplete() {},
                onError(e) {}
            });
        } catch(e) {}
        console.log(`  [${canary}] in module: ${count} hits`);
    }
}

// ---------------------------------------------------------------------------
// Part 5: Show dispatch log
// ---------------------------------------------------------------------------
function showLog() {
    console.log(`\n=== Dispatch Log (${dispatchLog.length} entries) ===`);
    const seen = {};
    for (const entry of dispatchLog) {
        const key = entry.msgId;
        if (!seen[key]) seen[key] = 0;
        seen[key]++;
    }
    console.log(`MsgId frequencies:`);
    for (const [msgId, count] of Object.entries(seen).sort((a,b) => a[0]-b[0])) {
        console.log(`  msgId=${msgId}: ${count} times`);
    }
    console.log(`\nLast 20 entries:`);
    for (const entry of dispatchLog.slice(-20)) {
        const marker = entry.msgId === 2 ? ' ← GetPlayerArchiveV2' : '';
        console.log(`  msgId=${entry.msgId} r8=${entry.r8} caller=${entry.callerRVA}${marker}`);
    }
}

// ---------------------------------------------------------------------------
// Part 6: Dump context around any address
// ---------------------------------------------------------------------------
function dumpCtx(addr, size) {
    size = size || 256;
    try {
        console.log(`\n[*] Dump around ${addr}:`);
        console.log(hexdump(addr.sub(64).readByteArray(size), {offset:0,length:size,header:false,ansi:true}));
    } catch(e) {
        console.log(`Error: ${e.message}`);
    }
}

console.log(`\n[READY] Commands:`);
console.log(`  scanCanary()   - Search heap for ZZCANARY markers`);
console.log(`  scanModule()   - Search module sections for canary markers`);
console.log(`  showLog()      - Show dispatch log summary`);
console.log(`  dumpCtx(addr)  - Hex dump around an address`);
console.log(`\n[*] Monitor is running. Watch for msgId=2 (GetPlayerArchiveV2) in dispatch log.`);
console.log(`[*] After msgId=2 fires, run scanCanary() to find where the data was stored.\n`);
