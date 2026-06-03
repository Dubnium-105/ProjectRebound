// =============================================================================
// Session 16: Force sub_9BF020 to always return 1 (pass validation)
//
// If this fixes the armory display, root cause = sub_9BF020's check.
//
// Strategy: Hook sub_9BF020's CALL SITE in sub_9B99A0, or hook sub_9BF020
// itself if possible. Force rax=1 after the call.
//
// Usage: frida -p <PID> -l tools/session16_force_pass.js
// =============================================================================

const BASE = (() => {
    for (const n of ["ProjectBoundarySteam-Win64-Shipping.exe","ProjectBoundary-Win64-Shipping.exe"])
        { try { return Process.getModuleByName(n).base; } catch(_){} }
    for (const m of Process.enumerateModules())
        { if (m.name.endsWith('.exe') && m.name.toLowerCase().includes('boundary')) return m.base; }
    throw new Error("Module not found");
})();

function hex(n) { return n instanceof NativePointer ? n.toString() : "0x" + n.toString(16); }

const validatorAddr = BASE.add(0x9BF020);
console.log(`[+] sub_9BF020: ${validatorAddr}`);

// Try multiple approaches to bypass the validation

// ---- Approach 1: Hook sub_9BF020 directly ----
let approach1 = false;
try {
    Interceptor.attach(validatorAddr, {
        onLeave(retval) {
            // Force return value to 1 (pass)
            if (retval.toInt32() === 0) {
                retval.replace(ptr(1));
                console.log(`[BYPASS] sub_9BF020 returned 0 → forced to 1`);
            }
        }
    });
    approach1 = true;
    console.log(`[+] Approach 1: Direct hook on sub_9BF020 OK`);
} catch (e) {
    console.log(`[!] Approach 1 failed: ${e.message}`);
}

// ---- Approach 2: Hook sub_9BF020's RET instruction specifically ----
if (!approach1) {
    try {
        // Scan sub_9BF020 for the RET instruction
        let addr = validatorAddr;
        while (addr.compare(validatorAddr.add(0x100)) < 0) {
            try {
                const insn = Instruction.parse(addr);
                if (insn.mnemonic === 'ret') {
                    Interceptor.attach(addr, {
                        onEnter(args) {
                            // Before ret, force rax = 1
                            if (this.context.rax.toInt32() === 0) {
                                this.context.rax = ptr(1);
                                console.log(`[BYPASS] RET hook: rax 0→1`);
                            }
                        }
                    });
                    console.log(`[+] Approach 2: RET instruction hook at ${addr}`);
                    approach1 = true; // use as flag
                    break;
                }
                addr = insn.next;
            } catch (_) { break; }
        }
    } catch (e) {
        console.log(`[!] Approach 2 failed: ${e.message}`);
    }
}

// ---- Approach 3: Hook sub_9B99A0 and patch the CALL to sub_9BF020 ----
// Find the call sub_9BF020 inside sub_9B99A0 and NOP it out, set al=1
const callerFunc = BASE.add(0x9B99A0);
let approach3Hooks = 0;

try {
    let addr = callerFunc;
    while (addr.compare(callerFunc.add(0x2000)) < 0) {
        try {
            const insn = Instruction.parse(addr);
            if (insn.mnemonic === 'call') {
                const ops = insn.operands;
                if (ops.length > 0 && ops[0].type === 'imm') {
                    const target = ptr(ops[0].value);
                    if (target.equals(validatorAddr)) {
                        // Found call sub_9BF020 — hook after it
                        const afterCall = insn.next;
                        Interceptor.attach(afterCall, {
                            onEnter(args) {
                                // Force the result seen after call returns
                                // The return value is in rax at this point
                                const rax = this.context.rax;
                                if (rax.toInt32() === 0) {
                                    this.context.rax = ptr(1);
                                    console.log(`[BYPASS] sub_9B99A0: call sub_9BF020 result 0→1`);
                                }
                            }
                        });
                        console.log(`[+] Approach 3: Hooked after call sub_9BF020 at ${afterCall}`);
                        approach3Hooks++;
                    }
                }
            }
            addr = insn.next;
        } catch (_) { break; }
    }
    if (approach3Hooks === 0) {
        console.log(`[!] Approach 3: call sub_9BF020 not found in sub_9B99A0`);
    }
} catch (e) {
    console.log(`[!] Approach 3 failed: ${e.message}`);
}

// ---- Approach 4: Hook sub_9BF020 via its vtable references ----
// The vtable unk_4258060's functions call sub_9BF020-style logic
// Hook the 3 functions that reference the vtable
const vtableRefs = [0x6319A0, 0x64DB70, 0x82EA60];
for (const rva of vtableRefs) {
    try {
        const addr = BASE.add(rva);
        Interceptor.attach(addr, {
            onLeave(retval) {
                // These create error objects — force them to return null
                // so the caller thinks no error occurred
                if (retval && !retval.isNull()) {
                    // If this function returns a struct (not null), it created an error
                    // Let's log it but not bypass — too risky
                }
            }
        });
        console.log(`[+] Approach 4: Hooked ${hex(addr)}`);
    } catch (e) {
        console.log(`[!] Approach 4 ${hex(rva)}: ${e.message}`);
    }
}

let bypassCount = 0;
console.log(`\n[*] Ready. Enter armory. Check if weapons display correctly.`);
console.log(`[*] If you see [BYPASS] messages, the bypass is working.\n`);
