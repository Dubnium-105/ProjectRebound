import argparse
import hashlib
import json
import shutil
import subprocess
import time
from datetime import datetime, timezone
from pathlib import Path

repo = Path('C:/wksp/ProjectRebound')
evidence = repo / '.tmp/strict-roster-20260907/native-evidence'
sha = lambda p: hashlib.sha256(p.read_bytes()).hexdigest()
parser = argparse.ArgumentParser()
parser.add_argument('--candidate-run', required=True)
parser.add_argument('--backend-source-root', help='Independent Git checkout whose Backend tree is compiled for this diagnostic.')
parser.add_argument('--backend-driver-receipt', help='Reuse one hash-verified driver built from a clean recorded Backend commit; never build against concurrently edited Backend sources.')
args = parser.parse_args()
backend_repo = Path(args.backend_source_root).resolve() if args.backend_source_root else repo
prior_backend = None
if args.backend_driver_receipt:
    prior_path = Path(args.backend_driver_receipt).resolve()
    prior_backend = json.loads(prior_path.read_text(encoding='utf-8-sig'))
    if prior_backend.get('backend_source_dirty') is not False:
        raise ValueError('Backend driver reuse requires a recorded clean Backend source input')
    if not any(item['name'] == 'driver-build' and item['exit_code'] == 0 for item in prior_backend['executions']):
        raise ValueError('Reused driver has no actual successful build execution')
    prior_driver = Path(prior_backend['driver_artifact']['path'])
    if sha(prior_driver) != prior_backend['driver_artifact']['sha256']:
        raise ValueError('Retained Backend driver bytes changed')
    prior_go = next(Path(item['path']) for item in prior_backend['sources'] if Path(item['path']).name == 'main.go')
    prior_go_sha = next(item['sha256'] for item in prior_backend['sources'] if Path(item['path']).name == 'main.go')
    if sha(prior_go) != prior_go_sha:
        raise ValueError('Retained Backend driver source changed')
candidate_run = Path(args.candidate_run).resolve()
candidate_receipt = json.loads((candidate_run / 'result.json').read_text(encoding='utf-8'))
candidate = Path(candidate_receipt['artifact']['path'])
if sha(candidate) != candidate_receipt['artifact']['sha256']:
    raise ValueError('Candidate no longer matches its actual build receipt')
for item in candidate_receipt['source_files']:
    if sha(Path(item['snapshot_path'])) != item['sha256']:
        raise ValueError('Retained candidate source changed: ' + item['path'])

tag = 'dynamic-root-' + datetime.now(timezone.utc).strftime('%Y%m%dT%H%M%S%fZ')
inputs = evidence / 'live-run-inputs' / tag
results = evidence / 'live-run-results' / tag
inputs.mkdir(parents=True, exist_ok=False)
results.mkdir(parents=True, exist_ok=False)
frozen_go = backend_repo / 'Backend/.tmp-native-fixture' / tag / 'main.go'
frozen_go.parent.mkdir(parents=True, exist_ok=False)
shutil.copyfile(prior_go if prior_backend else repo / 'Backend/.tmp-native-fixture/main.go', frozen_go)
shutil.copyfile(frozen_go, inputs / 'main.go')
shutil.copyfile(evidence / 'orchestrate-dedicated-one-real-steam-positive.ps1', inputs / 'orchestrate.ps1')
shutil.copyfile(candidate, inputs / 'Payload.dll')
if candidate_receipt['artifact']['sha256'].upper() not in (inputs / 'orchestrate.ps1').read_text(encoding='utf-8-sig'):
    raise ValueError('Frozen harness does not require the candidate digest')
head = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=backend_repo, text=True).strip()
def current_backend_changes():
    names = subprocess.check_output(['git','diff','--name-only','HEAD','--','Backend'],cwd=backend_repo,text=True).splitlines()
    names += subprocess.check_output(['git','ls-files','--others','--exclude-standard','--','Backend'],cwd=backend_repo,text=True).splitlines()
    return sorted(set(name for name in names if not name.startswith('Backend/.tmp-native-fixture/')))
