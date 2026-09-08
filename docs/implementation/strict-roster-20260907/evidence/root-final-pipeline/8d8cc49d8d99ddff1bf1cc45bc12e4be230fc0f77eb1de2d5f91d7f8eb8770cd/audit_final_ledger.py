import hashlib
import importlib.util
import json
import re
import subprocess
from collections import Counter
from datetime import datetime, timezone
from pathlib import Path
from ledger_evidence_helpers import execution_pair_from_report, execution_source_snapshot

repo=Path('C:/wksp/ProjectRebound')
root=repo/'docs/implementation/strict-roster-20260907'
read=lambda p:json.loads(p.read_text(encoding='utf-8-sig'))
sha=lambda p:hashlib.sha256(p.read_bytes()).hexdigest()
progress=read(root/'progress.json')
acceptance=read(root/'acceptance-tests.json')
artifacts=read(root/'artifact-inventory.json')
provenance=read(root/'artifacts-provenance.json')
gate_spec=importlib.util.spec_from_file_location('strict_roster_provenance',repo/'Tools/Release/strict_roster_provenance.py')
gate=importlib.util.module_from_spec(gate_spec)
gate_spec.loader.exec_module(gate)
errors=[]
expected={f'BP-{n:03}' for n in range(1,53)}
protected_unfinished = {
    'BP-038': ('partial','BLOCKED'),
    'BP-039': ('implemented','BLOCKED'),
    'BP-047': ('partial','BLOCKED'),
    'BP-052': ('partial','PARTIAL'),
}

def source_pair_complete(pair):
    return isinstance(pair,dict) and set(pair)=={'ProjectRebound','Toolbox'} and all(
        isinstance(pair.get(owner),str) and re.fullmatch(r'[0-9a-fA-F]{40}',pair[owner])
        for owner in ('ProjectRebound','Toolbox'))

def source_snapshot_clean(snapshot):
    dirty=snapshot.get('source_dirty') if isinstance(snapshot,dict) else None
    return dirty is False or (isinstance(dirty,dict) and set(dirty) == {'ProjectRebound', 'Toolbox'} and all(value is False for value in dirty.values()))

def evidence_path(value):
    path=Path(str(value))
    return path if path.is_absolute() else root/path

def check_snapshot(label, snapshot):
    """Validate a retained execution snapshot without upgrading its source."""
    if not isinstance(snapshot,dict):
        errors.append(label+' missing execution source snapshot object')
        return
    if snapshot.get('historical_only') is not True:
        errors.append(label+' execution source snapshot is not marked historical_only')
    pair=snapshot.get('execution_source_pair',snapshot.get('source_pair'))
    if pair is not None and not isinstance(pair,dict):
        errors.append(label+' execution source pair has invalid shape')
    log_path=snapshot.get('log_path')
    recorded=snapshot.get('log_sha256') or snapshot.get('recorded_log_sha256')
    if log_path and recorded:
        p=evidence_path(log_path)
        if not p.is_file() or sha(p).lower()!=str(recorded).lower():
            errors.append(label+' execution log digest mismatch')
if set(progress['issue_statuses'])!=expected: errors.append('52 breakpoint inventory mismatch')
if {x['id'] for x in acceptance['component_tests']}!={f'AC-{n:03}' for n in range(1,53)}: errors.append('component inventory mismatch')
if len(acceptance['component_tests'])!=52: errors.append('duplicate/missing component')
if len(acceptance['e2e_tests'])!=22 or {x['id'] for x in acceptance['e2e_tests']}!={f'E2E-{n:02}' for n in range(1,23)}: errors.append('E2E inventory mismatch')
generation_id=artifacts.get('generation_id')
if not generation_id or progress.get('generation_id')!=generation_id or acceptance.get('generation_id')!=generation_id:
    errors.append('final ledger generation mismatch')
provenance_pair={owner:entry.get('commit') for owner,entry in provenance.get('repositories',{}).items()}
if provenance_pair!=artifacts['source_commits']:
    errors.append('provenance source pair mismatch')
for issue_id, (expected_impl, expected_acceptance) in protected_unfinished.items():
    item=progress.get('issue_statuses',{}).get(issue_id)
    if not item:
        errors.append(issue_id+' protected unfinished issue is missing from progress')
        continue
    if item.get('implementation')!=expected_impl or item.get('acceptance')!=expected_acceptance:
        errors.append(issue_id+' was promoted past its explicit unresolved implementation/acceptance boundary')
    if item.get('current_candidate_acceptance')=='PASS':
        errors.append(issue_id+' protected unfinished issue is marked current-candidate PASS')
