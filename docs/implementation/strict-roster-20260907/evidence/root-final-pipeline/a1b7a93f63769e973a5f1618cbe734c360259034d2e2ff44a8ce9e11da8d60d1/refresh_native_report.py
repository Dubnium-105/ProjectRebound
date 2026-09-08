"""Refresh current Native review without relabeling historical executions."""
import hashlib
import json
import re
import shutil
import subprocess
from datetime import datetime, timezone
from pathlib import Path

repo = Path('C:/wksp/ProjectRebound')
root = repo/'docs/implementation/strict-roster-20260907'
private = repo/'.tmp/strict-roster-20260907/native-evidence'
read = lambda p: json.loads(p.read_text(encoding='utf-8-sig'))
sha = lambda p: hashlib.sha256(p.read_bytes()).hexdigest()
history = root/'evidence/native-review-history/native-report-before-final-refresh.json'
history.parent.mkdir(parents=True, exist_ok=True)
if not history.is_file():
    shutil.copyfile(root/'native-report.json', history)
old = read(history)
receipt_path = root/'evidence/native-scope-runtime-20260908/receipt.json'
receipt = read(receipt_path)
ab_comparison = receipt.get('ab_comparison')
if not ab_comparison:
    raise ValueError('Current component receipt is missing the old-42 A/B comparison')
ab_source = repo / ab_comparison['source_receipt']['path']
if not ab_source.is_file() or sha(ab_source) != ab_comparison['source_receipt']['sha256']:
    raise ValueError('Old-42 A/B safe receipt changed: ' + str(ab_source))
if (ab_comparison.get('status') != 'BLOCKED' or
        ab_comparison.get('acceptance') != 'NOT_PASS' or
        ab_comparison.get('mutation_executed') is not False or
        ab_comparison.get('guard_coverage') != 0 or
        ab_comparison.get('orchestration_exit_code') != 0 or
        ab_comparison.get('finalize_exit_code') != 1 or
        ab_comparison.get('owned_boundary_process_count') != 0 or
        ab_comparison.get('restored_payload_sha256') != '6C7B5E05540AC72A6D7A9FA78F867917285FCE00081156EE13D237AA4D6C24A3'):
    raise ValueError('Old-42 A/B canonical safety fields changed')
commit = subprocess.check_output(['git', 'log', '-1', '--format=%H', '--', 'Payload'], cwd=repo, text=True).strip()
builds = []
for item in receipt['builds']:
    if (not item.get('artifact') or item.get('source_input_commit') != commit or
            item.get('source_dirty') is not False or
            not all(command['exit_code'] == 0 for command in item['executions'])):
        continue
    source_receipt = repo/item['receipt']['path']
    if sha(source_receipt) != item['receipt']['sha256']:
        raise ValueError('Retained current Native build receipt changed')
    for command in item['executions']:
        log = repo/command['retained_log']['path']
        if sha(log) != command['retained_log']['sha256']:
            raise ValueError('Retained current Native build log changed')
    observed = read(source_receipt)
    if (observed.get('source_input_commit') != commit or observed.get('source_dirty') is not False or
            {command['name'] for command in observed['executions']} != {'configure', 'tests-build', 'ctest', 'payload-build'} or
            not all(command['exit_code'] == 0 for command in observed['executions'])):
        raise ValueError('Current Native receipt does not prove the complete clean-source build')
    builds.append(item)
if not builds:
    raise ValueError('No successful build from the current committed Payload source')
build = builds[-1]
execution = read(repo/build['receipt']['path'])
artifact = execution['artifact']
if sha(Path(artifact['path'])) != artifact['sha256']:
    raise ValueError('Frozen native artifact changed')
if subprocess.check_output(['git', 'status', '--porcelain', '--', 'Payload'], cwd=repo, text=True):
    raise ValueError('Cannot describe uncommitted Payload as the current committed candidate')
bp047 = receipt.get('bp047_cleanup')
if not bp047:
    raise ValueError('Current component receipt is missing the BP047 cleanup index')
