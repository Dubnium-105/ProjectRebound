def require(condition, message):
    if not condition:
        raise ValueError(message)

import hashlib
import importlib.util
import json
import re
import subprocess
from collections import Counter
from datetime import datetime, timezone
from pathlib import Path
from ledger_evidence_helpers import execution_pair_from_report, execution_source_snapshot

repo = Path('C:/wksp/ProjectRebound')
toolbox = Path('C:/wksp/ProjectReboundToolbox')
root = repo/'docs/implementation/strict-roster-20260907'
read = lambda p: json.loads(p.read_text(encoding='utf-8-sig'))
sha = lambda p: hashlib.sha256(p.read_bytes()).hexdigest()
inventory = read(root/'artifact-inventory.json')
pair = inventory['source_commits']
require(set(pair)=={'ProjectRebound','Toolbox'} and all(re.fullmatch(r'[0-9a-f]{40}',value) for value in pair.values()),'Artifact inventory lacks a complete frozen source pair')
require(len(inventory.get('artifacts',[]))==5,'Five actual frozen artifacts are required before generating the final ledger')
for owner,checkout in (('ProjectRebound',repo),('Toolbox',toolbox)):
    subprocess.run(['git','merge-base','--is-ancestor',pair[owner],'HEAD'],cwd=checkout,check=True)
    paths=['.']+([':(exclude)docs/implementation/strict-roster-20260907'] if owner=='ProjectRebound' else [])
    subprocess.run(['git','diff','--quiet',pair[owner],'HEAD','--',*paths],cwd=checkout,check=True)
    subprocess.run(['git','diff','--quiet','HEAD','--',*paths],cwd=checkout,check=True)
    status_paths = paths + ([':(exclude)Backend/.tmp-native-fixture'] if owner == 'ProjectRebound' else [])
    source_status = subprocess.check_output(['git', 'status', '--porcelain', '--untracked-files=all', '--', *status_paths], cwd=checkout, text=True)
    require(not source_status, owner+' has uncommitted or untracked product inputs')
generation_id = inventory.get('generation_id')
require(bool(generation_id),'Artifact inventory lacks a freeze generation')
now = datetime.now(timezone.utc).isoformat()
reports = {owner: read(root/f'{owner}-report.json') for owner in ['root','backend','native','toolbox']}
gate_spec=importlib.util.spec_from_file_location('strict_roster_provenance',repo/'Tools/Release/strict_roster_provenance.py')
gate=importlib.util.module_from_spec(gate_spec)
gate_spec.loader.exec_module(gate)

COMMIT_RE = re.compile(r'^[0-9a-fA-F]{40}$')

def _git_ok(checkout, *args):
    return subprocess.run(['git', *args], cwd=checkout, stdout=subprocess.DEVNULL,
                          stderr=subprocess.DEVNULL).returncode == 0

def _git_commit_time(checkout, commit):
    try:
        return int(subprocess.check_output(['git','show','-s','--format=%ct',commit], cwd=checkout,
                                           text=True, stderr=subprocess.DEVNULL).strip())
    except (subprocess.CalledProcessError, ValueError):
        return 0

def _report_commit_candidates(report, owner):
    """Collect only commits explicitly recorded by an owner report."""
    found = set()
    owner_keys = {owner, owner.lower(), owner.lower()+'_commit',
                  ('project_rebound_commit' if owner == 'ProjectRebound' else 'toolbox_commit')}
    def visit(value, key=None):
        if isinstance(value, dict):
            for child_key, child in value.items():
                visit(child, child_key)
        elif isinstance(value, str) and COMMIT_RE.fullmatch(value):
            # Avoid treating arbitrary artifact hashes as source commits.  A
            # source field or an owner-specific key is required.
            key_text = str(key or '').lower()
            if (key in owner_keys or owner.lower() in key_text or
                    key_text in {'commit','source_commit','source_input_commit','head','toolbox_commit','project_rebound_commit'}):
                found.add(value.lower())
    visit(report)
    return found

def implementation_history_anchors(source_pair):
    """Find report-recorded historical anchors; never use a fixed old hash."""
    anchors = {}
    for owner, checkout in (('ProjectRebound', repo), ('Toolbox', toolbox)):
        candidates = set()
        for report in reports.values():
            candidates.update(_report_commit_candidates(report, owner))
        valid = [commit for commit in candidates
                 if _git_ok(checkout, 'cat-file', '-e', f'{commit}^{{commit}}') and
                 _git_ok(checkout, 'merge-base', '--is-ancestor', commit, source_pair[owner])]
        valid.sort(key=lambda commit: (_git_commit_time(checkout, commit), commit))
        anchors[owner] = {
            'commit': valid[0] if valid else None,
            'candidates': valid,
            'selection': 'oldest explicitly recorded reachable report commit' if valid else 'none; no recorded reachable source commit',
            'reviewed_source_pair': source_pair[owner],
        }
    return anchors

history_anchors = implementation_history_anchors(pair)
def original_contract(name):
    content = subprocess.check_output(['git','show',f'5bb32ba:docs/implementation/strict-roster-20260907/{name}'], cwd=repo)
    return json.loads(content.decode('utf-8-sig')), hashlib.sha256(content).hexdigest()
original, original_breakpoints_sha = original_contract('breakpoints.json')
acceptance, original_acceptance_sha = original_contract('acceptance-tests.json')
records = {}

def resolve_evidence(entry):
    match = re.match(r'^(.*):(\d+)$', entry)
    value, line = (match.group(1), int(match.group(2))) if match else (entry, None)
    p = Path(value)
    if not p.is_absolute(): p = root/p
    p = p.resolve()
    generated_ledger = p.parent == root and p.name in (
        'progress.json', 'acceptance-tests.json', 'IMPLEMENTED.md', 'artifacts-provenance.json', 'implementation-evidence-index.json')
    return {'path': str(p), 'review_line': line, 'exists': p.is_file(),
            'sha256': sha(p) if p.is_file() and not generated_ledger else None,
            'hash_note': 'Navigation cross-reference only, never execution proof; verified against this generation after all ledgers are written.' if generated_ledger else None,
            'expected_generation_id': generation_id if generated_ledger else None,
            'kind': 'ledger_reference' if generated_ledger else 'source_reference' if p.suffix in ('.rs', '.cpp', '.h', '.jsx', '.js', '.mjs', '.go', '.py', '.ps1', '.sh', '.sql', '.toml', '.yaml', '.yml') else 'recorded_evidence'}

def evidence_text(filename):
    raw=(root/filename).read_bytes()
    return raw.decode('utf-16' if raw.startswith((b'\xff\xfe',b'\xfe\xff')) else 'utf-8-sig')

