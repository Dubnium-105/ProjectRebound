#!/usr/bin/env python3
"""Collect build provenance without executing artifacts; refuse incomplete releases."""
import argparse
import hashlib
import json
import re
import subprocess
import tomllib
from pathlib import Path
from datetime import datetime, timezone


SOURCE_NAMES = ("ProjectRebound", "Toolbox")
SOURCE_PAIR_KEYS = set(SOURCE_NAMES)
SHA256_RE = re.compile(r"[0-9a-f]{64}\Z")
COMMIT_RE = re.compile(r"[0-9a-f]{40}\Z")


def run(*args, cwd=None):
    result = subprocess.run(args, cwd=cwd, text=True, encoding="utf-8", errors="replace", capture_output=True)
    return {"command": list(args), "exit_code": result.returncode, "output": result.stdout.strip(), "error": result.stderr.strip()}


def sha(path):
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        while chunk := stream.read(1024 * 1024):
            digest.update(chunk)
    return digest.hexdigest()


def normalized_artifact_name(path):
    """Return the case-insensitive artifact basename used by all gates."""
    return Path(path).name.lower()


def artifact_kind(path_or_role):
    value = normalized_artifact_name(path_or_role)
    if value in ("control-plane", "meta-server", "edge-relay"):
        return value
    if value in ("payload", "payload.dll"):
        return "payload.dll"
    if value in ("tauri toolbox", "rebound_toolbox_tauri.exe", "tauri"):
        return "rebound_toolbox_tauri.exe"
    return None


def embedded_go_source_problems(path, build_info, reviewed_commit):
    """Check Go VCS stamping using the same normalized role as the inventory."""
    if artifact_kind(path) not in ("control-plane", "meta-server", "edge-relay"):
        return [], {}
    settings = {}
    for line in build_info.get("output", "").splitlines():
        parts = line.strip().split(None, 1)
        if len(parts) == 2 and parts[0] == "build" and "=" in parts[1]:
            key, value = parts[1].split("=", 1)
            settings[key] = value
    problems = []
    if build_info.get("exit_code") or settings.get("vcs.revision") != reviewed_commit:
        problems.append(Path(path).name + " does not embed the reviewed source revision")
    if settings.get("vcs.modified") != "false":
        problems.append(Path(path).name + " was not stamped as an unmodified source build")
    return problems, settings


def initial_artifact_release_state(path, source_problems=()):
    """Return the non-attested initial state before acceptance can be applied."""
    if normalized_artifact_name(path) == "matchserver":
        return False, "historical tracked binary excluded from all current source-built containers"
    if source_problems:
        return False, "embedded/source provenance validation failed"
    return None, "hash collection alone does not prove signature, compatibility, or native acceptance"


def valid_sha256(value):
    return isinstance(value, str) and bool(SHA256_RE.fullmatch(value.lower()))


def valid_commit(value):
    return isinstance(value, str) and bool(COMMIT_RE.fullmatch(value.lower()))


def valid_source_pair(value):
    return (isinstance(value, dict) and set(value) == SOURCE_PAIR_KEYS
            and all(valid_commit(value.get(key)) for key in SOURCE_NAMES))


def digest_set(items):
    if not isinstance(items, list) or not items:
        return None
    values = [item.get("sha256") if isinstance(item, dict) else None for item in items]
    if any(not valid_sha256(value) for value in values):
        return None
    return set(values) if len(set(values)) == len(values) else None


def source_tree_state(repo):
    """Capture the complete committed-tree state, including untracked files."""
    head = run("git", "rev-parse", "--verify", "HEAD", cwd=repo)
    state = run("git", "status", "--porcelain=v1", "--untracked-files=all", cwd=repo)
    return {
        "path": str(Path(repo).resolve()),
        "commit": head["output"],
        "dirty": bool(state["output"]),
        "status_output": state["output"],
        "git_exit_codes": [head["exit_code"], state["exit_code"]],
    }


def _path_sha_problem(path_value, expected, label):
    path = Path(path_value) if isinstance(path_value, str) else None
    if path is None or not path.is_file():
        return f"{label} is missing: {path_value}"
    if not valid_sha256(expected):
        return f"{label} has an invalid sha256"
    if sha(path).lower() != expected.lower():
        return f"{label} sha256 does not match its manifest"
    return None


