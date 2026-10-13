#!/usr/bin/env python3
import pathlib
import re
import unittest


ROOT = pathlib.Path(__file__).resolve().parents[2]
DOCKERFILES = {
    "backend": ROOT / "docker/backend.Dockerfile",
    "agent-server": ROOT / "docker/agent-server.Dockerfile",
    "llm-provider-server": ROOT / "docker/llm-provider-server.Dockerfile",
    "console": ROOT / "docker/console.Dockerfile",
    "oidc-gateway": ROOT / "docker/oidc-gateway.Dockerfile",
}
GO_BASE = "golang:1.27.2-alpine3.23@sha256:9e45f0eb4a63ed37ad6e604950407558b7c738e34e015ae77a5d4ad369cde079"
NODE_BASE = "node:24.21.0-alpine3.23@sha256:9ec4a2e289874ed0d722e1772ec2de45d2801541db8612f3638b26f128c69ac2"
TRIVY_IMAGE = "aquasec/trivy:0.75.0@sha256:af6acf9a6b85dfe389a1941505c0ce9efef52a4719635e1a962f022a3d855daa"


class ProductionImagePolicyTest(unittest.TestCase):
    def test_five_multistage_images_have_pinned_scratch_runtimes(self):
        self.assertEqual(set(DOCKERFILES), {"backend", "agent-server", "llm-provider-server", "console", "oidc-gateway"})
        for name, path in DOCKERFILES.items():
            text = path.read_text(encoding="utf-8")
            stages = re.findall(r"^FROM (.+)$", text, re.MULTILINE)
            self.assertGreaterEqual(len(stages), 2, name)
            self.assertEqual(stages[-1], "scratch", name)
            self.assertIn(GO_BASE, text, name)
            self.assertIn("-tags=timetzdata", text, name)
            if name == "console":
                self.assertIn(NODE_BASE, text)
            for stage in stages[:-1]:
                self.assertRegex(stage, r"@sha256:[0-9a-f]{64}(?: AS .+)?$", name)

    def test_runtime_contract_is_non_root_traceable_and_healthy(self):
        required = (
            "USER 65532:65532",
            "HEALTHCHECK ",
            "org.opencontainers.image.created",
            "org.opencontainers.image.revision",
            "org.opencontainers.image.source",
            "org.opencontainers.image.version",
            'ENTRYPOINT ["/',
        )
        for name, path in DOCKERFILES.items():
            runtime = path.read_text(encoding="utf-8").split("\nFROM scratch\n", 1)[1]
            for value in required:
                self.assertIn(value, runtime, f"{name}: {value}")
            self.assertNotRegex(runtime, r"\b(RUN|ADD)\b", name)
            self.assertNotRegex(runtime, r"\b(apk|apt|curl|wget|busybox|sh)\b", name)
            self.assertNotIn("COPY .", runtime, name)
        for name in ("backend", "agent-server", "llm-provider-server"):
            self.assertIn("/data", DOCKERFILES[name].read_text(encoding="utf-8"), name)

    def test_build_pipeline_enforces_evidence_and_clean_source(self):
        text = (ROOT / "scripts/build-production-images.ps1").read_text(encoding="utf-8")
        for name in DOCKERFILES:
            self.assertIn(f"Name = '{name}'", text)
        for control in (
            "--provenance=mode=max",
            "--sbom=true",
            "--driver docker-container",
            'type=oci,dest=$ociPath',
            TRIVY_IMAGE,
            "--format spdx-json",
            "--severity HIGH,CRITICAL --exit-code 1 --format sarif",
            "trivy_image = $trivyImage",
            "docker export",
            "forbidden runtime files",
            "--read-only",
            "--cap-drop",
            "no-new-privileges:true",
            "State.Health.Status",
            "ca-certificates.crt",
            "same-input rebuild differs",
            "oci_archive_sha256",
            "critical_high_findings = 0",
            "acceptance-summary.json",
            "source_tree",
            "inputs_sha256",
            "SHA256SUMS",
            "--untracked-files=all",
        ):
            self.assertIn(control, text)
        self.assertNotIn("docker scout", text.lower())
        self.assertNotIn("golang:1.27.1", "\n".join(path.read_text(encoding="utf-8") for path in DOCKERFILES.values()))

    def test_gateway_image_smoke_uses_ephemeral_test_file_secrets(self):
        build = (ROOT / "scripts/build-production-images.ps1").read_text(encoding="utf-8")
        gateway = DOCKERFILES["oidc-gateway"].read_text(encoding="utf-8")
        for value in (
            "stock-ai-test_oidc_client_secret",
            "stock-ai-test_oidc_cookie_key",
            "OIDC_ISSUER = 'https://identity.invalid/realms/stock-ai-test'",
            "OIDC_CLIENT_ID = 'stock-ai-test'",
            "--mount",
            "readonly",
            "Remove-Item -Recurse -Force -LiteralPath $runtimeSecretsDirectory",
        ):
            self.assertIn(value, build)
        self.assertIn("/run/secrets", gateway)
        self.assertIn("/issuer-check", gateway)
        self.assertIn("/healthz", gateway)

    def test_context_excludes_secrets_tests_and_local_state(self):
        patterns = (ROOT / ".dockerignore").read_text(encoding="utf-8").splitlines()
        self.assertEqual(patterns[0], "**")
        allowed = "\n".join(line for line in patterns if line.startswith("!"))
        for forbidden in (".env", ".db", ".log", "/test", ".scratch"):
            self.assertNotIn(forbidden, allowed)


if __name__ == "__main__":
    unittest.main()
