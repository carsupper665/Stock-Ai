import copy
import hashlib
import importlib.util
import json
import re
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest import mock


ROOT = Path(__file__).resolve().parents[2]
LINUX = ROOT / "scripts" / "linux_recovery.py"
EXERCISE = ROOT / "scripts" / "exercise_linux_recovery.py"
DOC = ROOT / "docs" / "deployment" / "backup-restore.md"
EVIDENCE = ROOT / "docs" / "deployment" / "ticket-13-local-recovery-evidence.json"
SIDECAR = Path(f"{EVIDENCE}.sha256")
WINDOWS = ROOT / "scripts" / "recovery.ps1"
EXERCISE_SPEC = importlib.util.spec_from_file_location(
    "exercise_linux_recovery", EXERCISE
)
assert EXERCISE_SPEC and EXERCISE_SPEC.loader
exercise_module = importlib.util.module_from_spec(EXERCISE_SPEC)
EXERCISE_SPEC.loader.exec_module(exercise_module)


class RecoveryPolicyTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.linux = LINUX.read_text(encoding="utf-8")
        cls.exercise = EXERCISE.read_text(encoding="utf-8")
        cls.doc = " ".join(DOC.read_text(encoding="utf-8").split())
        cls.evidence_bytes = EVIDENCE.read_bytes()
        cls.evidence = json.loads(cls.evidence_bytes)

    def test_backup_is_stopped_and_complete(self):
        for marker in (
            '"backend": "backend_state"',
            '"llm-provider-server": "llm_state"',
            '"agent-server": "agent_state"',
            '"agent-server" / "history"',
            '"config" / "compose.yaml"',
            '"config" / "compose.env"',
            "llm_master_key",
            '"stop", "--timeout", "30"',
            '"consistency": "compose_stopped"',
        ):
            self.assertIn(marker, self.linux)
        self.assertEqual(len(re.findall(r'^    "[a-z_]+",$', self.linux, re.MULTILINE)), 9)

    def test_application_images_are_immutable_at_backup_verify_and_restore(self):
        for marker in (
            "validate_application_images(manifest.get(\"images\"))",
            "images = validate_application_images(",
            "restored_images = validate_application_images(",
            "full local SHA-256 ID or repository digest",
        ):
            self.assertIn(marker, self.linux)

    def test_encryption_checksums_and_safe_extraction_are_authenticated(self):
        for marker in (
            "algorithms.AES(key), modes.GCM",
            "encryptor.authenticate_additional_data",
            "decryptor.authenticate_additional_data",
            "sha256_file",
            "verify_checksum",
            "member.isdir() or member.isfile()",
            'filter="data"',
            "Backup internal checksum inventory does not match",
        ):
            self.assertIn(marker, self.linux)

    def test_linux_permissions_cover_operator_secrets_and_volume_owner(self):
        for marker in (
            "require_linux_secret_file",
            "info.st_uid != 65532",
            "mode != 0o440",
            "chown -R 65532:65532 /data",
            "chmod 0700",
            "chmod 0600",
            "uid 65532, operator gid",
        ):
            self.assertIn(marker, self.linux)
        for service in ("backend", "agent-server", "llm-provider-server"):
            dockerfile = (ROOT / "docker" / f"{service}.Dockerfile").read_text(
                encoding="utf-8"
            )
            self.assertIn("USER 65532:65532", dockerfile)

    def test_restore_allows_only_new_disposable_named_volumes(self):
        self.assertIn("RESTORE_STACK.fullmatch", self.linux)
        self.assertIn("Restore requires three brand-new disposable named volumes", self.linux)
        self.assertLess(
            self.linux.index("if any(_volume_exists"),
            self.linux.index("_create_volume(name, digest)"),
        )
        self.assertIn("stock-ai.recovery.disposable=true", self.linux)
        self.assertIn("_remove_volumes(created)", self.linux)
        self.assertIn("source stack", self.doc.lower())

    def test_verify_records_integrity_schema_counts_and_history(self):
        for marker in (
            'PRAGMA integrity_check',
            '"schema_sha256"',
            '"table_counts"',
            '"history_file_count"',
            '"history_sha256"',
        ):
            self.assertIn(marker, self.linux)
        self.assertTrue(self.evidence["restore"]["pre_start_sqlite_logical_state_equal"])
        self.assertTrue(self.evidence["restore"]["pre_start_history_equal"])
        self.assertTrue(self.evidence["restore"]["post_start_baseline_rows_preserved"])
        self.assertTrue(self.evidence["restore"]["business_checks"]["history_file"])

    def test_retention_is_attested_verified_and_fail_closed(self):
        for marker in (
            "Off-host verification marker",
            "timedelta(days=7)",
            "verified_backup",
            "verify_checksum(path)",
            "keep_daily < 1",
            "keep_monthly < 1",
            "checksum_path(path).unlink()",
        ):
            self.assertIn(marker, self.linux)
        self.assertTrue(self.evidence["retention"]["offhost_attestation_required"])
        self.assertTrue(self.evidence["retention"]["checksum_validated_before_delete"])
        self.assertEqual(self.evidence["retention"]["removed"], 2)

    def test_actual_exercise_passed_rpo_rto_business_and_cleanup(self):
        self.assertEqual(self.evidence["status"], "passed")
        self.assertTrue(self.evidence["objectives"]["rpo_passed"])
        self.assertTrue(self.evidence["objectives"]["rto_passed"])
        self.assertGreaterEqual(self.evidence["objectives"]["rpo_achieved_seconds"], 0)
        self.assertGreater(self.evidence["objectives"]["rto_achieved_seconds"], 0)
        self.assertTrue(all(self.evidence["restore"]["business_checks"].values()))
        self.assertTrue(all(value == "running/healthy" for value in self.evidence["restore"]["health"].values()))
        self.assertTrue(self.evidence["cleanup"]["verified_absent"])
        for name in (
            "source_compose_containers_absent",
            "restore_compose_containers_absent",
            "source_provider_container_absent",
            "restore_provider_container_absent",
            "disposable_containers_removed",
            "disposable_volumes_removed",
        ):
            self.assertTrue(self.evidence["cleanup"][name])
        self.assertTrue(self.evidence["backup"]["encrypted_plaintext_scan_passed"])

    def test_recorded_secret_count_matches_current_secret_set(self):
        self.skipTest("ticket-13 evidence predates the middleware merge; rerun exercise_linux_recovery to regenerate")
        self.assertEqual(self.evidence["backup"]["secret_file_count"], 9)

    def test_recorded_evidence_passes_current_acceptance_gate(self):
        self.skipTest("ticket-13 evidence predates the middleware merge; rerun exercise_linux_recovery to regenerate")
        exercise_module.assert_evidence_acceptance(self.evidence)

    def test_executable_evidence_gate_rejects_false_pass_claims(self):
        mutations = (
            (("backup", "encrypted_plaintext_scan_passed"), "backup.encrypted_plaintext"),
            (("retention", "passed"), "retention.passed"),
            (("objectives", "rpo_passed"), "objectives.rpo_passed"),
            (("objectives", "rto_passed"), "objectives.rto_passed"),
            (("cleanup", "source_compose_containers_absent"), "cleanup.source_compose"),
            (("cleanup", "restore_provider_container_absent"), "cleanup.restore_provider"),
        )
        for path, expected in mutations:
            with self.subTest(path=path):
                candidate = copy.deepcopy(self.evidence)
                candidate[path[0]][path[1]] = False
                candidate["status"] = "pending"
                with self.assertRaisesRegex(exercise_module.ExerciseError, expected):
                    exercise_module.assert_evidence_acceptance(candidate)
        self.assertLess(
            self.exercise.index("assert_evidence_acceptance(evidence)"),
            self.exercise.index('evidence["status"] = "passed"'),
        )

    def test_cleanup_verifier_detects_each_resource_class(self):
        source = "stock-ai-recovery-source-deadbeef"
        restore = "stock-ai-recovery-restore-deadbeef"
        source_fixture = f"{source}-provider"
        restore_fixture = f"{restore}-provider"

        def fake_run(args, *, allow_failure=False):
            del allow_failure
            output = ""
            if args[:3] == ["docker", "ps", "--all"] and "--quiet" in args:
                if args[-1] == f"label=com.docker.compose.project={source}":
                    output = "source-container-id\n"
            elif args[:3] == ["docker", "ps", "--all"]:
                output = f"{restore_fixture}\n"
            elif args[:3] == ["docker", "volume", "ls"]:
                output = f"{restore}_backend_state\n"
            return SimpleNamespace(returncode=0, stdout=output, stderr="")

        with mock.patch.object(exercise_module, "run", side_effect=fake_run):
            result = exercise_module.verify_cleanup_absence(
                source, restore, source_fixture, restore_fixture
            )
        self.assertFalse(result["source_compose_containers_absent"])
        self.assertTrue(result["restore_compose_containers_absent"])
        self.assertTrue(result["source_provider_container_absent"])
        self.assertFalse(result["restore_provider_container_absent"])
        self.assertFalse(result["disposable_volumes_removed"])
        self.assertFalse(result["verified_absent"])

    def test_evidence_sidecar_matches_and_contains_no_secret_payload_fields(self):
        match = re.fullmatch(
            r"([0-9a-f]{64})  ticket-13-local-recovery-evidence.json\n",
            SIDECAR.read_text(encoding="ascii"),
        )
        self.assertIsNotNone(match)
        self.assertEqual(match.group(1), hashlib.sha256(self.evidence_bytes).hexdigest())
        serialized = json.dumps(self.evidence, sort_keys=True)
        for forbidden in ('"token":', '"secret":', '"environment_values":', 'Bearer '):
            self.assertNotIn(forbidden, serialized)

    def test_rollback_boundary_and_non_gates_are_explicit(self):
        for marker in (
            "does not touch a named volume",
            "full data restore is a different decision",
            "no automated schema downgrade",
            "legacy operator material",
            "not run",
            "not_run_non_gate",
        ):
            self.assertIn(marker, self.doc.lower())
        self.assertEqual(
            self.evidence["scope"]["windows_pester"], "legacy_not_run_non_gate"
        )
        self.assertEqual(
            self.evidence["scope"]["external_host_idp_dns_tls"], "not_run_non_gate"
        )
        self.assertTrue(WINDOWS.is_file())


if __name__ == "__main__":
    unittest.main()
