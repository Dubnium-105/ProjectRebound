import hashlib
import json
import shutil
import subprocess
from datetime import datetime, timezone
from pathlib import Path

repo = Path('C:/wksp/ProjectRebound')
toolbox = Path('C:/wksp/ProjectReboundToolbox')
private = toolbox / '.tmp/strict-roster-20260907'
target = repo / 'docs/implementation/strict-roster-20260907/evidence/toolbox-connection-scope-20260908'
target.mkdir(parents=True, exist_ok=True)
sha = lambda p: hashlib.sha256(p.read_bytes()).hexdigest()
read = lambda p: json.loads(p.read_text(encoding='utf-8-sig'))
files = []

def retain(source, relative):
    dest = target / relative
    dest.parent.mkdir(parents=True, exist_ok=True)
    if dest.exists() and dest.read_bytes() != source.read_bytes():
        raise ValueError('Refusing to rewrite retained evidence: ' + str(dest))
    shutil.copyfile(source, dest)
    files.append({'path': dest.relative_to(repo).as_posix(), 'sha256': sha(dest), 'original_path': str(source)})

ownership = private / 'host-ownership-20260908-final'
receipt = read(ownership / 'toolbox-host-ownership-final-receipt.json')
commit = subprocess.check_output(['git', 'rev-parse', 'f724078'], cwd=toolbox, text=True).strip()
raw_diff = subprocess.check_output(['git', 'diff', receipt['source']['head'], commit], cwd=toolbox)
patch = Path(receipt['source']['diff_patch'])
if sha(patch).upper() != receipt['source']['diff_sha256']:
    raise ValueError('Retained tested diff changed')
normalize = lambda data: data.decode('utf-8-sig').replace('\r\n', '\n')
if normalize(raw_diff) != normalize(patch.read_bytes()):
    raise ValueError('Committed source differs from the actual tested patch')
for execution in receipt['tests']:
    if sha(Path(execution['log'])).upper() != execution['sha256']:
        raise ValueError('Actual test log changed')
for run_name in ('wire-scope-20260908-1926', 'host-ownership-20260908-final'):
    for source in sorted((private / run_name).iterdir()):
        if source.is_file() and source.suffix in ('.json', '.log', '.patch'):
            retain(source, run_name + '/' + source.name)
fixture = repo / '.tmp/strict-roster-20260907/native-evidence/wire-scope-20260908-1926'
for source in sorted(fixture.iterdir()):
    if source.is_file() and source.suffix in ('.json', '.jsonl', '.log'):
        retain(source, 'actual-cpp-fixture/' + source.name)
sources = []
for name in subprocess.check_output(['git', 'diff', '--name-only', receipt['source']['head'], commit], cwd=toolbox, text=True).splitlines():
    sources.append({'path': name, 'observed_working_file_sha256_after_test': sha(toolbox / name),
                    'committed_git_blob_sha256': hashlib.sha256(subprocess.check_output(['git','show',commit+':'+name],cwd=toolbox)).hexdigest()})
result = {'recorded_at': datetime.now(timezone.utc).isoformat(), 'product_commit': commit,
          'actual_test_input_head': receipt['source']['head'], 'actual_test_input_dirty': True,
          'tested_patch_sha256': sha(patch), 'committed_diff_raw_sha256': hashlib.sha256(raw_diff).hexdigest(),
          'tested_patch_matches_committed_change_after_crlf_normalization': True,
          'test_scope': '296 Rust library tests and 7 Tauri tests passed. all-targets check and formatting completed. Earlier 294/7 logs are retained as historical results.',
          'limitations': receipt['limitations'], 'native_online_acceptance': 'NOT_PASSED',
          'source_hash_observation': 'Collected after execution and commit; not a claim that the test runner captured these at launch.',
          'sources': sources, 'evidence_files': files}
(target / 'receipt.json').write_text(json.dumps(result, indent=2)+'\n', encoding='utf-8')
print(json.dumps({'receipt': str(target / 'receipt.json'), 'product_commit': commit, 'retained_files': len(files)}))
