"""Tests the acceptance manifest gate, not game or release acceptance."""
import copy
import unittest

from strict_roster_provenance import acceptance_case_problems, artifact_inventory_problems


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
        return report

    def test_complete_synthetic_case_inventory(self):
        self.assertEqual(acceptance_case_problems(self.report()), [])

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


if __name__ == "__main__":
    unittest.main(verbosity=2)
