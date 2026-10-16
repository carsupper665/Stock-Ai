import subprocess
import tempfile
import unittest
from pathlib import Path

import prepare
from scripts.local_operations import WorkflowError

SETTINGS = {
    "RELEASE_APP_HOST": "stock-ai.corp.internal:8443",
    "RELEASE_TLS_CERT": "", "RELEASE_TLS_KEY": "",
    "OIDC_ISSUER": "https://login.corp.internal/application/o/stock-ai/",
    "OIDC_AUTHORIZATION_URL": "https://login.corp.internal/application/o/authorize/",
    "OIDC_TOKEN_URL": "https://login.corp.internal/application/o/token/",
    "OIDC_JWKS_URL": "https://login.corp.internal/application/o/stock-ai/jwks/",
    "OIDC_CLIENT_ID": "c0ffee", "OIDC_USER_SUBJECT": "user-1", "OIDC_AUDIENCE": "stock-ai-api",
    "OIDC_TOKEN_AUTH_METHOD": "client_secret_post", "OIDC_TOKEN_TYPE": "JWT",
}


class ExternalIdPPreparationTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="release-external-")
        self.base = Path(self.temporary.name)
        cert, key = self.base / "fullchain.pem", self.base / "privkey.pem"
        subprocess.run(["openssl", "req", "-x509", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:P-256", "-nodes",
                        "-keyout", str(key), "-out", str(cert), "-days", "30", "-subj", "/CN=stock-ai.corp.internal",
                        "-addext", "subjectAltName=DNS:stock-ai.corp.internal"], check=True, capture_output=True)
        self.settings = {**SETTINGS, "RELEASE_TLS_CERT": str(cert), "RELEASE_TLS_KEY": str(key)}
        self.secret = self.base / "client-secret"
        self.secret.write_text("x" * 40 + "\n")

    def tearDown(self):
        self.temporary.cleanup()

    def write_settings(self, values):
        path = self.base / "idp.env"
        path.write_text("# comment\n" + "".join(f"{key}={value}\n" for key, value in values.items()))
        return path

    def test_settings_require_every_idp_value_and_reject_unknown_keys(self):
        missing = dict(self.settings)
        del missing["OIDC_AUDIENCE"]
        with self.assertRaisesRegex(WorkflowError, "OIDC_AUDIENCE"):
            prepare.read_external_idp(self.write_settings(missing))
        with self.assertRaisesRegex(WorkflowError, "OIDC_CLIENT_SECRET"):
            prepare.read_external_idp(self.write_settings({**self.settings, "OIDC_CLIENT_SECRET": "inline"}))
        idp = prepare.read_external_idp(self.write_settings(self.settings))
        self.assertEqual(idp["RELEASE_BIND"], "127.0.0.1:8443")
        self.assertEqual(idp["OIDC_TOKEN_AUTH_METHOD"], "client_secret_post")

    def test_prepare_writes_external_idp_runtime_without_fixture_material(self):
        bundle = self.base / "chain.pem"
        bundle.write_text("-----BEGIN CERTIFICATE-----\nintermediate\n-----END CERTIFICATE-----\n")
        idp = prepare.read_external_idp(self.write_settings({**self.settings, "OIDC_CA_BUNDLE": str(bundle)}))
        runtime = self.base / "runtime"
        env_file = prepare.prepare(runtime=runtime, idp=idp, client_secret_file=self.secret)
        values = dict(line.split("=", 1) for line in env_file.read_text().splitlines())
        for key in ("OIDC_ISSUER", "OIDC_CLIENT_ID", "OIDC_TOKEN_AUTH_METHOD", "OIDC_TOKEN_TYPE", "OIDC_AUDIENCE", "RELEASE_APP_HOST"):
            self.assertEqual(values[key], SETTINGS[key])
        self.assertNotIn("LOCAL_RELEASE_FIXTURE_IMAGE", values)
        self.assertNotIn("RELEASE_TLS_KEY", values)
        self.assertEqual(Path(values["OIDC_CA_BUNDLE"]).read_bytes(), bundle.read_bytes())
        self.assertEqual((runtime / "secrets/stock-ai-release_oidc_client_secret").read_text(), "x" * 40 + "\n")
        self.assertFalse((runtime / "secrets/stock-ai-release_fixture_provider_secret").exists())
        self.assertFalse((runtime / "certs/config.json").exists())
        self.assertTrue((runtime / "secrets/stock-ai-release_oidc_cookie_key").exists())

    def test_prepare_rejects_tls_pair_for_another_host_and_short_client_secret(self):
        idp = prepare.read_external_idp(self.write_settings({**self.settings, "RELEASE_APP_HOST": "other.corp.internal"}))
        with self.assertRaisesRegex(WorkflowError, "does not cover"):
            prepare.prepare(runtime=self.base / "runtime-host", idp=idp, client_secret_file=self.secret)
        self.secret.write_text("short\n")
        idp = prepare.read_external_idp(self.write_settings(self.settings))
        with self.assertRaisesRegex(WorkflowError, "32 bytes"):
            prepare.prepare(runtime=self.base / "runtime-secret", idp=idp, client_secret_file=self.secret)


if __name__ == "__main__":
    unittest.main()
