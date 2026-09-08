import json
import re
from pathlib import Path

root = Path('C:/wksp/ProjectRebound/docs/implementation/strict-roster-20260907')
path = root / 'root-report.json'
report = json.loads(path.read_text(encoding='utf-8-sig'))
issues = {item['id']: item for item in report['issues']}

def update(number, note, evidence, implementation='implemented', acceptance='PARTIAL'):
    key = f'BP-{number:03}'
    item = issues.setdefault(key, {'id': key, 'review': 'revalidated_actual_source'})
    item.update(note=note, evidence=evidence, implementation=implementation,
                acceptance=acceptance, native_e2e='NOT_RUN')

update(1, 'Fixed game SHA256 and compile-time Payload SHA256 are checked before strict online launch/registration. Missing or malformed pin rejects the build; missing/replaced artifact regression uses actual file bytes. Source and artifact inventories are separate from native runtime acceptance. No native compatible run_id has been observed.', ['baseline.json', 'toolbox-final-all-features-test.log', 'backend-artifact-build.log'])
update(2, 'Actual loopback HTTP + isolated DPAPI: 20 concurrent 401s -> 1 refresh -> 20 successes. Logout while refresh response is held rejects the late response. A synthetic new-account atomic commit during an actual pending HTTP refresh preserves epoch8 and the new account credentials. These do not simulate or claim a Steam/native game login.', ['toolbox-auth-http-driver-build.log', 'toolbox-auth-http-concurrency-test.log', 'root-test-driver-artifacts.json'], acceptance='PASS')
update(3, 'Real killed writer after ciphertext flush/before atomic replacement recovers a complete decryptable old config and removes interrupted ciphertext temp files; actual concurrent stale preferences and credential updates retain the latest generation. Final full Rust suite runs these tests (no ignores).', ['toolbox-final-all-features-test.log'], acceptance='PASS')
update(5, 'Real synthetic WebSocket server verifies rotated credential on reconnect and snapshot resync. Captured account epoch is checked while connected and before reconnect. Producer/event byte and item bounds, stale queue purge, snapshot refresh, interruptible retry, and overflow stop are covered. Actual production WS/game reconnection remains unrun.', ['toolbox-final-all-features-test.log', 'toolbox-ws-rotation-test.log'])
update(6, 'Credential lifecycle registry records owner/purpose/storage/rotation/revocation without secret values. Actual HTTP dedicated-credential 401 causes zero player refreshes and cannot borrow the player session. Config and dedicated identities remove/redact secret Debug output; relay handshake buffers and keys zeroize. Full cross-purpose/native/administrator matrix remains unrun.', ['credential-registry.json', 'toolbox-auth-http-concurrency-test.log', 'toolbox-final-all-features-test.log'])
update(7, 'Managed attachments no longer mutate MatchLobby membership. Failed transport teardown retains ownership for retry and startup checks prerequisites before side effects. Full per-await failure-injection matrix with service/UI convergence remains unrun.', ['toolbox-final-all-features-test.log'], implementation='partial')
update(7, 'Managed attachments do not mutate MatchLobby membership. Backend connections created before a Legacy session are now retained immediately; failed close compensation retains the exact connection for shutdown retry and blocks a new attachment. Managed VNT Host/Member sessions are retained before startup and cleanup failure cannot discard them. VNT stop retains a locked secret file and session mutex until deletion succeeds; a real Windows exclusive-handle test observed the first failure and successful retry. This completes the reviewed local ownership paths; the full per-await cross-service E2E fault matrix remains unrun.', ['toolbox-prespawn-connection-close-test.log','toolbox-vnt-owned-cleanup-test.log','toolbox-final-all-features-test.log'])
update(11, 'Retired arbitrary room create/join paths return explicit errors; managed transport kind remains fixed and no cross-transport fallback is introduced. Native multi-client transport choice/retry matrix remains unrun.', ['toolbox-final-all-features-test.log'])
update(12, 'Actual UDP harness gives each remote peer its own host game socket/source port and routes replies only to that peer; removal preserves other mappings. Unreal Host+2 members acceptance remains unrun.', ['toolbox-final-all-features-test.log'])
update(13, 'Real Go Edge + Rust HOST and PEER binaries using different tokens exchanged bidirectional UDP payloads. Go-produced receiver-retagging vectors are decoded by real Rust; altered header/tag/payload and invalid MTU are rejected.', ['go-rust-live-relay-test.log', 'rust-relay-fixture-test.log', 'root-test-driver-artifacts.json'], acceptance='PASS')
update(14, 'Authentication/MTU/replay/header/backpressure counters and bounded 64-packet replay window are covered; authenticated limited reordering is allowed and forged packets cannot advance replay state. Long network impairment/game traffic acceptance remains unrun.', ['toolbox-final-all-features-test.log', 'go-rust-live-relay-test.log'])
update(15, 'Per-peer scoped migration stages next allocation and commits after local BIND. Blocking BIND is owned by a separate worker with deadline; existing routes continue. Actual slow-bind test passes; multi-client live Drain convergence and resources remain unrun.', ['toolbox-final-all-features-test.log'])
update(19, 'Windows pipe connection obtains real server PID and requires exact spawned/owned PID before sending any grant or allocation. Actual wrong-PID test confirms zero request bytes sent. Fixed game/compiled Payload pin rejects mismatched bytes. Separate real managed game/Payload diagnostics reached a pipe but strict readiness remained blocked; this is not a successful native admission run.', ['toolbox-real-windows-pipe-test.log', 'toolbox-final-all-features-test.log', 'native-report.json'])
update(22, 'Actual C++ EncodeFrame output (9 positive, 2 negative frames) consumed by real Rust parsers. Join request correlation is now mandatory; mismatched ID, busy, wrong command and stale cursors reject. accepted/queued means accepted, never Playable.', ['payload-wire-fixtures.jsonl', 'payload-wire-negative-fixtures.jsonl', 'cpp-rust-wire-test.log', 'toolbox-final-all-features-test.log'], acceptance='PASS')
update(23, 'Real Windows message-type named-pipe server tests cover fragmented/coalesced frames, per-frame limit, and bounded no-response deadline. Peek consumes exactly one frame and leaves subsequent bytes in the pipe. C++/Rust fixture parsing covers current protocol; real game commands/native failures remain unrun.', ['toolbox-real-windows-pipe-test.log', 'toolbox-final-all-features-test.log', 'cpp-rust-wire-test.log'])
update(29, 'MetaTunnel token pump captures initial session_epoch, checks during long waits and before writing replacement credentials, uses epoch-aware 401 refresh, and closes token stdin on failure/cancel. Seven MetaTunnel tests pass, including interruption of a 60-second refresh wait after an account change. Actual managed game preflight observed 401 -> refresh -> 200 separately. Full hot-rotation/EOF/parent-crash gameplay acceptance remains unrun.', ['toolbox-metatunnel-epoch-test.log', 'toolbox-final-all-features-test.log', 'native-report.json'])
update(31, 'Controller authority/material contexts use distinct identity and generation wrappers; HTTP/IPC conversion is explicit. Actual compile-fail doctests reject LobbyId as AttemptId and VntGeneration as RouteGeneration; deserialization rejects invalid identifiers/zero generations. Complete backend/native identity matrix remains separate.', ['toolbox-strong-id-compile-test.log', 'toolbox-final-all-features-test.log'])
update(51, 'All 52 issues have owner reports, source review and evidence references. Original acceptance matrix is preserved and final results separately identify actual PASS/PARTIAL/BLOCKED/NOT_RUN. Native/multi-machine scenarios are never inferred from component success.', ['implementation-evidence-index.json', 'progress.json'], implementation='implemented')
for number in (16, 17, 26, 44):
    update(number, 'LaunchOperation carries online role/Lobby/Attempt/session epoch, operation/run IDs, monotonic sequence and process-monotonic observation time. Cancellation targets the owned record even after the current lobby snapshot is gone, reaches the existing worker, is idempotent and does not fake a cleanup completion. An actual runtime regression verifies this ordering. Frontend account/operation/exit ordering tests are recorded by the Toolbox report. Complete per-phase native cancellation, cleanup and successive-attempt E2E remain unrun.', ['toolbox-runtime-operation-final-test.log', 'toolbox-final-all-features-test.log', 'toolbox-report.json'], implementation='partial')