bp038_negative = receipt.get('bp038_negative_runs', [])
for negative in bp038_negative:
    retained = negative.get('source_receipt')
    if not retained:
        raise ValueError('BP038 negative receipt is missing its retained safe source')
    retained_path = repo / retained['path']
    if not retained_path.is_file() or sha(retained_path) != retained['sha256']:
        raise ValueError('Retained BP038 negative receipt changed: ' + str(retained_path))
bp038_addendum_path = root/'evidence/native-scope-runtime-20260908/dynamic-root-20260908T001313550949Z/bp038-tampered-grant-addendum.json'
if not bp038_addendum_path.is_file():
    raise ValueError('Missing BP038 causal addendum: ' + str(bp038_addendum_path))
bp038_addendum = read(bp038_addendum_path)
if bp038_addendum.get('source_receipt', {}).get('sha256') != '207fdd7c04354fbd3d5fd4a34cebfe2c1d5fe452ecd618ce574302beb50fdca8':
    raise ValueError('BP038 causal addendum does not bind the retained source receipt')
if bp038_addendum.get('mutation_executed') is not False or bp038_addendum.get('guard_coverage') != 0:
    raise ValueError('BP038 causal addendum must record zero mutation/guard coverage')
bp038_login_observation_path = root/'evidence/native-scope-runtime-20260908/dynamic-root-20260908T003928442253Z/login-boundary-observation.json'
if not bp038_login_observation_path.is_file():
    raise ValueError('Missing BP038 login-boundary observation: ' + str(bp038_login_observation_path))
bp038_login_observation = read(bp038_login_observation_path)
if bp038_login_observation.get('acceptance') != 'BLOCKED' or bp038_login_observation.get('execution', {}).get('grant_mutation_executed') is not False:
    raise ValueError('BP038 login-boundary observation must remain blocked with no mutation')
bp038_negative_report = []
for negative in bp038_negative:
    value = dict(negative)
    if negative.get('run_id') == bp038_addendum.get('run_id'):
        value.update({
            'mutation_executed': False,
            'guard_coverage': 0,
            'canonical_status': bp038_addendum['canonical_status'],
            'causal_interpretation': 'client login-ready boundary was not reached; the configured wrong-audience mutation did not execute',
        })
    bp038_negative_report.append(value)
serializer_static_dir = root/'evidence/serializer-static-callees-20260908'
serializer_static_commit = 'd1431e206097a9aeb3e7fc2c01ae5dedc51383c9'
if subprocess.run(['git', 'cat-file', '-e', serializer_static_commit + '^{commit}'], cwd=repo, check=False).returncode != 0:
    raise ValueError('Serializer static evidence commit is not present')
serializer_static_files = []
for name in ('FINDINGS.md', 'binary-binding.json', 'ida-results.json'):
    path = serializer_static_dir/name
    if not path.is_file():
        raise ValueError('Missing serializer static evidence: ' + str(path))
    serializer_static_files.append({'path': str(path), 'sha256': sha(path), 'bytes': path.stat().st_size})
static_review = {
    'commit': serializer_static_commit,
    'fixed_executable_sha256': '181c49ffb522b3eb01014c84fd9d3a2a5c0b66ae80a6a6addff4bdd6f8125843',
    'evidence': serializer_static_files,
    'conclusion': 'Fixed-image field2 archive call graph supports borrowed FString validity only during the synchronous save call and shows no retain/free path for that call graph. It does not prove dynamic serializer execution or product-wide ownership.',
    'dynamic_acceptance': 'NOT_PROVEN',
}
tests = []
for item in receipt['builds']:
    for command in item['executions']:
        log = repo/command['retained_log']['path']
        text = log.read_text(encoding='utf-8-sig', errors='replace')
        count = re.search(r'0 tests failed out of (\d+)', text) if command['name'] == 'ctest' else None
        tests.append({'command': command['command'], 'exit_code': command['exit_code'],
                      'pass_count': int(count[1]) if count else None,
                      'log_path': str(log), 'sha256': command['retained_log']['sha256'],
                      'source_input_commit': item['source_input_commit'], 'source_dirty': item['source_dirty'],
                      'execution_receipt': item['receipt'],
                      'scope': 'Actual Native component build/test. No game or release PASS implied.'})
