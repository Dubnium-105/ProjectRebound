"""Retain the exact reviewed local evidence/build drivers without private fixtures."""
import hashlib
import json
import shutil
from datetime import datetime, timezone
from pathlib import Path

source=Path(__file__).resolve().parent
destination_root=Path('C:/wksp/ProjectRebound/docs/implementation/strict-roster-20260907/evidence/root-final-pipeline')
names=(
    'build_final_backend.py','build_final_toolbox.py','write_artifact_inventory.py',
    'update_root_report.py','normalize_evidence.py','create_acceptance_ledger.py',
    'ledger_evidence_helpers.py','audit_final_ledger.py','scan_evidence_credentials.py',
    'write_release_readiness.py','snapshot_root_pipeline.py','run_native_root_validation.py',
    'run_root_native_dynamic.py','validate_native_backend_driver.py',
    'test_native_event_file_protocol.ps1','record_native_serializer_fix.py',
    'record_native_scope_validation.py','record_toolbox_scope_validation.py',
    'audit_recent_evidence_commits.py',
    'freeze_native_backend_source.py', 'record_native_identity_validation.py',
    'record_host_route_preservation.py',
    'validate_schema48_update.py', 'validate_cleanup_receipt_component.py', 'record_current_wire_producer.py', 'refresh_native_report.py', 'refresh_integration_reports.py', 'retain_final_supplements.py',
    'validate_final_gate.py', 'import_final_followup_tests.py', 'run_final_provenance.py', 'validate_current_auth_http.py', 'stage_final_evidence.py', 'verify_static_archive_binding.py',
    'fix_retired_api_docs.py', 'validate_retired_api_docs.py', 'import_backend_runtime_supplements.py',
    'validate_current_strong_ids.py', 'validate_migration_line_endings.py', 'import_latest_backend_audit.py', 'import_bp014_counters.py', 'finalize_local_lab.py', 'record_staging_byte_mismatch.py',
    'native-evidence/orchestrate-dedicated-one-real-steam-positive.ps1',
)
sha=lambda path:hashlib.sha256(path.read_bytes()).hexdigest()
fingerprint=hashlib.sha256('\n'.join(name+':'+sha(source/name) for name in names).encode()).hexdigest()
destination=destination_root/fingerprint
destination.mkdir(parents=True,exist_ok=True)
files=[]
for name in names:
    original=source/name
    retained=destination/name
    retained.parent.mkdir(parents=True,exist_ok=True)
    if retained.is_file() and sha(retained)!=sha(original):
        raise ValueError('Previously frozen driver differs: '+name)
    if not retained.is_file(): shutil.copyfile(original,retained)
    if sha(original)!=sha(retained): raise ValueError('Driver changed during snapshot: '+name)
    files.append({'original_path':str(original),'path':str(retained),'sha256':sha(retained),'bytes':retained.stat().st_size})
manifest={'generated_at':datetime.now(timezone.utc).isoformat(),'source_set_sha256':fingerprint,'files':files,
          'scope':'Exact source snapshot only. Each actual execution, exit code and log is recorded separately; retaining a driver does not mean it ran or passed.',
          'private_fixture_policy':'Explicit filename allowlist; no native identity, allocation, grant, ticket, database dump or runtime private driver is copied.'}
(destination/'source-manifest.json').write_text(json.dumps(manifest,indent=2)+'\n',encoding='utf-8')
print(json.dumps({'retained_root_pipeline_sources':len(files),'execution_claim':'NONE'}))
