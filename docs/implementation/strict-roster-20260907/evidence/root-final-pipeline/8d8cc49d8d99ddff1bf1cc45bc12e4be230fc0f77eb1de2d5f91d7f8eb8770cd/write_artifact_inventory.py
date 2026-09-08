def require(condition, message):
    if not condition:
        raise ValueError(message)

import hashlib
import json
import shutil
import subprocess
import uuid
from datetime import datetime, timezone
from pathlib import Path

repo=Path('C:/wksp/ProjectRebound')
toolbox=Path('C:/wksp/ProjectReboundToolbox')
out=repo/'docs/implementation/strict-roster-20260907'
artifacts=repo/'.tmp/strict-roster-20260907/artifacts'
read=lambda p:json.loads(p.read_text(encoding='utf-8-sig'))
sha=lambda p:hashlib.sha256(p.read_bytes()).hexdigest()
head=lambda p:subprocess.check_output(['git','rev-parse','HEAD'],cwd=p,text=True).strip()
pair={'ProjectRebound':head(repo),'Toolbox':head(toolbox)}
candidate_path=toolbox/'src-tauri/target/release/strict-candidate.json'
candidate=read(candidate_path)
native=read(out/'native-report.json')
native_commit=native['commit']
generation_id=uuid.uuid4().hex
frozen=artifacts/'frozen'

def stable_copy(path):
    digest=sha(path)
    target=frozen/digest/path.name
    target.parent.mkdir(parents=True,exist_ok=True)
    if target.is_file():
        require(sha(target)==digest,'Previously frozen artifact changed: '+str(target))
    else:
        shutil.copyfile(path,target)
    require(sha(target)==digest,'Artifact changed during freeze: '+str(path))
    return target

def retain_build_evidence(receipt_path, receipt, owner):
    # Preserve original execution bytes and paths. The adjacent mapping gives
    # reviewers committed copies without rewriting the historical receipt.
    target = out/'evidence/final-artifact-builds'/owner/sha(receipt_path)
    target.mkdir(parents=True, exist_ok=True)
    sources = [receipt_path, Path(receipt['log_path'])]
    if receipt.get('candidate_build_manifest'):
        sources.append(Path(receipt['candidate_build_manifest']['path']))
    copies = []
    for source in sources:
        destination = target/source.name
        digest = sha(source)
        require(not destination.exists() or sha(destination) == digest, 'Retained build evidence changed')
        if not destination.exists():
            shutil.copyfile(source, destination)
        require(sha(destination) == digest, 'Build evidence changed while retaining')
        copies.append({'original_path': str(source), 'path': str(destination), 'sha256': digest})
    (target/'retained-files.json').write_text(json.dumps({'scope': 'Exact immutable build evidence copies; historical paths remain unchanged.', 'files': copies}, indent=2)+'\n', encoding='utf-8')
    return target/receipt_path.name

toolbox_state=subprocess.check_output(['git','status','--porcelain'],cwd=toolbox,text=True)
require(not toolbox_state,'Toolbox source must be clean while freezing the candidate')
payload_state=subprocess.check_output(['git','status','--porcelain','--','Payload'],cwd=repo,text=True)
require(not payload_state,'Payload source changed since compilation')
toolbox_receipts=[]
for receipt_path in artifacts.glob('toolbox-*/build-receipt.json'):
    value=read(receipt_path)
    if (value.get('source_commit')==pair['Toolbox'] and value.get('exit_code')==0
            and value.get('validation_status')=='PASS'
            and value.get('payload_sha256_before')==candidate['payload_sha256']
            and value.get('candidate_build_manifest',{}).get('sha256')==sha(candidate_path)):
        toolbox_receipts.append((receipt_path,value))