# Explicit evidence adjudications for these component criteria only. They do
# not overwrite owner reports or promote native/E2E acceptance. Missing or
# changed evidence must remove the component PASS instead of trusting an ID.
component_adjudications={}
def adjudicate(issue_id, rationale, checks):
    evidence=[]
    for filename, markers in checks.items():
        path=root/filename
        content=evidence_text(filename)
        evidence.append({'path':str(path), 'sha256':sha(path),
            'required_observations':markers,
            'all_observed':all(marker in content for marker in markers)})
    component_adjudications[issue_id]={'scope':'Original AC component criterion only; full native/E2E remains separate',
        'rationale':rationale, 'evidence':evidence,
        'evidence_satisfied':all(item['all_observed'] for item in evidence)}

def successful_execution(owner, filename, required_markers=(), expected_passes=None, dialect='rust'):
    content=evidence_text(filename)
    expected_path=(root/filename).resolve()
    def same_path(value):
        if not value:
            return False
        candidate=Path(str(value).replace('\\','/'))
        if not candidate.is_absolute():
            candidate=root/candidate
        return candidate.resolve()==expected_path
    records=[item for item in reports[owner].get('tests',[]) if same_path(item.get('log_path'))]
    # A textual marker alone cannot establish a successful invocation. Keep
    # the actual exit record, complete suite footer and explicit skip count.
    def recorded_digest(item):
        return item.get('log_sha256') or item.get('sha256')
    candidates=[dict(item, observed_log_sha256=sha(expected_path),
                     recorded_log_sha256=recorded_digest(item)) for item in records
                if item.get('exit_code')==0 and item.get('command')
                and not item.get('skip_names')
                and recorded_digest(item) and str(recorded_digest(item)).lower()==sha(expected_path).lower()]
    valid=bool(candidates) and all(marker in content for marker in required_markers)
    if dialect=='rust':
        footers=re.findall(r'test result: ok\. (\d+) passed; (\d+) failed; (\d+) ignored;',content)
        valid &= bool(footers) and not re.search(r'test result: FAILED|\.\.\. FAILED|\.\.\. ignored',content)
        if footers:
            passed,failed,ignored=map(int,footers[-1])
            valid &= all(int(footer[1]) == 0 and int(footer[2]) == 0 for footer in footers)
            valid &= failed==0 and ignored==0 and (expected_passes is None or passed==expected_passes)
    elif dialect=='go':
        valid &= bool(re.search(r'^PASS\r?\n^ok\s',content,re.M))
        valid &= not re.search(r'^\s*--- (FAIL|SKIP):|^FAIL\b',content,re.M)
    item_snapshot = execution_source_snapshot(candidates[-1] if candidates else (records[-1] if records else {}))
    # An owner report summarizes many runs. Its source_pair is a review or
    # baseline anchor, not evidence that every historical test used that pair.
    # Only an explicitly labelled execution_source_pair may fill this gap.
    report_execution = reports[owner].get('execution_source_pair')
    report_snapshot = execution_source_snapshot({'execution_source_pair':report_execution}) if report_execution else {}
    if not item_snapshot.get('execution_pair_complete') and report_snapshot.get('source_pair'):
        for field, value in report_snapshot.items():
            if item_snapshot.get(field) in (None, False) and value is not None:
                item_snapshot[field] = value
        item_snapshot['execution_pair_complete'] = bool(report_snapshot.get('execution_pair_complete'))
        item_snapshot['historical_only'] = True
    return {'owner':owner,'log_path':str(root/filename),'sha256':sha(root/filename),
        'execution_records':candidates,'all_matching_execution_records':records,
        'execution_source_snapshot':item_snapshot,
        'execution_source_pair':item_snapshot.get('execution_source_pair'),
        'source_binding_status':'historical_execution_only',
        'complete_success_observed':bool(valid),
        'source_scope':'Historical execution retained with its actual input scope; current review pair is not retroactively asserted as its build source.'}

def add_execution(issue_id, *executions):
    component_adjudications[issue_id]['executions']=list(executions)
    component_adjudications[issue_id]['evidence_satisfied'] &= all(item['complete_success_observed'] for item in executions)

auth_receipts = sorted((root/'evidence').glob('auth-http-current-*/receipt.json'))
require(bool(auth_receipts), 'Current HTTP component execution receipt is required')
auth_receipt = read(auth_receipts[-1])
auth_run = next(item for item in auth_receipt['commands'] if Path(item['log_path']).name == 'http-cases.log')
require(auth_run['exit_code'] == 0 and sha(Path(auth_run['log_path'])) == auth_run['log_sha256'], 'Current HTTP execution log changed')
auth_log = str(Path(auth_run['log_path']).relative_to(root))
rust_log = 'evidence/toolbox-final-followups-20260908/toolbox-full-lib-same-route-20260908.log'
relay_log = 'evidence/final-supplements/relay-windows-crosslang-20260908/go-test-rust-driver.log'
auth_result=json.loads(evidence_text(auth_log))
auth_cases={item['case']:item for item in auth_result['results']}
adjudicate('BP-002','Actual concurrent HTTP refresh and held-response logout/account-switch cases; source review confirms epoch CAS.',
    {auth_log:['20 concurrent real HTTP 401 requests retried','logout during actual pending refresh rejects','switch during actual pending refresh rejects']})
component_adjudications['BP-002']['evidence_satisfied'] &= (
    all(auth_cases[name]['status']=='PASS' and auth_cases[name]['driver_exit_code']==0 for name in ('concurrent','logout','switch'))
    and auth_cases['concurrent']['actual_http_counts']['old_401']==20
    and auth_cases['concurrent']['actual_http_counts']['refresh']==1
    and auth_cases['concurrent']['actual_http_counts']['protected_success']==20)
add_execution('BP-002',successful_execution('root',auth_log,dialect='json'))
adjudicate('BP-003','Actual killed writer, encrypted replacement and concurrent stale-preference tests are present in the final successful Rust suite.',
    {rust_log:[
        'config::config_types::tests::killed_config_writer_recovers_complete_previous_version ... ok',
        'config::config_types::tests::encrypted_replacements_are_complete_and_leave_no_plaintext_temporary ... ok',
        'config::config_types::tests::concurrent_stale_preferences_cannot_overwrite_refreshed_credentials ... ok','test result: ok.']})
