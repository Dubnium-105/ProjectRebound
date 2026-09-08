import hashlib
import json
import shutil
import subprocess
from datetime import datetime, timezone
from pathlib import Path

repo = Path('C:/wksp/ProjectRebound')
toolbox = Path('C:/wksp/ProjectReboundToolbox')
source = toolbox / '.tmp/strict-roster-20260907/host-route-preserve-20260908'
producer = repo / '.tmp/strict-roster-20260907/native-evidence/wire-preserved-host-20260908'
target = repo / 'docs/implementation/strict-roster-20260907/evidence/host-route-preservation-20260908'
target.mkdir(parents=True, exist_ok=True)
sha = lambda p: hashlib.sha256(p.read_bytes()).hexdigest()
files = []

def retain(path, relative, expected=None):
    if expected and sha(path) != expected.lower():
        raise ValueError('Source evidence hash changed: ' + str(path))
    destination = target / relative
    destination.parent.mkdir(parents=True, exist_ok=True)
    if destination.exists() and destination.read_bytes() != path.read_bytes():
        raise ValueError('Previously retained evidence changed: ' + str(destination))
    shutil.copyfile(path, destination)
    files.append({'path': destination.relative_to(repo).as_posix(), 'sha256': sha(destination),
                  'bytes': destination.stat().st_size, 'original_path': str(path)})

for name in ('host-route-preserve-receipt.json', 'actual-preserved-host-fixture-receipt.json'):
    path = source / name
    value = json.loads(path.read_text(encoding='utf-8-sig'))
    retain(path, name)
    for execution in value['tests']:
        log = Path(execution['log_path'])
        retain(log, 'toolbox/' + log.name, execution['log_sha256'])
for name in ('receipt.json', 'preserved-host-wire.jsonl', 'command-framework-wire.log'):
    retain(producer / name, 'native-producer/' + name)

wire = producer / 'preserved-host-wire.jsonl'
consumed = toolbox / 'tests/fixtures/preserved-host-wire.jsonl'
if wire.read_bytes() != consumed.read_bytes():
    raise ValueError('Actual producer and Rust input bytes differ')
receipt = {'recorded_at': datetime.now(timezone.utc).isoformat(), 'files': files,
    'reviewed_rebound_commit': subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=repo, text=True).strip(),
    'reviewed_toolbox_commit': subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=toolbox, text=True).strip(),
    'producer_and_consumer_exact_bytes': True, 'wire_sha256': sha(wire),
    'scope': 'Actual component execution only. The earlier Toolbox receipt correctly records the then-missing producer field; the later actual-producer fixture receipt resolves that wire defect. The 309-test suite predates the one additional consumer test; it is not relabeled as a 310-test execution. No native P2P HOST preservation game run is claimed.',
    'native_host_preservation_passed': False, 'release_ready': False}
(target / 'receipt.json').write_text(json.dumps(receipt, indent=2) + '\n', encoding='utf-8')
print(json.dumps({'retained_files': len(files), 'native_host_preservation_passed': False}))
