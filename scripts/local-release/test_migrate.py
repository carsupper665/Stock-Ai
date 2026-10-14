import base64
import json
import sqlite3
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import patch

import migrate
from cryptography.hazmat.primitives.ciphers.aead import AESGCM
from scripts.local_operations import WorkflowError


class MigrationSafetyTest(unittest.TestCase):
    def test_equal_counts_cannot_hide_changed_ids_or_records(self):
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "fixture.db"
            db = sqlite3.connect(path)
            db.execute("CREATE TABLE records (id TEXT PRIMARY KEY, secret BLOB)")
            db.execute("INSERT INTO records VALUES ('original', ?)", (b"private-credential",))
            db.commit()
            original = migrate.database_facts(path)
            db.execute("UPDATE records SET id='replacement'")
            db.commit()
            changed = migrate.database_facts(path)
            self.assertEqual(original["table_counts"], changed["table_counts"])
            self.assertNotEqual(original["tables"]["records"]["identifiers_sha256"],
                                changed["tables"]["records"]["identifiers_sha256"])
            self.assertNotEqual(original, changed)
            self.assertNotIn("private-credential", json.dumps(original))
            self.assertNotIn("original", json.dumps(original))
            db.close()

    def test_foreign_key_violation_blocks_migration(self):
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "fixture.db"
            db = sqlite3.connect(path)
            db.executescript("CREATE TABLE parent(id INTEGER PRIMARY KEY);"
                             "CREATE TABLE child(parent INTEGER REFERENCES parent(id));"
                             "INSERT INTO child VALUES(42);")
            db.close()
            with self.assertRaises(WorkflowError):
                migrate.database_facts(path)

    def test_matching_master_key_authenticates_ciphertext_and_wrong_key_fails(self):
        with tempfile.TemporaryDirectory() as temporary:
            stage = Path(temporary)
            (stage / "keys").mkdir()
            (stage / "llm-provider-server").mkdir()
            key = b"k" * 32
            nonce = b"n" * 12
            ciphertext = nonce + AESGCM(key).encrypt(nonce, b"private-fixture-key", b"provider\0token")
            db = sqlite3.connect(stage / "llm-provider-server/llm-provider.db")
            db.execute("CREATE TABLE provider_tokens(id TEXT, provider_id TEXT, secret_encrypted TEXT)")
            db.execute("INSERT INTO provider_tokens VALUES ('token', 'provider', ?)",
                       (base64.b64encode(ciphertext).decode(),))
            db.commit()
            db.close()
            key_file = stage / "keys/llm_master_key"
            key_file.write_bytes(base64.b64encode(key))
            self.assertEqual(migrate.decrypt_credentials(stage), 1)
            key_file.write_bytes(base64.b64encode(b"x" * 32))
            with self.assertRaises(Exception):
                migrate.decrypt_credentials(stage)

    def test_source_export_failure_unpauses_without_restart(self):
        args = SimpleNamespace(source_project="test-project", source_secrets=Path("unused"))
        entries = [{"Id": service, "Config": {"Labels": {"com.docker.compose.service": service}},
                    "State": {"Running": True, "Paused": False},
                    "Mounts": [{"Destination": "/data", "Type": "volume", "Name": service}]}
                   for service in migrate.recovery.VOLUMES]
        calls = []
        def command(command):
            calls.append(command)
            return SimpleNamespace(stdout=json.dumps(entries) if command[1] == "inspect" else "one two three")
        with tempfile.TemporaryDirectory() as temporary, patch("migrate.run", side_effect=command), \
                patch("migrate.copy_volumes", side_effect=WorkflowError("export failed")):
            with self.assertRaises(WorkflowError):
                migrate.source_export(args, "immutable-helper", Path(temporary))
        self.assertEqual([call[2] for call in calls if call[1] == "unpause"],
                         list(reversed(list(migrate.recovery.VOLUMES))))
        self.assertFalse(any("restart" in call or "compose" in call for call in calls))

    def test_authenticated_archive_inventory_tamper_rejected(self):
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            source = base / "source"
            source.mkdir()
            (source / "record").write_text("fixture")
            (source / "manifest.json").write_text(json.dumps({"schema": "fixture", "files": {"record": "wrong"}}))
            archive = base / "plain.tar.gz"
            migrate.recovery.build_archive(source, archive)
            key_file = base / "key"
            key_file.write_bytes(base64.b64encode(b"k" * 32))
            key_file.chmod(0o600)
            backup = base / "encrypted.sai-backup"
            migrate.recovery.encrypt_file(archive, backup, b"k" * 32)
            migrate.recovery.write_checksum(backup)
            destination = base / "verify"
            destination.mkdir()
            with self.assertRaises(WorkflowError):
                migrate.verified_archive(backup, key_file, destination, "fixture")

    def test_key_replacement_preserves_inode_mode_and_restores_original_bytes(self):
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            stage = base / "stage"
            (stage / "keys").mkdir(parents=True)
            (stage / "secrets").mkdir()
            runtime = base / "runtime"
            (runtime / "secrets").mkdir(parents=True)
            operator = SimpleNamespace(env_file=runtime / "compose.env")
            config = {"services": {"nginx": {"image": "immutable-helper"}}, "volumes": {
                volume: {"name": "target-" + volume} for volume in migrate.recovery.VOLUMES.values()}}
            inodes = {}
            for suffix in migrate.KEYS:
                path = runtime / "secrets" / f"stock-ai-release_{suffix}"
                path.write_bytes(b"original-longer-key")
                path.chmod(0o440)
                inodes[suffix] = path.stat().st_ino
                (stage / "keys" / suffix).write_bytes(b"new-key")
                (stage / "secrets" / path.name).write_bytes(b"original-longer-key")
            with patch("migrate.recovery.build_archive", side_effect=lambda _, archive: archive.write_bytes(b"archive")), \
                    patch("migrate.subprocess.run", return_value=SimpleNamespace(returncode=0)):
                migrate.replace_state(operator, config, stage)
                for suffix in migrate.KEYS:
                    path = runtime / "secrets" / f"stock-ai-release_{suffix}"
                    self.assertEqual(path.read_bytes(), b"new-key")
                    self.assertEqual(path.stat().st_ino, inodes[suffix])
                    self.assertEqual(path.stat().st_mode & 0o777, 0o440)
                migrate.replace_state(operator, config, stage, rollback=True)
                for suffix in migrate.KEYS:
                    self.assertEqual((runtime / "secrets" / f"stock-ai-release_{suffix}").read_bytes(), b"original-longer-key")

    def test_key_write_failure_restores_readonly_mode_and_rollback_can_write(self):
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "readonly-key"
            path.write_bytes(b"predeployment-key")
            path.chmod(0o440)
            original = path.stat()
            real_open = Path.open
            def fail_open(candidate, *args, **kwargs):
                if candidate == path and args == ("r+b",):
                    self.assertEqual(candidate.stat().st_mode & 0o777, 0o600)
                    raise PermissionError("injected write failure")
                return real_open(candidate, *args, **kwargs)
            with patch.object(Path, "open", fail_open), self.assertRaises(PermissionError):
                migrate.replace_key(path, b"replacement-key")
            self.assertEqual(path.stat().st_mode & 0o777, 0o440)
            migrate.replace_key(path, b"predeployment-key")
            restored = path.stat()
            self.assertEqual(path.read_bytes(), b"predeployment-key")
            self.assertEqual((restored.st_ino, restored.st_uid, restored.st_gid, restored.st_mode),
                             (original.st_ino, original.st_uid, original.st_gid, original.st_mode))

    def test_fsync_failure_restores_readonly_mode_before_rollback(self):
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "readonly-key"
            path.write_bytes(b"predeployment-key")
            path.chmod(0o440)
            with patch("migrate.os.fsync", side_effect=OSError("injected disk failure")), self.assertRaises(OSError):
                migrate.replace_key(path, b"partial-new-key")
            self.assertEqual(path.stat().st_mode & 0o777, 0o440)
            migrate.replace_key(path, b"predeployment-key")
            self.assertEqual(path.read_bytes(), b"predeployment-key")
            self.assertEqual(path.stat().st_mode & 0o777, 0o440)


if __name__ == "__main__":
    unittest.main()
