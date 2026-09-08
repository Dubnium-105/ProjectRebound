import hashlib
import json
import subprocess
from datetime import datetime, timezone
from pathlib import Path

repo = Path('C:/wksp/ProjectRebound')
sha = lambda p: hashlib.sha256(p.read_bytes()).hexdigest()
stamp = datetime.now(timezone.utc).strftime('%Y%m%dT%H%M%S%fZ')
run = repo/'docs/implementation/strict-roster-20260907/evidence'/('cleanup-receipt-component-'+stamp)
run.mkdir(parents=True, exist_ok=False)
files = ['Payload/Admission/StrictRosterCleanupReceipt.h',
         'Payload/Tests/StrictRosterCleanupReceiptTests.cpp', 'Payload/Tests/CMakeLists.txt']
snapshot = {name: sha(repo/name) for name in files}
cmake = Path('C:/Program Files/Microsoft Visual Studio/18/Community/Common7/IDE/CommonExtensions/Microsoft/CMake/CMake/bin')
commands = [
    ('configure', [str(cmake/'cmake.exe'), '-S', 'Payload/Tests', '-B', 'build/strict-roster-current']),
    ('build', [str(cmake/'cmake.exe'), '--build', 'build/strict-roster-current', '--config', 'Release', '--target', 'strict_roster_cleanup_receipt_tests']),
    ('test', [str(cmake/'ctest.exe'), '--test-dir', 'build/strict-roster-current', '-C', 'Release', '-R', '^strict_roster_cleanup_receipt_tests$', '--output-on-failure']),
]
receipt = {'source_input_commit': subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=repo, text=True).strip(),
           'source_dirty': True, 'source_files': snapshot, 'executions': [], 'started_at': stamp,
           'scope': 'Actual C++ cleanup completion receipt regression only; no game teardown or complete NativeCleared acceptance implied.'}
code = 0
for name, command in commands:
    path = run/(name+'.log')
    with path.open('wb') as stream:
        result = subprocess.run(command, cwd=repo, stdout=stream, stderr=subprocess.STDOUT)
    receipt['executions'].append({'command': command, 'exit_code': result.returncode, 'log_path': str(path), 'sha256': sha(path)})
    code = result.returncode
    if code:
        break
receipt['source_unchanged'] = all(sha(repo/name) == digest for name, digest in snapshot.items())
receipt['finished_at'] = datetime.now(timezone.utc).isoformat()
receipt['status'] = 'PASS_COMPONENT_ONLY' if code == 0 and receipt['source_unchanged'] else 'FAILED'
(run/'receipt.json').write_text(json.dumps(receipt, indent=2)+'\n', encoding='utf-8')
print(json.dumps({'status': receipt['status'], 'receipt_path': str(run/'receipt.json')}))
raise SystemExit(code or (0 if receipt['source_unchanged'] else 1))