require(bool(toolbox_receipts),'No validated actual Toolbox build receipt for this candidate')
toolbox_receipt_path,toolbox_receipt=max(toolbox_receipts,key=lambda item:item[1]['finished_at'])
require(sha(Path(toolbox_receipt['log_path']))==toolbox_receipt['log_sha256'],'Toolbox build log changed')
require(sha(Path(toolbox_receipt['candidate_build_manifest']['path']))==sha(candidate_path),'Retained Toolbox build manifest changed')
backend_receipts=[]
for receipt_path in artifacts.glob('backend-*/build-receipt.json'):
    value=read(receipt_path)
    if (value.get('source_commit')==pair['ProjectRebound'] and value.get('exit_code')==0
            and value.get('validation_status')=='PASS'):
        backend_receipts.append((receipt_path,value))
require(bool(backend_receipts),'No successful exact-source Backend build receipt')
backend_receipt_path,backend_receipt=max(backend_receipts,key=lambda item:item[1]['finished_at'])
require(sha(Path(backend_receipt['log_path']))==backend_receipt['log_sha256'],'Backend build log changed')
toolbox_receipt_path = retain_build_evidence(toolbox_receipt_path, toolbox_receipt, 'Toolbox')
backend_receipt_path = retain_build_evidence(backend_receipt_path, backend_receipt, 'Backend')
backend_paths={item['role']:Path(item['path']) for item in backend_receipt['artifacts']}
for item in backend_receipt['artifacts']:
    require(sha(Path(item['path']))==item['sha256'],'Backend build output no longer matches its execution receipt')
tree=lambda ref:subprocess.check_output(['git','rev-parse',ref+':Payload'],cwd=repo,text=True).strip()
require(tree(native_commit)==tree(pair['ProjectRebound']), 'Validation failed in write_artifact_inventory.py:20')
require(candidate['source_commit']==pair['Toolbox'] and candidate['source_dirty'] is False, 'Validation failed in write_artifact_inventory.py:21')
require(sha(Path(candidate['payload_path']))==candidate['payload_sha256'], 'Validation failed in write_artifact_inventory.py:22')
require(sha(Path(candidate['toolbox_path']))==candidate['toolbox_sha256'], 'Validation failed in write_artifact_inventory.py:23')
native_payloads=[item for item in native['artifacts'] if item.get('kind')=='Payload.dll_release_current']
native_build=native.get('current_build_execution',{})
require(native_build.get('source_dirty') is False,'Final Payload needs an actual validation/build from committed Payload inputs')
require(native_build.get('source_input_commit')==native_commit,'Native build execution commit differs from the declared Payload source')
require(len(native_payloads)==1, 'Native owner must identify exactly one current compiled Payload')
require(native_payloads[0]['sha256'].lower()==candidate['payload_sha256'].lower(), 'Validation failed in write_artifact_inventory.py:26')
require(sha(Path(native_payloads[0]['path']))==native_payloads[0]['sha256'].lower(), 'Validation failed in write_artifact_inventory.py:27')
require(Path(native_payloads[0]['path']).stat().st_size==native_payloads[0]['bytes'], 'Validation failed in write_artifact_inventory.py:28')