issues = old['issues']
notes = {
    'BP-022': 'Actual Windows CommandFramework producer emits the complete preserved HOST scope/operation/nonce. Rust consumes the exact 562-byte fixture and rejects missing/tampered fields. This is a cross-language pipe fixture, not game E2E.',
    'BP-038': 'Commit d1431e2 static fixed-image evidence supports synchronous borrowed FString validity and no retain/free in the pinned field2 archive writer call graph only. The retained 297c6 wrong-audience receipt is corrected by a bound addendum: mutation_executed=false and guard_coverage=0 because the client stopped before login-ready/Grant delivery. The first run reached authority-ready and 227 native_admission_unverified polls; the follow-up diagnostic run reached authority-ready and 229 such polls, with safe MetaTunnel profile preflight but no game-side connectServer/loadouts request or UMG_MainMenuBase_C.Construct in either run. No tampered Grant rejection, NMT_Login, Reserve, NATIVE_ADMITTED, or CONNECTED event was observed. This is BLOCKED coverage, not a negative PASS; Grant/Ticket carrier and game admission evidence do not substitute for dynamic serializer ownership.',
    'BP-039': 'Real fixed-game Steam possession validation, PreLogin reservation, native admission/readback, Backend Reserve/Confirm, CONNECTED and exact DISCONNECTED/release were observed with one real client. Full Playable remains blocked: online spawning requires at least two real players and this environment supplies one. No minimum-player/PvE bypass was made.',
    'BP-040': 'P2P local HOST binds native Player.ID separately from the real initialized SteamUser identity and requires signed authority allocation. Same-world preserved HOST scope is covered in components. A legal real P2P HOST positive run remains unrun.',
    'BP-041': 'Reservation, live connection and last-disconnected JTI/nonce/world/route are independent. Old live A disconnect preserves newer reserved B Backend/native proof; stale or mixed JTI/nonce is rejected. C++ components pass. A full real multi-client reconnect matrix remains unrun.',
    'BP-042': 'Grant/Ticket parsing is fail-closed, current signed JTI is rechecked after Steam callback, and staged, reserved and live credentials are independently scoped. Actual one-client admission/release is observed; complete real retry/revocation matrix remains unrun.',
    'BP-043': 'Route refresh preserves a still-live same-world HOST original scope/nonce/operation and updates only authority readiness. MEMBER authorization refresh is separate. Backend and Rust/C++ component route 1→2→3 evidence is retained; real multi-client HOST recovery remains unrun.',
    'BP-044': 'Clear/release validates exact allocation and native ownership. Old live disconnect cleans only its own scope and cannot clear a newer pending admission. Real one-client disconnect/release is observed; full delayed-stale-clear/reuse matrix remains unrun.',
    'BP-047': '74000 authority-only evidence reaches exact owned-process exit and service cleanup_state=CLEARED, while that private fixture leaves the server UNHEALTHY because its owned Payload process was not attached to the canonical registration worker. A separate fresh isolated gameserver service test PASS_COMPONENT_ONLY exercised instance-bound registration, real gst_ credentials, signed heartbeat, and fail-closed legacy capability clearing; the Toolbox capability predicate test also passes with native_authority_admission_verified=false remaining ineligible. Process-level capability reporting and READY reuse remain unexecuted. No SQL READY or synthetic process proof was used.'
}
for issue in issues:
    if issue['id'] in notes:
        issue['note'] = notes[issue['id']]
        issue['evidence'] = list(dict.fromkeys(issue.get('evidence', []) + [str(receipt_path), str(root/'evidence/host-route-preservation-20260908/receipt.json')]))
        if issue['id'] == 'BP-038':
            issue['evidence'] += [str(path) for path in serializer_static_dir.glob('*') if path.is_file()]
            issue['evidence'] += [str(bp038_addendum_path)]
            issue['evidence'] += [str(bp038_login_observation_path)]
            issue['evidence'] += [str(ab_source)]
        if issue['id'] == 'BP-047':
            issue['evidence'] += [str(repo/'Payload/Admission/StrictRosterCleanupReceipt.h'),
                                  str(repo/'Payload/Tests/StrictRosterCleanupReceiptTests.cpp'),
                                  str(repo/bp047['safe_receipt']['path']),
                                  str(repo/bp047['reuse_guard_review']['path']),
                                  str(repo/bp047['wrapper_config_review']['path']),
                                  str(repo/bp047['registration_component']['path']),
                                  str(repo/bp047['scheduling_guard_source_check']['path']),
                                  str(repo/bp047['capability_gate_component']['path']),
                                  str(repo/bp047['process_registration_preflight']['path'])]
        issue['acceptance'] = 'BLOCKED' if issue['id'] in ('BP-038', 'BP-039', 'BP-040', 'BP-047') else 'PARTIAL'
        if issue['id'] != 'BP-038' and issue['id'] != 'BP-047':
            issue['implementation'] = 'implemented'
        issue['evidence'] = [re.sub(r'(\.(?:cpp|h|rs)):\d+$', r'\1', p) for p in issue.get('evidence', [])]

