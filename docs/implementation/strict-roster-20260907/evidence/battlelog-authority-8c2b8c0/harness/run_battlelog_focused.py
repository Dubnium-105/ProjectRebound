import json,os,subprocess
from datetime import datetime,timezone
from pathlib import Path
repo=Path('C:/wksp/ProjectRebound')
out=repo/'.tmp/e2e21-acl'
env=os.environ.copy()
env['TEST_DATABASE_URL']='postgres://phanthy@172.23.120.17:55439/rebound_backend_restore_authority_20260907_final?sslmode=disable'
env.pop('TEST_META_DATABASE_URL',None)
command=['go','test','./internal/metaserver','-run','^(TestBattleLogSubmissionAgainstPostgreSQL|TestRepositoryIsolationAndRetiredMatchmakingAgainstPostgreSQL)$','-count=1','-v']
stamp=datetime.now(timezone.utc).strftime('%Y%m%d%H%M%S')
p=out/('battlelog-focused-'+stamp+'.log')
with p.open('wb') as f:
    result=subprocess.run(command,cwd=repo/'Backend',env=env,stdout=f,stderr=subprocess.STDOUT)
print(json.dumps({'command':command,'exit_code':result.returncode,'log_path':str(p)}))
print(p.read_text(encoding='utf-8',errors='replace')[-5000:])
raise SystemExit(result.returncode)
