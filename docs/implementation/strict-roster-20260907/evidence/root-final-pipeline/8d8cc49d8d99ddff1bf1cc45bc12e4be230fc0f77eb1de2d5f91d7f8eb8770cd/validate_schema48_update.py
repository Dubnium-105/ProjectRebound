import hashlib
import json
import shutil
import subprocess
from datetime import datetime, timezone
from pathlib import Path

repo = Path('C:/wksp/ProjectRebound')
stamp = datetime.now(timezone.utc).strftime('%Y%m%dT%H%M%SZ')
run = repo / 'docs/implementation/strict-roster-20260907/evidence' / ('schema48-update-' + stamp)
run.mkdir(parents=True, exist_ok=False)
sha = lambda p: hashlib.sha256(p.read_bytes()).hexdigest()
names = ['Backend/internal/update/service.go', 'Backend/internal/update/service_test.go',
         'Backend/internal/update/strict_online_fixture_test.go', 'Backend/api/fixtures/strict-online-update-v1.json']
sources = [{'path': name, 'sha256': sha(repo/name)} for name in names]
command = ['C:/Program Files/Go/bin/go.exe', 'test', './internal/update', '-count=1', '-v']
log = run / 'go-test.log'
with log.open('wb') as stream:
    result = subprocess.run(command, cwd=repo/'Backend', stdout=stream, stderr=subprocess.STDOUT)
for item in sources:
    if sha(repo/item['path']) != item['sha256']:
        raise ValueError('Source changed during update validation: ' + item['path'])
generation_log = repo/'.tmp/strict-roster-20260907/schema48-update-fixture-generation.log'
shutil.copyfile(generation_log, run/generation_log.name)
receipt = {'recorded_at': datetime.now(timezone.utc).isoformat(), 'command': command,
           'exit_code': result.returncode, 'source_input_commit': subprocess.check_output(
               ['git', 'rev-parse', 'HEAD'], cwd=repo, text=True).strip(), 'source_dirty': True,
           'source_files': sources, 'log_path': str(log), 'log_sha256': sha(log),
           'fixture_generation_log': {'path': str(run/generation_log.name), 'sha256': sha(generation_log)},
           'scope': 'Actual update validation and public-test-key Go manifest signing only. Rejects schema 47; no operational signing or release acceptance.'}
(run/'receipt.json').write_text(json.dumps(receipt, indent=2)+'\n', encoding='utf-8')
print(json.dumps({'receipt': str(run/'receipt.json'), 'exit_code': result.returncode}))
raise SystemExit(result.returncode)
