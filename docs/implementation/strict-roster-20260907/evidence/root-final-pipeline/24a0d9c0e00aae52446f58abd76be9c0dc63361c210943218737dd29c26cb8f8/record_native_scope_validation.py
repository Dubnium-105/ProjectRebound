import hashlib
import json
import re
import shutil
import subprocess
from datetime import datetime, timezone
from pathlib import Path

repo = Path('C:/wksp/ProjectRebound')
private = repo / '.tmp/strict-roster-20260907/native-evidence'
root = repo / 'docs/implementation/strict-roster-20260907'
target = root / 'evidence/native-scope-runtime-20260908'
target.mkdir(parents=True, exist_ok=True)
sha = lambda p: hashlib.sha256(p.read_bytes()).hexdigest()
read = lambda p: json.loads(p.read_text(encoding='utf-8-sig'))
files = []

def retain(source, relative):
    destination = target / relative
    destination.parent.mkdir(parents=True, exist_ok=True)
    if destination.exists() and destination.read_bytes() != source.read_bytes():
        raise ValueError('Retained evidence changed: ' + str(destination))
    shutil.copyfile(source, destination)
    record = {'path': destination.relative_to(repo).as_posix(), 'sha256': sha(destination),
              'bytes': destination.stat().st_size, 'original_path': str(source)}
    files.append(record)
    return record

builds = []
for source in sorted(private.glob('root-validation-20260907T*Z/result.json')):
    if source.parent.name < 'root-validation-20260907T190000':
        continue
    value = read(source)
    commands = []
    for execution in value['executions']:
        log = Path(execution['log_path'])
        if sha(log) != execution['sha256']:
            raise ValueError('Build log changed: ' + str(log))
        commands.append({**execution, 'retained_log': retain(log, source.parent.name + '/' + log.name)})
    builds.append({'run_id': source.parent.name, 'source_input_commit': value['source_input_commit'],
                   'source_dirty': value['source_dirty'], 'executions': commands,
                   'artifact': value.get('artifact'), 'receipt': retain(source, source.parent.name + '/result.json'),
                   'scope': 'Actual component build/test only; not native admission acceptance.'})