items=[]
for role,path,owner in [
    ('control-plane',backend_paths['control-plane'],'ProjectRebound'),
    ('meta-server',backend_paths['meta-server'],'ProjectRebound'),
    ('edge-relay',backend_paths['edge-relay'],'ProjectRebound'),
    ('Payload',Path(candidate['payload_path']),'ProjectRebound'),
    ('Tauri Toolbox',Path(candidate['toolbox_path']),'Toolbox'),
]:
    require(path.is_file(), 'Validation failed in write_artifact_inventory.py:38')
    original_path=path
    path=stable_copy(path)
    item={'role':role,'path':str(path),'sha256':sha(path),'bytes':path.stat().st_size,
          'build_output_path':str(original_path),
          'source_repository':owner,'source_pair_commit':pair[owner],
          'signature_status':'NOT_RUN','compatibility_status':'BLOCKED',
          'release_eligible':False,'scope':'local candidate; not a signed or native-accepted release'}
    if path.suffix.lower() in ('.dll','.exe'):
        ps="[string](Get-AuthenticodeSignature -LiteralPath '"+str(path).replace("'","''")+"').Status"
        observed=subprocess.check_output(['C:/Users/23587/.cache/codex-runtimes/codex-primary-runtime/dependencies/native/powershell/pwsh.exe','-NoProfile','-Command',ps],text=True).strip()
        item['authenticode_observed_status']=observed
        item['signature_status']='BLOCKED' if observed!='Valid' else 'NOT_RUN'
        item['signature_note']='Observed Authenticode status only; release manifest signing and compatibility acceptance are separate.'
        if role == 'Payload':
            item['build_source_commit']=native_commit
            item['build_execution_source_dirty']=native_build['source_dirty']
            item['build_execution_commands']=native_build['executions']
            item['verified_source_subtree']={'path':'Payload','git_tree':tree(native_commit),'matches_reviewed_source_pair':True}
    else:
        info=subprocess.check_output(['go','version','-m',str(path)],text=True)
        settings={}
        for line in info.splitlines():
            p=line.strip().split(None,1)
            if len(p)==2 and p[0]=='build' and '=' in p[1]:
                k,v=p[1].split('=',1);settings[k]=v
        require(settings.get('vcs.revision')==pair[owner], 'Validation failed in write_artifact_inventory.py:59')
        require(settings.get('vcs.modified')=='false', 'Validation failed in write_artifact_inventory.py:60')
        item['embedded_source_provenance']=settings
    items.append(item)
game=Path('C:/Steam/steamapps/common/Boundary/ProjectBoundary/Binaries/Win64/ProjectBoundarySteam-Win64-Shipping.exe')
require(sha(game)==candidate['game_sha256'], 'Validation failed in write_artifact_inventory.py:64')
original_candidate=stable_copy(candidate_path)
relocated_candidate=dict(candidate)
relocated_candidate.update(
    payload_path=next(item['path'] for item in items if item['role']=='Payload'),
    toolbox_path=next(item['path'] for item in items if item['role']=='Tauri Toolbox'),
    build_manifest_origin={'path':str(original_candidate),'sha256':sha(original_candidate)},
    generation_id=generation_id,
    freeze_note='Paths relocated to retained hash-addressed copies; original build manifest bytes and actual execution source are preserved.')
candidate_path=out/'candidate-build-manifest.json'
candidate_path.write_text(json.dumps(relocated_candidate,ensure_ascii=False,indent=2)+'\n',encoding='utf-8')
record={'schema_version':1,'generated_at':datetime.now(timezone.utc).isoformat(),'generation_id':generation_id,
        'source_commits':pair,'artifacts':items,
        'pinned_game_reference':{'path':str(game),'sha256':sha(game),'bytes':game.stat().st_size},
        'candidate_build_manifest':{'path':str(candidate_path),'sha256':sha(candidate_path)},
        'backend_build_receipt':{'path':str(backend_receipt_path),'sha256':sha(backend_receipt_path)},
        'toolbox_build_receipt':{'path':str(toolbox_receipt_path),'sha256':sha(toolbox_receipt_path)},
        'freeze_source_observations':{'Toolbox_status_porcelain':toolbox_state,'Payload_status_porcelain':payload_state,
            'scope':'Actual source observations during freezing; historical build-time source records are retained separately.'},
        'release_ready':False,
        'note':'The compiled Payload source subtree matches the reviewed source pair; exact native build/freeze/installed restore records are in native-report.json. None of these artifact hashes establishes native admission acceptance.'}
(out/'artifact-inventory.json').write_text(json.dumps(record,ensure_ascii=False,indent=2)+'\n',encoding='utf-8')
print(json.dumps({'artifacts':len(items),'source_commits':pair,'release_ready':False}))