add_execution('BP-003',successful_execution('root',rust_log))
adjudicate('BP-013','Real Go Edge and Rust HOST/PEER exchange plus receiver retagging and tamper rejection cover the cross-language wire criterion.',
    {relay_log:['PASS: distinct HOST/PEER bind, recipient re-tagging, bidirectional authenticated UDP','--- PASS: TestStrictRosterRustRelayClients'],
     rust_log:['vnt::legacy::relay::tests::decodes_go_edge_recipient_retagged_wire_vectors ... ok','vnt::legacy::relay::tests::relay_rejects_tampering_without_advancing_window ... ok']})
add_execution('BP-013',successful_execution('root',relay_log,dialect='go'),successful_execution('root',rust_log))
adjudicate('BP-022','Actual C++ encoder frames are decoded by Rust; positive and wrong-ID/command/replay cases preserve pending versus ready semantics. Native owner PASS and broader root/Toolbox PARTIAL are retained as separate review scopes.',
    {rust_log:['cpp_frames_reject_wrong_request_command_and_replayed_event_cursor ... ok','actual_cpp_frames_decode_in_rust_without_promoting_queued_or_pending_to_ready ... ok','test result: ok. 311 passed; 0 failed; 0 ignored;'],
     'payload-wire-fixtures.jsonl':['\n'], 'payload-wire-negative-fixtures.jsonl':['\n']})
def parsed_wire_fixture(filename, expected_commands, toolbox_filename):
    content=evidence_text(filename)
    frames={}
    for line in content.splitlines():
        command,body=line.split('\t',1)
        require(command not in frames, filename)
        payload=json.loads(body)
        require(isinstance(payload,dict) and isinstance(payload.get('request_id'),str) and payload['request_id'], filename)
        frames[command]=payload
    require(set(frames)==set(expected_commands), filename)
    consumed=toolbox/'tests/fixtures'/toolbox_filename
    require(consumed.read_bytes()==(root/filename).read_bytes(), 'C++ fixture differs from the bytes consumed by Rust: '+filename)
    return frames,{'path':str(root/filename),'sha256':sha(root/filename),'frame_count':len(frames),
        'commands':list(frames),'rust_input_path':str(consumed),'rust_input_sha256':sha(consumed),
        'comparison':'Exact bytes, including newlines.'}
positive,positive_receipt=parsed_wire_fixture('payload-wire-fixtures.jsonl',[
    'payload_status_ack','join_ack','install_match_allocation_ack','start_match_authority_ack',
    'confirm_client_match_connection_ack','match_connection_events_ack','confirm_match_admission_ack','confirm_match_connection_ack',
    'clear_match_allocation_pending','clear_match_allocation_ack'],'payload-wire-v2.jsonl')
negative,negative_receipt=parsed_wire_fixture('payload-wire-negative-fixtures.jsonl',['error','pong'],'payload-wire-negative-v2.jsonl')
require(positive['join_ack']['status']=='accepted', 'Validation failed in create_acceptance_ledger.py:132')
require(positive['confirm_match_admission_ack']['status']=='queued', 'Validation failed in create_acceptance_ledger.py:133')
require(positive['payload_status_ack']['ready'] is False and positive['payload_status_ack']['strict_online_ready'] is False, 'Validation failed in create_acceptance_ledger.py:134')
require(positive['clear_match_allocation_pending']['native_cleared'] is False, 'Validation failed in create_acceptance_ledger.py:135')
require(positive['clear_match_allocation_ack']['native_cleared'] is True, 'Validation failed in create_acceptance_ledger.py:136')
require(positive['confirm_client_match_connection_ack']['status'] == 'travel_requested'
        and positive['confirm_client_match_connection_ack']['scope_verified'] is False
        and positive['confirm_client_match_connection_ack']['local_pawn_ready'] is False,
        'Client travel acknowledgement cannot establish native Playable')
require(negative['error']['code']=='busy', 'Validation failed in create_acceptance_ledger.py:137')
component_adjudications['BP-022']['parsed_wire_fixtures']=[positive_receipt,negative_receipt]
wire_producer_receipts = sorted((root/'evidence').glob('cpp-wire-producer-*/receipt.json'))
require(bool(wire_producer_receipts), 'Ordinary C++ wire fixtures lack an actual producer receipt')
wire_producer_path = wire_producer_receipts[-1]
wire_producer = read(wire_producer_path)
producer_bound = (wire_producer.get('exit_code') == 0 and wire_producer.get('log_sha256') and
    sha(Path(wire_producer['log_path'])) == wire_producer['log_sha256'] and
    sha(Path(wire_producer['binary_path'])) == wire_producer['binary_sha256'])
for produced in wire_producer['fixtures']:
    producer_bound = producer_bound and produced['exact_consumer_bytes'] and (
        sha(Path(produced['path'])) == produced['sha256'] == sha(Path(produced['consumer_path'])))
component_adjudications['BP-022']['ordinary_wire_producer'] = {
    'path': str(wire_producer_path), 'sha256': sha(wire_producer_path), 'producer_bound': bool(producer_bound),
    'scope': 'Actual current C++ producer and exact existing Rust input bytes only; no native game claim.'}
component_adjudications['BP-022']['evidence_satisfied'] &= bool(producer_bound)
add_execution('BP-022',successful_execution('root',rust_log,required_markers=(
    'server::pipe::cpp_fixture_tests::cpp_frames_reject_wrong_request_command_and_replayed_event_cursor ... ok',
    'server::pipe::cpp_fixture_tests::actual_cpp_frames_decode_in_rust_without_promoting_queued_or_pending_to_ready ... ok')))

