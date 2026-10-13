import json
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "scripts" / "recovery.ps1"
POLICY = ROOT / "scripts" / "recovery-policy.psm1"
PESTER = ROOT / "scripts" / "test" / "RecoveryPolicy.Tests.ps1"
DOC = ROOT / "docs" / "deployment" / "backup-restore.md"
EVIDENCE = ROOT / "docs" / "deployment" / "ticket-13-exercise-blocked.json"


class RecoveryPolicyTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.script = SCRIPT.read_text(encoding="utf-8")
        cls.policy = POLICY.read_text(encoding="utf-8")
        cls.pester = PESTER.read_text(encoding="utf-8")
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
        self.assertIn("Assert-RecoveryVolumeScaffolding", self.script)
        self.assertIn("refusing to overwrite user data", self.policy)
        self.assertIn("llm-provider-server/llm-provider.db", self.script)
        self.assertIn("matching LLM master key", self.script)
        self.assertIn("do not change any named volume", self.doc)
        self.assertIn("automated schema", self.doc)

    def test_restore_uses_fixture_policy_for_scaffolding_acl_and_ownership(self):
        self.assertIn("Assert-RecoveryVolumeScaffolding -Service $service", self.script)
        self.assertIn("Invoke-RecoveryVolumeOwnership -VolumeName", self.script)
        self.assertLess(
            self.script.index("Set-RecoveryDirectoryAcl -Path $restoredSecrets"),
            self.script.index('Copy-Item -LiteralPath (Join-Path $expanded "secrets/$secret")'),
        )
        for behavior in (
            "allows only the image-owned empty backend logs directory",
            "rejects files in an otherwise allowed logs scaffold",
            "rejects database files, history files, and unknown directories",
            "sets and verifies the protected recovery directory DACL before use",
            "rejects an unexpected principal in the recovery directory DACL",
            "chowns every data volume to 65532 and verifies all entries read-only",
            "fails closed when chown or ownership verification fails",
        ):
            self.assertIn(behavior, self.pester)
        self.assertIn("chown -R 65532:65532 /data", self.policy)
        self.assertIn("Assert-RecoveryDirectoryAcl -Path $fullPath", self.policy)

    def test_recovery_uid_matches_each_persisted_service_image(self):
        helper = self.policy.split(" = '", 1)[1].split("'", 1)[0]
        for service in ("backend", "agent-server", "llm-provider-server"):
            dockerfile = ROOT / "docker" / f"{service}.Dockerfile"
            dockerfile_text = dockerfile.read_text(encoding="utf-8")
            self.assertIn("USER 65532:65532", dockerfile_text)
            self.assertIn(f"FROM {helper} AS build", dockerfile_text)

    def test_exercise_record_does_not_claim_a_recovery_run(self):
        record = json.loads(EVIDENCE.read_text(encoding="utf-8"))
        self.assertEqual(record["status"], "not-run")
        self.assertIsNone(record["measured_rpo_hours"])
        self.assertIsNone(record["measured_rto_minutes"])


if __name__ == "__main__":
    unittest.main()