def validate_candidate_binding(inventory_path, candidate_path, artifact_paths, repositories):
    """Validate the source/build/game binding for non-Go candidate artifacts.

    ``artifact-inventory.json`` is the stable, hash-addressed bridge between
    the files supplied to this command and the candidate-build manifest.  The
    candidate manifest is deliberately still checked independently: a path and
    a self-reported source commit are not evidence until the bytes at those
    paths and the checked-out source pair agree.
    """
    problems = []
    binding = {"validated": False}
    if inventory_path is None:
        return ["non-Go artifacts require --artifact-inventory"], binding
    inventory_path = Path(inventory_path).resolve()
    if not inventory_path.is_file():
        return ["artifact inventory is missing: " + str(inventory_path)], binding
    try:
        inventory = json.loads(inventory_path.read_text(encoding="utf-8-sig"))
    except (OSError, json.JSONDecodeError) as exc:
        return [f"artifact inventory cannot be read: {exc}"], binding
    binding.update({"inventory_path": str(inventory_path), "inventory_sha256": sha(inventory_path)})
    if inventory.get("schema_version") != 1:
        problems.append("artifact inventory schema_version must be 1")
    if not isinstance(inventory.get("generation_id"), str) or not inventory["generation_id"].strip():
        problems.append("artifact inventory lacks a stable generation_id")
    reviewed_pair = {name: repositories.get(name, {}).get("commit") for name in SOURCE_NAMES}
    inventory_pair = inventory.get("source_commits")
    if not valid_source_pair(inventory_pair) or inventory_pair != reviewed_pair:
        problems.append("artifact inventory source_commits do not match the reviewed source pair")
    inventory_artifacts = inventory.get("artifacts")
    if not isinstance(inventory_artifacts, list):
        problems.append("artifact inventory artifacts must be a list")
        inventory_artifacts = []
    by_kind = {}
    for entry in inventory_artifacts:
        if not isinstance(entry, dict):
            problems.append("artifact inventory contains a non-object artifact")
            continue
        kind = artifact_kind(entry.get("role", ""))
        if kind is None:
            problems.append("artifact inventory contains an unknown role")
            continue
        if kind in by_kind:
            problems.append("artifact inventory contains duplicate role " + kind)
            continue
        by_kind[kind] = entry
        path_problem = _path_sha_problem(entry.get("path"), entry.get("sha256"), kind + " inventory artifact")
        if path_problem:
            problems.append(path_problem)
        if isinstance(entry.get("bytes"), int) and isinstance(entry.get("path"), str) and Path(entry["path"]).is_file():
            if Path(entry["path"]).stat().st_size != entry["bytes"]:
                problems.append(kind + " inventory byte count does not match")
        owner = "Toolbox" if kind == "rebound_toolbox_tauri.exe" else "ProjectRebound"
        if entry.get("source_repository") != owner:
            problems.append(kind + " inventory source_repository is not " + owner)
        if entry.get("source_pair_commit") != reviewed_pair.get(owner):
            problems.append(kind + " inventory source_pair_commit does not match the reviewed source")
        if kind == "payload.dll":
            build_commit = entry.get("build_source_commit")
            subtree = entry.get("verified_source_subtree")
            current_tree = ""
            if not valid_commit(build_commit):
                problems.append("Payload inventory lacks a valid build_source_commit")
            else:
                payload_repo = repositories.get("ProjectRebound", {}).get("path")
                current_tree = run("git", "rev-parse", "HEAD:Payload", cwd=payload_repo)["output"] if payload_repo else ""
                build_tree = run("git", "rev-parse", build_commit + ":Payload", cwd=payload_repo)["output"] if payload_repo else ""
                if not current_tree or not build_tree or current_tree != build_tree:
                    problems.append("Payload build_source_commit does not match the reviewed Payload tree")
            if (not isinstance(subtree, dict) or subtree.get("path") != "Payload"
                    or not valid_commit(subtree.get("git_tree"))
                    or subtree.get("git_tree") != current_tree
                    or subtree.get("matches_reviewed_source_pair") is not True):
                problems.append("Payload inventory lacks a verified reviewed-source subtree")
    required_kinds = {artifact_kind(path) for path in artifact_paths}
    required_kinds.discard(None)
    for kind in sorted(required_kinds):
        if kind not in by_kind:
            problems.append("artifact inventory lacks " + kind)
    # The inventory is only useful when it describes the exact bytes supplied
    # to this invocation.  Check every role, including Go artifacts; checking
    # only Payload/Tauri allowed a substituted server binary to inherit a
    # candidate's attestation.
    for supplied in artifact_paths:
        kind = artifact_kind(supplied)
        entry = by_kind.get(kind) if kind is not None else None
        supplied_path = Path(supplied)
        if kind is None or entry is None or not supplied_path.is_file():
            continue
        expected = entry.get("sha256")
        if not valid_sha256(expected) or sha(supplied_path).lower() != expected.lower():
            problems.append(kind + " supplied artifact sha256 does not match inventory")
    candidate_ref = inventory.get("candidate_build_manifest")
    if not isinstance(candidate_ref, dict):
        problems.append("artifact inventory lacks candidate_build_manifest")
    else:
        ref_path = candidate_ref.get("path")
        ref_sha = candidate_ref.get("sha256")
        path_problem = _path_sha_problem(ref_path, ref_sha, "candidate_build_manifest")
        if path_problem:
            problems.append(path_problem)
    pinned_game = inventory.get("pinned_game_reference")
    if not isinstance(pinned_game, dict):
        problems.append("artifact inventory lacks pinned_game_reference")
        pinned_game = {}
    game_problem = _path_sha_problem(pinned_game.get("path"), pinned_game.get("sha256"), "pinned game reference")
    if game_problem:
        problems.append(game_problem)
    selected_candidate = Path(candidate_path).resolve() if candidate_path else None
    if selected_candidate is None and isinstance(candidate_ref, dict) and isinstance(candidate_ref.get("path"), str):
        selected_candidate = Path(candidate_ref["path"]).resolve()
    if selected_candidate is None or not selected_candidate.is_file():
        problems.append("candidate build manifest file is missing")
        candidate = {}
    else:
        try:
            candidate = json.loads(selected_candidate.read_text(encoding="utf-8-sig"))
        except (OSError, json.JSONDecodeError) as exc:
            candidate = {}
            problems.append(f"candidate build manifest cannot be read: {exc}")
        if isinstance(candidate_ref, dict) and isinstance(candidate_ref.get("sha256"), str):
            if valid_sha256(candidate_ref["sha256"]) and sha(selected_candidate).lower() != candidate_ref["sha256"].lower():
                problems.append("candidate build manifest does not match inventory sha256")
    binding["candidate_build_manifest_path"] = str(selected_candidate) if selected_candidate else None
    if selected_candidate and selected_candidate.is_file():
        binding["candidate_build_manifest_sha256"] = sha(selected_candidate)
    if candidate.get("source_commit") != reviewed_pair.get("Toolbox"):
        problems.append("candidate source_commit does not match the reviewed Toolbox source")
    if candidate.get("source_dirty") is not False:
        problems.append("candidate source_dirty is not false")
    payload_path = candidate.get("payload_path")
    toolbox_path = candidate.get("toolbox_path")
    for label, path_value, digest, kind in (
        ("candidate Payload", payload_path, candidate.get("payload_sha256"), "payload.dll"),
        ("candidate Toolbox", toolbox_path, candidate.get("toolbox_sha256"), "rebound_toolbox_tauri.exe"),
    ):
        problem = _path_sha_problem(path_value, digest, label)
        if problem:
            problems.append(problem)
        inventory_entry = by_kind.get(kind)
        if inventory_entry and (not valid_sha256(digest) or digest.lower() != str(inventory_entry.get("sha256", "")).lower()):
            problems.append(label + " sha256 does not match artifact inventory")
        provided = next((Path(path) for path in artifact_paths if artifact_kind(path) == kind), None)
        if provided is not None and valid_sha256(digest) and sha(provided).lower() != digest.lower():
            problems.append(label + " sha256 does not match supplied artifact")
    candidate_game_sha = candidate.get("game_sha256")
    if not valid_sha256(candidate_game_sha) or candidate_game_sha.lower() != str(pinned_game.get("sha256", "")).lower():
        problems.append("candidate game_sha256 does not match pinned_game_reference")
    binding["source_commits"] = reviewed_pair
    binding["generation_id"] = inventory.get("generation_id")
    binding["validated"] = not problems
    return problems, binding