def actual_preserved_host_fixture():
    """Record the real C++ preservation frame and its Rust consumer separately.

    This is deliberately fixture-level evidence.  It must never become a live
    native HOST acceptance or be rebound to the final source pair merely because
    the copied bytes still exist in the current checkout.
    """
    native_root=repo/'.tmp/strict-roster-20260907/native-evidence'
    candidates=sorted(native_root.glob('wire-preserved-host-*/preserved-host-wire.jsonl'))
    if not candidates:
        return {'status':'BLOCKED','current_candidate_acceptance':'BLOCKED',
                'reason':'No actual C++ preserved-host fixture was found.',
                'source_fixture':None,'consumer':None}
    fixture=candidates[-1]
    producer_receipt_path=fixture.with_name('receipt.json')
    copy_path=toolbox/'tests/fixtures/preserved-host-wire.jsonl'
    record={'status':'fixture_consumed_not_game_e2e','current_candidate_acceptance':'BLOCKED',
            'source_fixture':{'path':str(fixture),'sha256':sha(fixture)},
            'producer_receipt':{'path':str(producer_receipt_path)},
            'consumer':{'path':str(copy_path)},
            'limitations':[
                'Actual Windows CommandFramework producer bytes were consumed by the Rust parser.',
                'The producer uses a synthetic HOST callback proof; no live native game HOST acceptance was run.',
                'Parser negative mutations are in-memory consumer checks, not additional C++ producer frames.',
                'The recorded consumer source was dirty/historical and is not the final candidate execution pair.'
            ]}
    if not producer_receipt_path.is_file() or not copy_path.is_file():
        record['status']='BLOCKED'
        record['reason']='Producer receipt or exact Toolbox fixture copy is missing.'
        return record
    try:
        producer=read(producer_receipt_path)
    except (OSError, json.JSONDecodeError) as exc:
        record['status']='BLOCKED'; record['reason']=f'Producer receipt unreadable: {exc}'
        return record
    record['producer_receipt'].update({'sha256':sha(producer_receipt_path),'exit_code':producer.get('exit_code'),
                                      'fixture_sha256':producer.get('fixture_sha256'),
                                      'test_executable_sha256':producer.get('test_executable_sha256'),
                                      'log_path':producer.get('log_path'),'log_sha256':producer.get('log_sha256')})
    producer_log=Path(str(producer.get('log_path','')))
    producer_log_ok=(producer_log.is_file() and producer.get('log_sha256') and
                     sha(producer_log).lower()==str(producer.get('log_sha256')).lower())
    producer_ok=(producer.get('exit_code')==0 and
                  str(producer.get('fixture_sha256','')).lower()==sha(fixture).lower() and
                  producer.get('fixture_path','').replace('\\','/').lower()==str(fixture).replace('\\','/').lower() and
                  producer_log_ok)
    record['producer_receipt']['log_verified']=producer_log_ok
    copy_ok=(copy_path.read_bytes()==fixture.read_bytes() and sha(copy_path).lower()==sha(fixture).lower())
    record['consumer']['sha256']=sha(copy_path); record['consumer']['exact_byte_copy']=copy_ok
    tool_receipt_path=toolbox/'.tmp/strict-roster-20260907/host-route-preserve-20260908/actual-preserved-host-fixture-receipt.json'
    record['consumer_receipt']={'path':str(tool_receipt_path)}
    if tool_receipt_path.is_file():
        try:
            tool_receipt=read(tool_receipt_path)
            record['consumer_receipt'].update({'sha256':sha(tool_receipt_path),
                'source_head':tool_receipt.get('source',{}).get('head'),
                'source_dirty':tool_receipt.get('source',{}).get('dirty'),
                'tests':tool_receipt.get('tests',[])})
            final_tests=[test for test in tool_receipt.get('tests',[])
                         if test.get('exit_code')==0 and 'actual-preserved-host-fixture-test-final' in str(test.get('log_path',''))]
            if final_tests:
                final_test=final_tests[-1]
                test_path=Path(final_test['log_path'])
                test_ok=test_path.is_file() and final_test.get('log_sha256') and (
                                                  sha(test_path).lower()==str(final_test['log_sha256']).lower())
                record['consumer']['test']={'command':final_test.get('command'),'exit_code':final_test.get('exit_code'),
                    'log_path':str(test_path),'log_sha256':sha(test_path) if test_path.is_file() else None,
                    'recorded_log_sha256':final_test.get('log_sha256')}
                record['consumer']['test_ok']=test_ok
            else:
                record['consumer']['test_ok']=False
            source_head=tool_receipt.get('source',{}).get('head')
            record['execution_source_snapshot']=execution_source_snapshot({
                'source_pair':{'Toolbox':source_head} if isinstance(source_head,str) else None,
                'source_commit':source_head,
                'source_dirty':tool_receipt.get('source',{}).get('dirty'),
                'source_state':tool_receipt.get('source',{}).get('status')})
        except (OSError, json.JSONDecodeError) as exc:
            record['consumer']['test_ok']=False; record['reason']=f'Consumer receipt unreadable: {exc}'
    else:
        record['consumer']['test_ok']=False; record['reason']='Rust consumer receipt is missing.'
    record['producer_ok']=producer_ok; record['copy_ok']=copy_ok
    if not (producer_ok and copy_ok and record['consumer'].get('test_ok')):
        record['status']='BLOCKED'
        record.setdefault('reason','Producer/copy/consumer evidence is incomplete.')
    return record

component_adjudications['BP-022']['preserved_host_fixture']=actual_preserved_host_fixture()

# These original criteria describe protocol/type components. Their actual
# executions do not establish any of the separate gameplay/E2E criteria.
mapping_path = root/'evidence/final-supplements/toolbox-local-ac-mapping.json'
mapping = read(mapping_path)
mapped = {item['issue_id']: item for item in mapping['items']}
criteria = {
    'BP-009': ['p2p_authority_route_always_reports_an_explicit_port',
               'transport_target_brackets_ipv6_and_rejects_ambiguous_hosts'],
    'BP-023': ['actual_windows_message_pipe_preserves_fragmented_and_coalesced_frames',
               'actual_windows_pipe_no_response_has_a_bounded_deadline',
               'cpp_frames_reject_wrong_request_command_and_replayed_event_cursor',
               'actual_cpp_frames_decode_in_rust_without_promoting_queued_or_pending_to_ready'],
    'BP-031': ['identities_are_not_interchangeable_types',
               'generations_reject_zero_and_serialize_as_numbers',
               'host_playable_evidence_requires_the_native_host_scope',
               'p2p_owner_starts_authority_before_members_connect',
               'dedicated_owner_connects_as_an_ordinary_roster_member',
               'canonical_request_matches_backend_contract',
               'role_and_catalog_validation_is_fail_closed'],
}
for issue_id, names in criteria.items():
    item = mapped[issue_id]
    require(item['component_expected_verdict'] == 'PASS_COMPONENT' and not item['uncovered_conditions'],
            issue_id+' source mapping reports uncovered original conditions')
    original_case = next(case for case in acceptance['component_tests'] if case['issue_id'] == issue_id)
    require(item['original_expected'] == original_case['expected'], issue_id+' mapping changed the original criterion')
    markers = [name+' ... ok' for name in names]
    adjudicate(issue_id, item['original_expected']+' Original component scope only; no current artifact/gameplay acceptance.',
               {rust_log:markers})
    proof = component_adjudications[issue_id]
    proof['condition_mapping'] = {'path':str(mapping_path), 'sha256':sha(mapping_path), 'conditions':item['conditions']}
    proof['reviewed_source_hashes'] = []
    for condition in item['conditions']:
        for source in condition['source_assertions']:
            path = Path(source['path'])
            require(sha(path).lower() == source['sha256'].lower(), issue_id+' reviewed component source changed: '+str(path))
            proof['reviewed_source_hashes'].append({'path':str(path),'sha256':sha(path),'scope':'Source assertion, not a new test execution'})
    add_execution(issue_id, successful_execution('root',rust_log,required_markers=markers))

