import tempfile
import unittest
from pathlib import Path

from scripts.check_versions import VersionError, check


class CheckVersionsTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self.files = {
            "backend": ["1.2.3-b0r0"],
            "llm_provider_server": ["1.0.0-b0r0"],
            "agent_server": ["2.0.0-b0r0", "backend 1.2.3-b0r0", "llm_provider_server 1.0.0-b0r0"],
            "middleware": ["3.0.0-b0r0", "backend 1.2.3-b0r0", "agent_server 2.0.0-b0r0", "llm_provider_server 1.0.0-b0r0"],
            "frontend": ["4.0.0-b0r0", "backend 1.2.3-b0r0", "agent_server 2.0.0-b0r0",
                         "llm_provider_server 1.0.0-b0r0", "middleware 3.0.0-b0r0"],
        }

    def tearDown(self):
        self.temp.cleanup()

    def run_check(self):
        for service, lines in self.files.items():
            (self.root / service).mkdir(exist_ok=True)
            (self.root / service / "VERSION").write_text("\n".join(lines) + "\n", encoding="utf-8")
        return check(self.root)

    def test_repository_versions_are_compatible(self):
        check()

    def test_beta_numbers_are_not_compared(self):
        self.files["backend"] = ["1.2.3-b20r5"]
        self.assertEqual(self.run_check()["backend"], "1.2.3-b20r5")

    def test_service_rejects_different_release(self):
        self.files["backend"] = ["1.2.4-b0r0"]
        with self.assertRaisesRegex(VersionError, "agent_server requires backend 1.2.3-b0r0, found 1.2.4-b0r0"):
            self.run_check()

    def test_service_rejects_older_release(self):
        self.files["llm_provider_server"] = ["0.9.9-b0r0"]
        with self.assertRaisesRegex(VersionError, "agent_server requires llm_provider_server"):
            self.run_check()

    def test_frontend_accepts_older_service(self):
        self.files["middleware"][0] = "2.9.0-b0r0"
        self.files["frontend"][-1] = "middleware 3.0.0-b0r0"
        self.run_check()

    def test_frontend_rejects_newer_service(self):
        self.files["middleware"][0] = "3.0.1-b0r0"
        with self.assertRaisesRegex(VersionError, "frontend requires middleware at most 3.0.0-b0r0, found 3.0.1-b0r0"):
            self.run_check()

    def test_malformed_version(self):
        self.files["backend"] = ["1.2.3"]
        with self.assertRaisesRegex(VersionError, "backend: '1.2.3' is not X.Y.Z-bNrM"):
            self.run_check()

    def test_unknown_dependency(self):
        self.files["backend"].append("console 1.0.0-b0r0")
        with self.assertRaisesRegex(VersionError, "backend requires unknown service console"):
            self.run_check()


if __name__ == "__main__":
    unittest.main()