def artifact_inventory_problems(paths):
    expected = {"control-plane", "meta-server", "edge-relay", "payload.dll",
                "rebound_toolbox_tauri.exe"}
    names = [Path(path).name.lower() for path in paths]
    if len(names) != len(expected) or set(names) != expected:
        return ["release artifacts must contain exactly control-plane, meta-server, edge-relay, Payload.dll, and rebound_toolbox_tauri.exe"]
    return []


def _resolve_evidence_path(value, report_path=None):
    if not isinstance(value, str) or not value:
        return None
    path = Path(value)
    if not path.is_absolute() and report_path is not None:
        path = report_path.parent / path
    return path


def _execution_step_problems(case, evidence, report_path=None):
    """Validate the execution steps which justify a passing E2E case.

    The acceptance contract contains prose steps. The generated ledger must map
    every one of them to one or more observed execution steps explicitly; a
    count of arbitrary PASS strings is insufficient.
    """
    problems = []
    case_id = str(case.get("id"))
    contract_steps = case.get("steps")
    execution_steps = evidence.get("execution_steps")
    if not isinstance(contract_steps, list) or not contract_steps:
        return [case_id + " has no executable contract-step definition"]
    if not isinstance(execution_steps, list) or not execution_steps:
        return [case_id + " passed without execution_steps"]
    covered = set()
    for index, step in enumerate(execution_steps):
        prefix = f"{case_id} execution step {index}"
        if not isinstance(step, dict):
            problems.append(prefix + " is not an object")
            continue
        if str(step.get("status", "")).lower() not in ("pass", "passed"):
            problems.append(prefix + " is not passed")
        raw_indices = step.get("contract_step_indices")
        if raw_indices is None and "contract_step_index" in step:
            raw_indices = [step.get("contract_step_index")]
        if not isinstance(raw_indices, list) or not raw_indices:
            problems.append(prefix + " has no explicit contract_step_indices")
            raw_indices = []
        for raw_index in raw_indices:
            if type(raw_index) is not int or raw_index < 0 or raw_index >= len(contract_steps):
                problems.append(prefix + " references an invalid contract step")
            else:
                covered.add(raw_index)
        if not isinstance(step.get("id"), str) or not step["id"]:
            problems.append(prefix + " has no stable id")
        command = step.get("command") or step.get("command_or_harness")
        if not isinstance(command, str) or not command.strip():
            problems.append(prefix + " has no command or harness")
        logs = step.get("log_paths")
        if not isinstance(logs, list) or not logs or any(not isinstance(item, str) or not item for item in logs):
            problems.append(prefix + " has no log_paths")
        elif report_path is not None:
            for item in logs:
                path = _resolve_evidence_path(item, report_path)
                if path is None or not path.is_file():
                    problems.append(prefix + " references missing log " + str(item))
        if not step.get("observed_result"):
            problems.append(prefix + " has no observed_result")
        observed_exit = step.get("exit_code")
        expected_exit = step.get("expected_exit_code", 0)
        if type(observed_exit) is not int or type(expected_exit) is not int:
            problems.append(prefix + " has no integer exit/expected_exit code")
        elif observed_exit != expected_exit:
            problems.append(prefix + " observed exit does not match expected exit")
    expected_coverage = set(range(len(contract_steps)))
    if covered != expected_coverage:
        problems.append(case_id + " execution steps do not cover the contract exactly")
    return problems