update(4, 'Runtime serializes login/logout/redeem/session acquisition, revalidates the exact session epoch/token/SteamID before committing identity, and clears runtime identity even when remote logout fails. Account changes zeroize the old Steam ticket and AuthIdentity zeroizes it on drop. Account transition first drains player-owned launch/room/native work while its required credentials are still valid; cleanup failure retains that current session and returns AUTH_CLEANUP_PENDING before revocation. The frontend may restore only a freshly queried authenticated session for that explicit failure, never its old account snapshot after revocation. This complements CredentialsStore CAS and the executed concurrent HTTP tests; cross-machine native account-switch gameplay remains unrun.', ['toolbox-final-all-features-test.log','logs/toolbox-runtime-final-tests.log','evidence/frontend/auth-cleanup-final-ui.log','frontend-auth-cleanup-build.log'], implementation='implemented')
update(48, 'Toolbox requires the explicit accept_new_lobbies field separately from the strict_roster_v1 protocol capability. The Tauri view and React actions disable only creation while paused, preserve existing lobby listing/joining, display the pause reason, and refresh this policy with the lobby list. Backend startup/schema/admin-abort behavior is reported by its own executed E2E-19 record, not inferred from this UI change.', ['evidence/frontend/accept-new-final-bridge.log','frontend-accept-new-build.log','backend-report.json'])
issues['BP-004']['note'] += ' Public authenticated player requests retain the account-transition guard for the full operation. The independent match/status/room worker gates drain their in-flight network requests and compensation before account revocation. CommunityNode heartbeat retains separate ownership; its player-owned catalog sync uses a nonblocking transition guard and verifies the session epoch, while local stop needs no player token.'
for number in (16,17,26,44):
    issues[f'BP-{number:03}']['implementation'] = 'implemented'
    issues[f'BP-{number:03}']['note'] += ' Cleanup leases keep new starts blocked until owned game/transport/native cleanup finishes; retries perform the complete cleanup and failures remain scoped. Old IO cannot clear a newer receipt. A real duplicated Windows child handle supplies exited-process evidence for dead P2P authorities; a live or unverifiable process still requires an actual native pipe ACK. Pre-spawn controller/launcher failures retain exact Attempt scope and sealed not-started evidence. Every post-spawn failure retains a typed cleanup context; unavailable process proof remains quarantined rather than fabricating a not-started/exit receipt. The host_ready uncertainty retry first uses the exact world and permits empty-world owned-exit only after the specific backend MATCH_WORLD_INSTANCE_REQUIRED response.'
    issues[f'BP-{number:03}']['note'] += ' Final runtime shutdown retains the process-status scanner through KillTracked/resource reclamation, then sends its explicit Shutdown command and joins both that worker and its in-flight version probe. Account transitions do not stop the long-lived scanner. This lifecycle has a real-thread regression with a held synthetic version probe; native application-close acceptance remains separate.'
    issues[f'BP-{number:03}']['evidence'] += ['logs/toolbox-runtime-final-tests.log','C:/wksp/ProjectReboundToolbox/src/api/multiplayer/lobbies.rs','C:/wksp/ProjectReboundToolbox/src/launching/launch.rs','C:/wksp/ProjectReboundToolbox/src/server/registration.rs','C:/wksp/ProjectReboundToolbox/src/util/status_helper.rs','C:/wksp/ProjectRebound/Backend/internal/matchlobby/service.go']