# Native endpoint parsing is exercised by the actual fixed-source C++ suite.
cpp_receipt_path = root/'evidence/native-scope-runtime-20260908/root-validation-20260907T223103129065Z/result.json'
cpp_receipt = read(cpp_receipt_path)
cpp_test = next(run for run in cpp_receipt['executions'] if run['name'] == 'ctest')
cpp_log = cpp_receipt_path.parent/'ctest.log'
cpp_bound = (cpp_test['exit_code'] == 0 and sha(cpp_log) == cpp_test['sha256'] and
             '100% tests passed, 0 tests failed out of 24' in evidence_text(str(cpp_log.relative_to(root))) and
             bool(re.search(r'command_protocol_tests\s+\.+\s+Passed', evidence_text(str(cpp_log.relative_to(root))))))
component_adjudications['BP-009']['cpp_endpoint_validation'] = {
    'receipt_path':str(cpp_receipt_path),'receipt_sha256':sha(cpp_receipt_path),
    'log_path':str(cpp_log),'log_sha256':sha(cpp_log),'exit_code':cpp_test['exit_code'],
    'execution_source':cpp_receipt['source_input_commit'], 'source_dirty':cpp_receipt['source_dirty'],
    'scope':'Actual C++ CommandProtocol endpoint acceptance/rejection assertions within the 24-test suite'}
component_adjudications['BP-009']['evidence_satisfied'] &= bool(cpp_bound)
component_adjudications['BP-023']['ordinary_wire_producer'] = component_adjudications['BP-022']['ordinary_wire_producer']
component_adjudications['BP-023']['evidence_satisfied'] &= bool(producer_bound)

strong_id_receipts = sorted((root/'evidence').glob('strong-id-current-*/receipt.json'))
require(bool(strong_id_receipts), 'Actual current strong-ID compile-fail receipt missing')
strong_id_receipt = read(strong_id_receipts[-1])
strong_id_log = Path(strong_id_receipt['log_path'])
require(sha(strong_id_log) == strong_id_receipt['log_sha256'], 'Strong-ID compile-fail log changed')
add_execution('BP-031',successful_execution('root',rust_log),
    successful_execution('root',str(strong_id_log.relative_to(root)), expected_passes=2))

# Real database/service invocations satisfy these original Backend component
# criteria independently of the still-unexecuted native registration matrix.
counter_root = root/'evidence/final-supplements/bp014-counter-20260908T083620Z'
counter_path = counter_root/'bp014-toolbox-route-loop-receipt.json'
counter_receipt = read(counter_path)
counter_log = counter_root/'cargo-test-toolbox-route-loop-rerun.log'
counter_go_path = counter_root/'bp014-counter-receipt.json'
counter_go = read(counter_go_path)
counter_go_log = counter_root/'go-test-product-counters-final-with-driver.log'
counter_item = mapped['BP-014']
require(counter_item['original_expected']==next(case for case in acceptance['component_tests'] if case['issue_id']=='BP-014')['expected'], 'BP-014 original criterion changed')
require(counter_receipt['exit_code']==0 and sha(counter_log)==counter_receipt['log_sha256'], 'Actual route-loop counter log changed')
require(counter_go['exit_code']==0 and sha(counter_go_log)==counter_go['log_sha256'], 'Actual Go Edge counter log changed')
for condition in counter_item['conditions']:
    for source in condition['source_assertions']:
        require(sha(Path(source['path'])).lower()==source['sha256'].lower(), 'BP-014 reviewed product source changed')
adjudicate('BP-014','Real production route_loop counter observations close the earlier source-inferred counter gap. Historical relay tamper/replay/window/MTU executions remain independently bound; no impaired-game or complete native acceptance is inferred.', {
    str(counter_log.relative_to(root)):['BP014_TOOLBOX_LEGACY_ROUTE_STATS source=LegacyRouteStatus host_datagrams_sent=1 host_datagrams_received=1 member_datagrams_sent=1 member_datagrams_received=1'],
    str(counter_go_log.relative_to(root)):['--- PASS: TestBP014ProductCountersFromLiveRelay'],
    rust_log:['relay_rejects_tampering_without_advancing_window ... ok',
              'relay_receives_truncated_mac_and_reorders_without_replay ... ok',
              'replay_window_has_explicit_boundaries ... ok']})
component_adjudications['BP-014']['historical_condition_mapping'] = {
    'path':str(mapping_path),'sha256':sha(mapping_path),'conditions':counter_item['conditions']}
component_adjudications['BP-014']['numeric_counter_supplement'] = {
    'path':str(counter_path),'sha256':sha(counter_path),'observed':counter_receipt['observed_product_stats'],
    'scope':counter_receipt['scope'],'temporary_source_sha256':counter_receipt['temporary_full_source_sha256']}
stats=counter_receipt['observed_product_stats']
component_adjudications['BP-014']['evidence_satisfied'] &= all(stats.get(key)==1 for key in
    ('host_datagrams_sent','host_datagrams_received','member_datagrams_sent','member_datagrams_received'))
add_execution('BP-014',successful_execution('root',str(counter_log.relative_to(root)),expected_passes=1),
    successful_execution('root',str(counter_go_log.relative_to(root)),dialect='go'),
    successful_execution('root',rust_log))

backend_component_path = root/'evidence/final-supplements/backend-ac034-ac045-mapping-20260908-v2.json'
backend_component = read(backend_component_path)
backend_markers = {
    'BP-034':['--- PASS: TestStrictRosterDedicatedLifecycleAgainstPostgreSQL',
              '--- PASS: TestBattleLogSubmissionAgainstPostgreSQL/running',
              '--- PASS: TestBattleLogSubmissionAgainstPostgreSQL/completed_late_report',
              '--- PASS: TestBattleLogSubmissionAgainstPostgreSQL/pve_claim_non_official'],
    'BP-045':['--- PASS: TestStrictRosterDedicatedSelectionRejectsUnverifiedAndPreventsReadyReentryAgainstPostgreSQL'],
}
for entry in backend_component['entries']:
    issue_id = entry['breakpoint']
    original_case = next(case for case in acceptance['component_tests'] if case['issue_id']==issue_id)
    require(entry['contract']==original_case['expected'] and entry['acceptance']=='PASS', issue_id+' original criterion mismatch')
    for source in entry['source_hashes']:
        require(sha(repo/source['path']).lower()==source['sha256'].lower(), issue_id+' component source changed')
    invocation = entry['tests'][0]
    log = root/'evidence/final-supplements'/Path(invocation['log_path']).name
    require(sha(log).lower()==invocation['log_sha256'].lower() and invocation['exit_code']==0, issue_id+' actual log changed')
    filename = str(log.relative_to(root))
    adjudicate(issue_id, entry['observed'], {filename:backend_markers[issue_id]})
    component_adjudications[issue_id]['condition_mapping'] = {
        'path':str(backend_component_path),'sha256':sha(backend_component_path),
        'original_expected':entry['contract'],'source_hashes':entry['source_hashes']}
    add_execution(issue_id,successful_execution('backend',filename,backend_markers[issue_id],dialect='go'))