def _step_without_contract_mapping(step):
    if not isinstance(step, dict):
        return step
    return {key: value for key, value in step.items()
            if key not in ("contract_step_index", "contract_step_indices")}


def _execution_report_problems(case, evidence, execution_report, report_path, reviewed_pair):
    """Bind the generated E2E result to the bytes of its execution report."""
    problems = []
    execution_path = execution_report.get("path")
    execution_digest = execution_report.get("sha256")
    if not isinstance(execution_path, str) or not execution_path or not valid_sha256(execution_digest):
        return [str(case.get("id")) + " has an invalid execution_report path or digest"]
    path = _resolve_evidence_path(execution_path, report_path)
    if path is None or not path.is_file():
        return [str(case.get("id")) + " execution_report is missing"]
    if sha(path).lower() != execution_digest.lower():
        return [str(case.get("id")) + " execution_report has a changed digest"]
    try:
        actual = json.loads(path.read_text(encoding="utf-8-sig"))
    except (OSError, json.JSONDecodeError) as exc:
        return [str(case.get("id")) + f" execution_report cannot be parsed: {exc}"]
    case_id = str(case.get("id"))
    if actual.get("id") != case_id:
        problems.append(case_id + " execution_report id does not match the case")
    actual_status = str(actual.get("status", "")).lower()
    if actual_status not in ("pass", "passed"):
        problems.append(case_id + " execution_report status is not passed")
    actual_pair = actual.get("source_pair")
    if not valid_source_pair(actual_pair):
        problems.append(case_id + " execution_report lacks a complete source pair")
    else:
        if actual_pair != reviewed_pair:
            problems.append(case_id + " execution_report source pair does not match the reviewed release pair")
        outer_pair = evidence.get("execution_source_pair") or execution_report.get("source_pair")
        if outer_pair != actual_pair:
            problems.append(case_id + " outer execution source pair differs from the execution report")
    actual_result = actual.get("result")
    if not isinstance(actual_result, dict):
        problems.append(case_id + " execution_report lacks a result object")
    else:
        for key in ("environment", "command_or_harness", "exit_code", "log_paths", "observed_result"):
            if actual_result.get(key) != evidence.get(key):
                problems.append(case_id + " execution report result differs for " + key)
    actual_steps = actual.get("steps")
    if not isinstance(actual_steps, list):
        actual_steps = actual.get("execution_steps")
    recorded_steps = evidence.get("execution_steps")
    if not isinstance(actual_steps, list) or not isinstance(recorded_steps, list):
        problems.append(case_id + " execution report and ledger lack comparable steps")
    elif (len(actual_steps) != len(recorded_steps)
          or any(_step_without_contract_mapping(actual) != _step_without_contract_mapping(recorded)
                 for actual, recorded in zip(actual_steps, recorded_steps))):
        problems.append(case_id + " execution report steps differ from ledger steps")
    return problems


