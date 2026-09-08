import hashlib
import json
import shutil
from datetime import datetime, timezone
from pathlib import Path

repo = Path('C:/wksp/ProjectRebound')
private = repo / '.tmp/strict-roster-20260907/native-evidence'
destination = repo / 'docs/implementation/strict-roster-20260907/evidence/native-serializer-return-20260908'
destination.mkdir(parents=True, exist_ok=True)
digest = lambda p: hashlib.sha256(p.read_bytes()).hexdigest()
files = []

def retain(source, name):
    target = destination / name
    if target.exists() and target.read_bytes() != source.read_bytes():
        raise ValueError('Refusing to overwrite retained evidence: ' + name)
    shutil.copyfile(source, target)
    files.append({'path': target.relative_to(repo).as_posix(), 'sha256': digest(target),
                  'original_path': str(source), 'original_sha256': digest(source)})

retain(private / 'native-serializer-return-abi-20260908.json', 'pinned-executable-disassembly.json')
old = private / 'root-validation-20260907T183905087676Z'
for source in sorted(old.glob('crash-*.json')):
    retain(source, source.name)
build = private / 'root-validation-20260907T190853926682Z'
retain(build / 'result.json', 'actual-candidate-build.json')
for name in ('configure.log', 'tests-build.log', 'ctest.log', 'payload-build.log'):
    retain(build / name, name)

runtime = []
for run_id in ('dynamic-root-20260907T191025704743Z', 'dynamic-root-20260907T191237095397Z'):
    run = private / 'live-run-results' / run_id
    summary = next(run.glob('dedicated-component-*/summary.json'))
    frames = summary.parent / 'payload-frames.jsonl'
    observations = []
    for line in frames.read_text(encoding='utf-8-sig').splitlines():
        item = json.loads(line)
        if item.get('label') == 'client_join' or (item.get('label') == 'client_login_ready_poll'
                and item.get('match_login_completed') and item.get('match_login_ready')):
            observations.append(item)
    data = json.loads(summary.read_text(encoding='utf-8-sig'))
    runtime.append({'run_id': run_id, 'actual_outcome': data['outcome'],
                    'failure': data.get('failure'), 'summary_sha256': digest(summary),
                    'original_frames_sha256': digest(frames), 'selected_actual_frames': observations,
                    'restored_payload_sha256': data.get('restored_payload_sha256')})

report = {'recorded_at': datetime.now(timezone.utc).isoformat(),
          'finding': 'Pinned FString serializer returns FArchive&; the previous void detour lost RAX and crashed ordinary chained serialization before client login.',
          'change': 'Preserve the native pointer return through all forwarding paths; gate the pinned return bytes in addition to the existing callsite/prologue checks.',
          'validation_scope': '23/23 CTest and actual DLL build succeeded. Two real cold clients subsequently completed native login and accepted a scoped nonempty-Grant join without the prior serializer crash. Full native admission did not pass.',
          'candidate_sha256': '70a0f52d246e14bd243723b4e9cc0173408904e94355178bec003b01d51bbb40',
          'release_ready': False, 'evidence_files': files, 'real_native_runs': runtime}
(destination / 'receipt.json').write_text(json.dumps(report, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')
print(json.dumps({'receipt': str(destination / 'receipt.json'), 'retained_files': len(files), 'native_acceptance_passed': False}))
