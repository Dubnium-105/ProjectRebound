import hashlib
import json
import shutil
import subprocess
from datetime import datetime, timezone
from pathlib import Path

repo = Path('C:/wksp/ProjectRebound')
destination = Path('C:/Users/23587/AppData/Local/Temp/rebound-native-backend-16f74a4')
sha = lambda p: hashlib.sha256(p.read_bytes()).hexdigest()
git = lambda root, *args: subprocess.check_output(['git', *args], cwd=root, text=True).strip()
if git(repo, 'rev-parse', 'HEAD:Backend') != git(destination, 'rev-parse', 'HEAD:Backend'):
    raise ValueError('Backend baseline trees differ; create a matching independent checkout')
def changed_names():
    names = git(repo, 'diff', '--name-only', 'HEAD', '--', 'Backend').splitlines()
    names += git(repo, 'ls-files', '--others', '--exclude-standard', '--', 'Backend').splitlines()
    return sorted(set(n for n in names if not n.startswith('Backend/.tmp-native-fixture/')))
names = changed_names()
sources = []
for name in names:
    source = repo / name
    retained = (destination / name).resolve()
    if not retained.is_relative_to(destination.resolve() / 'Backend'):
        raise ValueError('Snapshot target escaped the independent Backend checkout')
    digest = sha(source)
    retained.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(source, retained)
    if sha(source) != digest or sha(retained) != digest:
        raise ValueError('Source changed while snapshotting: ' + name)
    sources.append({'path': name, 'sha256': digest, 'snapshot_path': str(retained)})
if changed_names() != names or any(sha(repo / x['path']) != x['sha256'] for x in sources):
    raise ValueError('Backend edit raced snapshot; retry before building')
result = {'recorded_at': datetime.now(timezone.utc).isoformat(),
          'original_workspace_head': git(repo, 'rev-parse', 'HEAD'),
          'snapshot_base_commit': git(destination, 'rev-parse', 'HEAD'),
          'backend_tree': git(destination, 'rev-parse', 'HEAD:Backend'),
          'backend_source_dirty': bool(sources), 'sources': sources,
          'scope': 'Immutable-at-capture diagnostic Backend source overlay, not a clean release candidate or test result.'}
receipt = destination / ('native-backend-snapshot-' + datetime.now(timezone.utc).strftime('%Y%m%dT%H%M%S%fZ') + '.json')
receipt.write_text(json.dumps(result, indent=2)+'\n', encoding='utf-8')
print(json.dumps({'snapshot_receipt': str(receipt), 'backend_source_dirty': bool(sources), 'files': len(sources)}))