backend_names = [] if prior_backend else current_backend_changes()
backend_sources=[]
for name in sorted(set(backend_names)):
    if name.startswith('Backend/.tmp-native-fixture/'):
        continue
    source=backend_repo/name
    retained=inputs/'backend-source'/name
    retained.parent.mkdir(parents=True,exist_ok=True)
    shutil.copyfile(source,retained)
    backend_sources.append({'path':name,'sha256':sha(source),'snapshot_path':str(retained)})
record = {'run_id': tag, 'started_at': datetime.now(timezone.utc).isoformat(),
          'source_input_commit': head, 'backend_tree': subprocess.check_output(['git','rev-parse','HEAD:Backend'],cwd=backend_repo,text=True).strip(),
          'backend_source_checkout': str(backend_repo),
          'observed_workspace_head_at_run': subprocess.check_output(['git','rev-parse','HEAD'],cwd=repo,text=True).strip(),
          'candidate_build_receipt': str(candidate_run / 'result.json'),
          'candidate_build_receipt_sha256': sha(candidate_run / 'result.json'),
          'backend_source_dirty':bool(backend_sources),'backend_source_files':backend_sources,
          'native_source_input_commit': candidate_receipt['source_input_commit'],
          'native_source_dirty': candidate_receipt['source_dirty'],
          'native_source_snapshot_differs_from_current_working_tree': [item['path'] for item in candidate_receipt['source_files'] if sha(repo / item['path']) != item['sha256']],
          'sources': [{'path': str(inputs / name), 'sha256': sha(inputs / name)} for name in ('main.go','orchestrate.ps1','Payload.dll')],
          'scope': 'One real Steam client and isolated Backend component fixture; synthetic second roster seat. Not three-player or release acceptance.',
          'executions': []}
if prior_backend:
    record.update(source_input_commit=prior_backend['source_input_commit'],
                  backend_tree=prior_backend['backend_tree'],
                  observed_workspace_head_at_run=head,
                  backend_driver_reuse={'prior_actual_execution_receipt': str(prior_path),
                                        'prior_receipt_sha256': sha(prior_path),
                                        'prior_run_id': prior_backend['run_id'],
                                        'build_executed_this_run': False})
record_path = results / 'execution-receipt.json'

def save():
    record_path.write_text(json.dumps(record, indent=2) + '\n', encoding='utf-8')

def execute(name, command, cwd):
    log = results / (name + '.log')
    with log.open('wb') as output:
        run = subprocess.run(command, cwd=cwd, stdout=output, stderr=subprocess.STDOUT,
                             creationflags=subprocess.CREATE_NO_WINDOW)
    record['executions'].append({'name': name, 'command': command, 'exit_code': run.returncode,
                                 'log_path': str(log), 'sha256': sha(log)})
    save()
    print(json.dumps({'step': name, 'exit_code': run.returncode, 'run': str(results)}), flush=True)
    if run.returncode:
        raise SystemExit(run.returncode)

driver = inputs / 'backend-native-driver.exe'
fixture = results / 'fixture-private.json'
database_name = 'rebound_native_' + datetime.now(timezone.utc).strftime('%Y%m%d%H%M%S%f')
database = f'postgres://phanthy@127.0.0.1:55439/{database_name}?sslmode=disable'
record['isolated_database'] = database_name
save()
if prior_backend:
    shutil.copyfile(prior_driver, driver)
    if sha(driver) != prior_backend['driver_artifact']['sha256']:
        raise ValueError('Backend driver changed during verified reuse')
else:
    execute('driver-build', ['C:/Program Files/Go/bin/go.exe','build','-o',str(driver),str(frozen_go)],backend_repo/'Backend')
    if current_backend_changes() != backend_names:
        raise ValueError('Backend source path inventory changed during driver build')
for item in backend_sources:
    if sha(backend_repo/item['path']) != item['sha256']:
        raise ValueError('Backend source changed during driver build: '+item['path'])