update(49, 'Actual isolated PostgreSQL 14 and Redis 7.4.11 reproduced three deployment permission defects: Meta could not read schema names/checksums, PUBLIC still allowed DDL, and Redis denied the gate Lua command. Provisioning now grants only the required schema columns, removes PUBLIC CREATE, and enables scoped EVAL/EVALSHA. After retiring the Meta lifecycle writers, the real restricted-role test verifies schema checks, sixteen concurrent one-time consumers, nine SQL denials and five Redis namespace/administrator denials. Historical failed assertions are retained. The PostgreSQL lab uses trust authentication, so these are role-privilege checks, not password rejection. Production deployment and the full credential-purpose/native matrix remain separate.', ['meta-permissions-receipt.json','meta-retirement-receipt.json','e2e-21-result.json','C:/wksp/ProjectRebound/Backend/deployments/control-plane/provision-meta-postgres.sh','C:/wksp/ProjectRebound/Backend/deployments/control-plane/docker-compose.yaml','C:/wksp/ProjectRebound/Backend/internal/metaserver/permissions_integration_test.go'])
update(32, 'Source review found the old Meta scheduler and matchmaking/lifecycle repository writers were still callable. Commit ec4add769bb71362bccb1e57f9349d92f24c1747 deletes the scheduler and its server goroutine, rejects retired HTTP operations with 410 and native matchmaking RPCs with a correlated nonzero failure, and removes Meta database permissions to mutate the authoritative allocation/server lifecycle. Actual PostgreSQL regression observed zero old tickets/matches and unchanged READY server states; the actual restricted-role regression rejects these writes. Loadout access requires a strict Attempt projection. BattleLog correction and complete original E2E-01 remain separately tracked.', ['meta-retirement-receipt.json','e2e-01-result.json','C:/wksp/ProjectRebound/Backend/internal/metaserver/server.go','C:/wksp/ProjectRebound/Backend/internal/metaserver/repository.go','C:/wksp/ProjectRebound/Backend/internal/metaserver/tcp.go','C:/wksp/ProjectRebound/Backend/internal/metaserver/legacy_matchmaking_test.go'])
update(34, 'Commit 8c2b8c08891320ee08c9924cc7c23ee5112a3235 removes Meta BattleLog reverse lifecycle writes. Explicit online reports require mutually bound canonical Dedicated Attempt/projection and read platform identity/trust from the frozen roster. Running and completed late uploads work; premature and retired online reports reject. Eight concurrent uploads produce one stored report and seven duplicate receipts in each of three actual PostgreSQL race scenarios. PvE is non-official; no upload changes Attempt, Meta, server or party lifecycle. These are synthetic repository fixtures, not real game BattleLogs or full E2E.', ['battlelog-authority-receipt.json','C:/wksp/ProjectRebound/Backend/internal/metaserver/battlelog_repository.go','C:/wksp/ProjectRebound/Backend/internal/metaserver/battlelog_repository_integration_test.go','C:/wksp/ProjectRebound/Backend/internal/metaserver/repository.go','C:/wksp/ProjectRebound/Backend/deployments/control-plane/provision-meta-postgres.sh'])
issues['BP-032']['note'] += ' The subsequent BattleLog writer and frozen loadout binding correction is committed in 8c2b8c0 and verified under the deployed restricted role.'
issues['BP-032']['evidence'].append('battlelog-authority-receipt.json')
issues['BP-049']['note'] += ' The later BattleLog integration adds only required canonical Attempt/roster SELECT columns. Its actual rerun observes twelve SQL denials, five Redis denials and successful restricted-role report/loadout operations with Linux Go race enabled.'
issues['BP-049']['evidence'].append('battlelog-authority-receipt.json')
update(50, 'Toolbox cd09d7e7f769dc73e26ed7f38233b0a0a605e273 makes updater journal replacement durable, recovers interrupted pending files, checks original parent process creation time using an owned handle, and protects shared next/swap files with the exclusive updater lock. Actual PowerShell cases cover tamper, rollback, interruption and lock contention. Final artifact compatibility/signing acceptance remains separate; a correctly failing release gate does not count as release success.', ['e2e-20-result.json','toolbox-report.json','C:/wksp/ProjectReboundToolbox/src/install/self_update.rs','C:/wksp/ProjectRebound/Tools/Release/test_updater_transactions.py'])