runtime = []
bp038_negative_runs = []
for run in sorted((private / 'live-run-results').glob('dynamic-root-*Z')):
    if run.name < 'dynamic-root-20260907T190000':
        continue
    execution_path = run / 'execution-receipt.json'
    if not execution_path.is_file():
        continue
    execution = read(execution_path)
    if not execution.get('finished_at'):
        continue
    for summary in sorted(run.glob('dedicated-component-*/summary.json')):
        result = read(summary)
        frames_path = summary.parent / 'payload-frames.jsonl'
        frames = [json.loads(line) for line in frames_path.read_text(encoding='utf-8-sig').splitlines()]
        # Keep exact safe frames at every meaningful transition, plus the last
        # observation. Raw stdout and private fixture/Grant files are excluded.
        selected = []
        seen = set()
        for frame in frames:
            key = (frame.get('label'), frame.get('status'), frame.get('code'),
                   frame.get('match_state'), frame.get('match_last_error'),
                   frame.get('match_login_completed'), frame.get('match_login_ready'),
                   frame.get('match_scope_verified'), frame.get('match_native_grant_injected'),
                   tuple((event.get('sequence'), event.get('state')) for event in frame.get('events', [])))
            if key not in seen:
                selected.append(frame)
                seen.add(key)
        if frames and frames[-1] not in selected:
            selected.append(frames[-1])
        bp038_result_path = run / 'bp038-tampered-grant-result.json'
        bp038_result = None
        bp038_retained = None
        if bp038_result_path.is_file():
            bp038_result = read(bp038_result_path)
            # This is a safe receipt: it contains only hashes, result codes,
            # counts, and explicit nulls.  It must remain separate from the
            # ordinary positive one-client execution record because the
            # tampered token did not reach the Grant/NMT stage in this run.
            bp038_retained = retain(
                bp038_result_path,
                run.name + '/bp038-tampered-grant-result.json')
            bp038_negative_runs.append({
                'run_id': run.name,
                'source_receipt': bp038_retained,
                'acceptance': bp038_result.get('acceptance'),
                'status': bp038_result.get('status'),
                'tamper_mode': bp038_result.get('tamper_mode'),
                'candidate_payload_sha256': bp038_result.get('candidate_payload_sha256'),
                'client_status_code': bp038_result.get('client_status_code'),
                'client_login_ready': bp038_result.get('client_login_ready'),
                'grant_marker_reached': bp038_result.get('grant_marker_reached'),
                'tampered_native_rejection_code': bp038_result.get('tampered_native_rejection_code'),
                'reserve_observed': bp038_result.get('reserve_observed'),
                'native_admitted_observed': bp038_result.get('native_admitted_observed'),
                'connected_observed': bp038_result.get('connected_observed'),
                'executions': bp038_result.get('executions', []),
                'restore_check_path': bp038_result.get('restore_check_path'),
                'restore_check_sha256': bp038_result.get('restore_check_sha256'),
                'restored_payload_sha256': bp038_result.get('restored_payload_sha256'),
                'owned_boundary_process_count': bp038_result.get('owned_boundary_process_count'),
                'scope': 'One real Steam client; wrong-audience Grant was prepared only in the private harness. No negative PASS is claimed when the fixed capability gate blocks before Grant delivery.'
            })
        probe_log = summary.parent / 'authority.stdout.private.raw.log'
        if not probe_log.is_file():
            probe_log = summary.parent / 'authority.stdout.raw.log'
        probes = []
        if probe_log.is_file():
            probes = [line for line in probe_log.read_text(encoding='utf-8-sig', errors='replace').splitlines()
                      if line.startswith(('[STRICT-ROSTER] identity_probe ', '[STRICT-ROSTER] PreLogin '))]
            for name in ('steam-platform-id-private.txt', 'native-player-id-private.txt'):
                secret = (private / name).read_text(encoding='utf-8-sig').strip()
                if secret and any(secret in line for line in probes):
                    raise ValueError('Native diagnostic line contains a private identity')
            probes = [re.sub(r'\bnonce=([A-Za-z0-9_-]+)',
                             lambda match: 'nonce_sha256=' + hashlib.sha256(match[1].encode()).hexdigest(),
                             line) for line in probes]
        runtime.append({'run_id': run.name, 'execution_receipt_sha256': sha(execution_path),
                        'source_input_commit': execution.get('source_input_commit'),
                        'backend_tree': execution.get('backend_tree'),
                        'backend_source_dirty': execution.get('backend_source_dirty'),
                        'candidate_build_receipt_sha256': execution.get('candidate_build_receipt_sha256'),
                        'candidate_payload_sha256': result['candidate_payload_sha256'],
                        'outcome': result['outcome'], 'failure': result.get('failure'),
                        'restored_payload_sha256': result.get('restored_payload_sha256'),
                        'summary': retain(summary, run.name + '/summary.json'),
                        'bp038_negative_result': bp038_retained,
                        'original_frames_sha256': sha(frames_path), 'selected_actual_frames': selected,
                        'identity_probe_lines': probes,
                        'native_acceptance_passed': result['outcome'] == 'PASS',
                        'scope': 'One real Steam client, isolated Backend and a synthetic second roster seat. Not three-player E2E.'})

# Keep the single old-candidate A/B login comparison as an immutable safe
# receipt.  It is separate from the dynamic-root positive/negative run list:
# the same 42C72B bytes reached login and native admission in the historical
# reference, but stopped before login-ready in the current environment.  This
# is a finite startup-boundary comparison, not a compatibility fallback or a
# release acceptance result.
ab_source = private / 'ab-old42-baseline-safe-receipt.json'
if not ab_source.is_file():
    raise ValueError('Missing old-42 A/B safe receipt: ' + str(ab_source))
ab_value = read(ab_source)
ab_retained = retain(ab_source, 'ab-old42-baseline-safe-receipt.json')
ab_current = ab_value.get('current_run', {})
ab_historical = ab_value.get('historical_run', {})
ab_compare = ab_value.get('comparison', {})
if ab_value.get('status') != 'BLOCKED' or ab_value.get('acceptance') != 'NOT_PASS':
    raise ValueError('Old-42 A/B receipt must remain BLOCKED/NOT_PASS')
