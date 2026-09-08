"""Record the observed first staging failure without changing evidence bytes."""
import hashlib
import json
import subprocess
from datetime import datetime, timezone
from pathlib import Path

repo = Path('C:/wksp/ProjectRebound')
scope = repo/'docs/implementation/strict-roster-20260907'
name = 'docs/implementation/strict-roster-20260907/baseline.json'
working = (repo/name).read_bytes()
indexed = subprocess.check_output(['git','show',':'+name],cwd=repo)
if working == indexed:
    raise ValueError('The original byte mismatch is no longer observable')
manifest_path = scope/'final-staging-manifest.json'
manifest = json.loads(manifest_path.read_text(encoding='utf-8'))
expected = next(item for item in manifest['files'] if item['path']==name)
sha = lambda data:hashlib.sha256(data).hexdigest()
if expected['sha256'] != sha(working):
    raise ValueError('Working file changed since the failed staging invocation')
out = scope/'evidence/staging-byte-mismatch-20260908'
out.mkdir(parents=True,exist_ok=False)
receipt = {
    'recorded_at':datetime.now(timezone.utc).isoformat(),
    'command':'C:/Python314/python.exe -B -X utf8 .tmp/strict-roster-20260907/stage_final_evidence.py',
    'observed_exit_code':1,
    'observed_error':'Index bytes differ from pre-staging evidence: '+name,
    'status':'FAILED_BYTE_BINDING',
    'path':name,
    'pre_staging_manifest_sha256':sha(manifest_path.read_bytes()),
    'working_sha256':sha(working), 'index_sha256':sha(indexed),
    'working_bytes':len(working), 'index_bytes':len(indexed),
    'same_when_crlf_is_normalized':working.replace(b'\r\n',b'\n')==indexed,
    'fix':'Force git add --renormalize under the existing -text rule after adding paths, then repeat the complete scanned-byte/index comparison.',
    'scope':'Evidence staging failure only. No product/native test or release acceptance claim.'
}
(out/'receipt.json').write_text(json.dumps(receipt,indent=2)+'\n',encoding='utf-8')
print(json.dumps({'recorded':str(out/'receipt.json'),'working_file_modified':False}))
