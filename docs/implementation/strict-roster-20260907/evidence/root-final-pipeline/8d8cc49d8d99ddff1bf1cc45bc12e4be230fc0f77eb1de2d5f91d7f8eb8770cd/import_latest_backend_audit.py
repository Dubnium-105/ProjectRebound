"""Retain actual audit-fix invocations without inventing execution source metadata."""
import hashlib
import json
import re
from pathlib import Path

repo = Path('C:/wksp/ProjectRebound')
root = repo/'docs/implementation/strict-roster-20260907'
source = repo/'.tmp/strict-roster-20260907'
read = lambda p: json.loads(p.read_text(encoding='utf-8-sig'))
sha = lambda p: hashlib.sha256(p.read_bytes()).hexdigest()
manifest = read(root/'evidence/final-supplements/retained-files.json')['files']

def retained(original, digest=None):
    original = Path(original)
    if not original.is_absolute():
        original = repo/original
    observed = sha(original)
    if digest and observed != digest.lower():
        raise ValueError('Actual audit bytes changed: '+str(original))
    matches = [r for r in manifest if Path(r['original_path']).resolve() == original.resolve()
               and r['sha256'].lower() == observed]
    if not matches:
        raise ValueError('Audit evidence was not explicitly retained: '+str(original))
    result = Path(matches[-1]['path'])
    if sha(result) != observed:
        raise ValueError('Retained audit evidence changed')
    return result

receipt_path = retained(source/'backend-audit-001-003-result-20260908T000953Z.json')
receipt = read(receipt_path)
allowlist_path = retained(source/'backend-audit-001-003-safe-allowlist-20260908T001448Z.json')
allowlist = read(allowlist_path)
hashes = {entry['path']:entry['sha256'] for entry in receipt['log_inventory']}
references = [receipt_path, allowlist_path]
tests = []
for entry in receipt['tests']:
    log = retained(entry['log'], hashes[entry['log']])
    references.append(log)
    tests.append({
        **entry, 'id':'backend-audit-001-003-'+entry['name'],
        'status':'COMPILE_ONLY' if entry['name'] == 'compile_smoke' else 'PASS' if entry['exit_code'] == 0 else 'EXECUTED_FAIL',
        'log_path':str(log), 'log_sha256':sha(log),
        'execution_log_path':str(repo/entry['log']),
        'receipt_path':str(receipt_path), 'receipt_sha256':sha(receipt_path),
        'execution_source_pair':{'ProjectRebound':receipt['source_head_observed']},
        'source_dirty':{'ProjectRebound':True},
        'source_observation_scope':'HEAD observed while producing the historical receipt; not a clean build or pre-execution whole-tree snapshot. No Toolbox input was recorded.',
        'post_execution_source_observation':{'path':str(allowlist_path), 'sha256':sha(allowlist_path),
            'generated_at_utc':allowlist['generated_at_utc'],
            'source_hash_entries':[entry for entry in allowlist['evidence_entries'] if entry['category']=='source_hash_only'],
            'limitation':'Hashes observed after execution; the earlier receipt said files were hashed but omitted them. This supplement does not retroactively establish pre-execution hashes.'}})

for entry in receipt['mutant_reproduction']:
    log = retained(entry['log'], hashes[entry['log']])
    text = log.read_bytes().decode('utf-8-sig')
    actual = re.findall(r'^EXIT_CODE=(\d+)\s*$',text,re.M)
    if len(actual) != 1:
        raise ValueError('Negative control lacks actual exit footer')
    references.append(log)
    tests.append({**entry, 'id':'backend-audit-001-003-negative-'+entry['name'],
        'status':'EXECUTED_FAIL', 'exit_code':int(actual[0]),
        'command':None, 'command_note':'Actual test name is in the log; the historical receipt did not preserve the complete command. No command is reconstructed as an execution fact.',
        'log_path':str(log),'log_sha256':sha(log), 'skip_names':[],
        'execution_source_pair':None, 'source_dirty':None,
        'source_scope':'Isolated source variant with the specified guard removed. Full source snapshot unavailable in original receipt; this is failure evidence, not candidate acceptance.'})

for index, entry in enumerate(receipt['historical_failures']):
    log = retained(entry['log'], hashes[entry['log']])
    references.append(log)
    tests.append({**entry, 'id':'backend-audit-001-003-historical-failure-'+str(index),
        'status':'EXECUTED_FAIL','command':None,'log_path':str(log),'log_sha256':sha(log),
        'skip_names':[], 'execution_source_pair':None, 'source_dirty':True})

