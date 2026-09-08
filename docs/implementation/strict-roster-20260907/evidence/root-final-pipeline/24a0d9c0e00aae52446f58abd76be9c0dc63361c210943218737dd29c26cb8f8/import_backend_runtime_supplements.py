#!/usr/bin/env python3
"""Import retained Backend and Toolbox runtime supplements without upgrading E2E status.

The script is deliberately fail-closed.  It reads the two immutable supplement
receipts, verifies every referenced execution log against its recorded SHA and
against final-supplements/retained-files.json, and only then can ``--apply``
write the five dedicated E2E result files and append report test records.
Execution source observations remain single-repository facts; a reviewed
two-repository pair is never substituted for an execution pair.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import sys
from copy import deepcopy
from datetime import datetime, timezone
from pathlib import Path
from typing import Any


REPO = Path("C:/wksp/ProjectRebound")
TMP = REPO / ".tmp/strict-roster-20260907"
DOCS = REPO / "docs/implementation/strict-roster-20260907"
EVIDENCE = DOCS / "evidence/final-supplements"
MANIFEST = EVIDENCE / "retained-files.json"
BACKEND_DIR = TMP / "e2e131415"
BACKEND_RECEIPT = BACKEND_DIR / "e2e-13-15-backend-result-20260908.json"
RUNTIME_DIR = TMP / "e2e-06-17-runtime-current-20260908"
RUNTIME_RECEIPT = RUNTIME_DIR / "receipt.json"
BACKEND_AUDIT_JSON = TMP / "backend-source-audit-20260908.json"
BACKEND_AUDIT_MD = TMP / "backend-source-audit-20260908.md"
OUTPUTS = {
    "E2E-13": DOCS / "e2e-13-result.json",
    "E2E-14": DOCS / "e2e-14-result.json",
    "E2E-15": DOCS / "e2e-15-result.json",
    "E2E-06": DOCS / "e2e-06-result.json",
    "E2E-17": DOCS / "e2e-17-result.json",
}
REPORTS = {
    "root": DOCS / "root-report.json",
    "backend": DOCS / "backend-report.json",
    "toolbox": DOCS / "toolbox-report.json",
}


class ImportState:
    def __init__(self) -> None:
        self.problems: list[str] = []
        self.retained: dict[str, dict[str, Any]] = {}
        self.evidence: dict[str, dict[str, Any]] = {}

    def problem(self, message: str) -> None:
        self.problems.append(message)


def canonical(path: Path | str) -> str:
    return os.path.normcase(os.path.normpath(os.path.abspath(str(path))))


def digest(path: Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            h.update(chunk)
    return h.hexdigest().upper()


def read_json(path: Path, state: ImportState) -> dict[str, Any] | None:
    if not path.is_file():
        state.problem(f"missing JSON input: {path}")
        return None
    try:
        value = json.loads(path.read_text(encoding="utf-8-sig"))
    except Exception as exc:  # pragma: no cover - diagnostic path
        state.problem(f"invalid JSON input {path}: {exc}")
        return None
    if not isinstance(value, dict):
        state.problem(f"JSON input is not an object: {path}")
        return None
    return value


def load_retained_manifest(state: ImportState) -> None:
    manifest = read_json(MANIFEST, state)
    if manifest is None:
        return
    files = manifest.get("files")
    if not isinstance(files, list):
        state.problem(f"retention manifest has no files list: {MANIFEST}")
        return
    for item in files:
        if not isinstance(item, dict):
            state.problem(f"invalid retention manifest entry: {item!r}")
            continue
        original = item.get("original_path")
        retained = item.get("path")
        expected = str(item.get("sha256", "")).upper()
        if not original or not retained or len(expected) != 64:
            state.problem(f"incomplete retention manifest entry: {item!r}")
            continue
        retained_path = Path(retained)
        if not retained_path.is_file():
            state.problem(f"retained evidence is missing: {retained_path}")
            continue
        actual = digest(retained_path)
        if actual != expected:
            state.problem(f"retained evidence SHA changed: {retained_path}")
            continue
        state.retained[canonical(original)] = {
            "original_path": str(original),
            "path": str(retained_path),
            "sha256": expected,
        }


def retain_reference(path: Path, expected_sha: str | None, state: ImportState) -> dict[str, Any] | None:
    if not path.is_file():
        state.problem(f"missing evidence input: {path}")
        return None
    actual = digest(path)
    expected = str(expected_sha or actual).upper()
    if actual != expected:
        state.problem(f"execution evidence SHA mismatch: {path} expected {expected} got {actual}")
    record = state.retained.get(canonical(path))
    if record is None:
        state.problem(
            "execution evidence is not in retained-files.json; run the explicit allowlist retention step first: "
            + str(path)
        )
        return {
            "original_path": str(path),
            "retained_path": None,
            "sha256": actual,
            "retained": False,
        }
    if record["sha256"] != actual:
        state.problem(f"retained manifest SHA does not match current evidence: {path}")
    result = {
        "original_path": str(path),
        "retained_path": record["path"],
        "sha256": actual,
        "retained": True,
    }
    state.evidence[canonical(path)] = result
    return result


def execution_source_observation(repository: str, commit: str, dirty: bool | None) -> dict[str, Any]:
    return {
        "execution_source_pair": {repository: commit},
        "execution_pair_complete": False,
        "source_dirty_by_repository": {repository: dirty},
        "source_observation": (
            "single-repository execution fact; no reviewed two-repository pair was substituted"
        ),
    }


def validate_backend_runs(receipt: dict[str, Any], state: ImportState) -> tuple[list[dict[str, Any]], list[dict[str, Any]]]:
    runs: list[dict[str, Any]] = []
    component_runs: list[dict[str, Any]] = []
    for field, destination in (("test_runs", runs), ("component_only_runs", component_runs)):
        values = receipt.get(field, [])
        if not isinstance(values, list):
            state.problem(f"Backend receipt field {field} is not a list")
            continue
        for index, item in enumerate(values):
            if not isinstance(item, dict):
                state.problem(f"Backend receipt {field}[{index}] is not an object")
                continue
            exit_code = item.get("exit_code")
            if exit_code is not None and not isinstance(exit_code, int):
                state.problem(f"Backend receipt {field}[{index}] has no integer exit_code")
            log_path = Path(item.get("log_path", ""))
            log_ref = retain_reference(log_path, item.get("log_sha256") or item.get("sha256"), state)
            if log_ref is None:
                continue
            enriched = deepcopy(item)
            enriched["log_evidence"] = log_ref
            destination.append(enriched)
    return runs, component_runs


def validate_runtime(receipt: dict[str, Any], state: ImportState) -> dict[str, Any]:
    log_path = Path(receipt.get("log_path", ""))
    log_ref = retain_reference(log_path, receipt.get("log_sha256"), state)
    result = deepcopy(receipt)
    result["log_evidence"] = log_ref
    result["receipt_evidence"] = retain_reference(RUNTIME_RECEIPT, None, state)
    return result


def stable_path(ref: dict[str, Any] | None) -> str | None:
    if ref and ref.get("retained_path"):
        return str(ref["retained_path"])
    return None


def backend_test_record(item: dict[str, Any], source: dict[str, Any], import_id: str) -> dict[str, Any]:
    log_ref = item["log_evidence"]
    code = item.get("exit_code")
    return {
        "import_id": import_id,
        "command": item.get("command"),
        "exit_code": code,
        "pass_count": item.get("top_level_pass", item.get("subtests_pass")),
        "skip_names": item.get("skip_names", []),
        "log_path": stable_path(log_ref),
        "log_sha256": log_ref["sha256"],
        "status": "PASS" if code == 0 else ("FAILED" if isinstance(code, int) else "UNKNOWN"),
        "scope": "Backend component supplement; the corresponding full E2E remains PARTIAL.",
        **source,
    }


def runtime_test_record(receipt: dict[str, Any], source: dict[str, Any]) -> dict[str, Any]:
    log_ref = receipt["log_evidence"]
    return {
        "import_id": "e2e06-17::toolbox-runtime-component",
        "command": receipt.get("command"),
        "exit_code": receipt.get("exit_code"),
        "pass_count": receipt.get("passed"),
        "filtered_count": receipt.get("filtered"),
        "skip_names": [],
        "log_path": stable_path(log_ref),
        "log_sha256": log_ref["sha256"] if log_ref else None,
        "status": receipt.get("status", "PASS_COMPONENT_SCOPE_ONLY"),
        "scope": receipt.get("scope"),
        **source,
    }


def build_backend_result(
    case: dict[str, Any],
    receipt: dict[str, Any],
    source: dict[str, Any],
    test_records: list[dict[str, Any]],
    component_records: list[dict[str, Any]],
    receipt_ref: dict[str, Any] | None,
    audit_refs: list[dict[str, Any] | None],
) -> dict[str, Any]:
    case_id = str(case["id"])
    evidence_paths = [r["log_path"] for r in test_records if r.get("log_path")]
    return {
        "id": case_id,
        "title": f"{case_id} Backend supplement",
        "status": "PARTIAL",
        "source_commit": receipt.get("source_commit"),
        **source,
        "source_scope": receipt.get("scope"),
        "steps": deepcopy(case.get("steps", [])),
        "backend_execution": test_records,
        "component_only_execution": component_records,
        "result": {
            "environment": receipt.get("environment"),
            "command_or_harness": [item.get("command") for item in receipt.get("test_runs", [])],
            "exit_code": None,
            "exit_code_note": "No single full E2E process was run; each Backend subrun exit_code is preserved in backend_execution.",
            "log_paths": evidence_paths,
            "observed_result": case.get("expected_assessment"),
        },
        "input_receipt": receipt_ref,
        "source_audit": audit_refs,
        "release_ready": False,
        "limitations": [
            "No native game, Toolbox IPC, process-kill/reconciler, or cross-process old-event ordering was executed by this supplement.",
            "Component-only PASS records are retained as component evidence and cannot upgrade the full E2E case.",
        ],
    }


def build_runtime_result(
    e2e_id: str,
    receipt: dict[str, Any],
    source: dict[str, Any],
    receipt_ref: dict[str, Any] | None,
) -> dict[str, Any]:
    return {
        "id": e2e_id,
        "title": f"{e2e_id} runtime supplement",
        "status": "NOT_RUN",
        "source_commit": receipt.get("source_snapshot", {}).get("head"),
        **source,
        "source_scope": receipt.get("scope"),
        "steps": [
            {
                "id": "full_e2e_scenario",
                "status": "NOT_RUN",
                "command": None,
                "exit_code": None,
                "observed_result": "The full UI/native/process-boundary E2E case was not executed.",
            }
        ],
        "result": {
            "environment": "No full E2E environment was exercised.",
            "command_or_harness": None,
            "exit_code": None,
            "log_paths": [],
            "observed_result": "NOT_RUN; the component supplement below is retained without upgrading the E2E case.",
        },
        "component_supplement": {
            "status": receipt.get("status", "PASS_COMPONENT_SCOPE_ONLY"),
            "command": receipt.get("command"),
            "exit_code": receipt.get("exit_code"),
            "passed": receipt.get("passed"),
            "failed": receipt.get("failed"),
            "ignored": receipt.get("ignored"),
            "filtered": receipt.get("filtered"),
            "log": receipt.get("log_evidence"),
            "coverage": receipt.get("e2e_06_coverage", []) if e2e_id == "E2E-06" else receipt.get("e2e_17_coverage", []),
            "limitations": receipt.get("limitations", []),
        },
        "input_receipt": receipt_ref,
        "release_ready": False,
    }


def append_once(report: dict[str, Any], records: list[dict[str, Any]]) -> dict[str, Any]:
    result = deepcopy(report)
    tests = result.setdefault("tests", [])
    known = {item.get("import_id") for item in tests if isinstance(item, dict)}
    for record in records:
        if record.get("import_id") not in known:
            tests.append(record)
            known.add(record.get("import_id"))
    result["release_ready"] = False
    result["supplement_import"] = {
        "updated_at": datetime.now(timezone.utc).isoformat(),
        "script": str(Path(__file__)),
        "records": [record.get("import_id") for record in records],
        "source_pair_complete": False,
        "note": "Single-repository execution facts retained; no reviewed pair substituted.",
    }
    return result


def write_atomic(path: Path, value: dict[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temp = path.with_name(path.name + ".next")
    temp.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    temp.replace(path)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--apply", action="store_true", help="write result files and append report tests")
    args = parser.parse_args()

    state = ImportState()
    load_retained_manifest(state)
    backend = read_json(BACKEND_RECEIPT, state)
    runtime = read_json(RUNTIME_RECEIPT, state)
    audit_json = retain_reference(BACKEND_AUDIT_JSON, None, state)
    audit_md = retain_reference(BACKEND_AUDIT_MD, None, state)
    audit_refs = [audit_json, audit_md]
    for report_path in REPORTS.values():
        if not report_path.is_file():
            state.problem(f"missing report input: {report_path}")
    report_values: dict[str, dict[str, Any]] = {}
    for owner, report_path in REPORTS.items():
        report = read_json(report_path, state)
        if report is not None:
            report_values[owner] = report

    if backend is None or runtime is None:
        print(json.dumps({"status": "BLOCKED", "problems": state.problems}, ensure_ascii=False, indent=2))
        return 2

    backend_commit = str(backend.get("source_commit", ""))
    if len(backend_commit) != 40:
        state.problem(f"Backend execution source_commit is not a complete 40-hex commit: {backend_commit!r}")
    runtime_snapshot = runtime.get("source_snapshot", {})
    toolbox_commit = str(runtime_snapshot.get("head", ""))
    if len(toolbox_commit) != 40:
        state.problem(f"Toolbox execution source head is not a complete 40-hex commit: {toolbox_commit!r}")

    backend_source = execution_source_observation("ProjectRebound", backend_commit, None)
    runtime_source = execution_source_observation("Toolbox", toolbox_commit, runtime_snapshot.get("dirty"))
    backend_runs, backend_components = validate_backend_runs(backend, state)
    runtime_checked = validate_runtime(runtime, state)
    backend_receipt_ref = retain_reference(BACKEND_RECEIPT, None, state)

    if state.problems:
        summary = {
            "status": "BLOCKED",
            "apply_requested": args.apply,
            "problems": state.problems,
            "required_retention_manifest": str(MANIFEST),
            "inputs": [str(BACKEND_RECEIPT), str(RUNTIME_RECEIPT), str(BACKEND_AUDIT_JSON), str(BACKEND_AUDIT_MD)],
        }
        print(json.dumps(summary, ensure_ascii=False, indent=2))
        return 2

    backend_test_records = [
        backend_test_record(item, backend_source, f"e2e13-15::backend::{index}")
        for index, item in enumerate(backend_runs)
    ]
    backend_component_records = [
        backend_test_record(item, backend_source, f"e2e13-15::component::{index}")
        for index, item in enumerate(backend_components)
    ]
    runtime_record = runtime_test_record(runtime_checked, runtime_source)
    cases = {str(item.get("id")): item for item in backend.get("e2e_cases", []) if isinstance(item, dict)}
    backend_outputs = {
        case_id: build_backend_result(
            cases[case_id],
            backend,
            backend_source,
            backend_test_records,
            backend_component_records,
            backend_receipt_ref,
            audit_refs,
        )
        for case_id in ("E2E-13", "E2E-14", "E2E-15")
        if case_id in cases
    }
    runtime_receipt_ref = runtime_checked.get("receipt_evidence")
    runtime_outputs = {
        case_id: build_runtime_result(case_id, runtime_checked, runtime_source, runtime_receipt_ref)
        for case_id in ("E2E-06", "E2E-17")
    }
    outputs = {**backend_outputs, **runtime_outputs}
    report_records_root = backend_test_records + backend_component_records + [runtime_record]
    report_records_backend = backend_test_records + backend_component_records
    report_records_toolbox = [runtime_record]

    if not args.apply:
        print(
            json.dumps(
                {
                    "status": "READY",
                    "apply_required": True,
                    "outputs": {key: str(path) for key, path in OUTPUTS.items()},
                    "backend_records": len(report_records_backend),
                    "toolbox_records": len(report_records_toolbox),
                    "source_pair_complete": False,
                    "component_passes_do_not_upgrade_e2e": True,
                },
                ensure_ascii=False,
                indent=2,
            )
        )
        return 0

    for case_id, value in outputs.items():
        write_atomic(OUTPUTS[case_id], value)

    report_inputs = {
        "root": (report_records_root, REPORTS["root"], report_values["root"]),
        "backend": (report_records_backend, REPORTS["backend"], report_values["backend"]),
        "toolbox": (report_records_toolbox, REPORTS["toolbox"], report_values["toolbox"]),
    }
    for owner, (records, path, report) in report_inputs.items():
        write_atomic(path, append_once(report, records))
    if state.problems:
        print(json.dumps({"status": "BLOCKED", "problems": state.problems}, ensure_ascii=False, indent=2))
        return 2
    print(
        json.dumps(
            {
                "status": "APPLIED",
                "outputs": {key: str(path) for key, path in OUTPUTS.items()},
                "reports": {owner: str(path) for owner, path in REPORTS.items()},
                "source_pair_complete": False,
                "component_passes_do_not_upgrade_e2e": True,
            },
            ensure_ascii=False,
            indent=2,
        )
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
