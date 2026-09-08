import hashlib, json, subprocess, sys
from datetime import datetime, timezone
from pathlib import Path

repo = Path('C:/wksp/ProjectRebound')
stamp = datetime.now(timezone.utc).strftime('%Y%m%dT%H%M%SZ')
out = repo/'docs/implementation/strict-roster-20260907/evidence'/('release-gate-'+stamp)
out.mkdir(parents=True, exist_ok=False)
sources = ['Tools/Release/strict_roster_provenance.py', 'Tools/Release/test_provenance_cases.py']
sha = lambda p: hashlib.sha256(p.read_bytes()).hexdigest()
command = [sys.executable, '-B', str(repo/sources[1])]
run = subprocess.run(command, cwd=repo/'Tools/Release', stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
log = out/'gate-tests.log'
log.write_bytes(run.stdout)
receipt = {'source_commit': subprocess.check_output(['git','rev-parse','HEAD'],cwd=repo,text=True).strip(),
 'source_dirty': True, 'source_sha256': {s:sha(repo/s) for s in sources},
 'command': command, 'exit_code':run.returncode, 'log_path':str(log), 'log_sha256':sha(log),
 'finished_at':datetime.now(timezone.utc).isoformat(),
 'scope':'Actual release gate regression run; no native acceptance or artifact signature claimed.'}
(out/'receipt.json').write_text(json.dumps(receipt,indent=2)+'\n',encoding='utf-8')
print(json.dumps(receipt))
sys.exit(run.returncode)
