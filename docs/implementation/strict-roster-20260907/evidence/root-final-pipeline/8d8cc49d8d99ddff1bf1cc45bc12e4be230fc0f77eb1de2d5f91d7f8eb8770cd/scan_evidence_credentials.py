import hashlib
import json
import re
import subprocess
from datetime import datetime, timezone
from pathlib import Path

repo=Path('C:/wksp/ProjectRebound')
root=repo/'docs/implementation/strict-roster-20260907'
paths=subprocess.check_output(['rg','--files','--hidden','--no-ignore',str(root)],text=True).splitlines()
patterns={
    'jwt_shaped_value':re.compile(r'\beyJ[A-Za-z0-9_-]{12,}\.[A-Za-z0-9_-]{12,}\.[A-Za-z0-9_-]{16,}\b'),
    'long_ticket_hex':re.compile(r'(?<![0-9A-Fa-f])[0-9A-Fa-f]{256,}(?![0-9A-Fa-f])'),
    'bearer_value':re.compile(r'(?i)Bearer\s+[A-Za-z0-9_.~-]{28,}'),
    'private_key_pem':re.compile(r'-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----'),
    'native_steam_ticket_carrier':re.compile(r'(?i)ReboundSteamTicket=[A-Za-z0-9_-]{16,}'),
}
private_identity=repo/'.tmp/strict-roster-20260907/native-evidence/steam-platform-id-private.txt'
if private_identity.is_file():
    value=private_identity.read_text(encoding='utf-8-sig').strip()
    if re.fullmatch(r'\d{17}',value):
        patterns['private_native_platform_identity']=re.compile(r'(?<!\d)'+re.escape(value)+r'(?!\d)')
private_player_identity=repo/'.tmp/strict-roster-20260907/native-evidence/native-player-id-private.txt'
if private_player_identity.is_file():
    value=private_player_identity.read_text(encoding='utf-8-sig').strip()
    if value:
        patterns['private_native_player_identity']=re.compile(re.escape(value))
findings=[]; scanned=[]

def decoded_views(raw):
    content=raw.decode('utf-16' if raw.startswith((b'\xff\xfe',b'\xfe\xff')) else 'utf-8-sig',errors='replace')
    views={'primary':content}
    if b'\x00' in raw:
        # Windows PowerShell/WSL may concatenate UTF-16 diagnostics and
        # UTF-8 test output without a shared BOM. Scan both representations
        # and the ASCII credential view with NUL separators removed.
        views['nul-stripped']=content.replace('\x00','')
        views['utf-16-le']=raw.decode('utf-16-le',errors='replace')
        views['utf-16-be']=raw.decode('utf-16-be',errors='replace')
    return views

for filename in paths:
    p=Path(filename)
    if p.suffix.lower() not in ('.json','.jsonl','.log','.txt','.md','.ps1','.py','.js','.mjs','.ts','.tsx','.jsx','.sh','.rs','.go','.cpp','.h','.hpp','.toml','.yaml','.yml','.sql','.csv','.tsv','.bat','.cmd','.diff','.patch'): continue
    if p.name=='final-credential-evidence-scan.json': continue
    raw=p.read_bytes()
    scanned.append({'path':str(p),'sha256':hashlib.sha256(raw).hexdigest()})
    for view,content in decoded_views(raw).items():
        for category, pattern in patterns.items():
            for match in pattern.finditer(content):
                findings.append({'path':str(p),'decoded_view':view,'line_in_decoded_view':content.count('\n',0,match.start())+1,
                    'category':category,'matched_characters':len(match.group()),
                    'note':'Candidate only; review without printing or copying the matched value. Mixed-encoding views may identify the same candidate more than once.'})
result={'generated_at':datetime.now(timezone.utc).isoformat(),'status':'REVIEW_REQUIRED' if findings else 'NO_CREDENTIAL_SHAPED_VALUES_FOUND',
    'files_scanned':len(scanned),'candidate_count':len(findings),'candidates':findings,
    'scanned_file_hashes':scanned,'scope':'Conservative pattern scan of selected evidence text. No match is printed. Not a proof against every possible secret format.'}
(root/'final-credential-evidence-scan.json').write_text(json.dumps(result,indent=2)+'\n',encoding='utf-8')
print(json.dumps({key:result[key] for key in ('status','files_scanned','candidate_count','candidates')}))
