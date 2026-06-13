// Session 36 v2: Hook AppendString on LEAVE to capture resolved strings
const BASE = Process.getModuleByName("ProjectBoundarySteam-Win64-Shipping.exe").base;
function hex(n) { return n instanceof NativePointer ? n.toString() : "0x" + n.toString(16); }

const fnameMap = {};
let count = 0;

Interceptor.attach(BASE.add(0x019D82B0), {
    onEnter(args) {
        this.fname = args[0]; // const FName*
        this.fstr  = args[1]; // FString&
    },
    onLeave(retval) {
        const fname = this.fname;
        const fstr  = this.fstr;

        try {
            const idx = fname.readU32();
            if (idx < 1 || idx > 200000 || fnameMap[idx]) return;

            // FString: Data ptr at +0, ArrayNum at +8
            const dataPtr = fstr.readPointer();
            const arrNum  = fstr.add(8).readU32();

            if (!dataPtr.isNull() && arrNum > 0 && arrNum < 256) {
                const str = dataPtr.readUtf16String(arrNum);
                if (str && str.length > 0) {
                    fnameMap[idx] = str;
                    count++;
                    if (count % 200 === 0) console.log(`[MAP] ${count} entries. idx=${idx}→"${str}"`);
                }
            }
        } catch(_) {}
    }
});

function dumpMap(n) { n=n||30; const k=Object.keys(fnameMap).sort((a,b)=>a-b); console.log(`\n=== ${k.length} entries ===`); for(const i of k.slice(0,n)) console.log(`  ${i}: ${fnameMap[i]}`); }
function lookup(s) { for(const [k,v] of Object.entries(fnameMap)) if(v.includes(s)) console.log(`  idx=${k} → "${v}"`); }

console.log(`[+] Hooked. Enter armory. dumpMap() lookup("weapon")\n`);
