"""Stop only this task's isolated PG/Redis after all real runs have finished."""
import hashlib
import json
import subprocess
from datetime import datetime, timezone
from pathlib import Path

repo = Path('C:/wksp/ProjectRebound')
stamp = datetime.now(timezone.utc).strftime('%Y%m%dT%H%M%SZ')
out = repo/'docs/implementation/strict-roster-20260907/evidence'/('local-lab-restoration-'+stamp)
out.mkdir(parents=True,exist_ok=False)
executions=[]

def run(name, command):
    result=subprocess.run(command,stdout=subprocess.PIPE,stderr=subprocess.PIPE)
    path=out/(name+'.log')
    path.write_bytes(result.stdout+b'\n'+result.stderr)
    executions.append({'name':name,'command':command,'exit_code':result.returncode,
                       'log_path':str(path),'log_sha256':hashlib.sha256(path.read_bytes()).hexdigest()})
    if result.returncode:
        raise RuntimeError(name+' failed; stopping without changing additional resources')
    return result.stdout.decode('utf-8-sig',errors='replace').strip()

status='BLOCKED'
try:
    game=Path('C:/Steam/steamapps/common/Boundary/ProjectBoundary/Binaries/Win64')
    expected={'Payload.dll':'6c7b5e05540ac72a6d7a9fa78f867917285fce00081156ee13d237aa4d6c24a3',
              'steam_appid.txt':'ddfe0e8d462af661f81db36589c39882dc0f2330785b5d80cd34f2f520ad618f'}
    observed={name:hashlib.sha256((game/name).read_bytes()).hexdigest() for name in expected}
    if observed!=expected:
        raise RuntimeError('Original game files are not restored; do not stop the shared lab during a run')
    processes=run('boundary-process-check',['powershell','-NoProfile','-Command',
        "@(Get-Process -Name ProjectBoundarySteam-Win64-Shipping -ErrorAction SilentlyContinue).Count"])
    if processes!='0':
        raise RuntimeError('A Boundary process is still present; shared lab remains running')
    pgbase=['wsl','-e','/usr/bin/psql','-h','127.0.0.1','-p','55439','-U','phanthy','-d','postgres','-At','-c']
    pgdir=run('postgres-owned-directory',pgbase+['SHOW data_directory'])
    if pgdir!='/tmp/rebound-pg-backend-20260907-a':
        raise RuntimeError('Isolated PostgreSQL directory differs; no service is stopped')
    redis=run('redis-owned-server',['wsl','-e','/usr/bin/redis-cli','-h','127.0.0.1','-p','56380','INFO','server'])
    values=dict(line.split(':',1) for line in redis.splitlines() if ':' in line and not line.startswith('#'))
    if values.get('process_id')!='2088' or values.get('tcp_port')!='56380':
        raise RuntimeError('Isolated Redis identity differs; no service is stopped')
    run('stop-owned-postgres',['wsl','-e','/usr/lib/postgresql/14/bin/pg_ctl','-D',pgdir,'-m','fast','-w','-t','15','stop'])
    run('stop-owned-redis',['wsl','-e','/usr/bin/redis-cli','-h','127.0.0.1','-p','56380','SHUTDOWN','NOSAVE'])
    remaining=run('owned-listener-check',['wsl','-e','/usr/bin/ss','-ltnH'])
    if any(':55439 ' in line or ':56380 ' in line for line in remaining.splitlines()):
        raise RuntimeError('An isolated service listener remains')
    status='PASS_RESTORATION_ONLY'
finally:
    receipt={'recorded_at':datetime.now(timezone.utc).isoformat(),'status':status,
             'game_files':locals().get('observed'), 'executions':executions,
             'scope':'Owned local diagnostic resource restoration only; not native cleanup/READY reuse acceptance.',
             'preserved':'PostgreSQL data directory and private fixtures/dumps retained. No database deleted. Original 5432/6379 and unrelated processes were not stopped.'}
    (out/'receipt.json').write_text(json.dumps(receipt,indent=2)+'\n',encoding='utf-8')
    print(json.dumps({'status':status,'receipt':str(out/'receipt.json')}))
