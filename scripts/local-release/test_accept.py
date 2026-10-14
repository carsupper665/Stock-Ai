import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import patch

import accept
from migrate import KEYS
from prepare import SUFFIXES


class FullProfileRollbackTest(unittest.TestCase):
    def test_restores_old_ca_environment_bundle_and_readonly_keys_without_volume_removal(self):
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            runtime, previous = base / "runtime", base / "previous"
            bundle = runtime / "deployment"
            files = [bundle / "deploy/compose" / name for name in ("compose.yaml", "compose.local-release.yaml")]
            for root in (runtime, previous):
                (root / "certs").mkdir(parents=True)
                (root / "secrets").mkdir()
            (previous / "deploy/compose").mkdir(parents=True)
            files[0].parent.mkdir(parents=True)
            for root, tag in ((runtime, "new"), (previous, "old")):
                (root / "compose.env").write_text("STOCK_AI_INSTANCE=stock-ai-local-release\nPROFILE=" + tag + "\n")
                for name in ("fullchain.pem", "privkey.pem", "jwks-signing-key.pem", "config.json"):
                    path = root / "certs" / name
                    path.write_text(tag + "-" + name)
                    path.chmod(0o440)
                for suffix in SUFFIXES:
                    path = root / "secrets" / f"stock-ai-release_{suffix}"
                    # Only the two migrated keys differ; all other secrets were
                    # retained throughout installation/migration.
                    path.write_text((tag if suffix in KEYS else "same") + suffix)
                    path.chmod(0o440)
            for file in files:
                file.write_text("new compose")
                (previous / "deploy/compose" / file.name).write_text("old compose")
            calls = []
            operator = SimpleNamespace(env_file=runtime / "compose.env", compose_files=files,
                                       evidence_dir=runtime / "evidence", compose_run=lambda *args: calls.append(args))
            def restore_keys(operator, config, stage, rollback):
                self.assertTrue(rollback)
                for suffix in ("llm_master_key", "backend_account_token_key"):
                    path = runtime / "secrets" / f"stock-ai-release_{suffix}"
                    accept.replace_key(path, (previous / "secrets" / path.name).read_bytes())
            facts = {"all_records": "matched", "history": "matched"}
            with patch("accept.replace_state", side_effect=restore_keys), patch("accept.store_facts", return_value=facts), \
                    patch("accept.stopped_facts", return_value=facts), patch("accept.start") as start:
                result = accept.restore_profile(operator, {"services": {"nginx": {"image": "approved"}}}, previous, base)
            self.assertEqual(calls, [("down", "--timeout", "30")])
            self.assertEqual(result["status"], "passed")
            self.assertTrue(result["named_volumes_preserved"])
            self.assertEqual((runtime / "compose.env").read_bytes(), (previous / "compose.env").read_bytes())
            for name in ("fullchain.pem", "privkey.pem", "jwks-signing-key.pem", "config.json"):
                path = runtime / "certs" / name
                self.assertEqual(path.read_bytes(), (previous / "certs" / name).read_bytes())
                self.assertEqual(path.stat().st_mode & 0o777, 0o440)
            self.assertEqual(files[0].read_text(), "old compose")
            start.assert_called_once()


if __name__ == "__main__":
    unittest.main()