# These criteria retain explicit acceptance gaps. Source implementation and
# required environment/runtime proof are recorded separately.
protected_unfinished = {
    'BP-038': {'implementation':'partial','acceptance':'BLOCKED',
               'gap':'Fixed-image static call-graph and binary binding support synchronous borrowed-string copying with no retain/free; the dynamic ownership probe still has zero target coverage, and the full native rejection matrix remains unrun.',
               'files':['Payload/Hooks/Hooks.cpp','Payload/ClientLogic/NativeLoginGrantPolicy.h','Payload/Hooks/StrictRosterSteamAuth.cpp']},
    'BP-039': {'implementation':'implemented','acceptance':'BLOCKED',
               'gap':'Real PreLogin/Steam/native admission/Backend Confirm/CONNECTED/release observed; positive Playable is blocked by online MinPlayersToStart=2 with only one real Steam client.',
               'files':['Payload/ClientLogic/ClientLogic.cpp','Payload/Hooks/StrictRosterSteamAuth.cpp','Payload/dllmain.cpp']},
    'BP-047': {'implementation':'partial','acceptance':'BLOCKED',
               'gap':'Actual Dedicated authority-only owned-HANDLE exit reached scoped CLEARED and rejected wrong world/route/PID. Reuse with a newly authenticated process/heartbeat and the complete multi-player next-match lifecycle remain unverified.',
               'files':['Payload/dllmain.cpp','Backend/internal/matchlobby/service.go','src/server/registration.rs','src/server/pipe.rs']},
    'BP-052': {'implementation':'partial','acceptance':'PARTIAL',
               'gap':'Five actual build artifacts are required by this ledger; final signed release provenance and native compatibility acceptance remain unverified.',
               'files':['Tools/Release/strict_roster_provenance.py','Backend','Tools/Release']},
}
native_gate_snapshot={
    'native_authority_admission_verified': reports['native'].get('binary_gate',{}).get('native_authority_admission_verified'),
    'strict_online_positive_acceptance': reports['native'].get('binary_gate',{}).get('strict_online_positive_acceptance'),
    'source_state': reports['native'].get('source_state'),
    'status': 'BLOCKED' if reports['native'].get('binary_gate',{}).get('native_authority_admission_verified') is not True else 'REVIEW_REQUIRED',
    'rule':'Native positive admission remains fail-closed; diagnostic/ABI evidence cannot set the verified bit.'
}
for bp in original['issues']:
    issue_id = bp['id']
    reviews = []
    for owner, report in reports.items():
        for item in report.get('issues', []):
            if item['id'] == issue_id:
                reviews.append({'owner': owner, **item})
    require(reviews, f'No actual source review for {issue_id}')
    implementations = [item.get('implementation', 'partial').lower() for item in reviews]
    code_status = 'implemented' if all(s == 'implemented' for s in implementations) else 'partial'
    if issue_id in ('BP-027', 'BP-038') and any(s == 'blocked' for s in implementations):
        code_status = 'blocked'
    statuses = [str(item.get('acceptance', 'PARTIAL')).upper() for item in reviews]
    adjudication=component_adjudications.get(issue_id)
    status = 'PASS' if adjudication and adjudication['evidence_satisfied'] else ('BLOCKED' if 'BLOCKED' in statuses else 'PARTIAL')
    if issue_id=='BP-027' and status!='PASS':
        code_status='blocked'  # This original item requires native QA, not another source-only claim.
    if issue_id in protected_unfinished:
        # Keep the report's concrete owner evidence, but clamp the generated
        # aggregate to the explicit unresolved source/acceptance boundary.
        protected=protected_unfinished[issue_id]
        code_status=protected['implementation']
        status=protected['acceptance']
    evidence = []
    private_evidence_withheld=[]
    for item in reviews:
        for entry in item.get('evidence', []):
            if 'private' in Path(entry).name.lower():
                private_evidence_withheld.append({'name':Path(entry).name,
                    'reason':'Private fixture retained locally; no private path, digest or contents is published as acceptance proof.'})
                continue
            evidence.append(resolve_evidence(entry))
    evidence = list({item['path']: item for item in evidence}.values())
    commits = {'baseline_contract': '5bb32ba949e7a36d2c76288f46a57c81fc55cadc',
               'baseline_contract_scope':'Original TARGET_CONTRACTS/IMPLEMENTATION_PLAN/breakpoints/acceptance contract; not an implementation source anchor.'}
    owners = {r['owner'] for r in reviews}
    modification_commits = {}
    for name, source_repo in [('ProjectRebound', repo), ('Toolbox', toolbox)]:
        baseline=history_anchors[name].get('commit')
        paths=[]
        for entry in evidence:
            p=Path(entry['path'])
            if p.is_relative_to(source_repo) and not p.is_relative_to(root) and not p.is_relative_to(repo/'.tmp'):
                paths.append(str(p.relative_to(source_repo)))
        if paths and baseline:
            history=subprocess.check_output(['git','log','--format=%H',f'{baseline}..{pair[name]}','--',*sorted(set(paths))], cwd=source_repo, text=True).splitlines()
            if history: modification_commits[name] = list(dict.fromkeys(history))
    execution_snapshots=[]
    for owner_review in reviews:
        owner_report=reports[owner_review['owner']]
        for test in owner_report.get('tests',[]):
            snapshot=execution_source_snapshot(test)
            explicit_execution_pair=owner_report.get('execution_source_pair')
            report_snapshot=execution_source_snapshot({'execution_source_pair':explicit_execution_pair}) if explicit_execution_pair else {}
            if not snapshot.get('execution_pair_complete') and report_snapshot.get('source_pair'):
                snapshot['source_pair']=report_snapshot.get('source_pair')
                snapshot['execution_source_pair']=report_snapshot.get('execution_source_pair')
                snapshot['execution_pair_complete']=report_snapshot.get('execution_pair_complete',False)
                snapshot['historical_only']=True
            if snapshot.get('source_pair') or snapshot.get('source_commit') or snapshot.get('source_input_commit'):
                snapshot['owner']=owner_review['owner']
                snapshot['log_path']=test.get('log_path')
                snapshot['log_sha256']=test.get('log_sha256') or test.get('sha256')
                snapshot['exit_code']=test.get('exit_code')
                snapshot['command']=test.get('command')
                execution_snapshots.append(snapshot)
    records[issue_id] = {'id': issue_id, 'title': bp['title'], 'depends_on': bp.get('depends_on', []),
        'original_evidence_class':bp.get('evidence_class'),
        'review': 'revalidated_actual_source', 'implementation': code_status,
        'acceptance': status, 'commits': commits, 'reviewed_source_pair':pair,
        'current_candidate_acceptance':'BLOCKED',
        'implementation_history_anchor': {name:history_anchors[name] for name in history_anchors},
        'implementation_commits': modification_commits,
        'modification_commits': modification_commits,
        'modification_commit_scope': 'Reachable implementation commits that touch the referenced source files after the recorded history anchor. Shared-file commits can contain changes for several breakpoints; the owner review and exact source references identify the change for this item.',
        'execution_source_snapshots': execution_snapshots,
        'execution_source_snapshot_status': 'RECORDED' if execution_snapshots else 'UNAVAILABLE_HISTORICAL_SOURCE_METADATA',
        'commit_missing': not bool(modification_commits),
        'no_modification_commit_reason': 'The original BP-027 is an explicit native QA/acceptance item, not a reproduced source defect. Its gameplay/respawn/next-world acceptance is incomplete; no independent source modification commit is invented for an unexecuted QA item.' if issue_id=='BP-027' and not modification_commits else None,
        'modification_note': 'Commits in this implementation branch that touched the referenced source files; source-pair anchors are recorded separately.',
        'evidence': evidence, 'owner_reviews': [dict(review,evidence=[entry for entry in review.get('evidence',[]) if 'private' not in Path(entry).name.lower()]) for review in reviews],
        'private_evidence_withheld':private_evidence_withheld,
        'acceptance_adjudication':adjudication,
        'full_native_acceptance': 'NOT_RUN',
        'native_gate': native_gate_snapshot,
        'unresolved_source_gap': protected_unfinished.get(issue_id,{}).get('gap'),
        'diagnostic_runtime_evidence': 'native-report.json retains actual one-client admission/release and bounded ABI diagnostics; these are not a complete Playable/three-player AC/E2E run'}

