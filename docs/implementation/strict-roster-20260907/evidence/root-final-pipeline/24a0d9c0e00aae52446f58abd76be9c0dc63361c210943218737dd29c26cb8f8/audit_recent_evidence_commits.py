import hashlib
import json
import subprocess
from datetime import datetime, timezone
from pathlib import Path

repo = Path('C:/wksp/ProjectRebound')
scope = 'docs/implementation/strict-roster-20260907'
full = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=repo, text=True).strip()
names = subprocess.check_output(['git', 'ls-tree', '-r', '--name-only', '-z', full, '--', scope], cwd=repo).decode().split('\0')
manifest_name = scope+'/final-staging-manifest.json'
manifest_bytes = subprocess.check_output(['git','show',full+':'+manifest_name],cwd=repo)
manifest = json.loads(manifest_bytes)
manifest_sha = hashlib.sha256(manifest_bytes).hexdigest()
staging_root = repo/'.tmp/strict-roster-20260907/final-staging'
immutable_manifest = staging_root/(manifest_sha+'.json')
if not immutable_manifest.is_file() or immutable_manifest.read_bytes()!=manifest_bytes:
    raise ValueError('Committed staging manifest differs from the immutable pre-staging record')
expected = {item['path']:item for item in manifest['files']}
scan_archive = staging_root/'scans'/(manifest['credential_scan_sha256']+'.json')
scan_bytes = scan_archive.read_bytes()
if hashlib.sha256(scan_bytes).hexdigest()!=manifest['credential_scan_sha256']:
    raise ValueError('Original credential scan changed')
scan = json.loads(scan_bytes)
if scan.get('candidate_count')!=0 or scan.get('status')!='NO_CREDENTIAL_SHAPED_VALUES_FOUND':
    raise ValueError('Pre-staging scan did not pass review')
scanned = {str(Path(item['path']).resolve()).lower():item['sha256'] for item in scan['scanned_file_hashes']}
for name,item in expected.items():
    if Path(name).name!='.gitattributes' and scanned.get(str((repo/name).resolve()).lower())!=item['sha256']:
        raise ValueError('Pre-staging bytes were not in the original credential scan: '+name)
excluded = {'final-committed-evidence-byte-audit.json', 'final-credential-evidence-scan.json','final-staging-manifest.json'}
actual_names = {name for name in names if name and Path(name).name not in excluded}
if actual_names != set(expected):
    raise ValueError('Committed evidence file set differs from pre-staging manifest: '+str(sorted(actual_names ^ set(expected))))
records, mismatches = [], []
process = subprocess.Popen(['git', 'cat-file', '--batch'], cwd=repo,
                           stdin=subprocess.PIPE, stdout=subprocess.PIPE)
try:
    for name in names:
        if not name or Path(name).name in excluded:
            continue
        if '\n' in name:
            raise ValueError('Unsupported newline in Git path')
        process.stdin.write((full + ':' + name + '\n').encode())
        process.stdin.flush()
        header = process.stdout.readline().decode().strip().split()
        if len(header) != 3 or header[1] != 'blob':
            raise ValueError('Expected committed evidence blob: ' + name)
        size = int(header[2])
        committed = process.stdout.read(size)
        if len(committed) != size or process.stdout.read(1) != b'\n':
            raise ValueError('Truncated Git blob: ' + name)
        actual = (repo / name).read_bytes() if (repo / name).is_file() else None
        original = expected[name]
        manifest_matches = len(committed)==original['bytes'] and hashlib.sha256(committed).hexdigest()==original['sha256']
        identical = actual == committed and manifest_matches
        if not identical:
            mismatches.append(name)
        records.append({'path': name, 'git_blob': header[0], 'bytes': size,
                        'git_blob_sha256': hashlib.sha256(committed).hexdigest(),
                        'working_file_sha256': hashlib.sha256(actual).hexdigest() if actual is not None else None,
                        'pre_staging_sha256':original['sha256'], 'pre_staging_bytes_match':manifest_matches,
                        'identical': identical})
finally:
    process.stdin.close()
    if process.wait(timeout=30) != 0:
        raise ValueError('git cat-file failed')
result = {'recorded_at': datetime.now(timezone.utc).isoformat(),
          'status': 'PASS' if not mismatches else 'FAILED', 'compared_git_commit': full,
          'scope': 'Exact committed evidence bytes and complete file set versus the immutable pre-staging manifest and current working tree; not product/native acceptance.',
          'pre_staging_manifest':{'path':manifest_name,'sha256':manifest_sha,'immutable_copy':str(immutable_manifest)},
          'excluded_self_referential_reports': sorted(excluded),
          'checked_files': len(records), 'mismatches': mismatches, 'files': records}
target = repo / scope / 'final-committed-evidence-byte-audit.json'
target.write_text(json.dumps(result, indent=2) + '\n', encoding='utf-8')
print(json.dumps({'status': result['status'], 'checked_files': len(records), 'report': str(target)}))
raise SystemExit(0 if not mismatches else 1)