toolbox = Path('C:/wksp/ProjectReboundToolbox')
source_map = {
    1:['src/security/strict_build.rs'], 2:['src/api/infrastructure/http.rs','src/security/auth.rs'],
    3:['src/config/config_types.rs'], 4:['src/security/auth.rs','src/core/runtime.rs','src/vnt/node_service.rs','frontend/src/App.jsx','frontend/src/lib/authTransition.js','frontend/tests/ui-contract.test.mjs'],
    5:['src/vnt/legacy/controller.rs'], 6:['src/config/config_types.rs','src/server/config.rs','src/server/registration.rs'],
    7:['src/vnt/rooms.rs','src/vnt/manager.rs','src/vnt/session.rs'], 9:['src/core/runtime.rs','src/vnt/rooms.rs'], 11:['src/vnt/rooms.rs','src/pages/launch.rs'],
    12:['src/vnt/legacy/controller.rs'], 13:['src/vnt/legacy/relay.rs'], 14:['src/vnt/legacy/relay.rs'],
    15:['src/vnt/legacy/controller.rs'], 16:['src/core/runtime.rs'], 17:['src/core/runtime.rs'],
    19:['src/server/pipe.rs','src/server/pipe_windows_tests.rs'], 22:['src/server/pipe.rs','src/server/pipe_cpp_fixture_tests.rs'],
    23:['src/server/pipe.rs','src/server/pipe_windows_tests.rs'], 26:['src/core/runtime.rs'],
    29:['src/launching/metatunnel.rs'], 31:['src/matchmaking/ids.rs','src/matchmaking/controller.rs'],
    44:['src/core/runtime.rs','frontend/src/lib/launchOperations.js'], 50:['src/install/self_update.rs','src/api/distribution/updates.rs'],
    48:['src/api/discovery/client_config.rs','src/core/runtime.rs','frontend/src/App.jsx','frontend/src/pages/LaunchPage.jsx','frontend/src/lib/tauriBridge.js','frontend/tests/tauri-bridge.test.mjs'],
}
for number, files in source_map.items():
    item=issues[f'BP-{number:03}']
    item['evidence']=list(dict.fromkeys(item['evidence']+[str(toolbox/p) for p in files]))
