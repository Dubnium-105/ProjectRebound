// =============================================================================
// Session 11: Get RoleConfig address for x64dbg hardware breakpoint
//
// Finds APBDisplayCharacter objects in memory, prints their RoleConfig address.
// Use in x64dbg: "hwbp <addr>" to set hardware write breakpoint (4 bytes).
// Then re-enter armory → breakpoint catches the overwrite.
//
// Usage: frida -p <PID> -l tools/session11_roleconfig_addr.js
// =============================================================================

const BASE = (() => {
    for (const n of ["ProjectBoundarySteam-Win64-Shipping.exe","ProjectBoundary-Win64-Shipping.exe"])
        { try { return Process.getModuleByName(n).base; } catch(_){} }
    for (const m of Process.enumerateModules())
        { if (m.name.endsWith('.exe') && m.name.toLowerCase().includes('boundary')) return m.base; }
    throw new Error("Module not found");
})();

function hex(n) { return n instanceof NativePointer ? n.toString() : "0x" + n.toString(16); }

// APBDisplayCharacter has RoleConfig at offset 0x03A0 (size 0xF8)
// We need to find APBDisplayCharacter instances.
//
// Strategy: find the APBDisplayCharacter UClass, then enumerate instances.
// Frida doesn't have UE4 object enumeration built in, so we use:
// - GObjectArray scanning, or
// - Hook SpawnDisplayCharacter to capture new objects,
// - Or just let the user enter armory and then scan memory

// Simplest: hook sub_9C4780 for msgId=2 (GetPlayerArchiveV2),
// then walk the heap to find objects whose vtable matches APBDisplayCharacter

// Even simpler: use the fact that the DLL already has SDK offsets.
// We'll hook the moment after SpawnDisplayCharacter returns and log RoleConfig.

// For now: enumerate objects of the right class by scanning known patterns
// The APBDisplayCharacter vtable can be found from the SDK header

// SDK says: APBDisplayCharacter vtable offset pattern...
// Let me use a different approach: hook the entry of the handler at 0x9C48B0
// and scan rcx/rdx for struct pointers, then try to find RoleConfig nearby

console.log("[*] Waiting for armory to open with DisplayCharacters present...");
console.log("[*] Press Enter in Frida console when armory is open.\n");

function scan() {
    console.log("[*] Scanning for APBDisplayCharacter objects...");

    // Strategy: enumerate all objects via Process.enumerateRanges
    // and check for the RoleConfig signature: CharacterID FName at +0x00

    // Actually, use a simpler method: the handler at 0x9C48B0 processes
    // archive data. When called, the struct at rcx+0x10 often points to
    // a DisplayCharacter's RoleConfig area.

    // Let's try: walk through all readable memory ranges,
    // look for plausible FName + FPBWeaponNetworkConfig patterns

    let found = 0;
    Process.enumerateRanges('r--').forEach(function(range) {
        if (found >= 5) return;
        if (range.size < 0x1000) return;

        // Scan for the vtable pointer that matches APBDisplayCharacter
        // The vtable is the first qword of the object
        // We can identify by scanning for the RoleConfig offset pattern

        // Too slow for large ranges. Let's use a targeted approach.
    });

    console.log("[!] Memory scan not feasible. Using targeted approach instead.");
    targetedScan();
}

function targetedScan() {
    // Hook the handler at 0x9C48B0 — it receives RoleConfig data context
    // When called, the caller's stack contains the DisplayCharacter pointer
    // We'll capture it from there.

    let handlerHit = 0;
    const handlerAddr = BASE.add(0x9C48B0);

    Interceptor.attach(handlerAddr, {
        onEnter(args) {
            handlerHit++;
            if (handlerHit > 3) return;

            // Try to find DisplayCharacter from various sources
            // Walk the stack to find SpawnDisplayCharacter return values
            const bt = Thread.backtrace(this.context, Backtracer.ACCURATE);
            for (let i = 0; i < bt.length; i++) {
                const sym = DebugSymbol.fromAddress(bt[i]);
                const symStr = sym.toString();
                if (symStr.includes('Spawn') || symStr.includes('Display')) {
                    console.log(`[#${handlerHit}] Stack #${i}: ${bt[i]} ${sym}`);
                    // Try to find the DisplayCharacter pointer in registers or stack
                }
            }

            // Also try: the memory at rcx-0x3A0 might be an APBDisplayCharacter
            // (if the handler receives a pointer to RoleConfig)
            try {
                const possibleChar = this.context.rcx.sub(0x3A0);
                // Check if it looks like a valid object (vtable ptr)
                const vt = possibleChar.readPointer();
                const vtSym = DebugSymbol.fromAddress(vt);
                if (vtSym && vtSym.toString().includes('Display')) {
                    const roleAddr = possibleChar.add(0x3A0);
                    console.log(`[FOUND!] APBDisplayCharacter: ${possibleChar}`);
                    console.log(`  RoleConfig @ ${roleAddr} (offset 0x3A0)`);
                    console.log(`  → In x64dbg: hwbp ${roleAddr} (set BEFORE re-entering armory)`);
                    found++;
                }
            } catch (_) {}
        }
    });

    console.log("[*] Handler hook active. Now re-enter the armory.");
    console.log("[*] The script will print RoleConfig addresses as they're found.\n");
}

// Auto-scan after a delay
setTimeout(function() {
    console.log("[*] Running targeted scan...");
    targetedScan();
}, 2000);
