import hashlib
import json
import re
import shutil
from datetime import datetime, timezone
from pathlib import Path

repo = Path('C:/wksp/ProjectRebound')
private = repo / '.tmp/strict-roster-20260907/native-evidence'
target = repo / 'docs/implementation/strict-roster-20260907/evidence/native-identity-20260908'
target.mkdir(parents=True, exist_ok=True)
sha = lambda p: hashlib.sha256(p.read_bytes()).hexdigest()
secrets = [(private / n).read_text(encoding='utf-8-sig').strip() for n in
           ('steam-platform-id-private.txt', 'native-player-id-private.txt')]
files = []
for original, name in (
    ('identity-abi-prelogin-sret-20260908.json', 'fixed-image-identity-abi.json'),
    ('native-player-id-private.provenance.json', 'authenticated-player-fixture-provenance.json'),
    ('real-player-driver-tests-20260908.log', 'private-driver-component-tests.log'),
    ('native-netmode-fixed-image-20260908.json', 'fixed-image-world-netmode.json'),
):
    source = private / original
    raw = source.read_bytes()
    content = raw.decode('utf-8-sig')
    if any(value and value in content for value in secrets) or re.search(
        r'\beyJ[A-Za-z0-9_-]{12,}\.[A-Za-z0-9_-]{12,}\.[A-Za-z0-9_-]{16,}\b|-----BEGIN .*PRIVATE KEY-----', content):
        raise ValueError('Evidence contains a private identity or credential: ' + original)
    destination = target / name
    if destination.exists() and destination.read_bytes() != raw:
        raise ValueError('Previously retained evidence changed: ' + name)
    shutil.copyfile(source, destination)
    files.append({'path': destination.relative_to(repo).as_posix(), 'sha256': sha(destination),
                  'bytes': len(raw), 'original_path': str(source)})
log = (target / 'private-driver-component-tests.log').read_text(encoding='utf-8-sig')
top = re.findall(r'^--- PASS:', log, re.M)
sub = re.findall(r'^\s+--- PASS:', log, re.M)
if len(top) != 11 or len(sub) != 6 or re.search(r'--- (FAIL|SKIP):|^FAIL\b', log, re.M):
    raise ValueError('Private driver execution results differ from the observed 11 top / 6 sub PASS')
receipt = {'recorded_at': datetime.now(timezone.utc).isoformat(), 'files': files,
    'private_native_player_identity': {'length': len(secrets[1]),
        'sha256': hashlib.sha256(secrets[1].encode()).hexdigest()},
    'private_driver_test_top_passes': len(top), 'private_driver_test_sub_passes': len(sub),
    'scope': 'Actual authenticated identity provenance, fixed-image ABI analysis, and private driver component execution only. Runtime admission results are separately retained in native-scope-runtime-20260908. No private identity or credential is copied.',
    'native_admission_passed': False, 'release_ready': False}
(target / 'receipt.json').write_text(json.dumps(receipt, indent=2) + '\n', encoding='utf-8')
print(json.dumps({'retained_files': len(files), 'native_admission_passed': False}))
