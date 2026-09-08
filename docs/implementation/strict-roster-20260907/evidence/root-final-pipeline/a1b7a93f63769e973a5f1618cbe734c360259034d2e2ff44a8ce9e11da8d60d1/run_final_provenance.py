import hashlib, json, subprocess, sys
from datetime import datetime, timezone
from pathlib import Path

repo = Path('C:/wksp/ProjectRebound')
root = repo/'docs/implementation/strict-roster-20260907'
clone = Path('C:/Users/23587/AppData/Local/Temp/rebound-source-clone-20260907')
read = lambda p: json.loads(p.read_text(encoding='utf-8-sig'))
sha = lambda p: hashlib.sha256(p.read_bytes()).hexdigest()
inventory = read(root/'artifact-inventory.json')
for owner, checkout in [('ProjectRebound', clone), ('Toolbox', Path('C:/wksp/ProjectReboundToolbox'))]:
    if subprocess.check_output(['git','rev-parse','HEAD'],cwd=checkout,text=True).strip() != inventory['source_commits'][owner]:
        raise ValueError('Actual gate checkout differs from frozen source pair: '+owner)
command = [sys.executable, '-B', str(clone/'Tools/Release/strict_roster_provenance.py'),
           '--repo', str(clone), '--toolbox-repo', 'C:/wksp/ProjectReboundToolbox']
for artifact in inventory['artifacts']:
    command += ['--artifact', artifact['path']]
command += ['--artifact-inventory', str(root/'artifact-inventory.json'),
            '--candidate-build-manifest', str(root/'candidate-build-manifest.json'),
            '--acceptance-report', str(root/'acceptance-tests.json'),
            '--output', str(root/'artifacts-provenance.json'), '--require-release-ready']
started = datetime.now(timezone.utc)
out = root/'evidence'/('final-provenance-'+started.strftime('%Y%m%dT%H%M%SZ'))
out.mkdir(parents=True, exist_ok=False)
run = subprocess.run(command, cwd=clone, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
log = out/'gate.log'
log.write_bytes(run.stdout)
result = read(root/'artifacts-provenance.json') if (root/'artifacts-provenance.json').is_file() else {}
receipt = {'command':command, 'working_directory':str(clone),
 'started_at':started.isoformat(), 'finished_at':datetime.now(timezone.utc).isoformat(),
 'exit_code':run.returncode, 'source_pair':inventory['source_commits'],
 'log_path':str(log), 'log_sha256':sha(log), 'generation_id':inventory['generation_id'],
 'acceptance_report_sha256':sha(root/'acceptance-tests.json'),
 'artifact_inventory_sha256':sha(root/'artifact-inventory.json'),
 'provenance_sha256':sha(root/'artifacts-provenance.json') if result else None,
 'candidate_build_binding_validated':result.get('candidate_build_binding',{}).get('validated'),
 'release_ready':result.get('release_ready'),
 'scope':'Actual release gate invocation. Exit 3 is an observed release block, never a release PASS. The clean clone is the actual Go build source; the original workspace still holds evidence and private diagnostic fixtures.'}
(out/'receipt.json').write_text(json.dumps(receipt,indent=2)+'\n',encoding='utf-8')
print(json.dumps({'exit_code':run.returncode,'receipt':str(out/'receipt.json'),
                 'candidate_binding':receipt['candidate_build_binding_validated'],
                 'release_ready':receipt['release_ready']}))
sys.exit(run.returncode)