require(len(records) == 52, 'Validation failed in create_acceptance_ledger.py:188')
for case in acceptance['component_tests']:
    item = records[case['issue_id']]
    case['status'] = 'partial' if item['acceptance']=='PASS' else item['acceptance'].lower()
    case['result'] = {'commit_pair': None, 'reviewed_source_pair':pair,
        'execution_source_pair':None, 'implementation': item['implementation'],
        'historical_component_status':item['acceptance'],
        'current_candidate_acceptance':'BLOCKED',
        'commit_pair_scope':'Aggregate of separately recorded historical invocations; no complete execution source pair is inferred. This component conclusion cannot attest the final release candidate.',
        'environment': 'Windows Rust/C++ and real Steam client; Go 1.26.6 Windows/Linux; isolated PostgreSQL14 schemas47→48 and nonpersistent Redis. Each retained execution states its actual environment; no complete strict gameplay run.',
        'command_or_harness': 'See owner report tests and the referenced exact command/output logs.',
        'exit_code': None, 'exit_code_note': 'Multiple evidence commands; individual exit codes are preserved in owner report tests. This is not one executed test command.',
        'log_paths': [e['path'] for e in item['evidence']],
        'observed_result': [r.get('note', '') for r in item['owner_reviews']],
        'artifact_hashes': read(root/'artifact-inventory.json')['artifacts'] if (root/'artifact-inventory.json').is_file() else [],
        'artifact_hash_scope': 'Final candidate reference inventory; this does not assert that every historical component invocation used these final binaries. Actual per-run inputs remain in the execution reports.',
        'full_native_acceptance': 'NOT_RUN',
        'component_adjudication':item['acceptance_adjudication'],
        'preserved_host_fixture':item['acceptance_adjudication'].get('preserved_host_fixture') if item['acceptance_adjudication'] else None,
        'execution_source_snapshots':item.get('execution_source_snapshots',[]),
        'execution_source_snapshot_status':item.get('execution_source_snapshot_status'),
        'execution_source_binding':'Historical execution snapshots are retained verbatim; reviewed_source_pair is never copied into them.',
        'execution_records': [{'owner': owner, 'report': f'{owner}-report.json',
            'tests': report.get('tests', [])} for owner, report in reports.items()
            if owner in {r['owner'] for r in item['owner_reviews']}]}
