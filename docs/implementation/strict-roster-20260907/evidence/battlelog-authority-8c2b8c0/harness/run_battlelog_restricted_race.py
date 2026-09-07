import hashlib,json,re,subprocess
from datetime import datetime,timezone
from pathlib import Path
repo=Path('C:/wksp/ProjectRebound')
out=repo/'.tmp/e2e21-acl'
prior=json.loads((Path((out/'latest-run.txt').read_text(encoding='utf-8'))/'result.json').read_text(encoding='utf-8'))
db=prior['postgres_database']; role=prior['postgres_role']
assert re.fullmatch(r'rebound_e2e21_\d{14}',db) and re.fullmatch(r'rebound_meta_e2e21_\d{14}',role)
stamp=datetime.now(timezone.utc).strftime('%Y%m%d%H%M%S')
log=out/('battlelog-restricted-race-'+stamp+'.log')
def role_login(value):
    args=['wsl','-e','psql','-h','127.0.0.1','-p','55439','-U','phanthy','-d',db,'-X','-v','ON_ERROR_STOP=1','-c','ALTER ROLE '+role+' '+value]
    result=subprocess.run(args,stdout=subprocess.PIPE,stderr=subprocess.STDOUT)
    (out/('battlelog-restricted-'+value.lower()+'-'+stamp+'.log')).write_bytes(result.stdout)
    assert result.returncode==0
code=None
command=['wsl','-e','env','TEST_DATABASE_URL=postgres://phanthy@127.0.0.1:55439/'+db+'?sslmode=disable','TEST_META_DATABASE_URL=postgres://'+role+'@127.0.0.1:55439/'+db+'?sslmode=disable','/tmp/rebound-strict-roster-20260907-go1266/go/bin/go','-C','/mnt/c/wksp/ProjectRebound/Backend','test','-race','./internal/metaserver','-run','^TestBattleLogSubmissionAgainstPostgreSQL$','-count=1','-v']
try:
    role_login('LOGIN')
    with log.open('wb') as handle:
        result=subprocess.run(command,stdout=handle,stderr=subprocess.STDOUT)
    code=result.returncode
finally:
    role_login('NOLOGIN')
record={'command':command,'exit_code':code,'log_path':str(log),'sha256':hashlib.sha256(log.read_bytes()).hexdigest(),'scope':'Actual Linux Go race, PostgreSQL admin fixture setup and deployed restricted Meta role. Lab trust authentication; no password-rejection claim.','source_commit':subprocess.check_output(['git','rev-parse','HEAD'],cwd=repo,text=True).strip(),'source_dirty':True,'source_files':{str(repo/p):hashlib.sha256((repo/p).read_bytes()).hexdigest() for p in ['Backend/internal/metaserver/battlelog_repository.go','Backend/internal/metaserver/battlelog_repository_integration_test.go','Backend/internal/metaserver/repository.go','Backend/deployments/control-plane/provision-meta-postgres.sh']}}
(out/('battlelog-restricted-race-'+stamp+'.json')).write_text(json.dumps(record,indent=2)+'\n',encoding='utf-8')
print(json.dumps(record))
raise SystemExit(code)
