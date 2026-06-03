// =============================================================================
// Session 12: Hook vtable references to ErrorCode factory (unk_4258060)
//
// From IDA xrefs, unk_4258060 is referenced by:
//   sub_6319A0, sub_64DB70, sub_82EA60, sub_BA0FC0
//
// These create ErrorCode structs. Hook them to capture the call stack.
// Also try to hook the 'mov [rax+0x0C], 4' instruction directly.
//
// Usage: frida -p <PID> -l tools/session12_hook_vtable_refs.js
// =============================================================================

const BASE = (() => {
    for (const n of ["ProjectBoundarySteam-Win64-Shipping.exe","ProjectBoundary-Win64-Shipping.exe"])
        { try { return Process.getModuleByName(n).base; } catch(_){} }
    for (const m of Process.enumerateModules())
        { if (m.name.endsWith('.exe') && m.name.toLowerCase().includes('boundary')) return m.base; }
    throw new Error("Module not found");
})();

function hex(n) { return n instanceof NativePointer ? n.toString() : "0x" + n.toString(16); }
function rva(ptr) { return ptr instanceof NativePointer ? hex(ptr.sub(BASE).toInt32()) : '?'; }

// Functions that create ErrorCode=4 structs via unk_4258060 vtable
const targets = [
    { rva: 0x6319A0, name: 'sub_6319A0' },
    { rva: 0x64DB70, name: 'sub_64DB70' },
    { rva: 0x82EA60, name: 'sub_82EA60' },
    // sub_BA0FC0 is too small to hook, skip
    // Also try: the second factory
    { rva: 0xBB4B60, name: 'sub_BB4B60 (factory type=8)' },
];

// Also try hooking the instruction "mov [rax+0x0C], 4" directly
// at RVA 0xBB4B84 using Memory.write to insert INT3
const instructionTargets = [
    { rva: 0xBB4B84, name: 'mov [rax+0Ch],4 @ sub_BB4B60' },
];

let hitCounts = {};

for (const t of targets) {
    try {
        const addr = BASE.add(t.rva);
        hitCounts[t.name] = 0;
        Interceptor.attach(addr, {
            onEnter(args) {
                hitCounts[t.name]++;
                if (hitCounts[t.name] > 3) return;
                console.log(`\n[${t.name} HIT #${hitCounts[t.name]}]`);
                console.log(`  ret=${rva(this.returnAddress)}`);

                // Call stack
                const bt = Thread.backtrace(this.context, Backtracer.ACCURATE);
                for (let i = 0; i < 8; i++) {
                    console.log(`  #${i}: RVA=${rva(bt[i])}  ${DebugSymbol.fromAddress(bt[i])}`);
                }
            }
        });
        console.log(`[+] Hooked ${t.name} @ ${addr}`);
    } catch (e) {
        console.log(`[!] ${t.name}: ${e.message}`);
    }
}

// For the instruction-level hooks, we need a different approach
// Try to patch the instruction with a software breakpoint →
// catch via exception handler

let broken = false;
function installInstructionBreak() {
    if (broken) return;
    try {
        const addr = BASE.add(0xBB4B84);
        console.log(`[*] Installing instruction break at ${addr}...`);

        // Use Memory.patchCode to safely modify code
        Memory.patchCode(addr, 8, function(code) {
            const writer = new X86Writer(code, { pc: addr });
            writer.putBreakpoint();
            writer.flush();
        });

        // Set up exception handler to catch the breakpoint
        Process.setExceptionHandler(function(details) {
            if (details.type === 'breakpoint' && details.address.equals(addr)) {
                console.log(`\n[!!!] mov [rax+0Ch],4 EXECUTED!`);
                console.log(`  address: ${details.address}`);
                console.log(`  context.rax: ${details.context.rax}`);

                // Check what's being written
                try {
                    const structAddr = details.context.rax;
                    const oldVal = structAddr.add(0x0C).readS32();
                    console.log(`  struct: ${structAddr}  old ErrorCode: ${oldVal}`);
                } catch (_) {}

                // Stack trace
                const bt = Thread.backtrace(details.context, Backtracer.ACCURATE);
                for (let i = 0; i < 10; i++) {
                    console.log(`  #${i}: RVA=${rva(bt[i])}  ${DebugSymbol.fromAddress(bt[i])}`);
                }

                // Resume execution (skip the breakpoint)
                details.context.pc = addr.add(7); // skip 7-byte instruction
                return true; // handled
            }
            return false;
        });

        broken = true;
        console.log(`[+] Instruction break installed.`);
    } catch (e) {
        console.log(`[!] Instruction break failed: ${e.message}`);
    }
}

function stats() {
    console.log('\n=== Hit counts ===');
    for (const [name, count] of Object.entries(hitCounts)) {
        console.log(`  ${name}: ${count}`);
    }
}

console.log(`[*] Hooks active. Enter armory, then stats().`);
console.log(`[*] installInstructionBreak() to try instruction-level hook.\n`);
