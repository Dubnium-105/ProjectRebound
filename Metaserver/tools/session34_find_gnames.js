// Session 34: Find GName pool via Memory.scan (fast native pattern search)
const BASE = Process.getModuleByName("ProjectBoundarySteam-Win64-Shipping.exe").base;
function hex(n) { return n instanceof NativePointer ? n.toString() : "0x" + n.toString(16); }

const WEAPONS = ["PEACE_GSW-AR","PROBE_RU-AKM","SNIPER_RU-MOSIN"];
let found = 0;

function scanFor(wid) {
    const bytes = [];
    for (let i = 0; i < wid.length; i++) bytes.push(wid.charCodeAt(i).toString(16).padStart(2,'0'));
    const pattern = bytes.join(' ').toUpperCase();
    console.log(`[*] Scanning for "${wid}": ${pattern}`);

    const start = BASE.add(0x400000);
    const end = BASE.add(0x2500000);
    const size = end.sub(start).toInt32();

    try {
        Memory.scan(start, size, pattern, {
            onMatch(address) {
                found++;
                let header = 0;
                try { header = address.sub(2).readU16(); } catch(_) {}
                const len = header & 0x3F;
                const wide = (header >> 15) & 1;
                console.log(`[#${found}] "${wid}" @ ${address}  hdr@-2=${hex(header)} len=${len} wide=${wide}`);
                try {
                    console.log(hexdump(address.sub(4).readByteArray(32), {offset:0,length:32,header:false,ansi:true}));
                } catch(_) {}
                if (found >= 5) return 'stop';
                // continue scanning
            },
            onComplete() {
                console.log(`  Done scanning for "${wid}"`);
            },
            onError(e) {
                console.log(`  Error: ${e}`);
            }
        });
    } catch(e) {
        console.log(`  Scan failed: ${e.message}`);
    }
}

for (const wid of WEAPONS) {
    scanFor(wid);
    if (found >= 5) break;
}

console.log(`\n[+] Found ${found} occurrences\n`);
