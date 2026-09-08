import hashlib
import json
import shutil
import subprocess
from datetime import datetime, timezone
from pathlib import Path

repo = Path('C:/wksp/ProjectRebound')
toolbox = Path('C:/wksp/ProjectReboundToolbox')
root = repo/'docs/implementation/strict-roster-20260907'
sha = lambda p: hashlib.sha256(p.read_bytes()).hexdigest()
stamp = datetime.now(timezone.utc).strftime('%Y%m%dT%H%M%S%fZ')
run = root/'evidence'/('cpp-wire-producer-'+stamp)
run.mkdir(parents=True, exist_ok=False)
binary_output = repo/'build/strict-roster-current/Release/command_protocol_tests.exe'
binary = repo/'.tmp/strict-roster-20260907/artifacts/frozen/drivers'/sha(binary_output)/binary_output.name
binary.parent.mkdir(parents=True, exist_ok=True)
if not binary.is_file():
    shutil.copyfile(binary_output, binary)
if sha(binary) != sha(binary_output):
    raise ValueError('Frozen C++ producer differs from the built executable')
positive, negative = run/'payload-wire-v2.jsonl', run/'payload-wire-negative-v2.jsonl'
command = [str(binary), str(positive), str(negative)]
log = run/'generate.log'
with log.open('wb') as stream:
    result = subprocess.run(command, cwd=repo, stdout=stream, stderr=subprocess.STDOUT)
record = {'command': command, 'exit_code': result.returncode,
          'source_input_commit': subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=repo, text=True).strip(),
          'source_dirty': bool(subprocess.check_output(['git', 'status', '--porcelain', '--', 'Payload'], cwd=repo, text=True).strip()),
          'source_sha256': sha(repo/'Payload/Tests/CommandProtocolTests.cpp'),
          'binary_path': str(binary), 'binary_build_output_path': str(binary_output), 'binary_sha256': sha(binary),
          'log_path': str(log), 'log_sha256': sha(log), 'fixtures': [],
          'scope': 'Actual C++ frame producer; synthetic callbacks and fixture consumption only, not game E2E.'}
if result.returncode == 0:
    for source, destination in [(positive, 'payload-wire-fixtures.jsonl'), (negative, 'payload-wire-negative-fixtures.jsonl')]:
        consumer = toolbox/'tests/fixtures'/source.name
        exact = source.read_bytes() == consumer.read_bytes()
        record['fixtures'].append({'path': str(source), 'sha256': sha(source),
            'consumer_path': str(consumer), 'consumer_sha256': sha(consumer), 'exact_consumer_bytes': exact})
        if not exact:
            raise ValueError('Actual producer differs from the currently tested Rust fixture')
        target = root/destination
        if target.is_file() and target.read_bytes() != source.read_bytes():
            history = root/'evidence/wire-fixture-history'/sha(target)/target.name
            history.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(target, history)
        shutil.copyfile(source, target)
(run/'receipt.json').write_text(json.dumps(record, indent=2)+'\n', encoding='utf-8')
print(json.dumps({'receipt_path': str(run/'receipt.json'), 'exit_code': result.returncode}))
raise SystemExit(result.returncode)