for issue in issues:
    if issue['id'] == 'BP-038' and bp038_negative:
        issue['evidence'] = list(dict.fromkeys(
            issue.get('evidence', []) +
            [str(repo / item['source_receipt']['path']) for item in bp038_negative]))

# Keep source implementation and runtime acceptance independent for the two
# Native items owned by this report.  These commits identify the reviewed
# product changes; the acceptance fields below remain blocked where the real
# game/registration evidence is incomplete.
native_modifications = {
    'BP-038': {
        'implementation': 'implemented',
        'modification_commits': [
            '438d0ae082cc71c9f7b80b571208ea2806bff5bb',
            'c76ef07642b195d6229c7fcb10ca9188d0505be3',
            '3c19dda2fd5a50c4ad07afda310fa4079761b567',
        ],
        'modification_note': 'Fixed-image-bound NMT field2 carrier/ABI and scoped Grant/Steam admission code are present in these commits; dynamic serializer ownership and complete online acceptance remain unproven.',
    },
    'BP-047': {
        'implementation': 'implemented',
        'modification_commits': [
            'dc49017e488e8b5bf6f6544b4428c46d230c0abd',
            'dfef93d12d0c4d6dfae5eba7bfe176e5d15cb37d',
        ],
        'modification_note': 'Scoped teardown/replay receipt and owned-world cleanup code are present; authority-only closure was observed, but legal registration-backed READY reuse and full game teardown remain unaccepted.',
    },
}
for issue in issues:
    if issue['id'] in native_modifications:
        issue.update(native_modifications[issue['id']])