if ab_current.get('candidate_sha256') != ab_historical.get('candidate_sha256'):
    raise ValueError('Old-42 A/B candidate bytes do not match')
if not ab_compare.get('same_candidate_bytes') or not ab_compare.get('same_wrapper_bytes'):
    raise ValueError('Old-42 A/B input identity is not bound')
if ab_current.get('wrapper_exit_code') != 0 or ab_current.get('driver_exit_code') != 1:
    raise ValueError('Old-42 A/B exit evidence changed')
if ab_current.get('restore', {}).get('installed_payload_sha256') != '6C7B5E05540AC72A6D7A9FA78F867917285FCE00081156EE13D237AA4D6C24A3':
    raise ValueError('Old-42 A/B restore hash changed')
if ab_current.get('restore', {}).get('owned_boundary_process_count') != 0:
    raise ValueError('Old-42 A/B still has an owned process')
ab_execution_source = private / 'live-run-results/dynamic-root-20260907T212120784508Z/execution-receipt.json'
ab_execution = read(ab_execution_source)
ab_source_binding = {
    'historical_reference_execution_receipt': {
        'path': str(ab_execution_source),
        'sha256': sha(ab_execution_source),
        'source_input_commit': ab_execution.get('native_source_input_commit'),
        'source_dirty': ab_execution.get('native_source_dirty'),
        'candidate_build_receipt': ab_execution.get('candidate_build_receipt'),
        'candidate_build_receipt_sha256': ab_execution.get('candidate_build_receipt_sha256'),
    },
    'historical_candidate_sha256': ab_historical.get('candidate_sha256'),
    'current_candidate_sha256': ab_current.get('candidate_sha256'),
    'related_297c_runs': [
        {
            'run_id': item['run_id'],
            'execution_receipt_sha256': item['execution_receipt_sha256'],
            'source_input_commit': item.get('source_input_commit'),
            'candidate_payload_sha256': item.get('candidate_payload_sha256'),
            'outcome': item.get('outcome'),
            'failure': item.get('failure'),
        }
        for item in runtime
        if str(item.get('candidate_payload_sha256', '')).lower() ==
        '297c6ea8585c8a606b3ab31a9949bcb153d7469b890ef8603521fb8af2cbfc1b'
    ],
}
ab_comparison = {
    'status': 'BLOCKED',
    'acceptance': 'NOT_PASS',
    'source_receipt': ab_retained,
    'source_binding': ab_source_binding,
    'classification': ab_compare.get('classification'),
    'canonical_reason': 'Current startup/auth boundary is unresolved. The finite A/B did not reproduce a failure unique to 297c; the same 42C72B candidate stopped before connectServer/login-ready in the current environment.',
    'mutation_executed': False,
    'guard_coverage': 0,
    'orchestration_exit_code': ab_current.get('wrapper_exit_code'),
    'finalize_exit_code': ab_current.get('driver_exit_code'),
    'restored_payload_sha256': ab_current.get('restore', {}).get('installed_payload_sha256'),
    'owned_boundary_process_count': ab_current.get('restore', {}).get('owned_boundary_process_count'),
    'historical_42_reference': {
        'run_id': ab_historical.get('run_id'),
        'candidate_payload_sha256': ab_historical.get('candidate_sha256'),
        'source_native_commit': ab_historical.get('source_native_commit'),
        'source_dirty': ab_historical.get('source_native_dirty'),
        'outcome': ab_historical.get('outcome'),
        'client_login_completed_count': ab_historical.get('login_completed_count'),
        'client_login_ready_count': ab_historical.get('login_ready_count'),
        'join_count': ab_historical.get('join_count'),
        'connected_confirmation_count': ab_historical.get('connected_confirmation_count'),
    },
    'current_42_observation': {
        'run_id': ab_current.get('run_id'),
        'candidate_payload_sha256': ab_current.get('candidate_sha256'),
        'outcome': ab_current.get('outcome'),
        'client_login_completed_count': ab_current.get('login_completed_count'),
        'client_login_ready_count': ab_current.get('login_ready_count'),
        'join_count': ab_current.get('join_count'),
        'authority_connection_event_count': ab_current.get('authority_connection_event_count'),
        'client_log': ab_current.get('client_log'),
    },
}

