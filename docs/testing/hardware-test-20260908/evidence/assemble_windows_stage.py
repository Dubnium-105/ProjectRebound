import hashlib
import json
import shutil
import subprocess
from pathlib import Path

root = Path('C:/wksp/ProjectRebound')
toolbox = Path('C:/wksp/ProjectReboundToolbox')
base = root/'.tmp/distribution-test-20260908'
stage = base/'windows-staging'
stage.mkdir(exist_ok=False)

def sha(path):
    return hashlib.file_digest(path.open('rb'), 'sha256').hexdigest()

def copy(source, name):
    target = stage/name
    target.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(source, target)
    assert sha(source) == sha(target)

for name in ['Payload.dll', 'rebound_toolbox_tauri.exe']:
    copy(base/'signed'/name, name)
for name in ['Install-StrictPayload.ps1', 'Restore-StrictPayload.ps1']:
    copy(root/'Tools/Distribution/windows-install'/name, name)
for name in ['Run-Toolbox.cmd', 'payload-manifest.json']:
    copy(base/'toolbox/portable'/name, name)
copy(root/'Tools/Distribution/windows-README.zh-CN.md', 'README.zh-CN.md')
for name in ['Check-ThisMachine.ps1', 'TEST-MATRIX.zh-CN.md']:
    copy(root/'Tools/Distribution'/name, name)
for name in ['Common-WindowsNative.ps1','Preflight-WindowsNative.ps1',
             'Collect-WindowsNative.ps1','Test-WindowsNative.ps1',
             'package-manifest.schema.json','README.md']:
    copy(root/'Tools/Distribution/windows-native'/name, 'native/'+name)
copy(root/'LICENSE.txt', 'licenses/ProjectRebound-LICENSE.txt')
copy(toolbox/'res/runtime/project-rebound-v0.8.4/LICENSE.txt', 'licenses/Embedded-Rebound-LICENSE.txt')
copy(toolbox/'res/vnt/runtime/THIRD-PARTY-NOTICES.txt', 'licenses/VNT-THIRD-PARTY-NOTICES.txt')

assert subprocess.check_output(['git','-C',str(toolbox),'rev-parse','HEAD'],text=True).strip() == 'e360b6f1c0cc6cf6c1e89288b1ef25b306bfe482'
assert subprocess.check_output(['git','-C',str(toolbox),'status','--porcelain'],text=True).strip() == ''
subprocess.run(['git','-C',str(root),'diff','--quiet','dc49017e488e8b5bf6f6544b4428c46d230c0abd','1fbc032665497502252f6245f3f1427d13314566','--','Payload'],check=True)
pins = {
    'Payload.dll': (2096440,'77e255b833e2bf953e67f1b199c731a35ffe76e75415d7b65830a5dd656e0f53'),
    'rebound_toolbox_tauri.exe': (49286456,'5b6be61380a7bd26f0ff203b1117731d1c0786221ca3e45147dae9c49d126b75')
}
for name, (size, digest) in pins.items():
    assert (stage/name).stat().st_size == size and sha(stage/name) == digest
assert pins['Payload.dll'][1].encode('ascii') in (stage/'rebound_toolbox_tauri.exe').read_bytes()
signatures = {}
for label in ['payload','toolbox']:
    receipt = json.loads((base/'signed'/(label+'-signature-receipt.json')).read_text(encoding='utf-8-sig'))
    signatures[label] = {key:receipt[key] for key in ['status','input_sha256','output_sha256','output_bytes',
        'signer_subject','signer_thumbprint','signer_valid_until','authenticode_status',
        'win_verify_trust_hresult','private_key_exported','trust_store_modified','timestamping']}
metadata = {
    'schema_version':1, 'purpose':'hardware-test-only', 'release_ready':False,
    'package_id':'rebound-hardware-test-20260908',
    'source_commits':{'ProjectRebound':'dc49017e488e8b5bf6f6544b4428c46d230c0abd',
                      'Toolbox':'e360b6f1c0cc6cf6c1e89288b1ef25b306bfe482'},
    'source_scope':'ProjectRebound identifies the actual Payload build; its Payload subtree equals the current backend candidate.',
    'backend_candidate_commit':'1fbc032665497502252f6245f3f1427d13314566',
    'backend_deployment_status':'NOT_ESTABLISHED_BY_CLIENT_PACKAGE',
    'service_origins':['https://api.project-rebound.space','https://cnapi.project-rebound.space','https://meta.project-rebound.space'],
    'backend_schema_required':48, 'ipc_protocol':'strict-roster-v2',
    'feature_mode':'Tauri custom-protocol; default product features; no lab-testing',
    'pinned_game':{'filename':'ProjectBoundarySteam-Win64-Shipping.exe','sha256':'181c49ffb522b3eb01014c84fd9d3a2a5c0b66ae80a6a6addff4bdd6f8125843','bytes':102362112},
    'msvc_minimum_version':'14.50.35717',
    'strict_payload_sha256':pins['Payload.dll'][1],
    'required_artifacts':{'windows-x64':[{'path':name,'bytes':size,'sha256':digest} for name,(size,digest) in pins.items()]},
    'signatures':signatures,
    'native_online_acceptance':{'status':'BLOCKED','executed_for_signed_pair':False,
        'reason':'Current Payload reports native_authority_admission_verified=false; ordinary strict online flow cannot complete. No flag or credential bypass is compiled.'},
    'permitted_test_scope':['installation and restoration','package and dependency checks','Toolbox login to existing services','managed startup failure observation'],
    'distribution_kind':'portable diagnostic delta over an existing complete Rebound game runtime',
    'publication':{'updater_published':False,'externally_uploaded':False}
}
(base/'public-metadata.json').write_text(json.dumps(metadata,indent=2)+'\n',encoding='utf-8')
print(json.dumps({'staging':str(stage),'files':sum(p.is_file() for p in stage.rglob('*')),'artifact_pins':pins}))
