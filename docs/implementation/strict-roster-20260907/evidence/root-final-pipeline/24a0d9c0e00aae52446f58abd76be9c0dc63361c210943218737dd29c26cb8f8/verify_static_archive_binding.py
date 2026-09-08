import hashlib, json, struct, sys
from datetime import datetime, timezone
from pathlib import Path
import pefile
from capstone import Cs, CS_ARCH_X86, CS_MODE_64

repo = Path('C:/wksp/ProjectRebound')
image = repo/'.tmp/ida-query-assets/ProjectBoundarySteam-Win64-Shipping.exe'
out = repo/'docs/implementation/strict-roster-20260907/evidence/serializer-static-callees-20260908'
sha = lambda p:hashlib.sha256(p.read_bytes()).hexdigest()
expected = '181c49ffb522b3eb01014c84fd9d3a2a5c0b66ae80a6a6addff4bdd6f8125843'
if sha(image) != expected: raise ValueError('Fixed image changed; no RVA reuse permitted')
pe = pefile.PE(str(image),fast_load=True)
base = pe.OPTIONAL_HEADER.ImageBase
if base != 0x140000000: raise ValueError('Unexpected fixed image base')
def call_target(rva):
    raw = pe.get_data(rva,5)
    if raw[0] != 0xE8: raise ValueError('Expected direct native call')
    return rva+5+struct.unpack('<i',raw[1:])[0]
bindings = {'outbound_bunch_constructor':call_target(0x3484ED2),
            'field2_serializer':call_target(0x3484F35),
            'archive_count_bytes':struct.unpack('<Q',pe.get_data(0x5007B00,8))[0]-base,
            'archive_serialize':struct.unpack('<Q',pe.get_data(0x5007C40,8))[0]-base}
if bindings != {'outbound_bunch_constructor':0x31CF3D0,'field2_serializer':0x189D040,
                'archive_count_bytes':0x86AFF0,'archive_serialize':0x19CD9F0}:
    raise ValueError('Call/vtable binding differs from the reviewed image')
disassembler = Cs(CS_ARCH_X86,CS_MODE_64)
functions = []
for name,rva,size in [('outbound_constructor',0x31CF3D0,40),('count_bytes',0x86AFF0,3),
                      ('writer',0x19CD9F0,0x119),('bit_copy',0x19D1A30,596),
                      ('narrow_convert',0x87D8A0,209),('convert_fallback',0x18B3EA0,452)]:
    raw = pe.get_data(rva,size)
    instructions = [{'rva':hex(i.address-base),'instruction':i.mnemonic+' '+i.op_str}
                    for i in disassembler.disasm(raw,base+rva)]
    functions.append({'name':name,'rva':hex(rva),'bytes':len(raw),
                      'sha256':hashlib.sha256(raw).hexdigest(),'instructions':instructions})
bit_copy = next(f for f in functions if f['name']=='bit_copy')
if any(i['instruction'].startswith('call ') for i in bit_copy['instructions']):
    raise ValueError('Bit-copy routine is no longer a leaf')
if pe.get_data(0x86AFF0,3) != bytes.fromhex('c20000'):
    raise ValueError('CountBytes is no longer a no-op return')
record = {'schema_version':1,'observed_at_utc':datetime.now(timezone.utc).isoformat(),
          'mode':'STATIC_FIXED_IMAGE_BINDING','static_binding_status':'VERIFIED',
          'native_game_execution':'NOT_RUN','dynamic_ownership_acceptance':'NOT_RUN',
          'image_path':str(image),'image_sha256':expected,'size_of_image':pe.OPTIONAL_HEADER.SizeOfImage,
          'command':[sys.executable,'-B',str(Path(__file__).resolve())],
          'driver_sha256':sha(Path(__file__)), 'bindings':{k:hex(v) for k,v in bindings.items()},
          'functions':functions,'ida_evidence_sha256':sha(out/'ida-results.json'),
          'scope':'Read-only direct-call/vtable/native-byte verification. Manual dataflow interpretation is recorded separately; no native execution is inferred.'}
(out/'binary-binding.json').write_text(json.dumps(record,indent=2)+'\n',encoding='utf-8')
print(json.dumps({'static_binding':'VERIFIED','image_sha256':expected,'functions':len(functions),'native_game_execution':'NOT_RUN'}))