for number in (50, 51, 52):
    item=issues[f'BP-{number:03}']
    item['evidence']=list(dict.fromkeys(item['evidence']+['C:/wksp/ProjectRebound/Tools/Release/strict_roster_provenance.py']))
    item['evidence']=list(dict.fromkeys(item['evidence']+['release-proof-gate-receipt.json']))
    if 'cab759835a63f0dbff6a480b21dbeff993987091' not in item['note']:
        item['note'] += ' Release-proof commit cab759835a63f0dbff6a480b21dbeff993987091 validates full source dirtiness, real execution reports and contract-step coverage, five actual artifact digests, Payload source-tree binding and candidate/game manifests. Historical PASS cannot be relabeled as current-candidate acceptance. Final gate regressions run 19 tests; they do not execute or approve a release.'
report['issues'] = [issues[key] for key in sorted(issues)]
def count_pass(filename):
    raw=(root/filename).read_bytes()
    text=raw.decode('utf-16' if raw.startswith((b'\xff\xfe',b'\xfe\xff')) else 'utf-8-sig')
    counts=re.findall(r'test result: ok\. (\d+) passed;',text)
    if not counts or 'test result: FAILED.' in text:
        raise ValueError('Expected actual successful Rust test footer in '+filename)
    return int(counts[-1])
