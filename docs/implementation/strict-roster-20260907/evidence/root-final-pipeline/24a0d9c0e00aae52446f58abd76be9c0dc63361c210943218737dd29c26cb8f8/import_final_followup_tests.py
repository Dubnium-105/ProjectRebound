"""Build an idempotent final-follow-up test import plan.

The default mode is deliberately read-only.  It reads the current owner
reports and retained receipts, resolves each log to the canonical retained
copy when one exists, verifies the bytes, and prints a JSON import plan.  The
optional ``--apply`` mode is the only mode that writes the owner reports.

Execution source observations are copied from the execution receipt itself.
This module never falls back to a report's reviewed/source-pair field: a
reviewed pair is not evidence that an older execution used that pair.
"""

from __future__ import annotations

import argparse
import copy
import hashlib
import json
import re
import sys
from datetime import datetime, timezone
from pathlib import Path
from typing import Any


ROOT = Path(r"C:\wksp\ProjectRebound")
LEDGER = ROOT / "docs/implementation/strict-roster-20260907"
TMP = ROOT / ".tmp/strict-roster-20260907"
REPORTS = {
    "root": LEDGER / "root-report.json",
    "backend": LEDGER / "backend-report.json",
    "toolbox": LEDGER / "toolbox-report.json",
}
MANIFEST = LEDGER / "evidence/toolbox-final-followups-20260908/evidence-manifest.json"
REQUIRED_SOURCE_REPOSITORIES = {"ProjectRebound", "Toolbox"}
HEX40 = re.compile(r"^[0-9a-fA-F]{40}$")


