"""Retain exact allowlisted follow-up receipts without importing private state."""
import hashlib
import json
import re
import shutil
from pathlib import Path

repo = Path('C:/wksp/ProjectRebound')
source = repo/'.tmp/strict-roster-20260907'
target = repo/'docs/implementation/strict-roster-20260907/evidence/final-supplements'
names = [
    'backend-ac034-ac045-mapping-20260908.json',
    'backend-ac034-ac045-mapping-20260908-v2.json',
    'ac045-dedicated-selection-20260908T003910Z.log',
    'ac045-dedicated-selection-20260908T004021Z.log',
    'ac045-dedicated-selection-20260908T004208Z.log',
    'ac045-dedicated-selection-20260908T004322Z.log',
    'ac034-close-freeze-battlelog-20260908T004350Z.log',
    'ac034-close-freeze-battlelog-20260908T004550Z.log',
    'ac045-create-rebound_ac045_component_20260908003910.log',
    'ac045-create-rebound_ac045_component_20260908004322.log',
    'ac034-create-rebound_ac034_component_20260908004350.log',
    'backend-audit-001-003-supplemental-20260908t002630z.json',
    'backend-final-race-go1266-schema48-20260908t001815z.log',
    'backend-final-migration-normalization-20260908t001657z.log',
    'backend-final-race-db-create-20260908t001637z.log',
    'run-backend-final-race-go1266-schema48-20260908t001815z.sh',
    'run-final-migration-normalization-20260908t001657z.sh',
    'run-final-race-db-create-20260908t001637z.sh',
    'backend-audit-001-003-safe-allowlist-20260908T001448Z.json',
    'e2e131415/backend-e2e13-15-focused-20260908T230116Z.log',
    'relay-windows-crosslang-20260908/go-test-rust-driver.log',
    'relay-windows-crosslang-20260908/relay-windows-crosslang-receipt.json',
    'provenance-gate-final-receipt-20260908.json',
    'provenance-gate-test-20260908-final.log',
    'toolbox-full-lib-70fcd-20260908.json',
    'toolbox-local-ac-mapping.json',
    'e2e131415/e2e-13-15-backend-result-20260908.json',
    'e2e131415/backend-e2e13-15-matchlobby-clean-20260908T230614Z.log',
    'e2e131415/backend-e2e13-15-database-clean-20260908T230743Z.log',
    'e2e131415/backend-e2e13-15-meta-clean-20260908T230859Z.log',
    'e2e131415/backend-e2e14-dedicated-alone-20260908T230445Z.log',
    'e2e131415/backend-battlelog-component-20260908T231649Z.log',
    'e2e-06-17-runtime-current-20260908/receipt.json',
    'e2e-06-17-runtime-current-20260908/runtime-tests.log',
    'backend-source-audit-20260908.json',
    'backend-source-audit-20260908.md',
]
counter_dir = 'bp014-counter-20260908T083620Z/'
names += [counter_dir+name for name in (
    'bp014_counter_supplement_test.go','bp014_toolbox_counter_supplement_test.go',
    'bp014_toolbox_legacy_route_loop_test.rs','bp014-counter-receipt.json',
    'bp014-toolbox-route-loop-receipt.json','cargo-test-toolbox-route-loop-rerun.log',
    'cargo-test-toolbox-route-loop.log','controller.rs.before-toolbox-test',
    'controller.rs.with-bp014-route-loop-test','driver-build-receipt.json','driver-build.log',
    'go-test-product-counters-current-driver.log','go-test-product-counters-final-with-driver.log',
    'go-test-product-counters-final.log','go-test-product-counters-rerun.log',
    'go-test-product-counters-rerun2.log','go-test-product-counters.log',
    'go-test-toolbox-private-counters-final.log','go-test-toolbox-private-counters-rerun.log',
    'go-test-toolbox-private-counters.log')]
aliases = {
    counter_dir+'controller.rs.before-toolbox-test':counter_dir+'controller-baseline.rs',
    counter_dir+'controller.rs.with-bp014-route-loop-test':counter_dir+'controller-with-route-loop-test.rs',
}
audit_allowlist = json.loads((source/'backend-audit-001-003-safe-allowlist-20260908T001448Z.json').read_text(encoding='utf-8-sig'))
for entry in audit_allowlist['evidence_entries']:
    if entry['category'] == 'source_hash_only':
        continue
    original = (repo/entry['path']).resolve()
    if not original.is_relative_to(source.resolve()) or original.suffix.lower() not in ('.log', '.json', '.sql', '.sh'):
        raise ValueError('Unexpected Backend audit evidence path')
    if hashlib.sha256(original.read_bytes()).hexdigest().lower() != entry['sha256'].lower():
        raise ValueError('Backend audit allowlisted bytes changed: '+entry['path'])
    names.append(original.relative_to(source).as_posix())
names += [str(p.relative_to(source)).replace('\\', '/') for p in sorted(
    (source/'native-evidence/live-run-results').glob('bp038-sidecar-*/bp038-sidecar-result.json'))]
audit = source/'native-evidence/native-serializer-retain-audit-20260908.json'
if audit.is_file():
    names.append(str(audit.relative_to(source)).replace('\\', '/'))
patterns = [re.compile(r'\beyJ[A-Za-z0-9_-]{12,}\.[A-Za-z0-9_-]{12,}\.[A-Za-z0-9_-]{16,}\b'),
            re.compile(r'(?i)Bearer\s+[A-Za-z0-9_.~-]{28,}'),
            re.compile(r'-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----')]
for private in ('steam-platform-id-private.txt', 'native-player-id-private.txt'):
    path = source/'native-evidence'/private
    if path.is_file():
        value = path.read_text(encoding='utf-8-sig').strip()
        if value:
            patterns.append(re.compile(re.escape(value)))
manifest_path = target/'retained-files.json'
records = json.loads(manifest_path.read_text(encoding='utf-8'))['files'] if manifest_path.is_file() else []
for retained_record in records:
    if hashlib.sha256(Path(retained_record['path']).read_bytes()).hexdigest() != retained_record['sha256']:
        raise ValueError('Previously retained evidence changed')
for name in names:
    original = source/name
    raw = original.read_bytes()
    content = raw.decode('utf-16' if raw.startswith((b'\xff\xfe', b'\xfe\xff')) else 'utf-8-sig')
    if any(pattern.search(content) for pattern in patterns):
        raise ValueError('Private content detected in ' + name)
    retained = target/aliases.get(name,name)
    retained.parent.mkdir(parents=True, exist_ok=True)
    digest = hashlib.sha256(raw).hexdigest()
    if retained.exists() and retained.read_bytes() != raw:
        retained = retained.with_name(digest+'-'+retained.name)
        if retained.exists() and retained.read_bytes() != raw:
            raise ValueError('Content-addressed retained evidence differs: ' + name)
    if not retained.exists():
        shutil.copyfile(original, retained)
    records.append({'original_path': str(original), 'path': str(retained), 'sha256': digest})
records = list({(r['original_path'], r['sha256']): r for r in records}.values())
target.mkdir(parents=True, exist_ok=True)
manifest_path.write_text(json.dumps({'files': records,
    'scope': 'Immutable actual execution receipts/logs; source snapshots and failures remain original.'}, indent=2)+'\n', encoding='utf-8')
print(json.dumps({'retained': len(records)}))
