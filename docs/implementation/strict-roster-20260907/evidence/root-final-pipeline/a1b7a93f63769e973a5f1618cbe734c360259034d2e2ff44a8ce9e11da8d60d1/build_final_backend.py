def require(condition, message):
    if not condition:
        raise ValueError(message)

"""Build the reviewed Backend from a clean, independent checkout; retain each run."""
import hashlib
import json
import os
import subprocess
from datetime import datetime, timezone
from pathlib import Path

repo = Path('C:/wksp/ProjectRebound')
checkout = Path('C:/Users/23587/AppData/Local/Temp/rebound-source-clone-20260907')
base = repo/'.tmp/strict-roster-20260907/artifacts'
go = 'C:/Program Files/Go/bin/go.exe'
sha = lambda path: hashlib.sha256(path.read_bytes()).hexdigest()
def git(cwd, *args):
    return subprocess.check_output(['git', *args], cwd=cwd, text=True).strip()

expected = git(repo, 'rev-parse', 'HEAD')
backend_changes = git(repo, 'status', '--porcelain', '--', 'Backend').splitlines()
require(all(line == '?? Backend/.tmp-native-fixture/' for line in backend_changes), 'Backend contains uncommitted product inputs')
require(not git(checkout, 'status', '--porcelain'), 'Do not overwrite changes in the isolated checkout')
subprocess.run(['git', 'fetch', str(repo), expected], cwd=checkout, check=True)
subprocess.run(['git', 'checkout', '--detach', expected], cwd=checkout, check=True)
require(git(checkout, 'rev-parse', 'HEAD') == expected, 'Validation failed in build_final_backend.py:22')
require(not git(checkout, 'status', '--porcelain'), 'Validation failed in build_final_backend.py:23')
stamp = datetime.now(timezone.utc).strftime('%Y%m%dT%H%M%SZ')
run = base/('backend-'+expected[:12]+'-'+stamp)
run.mkdir(parents=True, exist_ok=False)
environment = os.environ.copy()
environment.update(GOOS='linux', GOARCH='amd64', CGO_ENABLED='0')
command = [go, 'build', '-buildvcs=true', '-trimpath', '-o', str(run)+os.sep,
           './cmd/control-plane', './cmd/meta-server', './cmd/edge-relay']
log = run/'build.log'
with log.open('wb') as stream:
    result = subprocess.run(command, cwd=checkout/'Backend', env=environment,
                            stdout=stream, stderr=subprocess.STDOUT)
receipt = {'source_commit': expected, 'source_dirty': False,
           'backend_git_tree': git(checkout, 'rev-parse', expected+':Backend'),
           'checkout': str(checkout), 'started_at': stamp,
           'finished_at': datetime.now(timezone.utc).isoformat(),
           'command': command, 'environment': {key: environment[key] for key in ('GOOS','GOARCH','CGO_ENABLED')},
           'exit_code': result.returncode, 'log_path': str(log), 'log_sha256': sha(log),
           'artifacts': [], 'excluded_private_runtime_fixture': 'Backend/.tmp-native-fixture/',
           'scope': 'Local cross-compilation from committed source only; no signing, deployment or gameplay acceptance.'}
receipt['driver_source']={'path':str(Path(__file__).resolve()),'sha256':sha(Path(__file__))}
receipt['validation_errors']=[]
if result.returncode == 0:
    try:
        require(git(checkout, 'rev-parse', 'HEAD') == expected, 'Backend checkout HEAD changed during build')
        require(not git(checkout, 'status', '--porcelain'), 'Backend checkout became dirty during build')
        for role in ('control-plane','meta-server','edge-relay'):
            path = run/role
            info = subprocess.check_output([go, 'version', '-m', str(path)], text=True)
            require('vcs.revision='+expected in info and 'vcs.modified=false' in info, 'Backend embedded source metadata differs')
            receipt['artifacts'].append({'role': role, 'path': str(path), 'sha256': sha(path),
                                         'bytes': path.stat().st_size, 'embedded_build_info': info})
    except Exception as error:
        receipt['validation_errors'].append(str(error))
receipt['validation_status']='PASS' if result.returncode==0 and not receipt['validation_errors'] else 'FAILED'
(run/'build-receipt.json').write_text(json.dumps(receipt, indent=2)+'\n', encoding='utf-8')
print(json.dumps(receipt))
raise SystemExit(result.returncode or (1 if receipt['validation_errors'] else 0))