native_report_path=root/'native-report.json'
if native_report_path.is_file():
    native_report=read(native_report_path)
    binary_gate=native_report.get('binary_gate',{})
    if binary_gate.get('native_authority_admission_verified') is not False:
        errors.append('native authority admission gate is not explicitly false while positive acceptance is unverified')
    if str(binary_gate.get('strict_online_positive_acceptance','')).upper() not in ('NOT_RUN', 'BLOCKED'):
        errors.append('native full strict online positive acceptance must remain explicitly NOT_RUN or BLOCKED')
binding=provenance.get('candidate_build_binding',{})
if not binding.get('validated') or binding.get('generation_id')!=generation_id:
    errors.append('final candidate build binding/generation is not verified')
if binding.get('inventory_sha256')!=sha(root/'artifact-inventory.json'):
    errors.append('provenance artifact inventory digest mismatch')
for field in ('backend_build_receipt','toolbox_build_receipt','candidate_build_manifest'):
    receipt=artifacts.get(field,{})
    path=Path(receipt.get('path',''))
    if not path.is_file() or sha(path)!=receipt.get('sha256'):
        errors.append(field+' is missing or changed since the freeze')
        continue
    actual=read(path)
    if field!='candidate_build_manifest':
        if actual.get('exit_code')!=0 or actual.get('validation_status')!='PASS':
            errors.append(field+' is not a validated successful build execution')
        log=Path(actual.get('log_path',''))
        if not log.is_file() or sha(log)!=actual.get('log_sha256'):
            errors.append(field+' actual execution log changed')
evidence_count=0
source_repos={'ProjectRebound':repo,'Toolbox':Path('C:/wksp/ProjectReboundToolbox')}
commit_files={}
def touched_files(owner,commit):
    key=(owner,commit)
    if key not in commit_files:
        if not re.fullmatch(r'[0-9a-f]{40}',commit):
            errors.append(owner+' invalid modification commit '+commit)
            commit_files[key]=set()
        else:
            check=subprocess.run(['git','merge-base','--is-ancestor',commit,artifacts['source_commits'][owner]],cwd=source_repos[owner],capture_output=True)
            if check.returncode: errors.append(owner+' modification commit is not in the reviewed source history: '+commit)
            commit_files[key]=set(subprocess.check_output(['git','diff-tree','--no-commit-id','--name-only','-r',commit],cwd=source_repos[owner],text=True,encoding='utf-8').splitlines())
    return commit_files[key]
for key,item in progress['issue_statuses'].items():
    if item.get('implementation_commits')!=item.get('modification_commits'):
        errors.append(key+' implementation_commits and modification_commits diverge')
    if 'execution_source_snapshots' not in item:
        errors.append(key+' lacks the execution-source snapshot field')
    else:
        if not item.get('execution_source_snapshots') and item.get('execution_source_snapshot_status')!='UNAVAILABLE_HISTORICAL_SOURCE_METADATA':
            errors.append(key+' empty execution-source snapshots lack an explicit historical-metadata limitation')
        for index,snapshot in enumerate(item.get('execution_source_snapshots',[])):
            check_snapshot(f'{key} execution snapshot {index}',snapshot)
    if not item['modification_commits']:
        allowed_qa_gap=(key=='BP-027' and item.get('original_evidence_class')=='V'
                        and item.get('no_modification_commit_reason') and item['acceptance']!='PASS'
                        and item.get('current_candidate_acceptance')!='PASS')
        if not allowed_qa_gap: errors.append(key+' lacks referenced source modification commits or an explicit original-QA exception')
    for owner,commits in item['modification_commits'].items():
        if owner not in source_repos:
            errors.append(key+' unknown source repository '+owner)
            continue
        refs={str(Path(entry['path']).relative_to(source_repos[owner])).replace('\\','/')
              for entry in item['evidence'] if Path(entry['path']).is_relative_to(source_repos[owner])}
        for commit in commits:
            if not refs.intersection(touched_files(owner,commit)):
                errors.append(key+' modification commit does not touch referenced source: '+commit)
    for entry in item['evidence']:
        p=Path(entry['path'])
        evidence_count+=1
        if not p.is_file(): errors.append(key+' missing '+str(p))
        elif entry.get('sha256') and sha(p)!=entry['sha256']: errors.append(key+' evidence hash changed '+str(p))
        if entry.get('kind')=='ledger_reference' and entry.get('expected_generation_id')!=generation_id:
            errors.append(key+' stale generated ledger navigation reference')
    if item['acceptance']=='PASS':
        decision=item.get('acceptance_adjudication')
        if not decision or not decision.get('evidence_satisfied') or not decision.get('executions'):
            errors.append(key+' component PASS lacks adjudicated actual executions')
        else:
            for run in decision['executions']:
                p=Path(run['log_path'])
                if not run['complete_success_observed'] or not p.is_file() or sha(p)!=run['sha256']:
                    errors.append(key+' successful execution record or log digest changed')
                if not run['execution_records'] or any(record.get('exit_code')!=0 or not record.get('command') for record in run['execution_records']):
                    errors.append(key+' lacks a successful actual invocation record')
                check_snapshot(key+' adjudicated execution snapshot',run.get('execution_source_snapshot'))
                if run.get('execution_source_pair') is not None and not isinstance(run.get('execution_source_pair'),dict):
                    errors.append(key+' adjudicated execution source pair has invalid shape')
