import hashlib, json, re, subprocess
from pathlib import Path
from datetime import datetime, timezone
repo = Path('C:/wksp/ProjectRebound')
stamp = datetime.now(timezone.utc).strftime('%Y%m%dT%H%M%SZ')
run = repo/'docs/implementation/strict-roster-20260907/evidence'/('retired-api-contract-'+stamp)
run.mkdir(parents=True)
sha = lambda p: hashlib.sha256(p.read_bytes()).hexdigest()
paths = ['Backend/api/openapi/openapi.yaml', 'Backend/api/openapi/openapi_test.go', 'Backend/internal/controlplane/server.go', 'docs/api/external.md']
sources = {p: sha(repo/p) for p in paths}
command = ['C:/Program Files/Go/bin/go.exe','test','./api/openapi','-count=1','-v']
log = run/'openapi-tests.log'
with log.open('wb') as out:
    result = subprocess.run(command, cwd=repo/'Backend',stdout=out,stderr=subprocess.STDOUT)
yaml = (repo/paths[0]).read_text(encoding='utf-8')
routes = re.findall(r'router\.(Get|Post|Put|Delete)\("([^"]+)", \w+\.RetiredOnlineRoute\)', (repo/paths[2]).read_text())
checked = []
for method, path in routes:
    match = re.search(r'^  '+re.escape(path)+r':\n(.*?)(?=^  /|^components:)',yaml,re.M|re.S)
    assert match, path
    operation = re.search(r'^    '+method.lower()+r':\n(.*?)(?=^    [a-z]+:|\Z)',match[1],re.M|re.S)[1]
    assert 'deprecated: true' in operation and '"410":' in operation and not re.search(r'"2\d\d":',operation), path
    checked.append({'method':method.upper(),'path':path,'deprecated':True,'success_status_advertised':False})
assert len(checked)==17
assert sources == {p:sha(repo/p) for p in paths}, 'Relevant source changed during execution'
receipt = {'recorded_at':datetime.now(timezone.utc).isoformat(), 'source_input_commit':subprocess.check_output(['git','rev-parse','HEAD'],cwd=repo,text=True).strip(),
    'source_dirty':True, 'source_files':sources, 'command':command, 'exit_code':result.returncode,'log_path':str(log),'log_sha256':sha(log),
    'retired_route_contract_checks':checked,'scope':'Actual OpenAPI schema and reference tests plus comparison of 17 registered retired operations. No network/native E2E or operational credential claim.'}
(run/'receipt.json').write_text(json.dumps(receipt,indent=2)+'\n',encoding='utf-8')
print(json.dumps({'receipt':str(run/'receipt.json'),'exit_code':result.returncode,'retired_operations':len(checked)}))
raise SystemExit(result.returncode)
