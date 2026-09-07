"""Tests the acceptance manifest gate, not game or release acceptance."""
import copy
import unittest

from strict_roster_provenance import acceptance_case_problems


class AcceptanceCaseGateTests(unittest.TestCase):
    def report(self):
        return {
            field: [
                {"id": f"{prefix}{index:0{width}}", "status": "PASS",
                 "required_evidence_fields": ["command_or_harness", "exit_code"],
                 "result": {"command_or_harness": "synthetic gate fixture", "exit_code": 0,
                            "commit_pair": {}, "artifact_hashes": [], "environment": "gate unit test",
                            "log_paths": [], "observed_result": "synthetic manifest shape"}}
                for index in range(1, count + 1)
            ]
            for field, prefix, width, count in (
                ("component_tests", "AC-", 3, 52), ("e2e_tests", "E2E-", 2, 22))
        }

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


if __name__ == "__main__":
    unittest.main(verbosity=2)