# Keep the BP047 cleanup evidence as a separate, safe index.  The source
# receipt contains only paths, hashes, scope metadata, and explicit nulls for
# exit codes that were not retained; private fixture/Grant/Ticket files are
# never copied into the public evidence tree.
bp047_source = private / 'bp047-final-safe-receipt.json'
bp047_reuse_source = private / 'bp047-reuse-guard-review.json'
bp047_wrapper_source = private / 'bp047-wrapper-config-comparison.json'
bp047_registration_source = private / 'bp047-registration-heartbeat-component.json'
bp047_guard_source = private / 'bp047-registration-guard-source-check.json'
bp047_capability_source = private / 'bp047-registration-capability-gate-component.json'
bp047_process_preflight_source = private / 'bp047-process-registration-preflight.json'
for source in (bp047_source, bp047_reuse_source, bp047_wrapper_source, bp047_registration_source, bp047_guard_source, bp047_capability_source, bp047_process_preflight_source):
    if not source.is_file():
        raise ValueError('Missing BP047 safe evidence: ' + str(source))
bp047_receipt = read(bp047_source)
bp047_reuse = read(bp047_reuse_source)
bp047_wrapper = read(bp047_wrapper_source)
bp047_registration = read(bp047_registration_source)
bp047_guard = read(bp047_guard_source)
bp047_capability = read(bp047_capability_source)
bp047_process_preflight = read(bp047_process_preflight_source)
expected_payload_sha = bp047_receipt['candidate_payload']['sha256'].lower()
expected_restored_sha = bp047_receipt['restored_payload']['expected_sha256'].lower()
installed_payload = repo / 'C:/Steam/steamapps/common/Boundary/ProjectBoundary/Binaries/Win64/Payload.dll'
if sha(installed_payload).lower() != expected_restored_sha:
    raise ValueError('Installed Payload restore hash changed')
appid = repo / 'C:/Steam/steamapps/common/Boundary/ProjectBoundary/Binaries/Win64/steam_appid.txt'
appid_sha = sha(appid)
expected_appid_sha = 'ddfe0e8d462af661f81db36589c39882dc0f2330785b5d80cd34f2f520ad618f'
if appid_sha.lower() != expected_appid_sha:
    raise ValueError('Installed steam_appid.txt restore hash changed')
process_check = subprocess.run(
    ['powershell.exe', '-NoProfile', '-Command',
     '(Get-Process -Name ProjectBoundarySteam-Win64-Shipping -ErrorAction SilentlyContinue).Count'],
    cwd=repo, capture_output=True, text=True, check=False)
if process_check.returncode != 0:
    raise ValueError('Owned Boundary process check failed')
owned_process_count = int((process_check.stdout or '0').strip() or '0')
if owned_process_count != 0:
    raise ValueError('An owned Boundary process is still running')
static_dir = root / 'evidence/serializer-static-callees-20260908'
static_files = []
for name in ('FINDINGS.md', 'binary-binding.json', 'ida-results.json'):
    source = static_dir / name
    if not source.is_file():
        raise ValueError('Missing fixed-image serializer evidence: ' + str(source))
    static_files.append(retain(source, 'serializer-static-callees-20260908/' + name))
