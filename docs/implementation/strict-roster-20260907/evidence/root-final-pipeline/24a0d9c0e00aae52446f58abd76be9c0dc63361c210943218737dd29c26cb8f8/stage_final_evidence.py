"""Stage only the scanned text evidence, preserving private lab data locally."""
import hashlib, json, subprocess, sys
from pathlib import Path

repo = Path('C:/wksp/ProjectRebound')
root = (repo/'docs/implementation/strict-roster-20260907').resolve()
if subprocess.check_output(['git','diff','--cached','--name-only','-z'],cwd=repo):
    raise ValueError('Complete or clear existing staged changes before staging this evidence set')
scanner = Path(__file__).with_name('scan_evidence_credentials.py')
subprocess.run([sys.executable,'-B','-X','utf8',str(scanner)],cwd=repo,check=True)
scan = json.loads((root/'final-credential-evidence-scan.json').read_text(encoding='utf-8'))
if scan.get('candidate_count') != 0 or scan.get('status') != 'NO_CREDENTIAL_SHAPED_VALUES_FOUND':
    raise ValueError('Evidence scan requires review before staging')
sha = lambda p: hashlib.sha256(p.read_bytes()).hexdigest()
scanned = {str(Path(item['path']).resolve()).lower():item['sha256'] for item in scan['scanned_file_hashes']}
extensions = {'.json','.jsonl','.log','.txt','.md','.ps1','.py','.js','.mjs','.ts','.tsx','.jsx',
              '.sh','.rs','.go','.cpp','.h','.hpp','.toml','.yaml','.yml','.sql','.csv','.tsv','.bat','.cmd','.diff','.patch'}
self_reports = {'final-credential-evidence-scan.json','final-committed-evidence-byte-audit.json','final-staging-manifest.json'}
preserved_safe_history = {'docs/implementation/strict-roster-20260907/evidence/native-identity-20260908/private-driver-component-tests.log'}
paths, withheld = [], []
for path in sorted(root.rglob('*')):
    if not path.is_file(): continue
    path = path.resolve()
    if not path.is_relative_to(root): raise ValueError('Evidence path escapes the intended directory')
    relative = str(path.relative_to(repo)).replace('\\','/')
    if path.name in self_reports:
        continue  # Commit the two final audits after the exact-byte audit.
    if path.suffix.lower() == '.dump' or ('private' in path.name.lower() and relative not in preserved_safe_history):
        withheld.append(relative)
        continue
    if path.name != '.gitattributes':
        if path.suffix.lower() not in extensions: raise ValueError('Unreviewed evidence file type: '+relative)
        if scanned.get(str(path).lower()) != sha(path): raise ValueError('Evidence changed after its credential scan: '+relative)
    if path.read_bytes().startswith((b'MZ',b'\x7fELF',b'PK\x03\x04')):
        raise ValueError('Binary file has a text evidence extension: '+relative)
    if relative in preserved_safe_history:
        committed = subprocess.check_output(['git','show','HEAD:'+relative],cwd=repo)
        if committed != path.read_bytes(): raise ValueError('Previously reviewed safe historical log changed')
    paths.append(relative)
manifest = {'scope':'Exact pre-staging evidence bytes; no product/native acceptance claim.',
            'baseline_commit':subprocess.check_output(['git','rev-parse','HEAD'],cwd=repo,text=True).strip(),
            'scanner_source_sha256':sha(scanner),
            'credential_scan_sha256':sha(root/'final-credential-evidence-scan.json'),
            'excluded_self_referential_reports':sorted(self_reports),
            'files':[{'path':name,'bytes':(repo/name).stat().st_size,'sha256':sha(repo/name)} for name in paths]}
staging_root = repo/'.tmp/strict-roster-20260907/final-staging'
scan_archive = staging_root/'scans'/(manifest['credential_scan_sha256']+'.json')
scan_archive.parent.mkdir(parents=True,exist_ok=True)
scan_bytes = (root/'final-credential-evidence-scan.json').read_bytes()
if scan_archive.exists() and scan_archive.read_bytes()!=scan_bytes: raise ValueError('Immutable credential scan changed')
scan_archive.write_bytes(scan_bytes)
manifest_bytes = (json.dumps(manifest,indent=2)+'\n').encode()
fingerprint = hashlib.sha256(manifest_bytes).hexdigest()
archive = staging_root/(fingerprint+'.json')
archive.parent.mkdir(parents=True,exist_ok=True)
if archive.exists() and archive.read_bytes()!=manifest_bytes: raise ValueError('Immutable staging manifest changed')
archive.write_bytes(manifest_bytes)
manifest_path = root/'final-staging-manifest.json'
manifest_path.write_bytes(manifest_bytes)
paths.append(manifest_path.relative_to(repo).as_posix())
for index in range(0, len(paths), 60):
    subprocess.run(['git','add','-f','--',*paths[index:index+60]],cwd=repo,check=True)
staged = subprocess.check_output(['git','diff','--cached','--name-only','-z'],cwd=repo).decode().split('\0')
for name in filter(None, staged):
    path = (repo/name).resolve()
    if name not in paths or not path.is_relative_to(root):
        raise ValueError('Unexpected staged path: '+name)
for record in manifest['files']:
    staged_bytes = subprocess.check_output(['git','show',':'+record['path']],cwd=repo)
    if len(staged_bytes)!=record['bytes'] or hashlib.sha256(staged_bytes).hexdigest()!=record['sha256']:
        raise ValueError('Index bytes differ from pre-staging evidence: '+record['path'])
if subprocess.check_output(['git','show',':'+manifest_path.relative_to(repo).as_posix()],cwd=repo)!=manifest_bytes:
    raise ValueError('Index staging manifest differs')
print(json.dumps({'eligible_text_files':len(paths),'staged_changes':len([p for p in staged if p]),
                  'private_or_dump_paths_withheld':withheld,'audit_reports_deferred':sorted(self_reports-{'final-staging-manifest.json'}),
                  'immutable_staging_manifest':str(archive),'staging_manifest_sha256':fingerprint}))