required={'commit_pair','artifact_hashes','environment','command_or_harness','exit_code','log_paths','observed_result'}
for case in acceptance['component_tests']+acceptance['e2e_tests']:
    if not required.issubset(case['result']): errors.append(case['id']+' missing result fields')
    if case['result'].get('reviewed_source_pair')!=artifacts['source_commits']: errors.append(case['id']+' wrong reviewed source pair')
    if case['result']['commit_pair']!=case['result'].get('execution_source_pair'): errors.append(case['id']+' execution source was replaced')
    if case['id'].startswith('AC-'):
        if 'execution_source_snapshots' not in case['result']:
            errors.append(case['id']+' lacks component execution-source snapshots')
        for index,snapshot in enumerate(case['result'].get('execution_source_snapshots',[])):
            check_snapshot(f'{case["id"]} execution snapshot {index}',snapshot)
        fixture=case['result'].get('preserved_host_fixture')
        if fixture:
            if fixture.get('current_candidate_acceptance')=='PASS':
                errors.append(case['id']+' preserved-host fixture was promoted to current candidate PASS')
            source=fixture.get('source_fixture',{})
            consumer=fixture.get('consumer',{})
            if source.get('path') and consumer.get('path'):
                source_path=evidence_path(source['path']); consumer_path=evidence_path(consumer['path'])
                if not source_path.is_file() or sha(source_path).lower()!=str(source.get('sha256','')).lower():
                    errors.append(case['id']+' preserved-host source fixture digest mismatch')
                if not consumer_path.is_file() or sha(consumer_path).lower()!=str(consumer.get('sha256','')).lower():
                    errors.append(case['id']+' preserved-host consumer fixture digest mismatch')
                elif source_path.read_bytes()!=consumer_path.read_bytes():
                    errors.append(case['id']+' preserved-host consumer bytes differ from C++ producer fixture')
            producer=fixture.get('producer_receipt',{})
            producer_path=evidence_path(producer.get('path','')) if producer.get('path') else None
            if not producer_path or not producer_path.is_file() or (producer.get('sha256') and sha(producer_path).lower()!=str(producer['sha256']).lower()):
                errors.append(case['id']+' preserved-host producer receipt digest mismatch')
            producer_log=evidence_path(producer.get('log_path','')) if producer.get('log_path') else None
            if producer_log and (not producer_log.is_file() or (producer.get('log_sha256') and sha(producer_log).lower()!=str(producer['log_sha256']).lower())):
                errors.append(case['id']+' preserved-host producer log digest mismatch')
    if case['result'].get('current_candidate_acceptance')=='PASS' and case['result']['commit_pair']!=artifacts['source_commits']:
        errors.append(case['id']+' current candidate PASS lacks exact execution source binding')
    if case['result'].get('current_candidate_acceptance')=='PASS':
        snapshot=case['result'].get('execution_source_snapshot')
        if not snapshot or not source_pair_complete(snapshot.get('execution_source_pair',snapshot.get('source_pair'))):
            errors.append(case['id']+' current candidate PASS lacks a complete execution source snapshot')
        elif not source_snapshot_clean(snapshot):
            errors.append(case['id']+' current candidate PASS lacks an explicit clean execution source snapshot')
for item in artifacts['artifacts']:
    p=Path(item['path'])
    if not p.is_file() or sha(p)!=item['sha256']: errors.append(item['role']+' artifact digest mismatch')
