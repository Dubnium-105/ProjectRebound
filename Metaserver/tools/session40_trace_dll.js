// =============================================================================
// Session 40: Trace DLL's actual AppendString call at runtime
//
// The DLL calls FName::AppendString via CallGameFunction at offset 0x19D82B0,
// but we found this is mid-instruction in sub_19D8120. Let's trace what the DLL
// actually does at runtime.
//
// Also: find and hook the REAL FName::AppendString by scanning for its pattern.
// In UE4, AppendString takes (FName* this, FString& Out) and writes to the FString.
// =============================================================================

const BASE = Process.getModuleByName("ProjectBoundarySteam-Win64-Shipping.exe").base;
let dllBase = null;
try { dllBase = Process.getModuleByName("ProjectReboundMainDLL.dll").base; } catch(_) {}

function hex(n) { return n instanceof NativePointer ? n.toString() : "0x" + n.toString(16); }

console.log(`[+] Game BASE = ${BASE}`);
console.log(`[+] DLL  BASE = ${dllBase}`);

// ---------------------------------------------------------------------------
// Part 1: Hook the game function at the SDK's AppendString offset
// If it's ever called as a function entry, log its arguments and return value
// ---------------------------------------------------------------------------
const SDK_APPEND_STRING = BASE.add(0x019D82B0);
console.log(`[+] SDK AppendString offset = ${SDK_APPEND_STRING}`);

// Read what's actually at that address
try {
    const bytes = SDK_APPEND_STRING.readByteArray(16);
    console.log(`[+] Bytes at 0x19D82B0: ${hexdump(bytes, {offset:0,length:16,header:false,ansi:false})}`);
} catch(e) {
    console.log(`[-] Cannot read at 0x19D82B0: ${e.message}`);
}

// ---------------------------------------------------------------------------
// Part 2: Scan for functions that look like AppendString
//
// AppendString pattern: void AppendString(FName* this, FString& Out)
// - rcx = FName* (reads ComparisonIndex from [rcx])
// - rdx = FString* (writes to [rdx] = Data, [rdx+8] = ArrayNum)
//
// We'll scan the code section for functions that:
// 1. Read a uint32 from [rcx] (the FName ComparisonIndex)
// 2. Write to [rdx+8] (FString ArrayNum)
// 3. Reference a pool structure (lea reg, [rip+...])
//
// But a faster approach: use the DLL module's exports to find what it calls
// ---------------------------------------------------------------------------

// Find CallGameFunction in the DLL
let callGameFunctionAddr = null;
if (dllBase) {
    // Scan DLL exports for CallGameFunction
    const dllExports = Process.getModuleByName("ProjectReboundMainDLL.dll").enumerateExports();
    for (const exp of dllExports) {
        if (exp.name.includes("CallGame") || exp.name.includes("AppendString") || exp.name.includes("FName")) {
            console.log(`  DLL export: ${exp.name} @ ${exp.address}`);
        }
    }
}

// ---------------------------------------------------------------------------
// Part 3: Find real AppendString by scanning the module's code
//
// In UE4.27, FName::AppendString does:
//   1. GetDisplayIndex() → splits ComparisonIndex into Block/Offset
//   2. GetEntry(Block, Offset) → pool[Block+2] + 2*Offset
//   3. Read header, check wide flag
//   4. If wide: append UTF-16 directly
//   5. If narrow: widen bytes to UTF-16 and append
//
// Key signature:
//   mov eax, [rcx]           ; read ComparisonIndex from FName
//   mov edx, eax
//   shr edx, 10h             ; block index
//   movzx ecx, ax            ; offset within block
//   ...
//   mov [rdx], ...           ; write to FString.Data
//   mov [rdx+8], ...         ; write to FString.ArrayNum
//
// But the pool address would be different from 0x5D29280.
// ---------------------------------------------------------------------------

// Scan for the FName entry resolution pattern
function findAppendString() {
    console.log(`\n[*] Scanning for FName::AppendString pattern in game code...`);

    // Search for: mov eax,[rcx]; mov r8d,eax; shr r8d,10h; movzx ecx,ax
    // This is the standard FName split pattern
    const pattern = "8B 01 44 8B C0 41 C1 E8 10 0F B7 C8";
    const start = BASE.add(0x400000);
    const size = BASE.add(0x2200000).sub(start).toInt32();

    const candidates = [];

    Memory.scan(start, size, pattern, {
        onMatch(addr) {
            if (candidates.length >= 30) return 'stop';
            try {
                // Read the function prologue
                const funcStart = addr.sub(0x20); // approximate
                const bytes = funcStart.readByteArray(64);
                candidates.push({addr, bytes});
                const u8 = new Uint8Array(bytes);
                const hexStr = Array.from(u8.slice(0, 32)).map(b => b.toString(16).padStart(2,'0')).join(' ');
                console.log(`  [${candidates.length}] @ ${addr}: ${hexStr}...`);
            } catch(_) {}
        },
        onComplete() { /* done */ },
        onError(e) { console.log(`  Scan error: ${e}`); }
    });

    console.log(`[+] Found ${candidates.length} candidates`);
    return candidates;
}

// ---------------------------------------------------------------------------
// Part 4: Search for the REAL FName pool
//
// In UE4, GName is a global pointer. At startup, it's allocated via Malloc.
// The GName variable itself is in the .data section. Let's search for it
// by looking for cross-references FROM FName::StaticInit or similar.
// ---------------------------------------------------------------------------

// Look for all mov instructions that reference a .data address containing
// a pointer to heap-allocated block arrays

// ---------------------------------------------------------------------------
// Part 5: Hook the sub_19D8100/sub_19D8120 call chain to see what they do at runtime
// ---------------------------------------------------------------------------

// Hook sub_19D8100 (the wrapper that calls sub_19D8120)
const WRAPPER_RVA = 0x19D8100;

try {
    Interceptor.attach(BASE.add(WRAPPER_RVA), {
        onEnter(args) {
            // sub_19D8100 is called with two FName arguments
            // Let's see what it receives
            const rcx0 = args[0];
            const rdx0 = args[1];
            try {
                const compIdx1 = rcx0.readU32();
                const compIdx2 = rdx0.readU32();
                // Only log occasionally to avoid spam
                if (this.counter === undefined) this.counter = 0;
                this.counter++;
                if (this.counter <= 5) {
                    console.log(`[sub_19D8100] call #${this.counter}: cmpIdx1=${compIdx1}, cmpIdx2=${compIdx2}`);
                }
            } catch(_) {}
        }
    });
    let callCount19D8100 = 0;
    console.log(`[+] Hooked sub_19D8100 @ ${BASE.add(WRAPPER_RVA)}`);
} catch(e) {
    console.log(`[-] Failed to hook sub_19D8100: ${e.message}`);
}

// ---------------------------------------------------------------------------
// Part 6: Direct search for FName::ToString using UE4-specific patterns
//
// FName::ToString(FString& Out):
//   - Calls GetDisplayNameEntry() to get FNameEntry*
//   - Calls FNameEntry::AppendNameToString(Out)
//
// GetDisplayNameEntry pattern:
//   - Reads GName global pointer
//   - Uses ComparisonIndex >> 16 as block index
//   - Uses ComparisonIndex & 0xFFFF as offset
//   - Returns GName[blockIdx][offset]
//
// Search for: read from GName-like global, then use HIWORD/LOWORD split
// ---------------------------------------------------------------------------

console.log(`\n[READY] Script loaded. Waiting for activity...`);
console.log(`  findAppendString() - Memory scan for FName resolution pattern`);
console.log(`  Enter armory to trigger FName operations\n`);
