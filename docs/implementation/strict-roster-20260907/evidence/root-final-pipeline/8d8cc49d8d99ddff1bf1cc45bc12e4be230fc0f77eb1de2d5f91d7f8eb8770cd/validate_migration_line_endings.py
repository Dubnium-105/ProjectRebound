import argparse,hashlib,json,subprocess
from pathlib import Path
from datetime import datetime,timezone
parser=argparse.ArgumentParser()
parser.add_argument('--phase',choices=('before','after'),required=True)
args=parser.parse_args()
repo=Path('C:/wksp/ProjectRebound')
run=repo/'docs/implementation/strict-roster-20260907/evidence'/('migration-line-endings-'+args.phase+'-'+datetime.now(timezone.utc).strftime('%Y%m%dT%H%M%SZ'))
run.mkdir(parents=True)
sha=lambda p:hashlib.sha256(p.read_bytes()).hexdigest()
names=['Backend/internal/database/migrator.go','Backend/internal/database/migrator_test.go']
sources={name:sha(repo/name) for name in names}
command=['C:/Program Files/Go/bin/go.exe','test','./internal/database','-run','^TestMigration(Check|Format)','-count=1','-v']
log=run/'go-test.log'
with log.open('wb') as out: result=subprocess.run(command,cwd=repo/'Backend',stdout=out,stderr=subprocess.STDOUT)
assert sources=={name:sha(repo/name) for name in names}, 'Relevant source changed during execution'
receipt={'phase':args.phase,'recorded_at':datetime.now(timezone.utc).isoformat(),'source_input_commit':subprocess.check_output(['git','rev-parse','HEAD'],cwd=repo,text=True).strip(),
         'source_dirty':True,'source_files':sources,'command':command,'exit_code':result.returncode,'log_path':str(log),'log_sha256':sha(log),
         'scope':'Actual checkout-line-ending regression only; before phase must fail and remains failure evidence. No database restore or game acceptance.'}
(run/'receipt.json').write_text(json.dumps(receipt,indent=2)+'\n',encoding='utf-8')
print(json.dumps({'phase':args.phase,'receipt':str(run/'receipt.json'),'exit_code':result.returncode}))
raise SystemExit(result.returncode)
