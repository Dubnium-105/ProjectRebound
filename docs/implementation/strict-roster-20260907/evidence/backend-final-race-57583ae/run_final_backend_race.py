import hashlib,json,re,subprocess
from datetime import datetime,timezone
from pathlib import Path
repo=Path('C:/wksp/ProjectRebound')
root=repo/'docs/implementation/strict-roster-20260907'
stamp=datetime.now(timezone.utc).strftime('%Y%m%d%H%M%S')
out=repo/'.tmp/final-backend-race'/stamp
out.mkdir(parents=True)
head=subprocess.check_output(['git','rev-parse','HEAD'],cwd=repo,text=True).strip()
tree=subprocess.check_output(['git','rev-parse','HEAD:Backend'],cwd=repo,text=True).strip()
assert subprocess.run(['git','diff','--quiet','HEAD','--','Backend'],cwd=repo).returncode==0
command=['wsl','-e','env','TEST_DATABASE_URL=postgres://phanthy@127.0.0.1:55439/rebound_backend_restore_authority_20260907_final?sslmode=disable','TEST_REDIS_ADDRESS=127.0.0.1:56380','/tmp/rebound-strict-roster-20260907-go1266/go/bin/go','-C','/mnt/c/wksp/ProjectRebound/Backend','test','-race','-p','1','./...','-count=1','-v']
log=out/'backend-go-test-race.log'
with log.open('wb') as f:
    result=subprocess.run(command,stdout=f,stderr=subprocess.STDOUT)
text=log.read_bytes().decode('utf-8',errors='replace').replace('\x00','')
record={'source_commit':head,'backend_tree':tree,'source_scope':'Committed Backend source; unrelated report files may be dirty. Native component fixture uses a different isolated database.','command':command,'exit_code':result.returncode,'pass_count':len(re.findall(r'^--- PASS:',text,re.M)),'subtest_pass_count':len(re.findall(r'^\s+--- PASS:',text,re.M)),'skip_names':re.findall(r'^\s*--- SKIP: ([^\r\n]+)',text,re.M),'fail_names':re.findall(r'^\s*--- FAIL: ([^\r\n]+)',text,re.M),'log_path':str(log),'sha256':hashlib.sha256(log.read_bytes()).hexdigest(),'backend_tree_unchanged':tree==subprocess.check_output(['git','rev-parse','HEAD:Backend'],cwd=repo,text=True).strip() and subprocess.run(['git','diff','--quiet','HEAD','--','Backend'],cwd=repo).returncode==0}
assert record['backend_tree_unchanged']
(out/'result.json').write_text(json.dumps(record,ensure_ascii=False,indent=2)+'\n',encoding='utf-8')
print(json.dumps(record,ensure_ascii=False))
raise SystemExit(result.returncode)
