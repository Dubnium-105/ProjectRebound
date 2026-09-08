import hashlib,json,subprocess
from datetime import datetime,timezone
from pathlib import Path
repo=Path('C:/wksp/ProjectReboundToolbox')
root=Path('C:/wksp/ProjectRebound/docs/implementation/strict-roster-20260907')
run=root/'evidence'/('strong-id-current-'+datetime.now(timezone.utc).strftime('%Y%m%dT%H%M%SZ'))
run.mkdir(parents=True)
sha=lambda p:hashlib.sha256(p.read_bytes()).hexdigest()
head=subprocess.check_output(['git','rev-parse','HEAD'],cwd=repo,text=True).strip()
assert not subprocess.check_output(['git','status','--porcelain'],cwd=repo,text=True).strip()
command=['C:/Users/23587/.cargo/bin/cargo.exe','test','--locked','--doc','--features','vnt,lab-testing']
log=run/'compile-fail-tests.log'
with log.open('wb') as out: result=subprocess.run(command,cwd=repo,stdout=out,stderr=subprocess.STDOUT)
assert head==subprocess.check_output(['git','rev-parse','HEAD'],cwd=repo,text=True).strip()
assert not subprocess.check_output(['git','status','--porcelain'],cwd=repo,text=True).strip()
receipt={'recorded_at':datetime.now(timezone.utc).isoformat(),'execution_source_pair':{'Toolbox':head},'source_dirty':{'Toolbox':False},
    'source_files':{'src/matchmaking/ids.rs':sha(repo/'src/matchmaking/ids.rs')},'command':command,'exit_code':result.returncode,
    'log_path':str(log),'log_sha256':sha(log),'scope':'Actual Rust compile-fail doctests for typed ID and generation separation. No game/native or final artifact acceptance.'}
(run/'receipt.json').write_text(json.dumps(receipt,indent=2)+'\n',encoding='utf-8')
print(json.dumps({'receipt':str(run/'receipt.json'),'exit_code':result.returncode}))
raise SystemExit(result.returncode)
