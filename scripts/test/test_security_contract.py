import csv
import importlib.util
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location(
    "security_contract", ROOT / "scripts" / "validate_security_contract.py"
)
MODULE = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(MODULE)


class SecurityContractTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.text = MODULE.CONTRACT.read_text(encoding="utf-8")
        with MODULE.MATRIX.open(newline="", encoding="utf-8") as handle:
            cls.rows = list(csv.DictReader(handle))

    def test_repository_contract_is_valid(self):
        self.assertEqual([], MODULE.validate_contract(self.text))
        self.assertEqual([], MODULE.validate_matrix(self.rows))

    def test_anonymous_private_service_access_is_rejected(self):
        rows = [dict(row) for row in self.rows]
        target = next(row for row in rows if row["case_id"] == "AGENT-002")
        target.update(edge_action="allow", upstream="agent", upstream_credential="agent_admin")
        errors = MODULE.validate_matrix(rows)
        self.assertTrue(any("anonymous private-service" in error for error in errors), errors)

    def test_account_cannot_become_agent_admin(self):
        rows = [dict(row) for row in self.rows]
        target = next(row for row in rows if row["case_id"] == "AGENT-003")
        target.update(edge_action="allow", upstream="agent", upstream_credential="agent_admin")
        errors = MODULE.validate_matrix(rows)
        self.assertTrue(any("Account token unlocks" in error for error in errors), errors)

    def test_wrong_llm_credential_role_is_rejected(self):
        rows = [dict(row) for row in self.rows]
        target = next(row for row in rows if row["case_id"] == "LLM-002")
        target["upstream_credential"] = "llm_admin"
        errors = MODULE.validate_matrix(rows)
        self.assertTrue(any("wrong credential role" in error for error in errors), errors)

    def test_denial_cannot_contact_upstream(self):
        rows = [dict(row) for row in self.rows]
        target = next(row for row in rows if row["case_id"] == "HEADER-001")
        target.update(upstream="console", upstream_credential="none")
        errors = MODULE.validate_matrix(rows)
        self.assertTrue(any("denied request reaches" in error for error in errors), errors)

    def test_invalid_expected_status_is_rejected(self):
        rows = [dict(row) for row in self.rows]
        rows[0]["expected_status"] = "allowed"
        errors = MODULE.validate_matrix(rows)
        self.assertTrue(any("invalid expected_status" in error for error in errors), errors)

    def test_incomplete_risk_row_is_rejected(self):
        broken = self.text.replace(
            "| R-001 | Critical | Closed |", "| R-001 | Critical | Closed | |", 1
        )
        errors = MODULE.validate_contract(broken)
        self.assertTrue(any("malformed/incomplete risk row" in error for error in errors), errors)

    def test_test_release_values_are_explicitly_distinct(self):
        required_pairs = (
            ("stock-ai-test", "stock-ai-release"),
            ("stock-ai-api-test", "stock-ai-api-release"),
            ("__Host-stock-ai-test-session", "__Host-stock-ai-release-session"),
            ("${TEST_APP_HOST}", "${RELEASE_APP_HOST}"),
        )
        for test_value, release_value in required_pairs:
            with self.subTest(test_value=test_value):
                self.assertNotEqual(test_value, release_value)
                self.assertIn(test_value, self.text)
                self.assertIn(release_value, self.text)


if __name__ == "__main__":
    unittest.main()
