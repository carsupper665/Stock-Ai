import contextlib
import importlib.util
import io
import json
import os
import sys
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location(
    "production_secrets", ROOT / "scripts" / "production_secrets.py"
)
MODULE = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
sys.modules[SPEC.name] = MODULE
SPEC.loader.exec_module(MODULE)


class ProductionSecretsTest(unittest.TestCase):
    def generate(self, root: Path, environment: str) -> Path:
        destination = root / environment.lower() / MODULE.namespace_for(environment)
        destination.parent.mkdir()
        MODULE.generate(
            environment,
            destination,
            f"data/{environment.lower()}-backend.db",
            f"data/{environment.lower()}-provider.db",
        )
        return destination

    def test_generation_is_strong_distinct_isolated_and_silent(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            output = io.StringIO()
            with contextlib.redirect_stdout(output), contextlib.redirect_stderr(output):
                test = self.generate(root, "Test")
                release = self.generate(root, "Release")
            test_results, test_values = MODULE.validate("Test", test, release)
            release_results, release_values = MODULE.validate("Release", release, test)
            self.assertTrue(all(result.valid for result in test_results), test_results)
            self.assertTrue(all(result.valid for result in release_results), release_results)
            self.assertFalse(set(test_values.values()) & set(release_values.values()))
            combined = b"\n".join((*test_values.values(), *release_values.values()))
            self.assertNotIn(combined.decode("ascii"), output.getvalue())
            for value in (*test_values.values(), *release_values.values()):
                self.assertNotIn(value.decode("ascii"), output.getvalue())

    def test_missing_placeholder_weak_and_duplicate_values_are_rejected(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = self.generate(Path(temporary), "Release")
            manifest = json.loads((directory / "production-config.json").read_text())
            cases = {
                "backend_user_token": b"change-me\n",
                "backend_middleware_token": b"short\n",
                "agent_admin_token": b"a" * 64 + b"\n",
                "llm_admin_token": b"short\n",
                "backend_account_token_key": b"MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=\n",
            }
            for setting, value in cases.items():
                (directory / manifest["secret_files"][setting]).write_bytes(value)
            (directory / manifest["secret_files"]["oidc_client_secret"]).unlink()
            runtime = directory / manifest["secret_files"]["llm_runtime_token"]
            master = directory / manifest["secret_files"]["llm_master_key"]
            runtime.write_bytes(master.read_bytes())
            results, _ = MODULE.validate("Release", directory)
            invalid = {result.setting for result in results if not result.valid}
            self.assertTrue(set(cases) <= invalid)
            self.assertIn("oidc_client_secret", invalid)
            self.assertIn("llm_runtime_token", invalid)
            self.assertIn("secret_role_separation", invalid)

    def test_wrong_environment_namespace_and_cross_environment_reuse_are_rejected(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            test = self.generate(root, "Test")
            release = self.generate(root, "Release")
            wrong_results, _ = MODULE.validate("Release", test)
            self.assertTrue(any(not result.valid for result in wrong_results))

            test_manifest = json.loads((test / "production-config.json").read_text())
            release_manifest = json.loads((release / "production-config.json").read_text())
            test_path = test / test_manifest["secret_files"]["agent_admin_token"]
            release_path = release / release_manifest["secret_files"]["agent_admin_token"]
            release_path.write_bytes(test_path.read_bytes())
            results, _ = MODULE.validate("Release", release, test)
            isolation = next(result for result in results if result.setting == "environment_isolation")
            self.assertFalse(isolation.valid)

    def test_backend_middleware_token_is_generated_separate_from_agent_token(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = self.generate(Path(temporary), "Release")
            manifest = json.loads((directory / "production-config.json").read_text())
            self.assertEqual(manifest["secret_files"]["backend_middleware_token"], "stock-ai-release_backend_middleware_token")
            results, values = MODULE.validate("Release", directory)
            self.assertTrue(next(r for r in results if r.setting == "backend_middleware_token").valid)
            self.assertNotEqual(values["backend_middleware_token"], values["backend_user_token"])
            consumers = {
                mount["service"]
                for mount in manifest["container_mounts"]
                if mount["secret"] == "backend_middleware_token"
            }
            self.assertEqual(consumers, {"backend", "middleware"})

    def test_master_key_is_validated_before_database_use(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = self.generate(Path(temporary), "Release")
            manifest = json.loads((directory / "production-config.json").read_text())
            master = directory / manifest["secret_files"]["llm_master_key"]
            master.write_text("not-base64\n", encoding="ascii")
            results, _ = MODULE.validate("Release", directory)
            result = next(item for item in results if item.setting == "llm_master_key")
            self.assertFalse(result.valid)
            self.assertIn("base64", result.reason)

    def test_diagnostics_never_contain_secret_material(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = self.generate(Path(temporary), "Test")
            results, values = MODULE.validate("Test", directory)
            output = json.dumps([result.diagnostic() for result in results])
            for value in values.values():
                self.assertNotIn(value.decode("ascii"), output)
            self.assertTrue(all(result.source for result in results))

    def test_non_disclosure_scan_covers_image_frontend_http_error_and_logs(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            directory = self.generate(root, "Release")
            results, values = MODULE.validate("Release", directory)
            self.assertTrue(all(result.valid for result in results))
            clean = []
            for name in ("image.tar", "frontend.js", "http-response.txt", "error.txt", "normal.log"):
                path = root / name
                path.write_bytes(b"safe diagnostic content\n")
                clean.append(path)
            scans = MODULE.scan_disclosures(values, clean)
            self.assertTrue(all(result.valid for result in scans), scans)

            sentinel = values["agent_admin_token"]
            for path in clean:
                path.write_bytes(b"prefix " + sentinel + b" suffix")
                scan = MODULE.scan_disclosures(values, [path])[0]
                self.assertFalse(scan.valid, path.name)
                self.assertNotIn(sentinel.decode("ascii"), scan.reason)

    @unittest.skipIf(os.name == "nt", "POSIX permissions only")
    def test_group_or_world_readable_secret_is_rejected(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = self.generate(Path(temporary), "Test")
            manifest = json.loads((directory / "production-config.json").read_text())
            path = directory / manifest["secret_files"]["llm_runtime_token"]
            path.chmod(0o644)
            results, _ = MODULE.validate("Test", directory)
            result = next(item for item in results if item.setting == "llm_runtime_token")
            self.assertFalse(result.valid)
            self.assertIn("0600", result.reason)

    def test_windows_wrapper_enforces_acl_without_reading_values(self):
        wrapper = (ROOT / "scripts" / "production-secrets.ps1").read_text(encoding="utf-8")
        self.assertIn("Assert-RestrictedAcl", wrapper)
        self.assertIn("S-1-5-18", wrapper)
        self.assertIn("S-1-5-32-544", wrapper)
        self.assertIn("$entry.IsInherited", wrapper)
        self.assertNotIn("Get-Content -LiteralPath (Join-Path $Root", wrapper)


if __name__ == "__main__":
    unittest.main()