report['tests'] = [
    {'command':'cargo test --locked --lib --features vnt,lab-testing vnt::rooms::tests -- --nocapture','exit_code':0,'pass_count':8,'skip_names':[],'log_path':str(root/'toolbox-prespawn-connection-close-test.log'),'scope':'Actual component fault injection after backend connection creation and before local session ownership; failed compensation retained for exact-scope retry.'},
    {'command':'cargo test --locked --lib --features vnt,lab-testing vnt::session::tests -- --nocapture','exit_code':0,'pass_count':9,'skip_names':[],'log_path':str(root/'toolbox-vnt-owned-cleanup-test.log'),'scope':'Actual Windows supervised child tests and exclusive file-handle failure/recovery; no three-player VNT gameplay.'},
    {'command': 'cargo test --locked --lib --features vnt,lab-testing --no-fail-fast', 'exit_code': 0, 'pass_count': count_pass('toolbox-final-all-features-test.log'), 'skip_names': [], 'log_path': str(root/'toolbox-final-all-features-test.log'), 'scope': 'final Rust component+actual local UDP/WS/DPAPI/Windows pipes; no native game'},
    {'command': 'cargo check --locked --all-targets --features vnt,lab-testing', 'exit_code': 0, 'pass_count': None, 'skip_names': [], 'log_path': str(root/'toolbox-all-targets-final.log')},
    {'command': 'cargo test --locked --manifest-path src-tauri/Cargo.toml', 'exit_code': 0, 'pass_count': 7, 'skip_names': [], 'log_path': str(root/'toolbox-tauri-tests-final.log'), 'scope': 'actual Tauri adapter/architecture tests; no interactive GUI/native game'},
    {'command': 'cargo test --locked --manifest-path src-tauri/Cargo.toml --lib', 'exit_code': 101, 'pass_count': 0, 'log_path': str(root/'toolbox-tauri-tests-no-library-invocation-failure.log'), 'scope': 'preserved command invocation failure: the Tauri package has no library target; corrected full-package invocation is recorded separately'},
    {'command': 'cargo test --locked --lib --features vnt,lab-testing core::runtime::tests -- --nocapture', 'exit_code': 0, 'pass_count': 11, 'skip_names': [], 'log_path': str(root/'toolbox-runtime-operation-final-test.log')},
    {'command': 'cargo test --doc --features vnt,lab-testing', 'exit_code': 0, 'pass_count': 2, 'skip_names': [], 'log_path': str(root/'toolbox-strong-id-compile-test.log')},
    {'command': 'python tests/auth_http_concurrency.py --driver target/debug/auth_http_concurrency.exe', 'exit_code': 0, 'pass_count': 4, 'skip_names': [], 'log_path': str(root/'toolbox-auth-http-concurrency-test.log'), 'scope': 'real client HTTP + DPAPI, synthetic loopback auth server/new-account commit'},
    {'command': 'TOOLBOX_RELAY_DRIVER=<actual Rust exe> go test ./internal/relayruntime -count=1 -v', 'exit_code': 0, 'pass_count': 23, 'skip_names': [], 'log_path': str(root/'go-rust-live-relay-test.log')},
    {'command': 'go test ./internal/update -count=1 -v', 'exit_code': 0, 'pass_count': 15, 'skip_names': [], 'log_path': str(root/'go-update-compatibility-test.log')},
    {'command': 'python Tools/Release/test_updater_transactions.py --toolbox-repo C:/wksp/ProjectReboundToolbox', 'exit_code': 0, 'pass_count': 2, 'skip_names': [], 'log_path': str(root/'updater-transaction-test.log'), 'scope': 'actual PowerShell atomic replacement/rollback using synthetic invalid-PE files; no installed program overwritten'},
    {'command': 'GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -buildvcs=true -trimpath -o <artifacts>/backend/ ./cmd/control-plane ./cmd/meta-server ./cmd/edge-relay', 'exit_code': 0, 'pass_count': None, 'skip_names': [], 'log_path': str(root/'backend-artifact-build.log'), 'scope': 'cross-compiled local artifacts; container/native deployment NOT_RUN'},
    {'command': 'python Tools/Release/test_provenance_cases.py', 'exit_code': 0, 'pass_count': 10, 'skip_names': [], 'log_path': str(root/'provenance-case-gate-test.log'), 'scope': 'synthetic acceptance-manifest, source/digest/evidence binding and complete five-artifact inventory gate tests only; no gameplay or release acceptance'},
    {'command': 'cargo check --locked --all-targets --features vnt,lab-testing', 'exit_code': 101, 'pass_count': 0, 'log_path': str(root/'toolbox-owned-startup-failure-check.log'), 'scope': 'preserved intermediate compile failure while the cleanup retry closure returned String instead of unit; not a successful final build'},
    {'command': 'cargo test --locked --lib --features vnt,lab-testing launching::launch::tests', 'exit_code': 101, 'pass_count': 0, 'log_path': str(root/'toolbox-owned-startup-failure-tests.log'), 'scope': 'preserved intermediate compile failure while Option pipe/PID call sites and SecretString conversion were being integrated; no tests passed in this invocation'},
    {'command': 'npm --prefix frontend run build', 'exit_code': 0, 'pass_count': None, 'log_path': str(root/'frontend-auth-cleanup-build.log'), 'scope': 'Vite build and Sites artifact preparation; no website publication'},
]
for group, count in [('bridge',12),('downloads',7),('ui',21),('status',11),('sites',4)]:
    report['tests'].append({'command':f'npm --prefix frontend run test:{group}', 'exit_code':0,
        'pass_count':count, 'skip_names':[], 'log_path':str(root/f'evidence/frontend/auth-cleanup-final-{group}.log'),
        'scope':'historical frontend test group after auth cleanup UI changes, before the later accept_new_lobbies change; no interactive game acceptance'})