for case in acceptance['e2e_tests']:
    case['status'] = 'not_run'
    case['result'] = {'commit_pair': None, 'reviewed_source_pair':pair,
        'execution_source_pair':None, 'exit_code': None, 'native_game_run': 'NOT_RUN',
        'current_candidate_acceptance':'NOT_RUN',
        'artifact_hashes': read(root/'artifact-inventory.json')['artifacts'] if (root/'artifact-inventory.json').is_file() else [],
        'environment': 'Available: one local Windows/Steam/game session and isolated PostgreSQL/Redis. Environment requirements differ by case; incomplete non-game scenarios are not attributed to missing Steam sessions.',
        'command_or_harness': None,
        'log_paths': [str(root/'native-report.json'), str(root/'native-blockers.md')],
        'observed_result': 'NOT_RUN: no complete execution of this E2E case. Native diagnostic runs are separately recorded and are not counted as this case passing.',
        'reason': 'No complete run matching all steps of this E2E case was executed. Component evidence and isolated game startup diagnostics are not a substitute.',
        'blocking_evidence': 'native-blockers.md', 'related_issue_evidence': case.get('issue_ids', [])}
    executed_path = root/(case['id'].lower()+'-result.json')
    if executed_path.is_file():
        executed = read(executed_path)
        require(executed['id'] == case['id'], 'Validation failed in create_acceptance_ledger.py:223')
        executed_status = str(executed['status']).lower()
        require(executed_status in ('pass','partial','blocked','not_run','fail'), 'Validation failed in create_acceptance_ledger.py:225')
        actual = executed['result']
        required_actual = {'environment','command_or_harness','exit_code','log_paths','observed_result'}
        require(required_actual.issubset(actual), executed_path)
        if executed_status == 'pass':
            require(actual['exit_code'] == 0 and executed.get('steps'), 'Validation failed in create_acceptance_ledger.py:230')
            require(all(step['status'].lower() == 'pass' for step in executed['steps']), 'Validation failed in create_acceptance_ledger.py:231')
            require(len(executed['steps']) >= len(case['steps']), 'Validation failed in create_acceptance_ledger.py:232')
        execution_pair = execution_pair_from_report(executed)
        execution_snapshot = execution_source_snapshot(executed)
        step_evidence_gaps=gate._execution_step_problems(case,{'execution_steps':executed.get('steps',[])},executed_path) if executed_status=='pass' else []
        dirty=execution_snapshot.get('source_dirty')
        source_clean=(dirty is False or
                      (isinstance(dirty,dict) and set(dirty) == {'ProjectRebound', 'Toolbox'} and all(value is False for value in dirty.values())))
        source_and_steps_bound=(execution_pair==pair and source_clean and not step_evidence_gaps)
        case['status'] = 'partial' if executed_status=='pass' and not source_and_steps_bound else executed_status
        case['result'] = {**actual, 'commit_pair':execution_pair,
            'execution_source_pair':execution_pair, 'reviewed_source_pair':pair,
            'execution_source_snapshot':execution_snapshot,
            'execution_source_binding':'Exact execution source is retained from the dedicated report; incomplete/dirty historical source cannot bind the current candidate.',
            'historical_execution_status':executed_status,
            'current_candidate_acceptance':'BLOCKED' if not source_and_steps_bound else executed_status.upper(),
            'step_evidence_gaps':step_evidence_gaps,
            'source_binding_note':'Dedicated reports retain their actual historical source. Missing or different sources cannot establish acceptance of the final candidate.',
            'artifact_hashes':read(root/'artifact-inventory.json')['artifacts'],
            'artifact_hash_scope':'Final candidate reference inventory; actual test input binaries and source are recorded in the dedicated execution report.',
            'execution_report':{'path':str(executed_path),'sha256':sha(executed_path)},
            'execution_steps':executed['steps']}
        report_binding_gaps=gate._execution_report_problems(case,case['result'],case['result']['execution_report'],executed_path,pair) if executed_status=='pass' else []
        case['result']['execution_report_binding_gaps']=report_binding_gaps
        if report_binding_gaps:
            case['status']='partial'
            case['result']['current_candidate_acceptance']='BLOCKED'
acceptance.update(execution_status='partial_not_release_ready', generated_at=now,
    generation_id=generation_id,
    component_status_scope='Current candidate statuses; historical component conclusions are separately retained in result.historical_component_status and the progress evidence ledger.',
    original_contract={'commit':'5bb32ba','breakpoints_sha256':original_breakpoints_sha,'acceptance_tests_sha256':original_acceptance_sha},
    source_commits=pair, commit_pair=pair, release_ready=False,
    implementation_history_anchors=history_anchors,
    native_gate=native_gate_snapshot,
    protected_unfinished=protected_unfinished,
    source_commits_scope='Reviewed implementation/source-build anchors. Historical per-run inputs retain their own records and are not retroactively relabeled as this source pair.',
    artifact_hashes=read(root/'artifact-inventory.json')['artifacts'] if (root/'artifact-inventory.json').is_file() else [],
    release_blockers=[
        {'kind':'implementation_and_native_validation', 'reason':'Real one-client native admission and authority-only owned-process cleanup are observed; full Playable, dynamic serializer ownership coverage and teardown/reuse acceptance remain incomplete.', 'evidence':'native-blockers.md'},
        {'kind':'environment', 'reason':'Two additional simultaneous Steam test sessions/machines have not been supplied for the required three-player matrix.'},
        {'kind':'acceptance', 'reason':'The complete 52-component / 22-E2E matrix is not passed. Individual unexecuted/skipped cases are preserved.'},
        {'kind':'release', 'reason':'Production signing, full artifact compatibility acceptance and deployment have not been performed.'},
    ])
(root/'acceptance-tests.json').write_text(json.dumps(acceptance, ensure_ascii=False, indent=2)+'\n', encoding='utf-8')
progress = {'schema_version': '2.0', 'target': 'strict_authoritative_online_only', 'generated_at': now,
    'generation_id':generation_id,
    'status': 'implementation_in_progress_with_explicit_blockers', 'release_ready': False,
    'source_commits': pair, 'summary': dict(Counter(v['acceptance'] for v in records.values())),
    'implementation_history_anchors': history_anchors, 'native_gate': native_gate_snapshot,
    'protected_unfinished': protected_unfinished,
    'issue_statuses': records}
missing = [(key, item['path']) for key, record in records.items() for item in record['evidence'] if not item['exists']]
require(not missing, f'Evidence paths do not exist: {missing}')
(root/'progress.json').write_text(json.dumps(progress, ensure_ascii=False, indent=2)+'\n', encoding='utf-8')
rows = ['# 52 项实施与验收记录', '',
    '当前严格在线目标尚未完成验收，不能作为可发布版本。实现、组件测试与真实原生对局分别记录；没有用关闭严格名单、空 Grant 或 direct open 放行在线入场。', '',
    f'源码记录：ProjectRebound `{pair["ProjectRebound"]}`；Toolbox `{pair["Toolbox"]}`。逐项机器可读记录见 [progress.json](progress.json)，52 项组件与 22 项 E2E 结果见 [acceptance-tests.json](acceptance-tests.json)。', '',
    '组件 PASS 仅表示对应历史组件执行满足该条判据；每次实际源码和日志均保留。它不代表最终五个候选产物通过验收；当前候选状态单独记录。','',
    '| 断点 | 内容 | 实现 | 实际组件证据 | 当前候选 | 修改提交与证据 |', '|---|---|---|---|---|---|']
for item in records.values():
    refs = ', '.join(f'{k}: ' + ' / '.join(f'`{v[:8]}`' for v in values) for k,values in item['modification_commits'].items())
    if not refs: refs = '源码复核项；参见记录的提交对与 owner 说明'
    owners = ', '.join(f'[{r["owner"]}报告]({r["owner"]}-report.json)' for r in item['owner_reviews'])
    rows.append(f'| {item["id"]} | {item["title"]} | {item["implementation"]} | {item["acceptance"]} | {item["current_candidate_acceptance"]} | {refs}; {owners} |')
rows += ['', '具体未完成的 ABI/运行环境证据及下一步见 [native-blockers.md](native-blockers.md)。原始交接契约保持在 TARGET_CONTRACTS.md / IMPLEMENTATION_PLAN.md / breakpoints.json；其原始状态不作为本次执行结果。']
(root/'IMPLEMENTED.md').write_text('\n'.join(rows)+'\n', encoding='utf-8')
print(json.dumps({'issues':len(records), 'components':len(acceptance['component_tests']), 'e2e':len(acceptance['e2e_tests']), 'statuses':progress['summary']}))