bp047_index = {
    'status': 'PARTIAL',
    'acceptance': 'BLOCKED',
    'scope': 'Authority-only native cleanup and owned-process exit evidence. Reuse/READY and full game teardown were not accepted.',
    'safe_receipt': retain(bp047_source, 'bp047/bp047-final-safe-receipt.json'),
    # The earlier copy is retained as historical evidence; the registration
    # path clarification is a new immutable version rather than an overwrite.
    'reuse_guard_review': retain(bp047_reuse_source, 'bp047/bp047-reuse-guard-review-v2.json'),
    'wrapper_config_review': retain(bp047_wrapper_source, 'bp047/bp047-wrapper-config-comparison.json'),
    'registration_component': retain(bp047_registration_source, 'bp047/bp047-registration-heartbeat-component.json'),
    'scheduling_guard_source_check': retain(bp047_guard_source, 'bp047/bp047-registration-guard-source-check.json'),
    'capability_gate_component': retain(bp047_capability_source, 'bp047/bp047-registration-capability-gate-component.json'),
    'process_registration_preflight': retain(bp047_process_preflight_source, 'bp047/bp047-process-registration-preflight.json'),
    'registration_path_review': {
        'canonical_flow': bp047_reuse['registration_path_review']['canonical_flow'],
        'fixture_run_source': bp047_reuse['registration_path_review']['fixture_run_source'],
        'credential_available_to_this_run': bp047_reuse['registration_path_review']['credential_available_to_this_run'],
        'registration_flow_executed': bp047_reuse['registration_path_review']['registration_flow_executed'],
        'environment_observation': bp047_reuse['registration_path_review']['environment_observation'],
        'blocking_classification': bp047_reuse['registration_path_review']['blocking_classification'],
    },
    'executions': bp047_receipt['executions'],
    'candidate_payload_sha256': expected_payload_sha,
    'restored_payload_sha256': sha(installed_payload),
    'steam_appid_restored_sha256': appid_sha,
    'owned_boundary_process_count': owned_process_count,
    'restoration_checks': {
        'payload_path': str(installed_payload),
        'payload_expected_sha256': expected_restored_sha,
        'payload_observed_sha256': sha(installed_payload),
        'appid_path': str(appid),
        'appid_expected_sha256': expected_appid_sha,
        'appid_observed_sha256': appid_sha,
        'owned_process_command_exit_code': process_check.returncode,
        'owned_process_count': owned_process_count,
    },
    'notes': [
        '74000 reached service cleanup_state=CLEARED after exact owned-process exit, but server=UNHEALTHY prevented READY reuse.',
        'A fresh isolated gameserver service test executed instance-bound registration, real gst_ credentials, node certificate issuance, signed heartbeat, and fail-closed legacy capability clearing (PASS_COMPONENT_ONLY). The 74000 authority-only fixture still did not attach that registration worker to an owned Payload process, so READY reuse remains unaccepted.',
        'The process-level registration/heartbeat read of a real Payload capability remains unexecuted; this is a concrete harness scope gap, not a claim that the isolated test CA or service path is unavailable.',
        'Missing retained exit codes remain JSON null; no orchestration zero was promoted to native acceptance.',
        'No raw private identity, Grant, Ticket, or fixture JSON was copied into this evidence tree.',
    ],
}
serializer_static = {
    'commit': 'd1431e206097a9aeb3e7fc2c01ae5dedc51383c9',
    'fixed_executable_sha256': '181c49ffb522b3eb01014c84fd9d3a2a5c0b66ae80a6a6addff4bdd6f8125843',
    'evidence': static_files,
    'conclusion': 'Static fixed-image evidence supports synchronous borrowed FString validity and no retain/free in this field2 archive writer call graph only. It does not prove dynamic serializer execution, full Grant negative coverage, Playable, or release readiness.',
    'dynamic_acceptance': 'NOT_PROVEN',
}

report = {'recorded_at': datetime.now(timezone.utc).isoformat(), 'builds': builds,
          'real_native_runs': runtime, 'bp038_negative_runs': bp038_negative_runs,
          'ab_comparison': ab_comparison,
          'bp047_cleanup': bp047_index,
          'serializer_static_review': serializer_static,
          'retained_files': files, 'release_ready': False,
          'interpretation': 'Only recorded executions are included. Failed builds, blocked runs, and skipped tests retain their actual outcomes. Exit zero from the orchestration script is not admission PASS.'}
(target / 'receipt.json').write_text(json.dumps(report, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')
print(json.dumps({'receipt': str(target / 'receipt.json'), 'builds': len(builds),
                  'completed_native_runs': len(runtime), 'release_ready': False}))
