import copy
import json
import os
import sys
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parent))
import prepare
import lint
import start
from scripts.local_operations import Operator, WorkflowError


class ReleaseConfigurationTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.temporary = tempfile.TemporaryDirectory(prefix="release-config-")
        cls.runtime = Path(cls.temporary.name) / ".local-release"
        cls.env_file = prepare.prepare(runtime=cls.runtime)
        cls.operator = Operator(cls.env_file, lint.FILES, cls.runtime / "evidence", 60)
        cls.config = cls.operator.compose_config()
        cls.images, _ = prepare.approved_images(prepare.DEFAULT_MANIFEST)

    @classmethod
    def tearDownClass(cls):
        cls.temporary.cleanup()

    def test_real_rendered_configuration_and_material_pass(self):
        lint.validate(self.config, self.operator.values, self.images)
        lint.validate_material(self.config, self.operator.values, self.runtime)
        self.assertEqual(lint.lint(self.env_file, prepare.DEFAULT_MANIFEST)["status"], "passed")

    def test_environment_override_cannot_replace_release_identity(self):
        with patch.dict(os.environ, {"STOCK_AI_INSTANCE": "stock-ai-local-test", "OIDC_ISSUER": "https://idp.localtest"}):
            self.assertEqual(self.operator.compose_config()["name"], prepare.INSTANCE)

    def test_preparation_preserves_generated_secrets_and_keys(self):
        original = {path.relative_to(self.runtime): path.read_bytes() for path in self.runtime.rglob("*") if path.is_file()}
        prepare.prepare(runtime=self.runtime)
        self.assertEqual(original, {path.relative_to(self.runtime): path.read_bytes() for path in self.runtime.rglob("*") if path.is_file()})

    def test_fail_closed_mutations(self):
        mutations = {
            "test_subject": lambda c: c["services"]["backend"]["environment"].update(OIDC_USER_SUBJECT="local-test-user"),
            "test_cookie": lambda c: c["services"]["oidc-gateway"]["environment"].update(OIDC_COOKIE_NAME="__Host-stock-ai-test-session"),
            "missing_port": lambda c: c["services"]["nginx"].update(ports=[]),
            "wildcard_bind": lambda c: c["services"]["nginx"]["ports"][0].update(host_ip="0.0.0.0"),
            "test_port": lambda c: c["services"]["nginx"]["ports"][0].update(published="443"),
            "private_port": lambda c: c["services"]["console"].update(ports=[{"host_ip": "127.0.0.1", "published": "8088", "target": 8088}]),
            "mutable_image": lambda c: c["services"]["backend"].update(image="stock-ai/backend:latest"),
            "digest_substitution": lambda c: c["services"]["backend"].update(image="stock-ai/backend@sha256:"+"f"*64),
            "empty_build": lambda c: c["services"]["backend"].update(build={}),
            "missing_persistence": lambda c: c["services"]["backend"].update(volumes=[]),
            "test_volume": lambda c: c["volumes"]["backend_state"].update(name="stock-ai-local-test_backend_state"),
            "external_network": lambda c: c["networks"]["edge"].update(external=True),
            "static_auth": lambda c: c["services"]["backend"]["environment"].update(ALLOW_STATIC_USER_TOKEN="true"),
            "missing_gid": lambda c: c["services"]["oidc-gateway"].pop("user"),
            "wrong_origin": lambda c: c["services"]["oidc-gateway"]["environment"].update(OIDC_REDIRECT_URI="https://app.localrelease/oauth2/callback"),
            "wrong_db_path": lambda c: c["services"]["backend"]["environment"].update(DB_DSN="/tmp/backend.db"),
            "placeholder": lambda c: c["services"]["idp"]["environment"].update(UNEXPECTED_SECRET="changeme"),
        }
        for name, mutate in mutations.items():
            with self.subTest(name=name):
                config = copy.deepcopy(self.config)
                mutate(config)
                with self.assertRaises(WorkflowError):
                    lint.validate(config, self.operator.values, self.images)

    def test_placeholder_and_symlink_secret_are_rejected(self):
        path = self.runtime / "secrets/stock-ai-release_oidc_client_secret"
        original = path.read_bytes()
        path.chmod(0o600)
        path.write_bytes(b"changeme\n")
        path.chmod(0o440)
        try:
            with self.assertRaises(WorkflowError):
                lint.validate_material(self.config, self.operator.values, self.runtime)
        finally:
            path.chmod(0o600)
            path.write_bytes(original)
            path.chmod(0o440)
        path.unlink()
        path.symlink_to(self.runtime / "secrets/stock-ai-release_backend_user_token")
        try:
            with self.assertRaises(WorkflowError):
                lint.validate_material(self.config, self.operator.values, self.runtime)
        finally:
            path.unlink()
            path.write_bytes(original)
            path.chmod(0o440)

    def test_start_cannot_bypass_failed_lint(self):
        with patch.object(start, "lint", side_effect=WorkflowError("blocked")), patch.object(Operator, "compose_run") as run:
            with self.assertRaises(WorkflowError):
                start.start(self.env_file, prepare.DEFAULT_MANIFEST)
            run.assert_not_called()

    def test_start_always_disables_builds(self):
        with patch.object(start, "lint", return_value={"status": "passed"}), patch.object(Operator, "compose_run") as run:
            start.start(self.env_file, prepare.DEFAULT_MANIFEST)
            run.assert_called_once_with("up", "-d", "--no-build", "--wait")


if __name__ == "__main__":
    unittest.main()
