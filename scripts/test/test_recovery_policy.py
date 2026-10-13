import json
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "scripts" / "recovery.ps1"
DOC = ROOT / "docs" / "deployment" / "backup-restore.md"
EVIDENCE = ROOT / "docs" / "deployment" / "ticket-13-exercise-blocked.json"


class RecoveryPolicyTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.script = SCRIPT.read_text(encoding="utf-8")
        cls.doc = " ".join(DOC.read_text(encoding="utf-8").split())

    def test_backup_stops_stack_and_captures_all_data_and_secrets(self):
        for marker in (
            "backend_state",
            "llm_state",
            "agent_state",
            "agent-server/history",
            "llm_master_key",
            "config/compose.yaml",
            "config/compose.env",
            "Invoke-Compose @('stop', '--timeout', '30')",
            "Invoke-Docker @('cp'",
        ):
            self.assertIn(marker, self.script)
        self.assertIn("Invoke-Compose @('up', '-d', '--wait'", self.script)

    def test_encryption_integrity_and_restricted_retention_are_explicit(self):
        for marker in (
            "AesGcm",
            "OaepSHA256",
            "SHA256",
            "certificate_thumbprint",
            "InstallSchedule",
            "Register-ScheduledTask",
            "KeepDaily",
            "KeepMonthly",
            "recovery-{0}.log",
            "OffHostVerificationMarker",
            "AddDays(-90)",
            "FAIL action=$Action",
        ):
            self.assertIn(marker, self.script)
        self.assertIn("docker-users", self.doc)
        self.assertIn("Task Scheduler Last Run Result", self.doc)

    def test_restore_requires_empty_volumes_and_preserves_data_boundary(self):
        self.assertIn("Test-EmptyVolumes", self.script)
        self.assertIn("refusing to overwrite user data", self.script)
        self.assertIn("llm-provider-server/llm-provider.db", self.script)
        self.assertIn("matching LLM master key", self.script)
        self.assertIn("do not change any named volume", self.doc)
        self.assertIn("automated schema", self.doc)

    def test_exercise_record_does_not_claim_a_recovery_run(self):
        record = json.loads(EVIDENCE.read_text(encoding="utf-8"))
        self.assertEqual(record["status"], "not-run")
        self.assertIsNone(record["measured_rpo_hours"])
        self.assertIsNone(record["measured_rto_minutes"])


if __name__ == "__main__":
    unittest.main()
