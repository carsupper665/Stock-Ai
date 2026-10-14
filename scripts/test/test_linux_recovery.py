from __future__ import annotations

import base64
import contextlib
import importlib.util
import io
import json
import os
import sqlite3
import tempfile
import unittest
from datetime import datetime, timedelta, timezone
from pathlib import Path
from unittest import mock


MODULE_PATH = Path(__file__).resolve().parents[1] / "linux_recovery.py"
SPEC = importlib.util.spec_from_file_location("linux_recovery", MODULE_PATH)
assert SPEC and SPEC.loader
recovery = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(recovery)


class LinuxRecoveryTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory(prefix="linux-recovery-test-")
        self.root = Path(self.temporary.name)
        os.chmod(self.root, 0o700)
        self.key = self.root / "recovery.key"
        self.key.write_text(base64.b64encode(bytes(range(32))).decode() + "\n", encoding="ascii")
        os.chmod(self.key, 0o600)

    def tearDown(self) -> None:
        self.temporary.cleanup()

    def make_database(self, path: Path, table: str, rows: int = 1) -> None:
        path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
        connection = sqlite3.connect(path)
        try:
            connection.execute(f'CREATE TABLE "{table}" (id INTEGER PRIMARY KEY, marker TEXT)')
            for index in range(rows):
                connection.execute(
                    f'INSERT INTO "{table}" (marker) VALUES (?)', (f"fixture-{index}",)
                )
            connection.commit()
        finally:
            connection.close()
        os.chmod(path, 0o600)

    def make_contents(self) -> Path:
        contents = self.root / "contents"
        contents.mkdir(mode=0o700)
        for index, (service, filename) in enumerate(recovery.DATABASES.items(), start=1):
            self.make_database(contents / service / filename, f"table_{index}", index)
        history = contents / "agent-server" / "history"
        history.mkdir(mode=0o700)
        (history / "fixture.jsonl").write_text('{"fixture":"history"}\n', encoding="utf-8")
        config = contents / "config"
        config.mkdir(mode=0o700)
        (config / "compose.yaml").write_text("services: {}\n", encoding="utf-8")
        (config / "compose.env").write_text(
            "STOCK_AI_INSTANCE=stock-ai-source\n"
            "STOCK_AI_ENVIRONMENT=Test\n"
            "STOCK_AI_SECRET_NAMESPACE=stock-ai-test\n"
            f"STOCK_AI_SECRETS_DIR={self.root / 'source-secrets'}\n",
            encoding="utf-8",
        )
        secrets = contents / "secrets"
        secrets.mkdir(mode=0o700)
        for name in recovery.SECRETS:
            (secrets / name).write_text(f"private-{name}\n", encoding="utf-8")
        for path in contents.rglob("*"):
            os.chmod(path, 0o700 if path.is_dir() else 0o600)
        manifest = {
            "schema": recovery.SCHEMA,
            "created_at_utc": "2026-10-13T01:02:03+00:00",
            "environment": "Test",
            "stack": "stock-ai-source",
            "consistency": "compose_stopped",
            "uid_gid": "65532:65532",
            "volumes": list(recovery.VOLUMES.values()),
            "images": {service: f"example/{service}@sha256:{'1' * 64}" for service in recovery.SERVICES},
            "files": recovery.inventory(contents),
        }
        recovery.write_json_private(contents / "manifest.json", manifest)
        return contents

    def make_encrypted_backup(self) -> Path:
        contents = self.make_contents()
        archive = self.root / "archive.tar.gz"
        backup = self.root / "stock-ai-source-20261013T010203Z.sai-backup"
        recovery.build_archive(contents, archive)
        recovery.encrypt_file(archive, backup, recovery.recovery_key(self.key))
        recovery.write_checksum(backup)
        return backup

    def make_retention_backup(self, name: str, data: bytes | None = None) -> Path:
        path = self.root / name
        recovery._write_private(path, data or name.encode("ascii"))
        recovery.write_checksum(path)
        return path

    def marker(self, backup: Path, verified: datetime) -> Path:
        marker = self.root / "offhost-verification.json"
        recovery.write_json_private(
            marker,
            {
                "backup_file": backup.name,
                "sha256": recovery.sha256_file(backup),
                "verified_at_utc": verified.isoformat(),
            },
        )
        return marker

    def test_encrypt_verify_decrypt_and_sanitized_sqlite_evidence(self) -> None:
        backup = self.make_encrypted_backup()
        result = recovery.verify(backup, self.key)

        self.assertEqual(result["action"], "verify")
        self.assertTrue(result["encryption_authenticated"])
        self.assertEqual(result["secret_file_count"], 6)
        self.assertEqual(result["history_file_count"], 1)
        self.assertEqual(
            result["databases"]["agent-server"]["table_counts"]["table_3"], 3
        )
        encoded = json.dumps(result)
        self.assertNotIn("private-", encoded)
        self.assertNotIn("source-secrets", encoded)

    def test_checksum_and_gcm_each_reject_tampering(self) -> None:
        backup = self.make_encrypted_backup()
        data = bytearray(backup.read_bytes())
        data[len(recovery.MAGIC) + recovery.NONCE_BYTES + 1] ^= 1
        backup.write_bytes(data)
        os.chmod(backup, 0o600)
        with self.assertRaisesRegex(recovery.RecoveryError, "checksum"):
            recovery.verify(backup, self.key)

        recovery.checksum_path(backup).unlink()
        recovery.write_checksum(backup)
        with self.assertRaisesRegex(recovery.RecoveryError, "authentication"):
            recovery.verify(backup, self.key)

    def test_recovery_key_and_env_require_restrictive_permissions(self) -> None:
        os.chmod(self.key, 0o644)
        with self.assertRaisesRegex(recovery.RecoveryError, "group or other"):
            recovery.recovery_key(self.key)

        env = self.root / "compose.env"
        env.write_text("SECRET_VALUE=must-not-appear\n", encoding="utf-8")
        os.chmod(env, 0o644)
        with self.assertRaisesRegex(recovery.RecoveryError, "group or other") as failure:
            recovery.parse_env(env)
        self.assertNotIn("must-not-appear", str(failure.exception))

    def test_linux_secret_policy_is_65532_operator_group_and_0440(self) -> None:
        path = mock.MagicMock()
        path.is_symlink.return_value = False
        path.is_file.return_value = True
        path.stat.return_value = mock.Mock(
            st_uid=65532,
            st_gid=os.getgid(),
            st_mode=0o100440,
        )
        self.assertIs(recovery.require_linux_secret_file(path, "fixture"), path)

        path.stat.return_value = mock.Mock(
            st_uid=65532,
            st_gid=os.getgid(),
            st_mode=0o100444,
        )
        with self.assertRaisesRegex(recovery.RecoveryError, "0440"):
            recovery.require_linux_secret_file(path, "fixture")

    def test_prepare_secrets_targets_only_the_six_compose_files(self) -> None:
        secret_directory = self.root / "secrets"
        secret_directory.mkdir(mode=0o700)
        namespace = "stock-ai-test"
        expected = tuple(f"{namespace}_{name}" for name in recovery.SECRETS)
        for name in expected:
            (secret_directory / name).write_text("not-inspected\n", encoding="ascii")
            os.chmod(secret_directory / name, 0o600)
        env = self.root / "prepare.env"
        env.write_text(
            f"STOCK_AI_SECRET_NAMESPACE={namespace}\n"
            f"STOCK_AI_SECRETS_DIR={secret_directory}\n",
            encoding="utf-8",
        )
        os.chmod(env, 0o600)
        helper = "sha256:" + "1" * 64
        with (
            mock.patch.object(recovery, "validate_docker", return_value="linux fixture"),
            mock.patch.object(recovery, "_prepare_restored_secrets") as prepare,
        ):
            result = recovery.prepare_secrets(env, helper)
        prepare.assert_called_once_with(secret_directory, helper, expected)
        self.assertEqual(result["secret_file_count"], 6)

    def test_archive_rejects_links_and_traversal(self) -> None:
        archive = self.root / "unsafe.tar.gz"
        source = self.root / "source"
        source.mkdir()
        link = source / "link"
        link.symlink_to("/etc/passwd")
        with self.assertRaisesRegex(recovery.RecoveryError, "symbolic"):
            recovery.inventory(source)

        import tarfile

        payload = self.root / "payload"
        payload.write_text("x", encoding="ascii")
        with tarfile.open(archive, "w:gz") as tar:
            tar.add(payload, arcname="../escape")
        with self.assertRaisesRegex(recovery.RecoveryError, "unsafe"):
            recovery.extract_archive(archive, self.root / "expanded")

    def test_restore_refuses_existing_or_non_disposable_targets_before_writes(self) -> None:
        restore_directory = self.root / "restore"
        restore_directory.mkdir()
        with self.assertRaisesRegex(recovery.RecoveryError, "must not already exist"):
            recovery.restore(
                self.root / "missing.sai-backup",
                self.key,
                "stock-ai-restore-abcd",
                restore_directory,
            )
        restore_directory.rmdir()
        with self.assertRaisesRegex(recovery.RecoveryError, "disposable"):
            recovery.restore(
                self.root / "missing.sai-backup",
                self.key,
                "stock-ai-release",
                restore_directory,
            )

    def test_restore_refuses_any_preexisting_named_volume(self) -> None:
        backup = self.make_encrypted_backup()
        with (
            mock.patch.object(recovery, "validate_docker", return_value="linux fixture"),
            mock.patch.object(recovery, "_volume_exists", side_effect=[False, True]),
            mock.patch.object(recovery, "_create_volume") as create,
        ):
            with self.assertRaisesRegex(recovery.RecoveryError, "brand-new"):
                recovery.restore(
                    backup,
                    self.key,
                    "stock-ai-restore-abcd",
                    self.root / "restore",
                )
        create.assert_not_called()

    def test_restore_cleans_only_volumes_created_by_failed_attempt(self) -> None:
        backup = self.make_encrypted_backup()
        removed: list[str] = []

        def fail_second(_source: Path, volume: str, _helper: str) -> None:
            if volume.endswith("llm_state"):
                raise recovery.RecoveryError("fixture copy failure")

        with (
            mock.patch.object(recovery, "validate_docker", return_value="linux fixture"),
            mock.patch.object(recovery, "_volume_exists", return_value=False),
            mock.patch.object(recovery, "_create_volume"),
            mock.patch.object(recovery, "_restore_volume", side_effect=fail_second),
            mock.patch.object(recovery, "_remove_volumes", side_effect=lambda names: removed.extend(names)),
        ):
            with self.assertRaisesRegex(recovery.RecoveryError, "fixture copy"):
                recovery.restore(
                    backup,
                    self.key,
                    "stock-ai-restore-abcd",
                    self.root / "restore",
                )
        self.assertEqual(
            sorted(removed),
            sorted(
                [
                    "stock-ai-restore-abcd_backend_state",
                    "stock-ai-restore-abcd_llm_state",
                    "stock-ai-restore-abcd_agent_state",
                ]
            ),
        )
        self.assertFalse((self.root / "restore").exists())

    def test_restore_helper_must_be_immutable(self) -> None:
        with self.assertRaisesRegex(recovery.RecoveryError, "pinned"):
            recovery.restore(
                self.root / "missing.sai-backup",
                self.key,
                "stock-ai-restore-abcd",
                self.root / "restore",
                "nginx:latest",
            )

    def test_retention_preserves_daily_monthly_and_attested_backup(self) -> None:
        names = [
            "stock-ai-source-20261013T030000Z.sai-backup",
            "stock-ai-source-20261013T020000Z.sai-backup",
            "stock-ai-source-20261012T030000Z.sai-backup",
            "stock-ai-source-20260930T030000Z.sai-backup",
            "stock-ai-source-20260831T030000Z.sai-backup",
        ]
        backups = [self.make_retention_backup(name) for name in names]
        now = datetime(2026, 10, 13, 4, tzinfo=timezone.utc)
        marker = self.marker(backups[0], now - timedelta(hours=1))

        dry_run = recovery.prune(self.root, marker, 2, 2, dry_run=True, now=now)
        self.assertEqual(
            dry_run["removed"],
            [
                "stock-ai-source-20260831T030000Z.sai-backup",
                "stock-ai-source-20261013T020000Z.sai-backup",
            ],
        )
        self.assertTrue(all(path.exists() for path in backups))

        result = recovery.prune(self.root, marker, 2, 2, now=now)
        self.assertEqual(result["removed"], dry_run["removed"])
        self.assertFalse(backups[1].exists())
        self.assertFalse(recovery.checksum_path(backups[1]).exists())
        self.assertTrue(backups[0].exists())

    def test_retention_fails_closed_for_stale_marker_or_bad_artifact(self) -> None:
        backup = self.make_retention_backup(
            "stock-ai-source-20261013T030000Z.sai-backup"
        )
        now = datetime(2026, 10, 13, 4, tzinfo=timezone.utc)
        stale = self.marker(backup, now - timedelta(days=8))
        with self.assertRaisesRegex(recovery.RecoveryError, "stale"):
            recovery.prune(self.root, stale, 1, 1, now=now)

        stale.unlink()
        marker = self.marker(backup, now)
        other = self.make_retention_backup(
            "stock-ai-source-20260913T030000Z.sai-backup"
        )
        other.write_bytes(b"tampered")
        os.chmod(other, 0o600)
        with self.assertRaisesRegex(recovery.RecoveryError, "checksum"):
            recovery.prune(self.root, marker, 1, 1, now=now)
        self.assertTrue(other.exists())

    def test_cli_failure_is_nonzero_and_does_not_echo_secret_values(self) -> None:
        env = self.root / "compose.env"
        env.write_text("SECRET_VALUE=never-print-this\n", encoding="utf-8")
        os.chmod(env, 0o644)
        stderr = io.StringIO()
        with contextlib.redirect_stderr(stderr):
            code = recovery.main(
                [
                    "backup",
                    "--env-file",
                    str(env),
                    "--compose-file",
                    str(self.root / "compose.yaml"),
                    "--key-file",
                    str(self.key),
                ]
            )
        self.assertNotEqual(code, 0)
        self.assertNotIn("never-print-this", stderr.getvalue())

    def test_run_error_never_includes_subprocess_output(self) -> None:
        completed = mock.Mock(returncode=23, stdout="", stderr="secret=never-print")
        with mock.patch.object(recovery.subprocess, "run", return_value=completed):
            with self.assertRaises(recovery.RecoveryError) as failure:
                recovery.run(["fixture", "command", "argument"])
        self.assertNotIn("never-print", str(failure.exception))


if __name__ == "__main__":
    unittest.main()