report = {'report_version': 'strict-roster-native-current-review-v6',
          'generated_at_utc': datetime.now(timezone.utc).isoformat(), 'repository': str(repo),
          'head': commit, 'commit': commit, 'scope': old['scope'],
          'source_state': 'Current Payload product is committed. Actual builds/runs retain their original commit plus dirty-file snapshots and are not relabeled as this commit.',
          'commit_note': 'Current review commit and historical execution inputs are separate.',
          'skills_and_contracts_read': old['skills_and_contracts_read'],
          'protocol': dict(old['protocol'], admission_order='Signed staged Grant + native Player.ID + Steam ticket possession proof -> native reservation event -> Backend Reserve receipt -> native Team/Camp readback -> Backend Confirm receipt -> CONNECTED -> exact-scope client Playable when native Pawn/NetConnection are ready.'),
          'binary_gate': dict(old['binary_gate'], payload_dll=artifact['path'], payload_sha256=artifact['sha256'],
                              payload_bytes=artifact['bytes'], payload_built_at_utc=execution['finished_at'],
                              strict_online_positive_acceptance='BLOCKED', native_authority_admission_verified=False),
          'artifacts': [dict(kind='Payload.dll_release_current', **artifact),
                        {'kind': 'current_build_receipt', 'path': str(repo/build['receipt']['path']), 'sha256': build['receipt']['sha256']}],
          'tests': tests, 'issues': issues,
          'actual_native_executions': receipt['real_native_runs'],
          'ab_comparison': ab_comparison,
          'bp038_negative_runs': bp038_negative_report,
          'bp038_negative_addenda': [{'path': str(bp038_addendum_path), 'sha256': sha(bp038_addendum_path),
                                     'mutation_executed': bp038_addendum['mutation_executed'],
                                     'guard_coverage': bp038_addendum['guard_coverage'],
                                     'acceptance': bp038_addendum['canonical_acceptance']}],
          'bp038_login_boundary_observations': [{'path': str(bp038_login_observation_path),
                                                 'sha256': sha(bp038_login_observation_path),
                                                 'acceptance': bp038_login_observation['acceptance'],
                                                 'grant_mutation_executed': bp038_login_observation['execution']['grant_mutation_executed'],
                                                 'wrong_audience_guard_coverage': bp038_login_observation['execution']['wrong_audience_guard_coverage']}],
          'native_capability_gate_review': {
              'source_refs': [
                  str(repo / 'Payload/dllmain.cpp') + ':389',
                  str(repo / 'Payload/Admission/StrictRosterAdmissionGate.h') + ':78',
                  str(repo / 'Payload/Hooks/Hooks.cpp') + ':4117',
                  str(repo / 'Payload/ClientLogic/ClientLogic.cpp') + ':1996',
                  str(repo / 'Payload/ClientLogic/ClientLogic.cpp') + ':2016',
              ],
              'locked_capability': 'BuildPayloadStatus uses constexpr nativeAuthorityAdmissionVerified=false and CanReportStrictOnlineReady requires it. The status gate is fail-closed and was not changed.',
              'login_call_chain': 'The client login readiness used by the private harness is independent: ClientProcessEvent detects UMG_MainMenuBase_C.Construct, calls the original ProcessEvent, then NotifyClientLoginCompleted sets loginCompleted and the two-second travel settle deadline; GetClientMatchStatus exposes login_completed/login_ready. A search of the current Payload source finds no client-login callsite consuming nativeAuthorityAdmissionVerified.',
              'successful_reference_run': 'dynamic-root-20260907T212120784508Z with 42C72B candidate observed the same safe payload_status code native_admission_unverified before later client_login_completed/login_ready, then real Grant staging/join and native events. Its exact old candidate/source/runtime receipt is retained separately and is not relabeled as current.',
              'latest_failed_run': 'dynamic-root-20260908T003928442253Z with 297C6 candidate reproduced the boundary: authority ready, 229 client status polls with native_admission_unverified, and no UMG_MainMenuBase_C.Construct/login completion; therefore it did not fetch or mutate the Grant. The earlier 001313 run had the same result with 227 polls.',
              'runtime_comparison': 'The retained 42C72 reference and both current 297c6 runs use the same fixed executable hash, AppID-file hash, startgame.ps1, Warehouse map, authority mode, authority/client flags, and accepted MetaTunnel preflight; only candidate hash, random pipes/ephemeral ports, and the private wrong-audience mode differ. The reference clientlog reached UMG_MainMenuBase_C.Construct, POST /connectServer=200, GET /v1/users/me/loadouts=200 and signed native join. Both current clientlogs contain the injection-hook/CMDFW/EnterGame markers but none of those login-completion or game-side MetaTunnel markers; the follow-up also records profile preflight=200 but no game-side connectServer/loadouts response or explicit ticket marker. This is a concrete local client startup/auth-boundary divergence, not evidence that the capability flag consumed client login and not a two-session blocker.',
              'conclusion': 'native_authority_admission_verified=false remains a genuine product capability blocker for strict readiness, but it does not by itself explain the successful reference login path and it is not consumed by the client login completion call chain. The latest wrong-audience case remains untested at the target guard: mutation_executed=false and guard_coverage=0 because the client did not reach UMG_MainMenuBase_C.Construct or Grant delivery. The follow-up reproduced the same no-coverage boundary and should end identical retries until the local startup/auth divergence is instrumented or corrected. Do not set the bit true or bypass the join gate.',
          },
          'bp047_cleanup': bp047,
          'serializer_static_review': static_review,
          'current_build_execution': execution,
          'historical_report': {'path': str(history), 'sha256': sha(history), 'scope': 'Historical review only; old paths and old current-candidate claims are superseded.'},
          'runtime_environment': old['runtime_environment'],
          'blockers_document': str(root/'native-blockers.md'),
           'status_summary': {'native_playable_acceptance': 'BLOCKED', 'three_real_player_matrix': 'NOT_RUN',
                             'serializer_dynamic_ownership_acceptance': 'PENDING_SEPARATE_OBSERVATION', 'release_ready': False},
          'release_ready': False,
  'runtime_blockers': ['One real Steam client is available; online MinPlayersToStart=2 prevents role selection/Pawn with one player. Three-player transport/lifecycle acceptance needs two additional real sessions.',
                       'The fixed 297c6 wrong-audience runs did not reach client login completion or Grant delivery: authority was ready, and the first/follow-up produced 227/229 native_admission_unverified polls with no tampered rejection code. The follow-up observed MetaTunnel profile preflight=200 but no game-side connectServer/loadouts response or UMG_MainMenuBase_C.Construct. The retained 42C72 reference used the same fixed runtime inputs and reached those boundaries; the current local startup/auth divergence is therefore concrete but unresolved. This is unexecuted target-guard coverage, not a negative PASS and not the two-session/playable blocker.',
                       'The single old-42 A/B baseline is retained separately: current 42C72B orchestration exited 0 while Backend finalize exited 1 after client login-ready timeout; mutation_executed=false and guard_coverage=0, with Payload restored to 6C7B and owned process count 0. The same 42C72B bytes historically reached connectServer/Join, so this finite comparison does not reproduce a 297c-only regression. It narrows the issue to an unresolved current startup/auth boundary without claiming the environment is otherwise fully excluded.',
                       'BP047 process-level reuse requires a fresh owned authority attached to the canonical instance-bound registration worker and signed heartbeat flow. The isolated gameserver service registration/heartbeat component test passed, but no owned Payload process was attached in that run, so process capability reporting and READY reuse remain unexecuted. No handwritten token or SQL READY write is acceptable.',
                       'Serializer ownership sidecar and full teardown/reuse matrix are separate required evidence; successful injection alone is insufficient.']}
