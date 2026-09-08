"""Run the existing strict candidate build and preserve its actual inputs/output."""
import argparse
import hashlib
import json
import subprocess
import shutil
from datetime import datetime, timezone
from pathlib import Path

parser=argparse.ArgumentParser()
parser.add_argument('--payload',type=Path,required=True)
args=parser.parse_args()
repo=Path('C:/wksp/ProjectRebound')
toolbox=Path('C:/wksp/ProjectReboundToolbox')
sha=lambda path:hashlib.sha256(path.read_bytes()).hexdigest()
head=lambda path:subprocess.check_output(['git','rev-parse','HEAD'],cwd=path,text=True).strip()
payload=args.payload.resolve(strict=True)
source_commit=head(toolbox)
source_status=subprocess.check_output(['git','status','--porcelain'],cwd=toolbox,text=True)
if source_status: raise ValueError('Toolbox source contains changes; commit the reviewed inputs first')
stamp=datetime.now(timezone.utc).strftime('%Y%m%dT%H%M%SZ')
run=repo/'.tmp/strict-roster-20260907/artifacts'/('toolbox-'+source_commit[:12]+'-'+stamp)
run.mkdir(parents=True,exist_ok=False)
command=['C:/Users/23587/.cache/codex-runtimes/codex-primary-runtime/dependencies/native/powershell/pwsh.exe','-NoProfile','-ExecutionPolicy','Bypass','-File',
         str(toolbox/'scripts/build-strict-candidate.ps1'),'-PayloadPath',str(payload),
         '-ExpectedPayloadSha256',sha(payload)]
log=run/'build.log'
with log.open('wb') as stream:
    result=subprocess.run(command,cwd=toolbox,stdout=stream,stderr=subprocess.STDOUT)
receipt={'source_commit':source_commit,'source_status_before':source_status,
         'source_commit_after':head(toolbox),
         'source_status_after':subprocess.check_output(['git','status','--porcelain'],cwd=toolbox,text=True),
         'payload_path':str(payload),'payload_sha256_before':command[-1],
         'payload_sha256_after':sha(payload),'command':command,'exit_code':result.returncode,
         'started_at':stamp,'finished_at':datetime.now(timezone.utc).isoformat(),
         'log_path':str(log),'log_sha256':sha(log),'scope':'Actual local build only; not a native, update-signature or gameplay acceptance run.'}
receipt['driver_source']={'path':str(Path(__file__).resolve()),'sha256':sha(Path(__file__))}
receipt['build_script']={'path':command[5],'sha256':sha(Path(command[5]))}
receipt['validation_errors']=[]
if receipt['source_commit_after']!=source_commit or receipt['source_status_after']:
    receipt['validation_errors'].append('Toolbox source changed while building')
if receipt['payload_sha256_after']!=receipt['payload_sha256_before']:
    receipt['validation_errors'].append('Payload changed while building')
candidate=toolbox/'src-tauri/target/release/strict-candidate.json'
if result.returncode==0:
    try:
        retained=run/candidate.name
        shutil.copyfile(candidate,retained)
        value=json.loads(retained.read_text(encoding='utf-8-sig'))
        receipt['candidate_build_manifest']={'path':str(retained),'sha256':sha(retained),'build_output_path':str(candidate)}
        if value.get('source_commit')!=source_commit or value.get('source_dirty') is not False:
            raise ValueError('Candidate manifest source differs from this build')
        if value.get('payload_sha256')!=receipt['payload_sha256_before']:
            raise ValueError('Candidate manifest Payload differs from this build')
        if sha(Path(value['toolbox_path']))!=value['toolbox_sha256']:
            raise ValueError('Candidate Toolbox artifact digest differs')
    except Exception as error:
        receipt['validation_errors'].append(str(error))
receipt['validation_status']='PASS' if result.returncode==0 and not receipt['validation_errors'] else 'FAILED'
(run/'build-receipt.json').write_text(json.dumps(receipt,indent=2)+'\n',encoding='utf-8')
print(json.dumps(receipt))
raise SystemExit(result.returncode or (1 if receipt['validation_errors'] else 0))
