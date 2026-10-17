import argparse
import importlib.util
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location("release_bootstrap", ROOT / "scripts/release/bootstrap.py")
bootstrap = importlib.util.module_from_spec(spec)
spec.loader.exec_module(bootstrap)


class ReleaseBootstrapTest(unittest.TestCase):
    def test_invalid_codex_login_is_rejected_before_docker_changes(self):
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "auth.json"
            path.write_text("[]")
            args = argparse.Namespace(bundle=Path(temporary), verify_only=False, idp_env=None,
                                      oidc_client_secret_file=None, codex_auth_file=path)
            with patch.object(bootstrap, "verify", return_value={}), patch.object(bootstrap, "run") as docker:
                with self.assertRaisesRegex(ValueError, "auth.json"):
                    bootstrap.main(args)
                docker.assert_not_called()


if __name__ == "__main__":
    unittest.main()
