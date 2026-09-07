import hashlib
import json
import os
import re
import subprocess
import sys
from datetime import datetime, timezone
from pathlib import Path

repo = Path('C:/wksp/ProjectRebound')
work = repo/'.tmp/e2e21-acl'
stamp = datetime.now(timezone.utc).strftime('%Y%m%d%H%M%S')
run_root = work/stamp
run_root.mkdir()
pg_database = 'rebound_e2e21_'+stamp
pg_role = 'rebound_meta_e2e21_'+stamp
redis_user = 'rebound-meta-e2e21-'+stamp
fixture_password = 'E2e21-ephemeral-fixture'
records = []
sha = lambda path: hashlib.sha256(path.read_bytes()).hexdigest()
linux = lambda path: '/mnt/'+path.drive[0].lower()+str(path).replace('\\','/')[2:]
redis_cli = '/tmp/rebound-e2e21-redis7411-20260907/redis-7.4.11/src/redis-cli'
redis_server = '/tmp/rebound-e2e21-redis7411-20260907/redis-7.4.11/src/redis-server'

def execute(name, argv, stdin=None, expect=0):
    result = subprocess.run(['wsl','-e',*argv], input=stdin, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    path = run_root/(name+'.log')
    path.write_bytes(result.stdout)
    # WSL emits an unrelated UTF-16 proxy warning before ordinary UTF-8 output.
    text = result.stdout.decode('utf-8', errors='replace').replace('\x00','')
    displayed = ['<synthetic-fixture-password>' if fixture_password in arg else arg for arg in argv]
    record = {'name':name,'command':['wsl','-e',*displayed],'exit_code':result.returncode,
              'expected_exit_code':expect,'log_path':str(path),'sha256':sha(path)}
    records.append(record)
    print(json.dumps({'name':name,'exit_code':result.returncode,'log_path':str(path)},ensure_ascii=False), flush=True)
    if expect is not None and result.returncode != expect:
        raise RuntimeError('unexpected exit for '+name)
    return text, record

def psql(name, sql, role='phanthy', expect=0):
    return execute(name, ['psql','-h','127.0.0.1','-p','55439','-U',role,'-d',pg_database,
                          '-X','-A','-t','-v','ON_ERROR_STOP=1','-c',sql], expect=expect)

def provision(name, source):
    normalized = run_root/(name+'.sh')
    normalized.write_bytes(source.read_bytes().replace(b'\r\n',b'\n'))
    execute(name, ['env','PGPORT=55439','POSTGRES_HOST=127.0.0.1','POSTGRES_USER=phanthy',
                  'POSTGRES_PASSWORD='+fixture_password,'POSTGRES_DB='+pg_database,
                  'META_POSTGRES_USER='+pg_role,'META_POSTGRES_PASSWORD='+fixture_password,
                  'sh',linux(normalized)])

def apply_acl(name, compose):
    source = compose.read_text(encoding='utf-8-sig')
    block = source.split('ACL SETUSER',1)[1].split('depends_on:',1)[0]
    commands = re.findall(r'"(\+[^\"]+)"',block)
    assert '+@connection' in commands and '~meta:*' in block
    text, record = execute(name, [redis_cli,'-p','56384','--raw','ACL','SETUSER',redis_user,
                                 'reset','on','>'+fixture_password,'~meta:*',*commands])
    assert text.rstrip().endswith('OK'), text
    record['extracted_acl_commands'] = commands
    record['source_path'] = str(compose)
    record['source_sha256'] = sha(compose)

success = False
owned_redis_started = False
try:
    execute('create-isolated-database',['createdb','-h','127.0.0.1','-p','55439','-U','phanthy',pg_database])
    dump = repo/'docs/implementation/strict-roster-20260907/evidence/backend-schema47-source-20260907-authority-final.dump'
    execute('restore-schema47-fixture',['pg_restore','-h','127.0.0.1','-p','55439','-U','phanthy',
                                      '-d',pg_database,'--no-owner','--no-privileges','--exit-on-error',linux(dump)])
    psql('restored-schema', 'SELECT count(*),max(version) FROM schema_migrations')
    provision('baseline-provision',work/'baseline-provision-meta-postgres.sh')
    _, record = psql('baseline-schema-identities-denied','SELECT version,name,checksum FROM schema_migrations LIMIT 0',role=pg_role,expect=1)
    record['observed'] = 'Historical Meta role cannot read the columns required by actual VerifyCurrent.'
    _, record = psql('baseline-public-create-allowed','BEGIN; CREATE TABLE public.rebound_meta_forbidden_ddl_probe(id integer); ROLLBACK;',role=pg_role)
    record['observed'] = 'Historical direct REVOKE does not remove PUBLIC CREATE; test DDL succeeded and rolled back.'
    # This Redis instance is exclusively owned by this harness; no existing
    # 56380 instance or its global ACL/script cache is touched.
    execute('assert-redis-port-free',['python3','-c','import socket; s=socket.socket(); s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1); s.bind(("127.0.0.1",56384)); s.close()'])
    execute('start-owned-redis7411',[redis_server,'--bind','127.0.0.1','--port','56384','--save','',
                                    '--appendonly','no','--daemonize','yes','--pidfile',linux(run_root/'redis.pid'),
                                    '--logfile',linux(run_root/'redis-server.log')])
    owned_redis_started = True
    owned_redis_pid = int((run_root/'redis.pid').read_text().strip())
    execute('owned-redis-process-identity',['python3','-c',
        'import os,json; p='+repr(str(owned_redis_pid))+'; executable=os.readlink("/proc/"+p+"/exe"); '
        'print(json.dumps({"pid":int(p),"executable":executable})); assert executable=='+repr(redis_server)])
    apply_acl('baseline-compose-acl',work/'baseline-compose.yaml')
    text, record = execute('baseline-lua-consumption-denied',[redis_cli,'-p','56384','--raw','--user',redis_user,
                                    '-a',fixture_password,'--no-auth-warning','EVAL',
                                    'return redis.call("GET",KEYS[1])','1','meta:gate:e2e21-probe'])
    assert 'NOPERM' in text, text
    record['observed'] = 'Actual Redis 7.4.11 refuses Lua under the deployed historical ACL. redis-cli exit 0 is not application success.'
    provision('fixed-provision',repo/'Backend/deployments/control-plane/provision-meta-postgres.sh')
    apply_acl('fixed-compose-acl',repo/'Backend/deployments/control-plane/docker-compose.yaml')
    # Compile and run the product test with the real restricted credentials.
    text, record = execute('restricted-product-permissions-test',[
        'env','TEST_META_DATABASE_URL=postgres://'+pg_role+':'+fixture_password+'@127.0.0.1:55439/'+pg_database+'?sslmode=disable',
        'TEST_META_REDIS_ADDRESS=127.0.0.1:56384','TEST_META_REDIS_USERNAME='+redis_user,
        'TEST_META_REDIS_PASSWORD='+fixture_password,
        '/tmp/rebound-strict-roster-20260907-go1266/go/bin/go','-C','/mnt/c/wksp/ProjectRebound/Backend',
        'test','./internal/metaserver','-run','^TestRestrictedMetaServicePermissions$','-count=1','-v'],expect=None)
    assert record['exit_code']==0 and '--- PASS: TestRestrictedMetaServicePermissions' in text and '--- SKIP:' not in text, 'restricted product test failed'
    if os.environ.get('RUN_RESTRICTED_BATTLELOG') == '1':
        fixture_query = "SELECT count(*) FROM players WHERE id LIKE 'battlelog_player_%'"
        before, _ = psql('battlelog-owned-fixtures-before', fixture_query)
        text, record = execute('restricted-battlelog-live-test',[
            'env','TEST_DATABASE_URL=postgres://phanthy@127.0.0.1:55439/'+pg_database+'?sslmode=disable',
            'TEST_META_DATABASE_URL=postgres://'+pg_role+':'+fixture_password+'@127.0.0.1:55439/'+pg_database+'?sslmode=disable',
            '/tmp/rebound-strict-roster-20260907-go1266/go/bin/go','-C','/mnt/c/wksp/ProjectRebound/Backend',
            'test','./internal/metaserver','-run','^TestBattleLogSubmissionAgainstPostgreSQL$','-count=1','-v'],expect=None)
        assert record['exit_code']==0 and '--- PASS: TestBattleLogSubmissionAgainstPostgreSQL' in text and '--- SKIP:' not in text, 'restricted battlelog test failed'
        after, _ = psql('battlelog-owned-fixtures-after', fixture_query)
        assert before.splitlines()[-1] == after.splitlines()[-1], 'owned battlelog fixtures were not reclaimed'
    success = True
finally:
    if owned_redis_started:
        execute('stop-owned-redis',[redis_cli,'-p','56384','SHUTDOWN','NOSAVE'],expect=None)
        _, cleanup = execute('assert-owned-redis-stopped',['python3','-c',
            'import socket,os,json; p='+repr(str(owned_redis_pid))+'; s=socket.socket(); s.settimeout(2); '
            'refused=s.connect_ex(("127.0.0.1",56384))!=0; s.close(); '
            'alive=os.path.exists("/proc/"+p+"/exe"); print(json.dumps({"pid":int(p),"executable_alive":alive,"listener_refused":refused})); '
            'assert refused and not alive'],expect=None)
        success = success and cleanup['exit_code']==0
    # Preserve the isolated database as evidence, but leave no live test login.
    psql('disable-owned-meta-role','ALTER ROLE '+pg_role+' NOLOGIN',expect=None)
    result = {'status':'PASS' if success else 'FAIL','scope':'Real PostgreSQL/Redis deployment permission component; full E2E-21 native/player/node/admin matrix remains separate.',
              'generated_at':datetime.now(timezone.utc).isoformat(),'postgres_database':pg_database,'postgres_role':pg_role,
              'postgres_authentication_scope':'Isolated lab uses trust authentication; role privileges were tested, password rejection was not.',
              'redis_version':'7.4.11','redis_user':redis_user,'redis_address':'127.0.0.1:56384','records':records,
              'source_commit':subprocess.check_output(['git','rev-parse','HEAD'],cwd=repo,text=True).strip(),
              'source_dirty':True,'source_files':{str(p):sha(p) for p in [
                  repo/'Backend/deployments/control-plane/provision-meta-postgres.sh',
                  repo/'Backend/deployments/control-plane/docker-compose.yaml',
                  repo/'Backend/internal/metaserver/permissions_integration_test.go',
                  repo/'Backend/internal/metaserver/battlelog_repository.go',
                  repo/'Backend/internal/metaserver/battlelog_repository_integration_test.go',
                  repo/'Backend/internal/metaserver/repository.go']}}
    (run_root/'result.json').write_text(json.dumps(result,ensure_ascii=False,indent=2)+'\n',encoding='utf-8')
    (work/'latest-run.txt').write_text(str(run_root),encoding='utf-8')
    print(json.dumps({'status':result['status'],'result_path':str(run_root/'result.json')},ensure_ascii=False))
sys.exit(0 if success else 1)
