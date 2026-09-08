def require(condition, message):
    if not condition:
        raise ValueError(message)

import hashlib
import json
import re
import shutil
import subprocess
from datetime import datetime, timezone
from pathlib import Path

repo=Path('C:/wksp/ProjectRebound')
toolbox=Path('C:/wksp/ProjectReboundToolbox')
root=repo/'docs/implementation/strict-roster-20260907'
sha=lambda p:hashlib.sha256(p.read_bytes()).hexdigest()
pair={name:subprocess.check_output(['git','rev-parse','HEAD'],cwd=p,text=True).strip()
      for name,p in [('ProjectRebound',repo),('Toolbox',toolbox)]}
manifest_path=root/'evidence/import-manifest.json'
previous_manifest=json.loads(manifest_path.read_text(encoding='utf-8-sig')) if manifest_path.is_file() else {}
copies=list(previous_manifest.get('copies',[]))
for prior in copies:
    destination=Path(prior['destination'])
    require(destination.is_file() and sha(destination)==prior['sha256'], 'Previously imported evidence changed: '+str(destination))
mapping={}
supplements = root/'evidence/final-supplements/retained-files.json'
if supplements.is_file():
    for retained in json.loads(supplements.read_text(encoding='utf-8-sig'))['files']:
        target_path = Path(retained['path'])
        require(target_path.is_file() and sha(target_path) == retained['sha256'], 'Retained supplement changed')
        mapping[retained['original_path'].replace('\\','/')] = str(target_path).replace('\\','/')
private_skips=list(previous_manifest.get('private_files_not_imported',[]))
secret_patterns=[
    re.compile(r'\beyJ[A-Za-z0-9_-]{12,}\.[A-Za-z0-9_-]{12,}\.[A-Za-z0-9_-]{16,}\b'),
    re.compile(r'(?<![0-9A-Fa-f])[0-9A-Fa-f]{256,}(?![0-9A-Fa-f])'),
    re.compile(r'(?i)Bearer\s+[A-Za-z0-9_.~-]{28,}'),
    re.compile(r'-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----'),
    re.compile(r'(?i)ReboundSteamTicket=[A-Za-z0-9_-]{16,}'),
]
private_identity=repo/'.tmp/strict-roster-20260907/native-evidence/steam-platform-id-private.txt'
if private_identity.is_file():
    value=private_identity.read_text(encoding='utf-8-sig').strip()
    if re.fullmatch(r'\d{17}',value):
        secret_patterns.append(re.compile(r'(?<!\d)'+re.escape(value)+r'(?!\d)'))
private_player=repo/'.tmp/strict-roster-20260907/native-evidence/native-player-id-private.txt'
if private_player.is_file():
    value=private_player.read_text(encoding='utf-8-sig').strip()
    if value:
        secret_patterns.append(re.compile(re.escape(value)))

def safe_to_import(p):
    if 'private' in p.name.lower():
        private_skips.append({'path':str(p),'reason':'Private fixture remains local; never imported into public evidence.'})
        return False
    raw=p.read_bytes()
    content=raw.decode('utf-16' if raw.startswith((b'\xff\xfe',b'\xfe\xff')) else 'utf-8-sig',errors='replace')
    views=[content]
    if b'\x00' in raw:
        views += [content.replace('\x00',''),raw.decode('utf-16-le',errors='replace'),raw.decode('utf-16-be',errors='replace')]
    if any(pattern.search(view) for view in views for pattern in secret_patterns):
        private_skips.append({'path':str(p),'reason':'Credential-shaped content detected before copying; private original remains local.'})
        return False
    return True
for path in (root/'evidence/frontend').glob('final-front-*.log'):
    mapping[str(toolbox/'.tmp'/path.name).replace('\\','/')]=str(path).replace('\\','/')

def normalize_string(value):
    normalized=value.replace('\\','/')
    if normalized in mapping:
        return mapping[normalized]
    match=re.match(r'^(.*):(\d+)$', normalized)
    filename,line=(match.group(1),match.group(2)) if match else (normalized,None)
    p=Path(filename)
    import_owner=None
    if p.is_relative_to(repo/'.tmp/strict-roster-20260907/native-evidence'):
        import_owner='native'
    elif p.is_relative_to(repo/'Backend/docs/implementation/strict-roster-20260907'):
        import_owner='backend'
    if import_owner and p.is_file() and p.suffix in ('.log','.txt','.json','.jsonl','.md','.js','.py','.ps1'):
        if not safe_to_import(p): return value
        target=root/'evidence/imported'/import_owner/p.name
        target.parent.mkdir(parents=True,exist_ok=True)
        digest=sha(p)
        if target.is_file() and sha(target)!=digest:
            target=target.with_name(digest[:12]+'-'+target.name)
        if not target.is_file():
            shutil.copyfile(p,target)
        require(sha(target)==digest, 'Validation failed in normalize_evidence.py:73')
        copies.append({'source':str(p),'destination':str(target),'sha256':digest})
        result=str(target).replace('\\','/')
        return result+(':'+line if line else '')
    return value

def walk(value, key=None):
    # Commands and original execution inputs are historical facts. Retained
    # evidence paths may be relocated, but the invocation must not be rewritten
    # to imply that a later copied driver was the one that actually ran.
    if key in {'command', 'command_or_harness', 'original_path', 'source_path', 'execution_log_path',
               'working_directory', 'cwd', 'source_sha256', 'source_status_porcelain'}:
        return value
    if isinstance(value,str): return normalize_string(value)
    if isinstance(value,list): return [walk(v, key) for v in value]
    if isinstance(value,dict): return {k:walk(v, k) for k,v in value.items()}
    return value

for owner in ('root','backend','native','toolbox'):
    p=root/(owner+'-report.json')
    report=walk(json.loads(p.read_text(encoding='utf-8-sig')))
    report['reviewed_source_pair']=pair
    report['reviewed_source_pair_scope']='Latest source review anchors only. Historical execution commits, binaries, timestamps and dirty-state observations are retained as originally recorded; unknown per-run provenance is not inferred.'
    report['report_updated_at']=datetime.now(timezone.utc).isoformat()
    report['release_ready']=False
    if owner=='root':
        report['commit_status']='Reviewed implementation committed locally; actual one-client admission/release is recorded separately. Full native Playable/E2E and production release remain blocked. No push or deployment.'
    if owner=='backend':
        report['scope']['commit_status']='Backend authority and integration changes committed locally; exact modification commits are in progress.json. No push or deployment.'
    p.write_text(json.dumps(report,ensure_ascii=False,indent=2)+'\n',encoding='utf-8')

copies=list({(item['source'],item['destination'],item['sha256']):item for item in copies}.values())
private_skips=list({(item['path'],item['reason']):item for item in private_skips}.values())
manifest_path.write_text(json.dumps({'copies':copies,'private_files_not_imported':private_skips,'source_commits':pair},ensure_ascii=False,indent=2)+'\n',encoding='utf-8')
print(json.dumps({'copied_evidence_files':len(copies),'source_commits':pair}))