if provenance['acceptance_report']['sha256']!=sha(root/'acceptance-tests.json'): errors.append('provenance acceptance report digest mismatch')
if provenance['release_ready'] or acceptance['release_ready'] or progress['release_ready']: errors.append('unverified release incorrectly marked ready')
for case in acceptance['e2e_tests']:
    execution = case['result'].get('execution_report')
    if execution:
        p=Path(execution['path'])
        if not p.is_file() or sha(p)!=execution['sha256']:
            errors.append(case['id']+' dedicated execution report digest mismatch')
        else:
            actual=read(p)
            if case['result'].get('execution_source_pair')!=execution_pair_from_report(actual):
                errors.append(case['id']+' execution source differs from the recorded original report')
            actual_snapshot=execution_source_snapshot(actual)
            recorded_snapshot=case['result'].get('execution_source_snapshot')
            check_snapshot(case['id']+' dedicated execution snapshot',recorded_snapshot)
            if recorded_snapshot:
                for field in ('execution_source_pair','source_commit','source_input_commit','source_dirty','source_state'):
                    if recorded_snapshot.get(field)!=actual_snapshot.get(field):
                        errors.append(case['id']+' execution source snapshot field differs for '+field)
            if actual.get('steps')!=case['result'].get('execution_steps'):
                errors.append(case['id']+' original execution steps were changed')
            for field in ('environment','command_or_harness','exit_code','log_paths','observed_result'):
                if actual.get('result',{}).get(field)!=case['result'].get(field):
                    errors.append(case['id']+' original execution result differs for '+field)
            if actual['id']!=case['id'] or str(actual['status']).lower()!=case['result'].get('historical_execution_status'):
                errors.append(case['id']+' execution status differs from actual report')
            if case['status']=='pass' and case['result'].get('execution_source_pair')!=artifacts['source_commits']:
                errors.append(case['id']+' historical execution incorrectly promoted to current candidate PASS')
            if case['status']=='pass' and not source_snapshot_clean(actual_snapshot):
                errors.append(case['id']+' pass lacks explicit clean execution source state')
            if case['status']=='pass' and (actual['result']['exit_code']!=0 or not actual.get('steps') or any(step['status'].lower()!='pass' for step in actual['steps'])):
                errors.append(case['id']+' lacks complete successful execution evidence')
            if case['status']=='pass':
                errors.extend(gate._execution_step_problems(case,case['result'],p))
                errors.extend(gate._execution_report_problems(case,case['result'],execution,p,artifacts['source_commits']))
    elif case['status'] not in ('not_run','blocked'):
        errors.append(case['id']+' execution status lacks a dedicated execution report')
index={'schema_version':2,'generated_at':datetime.now(timezone.utc).isoformat(),'generation_id':generation_id,
       'source_commits':artifacts['source_commits'],'release_ready':False,
       'issue_count':52,'entries':[{'id':key,'implementation':item['implementation'],
       'acceptance':item['acceptance'],'modification_commits':item['modification_commits'],
       'evidence':item['evidence']} for key,item in progress['issue_statuses'].items()],
       'note':'Generated from the final canonical 52-item ledger. Historical early report snapshots retain their own original counts.'}
(root/'implementation-evidence-index.json').write_text(json.dumps(index,ensure_ascii=False,indent=2)+'\n',encoding='utf-8')
result={'generated_at':datetime.now(timezone.utc).isoformat(),'status':'CONSISTENT_NOT_RELEASE_READY' if not errors else 'FAILED',
        'generation_id':generation_id,
        'source_commits':artifacts['source_commits'],'breakpoints':52,'component_cases':52,'e2e_cases':22,
        'referenced_evidence_entries_checked':evidence_count,'artifacts_checked':len(artifacts['artifacts']),
        'component_statuses':progress['summary'],'e2e_statuses':dict(Counter(case['status'] for case in acceptance['e2e_tests'])),'errors':errors,
        'release_ready':False,'scope':'Ledger/path/hash/source-pair consistency. This audit does not execute gameplay or convert skipped/not-run tests into passes.',
        'prior_audit_resolution':{'canonical_results':'Final 52/22 records now populated with explicit execution status.',
          'missing_index_paths':'Index regenerated from existing final evidence paths.',
          'restore_archive_binding':'A new unique archive was dumped and restored to a new isolated PostgreSQL DB; exact file hash and schema/data comparison are in the backend report. The prior failed socket attempt is retained.'}}
(root/'final-evidence-consistency-audit.json').write_text(json.dumps(result,ensure_ascii=False,indent=2)+'\n',encoding='utf-8')
print(json.dumps(result,ensure_ascii=False))
raise SystemExit(1 if errors else 0)
