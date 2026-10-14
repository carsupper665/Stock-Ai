"""Regressions for private, idempotent local Test material preparation."""

import os
import stat
import tempfile
import unittest
from pathlib import Path

import prepare


class PrepareTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="local-test-prepare-")
        self.root = Path(self.temporary.name)
        self.original_paths = (prepare.RUNTIME, prepare.CERTS, prepare.SECRETS)
        prepare.RUNTIME = self.root / "runtime"
        prepare.CERTS = prepare.RUNTIME / "certs"
        prepare.SECRETS = prepare.RUNTIME / "secrets"
        prepare.RUNTIME.mkdir(mode=0o700)
        (prepare.RUNTIME / "registry-references.json").write_text(
            '{"backend":"127.0.0.1:5000/stock-ai/backend@sha256:' + "a" * 64 + '",'
            '"agent-server":"127.0.0.1:5000/stock-ai/agent-server@sha256:' + "b" * 64 + '",'
            '"llm-provider-server":"127.0.0.1:5000/stock-ai/llm-provider-server@sha256:' + "c" * 64 + '",'
            '"console":"127.0.0.1:5000/stock-ai/console@sha256:' + "d" * 64 + '",'
            '"oidc-gateway":"127.0.0.1:5000/stock-ai/oidc-gateway@sha256:' + "e" * 64 + '"}\n',
            encoding="utf-8",
        )

    def tearDown(self):
        prepare.RUNTIME, prepare.CERTS, prepare.SECRETS = self.original_paths
        self.temporary.cleanup()

    def material_bytes(self):
        paths = sorted(prepare.CERTS.glob("*.pem")) + sorted(prepare.SECRETS.iterdir())
        return {path.relative_to(prepare.RUNTIME).as_posix(): path.read_bytes() for path in paths}

    @staticmethod
    def can_access_as(path: Path, uid: int, gid: int) -> bool:
        info = path.stat()
        owner, group, other = ((stat.S_IXUSR, stat.S_IXGRP, stat.S_IXOTH) if path.is_dir()
                               else (stat.S_IRUSR, stat.S_IRGRP, stat.S_IROTH))
        if uid == info.st_uid:
            mask = owner
        elif gid == info.st_gid:
            mask = group
        else:
            mask = other
        return bool(info.st_mode & mask)

    def test_two_prepare_runs_preserve_all_key_and_secret_bytes_and_private_modes(self):
        prepare.main()
        first = self.material_bytes()
        prepare.main()
        self.assertEqual(self.material_bytes(), first)

        self.assertEqual(prepare.RUNTIME.stat().st_mode & 0o777, 0o700)
        self.assertEqual(prepare.CERTS.stat().st_mode & 0o777, 0o750)
        self.assertEqual(prepare.SECRETS.stat().st_mode & 0o777, 0o700)
        private_paths = [prepare.CERTS / "privkey.pem", prepare.CERTS / "jwks-signing-key.pem"]
        private_paths.extend(prepare.SECRETS.iterdir())
        for path in private_paths:
            mode = path.stat().st_mode & 0o777
            self.assertEqual(mode, 0o440, path.name)
            self.assertEqual(path.stat().st_gid, os.getgid(), path.name)
            self.assertTrue(self.can_access_as(path, 65532, os.getgid()), path.name)
            self.assertEqual(mode & 0o007, 0, path.name)
        self.assertTrue(self.can_access_as(prepare.CERTS, 101, os.getgid()))
        self.assertTrue(self.can_access_as(prepare.CERTS / "privkey.pem", 101, os.getgid()))

    def test_partial_tls_pair_is_rejected_without_rotating_remaining_bytes(self):
        prepare.main()
        cert = prepare.CERTS / "fullchain.pem"
        key = prepare.CERTS / "privkey.pem"
        original_cert = cert.read_bytes()
        key.unlink()
        with self.assertRaisesRegex(prepare.PrepareError, "partial TLS"):
            prepare.main()
        self.assertEqual(cert.read_bytes(), original_cert)
        self.assertFalse(key.exists())

    def test_malformed_tls_and_jwks_keys_fail_closed(self):
        prepare.main()
        tls_key = prepare.CERTS / "privkey.pem"
        original = tls_key.read_bytes()
        tls_key.chmod(0o600)
        tls_key.write_bytes(b"malformed private key\n")
        with self.assertRaises(prepare.PrepareError):
            prepare.main()
        self.assertEqual(tls_key.read_bytes(), b"malformed private key\n")
        tls_key.chmod(0o600)
        tls_key.write_bytes(original)

        jwks = prepare.CERTS / "jwks-signing-key.pem"
        jwks.chmod(0o600)
        jwks.write_bytes(b"malformed JWKS private key\n")
        with self.assertRaises(prepare.PrepareError):
            prepare.main()
        self.assertEqual(jwks.read_bytes(), b"malformed JWKS private key\n")

    def test_malformed_existing_application_secret_is_not_rotated(self):
        prepare.main()
        token = prepare.SECRETS / "stock-ai-test_backend_user_token"
        token.chmod(0o600)
        token.write_bytes(b"bad-token\n")
        with self.assertRaisesRegex(prepare.PrepareError, "malformed existing secret"):
            prepare.main()
        self.assertEqual(token.read_bytes(), b"bad-token\n")


if __name__ == "__main__":
    unittest.main()
