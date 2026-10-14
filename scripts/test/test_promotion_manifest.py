import hashlib
import json
import tempfile
import unittest
from pathlib import Path

from scripts.create_promotion_manifest import (
    PromotionError,
    REQUIRED_RESULTS,
    TEST_IMAGES,
    create_manifest,
    main,
)


class PromotionManifestTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self.build = self.root / "build"
        self.build.mkdir()
        self.certification_path = self.root / "test-certification.json"
        self.references_path = self.root / "registry-references.json"
        self.revision = "a" * 40
        self._write_build_evidence()
        evidence_records = {}
        for result in REQUIRED_RESULTS:
            evidence_path = self.root / f"{result}.json"
            evidence_path.write_text(json.dumps({"result": result, "passed": True}), encoding="utf-8")
            evidence_records[result] = {
                "file": evidence_path.name,
                "sha256": hashlib.sha256(evidence_path.read_bytes()).hexdigest(),
            }
        self.certification = {
            "schema": "stock-ai.test-certification.v1",
            "status": "passed",
            "revision": self.revision,
            "configuration_schema_version": "compose-v1",
            "accepted_image_digests": self.digests,
            "results": {name: "passed" for name in REQUIRED_RESULTS},
            "evidence": evidence_records,
        }
        self.certification_path.write_text(json.dumps(self.certification), encoding="utf-8")
        self._write_certification_sidecar()
        self.references_path.write_text(
            json.dumps({
                service: f"registry.example/stock-ai/{service}@{self.digests[service]}"
                for service in TEST_IMAGES
            }),
            encoding="utf-8",
        )

    def tearDown(self):
        self.temp.cleanup()

    def _write_build_evidence(self):
        image_records = {}
        self.digests = {}
        for index, service in enumerate(TEST_IMAGES):
            self.digests[service] = f"sha256:{index + 1:064x}"
            image_records[service] = {"build_result_digest": self.digests[service]}
            (self.build / f"{service}-sbom.spdx.json").write_text("{}\n", encoding="utf-8")
            (self.build / f"{service}-vulnerabilities.sarif").write_text(
                '{"runs":[{"results":[]}]}\n', encoding="utf-8"
            )
            (self.build / f"{service}-files.txt").write_text(
                "app\netc/ssl/certs/ca-certificates.crt\npublic/assets/NewSession-Bundle.js\n", encoding="utf-8"
            )
        (self.build / "provenance.json").write_text(
            json.dumps({"revision": self.revision}), encoding="utf-8"
        )
        (self.build / "acceptance-summary.json").write_text(
            json.dumps({
                "status": "pass",
                "revision": self.revision,
                "critical_high_findings": 0,
                "images": image_records,
            }),
            encoding="utf-8",
        )
        lines = []
        for path in sorted(self.build.iterdir()):
            lines.append(f"{hashlib.sha256(path.read_bytes()).hexdigest()}  {path.name}")
        (self.build / "SHA256SUMS").write_text("\n".join(lines) + "\n", encoding="ascii")

    def _write_certification_sidecar(self):
        files = [self.certification_path]
        files.extend(self.root / record["file"] for record in self.certification["evidence"].values())
        files.sort()
        lines = [f"{hashlib.sha256(path.read_bytes()).hexdigest()}  {path.name}" for path in files]
        (self.root / "SHA256SUMS").write_text("\n".join(lines) + "\n", encoding="ascii")

    def test_manifest_binds_all_stack_artifacts_and_exact_test_digests(self):
        manifest = create_manifest(self.build, self.certification_path, self.references_path)
        self.assertEqual(manifest["status"], "promoted")
        self.assertEqual(manifest["git_revision"], self.revision)
        self.assertEqual(set(manifest["test_stack_images"]), set(TEST_IMAGES))
        self.assertEqual(set(manifest["images"]), set(TEST_IMAGES[:-1]))
        self.assertEqual(set(manifest["artifacts"]), set(TEST_IMAGES))
        for records in manifest["artifacts"].values():
            for key in ("sbom", "scan", "filesystem_inventory", "provenance", "acceptance"):
                self.assertEqual(len(records[key]["sha256"]), 64)

    def test_failed_acceptance_or_mutable_digest_blocks_promotion(self):
        self.certification["results"]["idp_restart"] = "not_run"
        self.certification_path.write_text(json.dumps(self.certification), encoding="utf-8")
        self._write_certification_sidecar()
        with self.assertRaisesRegex(PromotionError, "required passing"):
            create_manifest(self.build, self.certification_path, self.references_path)

        self.certification["results"]["idp_restart"] = "passed"
        self.certification_path.write_text(json.dumps(self.certification), encoding="utf-8")
        self._write_certification_sidecar()
        refs = json.loads(self.references_path.read_text(encoding="utf-8"))
        refs["backend"] = "registry.example/backend:1.0.0"
        self.references_path.write_text(json.dumps(refs), encoding="utf-8")
        with self.assertRaisesRegex(PromotionError, "immutable SHA256"):
            create_manifest(self.build, self.certification_path, self.references_path)

    def test_test_material_in_flattened_image_inventory_blocks_promotion(self):
        inventory = self.build / "console-files.txt"
        inventory.write_text("app\nrun/secrets/stock-ai-test_oidc_client_secret\n", encoding="utf-8")
        lines = []
        for path in sorted(self.build.iterdir()):
            if path.name != "SHA256SUMS":
                lines.append(f"{hashlib.sha256(path.read_bytes()).hexdigest()}  {path.name}")
        (self.build / "SHA256SUMS").write_text("\n".join(lines) + "\n", encoding="ascii")
        with self.assertRaisesRegex(PromotionError, "Test material"):
            create_manifest(self.build, self.certification_path, self.references_path)

    def test_modified_build_evidence_fails_checksum_validation(self):
        (self.build / "backend-files.txt").write_text("app\nextra\n", encoding="utf-8")
        with self.assertRaisesRegex(PromotionError, "checksum mismatch"):
            create_manifest(self.build, self.certification_path, self.references_path)

    def test_test_result_must_match_certification_sidecar(self):
        result_path = self.root / self.certification["evidence"]["wrong_user"]["file"]
        result_path.write_text('{"result":"wrong_user","passed":false}\n', encoding="utf-8")
        with self.assertRaisesRegex(PromotionError, "checksum mismatch"):
            create_manifest(self.build, self.certification_path, self.references_path)

    def test_cli_packages_evidence_and_checksums_every_output(self):
        output = self.root / "promotion" / "promotion.json"
        self.assertEqual(
            main([
                "--build-evidence", str(self.build),
                "--test-certification", str(self.certification_path),
                "--registry-references", str(self.references_path),
                "--output", str(output),
            ]),
            0,
        )
        sums = output.parent / "SHA256SUMS"
        entries = {}
        for line in sums.read_text(encoding="ascii").splitlines():
            digest, name = line.split("  ", 1)
            entries[name] = digest
        self.assertEqual(len(entries), 27)
        for name, digest in entries.items():
            self.assertEqual(hashlib.sha256((output.parent / name).read_bytes()).hexdigest(), digest)
        self.assertIn("oidc-gateway-sbom.spdx.json", entries)


if __name__ == "__main__":
    unittest.main()
