import hashlib
import json
import re
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "scripts" / "windows-operations.ps1"
DOC = ROOT / "docs" / "deployment" / "windows-operations.md"
LOCAL_DOC = ROOT / "docs" / "deployment" / "local-operations.md"


class WindowsOperationsPolicyTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.script = SCRIPT.read_text(encoding="utf-8")
        cls.doc = DOC.read_text(encoding="utf-8")
        cls.local_doc = LOCAL_DOC.read_text(encoding="utf-8")

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

    def test_status_digest_is_derived_from_the_configured_reference(self):
        pattern = re.search(
            r"\$script:ImmutableImageReferenceRegex = '([^']+)'", self.script
        ).group(1)
        python_pattern = pattern.replace("(?<digest>", "(?P<digest>")

        def configured_digest(reference):
            match = re.fullmatch(python_pattern, reference)
            return match.group("digest") if match else None

        digest = "a" * 64
        self.assertEqual(
            configured_digest(f"registry.example/backend@sha256:{digest}"),
            f"sha256:{digest}",
        )
        self.assertIsNone(configured_digest("stock-ai/backend:0.7.0"))
        self.assertIsNone(configured_digest(f"registry.example/backend@sha256:{'A' * 64}"))
        self.assertIn("Get-ConfiguredImageDigest $imageReference", self.script)

    def test_release_requires_digests_while_test_tags_are_an_explicit_exception(self):
        policy = re.search(
            r"\$script:DigestRequiredEnvironments = @\(([^)]*)\)", self.script
        )
        digest_required_environments = set(re.findall(r"'([^']+)'", policy.group(1)))
        self.assertEqual(digest_required_environments, {"Release"})
        self.assertIn(
            "$script:DigestRequiredEnvironments -contains $Environment", self.script
        )
        self.assertIn("Assert-EnvironmentCompose $config", self.script)

        image_pattern = re.search(
            r"\$script:ImmutableImageReferenceRegex = '([^']+)'", self.script
        ).group(1).replace("(?<digest>", "(?P<digest>")
        is_immutable = lambda image: re.fullmatch(image_pattern, image) is not None
        tagged_image = "stock-ai/backend:0.7.0"
        pinned_image = f"registry.example/backend@sha256:{'b' * 64}"
        def environment_accepts(environment, image):
            if environment not in {"Test", "Release"}:
                return False
            return environment not in digest_required_environments or is_immutable(image)

        self.assertFalse(environment_accepts("Release", tagged_image))
        self.assertTrue(environment_accepts("Test", tagged_image))
        self.assertTrue(environment_accepts("Release", pinned_image))
        self.assertFalse(environment_accepts("Production", pinned_image))
        self.assertNotIn("Test", digest_required_environments)

    def test_promotion_fixture_rejects_bad_or_missing_sha256s(self):
        digest_pattern = re.search(
            r"\$script:ImmutableImageReferenceRegex = '([^']+)'", self.script
        ).group(1).replace("(?<digest>", "(?P<digest>")
        checksum_prefix = re.search(
            r"\$script:ChecksumLinePrefixRegex = '([^']+)'", self.script
        ).group(1)
        services = {"backend", "llm-provider-server", "agent-server", "console"}

        def validate_fixture(manifest_text, checksum_text, filename="promotion.json"):
            try:
                manifest = json.loads(manifest_text)
            except json.JSONDecodeError:
                return False
            if manifest.get("status") != "promoted" or set(manifest.get("images", {})) != services:
                return False
            if not all(
                re.fullmatch(digest_pattern, image)
                for image in manifest["images"].values()
            ):
                return False
            expected = hashlib.sha256(manifest_text.encode("utf-8")).hexdigest()
            line_pattern = re.compile(checksum_prefix + re.escape(filename) + r"$")
            return any(
                match.group(1).lower() == expected
                for line in checksum_text.splitlines()
                if (match := line_pattern.match(line))
            )

        manifest = {
            "status": "promoted",
            "images": {
                service: f"registry.example/{service}@sha256:{'c' * 64}"
                for service in services
            },
        }
        manifest_text = json.dumps(manifest, sort_keys=True)
        valid_hash = hashlib.sha256(manifest_text.encode("utf-8")).hexdigest()
        valid_checksum = f"{valid_hash}  *promotion.json\n"
        self.assertTrue(validate_fixture(manifest_text, valid_checksum))
        self.assertFalse(validate_fixture(manifest_text, ""))
        self.assertFalse(validate_fixture(manifest_text, f"{'0' * 64}  promotion.json\n"))
        self.assertFalse(validate_fixture(manifest_text, f"{valid_hash}  other.json\n"))

        mutable = json.loads(manifest_text)
        mutable["images"]["backend"] = "stock-ai/backend:0.7.0"
        mutable_text = json.dumps(mutable, sort_keys=True)
        mutable_hash = hashlib.sha256(mutable_text.encode("utf-8")).hexdigest()
        self.assertFalse(
            validate_fixture(mutable_text, f"{mutable_hash}  promotion.json\n")
        )

        get_promotion = self.script.split("function Get-Promotion", 1)[1].split(
            "function Set-ImageAssignments", 1
        )[0]
        self.assertIn("$script:ImmutableImageReferenceRegex", get_promotion)
        self.assertIn("$script:ChecksumLinePrefixRegex", get_promotion)
        self.assertIn("if (!$verified) { throw", get_promotion)

    def test_legacy_exclusion_and_spaced_path_instructions(self):
        self.assertIn("WSL/Linux", self.doc)
        self.assertIn("not_run", self.doc)
        self.assertIn("-LiteralPath", self.script)
        self.assertIn("legacy mixed Windows/Linux\ndistribution", self.local_doc)

if __name__ == "__main__":
    unittest.main()
