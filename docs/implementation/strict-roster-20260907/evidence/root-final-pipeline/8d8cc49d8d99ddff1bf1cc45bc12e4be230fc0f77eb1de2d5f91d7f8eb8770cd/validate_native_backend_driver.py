import hashlib
import json
import shutil
import subprocess
from datetime import datetime, timezone
from pathlib import Path

repo=Path('C:/wksp/ProjectRebound')
tag='driver-tests-'+datetime.now(timezone.utc).strftime('%Y%m%dT%H%M%S%fZ')
run=repo/'.tmp/strict-roster-20260907/native-evidence'/tag
run.mkdir(parents=True,exist_ok=False)
source=repo/'Backend/.tmp-native-fixture'/tag
source.mkdir(parents=True,exist_ok=False)
sha=lambda p:hashlib.sha256(p.read_bytes()).hexdigest()
sources=[]
for name in ('main.go','main_test.go'):
    original=repo/'Backend/.tmp-native-fixture'/name
    shutil.copyfile(original,source/name)
    shutil.copyfile(original,run/name)
    sources.append({'path':str(source/name),'snapshot_path':str(run/name),'sha256':sha(original)})
command=['C:/Program Files/Go/bin/go.exe','test','./.tmp-native-fixture/'+tag,'-count=1','-v']
log=run/'go-test.log'
with log.open('wb') as stream:
    result=subprocess.run(command,cwd=repo/'Backend',stdout=stream,stderr=subprocess.STDOUT)
report={'source_files':sources,'command':command,'exit_code':result.returncode,'log_path':str(log),'log_sha256':sha(log),
        'scope':'Private fixture driver component tests only; no real Native admission executed.',
        'observed_at':datetime.now(timezone.utc).isoformat()}
(run/'receipt.json').write_text(json.dumps(report,indent=2)+'\n',encoding='utf-8')
print(json.dumps({'receipt':str(run/'receipt.json'),'exit_code':result.returncode}))
raise SystemExit(result.returncode)