def sha256(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest().upper()


def load_json(path: Path) -> Any:
    return json.loads(path.read_text(encoding="utf-8-sig"))


def dump_json(path: Path, value: Any) -> None:
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def path_from_value(value: Any) -> Path | None:
    if not isinstance(value, str) or not value.strip():
        return None
    raw = value.strip().replace("/", "\\")
    path = Path(raw)
    if path.is_absolute():
        return path
    return ROOT / raw


def normal_key(path: Path | str) -> str:
    return str(path).replace("/", "\\").lower()


def source_repository_name(name: Any) -> str:
    if not isinstance(name, str):
        return str(name)
    lowered = name.lower()
    if lowered in {"toolbox", "projectreboundtoolbox"}:
        return "Toolbox"
    if lowered in {"projectrebound", "rebound"}:
        return "ProjectRebound"
    return name


def complete_source_pair(pair: Any) -> bool:
    if not isinstance(pair, dict) or set(pair) != REQUIRED_SOURCE_REPOSITORIES:
        return False
    return all(isinstance(value, str) and HEX40.fullmatch(value) for value in pair.values())


def finalize_source_observation(observation: dict[str, Any]) -> dict[str, Any]:
    pair = {
        source_repository_name(name): value
        for name, value in observation.get("source_pair", {}).items()
        if isinstance(value, str) and value
    }
    dirty_by_repository = {
        source_repository_name(name): value
        for name, value in observation.get("source_dirty_by_repository", {}).items()
    }
    observation["source_pair"] = pair
    observation["source_dirty_by_repository"] = dirty_by_repository
    observation["source_pair_complete"] = complete_source_pair(pair)
    if set(dirty_by_repository) == REQUIRED_SOURCE_REPOSITORIES:
        observation["source_dirty"] = any(value is True for value in dirty_by_repository.values())
        observation["source_dirty_scope"] = "both required repositories"
    else:
        # Preserve repository-specific dirty facts without presenting one
        # repository's clean state as a clean two-repository execution pair.
        observation["source_dirty"] = None
        observation["source_dirty_scope"] = "repository-specific only; complete pair dirty state was not observed"
    return observation


def resolve_retained_path(value: Any) -> tuple[Path | None, str]:
    """Resolve a receipt path, preferring a byte-identical canonical copy."""

    original = path_from_value(value)
    if original is None:
        return None, "missing"
    candidates = [original]
    key = normal_key(original)
    # These are the only known private-to-canonical evidence mappings used by
    # this import.  The fallback remains the original retained .tmp path.
    suffix_map = {
        r".tmp\strict-roster-20260907\backend-restricted-meta-schema48-harness-20260908.log":
            LEDGER / "evidence/toolbox-final-followups-20260908/backend-restricted-meta-schema48-harness-20260908.log",
        r".tmp\strict-roster-20260907\backend-restricted-meta-schema48-race-harness-20260908.log":
            LEDGER / "evidence/toolbox-final-followups-20260908/backend-restricted-meta-schema48-race-harness-20260908.log",
        r".tmp\strict-roster-20260907\local-s3-rerun-20260908\storage-test.log":
            LEDGER / "evidence/toolbox-final-followups-20260908/s3-storage-test-20260908.log",
        r".tmp\strict-roster-20260907\toolbox-same-route-recovery-final-20260908.log":
            LEDGER / "evidence/toolbox-final-followups-20260908/toolbox-same-route-recovery-final-20260908.log",
        r".tmp\strict-roster-20260907\toolbox-full-lib-same-route-20260908.log":
            LEDGER / "evidence/toolbox-final-followups-20260908/toolbox-full-lib-same-route-20260908.log",
        r".tmp\strict-roster-20260907\toolbox-all-targets-same-route-20260908.log":
            LEDGER / "evidence/toolbox-final-followups-20260908/toolbox-all-targets-same-route-20260908.log",
        r".tmp\strict-roster-20260907\toolbox-same-route-fmt-check-20260908.log":
            LEDGER / "evidence/toolbox-final-followups-20260908/toolbox-same-route-fmt-check-20260908.log",
        r".tmp\strict-roster-20260907\schema48-self-update-tests-20260908.log":
            LEDGER / "evidence/schema48-toolbox-update-20260908/schema48-self-update-tests-20260908.log",
        r".tmp\strict-roster-20260907\schema48-fmt-20260908.log":
            LEDGER / "evidence/schema48-toolbox-update-20260908/schema48-fmt-20260908.log",
        r".tmp\strict-roster-20260907\relay-windows-crosslang-20260908\go-test-rust-driver.log":
            LEDGER / "evidence/final-supplements/relay-windows-crosslang-20260908/go-test-rust-driver.log",
    }
    for suffix, retained in suffix_map.items():
        if key.endswith(suffix) and retained not in candidates:
            candidates.insert(0, retained)
    for candidate in candidates:
        if candidate.is_file():
            scope = "docs" if normal_key(candidate).startswith(normal_key(LEDGER)) else "private_tmp"
            return candidate, scope
    return original, "missing"


def source_observation_from(value: Any, owner_hint: str | None = None) -> dict[str, Any]:
    """Extract execution source facts without inventing a reviewed pair."""

    observation: dict[str, Any] = {
        "source_pair": {},
        "source_dirty": None,
        "source_dirty_by_repository": {},
        "source_pair_complete": False,
        "source_observation": "execution receipt did not provide a complete reviewed pair",
    }
    if not isinstance(value, dict):
        return observation

    snapshot = value.get("source_snapshot")
    repositories = value.get("repositories")
    if isinstance(snapshot, dict):
        for repo, item in snapshot.items():
            if not isinstance(item, dict):
                continue
            head = item.get("head")
            if isinstance(head, str) and head:
                observation["source_pair"][source_repository_name(repo)] = head
            if "dirty" in item:
                observation["source_dirty_by_repository"][source_repository_name(repo)] = item["dirty"]
            if item.get("relevant_file_sha256"):
                observation.setdefault("source_file_hashes", {}).update(item["relevant_file_sha256"])
        if observation["source_dirty_by_repository"]:
            observation["source_dirty"] = any(
                item is True for item in observation["source_dirty_by_repository"].values()
            )
    elif isinstance(repositories, dict):
        for repo, item in repositories.items():
            if not isinstance(item, dict):
                continue
            head = item.get("head")
            if isinstance(head, str) and head:
                observation["source_pair"][source_repository_name(repo)] = head
            dirty = item.get("dirty")
            if dirty is None and isinstance(item.get("dirty_files"), list):
                dirty = bool(item["dirty_files"])
            if dirty is not None:
                observation["source_dirty_by_repository"][source_repository_name(repo)] = dirty
    execution_pair = value.get("execution_source_pair")
    if isinstance(execution_pair, dict):
        for repo, head in execution_pair.items():
            if isinstance(head, str) and head:
                observation["source_pair"][source_repository_name(repo)] = head
    execution_repository = value.get("execution_repository") or owner_hint
    execution_dirty = value.get("source_dirty")
    if isinstance(execution_dirty, dict):
        for repo, dirty in execution_dirty.items():
            if isinstance(dirty, bool):
                observation["source_dirty_by_repository"][source_repository_name(repo)] = dirty
    elif isinstance(execution_dirty, bool) and execution_repository:
        observation["source_dirty_by_repository"][source_repository_name(execution_repository)] = execution_dirty
    direct_commit = value.get("source_commit")
    if isinstance(direct_commit, str) and direct_commit:
        repository = source_repository_name(execution_repository or "unknown")
        observation["source_pair"][repository] = direct_commit
        if isinstance(execution_dirty, bool):
            observation["source_dirty_by_repository"] = {
                repository: execution_dirty
            }
    direct_head = value.get("source_head_observed") or value.get("source_head")
    if isinstance(direct_head, str) and direct_head:
        repository = source_repository_name(owner_hint or "ProjectRebound")
        observation["source_pair"][repository] = direct_head
        if isinstance(value.get("source_tree_dirty"), bool):
            observation["source_dirty_by_repository"] = {
                repository: value["source_tree_dirty"]
            }
    if isinstance(value.get("source_commit_before_change"), str):
        observation["source_pair"] = {"Toolbox": value["source_commit_before_change"]}
        observation["source_pair_complete"] = False
        observation["source_pair_semantics"] = "pre_change_baseline_only; post-change execution commit was not recorded"
        observation["source_observation"] = "only the pre-change baseline was recorded; reviewed pair is intentionally not substituted"
    else:
        pair = observation["source_pair"]
        observation["source_pair_complete"] = complete_source_pair(pair)
        if pair:
            observation["source_observation"] = "copied from execution receipt; not from report reviewed_source_pair"
    if isinstance(value.get("source_files"), list):
        observation["source_files"] = copy.deepcopy(value["source_files"])
    if isinstance(value.get("changed_files"), list):
        observation["changed_files"] = copy.deepcopy(value["changed_files"])
    return finalize_source_observation(observation)


def merge_source_observation(base: dict[str, Any], overlay: dict[str, Any]) -> dict[str, Any]:
    merged = copy.deepcopy(base)
    for key, value in overlay.items():
        if key == "source_pair" and value:
            merged[key] = value
        elif key == "source_dirty_by_repository" and value:
            merged[key] = value
        elif key == "source_dirty" and value is not None:
            merged[key] = value
        elif key not in merged or not merged[key]:
            merged[key] = value
    return finalize_source_observation(merged)


def receipt_hash(path: Path) -> str:
    return sha256(path)


def log_observation(raw_log: Any, expected_sha: Any) -> tuple[dict[str, Any], list[str]]:
    problems: list[str] = []
    retained, scope = resolve_retained_path(raw_log)
    item: dict[str, Any] = {
        "execution_log_path": str(raw_log) if raw_log is not None else None,
        "retained_log_path": str(retained) if retained is not None else None,
        "retained_log_scope": scope,
        "execution_log_sha256": str(expected_sha).upper() if isinstance(expected_sha, str) else None,
        "retained_log_sha256": None,
        "retained_log_digest_observed_at_utc": datetime.now(timezone.utc).isoformat(),
        "retained_digest_is_execution_time_claim": False,
    }
    if raw_log is None:
        item["retained_log_note"] = "The execution receipt did not record a log path; no digest or exit code is invented."
        return item, problems
    if retained is None or not retained.is_file():
        problems.append(f"missing retained log: {raw_log}")
        return item, problems
    actual = sha256(retained)
    item["retained_log_sha256"] = actual
    if isinstance(expected_sha, str) and expected_sha:
        if actual.lower() != expected_sha.lower():
            problems.append(
                f"log sha mismatch for {raw_log}: receipt={expected_sha.upper()} retained={actual}"
            )
    else:
        item["retained_digest_note"] = (
            "No execution-time digest was present; this digest was collected during this review and must not be relabeled as the execution digest."
        )
    return item, problems


def make_record(
    record_id: str,
    owner: str,
    targets: list[str],
    test: dict[str, Any],
    source: dict[str, Any],
    receipt_path: Path,
    issue_ids: list[str],
    *,
    status: str | None = None,
    parent_exit_code: int | None = None,
    note: str | None = None,
) -> tuple[dict[str, Any], list[str]]:
    problems: list[str] = []
    command = test.get("command", test.get("command_or_harness"))
    raw_log = test.get("log_path", test.get("log"))
    expected_sha = test.get("log_sha256", test.get("sha256"))
    log, log_problems = log_observation(raw_log, expected_sha)
    problems.extend(log_problems)
    exit_code = test.get("exit_code") if "exit_code" in test else None
    if status == "SKIP":
        parent_exit_code = exit_code if parent_exit_code is None else parent_exit_code
        exit_code = None
    if status is None:
        if exit_code == 0 and test.get("skip_names"):
            status = "PASS_WITH_EXPLICIT_SKIPS"
        elif exit_code == 0:
            status = "PASS"
        elif exit_code is None:
            status = "NOT_RUN"
        else:
            status = "EXECUTED_FAIL"
    receipt_actual = receipt_hash(receipt_path) if receipt_path.is_file() else None
    if receipt_actual is None:
        problems.append(f"missing receipt: {receipt_path}")
    record: dict[str, Any] = {
        "id": record_id,
        "owner": owner,
        "report_targets": targets,
        "status": status,
        "source_pair": source.get("source_pair") or None,
        "source_dirty": source.get("source_dirty"),
        "source_dirty_by_repository": source.get("source_dirty_by_repository", {}),
        "source_pair_complete": source.get("source_pair_complete", False),
        "source_observation": source.get("source_observation"),
        "command": command,
        "exit_code": exit_code,
        "pass_count": test.get(
            "pass_count",
            test.get("passed", test.get("pass_tests", test.get("top_pass"))),
        ),
        "subtest_pass_count": test.get("subtest_pass_count"),
        "filtered_count": test.get("filtered", test.get("filtered_count")),
        "skip_names": copy.deepcopy(test.get("skip_names", [])),
        "parent_command_exit_code": parent_exit_code,
        "execution_evidence": log,
        "receipt_path": str(receipt_path),
        "receipt_sha256": receipt_actual,
        "issue_ids": issue_ids,
    }
    if source.get("source_files"):
        record["source_files"] = copy.deepcopy(source["source_files"])
    if source.get("changed_files"):
        record["changed_files"] = copy.deepcopy(source["changed_files"])
    # Keep the fields consumed by the ledger's evidence checker at the record
    # top level, but only when the receipt supplied an execution-time digest
    # and the retained bytes matched it.  A review-time digest alone remains
    # in execution_evidence and is never promoted to log_sha256.
    if (
        isinstance(expected_sha, str)
        and bool(expected_sha)
        and not log_problems
        and log.get("retained_log_path")
        and isinstance(log.get("retained_log_sha256"), str)
        and log["retained_log_sha256"].lower() == expected_sha.lower()
    ):
        record["log_path"] = log["retained_log_path"]
        record["log_sha256"] = expected_sha.upper()
        record["execution_source_pair"] = copy.deepcopy(source.get("source_pair", {}))
    if note:
        record["note"] = note
    if problems:
        record["evidence_problems"] = problems
        if status == "PASS":
            record["status"] = "BLOCKED_EVIDENCE_MISMATCH"
    return record, problems


def load_report_inputs() -> tuple[dict[str, dict[str, Any]], list[str]]:
    loaded: dict[str, dict[str, Any]] = {}
    problems: list[str] = []
    for name, path in REPORTS.items():
        if not path.is_file():
            problems.append(f"missing {name} report: {path}")
            continue
        try:
            report = load_json(path)
        except Exception as exc:  # pragma: no cover - diagnostic path
            problems.append(f"cannot parse {name} report: {exc}")
            continue
        if not isinstance(report, dict):
            problems.append(f"{name} report is not an object")
            continue
        loaded[name] = {
            "path": str(path),
            "sha256": sha256(path),
            "issue_count": len(report.get("issues", [])) if isinstance(report.get("issues"), list) else None,
            "test_count": len(report.get("tests", [])) if isinstance(report.get("tests"), list) else None,
        }
    return loaded, problems


def append_record(records: list[dict[str, Any]], record: dict[str, Any]) -> None:
    if any(existing.get("id") == record.get("id") for existing in records):
        return
    records.append(record)


def build_plan() -> tuple[dict[str, Any], list[str]]:
    report_inputs, problems = load_report_inputs()
    if not MANIFEST.is_file():
        problems.append(f"missing canonical evidence manifest: {MANIFEST}")
        manifest: dict[str, Any] = {}
    else:
        manifest = load_json(MANIFEST)
    records: list[dict[str, Any]] = []

    host_path = LEDGER / "evidence/backend-host-scope-fix-evidence-20260908.json"
    if host_path.is_file():
        host = load_json(host_path)
        host_source = source_observation_from(host, "ProjectRebound")
        for test in host.get("tests", []):
            name = test.get("name", "unknown")
            if name == "focused-host-scope-positive":
                rid, status, issues = "backend-host-scope-focused-positive", "PASS", ["BP-035", "BP-043", "BP-045"]
            elif name == "focused-host-scope-negative-control":
                rid, status, issues = "backend-host-scope-negative-control", "EXPECTED_FAIL", ["BP-043"]
            elif name == "schema47-to-48-live-history":
                rid, status, issues = "backend-schema47-to-48-live-history", "PASS", ["BP-049"]
            elif name == "full-backend-race-p1":
                rid, status, issues = "backend-host-scope-full-race-403", "PASS_WITH_EXPLICIT_SKIPS", ["BP-035", "BP-036", "BP-043", "BP-045", "BP-046", "BP-047", "BP-048", "BP-049"]
            else:
                continue
            enriched = dict(test)
            if "top_pass" in test:
                enriched["pass_count"] = test["top_pass"]
                enriched["subtest_pass_count"] = test.get("subtest_pass_one_level", 0) + test.get("subtest_pass_deeper", 0)
            record, record_problems = make_record(
                rid, "backend", ["backend", "root"], enriched, host_source, host_path, issues,
                status=status,
                note=test.get("observed"),
            )
            append_record(records, record)
            problems.extend(f"{rid}: {item}" for item in record_problems)
            if name == "full-backend-race-p1":
                for skip_name in test.get("skip_names", []):
                    skip_test = dict(test)
                    skip_test["skip_names"] = [skip_name]
                    skip_test["pass_count"] = None
                    skip_test["exit_code"] = None
                    skip_record, skip_problems = make_record(
                        f"backend-host-scope-skip-{re.sub(r'[^a-z0-9]+', '-', skip_name.lower()).strip('-')}",
                        "backend", ["backend", "root"], skip_test, host_source, host_path, ["BP-035", "BP-048", "BP-049"],
                        status="SKIP", parent_exit_code=test.get("exit_code"),
                        note="Explicit test-level SKIP inside the 403-PASS host-scope race command; no per-test exit code exists, so exit_code remains null.",
                    )
                    append_record(records, skip_record)
                    problems.extend(f"{skip_record['id']}: {item}" for item in skip_problems)
    else:
        problems.append(f"missing HOST scope receipt: {host_path}")

    component_path = TMP / "backend-component-rerun-receipt-20260908.json"
    if component_path.is_file():
        component = load_json(component_path)
        component_source = source_observation_from(component, "ProjectRebound")
        selected = {
            "schema48-migration-bootstrap": ("backend-schema48-migration-bootstrap", ["BP-049"]),
            "restricted-meta-permissions-current-schema48": ("backend-meta-schema48-current", ["BP-049"]),
            "restricted-meta-permissions-current-schema48-race": ("backend-meta-schema48-race", ["BP-049"]),
            "s3-compatible-storage-current": ("backend-s3-current", ["BP-049"]),
            "restricted-meta-old-schema47-harness": ("backend-meta-schema47-rejected", ["BP-049"]),
            "s3-compatible-storage-initial-attempt-blocked": ("backend-s3-initial-blocked", ["BP-049"]),
            "battlelog-real-game-authority": ("backend-battlelog-native-not-run", ["BP-034"]),
        }
        for test in component.get("tests", []):
            if test.get("id") not in selected:
                continue
            rid, issues = selected[test["id"]]
            status = {
                "restricted-meta-old-schema47-harness": "EXECUTED_FAIL",
                "s3-compatible-storage-initial-attempt-blocked": "BLOCKED",
                "battlelog-real-game-authority": "NOT_RUN",
            }.get(test["id"])
            record, record_problems = make_record(
                rid, "backend", ["backend", "root"], test, component_source, component_path, issues,
                status=status, note=test.get("note"),
            )
            append_record(records, record)
            problems.extend(f"{rid}: {item}" for item in record_problems)
    else:
        problems.append(f"missing Backend component receipt: {component_path}")

    schema_path = LEDGER / "evidence/schema48-toolbox-update-20260908/schema48-toolbox-update-receipt-20260908.json"
    if schema_path.is_file():
        schema_receipt = load_json(schema_path)
        schema_source = source_observation_from(schema_receipt, "Toolbox")
        for index, test in enumerate(schema_receipt.get("tests", [])):
            enriched = dict(test)
            if "log_path" in enriched:
                enriched["log_path"] = enriched["log_path"]
            record, record_problems = make_record(
                f"toolbox-schema48-update-{index + 1}", "toolbox", ["toolbox", "root"], enriched,
                schema_source, schema_path, ["BP-050", "BP-051"], note=schema_receipt.get("scope_note"),
            )
            append_record(records, record)
            problems.extend(f"{record['id']}: {item}" for item in record_problems)
    else:
        problems.append(f"missing Toolbox schema48 receipt: {schema_path}")

    route_path = TMP / "host-route-recovery-fix-receipt-20260908.json"
    if route_path.is_file():
        route_receipt = load_json(route_path)
        route_source = source_observation_from(route_receipt, "Toolbox")
        route_note = (
            "Execution passed, but the receipt records only source_commit_before_change; the post-change execution commit and dirty state were not captured. "
            "This record must remain source-binding incomplete and must not inherit the reviewed pair."
        )
        route_tests = route_receipt.get("tests", [])
        for index, test in enumerate(route_tests):
            record, record_problems = make_record(
                f"toolbox-same-route-followup-{index + 1}", "toolbox", ["toolbox", "root"], test,
                route_source, route_path, ["BP-043", "BP-044"],
                status="PASS" if test.get("exit_code") == 0 else "EXECUTED_FAIL", note=route_note,
            )
            record["source_binding_status"] = "INCOMPLETE"
            append_record(records, record)
            problems.extend(f"{record['id']}: {item}" for item in record_problems)
    else:
        problems.append(f"missing Toolbox same-route receipt: {route_path}")

    tauri_path = LEDGER / "evidence/toolbox-final-followups-20260908/tauri-e360-execution-receipt.json"
    if tauri_path.is_file():
        tauri = load_json(tauri_path)
        source = source_observation_from(tauri, "Toolbox")
        test = dict(tauri)
        test["pass_count"] = tauri.get("tests", {}).get("passed")
        test["skip_names"] = []
        record, record_problems = make_record(
            "toolbox-tauri-e360", "toolbox", ["toolbox", "root"], test, source, tauri_path, ["BP-051"],
            status="PASS", note=tauri.get("scope"),
        )
        append_record(records, record)
        problems.extend(f"toolbox-tauri-e360: {item}" for item in record_problems)
    else:
        problems.append(f"missing e360 Tauri receipt: {tauri_path}")

    auth_receipts = sorted((LEDGER / "evidence").glob("auth-http-current-*/receipt.json"))
    if auth_receipts:
        auth_path = auth_receipts[-1]
        auth = load_json(auth_path)
        auth_source = source_observation_from(auth, auth.get("execution_repository") or "Toolbox")
        commands = auth.get("commands", [])
        if not isinstance(commands, list) or not commands:
            problems.append(f"auth HTTP receipt has no commands: {auth_path}")
        else:
            for index, command in enumerate(commands):
                if not isinstance(command, dict):
                    problems.append(f"auth HTTP receipt command {index} is not an object: {auth_path}")
                    continue
                command_name = "build" if index == 0 else "driver"
                exit_code = command.get("exit_code")
                if exit_code == 0:
                    command_status = "PASS"
                elif exit_code is None:
                    command_status = "NOT_RUN"
                else:
                    command_status = "EXECUTED_FAIL"
                test = {
                    "command": command.get("command"),
                    "exit_code": exit_code,
                    "log_path": command.get("log_path"),
                    "log_sha256": command.get("log_sha256"),
                    "skip_names": [],
                    # The receipt does not claim a test-case count for either
                    # command, so leave pass_count unset rather than infer it
                    # from a zero exit code.
                }
                record_id = f"toolbox-auth-http-current-{command_name}"
                record, record_problems = make_record(
                    record_id,
                    "toolbox",
                    ["toolbox", "root"],
                    test,
                    auth_source,
                    auth_path,
                    ["BP-002"],
                    status=command_status,
                    note=(
                        "Current Toolbox-only loopback HTTP/isolated-DPAPI execution with synthetic credentials; "
                        "the receipt explicitly excludes native gameplay and full E2E-11 acceptance."
                    ),
                )
                append_record(records, record)
                problems.extend(f"{record_id}: {item}" for item in record_problems)
    else:
        problems.append("missing current auth HTTP receipt under evidence/auth-http-current-*/receipt.json")

    relay_path = LEDGER / "evidence/final-supplements/relay-windows-crosslang-20260908/relay-windows-crosslang-receipt.json"
    if relay_path.is_file():
        relay = load_json(relay_path)
        source = source_observation_from(relay, None)
        test = dict(relay)
        test["log_path"] = relay.get("log_path")
        test["log_sha256"] = relay.get("log_sha256")
        record, record_problems = make_record(
            "backend-windows-cross-language-relay", "backend", ["backend", "root"], test, source, relay_path,
            ["BP-013", "BP-014"], status="PASS" if relay.get("status") == "PASS" else relay.get("status"),
            note=relay.get("scope"),
        )
        record["source_binding_status"] = "INCOMPLETE" if relay.get("driver", {}).get("source_commit", "").startswith("UNAVAILABLE") else "COMPLETE"
        append_record(records, record)
        problems.extend(f"backend-windows-cross-language-relay: {item}" for item in record_problems)
    else:
        problems.append(f"missing Windows cross-language relay receipt: {relay_path}")

    def add_gate_record(gate_path: Path, record_id: str, historical_note: str | None = None) -> None:
        if not gate_path.is_file():
            problems.append(f"missing provenance gate receipt: {gate_path}")
            return
        gate = load_json(gate_path)
        source = source_observation_from(gate, "ProjectRebound")
        # The older private receipt wrapped the actual test in ``tests``;
        # preserve that execution record rather than treating the receipt
        # envelope (which has no exit code) as a new failed run.
        if isinstance(gate.get("tests"), list) and gate["tests"]:
            test = dict(gate["tests"][0])
            test_source = dict(gate)
            test_source.pop("tests", None)
            source = merge_source_observation(source, source_observation_from(test_source, "ProjectRebound"))
        else:
            test = dict(gate)
        log_path = gate.get("log_path")
        retained_log, _scope = resolve_retained_path(log_path)
        if test.get("pass_count") is None and retained_log is not None and retained_log.is_file():
            match = re.search(r"Ran (\d+) tests?", retained_log.read_text(encoding="utf-8", errors="replace"))
            if match:
                test["pass_count"] = int(match.group(1))
        test["skip_names"] = []
        record, record_problems = make_record(
            record_id, "root", ["root"], test, source, gate_path, ["BP-051", "BP-052"],
            status=("PASS_HISTORICAL_SUPERSEDED" if historical_note and "Historical" in historical_note
                    and test.get("exit_code") == 0
                    else ("PASS" if test.get("exit_code") == 0 else "EXECUTED_FAIL")),
            note=historical_note or gate.get("scope"),
        )
        append_record(records, record)
        problems.extend(f"{record_id}: {item}" for item in record_problems)

    add_gate_record(
        LEDGER / "evidence/release-gate-20260907T225228Z/receipt.json",
        "provenance-gate-final-release",
        "Final retained release-gate execution; source was dirty and the gate did not claim native acceptance or artifact signing.",
    )
    add_gate_record(
        TMP / "provenance-gate-final-receipt-20260908.json",
        "provenance-gate-historical-working-tree",
        "Historical earlier 22-test gate run retained separately; superseded by the final stable release-gate receipt and not relabeled as that execution.",
    )

    manifest_artifacts = manifest.get("artifacts", []) if isinstance(manifest, dict) else []
    manifest_paths = {normal_key(item.get("path")): item for item in manifest_artifacts if isinstance(item, dict)}
    for record in records:
        receipt_key = normal_key(record.get("receipt_path", ""))
        if receipt_key and receipt_key not in manifest_paths and record.get("receipt_path", "").startswith(str(LEDGER)):
            record.setdefault("manifest_note", "Receipt is outside the 22-artifact Toolbox follow-up manifest; retained separately.")

    plan = {
        "schema_version": 1,
        "plan_id": "final-followup-test-import-20260908",
        "mode": "dry-run",
        "generated_at_utc": datetime.now(timezone.utc).isoformat(),
        "inputs": {
            "reports": report_inputs,
            "canonical_evidence_manifest": {
                "path": str(MANIFEST),
                "sha256": sha256(MANIFEST) if MANIFEST.is_file() else None,
                "artifact_count": len(manifest_artifacts),
            },
        },
        "source_pair_policy": "Execution source facts come only from receipts/source snapshots. Reviewed report pairs are never copied into an execution record.",
        "records": records,
        "record_count": len(records),
        "records_by_owner": {
            owner: sum(1 for item in records if item.get("owner") == owner)
            for owner in ("root", "backend", "toolbox")
        },
        "problems": problems,
        "apply_policy": "Default is read-only. --apply is rejected while evidence problems exist; with a clean plan it upserts records by id and appends only deduplicated evidence paths to the listed owner reports.",
    }
    return plan, problems


def apply_plan(plan: dict[str, Any]) -> None:
    by_report: dict[str, list[dict[str, Any]]] = {name: [] for name in REPORTS}
    for record in plan["records"]:
        for target in record.get("report_targets", []):
            if target in by_report:
                by_report[target].append(record)
    for name, records in by_report.items():
        if not records:
            continue
        path = REPORTS[name]
        report = load_json(path)
        tests = report.setdefault("tests", [])
        existing = {item.get("id"): index for index, item in enumerate(tests) if isinstance(item, dict) and item.get("id")}
        for record in records:
            imported = copy.deepcopy(record)
            imported["imported_by"] = "import_final_followup_tests.py"
            imported["import_source_receipt"] = record.get("receipt_path")
            if imported.get("id") in existing:
                tests[existing[imported["id"]]] = imported
            else:
                existing[imported["id"]] = len(tests)
                tests.append(imported)
            for issue in report.get("issues", []):
                if issue.get("id") in record.get("issue_ids", []):
                    evidence = issue.setdefault("evidence", [])
                    for path_value in (
                        record.get("execution_evidence", {}).get("retained_log_path"),
                        record.get("receipt_path"),
                    ):
                        if path_value and path_value not in evidence:
                            evidence.append(path_value)
        dump_json(path, report)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--apply", action="store_true", help="upsert the plan into owner reports")
    args = parser.parse_args()
    plan, problems = build_plan()
    # Never mutate owner reports when the plan has unresolved evidence
    # problems.  The caller must first correct or explicitly account for the
    # missing evidence in a subsequent dry-run; a partially imported plan
    # would make the reports look newer than the actual execution record.
    if args.apply and problems:
        plan["mode"] = "apply-rejected"
        plan["apply_rejection"] = "unresolved evidence problems; no owner report was written"
    elif args.apply:
        apply_plan(plan)
        plan["mode"] = "apply"
    print(json.dumps(plan, ensure_ascii=False, indent=2))
    # Missing or mismatched evidence is never silently promoted.  A dry-run
    # still exits nonzero so the caller must inspect the printed plan before a
    # release ledger can consume it.
    return 2 if problems else 0


if __name__ == "__main__":
    sys.exit(main())