def acceptance_case_problems(report, report_path=None):
    problems = []
    reviewed_pair = report.get("reviewed_source_pair") or report.get("commit_pair")
    required = {"commit_pair", "artifact_hashes", "environment", "command_or_harness",
                "exit_code", "log_paths", "observed_result"}
    for field, expected in (
        ("component_tests", {f"AC-{index:03}" for index in range(1, 53)}),
        ("e2e_tests", {f"E2E-{index:02}" for index in range(1, 23)}),
    ):
        cases = report.get(field, [])
        actual = [case.get("id") if isinstance(case, dict) else None for case in cases]
        if len(actual) != len(expected) or set(actual) != expected:
            problems.append(field + " must contain every required case exactly once")
        for case in cases:
            if not isinstance(case, dict):
                problems.append(field + " contains a non-object case")
                continue
            case_id = str(case.get("id"))
            status = str(case.get("status", "")).lower()
            if status not in ("passed", "pass"):
                problems.append(case_id + " is not passed")
            candidate_status = case.get("current_candidate_acceptance")
            if candidate_status is None and isinstance(case.get("result"), dict):
                candidate_status = case["result"].get("current_candidate_acceptance")
            if (status in ("passed", "pass")
                    and candidate_status is not None
                    and str(candidate_status).lower() not in ("passed", "pass")):
                problems.append(case_id + " is not accepted for the current candidate")
            evidence = case.get("result")
            if not isinstance(evidence, dict) or not evidence:
                problems.append(case_id + " has no recorded evidence")
                continue
            for key in sorted(required | set(case.get("required_evidence_fields", []))):
                if key not in evidence:
                    problems.append(case_id + " is missing evidence field " + key)
            if status not in ("passed", "pass"):
                continue
            pair = evidence.get("commit_pair")
            if not valid_source_pair(pair) or pair != reviewed_pair:
                problems.append(case_id + " has no exact matching source commit pair")
            hashes = evidence.get("artifact_hashes")
            expected_hashes = report.get("artifact_hashes")
            if digest_set(hashes) is None or digest_set(hashes) != digest_set(expected_hashes):
                problems.append(case_id + " has no exact matching artifact digest inventory")
            for key in ("environment", "command_or_harness", "log_paths", "observed_result"):
                if not evidence.get(key):
                    problems.append(case_id + " has empty execution evidence field " + key)
            if evidence.get("exit_code") is None:
                if not evidence.get("execution_records"):
                    problems.append(case_id + " has no individual execution records for its aggregate result")
            elif type(evidence["exit_code"]) is not int or evidence["exit_code"] != 0:
                problems.append(case_id + " is marked passed with an unsuccessful exit code")
            if field == "e2e_tests":
                execution_report = evidence.get("execution_report")
                if not isinstance(execution_report, dict):
                    problems.append(case_id + " passed without a dedicated execution_report")
                else:
                    execution_path = execution_report.get("path")
                    execution_digest = execution_report.get("sha256")
                    if not isinstance(execution_path, str) or not execution_path or not valid_sha256(execution_digest):
                        problems.append(case_id + " has an invalid execution_report path or digest")
                    elif report_path is not None:
                        path = _resolve_evidence_path(execution_path, report_path)
                        if path is None or not path.is_file() or sha(path) != execution_digest.lower():
                            problems.append(case_id + " execution_report is missing or has a changed digest")
                    execution_pair = evidence.get("execution_source_pair") or execution_report.get("source_pair")
                    if not valid_source_pair(execution_pair) or execution_pair != reviewed_pair:
                        problems.append(case_id + " execution source pair does not match the reviewed release pair")
                    if report_path is not None:
                        problems.extend(_execution_report_problems(
                            case, evidence, execution_report, report_path, reviewed_pair))
                problems.extend(_execution_step_problems(case, evidence, report_path))
    return problems


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo", type=Path, required=True)
    parser.add_argument("--toolbox-repo", type=Path)
    parser.add_argument("--artifact", type=Path, action="append", default=[])
    parser.add_argument("--artifact-inventory", type=Path,
                        help="Stable artifact-inventory.json binding candidate bytes to source and game hashes")
    parser.add_argument("--candidate-build-manifest", type=Path,
                        help="strict-candidate.json; defaults to the path recorded by --artifact-inventory")
    parser.add_argument("--acceptance-report", type=Path)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--require-release-ready", action="store_true")
    args = parser.parse_args()
    problems = artifact_inventory_problems(args.artifact)
    repos = {}
    for name, repo in [("ProjectRebound", args.repo), ("Toolbox", args.toolbox_repo)]:
        if repo is None:
            problems.append(name + " source repository missing")
            continue
        repo = repo.resolve(strict=True)
        repos[name] = source_tree_state(repo)
        if (repos[name]["git_exit_codes"] != [0, 0]
                or not valid_commit(repos[name]["commit"])
                or repos[name]["dirty"]):
            problems.append(name + " source provenance is not a clean committed tree")
    artifacts = []
    components = {}
    lockfiles = []
    if args.toolbox_repo:
        for relative in ("Cargo.lock", "src-tauri/Cargo.lock", "frontend/package-lock.json"):
            path = args.toolbox_repo / relative
            if not path.is_file():
                problems.append("dependency lockfile missing: " + relative)
                continue
            lockfiles.append({"path": str(path.resolve()), "sha256": sha(path)})
            if path.suffix == ".lock":
                packages = tomllib.loads(path.read_text(encoding="utf-8"))["package"]
                for package in packages:
                    if "source" not in package:
                        continue
                    name, version = package["name"], package["version"]
                    components[("cargo", name, version)] = {
                        "type": "library", "name": name, "version": version,
                        "purl": "pkg:cargo/" + name + "@" + version,
                    }
            else:
                packages = json.loads(path.read_text(encoding="utf-8"))["packages"]
                for location, package in packages.items():
                    if "node_modules/" not in location or "version" not in package:
                        continue
                    name = package.get("name", location.rsplit("node_modules/", 1)[1])
                    version = package["version"]
                    components[("npm", name, version)] = {
                        "type": "library", "name": name, "version": version,
                        "purl": "pkg:npm/" + name.replace("@", "%40") + "@" + version,
                    }
    for given in args.artifact:
        path = given.resolve(strict=True)
        build_info = run("go", "version", "-m", str(path))
        artifact = {"path": str(path), "size": path.stat().st_size, "sha256": sha(path), "go_build_info": build_info}
        kind = artifact_kind(path)
        artifact["kind"] = kind
        source_problems = []
        if kind in ("control-plane", "meta-server", "edge-relay"):
            source_problems, settings = embedded_go_source_problems(
                path, build_info, repos.get("ProjectRebound", {}).get("commit"))
            problems.extend(source_problems)
            artifact["embedded_source_provenance"] = settings
        release_state, reason = initial_artifact_release_state(path, source_problems)
        artifact["release_eligible"] = release_state
        artifact["reason"] = reason
        if normalized_artifact_name(path) == "matchserver":
            problems.append("historical matchserver artifact cannot be released")
        for line in build_info["output"].splitlines():
            parts = line.strip().split()
            if len(parts) >= 3 and parts[0] == "dep":
                name, version = parts[1:3]
                components[("golang", name, version)] = {"type": "library", "name": name, "version": version, "purl": "pkg:golang/" + name + "@" + version}
        artifacts.append(artifact)
    if not artifacts:
        problems.append("no release artifacts supplied")
    non_go_artifacts = [path for path in args.artifact
                        if artifact_kind(path) in ("payload.dll", "rebound_toolbox_tauri.exe")]
    candidate_binding = {"validated": False}
    if non_go_artifacts or args.artifact_inventory or args.candidate_build_manifest:
        binding_problems, candidate_binding = validate_candidate_binding(
            args.artifact_inventory, args.candidate_build_manifest, args.artifact, repos)
        problems.extend(binding_problems)
    elif any(artifact_kind(path) is None for path in args.artifact):
        problems.append("unknown artifact kind cannot receive candidate source binding")
    if non_go_artifacts and not candidate_binding.get("validated"):
        for path in non_go_artifacts:
            problems.append(normalized_artifact_name(path) + " lacks a validated candidate source binding")
    for artifact in artifacts:
        if artifact.get("kind") in ("payload.dll", "rebound_toolbox_tauri.exe"):
            artifact["candidate_source_binding"] = "PASS" if candidate_binding.get("validated") else "BLOCKED"
    report = None
    if args.acceptance_report:
        path = args.acceptance_report.resolve(strict=True)
        result = json.loads(path.read_text(encoding="utf-8-sig"))
        report = {"path": str(path), "sha256": sha(path)}
        problems.extend(acceptance_case_problems(result, path))
        expected_commits = result.get("reviewed_source_pair") or result.get("commit_pair", {})
        for name, repo in repos.items():
            if expected_commits.get(name) != repo["commit"]:
                problems.append(name + " acceptance commit does not match the checked out source")
        attested_artifacts = {item.get("sha256"): item for item in result.get("artifact_hashes", []) if isinstance(item, dict)}
        for artifact in artifacts:
            attestation = attested_artifacts.get(artifact["sha256"], {})
            if attestation.get("signature_status") != "PASS" or attestation.get("compatibility_status") != "PASS":
                problems.append(Path(artifact["path"]).name + " has no matching verified artifact/signature/compatibility evidence")
            elif artifact["release_eligible"] is not False and (
                    artifact.get("kind") not in ("payload.dll", "rebound_toolbox_tauri.exe")
                    or candidate_binding.get("validated")):
                artifact["release_eligible"] = True
                artifact["reason"] = "exact digest linked to supplied verified acceptance evidence"
        if result.get("release_ready") is not True:
            problems.append("acceptance report has not established release readiness")
    else:
        problems.append("native acceptance report missing")
    for artifact in artifacts:
        if artifact["release_eligible"] is not True:
            problems.append(Path(artifact["path"]).name + " is not an attested release artifact")
    output = args.output.resolve()
    output.parent.mkdir(parents=True, exist_ok=True)
    manifest = {"schema_version": 1, "generated_at": datetime.now(timezone.utc).isoformat(), "mode": "read_only_provenance", "repositories": repos,
                "artifacts": artifacts, "dependency_lockfiles": lockfiles,
                "sbom_scope": "Go artifact build metadata plus locked Cargo/npm dependencies; no container scan or license attestation",
                "acceptance_report": report, "candidate_build_binding": candidate_binding,
                "release_ready": not problems, "blockers": problems}
    output.write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    sbom = {"bomFormat": "CycloneDX", "specVersion": "1.5", "version": 1, "components": list(components.values())}
    output.with_suffix(".sbom.json").write_text(json.dumps(sbom, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(json.dumps({"output": str(output), "artifacts": len(artifacts), "release_ready": not problems, "blocker_count": len(problems)}))
    return 3 if args.require_release_ready and problems else 0


if __name__ == "__main__":
    raise SystemExit(main())
