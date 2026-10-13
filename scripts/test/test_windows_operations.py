import re
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "scripts" / "windows-operations.ps1"
DOC = ROOT / "docs" / "deployment" / "windows-operations.md"


class WindowsOperationsPolicyTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.script = SCRIPT.read_text(encoding="utf-8")
        cls.doc = DOC.read_text(encoding="utf-8")

    def test_noninteractive_actions_and_compose_path_are_explicit(self):
        for action in ("Install", "Preflight", "Start", "Status", "Stop", "Update", "Rollback", "Diagnose"):
            self.assertIn(f"'{action}'", self.script)
        self.assertIn("Resolve-Path -LiteralPath $ComposeFile", self.script)
        self.assertIn("Resolve-Path -LiteralPath $EnvFile", self.script)
        self.assertIn("docker.exe", self.script)
        self.assertIn("Invoke-Compose @('up', '-d', '--wait'", self.script)

    def test_preflight_covers_windows_docker_resources_virtualization_and_ports(self):
        for control in (
            "Win32_OperatingSystem",
            "19045",
            "22000",
            "VirtualizationFirmwareEnabled",
            "linux|",
            "TotalPhysicalMemory",
            "FreeSpace",
            "Get-NetTCPConnection",
            "'STOCK_AI_PUBLIC_PORTS'",
        ):
            self.assertIn(control, self.script)

    def test_health_status_and_diagnostics_are_nonsecret_and_actionable(self):
        self.assertIn("function Wait-ForHealth", self.script)
        self.assertIn("Diagnostic location:", self.script)
        self.assertIn("function Write-Diagnostics", self.script)
        for key in ("container_id", "restart_count", "disk_bytes", "volume_names", "docker_disk_usage", "expires_utc", "issuer_host", "age_hours"):
            self.assertIn(key, self.script)
        self.assertNotIn("docker compose config", self.script.lower())
        self.assertNotRegex(self.script, r"(?i)Write-(?:Host|Output|Information).*secret|Get-Content.*secret.*Write")

    def test_ordered_shutdown_digest_gate_and_deterministic_rollback(self):
        stop = self.script.split("'Stop' {", 1)[1].split("'Update' {", 1)[0]
        self.assertLess(stop.index("'console'"), stop.index("'agent-server'"))
        self.assertLess(stop.index("'agent-server'"), stop.index("'llm-provider-server'"))
        self.assertLess(stop.index("'llm-provider-server'"), stop.index("'backend'"))
        self.assertIn("@sha256:[0-9a-f]{64}", self.script)
        self.assertIn("SHA256SUMS", self.script)
        self.assertIn("previous_images", self.script)
        self.assertIn("promotion_manifest_sha256", self.script)
        self.assertIn("-RollbackMetadata", self.script)

    def test_legacy_exclusion_and_spaced_path_instructions(self):
        self.assertIn("C:\\ProgramData\\Stock AI", self.doc)
        self.assertIn("-LiteralPath", self.script)
        self.assertIn("legacy\nmixed Windows/Linux distribution is excluded", self.doc)

if __name__ == "__main__":
    unittest.main()