ownership = []
for path in sorted((root/'evidence/final-supplements/native-evidence/live-run-results').glob('bp038-sidecar-*/bp038-sidecar-result.json')):
    value = read(path)
    ownership.append({'path': str(path), 'sha256': sha(path), 'status': value.get('status'),
                      'acceptance': value.get('acceptance'), 'coverage': value.get('coverage'),
                      'exit_codes': value.get('exit_codes'), 'failure': value.get('failure'),
                      'scope': 'Original read-only sidecar execution; zero shell exit is not a native acceptance PASS.'})
report['serializer_ownership_executions'] = ownership
if ownership:
    report['status_summary']['serializer_dynamic_ownership_acceptance'] = ownership[-1]['acceptance']
    for issue in report['issues']:
        if issue['id'] == 'BP-038':
            issue['evidence'] = list(dict.fromkeys(issue['evidence'] + [item['path'] for item in ownership]))
(root/'native-report.json').write_text(json.dumps(report, ensure_ascii=False, indent=2)+'\n', encoding='utf-8')
(root/'native-blockers.md').write_text('''# 原生验收状态

当前源码已包含严格 Grant、Steam Ticket、账号身份绑定、完整连接 scope 与后端回执链；真实单客户端运行已观察到 RESERVED、NATIVE_ADMITTED、CONNECTED、DISCONNECTED 和后端 release。组件测试与 DLL 构建结果、每次失败及后续修复见 native-report.json 的实际执行记录。

Playable 仍未通过。在线 Config.cpp 固定要求至少 2 名玩家才开始倒计时和选角，本环境只有 1 名真实 Steam 玩家；本次日志显示 1 次玩家连接、1 次初始 LateJoin 排队、0 次选角，客户端停留 WorldReady。合成的第二个冻结席位没有伪装成实际连接。未改 IsPvE、MinPlayersToStart、严格名单或原生 Token。

完整 3 人传输与生命周期矩阵还需要另外 2 个可同时运行的真实 Steam 会话。原生 HOST 恢复、最终清理 ACK/复用，以及 serializer 临时 FString 的动态 ownership 证明仍按独立执行证据判定；没有以组件 PASS 或注入成功代替。

固定 EXE 的 field2 serializer 静态 writer 证据已保留在 `docs/implementation/strict-roster-20260907/evidence/serializer-static-callees-20260908/FINDINGS.md` 及其绑定 JSON：它只证明该固定同步调用图内借用 FString 在调用期间未被 retain/free，不证明动态 serializer 覆盖或产品整体 ownership；BP038 动态验收仍未通过。

本轮固定 297c6 DLL 的私有 `wrong_audience` 诊断已真实启动 Dedicated authority 并完成 allocation/authority-ready；首次运行有 227 次、跟进诊断有 229 次 `native_admission_unverified`，均在 Grant 交付前停止，没有到达 `client-ready`、NMT_Login、Reserve、NATIVE_ADMITTED 或 CONNECTED，因此没有把它记为负向拒绝 PASS。绑定 addendum 和跟进 observation 明确 `mutation_executed=false`、`guard_coverage=0`，原始 receipt/hash 保留不改。跟进采样显示 authority/client MetaTunnel profile preflight=200，但客户端没有 game-side `POST /connectServer`、loadouts 或 `UMG_MainMenuBase_C.Construct`；对照 42C72 成功 run，固定 EXE、AppID 文件、startgame.ps1、地图/模式、客户端/authority 参数与 preflight 一致，旧 run 到达主菜单和 signed native join。当前是尚未定位的本地客户端启动/认证边界差异。源码调用链仍显示 `native_authority_admission_verified=false` 只参与严格 readiness gate；客户端登录完成由固定 `UMG_MainMenuBase_C.Construct` → `NotifyClientLoginCompleted` 独立设置，不能把差异归因该 flag，也不能把 flag 改为 true。已按同一零覆盖规则停止重复运行；下一步须先复现 login-ready 或记录具体启动/认证失败，再运行 wrong-audience 目标 guard。

本轮唯一的 42C72B A/B 对照使用同一冻结 DLL、wrapper 和 driver：当前 run 的 orchestration exit=0、Backend finalize exit=1，client 停在 `native_admission_unverified`，没有 Grant mutation 或目标 guard 覆盖；旧 42C72B 历史 run 到达 `/connectServer`、Join 与原生 admission。A/B receipt 保留两次的源码/制品哈希，canonical reason 是当前启动/认证边界尚未定位；这只是有限对照推论，不能声称环境已完全排除，也不把旧 DLL 当兼容回退。

BP047 的 74000 authority-only 执行在精确 owned 进程退出后观察到 cleanup_state=CLEARED，但该私有 fixture 没有把 owned Payload 进程接入实例绑定注册 worker，因此服务按安全规则保持 UNHEALTHY，READY 复用与旧 receipt 隔离未验收。本轮另有独立隔离 gameserver service test 已真实执行注册、gst_ 凭证、签名 heartbeat 和旧能力清除，状态为 PASS_COMPONENT_ONLY；仍缺的是将该合法注册链接到真实 owned Payload 进程并读取其 capability。下一步应复用隔离测试 CA/实例注册流程完成 process-level heartbeat、NativeCleared 和新 world 流程，禁止手写 token 或 SQL READY。

历史失败不会改写为通过，后续成功也只能覆盖其实际运行源码、DLL 和环境。
''', encoding='utf-8')
print(json.dumps({'current_payload_commit': commit, 'payload_sha256': artifact['sha256'], 'native_runs': len(receipt['real_native_runs']), 'release_ready': False}))
