// =============================================================================
// Session 18: Cheat-style RoleConfig injection
//
// 1. Find ProcessEvent from UObject vtable via handler hook
// 2. Log ALL BP events during armory entry (FName index)
// 3. Identify SpawnDisplayCharacter event, hook it, overwrite RoleConfig
//
// Usage: frida -p <PID> -l tools/session18_cheat_test.js
// =============================================================================

const BASE = (() => {
    for (const n of ["ProjectBoundarySteam-Win64-Shipping.exe","ProjectBoundary-Win64-Shipping.exe"])
        { try { return Process.getModuleByName(n).base; } catch(_){} }
    for (const m of Process.enumerateModules())
        { if (m.name.endsWith('.exe') && m.name.toLowerCase().includes('boundary')) return m.base; }
    throw new Error("Module not found");
})();

function hex(n) { return n instanceof NativePointer ? n.toString() : "0x" + n.toString(16); }

let processEventAddr = null;
let hookInstalled = false;

// =============================================================================
// Find ProcessEvent from handler's UObject args
// =============================================================================

const handlerAddr = BASE.add(0x9C48B0);
Interceptor.attach(handlerAddr, {
    onEnter(args) {
        if (processEventAddr) return;
        for (const reg of [this.context.rcx, this.context.rdx, this.context.r8, this.context.r9]) {
            if (!reg || reg.isNull()) continue;
            try {
                const vt = reg.readPointer();
                if (vt.compare(BASE) < 0 || vt.compare(BASE.add(0x20000000)) > 0) continue;
                const pe = vt.add(64 * 8).readPointer();
                if (pe.isNull()) continue;
                if (pe.compare(BASE) < 0 || pe.compare(BASE.add(0x20000000)) > 0) continue;

                processEventAddr = pe;
                console.log(`[+] ProcessEvent: ${pe}  RVA=${hex(pe.sub(BASE).toInt32())}`);
                installPEHook();
                return;
            } catch (_) {}
        }
    }
});

// =============================================================================
// Hook ProcessEvent
// =============================================================================

let eventLog = [];
let logging = false;
let displayChars = [];

function installPEHook() {
    if (hookInstalled) return;
    hookInstalled = true;

    Interceptor.attach(processEventAddr, {
        onEnter(args) {
            const uobj = args[0];
            const func = args[1];
            const parms = args[2];

            if (!func || func.isNull() || !logging) return;

            // Read UFunction's FName: ComparisonIndex at func+0x18, Number at func+0x1C
            let nameIdx = 0, nameNum = 0;
            try {
                nameIdx = func.add(0x18).readU32();
                nameNum = func.add(0x1C).readU32();
            } catch (_) {}

            if (nameIdx === 0) return;

            // Record the event
            const rec = { idx: nameIdx, num: nameNum, obj: uobj.toString(), parms: parms.toString() };
            const key = `${nameIdx}`;

            if (!eventLog.find(e => e.idx === nameIdx)) {
                eventLog.push(rec);
                if (eventLog.length <= 40) {
                    console.log(`[EVENT] FName(${nameIdx},${nameNum})  obj=${uobj}`);
                }
            }

            // Store context for onLeave
            this._idx = nameIdx;
            this._num = nameNum;
            this._obj = uobj;
            this._parms = parms;
        },
        onLeave(retval) {
            // Check if this was a spawn event (find ReturnValue in parms)
            if (!this._parms || this._parms.isNull()) return;

            // Try to find a returned DisplayCharacter pointer in the params
            for (const off of [0x00, 0x08, 0x10, 0x18, 0x20]) {
                try {
                    const rv = this._parms.add(off).readPointer();
                    if (rv.isNull()) continue;
                    const vt = rv.readPointer();
                    if (vt.compare(BASE) < 0 || vt.compare(BASE.add(0x20000000)) > 0) continue;

                    // Check RoleConfig at +0x3A0
                    const cid = rv.add(0x3A0).readU32();
                    if (cid < 1 || cid > 50000) continue;

                    // Found a DisplayCharacter!
                    const key = rv.toString();
                    if (!displayChars.find(dc => dc.addr.equals(rv))) {
                        displayChars.push({
                            addr: rv,
                            roleConfigAddr: rv.add(0x3A0),
                            fromEvent: `${this._idx}`,
                        });
                        console.log(`\n[CHAR #${displayChars.length}] ${rv}`);
                        console.log(`  From event FName(${this._idx})`);
                        console.log(`  CharID FName: idx=${cid}`);
                        console.log(`  W1 FName: idx=${rv.add(0x3A0+0x30+0x28).readU32()}`);
                    }
                } catch (_) {}
            }
        }
    });
    console.log(`[+] ProcessEvent hook active. Use startLog() to begin.`);
}

// =============================================================================
// REPL
// =============================================================================

function startLog() {
    logging = true;
    eventLog = [];
    displayChars = [];
    console.log(`[+] Logging STARTED. Enter armory now!`);
    console.log(`[*] All BP events will be logged. When done, stopLog().`);
}

function stopLog() {
    logging = false;
    console.log(`\n=== Logged ${eventLog.length} unique events ===`);
    for (const e of eventLog.slice(0, 30)) {
        console.log(`  FName(${e.idx},${e.num})`);
    }
    console.log(`\n=== Found ${displayChars.length} DisplayCharacters ===`);
    listChars();
}

function listChars() {
    for (let i = 0; i < displayChars.length; i++) {
        const dc = displayChars[i];
        try {
            const cid = dc.addr.add(0x3A0).readU32();
            const w1  = dc.addr.add(0x3A0 + 0x30 + 0x28).readU32();
            const w2  = dc.addr.add(0x3A0 + 0x68 + 0x28).readU32();
            console.log(`  [${i}] ${dc.addr}  Char=${cid}  W1=${w1}  W2=${w2}  event=${dc.fromEvent}`);
        } catch (_) {}
    }
}

function patchChar(idx, weaponFNameIdx) {
    if (idx >= displayChars.length) { console.log("Invalid index"); return; }
    const dc = displayChars[idx];
    dc.addr.add(0x3A0 + 0x30 + 0x28).writeU32(weaponFNameIdx);
    dc.addr.add(0x3A0 + 0x30 + 0x2C).writeU32(0); // Number = 0
    console.log(`[+] Patched char[${idx}] W1 -> FName(${weaponFNameIdx},0)`);
}

if (!processEventAddr) {
    console.log(`[*] ProcessEvent not yet found. Waiting for handler call...`);
    console.log(`[*] Enter armory to trigger handler, which reveals ProcessEvent.`);
}
console.log(`[*] Commands: startLog(), stopLog(), listChars(), patchChar(N, weaponIdx)\n`);