record['driver_artifact'] = {'path': str(driver), 'sha256': sha(driver)}
execute('isolated-database-create', ['wsl.exe','--','createdb','--host=127.0.0.1','--port=55439','--username=phanthy',database_name], repo)
execute('fixture-prepare', [str(driver),'-mode','prepare','-database-url',database,'-fixture',str(fixture),'-authority-port','47777'], repo/'Backend')
record['fixture'] = {'path': str(fixture), 'sha256': sha(fixture)}
shutil.copyfile(fixture, inputs / 'fixture-initial-private.json')
record['fixture_initial'] = {'path': str(inputs / 'fixture-initial-private.json'), 'sha256': sha(inputs / 'fixture-initial-private.json')}
authority = results / 'backend-authority-ready.json'
client_ready = results / 'client-ready-private.json'
grant = results / 'backend-grant-private.json'
events = results / 'native-events-private.json'
receipts = results / 'backend-receipts'
admission = results / 'backend-admission-evidence.json'
driver_args = [str(driver),'-mode','finalize','-database-url',database,'-fixture',str(fixture),
               '-authority-evidence-root',str(results),'-authority-marker',str(authority),
               '-client-ready-marker',str(client_ready),'-grant-marker',str(grant),
               '-native-events',str(events),'-receipt-dir',str(receipts),'-authority-port','47777']
driver_log = results / 'backend-finalize-private.log'
with driver_log.open('wb') as stream:
    backend = subprocess.Popen(driver_args, cwd=repo/'Backend', stdin=subprocess.DEVNULL,
                               stdout=stream, stderr=subprocess.STDOUT, creationflags=subprocess.CREATE_NO_WINDOW)
    record['backend_owned_pid'] = backend.pid
    save()
    try:
        execute('native-harness', ['C:/Users/23587/.cache/codex-runtimes/codex-primary-runtime/dependencies/native/powershell/pwsh.exe','-NoProfile','-File',str(inputs/'orchestrate.ps1'),
                    '-FixturePath',str(fixture),'-EvidenceRoot',str(results),'-CandidatePayload',str(inputs/'Payload.dll'),
                    '-BackendAuthorityReadyEvidencePath',str(authority),'-ClientReadyMarkerPath',str(client_ready),
                    '-BackendGrantEvidencePath',str(grant),'-BackendScopedEventPath',str(events),
                    '-BackendReceiptDir',str(receipts),'-BackendAdmissionEvidencePath',str(admission),
                    '-TimeoutSeconds','180','-Execute'],repo)
    finally:
        try:
            backend.wait(timeout=3)
            record['backend_stop_reason'] = 'exited'
        except subprocess.TimeoutExpired:
            # This Popen retains the actual process handle; no PID lookup or
            # descendant/name-wide termination can affect another process.
            backend.terminate()
            backend.wait(timeout=10)
            record['backend_stop_reason'] = 'owned_handle_terminated_after_harness'
        record['executions'].append({'name':'backend-finalize','command':driver_args,'exit_code':backend.returncode,
                                     'log_path':str(driver_log),'sha256':sha(driver_log)})
        summaries = sorted(results.glob('dedicated-component-*/summary.json'))
        record['native_summaries'] = [{'path':str(p),'sha256':sha(p)} for p in summaries]
        record['outcomes'] = [json.loads(p.read_text(encoding='utf-8-sig'))['outcome'] for p in summaries]
        record['fixture_final_sha256'] = sha(fixture)
        record['finished_at'] = datetime.now(timezone.utc).isoformat()
        save()
summaries = sorted(results.glob('dedicated-component-*/summary.json'))
record['native_summaries'] = [{'path':str(p),'sha256':sha(p)} for p in summaries]
record['outcomes'] = [json.loads(p.read_text(encoding='utf-8-sig'))['outcome'] for p in summaries]
record['finished_at'] = datetime.now(timezone.utc).isoformat()
save()
print(json.dumps({'receipt':str(record_path),'outcomes':record['outcomes']}),flush=True)