report['tests'].append({'command':'npm --prefix frontend run build','exit_code':0,'pass_count':None,
    'log_path':str(root/'frontend-accept-new-build.log'),'scope':'final frontend build after separating creation pause from protocol support; no website publication'})
for group,count in [('bridge',13),('downloads',7),('ui',21),('status',11),('sites',4)]:
    report['tests'].append({'command':f'npm --prefix frontend run test:{group}','exit_code':0,
        'pass_count':count,'skip_names':[],'log_path':str(root/f'evidence/frontend/accept-new-final-{group}.log'),
        'scope':'actual final frontend group after accept_new_lobbies UI change; no interactive native game acceptance'})
permissions = json.loads((root/'meta-permissions-receipt.json').read_text(encoding='utf-8-sig'))
for execution in permissions['executions']:
    actual = next(item for item in execution['records'] if item['name']=='restricted-product-permissions-test')
    report['tests'].append({'command':actual['command'], 'exit_code':actual['exit_code'],
        'pass_count':1 if actual['exit_code']==0 else 0, 'skip_names':[],
        'log_path':actual['stable_log_path'],'source_input_commit':execution['source_commit'],
        'source_dirty':execution['source_dirty'],'source_file_hashes':execution['source_files'],
        'scope':'Real PostgreSQL/Redis restricted service permissions; the first failed assertion and corrected successful run are both retained. No native credential-purpose E2E acceptance.'})
retirement = json.loads((root/'meta-retirement-receipt.json').read_text(encoding='utf-8-sig'))
for test in retirement['tests']:
    report['tests'].append(dict(test, source_modification_commit=retirement['modification_commit']))
battlelog = json.loads((root/'battlelog-authority-receipt.json').read_text(encoding='utf-8-sig'))
report['tests'] += battlelog['tests']
report['tests'] += json.loads((root/'release-proof-gate-receipt.json').read_text(encoding='utf-8-sig'))['tests']
backend_race=json.loads((root/'backend-final-race-receipt.json').read_text(encoding='utf-8-sig'))
for execution in backend_race['executions']:
    report['tests'].append({'command':execution['command'],'exit_code':execution['exit_code'],
        'pass_count':execution['pass_count'],'subtest_pass_count':execution['subtest_pass_count'],
        'skip_names':execution['skip_names'],'fail_names':execution['fail_names'],
        'log_path':execution['stable_log_path'],'sha256':execution['sha256'],
        'source_input_commit':execution['source_commit'],'backend_tree':execution['backend_tree'],
        'scope':'Full actual Linux Backend race suite. Historical missing -p 1 invocation failure and corrected existing-CI invocation are retained; four external fixture SKIPs remain explicit.'})
for test in report['tests']:
    if Path(str(test.get('log_path',''))).name in ('toolbox-final-all-features-test.log','toolbox-all-targets-final.log','toolbox-tauri-tests-final.log'):
        test['source_input_commit'] = '22015fb41fa06fea0b8767049109291c89758fff'
        test['scope'] = 'Historical complete component/check run at 22015fb, before the updater cd09d7e change; logs retained without relabeling.'
toolbox_report=json.loads((root/'toolbox-report.json').read_text(encoding='utf-8-sig'))
for test in toolbox_report.get('tests',[]):
    if Path(str(test.get('log_path',''))).name in ('e2e20-final-cd09-full-lib.log','e2e20-final-cd09-all-targets.log','e2e20-final-cd09-tauri-tests.log'):
        report['tests'].append(dict(test, source_input_commit='cd09d7e7f769dc73e26ed7f38233b0a0a605e273'))
        for item in report['issues']:
            if 'toolbox-final-all-features-test.log' in item.get('evidence',[]):
                item['evidence'] = list(dict.fromkeys(item['evidence']+[test['log_path']]))
path.write_text(json.dumps(report, ensure_ascii=False, indent=2)+'\n', encoding='utf-8')
print('root-report updated from actual final component test logs; full native admission remains separately adjudicated')