full = receipt['full_schema47_to48']
for key in ('restore_log','precheck_log','upgrade_log','postcheck_log'):
    log = retained(full[key], hashes[full[key]])
    references.append(log)
    prefix = key.removesuffix('_log')
    content = log.read_bytes().decode('utf-8-sig',errors='replace')
    recorded_exit = full.get(prefix+'_exit_code')
    if prefix == 'restore':
        actual = re.findall(r'^RESTORE_EXIT_CODE=(\d+)\s*$',content,re.M)
        if len(actual) != 1:
            raise ValueError('Actual restore exit footer is missing')
        recorded_exit = int(actual[0])
    tests.append({'id':'backend-audit-001-003-schema47full-'+prefix,
        'status':'PASS' if recorded_exit == 0 else 'EXECUTED_FAIL',
        'exit_code':recorded_exit, 'command':full.get(prefix+'_command'),
        'command_note':'Unrecorded complete commands remain null; exact retained drivers/logs and aggregate receipt identify scope.',
        'log_path':str(log), 'log_sha256':sha(log), 'skip_names':[],
        'execution_source_pair':{'ProjectRebound':receipt['source_head_observed']},
        'source_dirty':{'ProjectRebound':True},
        'source_observation_scope':'Historical HEAD observed at aggregate receipt time; not current canonical-checksum loader execution.',
        'scope':full['seed'] if prefix == 'restore' else full['postcheck_assertions'],
        'receipt_path':str(receipt_path),'receipt_sha256':sha(receipt_path)})

supplement_path = retained(source/'backend-audit-001-003-supplemental-20260908t002630z.json')
supplement = read(supplement_path)
references.append(supplement_path)
for key in ('full_race','normalized_migration'):
    entry = supplement[key]
    log = retained(entry['log_path'], entry['log_sha256'])
    references.append(log)
    tests.append({**entry, 'id':'backend-current-345bde0-'+key,
        'status':'PASS_WITH_SKIPS' if entry.get('skip_names') else 'PASS',
        'log_path':str(log), 'execution_log_path':entry['log_path'],
        'log_sha256':sha(log), 'skip_names':entry.get('skip_names',[]),
        'receipt_path':str(supplement_path), 'receipt_sha256':sha(supplement_path),
        'execution_source_pair':{'ProjectRebound':supplement['source_head_observed']},
        'source_dirty':{'ProjectRebound':supplement['tracked_backend_source_dirty_at_run_start']},
        'source_scope':'Tracked Backend source clean at invocation start. Single-repository Go execution; no Toolbox or native candidate acceptance.',
        'untracked_input_note':supplement['untracked_backend_note'],
        'post_execution_source_observation':{'generated_at_utc':supplement['generated_at_utc'],
            'source_hashes':supplement['source_hashes'], 'scope':supplement['source_scope']}})

component_path = retained(source/'backend-ac034-ac045-mapping-20260908-v2.json')
component = read(component_path)
references.append(component_path)
for entry in component['entries']:
    for index, invocation in enumerate(entry['tests']):
        log = retained(invocation['log_path'], invocation['log_sha256'])
        references.append(log)
        tests.append({**invocation, 'id':'backend-ac-component-'+entry['breakpoint']+'-'+str(index),
            'status':'PASS', 'skip_names':[], 'issue_ids':[entry['breakpoint']],
            'log_path':str(log), 'log_sha256':sha(log), 'execution_log_path':invocation['log_path'],
            'receipt_path':str(component_path), 'receipt_sha256':sha(component_path),
            'execution_source_pair':{'ProjectRebound':component['source_head_observed']},
            'source_dirty':{'ProjectRebound':True}, 'source_files':entry['source_hashes'],
            'source_scope':component['changed_source'], 'scope':entry['observed']})
for suffix, cause in [('003910Z','Fixture URL expansion produced an invalid database name'),
                      ('004021Z','Fixture SQL required an explicit timestamptz parameter cast')]:
    log = retained(source/('ac045-dedicated-selection-20260908T'+suffix+'.log'))
    references.append(log)
    if '--- FAIL: TestStrictRosterDedicatedSelectionRejectsUnverifiedAndPreventsReadyReentryAgainstPostgreSQL' not in log.read_bytes().decode('utf-8',errors='replace'):
        raise ValueError('Historical fixture failure log changed')
    tests.append({'id':'backend-ac045-fixture-failure-'+suffix, 'status':'EXECUTED_FAIL',
        'exit_code':1, 'exit_code_source':'Agent actual command result, also independently matching the retained Go FAIL footer.',
        'command':None,'command_note':'Complete historical shell invocation was not retained in the original mapping; no command is reconstructed.',
        'log_path':str(log),'log_sha256':sha(log),'skip_names':[], 'scope':cause,
        'execution_source_pair':None,'source_dirty':True})

for owner in ('root','backend'):
    path = root/(owner+'-report.json')
    report = read(path)
    ids = {entry['id'] for entry in tests}
    report['tests'] = [entry for entry in report.get('tests',[]) if entry.get('id') not in ids]+tests
    for issue in report.get('issues',[]):
        if issue['id'] in ('BP-034','BP-038','BP-040','BP-041','BP-043','BP-045','BP-047','BP-049','BP-051'):
            issue['evidence'] = list(dict.fromkeys(issue.get('evidence',[])+[str(p) for p in references]))
    report['latest_backend_audit_scope'] = 'Audit-001/002/003 fixes are committed in 362f9c6; canonical migration checksums in 345bde0. The separate actual current-source full race passed 406 top-level tests and 144 subtests with 4 explicit skips, exit 0. The restored/upgraded full-schema database also passed the current four migration tests. Historical invocations, fixture failure, removed-guard failures and post-execution source hash observations retain their original scope.'
    path.write_text(json.dumps(report,ensure_ascii=False,indent=2)+'\n',encoding='utf-8')
print(json.dumps({'imported_invocations':len(tests),'historical_source_relabelled':False}))
