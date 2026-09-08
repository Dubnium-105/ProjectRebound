import hashlib, json, shutil, subprocess, sys
from datetime import datetime, timezone
from pathlib import Path

repo = Path('C:/wksp/ProjectRebound')
toolbox = Path('C:/wksp/ProjectReboundToolbox')
root = repo/'docs/implementation/strict-roster-20260907'
stamp = datetime.now(timezone.utc).strftime('%Y%m%dT%H%M%SZ')
out = root/'evidence'/('auth-http-current-'+stamp)
out.mkdir(parents=True, exist_ok=False)
sha = lambda p: hashlib.sha256(p.read_bytes()).hexdigest()
head = lambda p: subprocess.check_output(['git','rev-parse','HEAD'],cwd=p,text=True).strip()
pair = {'ProjectRebound':head(repo),'Toolbox':head(toolbox)}
if subprocess.check_output(['git','status','--porcelain'],cwd=toolbox,text=True):
    raise ValueError('Toolbox source is not clean')
commands = []
def run(name, command):
    result = subprocess.run(command, cwd=toolbox, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    path = out/(name+'.log')
    path.write_bytes(result.stdout)
    commands.append({'command':command,'exit_code':result.returncode,'log_path':str(path),'log_sha256':sha(path)})
    if result.returncode:
        write_receipt()
        raise SystemExit(result.returncode)
    return path
def write_receipt():
    record = {'reviewed_source_pair':pair, 'execution_source_pair':{'Toolbox':pair['Toolbox']},
      'source_dirty':{'Toolbox':False},
      'source_scope':'Toolbox HTTP helper built from its clean commit. ProjectRebound is a review anchor only and is not compiled or exercised by this Toolbox-only test.',
      'execution_repository':'Toolbox', 'source_commit':pair['Toolbox'],
      'commands':commands, 'finished_at':datetime.now(timezone.utc).isoformat(),
      'scope':'Actual loopback HTTP and isolated DPAPI temporary configuration; synthetic credentials only. No native gameplay or full E2E-11 acceptance.'}
    # A single-repository invocation must not claim that the other repository
    # was executed or clean; the complete pair is context, not proof.
    if 'binary' in globals(): record['driver'] = {'path':str(binary),'sha256':sha(binary)}
    (out/'receipt.json').write_text(json.dumps(record,indent=2)+'\n',encoding='utf-8')
    return record
run('build',['cargo','build','--locked','--features','lab-testing','--bin','auth_http_concurrency'])
original = toolbox/'target/debug/auth_http_concurrency.exe'
binary = repo/'.tmp/strict-roster-20260907/artifacts/frozen/drivers'/sha(original)/original.name
binary.parent.mkdir(parents=True,exist_ok=True)
if binary.exists() and sha(binary) != sha(original): raise ValueError('Frozen HTTP driver changed')
if not binary.exists(): shutil.copyfile(original,binary)
log = run('http-cases',[sys.executable,'-B',str(toolbox/'tests/auth_http_concurrency.py'),'--driver',str(binary)])
result = json.loads(log.read_text(encoding='utf-8-sig'))
if not result.get('results') or any(x.get('status') != 'PASS' or x.get('driver_exit_code') != 0 for x in result['results']):
    write_receipt()
    raise ValueError('HTTP scenario did not pass')
record = write_receipt()
print(json.dumps({'receipt':str(out/'receipt.json'),'cases':len(result['results']),'exit_codes':[x['exit_code'] for x in commands]}))
