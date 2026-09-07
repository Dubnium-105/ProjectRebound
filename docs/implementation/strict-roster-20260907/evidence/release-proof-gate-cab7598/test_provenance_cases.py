"""Tests the acceptance manifest gate, not game or release acceptance."""
import copy
import json
import subprocess
import tempfile
import unittest
from pathlib import Path

from strict_roster_provenance import (
    acceptance_case_problems,
    artifact_inventory_problems,
    artifact_kind,
    embedded_go_source_problems,
    initial_artifact_release_state,
    sha,
    source_tree_state,
    validate_candidate_binding,
)


class AcceptanceCaseGateTests(unittest.TestCase):
    def report(self):
        pair = {"ProjectRebound": "a" * 40, "Toolbox": "b" * 40}
        artifacts = [{"sha256": str(index) * 64} for index in range(1, 6)]
        report = {
            field: [
                {"id": f"{prefix}{index:0{width}}", "status": "PASS",
                 "required_evidence_fields": ["command_or_harness", "exit_code"],
                 "result": {"command_or_harness": "synthetic gate fixture", "exit_code": 0,
                            "commit_pair": dict(pair), "artifact_hashes": copy.deepcopy(artifacts), "environment": "gate unit test",
                            "log_paths": ["synthetic-gate-fixture.log"], "observed_result": "synthetic manifest shape"}}
                for index in range(1, count + 1)
            ]
            for field, prefix, width, count in (
                ("component_tests", "AC-", 3, 52), ("e2e_tests", "E2E-", 2, 22))
        }
        report.update(commit_pair=pair, artifact_hashes=artifacts)
        for case in report["e2e_tests"]:
            case["steps"] = ["synthetic contract step one", "synthetic contract step two"]
            case["result"].update(
                execution_report={
                    "path": "synthetic-e2e-result.json",
                    "sha256": "c" * 64,
                    "source_pair": dict(pair),
                },
                execution_source_pair=dict(pair),
                execution_steps=[
                    {
                        "id": "synthetic-step-0",
                        "status": "PASS",
                        "contract_step_indices": [0],
                        "command": "synthetic e2e step one",
                        "exit_code": 0,
                        "log_paths": ["synthetic-step-0.log"],
                        "observed_result": "step one observed",
                    },
                    {
                        "id": "synthetic-step-1",
                        "status": "PASS",
                        "contract_step_indices": [1],
                        "command": "synthetic e2e step two",
                        "exit_code": 0,
                        "log_paths": ["synthetic-step-1.log"],
                        "observed_result": "step two observed",
                    },
                ],
            )
        return report

    def test_complete_synthetic_case_inventory(self):
        self.assertEqual(acceptance_case_problems(self.report()), [])

    def test_historical_pass_cannot_hide_nested_candidate_blocker(self):
        for field in ("component_tests", "e2e_tests"):
            with self.subTest(field=field):
                report = self.report()
                case = report[field][0]
                case["result"]["current_candidate_acceptance"] = "BLOCKED"
                self.assertIn(case["id"] + " is not accepted for the current candidate",
                              acceptance_case_problems(report))

    def test_same_count_cannot_hide_duplicate_or_unknown_case(self):
        for field in ("component_tests", "e2e_tests"):
            with self.subTest(field=field):
                report = self.report()
                report[field][-1] = copy.deepcopy(report[field][0])
                self.assertIn(field + " must contain every required case exactly once",
                              acceptance_case_problems(report))
                report[field][-1]["id"] = "UNKNOWN"
                self.assertIn(field + " must contain every required case exactly once",
                              acceptance_case_problems(report))

    def test_skip_or_not_run_never_counts_as_pass(self):
        for status in ("SKIP", "skipped", "NOT_RUN", "partial", "blocked", "fail"):
            with self.subTest(status=status):
                report = self.report()
                report["e2e_tests"][0]["status"] = status
                self.assertIn("E2E-01 is not passed", acceptance_case_problems(report))

    def test_pass_without_required_evidence_is_rejected(self):
        report = self.report()
        report["component_tests"][0]["required_evidence_fields"] = []
        del report["component_tests"][0]["result"]["exit_code"]
        self.assertIn("AC-001 is missing evidence field exit_code",
                      acceptance_case_problems(report))

    def test_pass_requires_matching_source_and_artifact_scope(self):
        for key, value in (("commit_pair", {}), ("artifact_hashes", []),
                           ("commit_pair", {"ProjectRebound": "c" * 40, "Toolbox": "b" * 40}),
                           ("artifact_hashes", [{"sha256": "f" * 64}])):
            with self.subTest(key=key, value=value):
                report = self.report()
                report["component_tests"][0]["result"][key] = value
                self.assertTrue(acceptance_case_problems(report))

    def test_pass_cannot_hide_failure_or_empty_execution(self):
        for key, value in (("exit_code", 1), ("exit_code", None), ("exit_code", False),
                           ("environment", ""), ("command_or_harness", None),
                           ("log_paths", []), ("observed_result", [])):
            with self.subTest(key=key, value=value):
                report = self.report()
                report["e2e_tests"][0]["result"][key] = value
                self.assertTrue(acceptance_case_problems(report))

    def test_aggregate_result_requires_individual_execution_records(self):
        report = self.report()
        result = report["component_tests"][0]["result"]
        result["exit_code"] = None
        result["execution_records"] = [{"command": "synthetic gate fixture", "exit_code": 0}]
        self.assertEqual(acceptance_case_problems(report), [])

    def test_e2e_pass_requires_step_mapping_and_current_execution_source(self):
        report = self.report()
        result = report["e2e_tests"][0]["result"]
        result.pop("execution_steps")
        errors = acceptance_case_problems(report)
        self.assertIn("E2E-01 passed without execution_steps", errors)

        report = self.report()
        report["e2e_tests"][0]["result"]["execution_source_pair"]["Toolbox"] = "c" * 40
        errors = acceptance_case_problems(report)
        self.assertIn("E2E-01 execution source pair does not match the reviewed release pair", errors)

    def test_e2e_step_without_evidence_cannot_pass(self):
        report = self.report()
        step = report["e2e_tests"][0]["result"]["execution_steps"][1]
        step.pop("log_paths")
        self.assertIn("E2E-01 execution step 1 has no log_paths", acceptance_case_problems(report))

    def test_e2e_execution_report_binds_real_source_result_and_steps(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            acceptance_path = root / "acceptance.json"
            (root / "synthetic-step-0.log").write_text("step 0\n", encoding="utf-8")
            (root / "synthetic-step-1.log").write_text("step 1\n", encoding="utf-8")
            report = self.report()
            pair = report["commit_pair"]
            def actual_for(case):
                evidence = case["result"]
                return {
                    "id": case["id"], "status": "PASS", "source_pair": dict(pair),
                    "result": {key: evidence[key] for key in (
                        "environment", "command_or_harness", "exit_code", "log_paths", "observed_result")},
                    "steps": [
                        {key: value for key, value in step.items()
                         if key not in ("contract_step_index", "contract_step_indices")}
                        for step in evidence["execution_steps"]
                    ],
                }

            # A report_path makes the gate verify every E2E execution report,
            # so give the other synthetic cases equally real fixture bytes.
            for other_case in report["e2e_tests"]:
                other_path = root / (other_case["id"] + ".json")
                other_path.write_text(json.dumps(actual_for(other_case)), encoding="utf-8")
                other_case["result"]["execution_report"] = {
                    "path": other_path.name, "sha256": sha(other_path), "source_pair": dict(pair)}

            case = report["e2e_tests"][0]
            evidence = case["result"]
            actual = actual_for(case)
            execution_path = root / "execution.json"

            def write_execution(value):
                execution_path.write_text(json.dumps(value), encoding="utf-8")
                evidence["execution_report"]["sha256"] = sha(execution_path)

            evidence["execution_report"]["path"] = execution_path.name
            write_execution(actual)
            self.assertEqual(acceptance_case_problems(report, acceptance_path), [])

            for field, value, expected in (
                ("source_pair", {"ProjectRebound": "a" * 40, "Toolbox": "c" * 40},
                 "E2E-01 execution_report source pair does not match the reviewed release pair"),
                ("status", "FAIL", "E2E-01 execution_report status is not passed"),
            ):
                with self.subTest(field=field):
                    changed = copy.deepcopy(actual)
                    changed[field] = value
                    write_execution(changed)
                    self.assertIn(expected, acceptance_case_problems(report, acceptance_path))

            changed = copy.deepcopy(actual)
            changed["steps"][0]["observed_result"] = "tampered step"
            write_execution(changed)
            self.assertIn("E2E-01 execution report steps differ from ledger steps",
                          acceptance_case_problems(report, acceptance_path))

            changed = copy.deepcopy(actual)
            changed["result"]["observed_result"] = "tampered result"
            write_execution(changed)
            self.assertIn("E2E-01 execution report result differs for observed_result",
                          acceptance_case_problems(report, acceptance_path))


class ArtifactInventoryGateTests(unittest.TestCase):
    complete = ["control-plane", "meta-server", "edge-relay", "Payload.dll",
                "rebound_toolbox_tauri.exe"]

    def test_all_five_artifacts_are_required(self):
        self.assertEqual(artifact_inventory_problems(self.complete), [])
        for missing in range(len(self.complete)):
            with self.subTest(missing=self.complete[missing]):
                self.assertTrue(artifact_inventory_problems(
                    self.complete[:missing] + self.complete[missing + 1:]))

    def test_duplicate_cannot_substitute_for_missing_artifact(self):
        self.assertTrue(artifact_inventory_problems(
            self.complete[:-1] + [self.complete[0]]))

    def test_legacy_or_unknown_binary_cannot_enter_the_release_set(self):
        for path in ("matchserver", "rebound_toolbox.exe", "unknown.exe"):
            with self.subTest(path=path):
                self.assertTrue(artifact_inventory_problems(self.complete + [path]))
                self.assertTrue(artifact_inventory_problems(self.complete[:-1] + [path]))

    def test_case_insensitive_go_role_still_requires_vcs_stamp(self):
        self.assertEqual(artifact_kind("CONTROL-PLANE"), "control-plane")
        problems, _ = embedded_go_source_problems(
            Path("CONTROL-PLANE"),
            {"exit_code": 0, "output": "build vcs.revision=" + "a" * 40 + "\nbuild vcs.modified=false"},
            "b" * 40,
        )
        self.assertIn("CONTROL-PLANE does not embed the reviewed source revision", problems)

    def test_unproven_non_go_artifacts_are_blocked_without_inventory(self):
        problems, binding = validate_candidate_binding(
            None,
            None,
            [Path("Payload.dll"), Path("rebound_toolbox_tauri.exe")],
            {"ProjectRebound": {"commit": "a" * 40}, "Toolbox": {"commit": "b" * 40}},
        )
        self.assertFalse(binding["validated"])
        self.assertIn("non-Go artifacts require --artifact-inventory", problems)

    def test_untracked_source_is_dirty(self):
        with tempfile.TemporaryDirectory() as directory:
            repo = Path(directory)
            subprocess.run(["git", "init", "-q"], cwd=repo, check=True)
            (repo / "tracked.txt").write_text("tracked\n", encoding="utf-8")
            subprocess.run(["git", "add", "tracked.txt"], cwd=repo, check=True)
            subprocess.run(
                ["git", "-c", "user.name=provenance-test", "-c", "user.email=provenance-test@example.invalid",
                 "commit", "-qm", "initial"],
                cwd=repo,
                check=True,
            )
            self.assertFalse(source_tree_state(repo)["dirty"])
            (repo / "untracked-source.rs").write_text("fn main() {}\n", encoding="utf-8")
            state = source_tree_state(repo)
            self.assertTrue(state["dirty"])
            self.assertIn("?? untracked-source.rs", state["status_output"])

    def test_candidate_inventory_binds_all_supplied_artifact_bytes(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            project = root / "project"
            toolbox = root / "toolbox"
            project.mkdir()
            toolbox.mkdir()
            (project / "Payload").mkdir()
            (project / "Payload" / "source.cpp").write_text("strict payload\n", encoding="utf-8")
            (toolbox / "Cargo.lock").write_text("# lock\n", encoding="utf-8")

            def commit(repo, path, message):
                subprocess.run(["git", "init", "-q"], cwd=repo, check=True)
                subprocess.run(["git", "add", str(path)], cwd=repo, check=True)
                subprocess.run(
                    ["git", "-c", "user.name=provenance-test", "-c",
                     "user.email=provenance-test@example.invalid", "commit", "-qm", message],
                    cwd=repo, check=True)

            commit(project, Path("Payload"), "payload")
            commit(toolbox, Path("Cargo.lock"), "toolbox")
            repositories = {"ProjectRebound": source_tree_state(project),
                            "Toolbox": source_tree_state(toolbox)}
            artifacts = []
            for name, data in (
                ("control-plane", b"control"), ("meta-server", b"meta"),
                ("edge-relay", b"edge"), ("Payload.dll", b"payload"),
                ("rebound_toolbox_tauri.exe", b"tauri")):
                path = root / name
                path.write_bytes(data)
                artifacts.append(path)
            game = root / "Boundary.exe"
            game.write_bytes(b"pinned game")
            candidate = root / "candidate.json"
            candidate_data = {
                "source_commit": repositories["Toolbox"]["commit"],
                "source_dirty": False,
                "payload_path": str(artifacts[3]), "payload_sha256": sha(artifacts[3]),
                "toolbox_path": str(artifacts[4]), "toolbox_sha256": sha(artifacts[4]),
                "game_sha256": sha(game),
            }
            candidate.write_text(json.dumps(candidate_data), encoding="utf-8")
            inventory = root / "artifact-inventory.json"
            inventory_data = {
                "schema_version": 1, "generation_id": "test-generation",
                "source_commits": {name: value["commit"] for name, value in repositories.items()},
                "artifacts": [],
                "candidate_build_manifest": {"path": str(candidate), "sha256": sha(candidate)},
                "pinned_game_reference": {"path": str(game), "sha256": sha(game)},
            }
            for path in artifacts:
                kind = artifact_kind(path)
                owner = "Toolbox" if kind == "rebound_toolbox_tauri.exe" else "ProjectRebound"
                entry = {"role": kind, "path": str(path), "sha256": sha(path),
                         "bytes": path.stat().st_size, "source_repository": owner,
                         "source_pair_commit": repositories[owner]["commit"]}
                if kind == "payload.dll":
                    tree = subprocess.run(["git", "rev-parse", "HEAD:Payload"], cwd=project,
                                          text=True, capture_output=True, check=True).stdout.strip()
                    entry.update(build_source_commit=repositories["ProjectRebound"]["commit"],
                                 verified_source_subtree={"path": "Payload", "git_tree": tree,
                                                          "matches_reviewed_source_pair": True})
                inventory_data["artifacts"].append(entry)
            inventory.write_text(json.dumps(inventory_data), encoding="utf-8")

            self.assertEqual(validate_candidate_binding(inventory, candidate, artifacts, repositories)[0], [])
            for path in artifacts:
                with self.subTest(artifact=path.name):
                    original = path.read_bytes()
                    path.write_bytes(original + b"tampered")
                    problems, _ = validate_candidate_binding(inventory, candidate, artifacts, repositories)
                    self.assertIn(artifact_kind(path) + " supplied artifact sha256 does not match inventory", problems)
                    path.write_bytes(original)

    def test_go_source_failure_cannot_become_release_eligible(self):
        state, reason = initial_artifact_release_state(Path("CONTROL-PLANE"), ["bad source stamp"])
        self.assertFalse(state)
        self.assertIn("provenance", reason)


if __name__ == "__main__":
    unittest.main(verbosity=2)
