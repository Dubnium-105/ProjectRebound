import hashlib
import json
import shutil
import subprocess
from datetime import datetime, timezone
from pathlib import Path

repo=Path('C:/wksp/ProjectRebound')
sha=lambda path:hashlib.sha256(path.read_bytes()).hexdigest()
stamp=datetime.now(timezone.utc).strftime('%Y%m%dT%H%M%S%fZ')
run=repo/'.tmp/strict-roster-20260907/native-evidence'/('root-validation-'+stamp)
run.mkdir(parents=True,exist_ok=False)
head=subprocess.check_output(['git','rev-parse','HEAD'],cwd=repo,text=True).strip()
tracked=subprocess.check_output(['git','diff','--name-only','HEAD','--','Payload'],cwd=repo,text=True).splitlines()
added=subprocess.check_output(['git','ls-files','--others','--exclude-standard','--','Payload'],cwd=repo,text=True).splitlines()
sources=[]
for name in sorted(set(tracked+added)):
    path=repo/name
    retained=run/'source'/name
    retained.parent.mkdir(parents=True,exist_ok=True)
    shutil.copyfile(path,retained)
    sources.append({'path':name,'sha256':sha(path),'snapshot_path':str(retained)})
manifest={'started_at':datetime.now(timezone.utc).isoformat(),'source_input_commit':head,
          'source_dirty':bool(sources),'source_files':sources,'executions':[],
          'scope':'Actual local C++ compilation and component tests only. Native game admission is a separate execution.'}
cmake_root=Path('C:/Program Files/Microsoft Visual Studio/18/Community/Common7/IDE/CommonExtensions/Microsoft/CMake/CMake/bin')
commands=[
    ('configure',[str(cmake_root/'cmake.exe'),'-S','Payload/Tests','-B','build/strict-roster-current']),
    ('tests-build',[str(cmake_root/'cmake.exe'),'--build','build/strict-roster-current','--config','Release','--parallel','4']),
    ('ctest',[str(cmake_root/'ctest.exe'),'--test-dir','build/strict-roster-current','-C','Release','--output-on-failure']),
    ('payload-build',['C:/Program Files/Microsoft Visual Studio/18/Community/MSBuild/Current/Bin/amd64/MSBuild.exe',
                     'Payload/Payload.vcxproj','/m:1','/nr:false','/p:Configuration=Release','/p:Platform=x64','/v:minimal']),
]
for name,command in commands:
    log=run/(name+'.log')
    with log.open('wb') as stream:
        result=subprocess.run(command,cwd=repo,stdout=stream,stderr=subprocess.STDOUT)
    manifest['executions'].append({'name':name,'command':command,'exit_code':result.returncode,'log_path':str(log),'sha256':sha(log)})
    (run/'result.json').write_text(json.dumps(manifest,indent=2)+'\n',encoding='utf-8')
    print(json.dumps({'name':name,'exit_code':result.returncode,'log_path':str(log)}),flush=True)
    if result.returncode: raise SystemExit(result.returncode)
for item in sources:
    if sha(repo/item['path'])!=item['sha256']: raise ValueError('Source changed during validation: '+item['path'])
payload=repo/'Payload/x64/Release/Payload.dll'
shutil.copyfile(payload,run/'Payload.dll')
manifest['artifact']={'path':str(run/'Payload.dll'),'sha256':sha(run/'Payload.dll'),'bytes':payload.stat().st_size}
symbol_map=payload.with_suffix('.map')
if symbol_map.is_file():
    shutil.copyfile(symbol_map,run/'Payload.map')
    manifest['symbol_map']={'path':str(run/'Payload.map'),'sha256':sha(run/'Payload.map'),'bytes':symbol_map.stat().st_size}
manifest['finished_at']=datetime.now(timezone.utc).isoformat()
manifest['status']='COMPONENT_VALIDATION_COMPLETED_NATIVE_ACCEPTANCE_NOT_RUN'
(run/'result.json').write_text(json.dumps(manifest,indent=2)+'\n',encoding='utf-8')
print(json.dumps({'result_path':str(run/'result.json'),'artifact':manifest['artifact']}),flush=True)
