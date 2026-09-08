"""Append current implementation review and actual receipts, preserving history."""
import hashlib
import json
import subprocess
from datetime import datetime, timezone
from pathlib import Path

repo = Path('C:/wksp/ProjectRebound')
toolbox = Path('C:/wksp/ProjectReboundToolbox')
root = repo/'docs/implementation/strict-roster-20260907'
read = lambda p: json.loads(p.read_text(encoding='utf-8-sig'))
sha = lambda p: hashlib.sha256(p.read_bytes()).hexdigest()
head = lambda p: subprocess.check_output(['git','rev-parse','HEAD'],cwd=p,text=True).strip()
pair = {'ProjectRebound': head(repo), 'Toolbox': head(toolbox)}
notes = {
 'BP-032': 'Standalone online P2P room and match routes fail explicitly at runtime; OpenAPI and public reference docs now advertise only their 410 retirement responses. The actual four OpenAPI tests and all 17 registered retired operations were verified. Managed MatchLobby/Attempt is the sole online path; transport primitives are not admission fallbacks.',
 'BP-001': 'Current strict candidate pins the fixed game SHA and the actual frozen Payload SHA. The built Tauri manifest requires schema 48 and records an unmodified Toolbox commit. Native/full compatibility and operational signing are independent gates.',
 'BP-022': 'The actual Windows C++ CommandFramework producer emits preserved HOST full scope/nonce/operation; Rust consumes the exact fixture bytes and rejects missing/tampered fields. This closes wire-fixture consumption only, not real game transport E2E.',
 'BP-039': 'Actual fixed-game one-client Steam/PreLogin, native admission/readback, Backend Reserve/Confirm, CONNECTED and DISCONNECTED/release are observed. Online MinPlayersToStart=2 prevents Pawn/Playable with one real player. No fake seat was connected and no native minimum/PvE/Token bypass was used.',
 'BP-041': 'Live, reserved and last-disconnected generation/route/JTI/nonce remain separate. A delayed old live disconnect does not clear the newer reservation Backend/native proof. Component regressions and actual one-client chain are retained; multi-client reconnect acceptance remains unrun.',
 'BP-043': 'Same-world route recovery preserves the exact still-live HOST scope/nonce/operation/cursor across route 1→2→3 while updating authority readiness and MEMBER authorization separately. A later real HOST disconnect advances its authorization generation, invalidates the Payload installation gate and requires the refreshed allocation. Toolbox admits this exact Backend-confirmed old HOST proof into the same-route fresh-HOST branch, which requires a new native nonce and CONNECTED proof. Backend, Payload and Toolbox source are aligned; real multi-client HOST recovery remains unrun.',
 'BP-050': 'Updater replacement/recovery ownership and signed strict metadata validation remain implemented. Backend and Toolbox now both reject schema 47 and require schema 48. Actual Go public-test-key signature bytes are consumed by Rust; operational signing and full candidate compatibility are not passed.',
 'BP-049': 'Append-only migrations 44–48 preserve existing service data. Migration 48 separates live and authorized generation/route; only nonce-bound live HOST rows retain a preservation acknowledgement. Missing-nonce historical HOST/MEMBER rows cannot satisfy live quorum. Startup verifies migration names/content checksums and rejects future versions. Embedded SQL execution/checksums now canonicalize CRLF to LF, while verification accepts only the exact known historical CRLF representation; SQL content drift remains rejected. The actual pre-fix line-ending regression failed, and both post-fix unit tests passed. Historical schema48-as-future rejection was executed while the embedded maximum was47 and is not a current-schema claim. Actual database restore and role/ACL executions are retained separately; trust-only local DB authentication does not prove password rejection.',
 'BP-051': 'The canonical 52-BP/52-AC/22-E2E ledger links source changes and exact retained execution evidence. Historical source/dirty state, failures, skips and missing coverage stay explicit. The final five-artifact source binding is verified separately from release acceptance; the supported gameplay matrix is incomplete and release remains blocked.',
 'BP-052': 'Current local candidate builds bind the actual Payload and Tauri executable plus three source-stamped Go artifacts when present in artifact-inventory.json. Historical matchserver remains excluded. Five build outputs alone do not establish signing, native compatibility or release readiness.'
}
source_refs = {
 'BP-032': [repo/'Backend/api/openapi/openapi.yaml', repo/'docs/api/external.md',
            repo/'Backend/internal/controlplane/server.go'],
 'BP-001': [repo/'Tools/Release/strict_roster_provenance.py', toolbox/'src/security/strict_build.rs'],
 'BP-022': [repo/'Payload/Tests/CommandProtocolTests.cpp', toolbox/'src/server/pipe_cpp_fixture_tests.rs'],
 'BP-043': [repo/'Backend/migrations/000048_strict_roster_live_generation.sql',
            repo/'Backend/internal/matchlobby/service.go', repo/'Backend/internal/matchlobby/sweeper.go',
            toolbox/'src/core/runtime.rs'],
 'BP-047': [repo/'Payload/Admission/StrictRosterCleanupReceipt.h', repo/'Payload/Tests/StrictRosterCleanupReceiptTests.cpp',
            repo/'Payload/dllmain.cpp'],
 'BP-050': [repo/'Backend/internal/update/service.go', toolbox/'src/api/distribution/updates.rs'],
 'BP-049': [repo/'Backend/migrations/000048_strict_roster_live_generation.sql',
            repo/'Backend/internal/database/migrator.go', repo/'Backend/internal/database/migrator_test.go'],
 'BP-051': [repo/'Tools/Release/strict_roster_provenance.py', repo/'Tools/Release/test_provenance_cases.py'],
 'BP-052': [repo/'Tools/Release/strict_roster_provenance.py', repo/'Tools/Release/test_provenance_cases.py'],
}
for owner in ('root','toolbox','backend'):
    path = root/(owner+'-report.json')
    report = read(path)
    if owner == 'root':
        report.setdefault('baseline_source_pair', report.get('source_pair'))
        report['source_pair_scope'] = 'Historical report anchor, preserved unchanged; use reviewed_source_pair for the current implementation review. Per-run execution records remain authoritative.'
    report['reviewed_source_pair'] = pair
    report['report_updated_at'] = datetime.now(timezone.utc).isoformat()
    report['release_ready'] = False
    if owner in ('root', 'toolbox'):
        for receipt_path in sorted((root/'evidence').glob('strong-id-current-*/receipt.json')):
            receipt = read(receipt_path)
            log = Path(receipt['log_path'])
            if sha(log).lower() != receipt['log_sha256'].lower():
                raise ValueError('Strong-ID compile-fail validation log changed')
            item = {**receipt, 'id':receipt_path.parent.name, 'skip_names':[],
                    'receipt_path':str(receipt_path), 'receipt_sha256':sha(receipt_path),
                    'status':'PASS' if receipt['exit_code'] == 0 else 'EXECUTED_FAIL'}
            tests = report.setdefault('tests', [])
            tests[:] = [existing for existing in tests if existing.get('id') != item['id']] + [item]
            for issue in report.get('issues', []):
                if issue['id'] == 'BP-031':
                    issue['evidence'] = list(dict.fromkeys(issue.get('evidence', []) + [str(receipt_path), str(log)]))
    if owner in ('root', 'backend'):
        for receipt_path in sorted((root/'evidence').glob('migration-line-endings-*/receipt.json')):
            receipt = read(receipt_path)
            log = Path(receipt['log_path'])
            if sha(log).lower() != receipt['log_sha256'].lower():
                raise ValueError('Migration line-ending validation log changed')
            item = {**receipt, 'id':receipt_path.parent.name, 'skip_names':[],
                    'receipt_path':str(receipt_path), 'receipt_sha256':sha(receipt_path),
                    'execution_source_pair':{'ProjectRebound':receipt['source_input_commit']},
                    'source_dirty':{'ProjectRebound':receipt['source_dirty']},
                    'status':'PASS' if receipt['exit_code'] == 0 else 'EXECUTED_FAIL'}
            tests = report.setdefault('tests', [])
            tests[:] = [existing for existing in tests if existing.get('id') != item['id']] + [item]
            for issue in report.get('issues', []):
                if issue['id'] == 'BP-049':
                    issue['evidence'] = list(dict.fromkeys(issue.get('evidence', []) + [str(receipt_path), str(log)]))
        for receipt_path in sorted((root/'evidence').glob('retired-api-contract-*/receipt.json')):
            receipt = read(receipt_path)
            log = Path(receipt['log_path'])
            if sha(log).lower() != receipt['log_sha256'].lower():
                raise ValueError('Retired API validation log changed')
            item = {'id': 'retired-api-contract-'+receipt_path.parent.name,
                    'command': receipt['command'], 'exit_code': receipt['exit_code'],
                    'log_path': str(log), 'log_sha256': sha(log), 'receipt_path': str(receipt_path),
                    'receipt_sha256': sha(receipt_path), 'skip_names': [],
                    'status': 'PASS' if receipt['exit_code'] == 0 else 'EXECUTED_FAIL',
                    'execution_source_pair': {'ProjectRebound': receipt['source_input_commit']},
                    'source_dirty': {'ProjectRebound': receipt['source_dirty']},
                    'source_files': receipt['source_files'], 'scope': receipt['scope']}
            tests = report.setdefault('tests', [])
            tests[:] = [existing for existing in tests if existing.get('id') != item['id']] + [item]
            for issue in report.get('issues', []):
                if issue['id'] == 'BP-032':
                    issue['evidence'] = list(dict.fromkeys(issue.get('evidence', []) + [str(receipt_path), str(log)]))
    for issue in report.get('issues', []):
        if issue['id'] == 'BP-051':
            schema_refs = {str(p).replace('\\','/') for p in source_refs['BP-049']}
            issue['evidence'] = [p for p in issue.get('evidence', []) if p.replace('\\','/') not in schema_refs]
        if issue['id'] in source_refs:
            for source in source_refs[issue['id']]:
                if not source.is_file():
                    raise ValueError('Reviewed source reference is missing: '+str(source))
            issue['evidence'] = list(dict.fromkeys(issue.get('evidence', []) + [str(p) for p in source_refs[issue['id']]]))
        if issue['id'] in notes:
            issue['note'] = notes[issue['id']]
            paths = [root/'evidence/native-scope-runtime-20260908/receipt.json',
                     root/'evidence/host-route-preservation-20260908/receipt.json',
                     root/'evidence/schema48-toolbox-update-20260908/schema48-toolbox-update-receipt-20260908.json']
            issue['evidence'] = list(dict.fromkeys(issue.get('evidence', [])+[str(p) for p in paths if p.is_file()]))
            if issue['id'] == 'BP-039':
                issue.update(implementation='implemented', acceptance='BLOCKED')
    retained = []
    for pattern in ('schema48-update-*/receipt.json', 'schema48-toolbox-update-*/schema48-toolbox-update-receipt-*.json',
                    'host-route-preservation-20260908/receipt.json', 'native-scope-runtime-20260908/receipt.json',
                    'backend-a-final-*-audit-20260908.json', 'backend-host-scope-*.json',
                    'final-supplements/relay-windows-crosslang-20260908/relay-windows-crosslang-receipt.json',
                    'toolbox-final-followups-20260908/*manifest*.json',
                    'cleanup-receipt-component-*/receipt.json'):
        for p in sorted((root/'evidence').glob(pattern)):
            retained.append({'path': str(p), 'sha256': sha(p),
                             'scope': 'Original execution source/dirty state and outcomes inside this receipt remain authoritative.'})
    report['current_followup_execution_receipts'] = retained
    report['commit_status'] = 'Implementation committed locally where identified in the final ledger; actual Native/full E2E and operational release gates remain separate. No push or deployment.'
    path.write_text(json.dumps(report,ensure_ascii=False,indent=2)+'\n',encoding='utf-8')
print(json.dumps({'reviewed_source_pair':pair,'execution_inputs_relabelled':False}))
